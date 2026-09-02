// service.go 备份服务：任务 CRUD、备份执行、还原、生命周期、调度注册。
package backup

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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

// snapshotSuffix 增量快照 sidecar 后缀：与产物同级存放，记录本次备份覆盖的
// 源文件状态（rel → size+mtime），供下次增量备份精确比对，避免重新遍历/解压产物。
const snapshotSuffix = ".snapshot.json"

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

	prog *progTracker // 任务执行实时进度（内存态）

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
		prog:      newProgTracker(),
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
	// 启动补跑（v0.21）：服务离线期间错过的定时任务，启动后自动补执行一次
	go s.catchupMissedSchedules()
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
		expr, ok := cronExprOf(t)
		if !ok {
			continue
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

// cronExprOf 任务 → 标准 5 段 cron 表达式（timer 直接用；interval 小时数转换）
func cronExprOf(t *Task) (string, bool) {
	switch t.TriggerMode {
	case TriggerTimer:
		if strings.TrimSpace(t.CronExpr) == "" {
			return "", false
		}
		return t.CronExpr, true
	case TriggerInterval:
		// 间隔小时数内部转 cron：每 N 小时（如 "0 */2 * * *"）
		hours, perr := strconv.Atoi(strings.TrimSpace(t.CronExpr))
		if perr != nil || hours <= 0 {
			return "", false
		}
		if hours >= 24 {
			return fmt.Sprintf("0 0 */%d * *", hours/24), true
		}
		return fmt.Sprintf("0 */%d * * *", hours), true
	}
	return "", false
}

// nextMissedRun 计算 ref（上次执行/任务创建）之后、now 之前是否存在错过的计划时间。
// 返回最近一次错过的计划时间与是否存在（迭代上限保护，避免极端表达式死循环）。
func nextMissedRun(expr string, ref, now time.Time) (time.Time, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" || !ref.Before(now) {
		return time.Time{}, false
	}
	sched, err := cronv3.ParseStandard(expr)
	if err != nil {
		return time.Time{}, false
	}
	var missed time.Time
	t := ref
	for i := 0; i < 200000; i++ {
		next := sched.Next(t)
		if next.IsZero() || !next.Before(now) {
			break
		}
		missed = next
		t = next
	}
	return missed, !missed.IsZero()
}

// catchupMissedSchedules 启动补跑：服务离线期间错过的定时任务，启动后各补执行一次。
// 以「最近一次执行时间（任何状态）/ 任务创建时间」中较新者为基准，
// 若其后仍有到期的计划时间未执行，则触发（RunTask 自带并发去重）。
func (s *Service) catchupMissedSchedules() {
	tasks, err := s.repo.ListTasks(nil)
	if err != nil {
		return
	}
	now := time.Now()
	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		expr, ok := cronExprOf(t)
		if !ok {
			continue
		}
		ref := t.CreateTime
		if list, lerr := s.repo.ListHistories(t.ID, 1); lerr == nil && len(list) > 0 && list[0].StartTime.After(ref) {
			ref = list[0].StartTime
		}
		missed, ok := nextMissedRun(expr, ref, now)
		if !ok {
			continue
		}
		s.logf("任务 %d（%s）存在错过的定时计划 %s，启动补跑",
			t.ID, t.Name, missed.Format("2006-01-02 15:04"))
		go s.RunTask(t.ID, true)
	}
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
	_ = os.Remove(h.StorePath + snapshotSuffix)
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

// ProgressList 返回各任务最近一次执行的实时进度快照（最新的在前）
func (s *Service) ProgressList() []Progress {
	return s.prog.snapshot()
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

// execute 执行一次备份（完整 / 增量），全程更新实时进度（v0.21）
func (s *Service) execute(t *Task) (*History, error) {
	h := &History{TaskID: t.ID, StartTime: time.Now(), Status: StatusFailed}
	prog := s.prog.start(t.ID, t.Name)
	// 兜底：任何返回路径（预检查失败/压缩失败等）都确保进度收尾
	defer func() {
		if prog.Status == "running" {
			prog.finish(h.Status, h.Remark)
		}
	}()

	// 备份前预检查：源可读 + 目标磁盘剩余空间
	if remark := s.precheck(t); remark != "" {
		prog.logf("预检查失败：%s", remark)
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

	// 产物目录：output_dir/<任务名称>/<任务名称>_<年月日-时分秒>
	// 命名精确到秒（用户要求）；同秒已有产物（极快连续备份）追加序号防覆盖。
	// 任务名称作为独立文件夹，便于按任务归档管理
	ts := h.StartTime.Format("20060102-150405")
	taskDir := filepath.Join(t.OutputDir, sanitizeFileName(t.Name))
	nameBase := fmt.Sprintf("%s_%s", sanitizeFileName(t.Name), ts)
	baseName := nameBase
	for n := 1; ; n++ {
		zp, dp := filepath.Join(taskDir, baseName+".zip"), filepath.Join(taskDir, baseName)
		_, ze := os.Stat(zp)
		_, de := os.Stat(dp)
		if os.IsNotExist(ze) && os.IsNotExist(de) {
			break
		}
		baseName = fmt.Sprintf("%s_%d", nameBase, n)
	}
	skip := &skippedFiles{}
	var storePath string
	var total int64
	var execErr error

	// 逐源备份（产物内按源完整路径布局）
	var sources []string
	sources = append(sources, t.SourcePaths...)

	// 本次备份后的完整源状态快照 = 父快照 ∪ 本次产物内容（子覆盖父），
	// 备份成功后写入 sidecar（<产物>.snapshot.json），下次增量据此精确比对。
	// 快照键统一为「产物相对路径」（单源=源相对路径；多源=source_N_<名称>/前缀），
	// 与目录/zip 合并回退路径的键一致；增量比对时按源剥离前缀（snapshotForSource）。
	newSnap := make(map[string]fileStamp, len(parentSnap))
	for k, v := range parentSnap {
		newSnap[k] = v
	}
	// collect 将本源已实际写入产物的文件指纹并入 newSnap（rel 相对该源的 dst 根，
	// 多源时加上 source_N 前缀构成产物相对路径）。文件 mtime 已由 copyFileHot
	// 保留为源 mtime，可精确比对。
	collect := func(root, prefix string) {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return nil
			}
			if prefix != "" {
				rel = filepath.Join(prefix, rel)
			}
			if fi, ierr := d.Info(); ierr == nil {
				newSnap[filepath.ToSlash(rel)] = fileStamp{Size: fi.Size(), ModNano: fi.ModTime().UnixNano()}
			}
			return nil
		})
	}

	// 预扫描：估算待备份总量（完整=全部文件；增量=与父快照比对后的变化文件）
	prog.setPhase(PhasePreScan)
	estB, estF := estimateBackup(t, sources, parentSnap, h.Type == TypeIncremental)
	prog.TotalBytes, prog.FilesTotal = estB, estF
	prog.logf("开始执行（类型：%s），待备份 %d 个文件，约 %s", typeLabel(h.Type), estF, humanBytes(estB))
	prog.setPhase(PhaseCopying)

	// 进度回调：拷贝/压缩每完成一个文件触发一次
	onProg := func(file string, n int64) {
		prog.CurrentFile = file
		prog.CopiedBytes += n
		prog.FilesDone++
		prog.logf("已复制 %s（%s）", file, humanBytes(n))
		prog.recompute(t.EnableCompress)
	}
	// srcOn 将压缩模式下 staging 临时目录内的路径换算回源路径展示，
	// 避免进度日志出现系统临时目录（如 Z:\TEMP\smartnas-bak-*）让用户困惑
	srcOn := func(srcRoot, dstRoot string) fileProg {
		return func(file string, n int64) {
			if rel, rerr := filepath.Rel(dstRoot, file); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				file = filepath.Join(srcRoot, rel)
			}
			onProg(file, n)
		}
	}
	// srcPath 同上，用于跳过条目中路径的换算
	srcPath := func(srcRoot, dstRoot string) func(string) string {
		return func(p string) string {
			if rel, rerr := filepath.Rel(dstRoot, p); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				return filepath.Join(srcRoot, rel)
			}
			return p
		}
	}
	// 执行一次拷贝并记录新产生的跳过项（mapPath 可将条目中的临时路径换算回源路径）
	runCopy := func(fn func() (int64, error), mapPath func(string) string) {
		before := len(skip.Items)
		if _, err := fn(); err != nil {
			execErr = err
		}
		for _, it := range skip.Items[before:] {
			if mapPath != nil {
				if idx := strings.Index(it, ": "); idx > 0 {
					it = mapPath(it[:idx]) + it[idx:]
				}
			}
			prog.Skipped++
			prog.logf("跳过：%s", it)
		}
	}

	if t.EnableCompress {
		zipPath := filepath.Join(taskDir, baseName+".zip")
		if err := os.MkdirAll(taskDir, 0o755); err != nil {
			return s.failHistory(t, h, "创建输出目录失败: "+err.Error()), err
		}
		// 多源时先汇总到临时目录再压缩
		staging := filepath.Join(os.TempDir(), "smartnas-bak-"+baseName)
		defer os.RemoveAll(staging)
		for _, src := range sources {
			srcAbs := src
			if !filepath.IsAbs(srcAbs) {
				if a, aerr := filepath.Abs(srcAbs); aerr == nil {
					srcAbs = a
				}
			}
			// 产物内布局完全按源路径生成（从盘符开始，如 S:\Code\AI → S/Code/AI）
			pfx := layoutRel(srcAbs)
			dst := filepath.Join(staging, filepath.FromSlash(pfx))
			if h.Type == TypeIncremental && parentSnap != nil {
				// 增量：仅拷贝变化文件到 staging
				snap := snapshotForSource(parentSnap, srcAbs)
				runCopy(func() (int64, error) {
					return incrementalCopy(srcAbs, dst, t.ExcludePatterns, snap, skip, srcOn(srcAbs, dst))
				}, srcPath(srcAbs, dst))
			} else {
				runCopy(func() (int64, error) {
					return fullCopy(srcAbs, dst, t.ExcludePatterns, skip, srcOn(srcAbs, dst))
				}, srcPath(srcAbs, dst))
			}
			collect(dst, pfx)
		}
		// 压缩阶段（进度占比 60-95%）
		prog.setPhase(PhaseZipping)
		prog.CopiedBytes, prog.FilesDone, prog.CurrentFile = 0, 0, ""
		prog.logf("开始压缩（级别 %d）…", t.CompressLevel)
		n, zerr := zipDir(staging, zipPath, t.CompressLevel, nil, skip, onProg)
		if zerr != nil {
			_ = os.Remove(zipPath) // 中途失败：丢弃不完整产物
			return s.failHistory(t, h, "压缩失败: "+zerr.Error()), zerr
		}
		total = n
		storePath = zipPath
		_ = saveSnapshot(zipPath+snapshotSuffix, newSnap)
	} else {
		storePath = filepath.Join(taskDir, baseName)
		if err := os.MkdirAll(storePath, 0o755); err != nil {
			return s.failHistory(t, h, "创建输出目录失败: "+err.Error()), err
		}
		for _, src := range sources {
			srcAbs := src
			if !filepath.IsAbs(srcAbs) {
				if a, aerr := filepath.Abs(srcAbs); aerr == nil {
					srcAbs = a
				}
			}
			// 产物内布局完全按源路径生成（与 zip 模式一致）
			pfx := layoutRel(srcAbs)
			dst := filepath.Join(storePath, filepath.FromSlash(pfx))
			if h.Type == TypeIncremental && parentSnap != nil {
				snap := snapshotForSource(parentSnap, srcAbs)
				runCopy(func() (int64, error) {
					n, err := incrementalCopy(srcAbs, dst, t.ExcludePatterns, snap, skip, onProg)
					total += n
					return n, err
				}, nil)
			} else {
				runCopy(func() (int64, error) {
					n, err := fullCopy(srcAbs, dst, t.ExcludePatterns, skip, onProg)
					total += n
					return n, err
				}, nil)
			}
			collect(dst, pfx)
		}
		if !skip.isEmpty() || total == 0 {
			// 目录模式产物大小按实际统计
			total = dirSize(storePath)
		}
		_ = saveSnapshot(storePath+snapshotSuffix, newSnap)
	}

	h.EndTime = time.Now()
	h.StorePath = storePath
	h.TotalSize = total

	// 校验哈希（目录模式进度占比 90-100%，zip 模式 95-100%）
	prog.setPhase(PhaseHashing)
	prog.recompute(t.EnableCompress)
	prog.logf("校验完整性（SHA-256）…")
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
	prog.logf("备份完成：状态=%s 大小=%s 耗时=%s",
		statusLabel(h.Status), humanBytes(h.TotalSize), h.EndTime.Sub(h.StartTime).Round(time.Second))
	prog.finish(h.Status, h.Remark)

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

// saveSnapshot 将备份后的源状态快照写入产物旁的 sidecar 文件（JSON）。
// 供下次增量备份精确比对（mtime 纳秒级），避免重新遍历/解压产物。
func saveSnapshot(path string, snap map[string]fileStamp) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// loadSnapshot 读取 sidecar 快照；不存在或损坏返回 nil（调用方回退为遍历产物）
func loadSnapshot(path string) map[string]fileStamp {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var snap map[string]fileStamp
	if json.Unmarshal(data, &snap) != nil {
		return nil
	}
	return snap
}

// buildChainSnapshot 沿增量链向上汇总快照（链断裂返回已收集部分）。
// 快照 = 父备份内容 + 各级增量内容（子覆盖父）。
// 优先读取各备份的 sidecar 快照（v0.21.5，键为产物相对路径、mtime 纳秒级精确）；
// 旧备份无 sidecar 时回退为遍历产物（zip 条目无精确 mtime，多源键带前缀仅影响比对精度，
// 首次增量会多拷贝一次并自愈生成 sidecar）。
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
		if sc := loadSnapshot(hh.StorePath + snapshotSuffix); sc != nil {
			for k, v := range sc {
				snap[k] = v
			}
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

// layoutRel 源绝对路径 → 产物内布局路径（从盘符开始，如 S:\Code\AI → S/Code/AI）。
// 盘符冒号去除；UNC 路径去掉前导反斜杠（\\srv\share\dir → srv/share/dir）。
func layoutRel(srcAbs string) string {
	p := filepath.Clean(srcAbs)
	vol := filepath.VolumeName(p)
	rest := strings.Trim(strings.ReplaceAll(strings.TrimPrefix(p, vol), "\\", "/"), "/")
	vol = strings.TrimSuffix(vol, ":")
	vol = strings.Trim(strings.ReplaceAll(vol, "\\", "/"), "/")
	switch {
	case vol == "":
		return rest
	case rest == "":
		return vol
	default:
		return vol + "/" + rest
	}
}

// snapshotForSource 将产物布局路径的快照键换算回源相对路径（增量比对按源查找）。
// 快照键 = layoutRel(源)/源相对路径，此处按源剥离前缀。
func snapshotForSource(snap map[string]fileStamp, srcAbs string) map[string]fileStamp {
	base := layoutRel(srcAbs) + "/"
	out := make(map[string]fileStamp, len(snap))
	for k, v := range snap {
		if strings.HasPrefix(k, base) {
			out[strings.TrimPrefix(k, base)] = v
		}
	}
	return out
}

// sanitizeFileName 任务名称转安全的文件夹名：替换 Windows 非法字符，
// 去除首尾空白与点号（Windows 目录名不允许以点/空格结尾）。
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.NewReplacer(
		"\\", "_", "/", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
	).Replace(name)
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "未命名任务"
	}
	return name
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
