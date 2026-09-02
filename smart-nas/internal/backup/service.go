// service.go 备份服务：任务 CRUD、备份执行、还原、生命周期、调度注册。
package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cronv3 "github.com/robfig/cron/v3"

	"smart-nas/pkg/logger"
)

// Service 备份服务
type Service struct {
	repo *repository
	// 运行中的任务（同一时间同一任务只允许一个执行；全局并发=1 由 execMu 保证）
	running   map[int64]struct{}
	runningMu sync.Mutex
	execMu    sync.Mutex

	cron      *cronv3.Cron
	cronMu    sync.Mutex
	cronIDs   map[int64]cronv3.EntryID // taskID -> cron entry
	stopWatch chan struct{}

	usb       *USBWatcher
	usbMu     sync.Mutex
	logPath   string // 独立 backup.log
	logCloser io.Closer
}

// NewService 创建备份服务
func NewService(dbPath, logPath string) (*Service, error) {
	repo, err := NewRepository(dbPath)
	if err != nil {
		return nil, err
	}
	s := &Service{
		repo:      repo,
		running:   make(map[int64]struct{}),
		cron:      cronv3.New(cronv3.WithChain(cronv3.Recover(cronv3.DefaultLogger))),
		cronIDs:   make(map[int64]cronv3.EntryID),
		stopWatch: make(chan struct{}),
		logPath:   logPath,
	}
	if err := s.openLog(); err != nil {
		logger.Warn("backup.log 初始化失败（仅控制台记录）", "error", err)
	}
	return s, nil
}

// openLog 打开独立备份日志（追加写）
func (s *Service) openLog() error {
	if s.logPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.logPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	s.logCloser = f
	return nil
}

// logf 写备份日志（backup.log + 主日志）
func (s *Service) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logger.Info("[backup] " + msg)
	if s.logCloser != nil {
		if f, ok := s.logCloser.(io.Writer); ok {
			_, _ = io.WriteString(f, time.Now().Format("2006-01-02 15:04:05")+" "+msg+"\n")
		}
	}
}

// Close 停止调度与 USB 监控并关闭资源
func (s *Service) Close() {
	s.stop()
	if s.repo != nil {
		_ = s.repo.Close()
	}
	if s.logCloser != nil {
		_ = s.logCloser.Close()
	}
}

// Start 启动 cron 调度、加载任务、启动 USB 监控
func (s *Service) Start() error {
	s.cron.Start()
	if err := s.reloadJobs(); err != nil {
		return err
	}
	// 定时重新加载任务（捕获新增/修改/删除的任务）
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopWatch:
				return
			case <-ticker.C:
				_ = s.reloadJobs()
			}
		}
	}()
	// USB 实时监控
	s.usb = NewUSBWatcher(s.onUSBDevice)
	s.usb.Start()
	return nil
}

// stop 停止所有后台活动
func (s *Service) stop() {
	s.cronMu.Lock()
	s.cron.Stop()
	s.cronMu.Unlock()
	select {
	case <-s.stopWatch:
	default:
		close(s.stopWatch)
	}
	s.usbMu.Lock()
	if s.usb != nil {
		s.usb.Stop()
	}
	s.usbMu.Unlock()
}

// reloadJobs 按任务表重建 cron 调度（timer/interval 模式）
func (s *Service) reloadJobs() error {
	tasks, err := s.repo.ListTasks(nil)
	if err != nil {
		return err
	}
	s.cronMu.Lock()
	defer s.cronMu.Unlock()
	// 全量重建：先清空
	for id, entry := range s.cronIDs {
		s.cron.Remove(entry)
		delete(s.cronIDs, id)
	}
	for _, t := range tasks {
		if !t.Enabled || (t.TriggerMode != TriggerTimer && t.TriggerMode != TriggerInterval) {
			continue
		}
		expr := t.CronExpr
		if t.TriggerMode == TriggerInterval {
			// 间隔小时数内部转 cron：每 N 小时（如 "0 */2 * * *"）
			hours, perr := strconv.Atoi(strings.TrimSpace(t.CronExpr))
			if perr != nil || hours <= 0 {
				continue
			}
			if hours >= 24 {
				expr = fmt.Sprintf("0 0 */%d * *", hours/24)
			} else {
				expr = fmt.Sprintf("0 */%d * * *", hours)
			}
		}
		taskID := t.ID
		entry, err := s.cron.AddFunc(expr, func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("[backup] 定时任务 panic", "task", taskID, "panic", r)
				}
			}()
			s.RunTask(taskID, true)
		})
		if err != nil {
			s.logf("任务 %d cron 注册失败: %v", t.ID, err)
			continue
		}
		s.cronIDs[t.ID] = entry
	}
	return nil
}

// onUSBDevice USB 设备接入回调：绑定了该设备的 realtime 任务自动执行
func (s *Service) onUSBDevice(deviceID string) {
	tasks, err := s.repo.ListTasks(nil)
	if err != nil {
		return
	}
	for _, t := range tasks {
		if !t.Enabled || t.TriggerMode != TriggerRealtime || t.USBDeviceID == "" {
			continue
		}
		// 设备匹配：串号或卷标一致即触发（防止陌生 U 盘误触发）
		if usbMatch(t.USBDeviceID, deviceID) {
			s.logf("USB 设备接入触发任务 %d（%s）", t.ID, t.Name)
			go s.RunTask(t.ID, true)
		}
	}
}

// usbMatch 设备标识匹配（大小写不敏感；支持「串号|卷标」任一命中）
func usbMatch(bound, device string) bool {
	bound = strings.ToLower(strings.TrimSpace(bound))
	device = strings.ToLower(strings.TrimSpace(device))
	if bound == "" || device == "" {
		return false
	}
	if bound == device {
		return true
	}
	for _, part := range strings.Split(bound, "|") {
		part = strings.TrimSpace(part)
		if part != "" && part == device {
			return true
		}
	}
	return false
}

// ---- 任务 CRUD（对外 API）----

// CreateTask 创建任务（simple 周期由调用方转成 cron 后传入）
func (s *Service) CreateTask(t *Task) (*Task, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.CreateTask(t); err != nil {
		return nil, err
	}
	_ = s.reloadJobs()
	s.logf("创建备份任务 %d（%s）", t.ID, t.Name)
	return t, nil
}

// UpdateTask 更新任务
func (s *Service) UpdateTask(t *Task) (*Task, error) {
	old, err := s.repo.GetTask(t.ID)
	if err != nil {
		return nil, err
	}
	t.CreateTime = old.CreateTime
	if err := t.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateTask(t); err != nil {
		return nil, err
	}
	_ = s.reloadJobs()
	s.logf("更新备份任务 %d（%s）", t.ID, t.Name)
	return t, nil
}

// DeleteTask 删除任务（历史保留）
func (s *Service) DeleteTask(id int64) error {
	if _, err := s.repo.GetTask(id); err != nil {
		return err
	}
	if err := s.repo.DeleteTask(id); err != nil {
		return err
	}
	_ = s.reloadJobs()
	s.logf("删除备份任务 %d", id)
	return nil
}

// GetTask / ListTasks 透传
func (s *Service) GetTask(id int64) (*Task, error)     { return s.repo.GetTask(id) }
func (s *Service) GetBackup(id int64) (*History, error) { return s.repo.GetHistory(id) }
func (s *Service) ListTasks() ([]*Task, error)         { return s.repo.ListTasks(nil) }
func (s *Service) ListHistories(taskID int64) ([]*History, error) {
	return s.repo.ListHistories(taskID, 0)
}

// Freeze 冻结 / 解冻备份
func (s *Service) Freeze(historyID int64, frozen bool) (*History, error) {
	h, err := s.repo.GetHistory(historyID)
	if err != nil {
		return nil, err
	}
	h.IsFrozen = frozen
	if err := s.repo.UpdateHistory(h); err != nil {
		return nil, err
	}
	if frozen {
		s.logf("备份 %d 已冻结", historyID)
	} else {
		s.logf("备份 %d 已解除冻结", historyID)
	}
	return h, nil
}

// DeleteBackup 手动删除备份（产物 + 记录；冻结的也允许手动删）
func (s *Service) DeleteBackup(historyID int64) error {
	h, err := s.repo.GetHistory(historyID)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(h.StorePath)
	if err := s.repo.DeleteHistory(historyID); err != nil {
		return err
	}
	s.logf("手动删除备份 %d（%s）", historyID, h.StorePath)
	return nil
}

// RunBackup 手动触发备份（triggerMode=manual 的正常路径，也允许手动触发任意任务）
func (s *Service) RunBackup(taskID int64) (*History, error) {
	return s.RunTask(taskID, false)
}

// RunTask 执行备份任务（force=true 由调度/USB 触发）。
// 同一任务未执行完毕时再次触发，直接跳过本次执行（需求 §2.3.2）。
func (s *Service) RunTask(taskID int64, scheduled bool) (*History, error) {
	t, err := s.repo.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if !t.Enabled && scheduled {
		return nil, fmt.Errorf("任务已禁用")
	}
	s.runningMu.Lock()
	if _, busy := s.running[taskID]; busy {
		s.runningMu.Unlock()
		return nil, fmt.Errorf("任务「%s」正在执行中，本次触发已跳过", t.Name)
	}
	s.running[taskID] = struct{}{}
	s.runningMu.Unlock()
	defer func() {
		s.runningMu.Lock()
		delete(s.running, taskID)
		s.runningMu.Unlock()
	}()

	// 全局串行：同一时间只运行一个备份任务（需求 §3.1）
	s.execMu.Lock()
	defer s.execMu.Unlock()

	return s.execute(t)
}

// execute 执行一次备份（完整 / 增量）
func (s *Service) execute(t *Task) (*History, error) {
	h := &History{TaskID: t.ID, StartTime: time.Now(), Status: StatusFailed}

	// 备份前预检查：源可读 + 目标磁盘剩余空间
	if remark := s.precheck(t); remark != "" {
		h.EndTime = time.Now()
		h.Remark = remark
		_ = s.repo.CreateHistory(h)
		s.logf("任务 %d 预检查失败: %s", t.ID, remark)
		s.notifyResult(t, h)
		return h, fmt.Errorf("%s", remark)
	}

	// 决定备份类型：
	//   - 任务 backup_type=full：每次完全备份（用户显式选择）；
	//   - 默认 auto：首次完整，后续基于父备份链增量；链不可用（如产物被删）退化为完整。
	h.Type = TypeFull
	var parentSnap map[string]fileStamp
	if t.BackupType != BackupTypeFull {
		if latest := s.latestSuccessHistory(t.ID); latest != nil && latest.Type != "" {
			// 增量备份依赖已有备份（需求 §4.2）
			h.Type = TypeIncremental
			h.ParentBackupID = latest.ID
			parentSnap = s.buildChainSnapshot(latest)
			if len(parentSnap) == 0 {
				// 链不可用（如产物被删）：退化为完整备份
				h.Type = TypeFull
				h.ParentBackupID = 0
			}
		}
	}

	// 产物目录：output_dir/backup_<taskID>_<时间戳>（纳秒后缀防同秒多次备份相互覆盖）
	ts := h.StartTime.Format("20060102-150405") + "-" + strconv.FormatInt(h.StartTime.UnixNano()%1000000, 10)
	baseName := fmt.Sprintf("backup_%d_%s", t.ID, ts)
	skip := &skippedFiles{}
	var storePath string
	var total int64
	var execErr error

	// 逐源备份（多源时每个源一个子目录）
	isMulti := len(t.SourcePaths) > 1
	var sources []string
	sources = append(sources, t.SourcePaths...)

	if t.EnableCompress {
		zipPath := filepath.Join(t.OutputDir, baseName+".zip")
		if err := os.MkdirAll(t.OutputDir, 0o755); err != nil {
			return s.failHistory(t, h, "创建输出目录失败: "+err.Error()), err
		}
		// 多源时先汇总到临时目录再压缩
		staging := filepath.Join(os.TempDir(), "smartnas-bak-"+baseName)
		defer os.RemoveAll(staging)
		for i, src := range sources {
			srcAbs := src
			if !filepath.IsAbs(srcAbs) {
				if a, aerr := filepath.Abs(srcAbs); aerr == nil {
					srcAbs = a
				}
			}
			dst := staging
			if isMulti {
				dst = filepath.Join(staging, fmt.Sprintf("source_%d_%s", i+1, filepath.Base(srcAbs)))
			}
			if h.Type == TypeIncremental && parentSnap != nil {
				// 增量：仅拷贝变化文件到 staging
				snap := remapSnapshotForSubdir(parentSnap, src, isMulti, i)
				if _, err := incrementalCopy(srcAbs, dst, t.ExcludePatterns, snap, skip); err != nil {
					execErr = err
				}
			} else {
				if _, err := fullCopy(srcAbs, dst, t.ExcludePatterns, skip); err != nil {
					execErr = err
				}
			}
		}
		n, zerr := zipDir(staging, zipPath, t.CompressLevel, nil, skip)
		if zerr != nil {
			_ = os.Remove(zipPath) // 中途失败：丢弃不完整产物
			return s.failHistory(t, h, "压缩失败: "+zerr.Error()), zerr
		}
		total = n
		storePath = zipPath
	} else {
		storePath = filepath.Join(t.OutputDir, baseName)
		if err := os.MkdirAll(storePath, 0o755); err != nil {
			return s.failHistory(t, h, "创建输出目录失败: "+err.Error()), err
		}
		for i, src := range sources {
			srcAbs := src
			if !filepath.IsAbs(srcAbs) {
				if a, aerr := filepath.Abs(srcAbs); aerr == nil {
					srcAbs = a
				}
			}
			dst := storePath
			if isMulti {
				dst = filepath.Join(storePath, fmt.Sprintf("source_%d_%s", i+1, filepath.Base(srcAbs)))
			}
			if h.Type == TypeIncremental && parentSnap != nil {
				snap := remapSnapshotForSubdir(parentSnap, src, isMulti, i)
				if n, err := incrementalCopy(srcAbs, dst, t.ExcludePatterns, snap, skip); err != nil {
					execErr = err
				} else {
					total += n
				}
			} else {
				if n, err := fullCopy(srcAbs, dst, t.ExcludePatterns, skip); err != nil {
					execErr = err
				} else {
					total += n
				}
			}
		}
		if !skip.isEmpty() || total == 0 {
			// 目录模式产物大小按实际统计
			total = dirSize(storePath)
		}
	}

	h.EndTime = time.Now()
	h.StorePath = storePath
	h.TotalSize = total

	// 校验哈希
	if sum, err := hashDir(storePath, t.EnableCompress); err == nil {
		h.HashSum = sum
	}

	// 状态判定：有跳过 → partial；全部失败 → failed
	if total == 0 && !skip.isEmpty() {
		h.Status = StatusFailed
		h.Remark = "全部源文件被跳过: " + strings.Join(skip.Items, "; ")
	} else if !skip.isEmpty() {
		h.Status = StatusPartial
		h.Remark = "跳过 " + strconv.Itoa(len(skip.Items)) + " 项（被占用/无权限/写入中）: " +
			strings.Join(skip.Items, "; ")
	} else {
		h.Status = StatusSuccess
	}
	if execErr != nil && h.Status == StatusSuccess {
		h.Status = StatusPartial
		h.Remark = "部分源处理异常: " + execErr.Error()
	}

	if err := s.repo.CreateHistory(h); err != nil {
		return h, err
	}
	s.logf("任务 %d（%s）备份完成: 类型=%s 状态=%s 大小=%d 产物=%s",
		t.ID, t.Name, h.Type, h.Status, h.TotalSize, h.StorePath)

	// 生命周期清理 + 邮件通知
	if _, err := s.cleanupAfterBackup(t.ID); err != nil {
		s.logf("任务 %d 清理失败: %v", t.ID, err)
	}
	s.notifyResult(t, h)
	return h, nil
}

// failHistory 记录失败历史
func (s *Service) failHistory(t *Task, h *History, remark string) *History {
	h.EndTime = time.Now()
	h.Status = StatusFailed
	h.Remark = remark
	_ = s.repo.CreateHistory(h)
	s.logf("任务 %d（%s）备份失败: %s", t.ID, t.Name, remark)
	s.notifyResult(t, h)
	return h
}

// precheck 备份前预检查：源目录可读校验 + 备份磁盘剩余空间校验。
// 返回空串表示通过；否则返回失败原因。
func (s *Service) precheck(t *Task) string {
	var need int64
	for _, src := range t.SourcePaths {
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Sprintf("源不可访问: %s（%v）", src, err)
		}
		if info.IsDir() {
			if f, err := os.Open(src); err != nil {
				return fmt.Sprintf("源目录不可读: %s", src)
			} else {
				f.Close()
			}
			need += dirSize(src)
		} else {
			need += info.Size()
		}
	}
	if err := os.MkdirAll(t.OutputDir, 0o755); err != nil {
		return fmt.Sprintf("备份存放目录不可创建: %s", t.OutputDir)
	}
	// 剩余空间校验（Windows：卷空间查询走 util 系统调用；失败则跳过该项检查）
	if free, ok := freeSpaceOf(t.OutputDir); ok && free < need {
		return fmt.Sprintf("备份磁盘剩余空间不足: 需约 %d 字节，仅剩 %d 字节", need, free)
	}
	return ""
}

// latestSuccessHistory 最近一次成功/部分成功的备份（增量链的父）
func (s *Service) latestSuccessHistory(taskID int64) *History {
	list, err := s.repo.ListHistories(taskID, 10)
	if err != nil {
		return nil
	}
	for _, h := range list {
		if (h.Status == StatusSuccess || h.Status == StatusPartial) && h.StorePath != "" {
			return h
		}
	}
	return nil
}

// buildChainSnapshot 沿增量链向上汇总快照（链断裂返回已收集部分）。
// 快照 = 父备份内容 + 各级增量内容（子覆盖父）。
func (s *Service) buildChainSnapshot(h *History) map[string]fileStamp {
	// 链条：当前 → parent → … → full。为避免无限循环限制深度 32
	var chain []*History
	cur := h
	for depth := 0; cur != nil && depth < 32; depth++ {
		chain = append(chain, cur)
		if cur.ParentBackupID == 0 {
			break
		}
		p, err := s.repo.GetHistory(cur.ParentBackupID)
		if err != nil {
			break // 链断裂
		}
		cur = p
	}
	snap := make(map[string]fileStamp)
	// 从链根往回叠加（父先、子后）
	for i := len(chain) - 1; i >= 0; i-- {
		hh := chain[i]
		if _, err := os.Stat(hh.StorePath); err != nil {
			continue
		}
		if strings.HasSuffix(strings.ToLower(hh.StorePath), ".zip") {
			mergeZipSnapshot(hh.StorePath, snap)
		} else {
			mergeDirSnapshot(hh.StorePath, snap)
		}
	}
	return snap
}

// remapSnapshotForSubdir 多源备份时，父快照的相对路径需要映射到 staging 子目录内。
// 单源时快照路径直接可用。
func remapSnapshotForSubdir(snap map[string]fileStamp, src string, isMulti bool, index int) map[string]fileStamp {
	if !isMulti {
		return snap
	}
	base := fmt.Sprintf("source_%d_%s", index+1, filepath.Base(src))
	out := make(map[string]fileStamp, len(snap))
	for rel, stamp := range snap {
		out[filepath.Join(base, rel)] = stamp
	}
	return out
}

// ListBackupsByTask 带冻结排序的历史列表（新→旧）
func (s *Service) ListBackupsByTask(taskID int64) ([]*History, error) {
	list, err := s.repo.ListHistories(taskID, 0)
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].IsFrozen != list[j].IsFrozen {
			return list[i].IsFrozen // 冻结排前面便于查看
		}
		return list[i].ID > list[j].ID
	})
	return list, nil
}
