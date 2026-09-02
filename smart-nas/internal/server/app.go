package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/ai"
	"smart-nas/internal/api/types"
	"smart-nas/internal/auth"
	"smart-nas/internal/backup"
	"smart-nas/internal/config"
	"smart-nas/internal/iot"
	"smart-nas/internal/play"
	"smart-nas/internal/plugin"
	"smart-nas/internal/settings"
	"smart-nas/internal/storage"
	"smart-nas/internal/task"
	"smart-nas/internal/transport"
	"smart-nas/internal/transport/tusd"
	"smart-nas/internal/user"
	"smart-nas/internal/util"
	"smart-nas/internal/webdav"
	"smart-nas/internal/ws"
	"smart-nas/pkg/logger"
)

// Deps 服务依赖集合
type Deps struct {
	Cfg        *config.Manager
	Auth       *auth.Service
	Users      *user.Service
	Storage    *storage.Service
	Settings   *settings.Service
	Transport  *transport.Manager
	Tus        *tusd.Handler
	Hub        *ws.Hub
	AI         *ai.Service
	IoT        *iot.Service
	Plugins    *plugin.Manager
	Scheduler  *task.Scheduler
	Worker     *task.Worker
	WebDAV     *webdav.Handler
	Play       *play.Service
	Backup     *backup.Service
	WebDAVPrefix string
	TusPrefix    string
}

// Server HTTP 服务
type Server struct {
	deps    Deps
	engine  *gin.Engine
	metrics *Metrics
	cfg     *config.Manager
	httpSrv *http.Server
}

// New 创建服务实例并注册路由
func New(deps Deps) *Server {
	s := &Server{
		deps:    deps,
		metrics: NewMetrics(),
		cfg:     deps.Cfg,
	}
	gin.SetMode(gin.ReleaseMode)
	if deps.Cfg != nil {
		if mode := deps.Cfg.GetConfig().Server.Mode; mode == "debug" || mode == "test" {
			gin.SetMode(mode)
		}
	}
	s.engine = gin.New()
	s.engine.Use(gin.Recovery(), TraceID(), AccessLog(), CORS())
	s.setupRoutes()
	s.setupMetrics()
	return s
}

// Engine 返回 gin 引擎（调试 / 测试用）
func (s *Server) Engine() *gin.Engine { return s.engine }

// setupRoutes 注册全部路由（对应文档 §API 设计）
func (s *Server) setupRoutes() {
	engine := s.engine
	metrics := s.metrics
	authSvc := s.deps.Auth

	// 公开：健康检查 / 指标
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.OK(map[string]interface{}{"status": "ok"}))
	})
	engine.GET("/metrics", metrics.Handler())

	// 静态前端（web/ 目录存在时启用，单页应用回退）
	// 采用 NoRoute 回退而非 Static("/")，避免通配路由与 API 路由冲突；
	// 未匹配的 API/接口路径仍返回 JSON 404。
	if stat, err := os.Stat("web"); err == nil && stat.IsDir() {
		engine.NoRoute(staticFallback("web"))
	}

	// 公开 API
	api := engine.Group("/api")
	api.POST("/auth/login", s.login)
	api.POST("/auth/logout", s.logout)
	api.GET("/files/share/:token", s.shareDownload)

	// 需认证的 API
	authed := api.Group("")
	authed.Use(Auth(authSvc))
	authed.GET("/auth/me", s.me)
	authed.POST("/auth/change-password", s.changePassword)
	authed.GET("/system/status", s.systemStatus)
	s.registerFileRoutes(authed)
	// 说明：AI（/api/ai）与 IoT（/api/iot）模块当前仅保留扩展接口，
	// 具体 HTTP 路由后续实现，届时在 authed 组注册 registerAIRoutes/registerIoTRoutes。
	s.registerPluginRoutes(authed)

	// 备份还原（v0.20）：登录用户可见任务与历史；触发/写操作要求主人/管理员
	if s.deps.Backup != nil {
		s.registerBackupRoutes(authed)
	}

	// 视频在线播放（v0.16）
	// /api/video 由播放凭证（Ticket）认证，供 <video> 标签直接加载（无法附带 JWT 头）
	if s.deps.Play != nil && s.deps.Play.Enabled() {
		authed.POST("/play/ticket", s.playCreateTicket)
		authed.DELETE("/play/ticket/:token", s.playReleaseTicket)
		authed.GET("/video/info", s.playVideoInfo)
		engine.GET("/api/video", s.playVideoStream)
	}

	// 管理员 API
	admin := authed.Group("/admin")
	admin.Use(RequireAdmin())
	s.registerAdminRoutes(admin)

	// WebSocket
	engine.GET("/ws", s.deps.Hub.Handle)

	// tus 上传端点（内部通过 authenticate 校验）
	if s.deps.Tus != nil {
		prefix := s.deps.TusPrefix
		engine.Any(prefix+"/*filepath", s.deps.Tus.Handle)
	}

	// WebDAV
	if s.deps.WebDAV != nil && s.deps.WebDAVPrefix != "" {
		s.deps.WebDAV.Mount(engine, s.deps.WebDAVPrefix)
		logger.Info("WebDAV 已挂载", "prefix", s.deps.WebDAVPrefix)
	}
}

// setupMetrics 上报基础系统指标
func (s *Server) setupMetrics() {
	go func() {
		for range time.Tick(15 * time.Second) {
			if st, err := util.GetSystemStatus(); err == nil {
				s.metrics.SetGauge("nas_system_cpu_usage_percent", "CPU usage percent", st.CPUUsage)
			}
			s.metrics.SetGauge("nas_ws_connections", "Active websocket connections", float64(s.deps.Hub.Count()))
			if s.deps.Play != nil {
				s.metrics.SetGauge("nas_play_remux_active", "Active video remux tasks", s.deps.Play.ActiveRemux())
				s.metrics.SetGauge("nas_play_transcode_active", "Active video transcode tasks", s.deps.Play.ActiveTranscode())
				s.metrics.SetGauge("nas_play_tickets", "Active playback tickets", s.deps.Play.ActiveTickets())
			}
		}
	}()
}

// Run 启动 HTTP 服务（阻塞），ctx 取消时优雅退出
func (s *Server) Run(ctx context.Context, addr string) error {
	s.httpSrv = &http.Server{
		Addr:    addr,
		Handler: s.engine,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP 服务启动", "addr", addr)
		if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.httpSrv.Shutdown(shutdownCtx)
	}
}

// staticFallback 返回未匹配路由的处理器：优先服务静态文件，其余回退 index.html
func staticFallback(webDir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		// API / 实时 / 上传 / 指标等未命中路径返回 JSON 404，避免误回退到前端
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/files/upload") ||
			strings.HasPrefix(p, "/metrics") || strings.HasPrefix(p, "/dav") ||
			p == "/ws" || p == "/healthz" {
			c.JSON(http.StatusNotFound, types.Fail(types.CodeServerError, "接口不存在"))
			return
		}
		if p == "/" {
			c.File(filepath.Join(webDir, "index.html"))
			return
		}
		fp := filepath.Join(webDir, filepath.FromSlash(strings.TrimPrefix(p, "/")))
		if fi, err := os.Stat(fp); err == nil && !fi.IsDir() {
			c.File(fp)
			return
		}
		c.File(filepath.Join(webDir, "index.html"))
	}
}

// ---- 通用 Helper ----
func currentUID(c *gin.Context) uint {
	if v, ok := c.Get("userID"); ok {
		return v.(uint)
	}
	return 0
}

// parseID 路由参数转 uint
func parseID(c *gin.Context, name string) (uint, bool) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return uint(id), true
}

// bindJSON 绑定请求体并统一错误响应
func bindJSON(c *gin.Context, v interface{}) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, fmt.Sprintf("参数错误: %v", err)))
		return false
	}
	return true
}

// errJSON 统一错误响应
func errJSON(c *gin.Context, err error) {
	c.JSON(http.StatusInternalServerError, types.Fail(types.CodeServerError, err.Error()))
}

// uintOf 将接口值安全转为 uint（日志、事件载荷等场景）
func uintOf(v interface{}) uint {
	switch t := v.(type) {
	case uint:
		return t
	case int:
		return uint(t)
	case int64:
		return uint(t)
	case float64:
		return uint(t)
	case string:
		n, err := strconv.ParseUint(t, 10, 64)
		if err != nil {
			return 0
		}
		return uint(n)
	default:
		return 0
	}
}