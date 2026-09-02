package backup

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"smart-nas/pkg/logger"
)

// schema 备份元数据表（与需求文档 §2.1 SQL 一致，时间以 Unix 纳秒整数存储）
const schema = `
CREATE TABLE IF NOT EXISTS backup_task (
	task_id           INTEGER PRIMARY KEY AUTOINCREMENT,
	task_name         TEXT    NOT NULL,
	source_paths      TEXT    NOT NULL DEFAULT '[]',
	exclude_patterns  TEXT    NOT NULL DEFAULT '[]',
	output_dir        TEXT    NOT NULL DEFAULT '',
	enable_compress   INTEGER NOT NULL DEFAULT 0,
	compress_level    INTEGER NOT NULL DEFAULT 0,
	backup_type       TEXT    NOT NULL DEFAULT 'auto',
	trigger_mode      TEXT    NOT NULL DEFAULT 'manual',
	cron_expr         TEXT    NOT NULL DEFAULT '',
	usb_device_id     TEXT    NOT NULL DEFAULT '',
	max_backup_count  INTEGER NOT NULL DEFAULT 0,
	max_backup_size   TEXT    NOT NULL DEFAULT '',
	enable_email_notify INTEGER NOT NULL DEFAULT 0,
	smtp_config       TEXT    NOT NULL DEFAULT '',
	enabled           INTEGER NOT NULL DEFAULT 1,
	create_time       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_bt_enabled ON backup_task(enabled);

CREATE TABLE IF NOT EXISTS backup_history (
	backup_id       INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id         INTEGER NOT NULL DEFAULT 0,
	backup_type     TEXT    NOT NULL DEFAULT 'full',
	start_time      INTEGER NOT NULL DEFAULT 0,
	end_time        INTEGER NOT NULL DEFAULT 0,
	store_path      TEXT    NOT NULL DEFAULT '',
	total_size      INTEGER NOT NULL DEFAULT 0,
	is_frozen       INTEGER NOT NULL DEFAULT 0,
	status          TEXT    NOT NULL DEFAULT 'success',
	parent_backup_id INTEGER NOT NULL DEFAULT 0,
	hash_sum        TEXT    NOT NULL DEFAULT '',
	remark          TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_task_id  ON backup_history(task_id);
CREATE INDEX IF NOT EXISTS idx_frozen   ON backup_history(is_frozen);
CREATE INDEX IF NOT EXISTS idx_bh_start ON backup_history(start_time);
`

// NewRepository 打开（或创建）备份元数据库
func NewRepository(dbPath string) (*repository, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("备份数据库路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(dbPath) +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化备份数据库失败: %w", err)
	}
	// 旧库升级：v0.20 初版无 backup_type 列，存在则补齐（SQLite 无 IF NOT EXISTS 语法）
	if _, err := db.Exec(`ALTER TABLE backup_task ADD COLUMN backup_type TEXT NOT NULL DEFAULT 'auto'`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			_ = db.Close()
			return nil, fmt.Errorf("升级备份任务表失败: %w", err)
		}
	}
	return &repository{db: db}, nil
}

// Close 关闭数据库（WAL 安全落盘）
func (r *repository) Close() error { return r.db.Close() }

// ---- 时间与 JSON 数组辅助 ----

func nanos(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixNano()
}

func timeOf(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(0, v.Int64).Local()
}

func marshalList(list []string) string {
	b, err := json.Marshal(list)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func unmarshalList(s string) []string {
	var out []string
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// ---- 任务 ----

const taskCols = `task_id, task_name, source_paths, exclude_patterns, output_dir,
	enable_compress, compress_level, backup_type, trigger_mode, cron_expr, usb_device_id,
	max_backup_count, max_backup_size, enable_email_notify, smtp_config, enabled, create_time`

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	var createTime sql.NullInt64
	var sourcePaths, excludePatterns string
	if err := row.Scan(&t.ID, &t.Name, &sourcePaths, &excludePatterns, &t.OutputDir,
		&t.EnableCompress, &t.CompressLevel, &t.BackupType, &t.TriggerMode, &t.CronExpr, &t.USBDeviceID,
		&t.MaxBackupCount, &t.MaxBackupSize, &t.EnableEmail, &t.SMTPConfig, &t.Enabled, &createTime); err != nil {
		return nil, err
	}
	t.SourcePaths = unmarshalList(sourcePaths)
	t.ExcludePatterns = unmarshalList(excludePatterns)
	if t.BackupType == "" {
		t.BackupType = BackupTypeAuto
	}
	t.CreateTime = timeOf(createTime)
	return &t, nil
}

// CreateTask 创建任务
func (r *repository) CreateTask(t *Task) error {
	t.CreateTime = time.Now()
	if t.BackupType == "" {
		t.BackupType = BackupTypeAuto
	}
	res, err := r.db.Exec(`INSERT INTO backup_task (task_name, source_paths, exclude_patterns,
		output_dir, enable_compress, compress_level, backup_type, trigger_mode, cron_expr, usb_device_id,
		max_backup_count, max_backup_size, enable_email_notify, smtp_config, enabled, create_time)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Name, marshalList(t.SourcePaths), marshalList(t.ExcludePatterns), t.OutputDir,
		t.EnableCompress, t.CompressLevel, t.BackupType, t.TriggerMode, t.CronExpr, t.USBDeviceID,
		t.MaxBackupCount, t.MaxBackupSize, t.EnableEmail, t.SMTPConfig, t.Enabled, nanos(t.CreateTime))
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	t.ID = id
	return nil
}

// UpdateTask 更新任务（保持 ID 与创建时间不变）
func (r *repository) UpdateTask(t *Task) error {
	if t.BackupType == "" {
		t.BackupType = BackupTypeAuto
	}
	res, err := r.db.Exec(`UPDATE backup_task SET task_name=?, source_paths=?, exclude_patterns=?,
		output_dir=?, enable_compress=?, compress_level=?, backup_type=?, trigger_mode=?, cron_expr=?, usb_device_id=?,
		max_backup_count=?, max_backup_size=?, enable_email_notify=?, smtp_config=?, enabled=?
		WHERE task_id=?`,
		t.Name, marshalList(t.SourcePaths), marshalList(t.ExcludePatterns), t.OutputDir,
		t.EnableCompress, t.CompressLevel, t.BackupType, t.TriggerMode, t.CronExpr, t.USBDeviceID,
		t.MaxBackupCount, t.MaxBackupSize, t.EnableEmail, t.SMTPConfig, t.Enabled, t.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetTask 按 ID 查询任务
func (r *repository) GetTask(id int64) (*Task, error) {
	row := r.db.QueryRow(`SELECT `+taskCols+` FROM backup_task WHERE task_id=?`, id)
	t, err := scanTask(row)
	if err != nil {
		return nil, ErrNotFound
	}
	return t, nil
}

// ListTasks 任务列表（按 ID 排序；enabled 为 nil 时返回全部）
func (r *repository) ListTasks(enabled *bool) ([]*Task, error) {
	q := `SELECT ` + taskCols + ` FROM backup_task`
	var args []any
	if enabled != nil {
		q += ` WHERE enabled=?`
		args = append(args, *enabled)
	}
	q += ` ORDER BY task_id`
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// DeleteTask 删除任务（不删除历史记录）
func (r *repository) DeleteTask(id int64) error {
	_, err := r.db.Exec(`DELETE FROM backup_task WHERE task_id=?`, id)
	return err
}

// ---- 历史 ----

const histCols = `backup_id, task_id, backup_type, start_time, end_time, store_path,
	total_size, is_frozen, status, parent_backup_id, hash_sum, remark`

func scanHist(row interface{ Scan(...any) error }) (*History, error) {
	var h History
	var start, end sql.NullInt64
	if err := row.Scan(&h.ID, &h.TaskID, &h.Type, &start, &end, &h.StorePath,
		&h.TotalSize, &h.IsFrozen, &h.Status, &h.ParentBackupID, &h.HashSum, &h.Remark); err != nil {
		return nil, err
	}
	h.StartTime = timeOf(start)
	h.EndTime = timeOf(end)
	return &h, nil
}

// CreateHistory 记录一次备份执行（ID 自增）
func (r *repository) CreateHistory(h *History) error {
	res, err := r.db.Exec(`INSERT INTO backup_history (task_id, backup_type, start_time, end_time,
		store_path, total_size, is_frozen, status, parent_backup_id, hash_sum, remark)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		h.TaskID, h.Type, nanos(h.StartTime), nanos(h.EndTime), h.StorePath,
		h.TotalSize, h.IsFrozen, h.Status, h.ParentBackupID, h.HashSum, h.Remark)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	h.ID = id
	return nil
}

// UpdateHistory 更新历史记录（冻结标记 / 校验哈希 / 备注 / 状态）
func (r *repository) UpdateHistory(h *History) error {
	res, err := r.db.Exec(`UPDATE backup_history SET backup_type=?, start_time=?, end_time=?,
		store_path=?, total_size=?, is_frozen=?, status=?, parent_backup_id=?, hash_sum=?, remark=?
		WHERE backup_id=?`,
		h.Type, nanos(h.StartTime), nanos(h.EndTime), h.StorePath,
		h.TotalSize, h.IsFrozen, h.Status, h.ParentBackupID, h.HashSum, h.Remark, h.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetHistory 按 ID 查询
func (r *repository) GetHistory(id int64) (*History, error) {
	row := r.db.QueryRow(`SELECT `+histCols+` FROM backup_history WHERE backup_id=?`, id)
	h, err := scanHist(row)
	if err != nil {
		return nil, ErrNotFound
	}
	return h, nil
}

// ListHistories 任务的历史（新→旧；limit<=0 返回全部）
func (r *repository) ListHistories(taskID int64, limit int) ([]*History, error) {
	q := `SELECT ` + histCols + ` FROM backup_history WHERE task_id=? ORDER BY backup_id DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	return r.queryHist(q, taskID)
}

// FrozenCount 任务冻结备份数量
func (r *repository) FrozenCount(taskID int64) (int, error) {
	var n int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM backup_history WHERE task_id=? AND is_frozen=1`, taskID).Scan(&n)
	return n, err
}

// FrozenSize 任务冻结备份总字节数
func (r *repository) FrozenSize(taskID int64) (int64, error) {
	var total sql.NullInt64
	err := r.db.QueryRow(`SELECT SUM(total_size) FROM backup_history WHERE task_id=? AND is_frozen=1`, taskID).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// DeleteHistory 删除历史记录（调用方负责物理删除产物）
func (r *repository) DeleteHistory(id int64) error {
	_, err := r.db.Exec(`DELETE FROM backup_history WHERE backup_id=?`, id)
	return err
}

func (r *repository) queryHist(q string, args ...any) ([]*History, error) {
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*History
	for rows.Next() {
		h, err := scanHist(rows)
		if err != nil {
			continue
		}
		out = append(out, h)
	}
	return out, nil
}

// closeLogged 供测试使用的关闭包装（记录错误不吞异常）
func (r *repository) closeLogged() {
	if err := r.Close(); err != nil {
		logger.Warn("关闭备份数据库失败", "error", err)
	}
}
