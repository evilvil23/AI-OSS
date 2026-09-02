// progress.go 备份任务执行进度跟踪（v0.21）。
//
// 每次备份执行时创建一份进度记录：预扫描总量 → 拷贝/压缩/校验各阶段推进百分比，
// 并维护最近 200 条执行日志供前端"任务进度"面板实时展示。
// 进度仅存于内存（任务级覆盖：同一任务新一次执行会替换上一次的记录）。
package backup

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 进度阶段
const (
	PhasePreScan  = "prescan"  // 预扫描统计总量
	PhaseCopying  = "copying"  // 拷贝中
	PhaseZipping  = "zipping"  // 压缩中
	PhaseHashing  = "hashing"  // 校验哈希
	PhaseFinished = "finished" // 已结束
)

// Progress 一次备份执行的实时进度
type Progress struct {
	TaskID      int64     `json:"task_id"`
	TaskName    string    `json:"task_name"`
	Status      string    `json:"status"`        // running / success / partial / failed
	Phase       string    `json:"phase"`         // prescan/copying/zipping/hashing/finished
	Percent     float64   `json:"percent"`       // 0-100（-1 表示无法估算）
	CurrentFile string    `json:"current_file"`  // 当前正在处理的文件
	CopiedBytes int64     `json:"copied_bytes"`  // 已处理字节数
	TotalBytes  int64     `json:"total_bytes"`   // 预扫描估算的待处理总字节数（0=未知）
	FilesDone   int64     `json:"files_done"`    // 已处理文件数
	FilesTotal  int64     `json:"files_total"`   // 预扫描估算的文件总数（0=未知）
	Skipped     int       `json:"skipped"`       // 已跳过文件数
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
	Remark      string    `json:"remark,omitempty"` // 结束时的备注（失败原因等）
	Log         []string  `json:"log"`              // 最近执行日志（最多 200 条）
}

// logLimit 进度日志环形缓冲上限
const logLimit = 200

// retainDuration 任务结束后进度记录保留时长（供前端收尾展示）
const retainDuration = 10 * time.Minute

// progTracker 进度注册表（taskID → 最新一次执行的进度）
type progTracker struct {
	mu    sync.Mutex
	progs map[int64]*Progress
}

func newProgTracker() *progTracker { return &progTracker{progs: make(map[int64]*Progress)} }

// start 开始一次执行（覆盖同任务旧记录；清理过期记录）
func (pt *progTracker) start(taskID int64, name string) *Progress {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	now := time.Now()
	for id, p := range pt.progs {
		if !p.EndedAt.IsZero() && now.Sub(p.EndedAt) > retainDuration {
			delete(pt.progs, id)
		}
	}
	p := &Progress{
		TaskID:    taskID,
		TaskName:  name,
		Status:    "running",
		Phase:     PhasePreScan,
		Percent:   0,
		StartedAt: now,
		Log:       make([]string, 0, 16),
	}
	pt.progs[taskID] = p
	return p
}

// get 读取指定任务的进度（不存在返回 nil）
func (pt *progTracker) get(taskID int64) *Progress {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return pt.progs[taskID]
}

// snapshot 返回所有进度记录的副本（按开始时间倒序，最新的在前）
func (pt *progTracker) snapshot() []Progress {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	out := make([]Progress, 0, len(pt.progs))
	for _, p := range pt.progs {
		cp := *p
		cp.Log = append([]string(nil), p.Log...)
		out = append(out, cp)
	}
	// 最新在前
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].StartedAt.After(out[i].StartedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// logf 追加一条日志（超出上限丢弃最旧的）
func (p *Progress) logf(format string, args ...interface{}) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	p.Log = append(p.Log, line)
	if len(p.Log) > logLimit {
		p.Log = p.Log[len(p.Log)-logLimit:]
	}
}

// setPhase 切换阶段并记录日志
func (p *Progress) setPhase(phase string) {
	p.Phase = phase
}

// recompute 依据已处理字节与阶段重算百分比。
// compress 模式下拷贝占 0-60%、压缩占 60-95%、哈希占 95-100%；
// 目录模式拷贝占 0-90%、哈希占 90-100%。
func (p *Progress) recompute(compress bool) {
	if p.TotalBytes <= 0 {
		p.Percent = -1 // 无法估算
		return
	}
	switch p.Phase {
	case PhaseCopying:
		span := 90.0
		base := 0.0
		if compress {
			span, base = 60.0, 0.0
		}
		p.Percent = clampPct(base + span*float64(p.CopiedBytes)/float64(p.TotalBytes))
	case PhaseZipping:
		p.Percent = clampPct(60 + 35*float64(p.CopiedBytes)/float64(p.TotalBytes))
	case PhaseHashing:
		base := 90.0
		if compress {
			base = 95.0
		}
		p.Percent = clampPct(base)
	default:
		p.Percent = clampPct(100)
	}
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// finish 结束一次执行
func (p *Progress) finish(status, remark string) {
	p.Status = status
	p.Phase = PhaseFinished
	p.Percent = 100
	p.EndedAt = time.Now()
	p.Remark = remark
}

// ---------- 估算与展示辅助 ----------

// estimateBackup 预扫描估算待备份总量。
// incremental=true 且快照非空时，仅统计与快照相比发生变化（或新增）的文件。
func estimateBackup(t *Task, sources []string, parentSnap map[string]fileStamp, incremental bool) (int64, int64) {
	var bytes, files int64
	for _, src := range sources {
		srcAbs := src
		if !filepath.IsAbs(srcAbs) {
			if a, aerr := filepath.Abs(srcAbs); aerr == nil {
				srcAbs = a
			}
		}
		var snap map[string]fileStamp
		if incremental && parentSnap != nil {
			snap = snapshotForSource(parentSnap, srcAbs)
		}
		b, f := estimateSource(srcAbs, t.ExcludePatterns, snap)
		bytes += b
		files += f
	}
	return bytes, files
}

// estimateSource 估算单个源目录的待备份字节/文件数（snap==nil 表示完整统计）
func estimateSource(srcAbs string, excludes []string, snap map[string]fileStamp) (int64, int64) {
	info, err := os.Stat(srcAbs)
	if err != nil {
		return 0, 0
	}
	if !info.IsDir() {
		if excluded(filepath.Base(srcAbs), excludes) {
			return 0, 0
		}
		if snap != nil {
			return 0, 0 // 增量不支持单文件源（与 execute 行为一致）
		}
		return info.Size(), 1
	}
	var bytes, files int64
	_ = filepath.WalkDir(srcAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			rel, rerr := filepath.Rel(srcAbs, p)
			if rerr == nil && rel != "." && excluded(rel, excludes) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(srcAbs, p)
		if rerr != nil || excluded(rel, excludes) {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if snap != nil {
			stamp := fileStamp{Size: fi.Size(), ModNano: fi.ModTime().UnixNano()}
			// 快照键为「/」分隔，查找前转换分隔符（同 incrementalCopy）
			if parent, ok := snap[filepath.ToSlash(rel)]; ok && sameStamp(parent, stamp) {
				return nil // 未变化
			}
		}
		bytes += fi.Size()
		files++
		return nil
	})
	return bytes, files
}

// humanBytes 字节数人性化展示（如 1.5GB / 300KB）
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// typeLabel 备份类型中文标签
func typeLabel(t string) string {
	if t == TypeIncremental {
		return "增量"
	}
	return "完整"
}

// statusLabel 执行状态中文标签
func statusLabel(s string) string {
	switch s {
	case StatusSuccess:
		return "成功"
	case StatusPartial:
		return "部分成功"
	case StatusFailed:
		return "失败"
	}
	return s
}
