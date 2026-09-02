// Package backup 文件备份还原模块（v0.20）。
//
// 职责：
//   - 以任务的形式组织备份（backup_task）；
//   - 支持完整备份与增量备份（增量依赖父备份链，链损坏拒绝还原）；
//   - 热备份（不强制锁文件，被占用文件跳过并标记）；
//   - 定时/间隔/实时/手动触发（简单周期内部转 Cron 存储）；
//   - USB 移动设备绑定监控（陌生 U 盘不触发）；
//   - 生命周期管理（数量/大小双阈值自动清理，冻结备份绝不自动删除）；
//   - 还原（原位置/指定位置，覆盖策略可选，还原前后完整性校验）；
//   - 元数据存 SQLite（backup_task / backup_history 两表）；
//   - 独立 backup.log 操作日志 + 邮件通知。
package backup

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// 触发模式
const (
	TriggerManual    = "manual"    // 手动
	TriggerTimer     = "timer"     // 定时（cron）
	TriggerRealtime  = "realtime"  // 实时（USB 接入即执行）
	TriggerInterval  = "interval"  // 间隔（小时数）
)

// 备份类型
const (
	TypeFull        = "full"
	TypeIncremental = "incremental"
)

// Task.BackupType 取值：full（每次完全备份）/ auto（首次完整，后续增量，默认）。
// 需求 §2.4：无完整备份（链）时禁止增量——auto 模式下无父备份自动退化为完整，
// 因此旧任务的 auto 语义兼容；full 模式为用户显式选择"每次完全备份"。
const (
	BackupTypeAuto = "auto" // 首次完整，后续增量
	BackupTypeFull = "full" // 每次完全备份
)

// 备份状态
const (
	StatusSuccess = "success"
	StatusPartial = "partial" // 部分成功（源被占用跳过）
	StatusFailed  = "failed"
)

// 还原冲突策略
const (
	ConflictAsk      = "ask"      // 询问用户（接口返回冲突清单，用户决定）
	ConflictOverwrite = "overwrite" // 直接覆盖
	ConflictSkip     = "skip"     // 跳过
	ConflictRename   = "rename"   // 重命名旧文件
)

// Task 备份任务（对应表 backup_task）
type Task struct {
	ID              int64     `json:"task_id"`
	Name            string    `json:"task_name"`
	SourcePaths     []string  `json:"source_paths"`     // json 数组存储
	ExcludePatterns []string  `json:"exclude_patterns"` // json 数组存储
	OutputDir       string    `json:"output_dir"`       // 备份包存放目录
	EnableCompress  bool      `json:"enable_compress"`
	CompressLevel   int       `json:"compress_level"` // 1-9（0 取全局默认 6）
	BackupType      string    `json:"backup_type"`    // auto（首次完整后续增量，默认）/ full（每次完全）
	TriggerMode     string    `json:"trigger_mode"`   // manual/timer/realtime/interval
	CronExpr        string    `json:"cron_expr"`      // timer 模式的 cron；interval 转换存储
	USBDeviceID     string    `json:"usb_device_id"`  // 绑定的 USB 设备标识（空=不监控）
	MaxBackupCount  int       `json:"max_backup_count"`
	MaxBackupSize   string    `json:"max_backup_size"` // 如 "10GB"、"500MB"
	EnableEmail     bool      `json:"enable_email_notify"`
	SMTPConfig      string    `json:"smtp_config"` // json：host/port/username/password/from/to
	Enabled         bool      `json:"enabled"`
	CreateTime      time.Time `json:"create_time"`
}

// History 一次备份执行记录（对应表 backup_history）
type History struct {
	ID             int64     `json:"backup_id"`
	TaskID         int64     `json:"task_id"`
	Type           string    `json:"backup_type"` // full / incremental
	StartTime      time.Time `json:"start_time"`
	EndTime        time.Time `json:"end_time"`
	StorePath      string    `json:"store_path"` // 备份产物路径（目录或 zip 包）
	TotalSize      int64     `json:"total_size"`
	IsFrozen       bool      `json:"is_frozen"`
	Status         string    `json:"status"` // success/partial/failed
	ParentBackupID int64     `json:"parent_backup_id"`
	HashSum        string    `json:"hash_sum"`
	Remark         string    `json:"remark"` // 错误信息 / 跳过文件列表
}

// Deleted 是否已删除（预留：当前历史仅手动删除）
func (h *History) Deleted() bool { return h == nil || h.ID == 0 }

// Validate 任务静态校验
func (t *Task) Validate() error {
	if t == nil {
		return errors.New("任务为空")
	}
	if t.Name == "" {
		return errors.New("任务名不能为空")
	}
	if len(t.SourcePaths) == 0 {
		return errors.New("至少需要一个备份源")
	}
	if t.OutputDir == "" {
		return errors.New("备份存放目录不能为空")
	}
	if t.TriggerMode == "" {
		t.TriggerMode = TriggerManual
	}
	switch t.TriggerMode {
	case TriggerManual:
	case TriggerTimer:
		if t.CronExpr == "" {
			return errors.New("定时任务需要 cron 表达式")
		}
	case TriggerInterval:
		if t.CronExpr == "" {
			return errors.New("间隔任务需要间隔值")
		}
	case TriggerRealtime:
		if t.USBDeviceID == "" {
			return errors.New("实时任务需要绑定 USB 设备")
		}
	default:
		return errors.New("无效的触发模式: " + t.TriggerMode)
	}
	if t.CompressLevel < 0 || t.CompressLevel > 9 {
		return errors.New("压缩级别应为 1-9")
	}
	switch strings.TrimSpace(t.BackupType) {
	case "":
		t.BackupType = BackupTypeAuto
	case BackupTypeAuto, BackupTypeFull:
	default:
		return errors.New("备份类型无效（应为 auto 或 full）")
	}
	return nil
}

// repository 元数据访问层接口（SQLite 实现）
type repository struct {
	db *sql.DB
}

var ErrNotFound = errors.New("记录不存在")
