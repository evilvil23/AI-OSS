package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/api/types"
	"smart-nas/internal/config"
	"smart-nas/internal/settings"
	"smart-nas/internal/user"
	"smart-nas/internal/util"
	"smart-nas/pkg/logger"
)

// registerAdminRoutes 注册管理员路由（已包 RequireAdmin，主人/管理员可访问）
func (s *Server) registerAdminRoutes(admin *gin.RouterGroup) {
	admin.GET("/users", s.adminUsers)
	admin.POST("/users", s.adminCreateUser)
	admin.PUT("/users/:id", s.adminUpdateUser)
	admin.PUT("/users/:id/permissions", s.adminSetPermissions)
	admin.DELETE("/users/:id", s.adminDeleteUser)
	admin.GET("/settings", s.adminGetSettings)
	admin.PUT("/settings", s.adminSaveSettings)
	admin.POST("/settings/reset", s.adminResetSettings)
	admin.POST("/cache/clear", s.adminClearCache)
	admin.POST("/restart", s.adminRestart)
	admin.GET("/system/status", s.adminSystemStatus)
	admin.GET("/metrics", s.adminMetrics)
}

// adminRestart POST /api/admin/restart 请求进程级重启（主人/管理员，v0.23）。
// 非阻塞写入 RestartCh（缓冲 1），main goroutine 收到后走完整优雅关闭序列并重新
// 拉起自身进程；响应先于重启同步返回，前端轮询 /healthz 恢复后刷新页面。
func (s *Server) adminRestart(c *gin.Context) {
	if s.deps.RestartCh == nil {
		c.JSON(http.StatusServiceUnavailable, types.Fail(types.CodeServerError, "当前运行方式不支持自重启"))
		return
	}
	select {
	case s.deps.RestartCh <- struct{}{}:
	default:
		c.JSON(http.StatusConflict, types.Fail(types.CodeServerError, "重启已在进行中"))
		return
	}
	op := s.currentUser(c)
	logger.Info("收到网页重启请求，服务即将重启", "by", op.Username)
	c.JSON(http.StatusOK, types.OK(gin.H{"restarting": true}))
}

// systemStatus GET /api/system/status（所有登录用户可用，作为主页）
// 返回系统状态 + WebSocket 连接数 + 在线用户（主人/管理员才可见用户明细）
func (s *Server) systemStatus(c *gin.Context) {
	st, err := util.GetSystemStatus()
	if err != nil {
		errJSON(c, err)
		return
	}
	payload := map[string]interface{}{
		"system":        st,
		"ws_connections": s.deps.Hub.Count(),
	}
	role, _ := c.Get("role")
	if role == "master" || role == "admin" {
		payload["online_users"] = s.deps.Hub.OnlineUsers()
	}
	c.JSON(http.StatusOK, types.OK(payload))
}

// adminUsers GET /api/admin/users
func (s *Server) adminUsers(c *gin.Context) {
	users, err := s.deps.Users.List()
	if err != nil {
		errJSON(c, err)
		return
	}
	out := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		out = append(out, u.Public())
	}
	c.JSON(http.StatusOK, types.OK(out))
}

// adminCreateUser POST /api/admin/users {username,password,role,permissions}
// 主人可创建任意角色；管理员只能创建普通用户。
func (s *Server) adminCreateUser(c *gin.Context) {
	operator := s.currentUser(c)
	var req struct {
		Username    string            `json:"username" binding:"required"`
		Password    string            `json:"password" binding:"required"`
		Role        string            `json:"role"`
		Permissions []user.Permission `json:"permissions"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if req.Role == "" {
		req.Role = user.RoleUser
	}
	if operator.Role != user.RoleMaster && req.Role == user.RoleAdmin {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "只有主人才可创建管理员账号"))
		return
	}
	if operator.Role != user.RoleMaster && req.Role == user.RoleMaster {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "主人账号不可新增"))
		return
	}
	// 非主人只能授予自身权限范围内的目录权限（v0.13）
	if err := s.deps.Users.CanGrant(operator, req.Permissions); err != nil {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, err.Error()))
		return
	}
	u, err := s.deps.Users.CreateUser(req.Username, req.Password, req.Role, req.Permissions)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(u.Public()))
}

// adminUpdateUser PUT /api/admin/users/:id {role,permissions,password}
func (s *Server) adminUpdateUser(c *gin.Context) {
	operator := s.currentUser(c)
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的用户 ID"))
		return
	}
	var req struct {
		Role        *string           `json:"role"`
		Permissions []user.Permission `json:"permissions"`
		Password    *string           `json:"password"`
	}
	if !bindJSON(c, &req) {
		return
	}
	target, err := s.deps.Users.GetByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	if !operator.CanManageUser(target) {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "无权管理该用户"))
		return
	}
	// 管理员不能提升/降级角色到管理员
	if operator.Role == user.RoleAdmin && req.Role != nil && *req.Role != user.RoleUser {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "管理员只能管理普通用户"))
		return
	}
	// 非主人只能授予自身权限范围内的目录权限（v0.13）
	if req.Permissions != nil {
		if err := s.deps.Users.CanGrant(operator, req.Permissions); err != nil {
			c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, err.Error()))
			return
		}
	}
	u, err := s.deps.Users.UpdateUser(id, req.Role, req.Permissions, req.Password)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(u.Public()))
}

// adminSetPermissions PUT /api/admin/users/:id/permissions {permissions}
func (s *Server) adminSetPermissions(c *gin.Context) {
	operator := s.currentUser(c)
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的用户 ID"))
		return
	}
	var req struct {
		Permissions []user.Permission `json:"permissions"`
	}
	if !bindJSON(c, &req) {
		return
	}
	target, err := s.deps.Users.GetByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	if !operator.CanManageUser(target) {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "无权管理该用户"))
		return
	}
	// 非主人只能授予自身权限范围内的目录权限（v0.13）
	if err := s.deps.Users.CanGrant(operator, req.Permissions); err != nil {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, err.Error()))
		return
	}
	u, err := s.deps.Users.SetPermissions(id, req.Permissions)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(u.Public()))
}

// adminDeleteUser DELETE /api/admin/users/:id
func (s *Server) adminDeleteUser(c *gin.Context) {
	operator := s.currentUser(c)
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的用户 ID"))
		return
	}
	target, err := s.deps.Users.GetByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	if !operator.CanManageUser(target) {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "无权删除该用户"))
		return
	}
	if target.IsMaster() {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "主人账号不可删除"))
		return
	}
	if err := s.deps.Users.DeleteUser(id); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"deleted": id}))
}

// adminGetSettings GET /api/admin/settings
func (s *Server) adminGetSettings(c *gin.Context) {
	c.JSON(http.StatusOK, types.OK(s.deps.Settings.Get()))
}

// adminSaveSettings PUT /api/admin/settings {…设置项}
// 仅主人（master）可修改系统设置；管理员仅可查看。
func (s *Server) adminSaveSettings(c *gin.Context) {
	operator := s.currentUser(c)
	if operator.Role != user.RoleMaster {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "只有主人可以修改系统设置"))
		return
	}
	var patch map[string]interface{}
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "参数错误"))
		return
	}
	st, err := s.deps.Settings.Update(patch)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	// 应用回收站位置到存储服务
	if v, ok := patch["trash_path"]; ok {
		if p, ok2 := v.(string); ok2 {
			s.deps.Storage.SetTrashPath(p)
		}
	}
	// 应用日志设置：路径 / 最大大小 / 保留天数变化时重配日志器（立即生效）
	if _, ok1 := patch["log_path"]; ok1 {
		s.applyLogSettings(st)
	} else if _, ok2 := patch["log_max_size"]; ok2 {
		s.applyLogSettings(st)
	} else if _, ok3 := patch["log_max_age"]; ok3 {
		s.applyLogSettings(st)
	}
	c.JSON(http.StatusOK, types.OK(st))
}

// adminResetSettings POST /api/admin/settings/reset 重置为默认设置（仅主人）
func (s *Server) adminResetSettings(c *gin.Context) {
	operator := s.currentUser(c)
	if operator.Role != user.RoleMaster {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "只有主人可以重置系统设置"))
		return
	}
	st, err := s.deps.Settings.Reset()
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.Fail(types.CodeServerError, err.Error()))
		return
	}
	// 重置后同步生效项：回收站位置、日志器
	s.deps.Storage.SetTrashPath(st.TrashPath)
	s.applyLogSettings(st)
	logger.Info("系统设置已重置为默认值", "user", operator.Username)
	c.JSON(http.StatusOK, types.OK(st))
}

// adminClearCache POST /api/admin/cache/clear 清空视频转封装产物缓存（仅主人）
func (s *Server) adminClearCache(c *gin.Context) {
	operator := s.currentUser(c)
	if operator.Role != user.RoleMaster {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "只有主人可以清除缓存"))
		return
	}
	if s.deps.Play == nil {
		c.JSON(http.StatusOK, types.OK(map[string]interface{}{"cleared": false, "message": "播放功能未启用，无需清除"}))
		return
	}
	if err := s.deps.Play.ClearCache(); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"cleared": true}))
}

// applyLogSettings 按当前系统设置重配日志器（日志级别沿用 config.toml）
func (s *Server) applyLogSettings(st *settings.Settings) {
	lc := config.LogConfig{Level: "info", MaxBackups: 7, MaxSize: 100, MaxAge: 30, Path: ""}
	if s.cfg != nil {
		lc = s.cfg.GetConfig().Log
	}
	if st.LogPath != "" {
		lc.Path = st.LogPath
	}
	if st.LogMaxSize > 0 {
		lc.MaxSize = st.LogMaxSize
	}
	if st.LogMaxAge > 0 {
		lc.MaxAge = st.LogMaxAge
	}
	if lc.MaxSize <= 0 {
		lc.MaxSize = 100
	}
	if lc.MaxAge <= 0 {
		lc.MaxAge = 30
	}
	if err := logger.Reconfigure(lc.Level, lc.Path, lc.MaxSize, lc.MaxBackups, lc.MaxAge); err != nil {
		logger.Warn("日志重配失败", "error", err)
	} else {
		logger.Info("日志设置已生效", "path", lc.Path, "max_size_mb", lc.MaxSize, "max_age_days", lc.MaxAge)
	}
}

// adminSystemStatus GET /api/admin/system/status（管理员视角，含在线用户）
func (s *Server) adminSystemStatus(c *gin.Context) {
	st, err := util.GetSystemStatus()
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{
		"system":         st,
		"ws_connections": s.deps.Hub.Count(),
		"online_users":   s.deps.Hub.OnlineUsers(),
	}))
}

// adminMetrics GET /api/admin/metrics 返回指标快照（JSON，供前端监控页渲染）
func (s *Server) adminMetrics(c *gin.Context) {
	c.JSON(http.StatusOK, types.OK(s.metrics.Snapshot()))
}

// currentUser 从上下文取当前登录用户
func (s *Server) currentUser(c *gin.Context) *user.User {
	if u, ok := c.Get("authUser"); ok {
		if us, ok2 := u.(*user.User); ok2 {
			return us
		}
	}
	return &user.User{Role: "", ID: currentUID(c), Username: ""}
}