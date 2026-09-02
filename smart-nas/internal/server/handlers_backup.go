// handlers_backup.go 备份还原 API（v0.20）。
//
// 读接口（任务/历史/内容浏览）：所有登录用户；
// 写接口（创建/更新/删除任务、触发备份、冻结、删除备份、还原）：仅主人/管理员。
package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/api/types"
	"smart-nas/internal/backup"
	"smart-nas/pkg/logger"
)

// registerBackupRoutes 注册备份路由
func (s *Server) registerBackupRoutes(authed *gin.RouterGroup) {
	r := authed.Group("/backup")
	r.GET("/tasks", s.backupListTasks)
	r.GET("/defaults", s.backupDefaults)
	r.GET("/tasks/:id/history", s.backupListHistory)
	r.GET("/backups/:id/contents", s.backupContents)
	r.GET("/usb-devices", s.backupUSBDevices)

	// 以下写操作要求主人/管理员
	admin := r.Group("")
	admin.Use(RequireAdmin())
	admin.POST("/tasks", s.backupCreateTask)
	admin.PUT("/tasks/:id", s.backupUpdateTask)
	admin.DELETE("/tasks/:id", s.backupDeleteTask)
	admin.POST("/tasks/:id/run", s.backupRunTask)
	admin.POST("/backups/:id/restore", s.backupRestore)
	admin.POST("/backups/:id/freeze", s.backupFreeze)
	admin.DELETE("/backups/:id", s.backupDelete)
}

// backupDefaults GET /api/backup/defaults （全局默认：存放目录/压缩级别，新建任务表单预填）
func (s *Server) backupDefaults(c *gin.Context) {
	out := map[string]interface{}{"output_dir": "", "compress_level": 6}
	if s.deps.Settings != nil {
		st := s.deps.Settings.Get()
		if st.BackupOutputDir != "" {
			out["output_dir"] = st.BackupOutputDir
		}
		out["compress_level"] = st.BackupCompressLevel
	}
	c.JSON(http.StatusOK, types.OK(out))
}

// backupListTasks GET /api/backup/tasks
func (s *Server) backupListTasks(c *gin.Context) {
	tasks, err := s.deps.Backup.ListTasks()
	if err != nil {
		errJSON(c, err)
		return
	}
	if tasks == nil {
		tasks = []*backup.Task{} // 空列表编码为 []（而非 null），避免前端读 length 报错
	}
	c.JSON(http.StatusOK, types.OK(tasks))
}

type backupTaskRequest struct {
	Name            string   `json:"task_name"`
	SourcePaths     []string `json:"source_paths"`
	ExcludePatterns []string `json:"exclude_patterns"`
	OutputDir       string   `json:"output_dir"`
	EnableCompress  bool     `json:"enable_compress"`
	CompressLevel   int      `json:"compress_level"` // 0 = 使用全局默认（设置页）
	BackupType      string   `json:"backup_type"`    // auto（首次完整后续增量，默认）/ full（每次完全）
	TriggerMode     string   `json:"trigger_mode"`
	// 定时：直接给 cron_expr，或给 simple_period（内部转 cron 存储）
	CronExpr     string             `json:"cron_expr"`
	SimplePeriod *backup.SimplePeriod `json:"simple_period"`
	USBDeviceID  string             `json:"usb_device_id"`
	MaxBackupCount int              `json:"max_backup_count"`
	MaxBackupSize  string           `json:"max_backup_size"`
	EnableEmail    bool             `json:"enable_email_notify"`
	SMTPConfig     string           `json:"smtp_config"`
	Enabled        *bool            `json:"enabled"`
}

// buildTask 从请求构造 Task（简单周期转 cron；applyDefaults 用于新建任务时套用全局默认）
func (s *Server) buildTask(req *backupTaskRequest, existing *backup.Task, applyDefaults bool) (*backup.Task, error) {
	t := &backup.Task{}
	if existing != nil {
		*t = *existing
	}
	t.Name = req.Name
	t.SourcePaths = req.SourcePaths
	t.ExcludePatterns = req.ExcludePatterns
	t.OutputDir = req.OutputDir
	t.EnableCompress = req.EnableCompress
	t.CompressLevel = req.CompressLevel
	t.BackupType = req.BackupType
	t.TriggerMode = req.TriggerMode
	t.CronExpr = req.CronExpr
	t.USBDeviceID = req.USBDeviceID
	t.MaxBackupCount = req.MaxBackupCount
	t.MaxBackupSize = req.MaxBackupSize
	t.EnableEmail = req.EnableEmail
	t.SMTPConfig = req.SMTPConfig
	t.Enabled = true
	if req.Enabled != nil {
		t.Enabled = *req.Enabled
	} else if existing != nil && req.Enabled == nil {
		t.Enabled = existing.Enabled
	}
	// 新建任务：存放目录/压缩级别留空时套用全局默认（设置页可配，压缩默认 9 无损最高）
	if applyDefaults {
		if t.OutputDir == "" && s.deps.Settings != nil {
			t.OutputDir = s.deps.Settings.Get().BackupOutputDir
		}
		if t.EnableCompress && t.CompressLevel <= 0 && s.deps.Settings != nil {
			t.CompressLevel = s.deps.Settings.Get().BackupCompressLevel
		}
	}
	// 简单周期转 cron（cron_expr 为空时生效）
	if req.SimplePeriod != nil && t.CronExpr == "" {
		expr, err := req.SimplePeriod.ToCron()
		if err != nil {
			return nil, err
		}
		t.CronExpr = expr
	}
	return t, nil
}

// backupCreateTask POST /api/backup/tasks
func (s *Server) backupCreateTask(c *gin.Context) {
	var req backupTaskRequest
	if !bindJSON(c, &req) {
		return
	}
	t, err := s.buildTask(&req, nil, true) // 新建：套用全局默认
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	created, err := s.deps.Backup.CreateTask(t)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(created))
}

// backupUpdateTask PUT /api/backup/tasks/:id
func (s *Server) backupUpdateTask(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的任务 ID"))
		return
	}
	var req backupTaskRequest
	if !bindJSON(c, &req) {
		return
	}
	old, err := s.deps.Backup.GetTask(int64(id))
	if err != nil {
		c.JSON(http.StatusNotFound, types.Fail(types.CodeBadRequest, "任务不存在"))
		return
	}
	t, err := s.buildTask(&req, old, false) // 编辑：不覆盖任务已有个性化设置
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	t.ID = old.ID
	updated, err := s.deps.Backup.UpdateTask(t)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(updated))
}

// backupDeleteTask DELETE /api/backup/tasks/:id
func (s *Server) backupDeleteTask(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的任务 ID"))
		return
	}
	if err := s.deps.Backup.DeleteTask(int64(id)); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"deleted": id}))
}

// backupRunTask POST /api/backup/tasks/:id/run （手动触发）
func (s *Server) backupRunTask(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的任务 ID"))
		return
	}
	h, err := s.deps.Backup.RunBackup(int64(id))
	if err != nil {
		// 任务已在执行中：返回 409 冲突语义（用 400 + 消息，保持项目错误风格）
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(h))
}

// backupListHistory GET /api/backup/tasks/:id/history
func (s *Server) backupListHistory(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的任务 ID"))
		return
	}
	list, err := s.deps.Backup.ListBackupsByTask(int64(id))
	if err != nil {
		errJSON(c, err)
		return
	}
	if list == nil {
		list = []*backup.History{} // 空列表编码为 []（而非 null）
	}
	c.JSON(http.StatusOK, types.OK(list))
}

// backupContents GET /api/backup/backups/:id/contents （浏览备份内文件列表）
func (s *Server) backupContents(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的备份 ID"))
		return
	}
	h, err := s.deps.Backup.GetBackup(int64(id))
	if err != nil {
		c.JSON(http.StatusNotFound, types.Fail(types.CodeFileNotFound, "备份记录不存在"))
		return
	}
	entries, err := backup.ListBackupContents(h.StorePath)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	if entries == nil {
		entries = []backup.BackupEntry{} // 空列表编码为 []（而非 null）
	}
	c.JSON(http.StatusOK, types.OK(entries))
}

// backupRestore POST /api/backup/backups/:id/restore
func (s *Server) backupRestore(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的备份 ID"))
		return
	}
	var req backup.RestoreRequest
	if !bindJSON(c, &req) {
		return
	}
	req.BackupID = int64(id)
	res, err := s.deps.Backup.Restore(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(res))
}

// backupFreeze POST /api/backup/backups/:id/freeze {frozen: true/false}
func (s *Server) backupFreeze(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的备份 ID"))
		return
	}
	var req struct {
		Frozen *bool `json:"frozen"`
	}
	if !bindJSON(c, &req) || req.Frozen == nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "缺少 frozen 参数"))
		return
	}
	h, err := s.deps.Backup.Freeze(int64(id), *req.Frozen)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(h))
}

// backupDelete DELETE /api/backup/backups/:id （手动删除备份，冻结的也允许手动删除）
func (s *Server) backupDelete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的备份 ID"))
		return
	}
	if err := s.deps.Backup.DeleteBackup(int64(id)); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	logger.Info("备份已删除", "backup_id", id)
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"deleted": id}))
}

// backupUSBDevices GET /api/backup/usb-devices （列出当前接入的可移动设备）
func (s *Server) backupUSBDevices(c *gin.Context) {
	devices := backup.ListUSBDevices()
	if devices == nil {
		devices = []backup.USBDevice{} // 空列表编码为 []（而非 null）
	}
	c.JSON(http.StatusOK, types.OK(devices))
}
