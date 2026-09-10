package server

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/ai"
	"smart-nas/internal/ai/ha"
	"smart-nas/internal/ai/hardware"
	"smart-nas/internal/ai/ollama"
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
	Lifecycle  *ollama.Lifecycle // Ollama 进程生命周期管理（v0.21）
	Hardware   *hardware.Info    // 硬件检测结果（v0.21）
	HAClient   *ha.Client        // HomeAssistant 客户端（v0.23 设备管理 API）
	RestartCh  chan struct{}     // 进程级重启请求通道（v0.23，main 收到后优雅重启；nil = 不支持）
	IoT        *iot.Service
	Plugins    *plugin.Manager
	Scheduler  *task.Scheduler
	Worker     *task.Worker
	WebDAV     *webdav.Handler
	Play       *play.Service
	Backup       *backup.Service
	DataDir      string            // 运行数据目录（绝对路径，供设置页默认值计算）
	WebDAVPrefix string
	TusPrefix    string
}

// WebVersion 前端静态资源版本号（index.html 模板经 {{.WebVersion}} 注入，
// 作为资源 URL 的 ?v= 缓存参数；发布新前端时同步更新此处）
const WebVersion = "0.23.1"

// Server HTTP 服务
type Server struct {
	deps    Deps
	engine  *gin.Engine
	metrics *Metrics
	cfg     *config.Manager
	httpSrv *http.Server
	webTmpl *template.Template // 前端页面模板（web/templates/index.html）
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
	s.loadWebTemplate()
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

	// 前端（v0.22 重组为 templates/ + static/）：
	//   /static/*      —— css/js 静态资源（gin Static）
	//   / 与 SPA 回退  —— html/template 渲染 templates/index.html（注入 WebVersion）
	// NoRoute 回退而非通配路由，避免与 API 路由冲突；未匹配的接口路径仍返回 JSON 404。
	if s.webTmpl != nil {
		engine.Static("/static", "web/static")
		engine.NoRoute(s.staticFallback())
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
	// AI 管家 / 知识库 / Ollama 管理（v0.21）：登录用户可见，管理接口要求管理员
	if s.deps.AI != nil {
		s.registerAIRoutes(authed)
	}
	// HomeAssistant 设备管理（v0.23）：独立于 AI 服务注册（HA 与 Ollama 解耦）
	s.registerHARoutes(authed)
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

// loadWebTemplate 启动时解析前端页面模板（web/templates/index.html）。
// 模板缺失或语法错误时记日志并置空——服务仍可提供 API（前端不可用）。
func (s *Server) loadWebTemplate() {
	const tplPath = "web/templates/index.html"
	data, err := os.ReadFile(tplPath)
	if err != nil {
		logger.Warn("前端页面模板不存在，Web 界面不可用", "path", tplPath, "error", err)
		return
	}
	tpl, err := template.New("index.html").Parse(string(data))
	if err != nil {
		logger.Warn("前端页面模板解析失败，Web 界面不可用", "path", tplPath, "error", err)
		return
	}
	s.webTmpl = tpl
	logger.Info("前端页面模板加载完成", "version", WebVersion)
}

// webRender 渲染前端页面模板（注入 WebVersion 缓存参数）
func (s *Server) webRender(c *gin.Context) {
	c.Header("Cache-Control", "no-cache") // 协商缓存：保证 ?v= 版本参数变更能及时生效
	c.Status(http.StatusOK)
	if err := s.webTmpl.Execute(c.Writer, gin.H{"WebVersion": WebVersion}); err != nil {
		logger.Warn("前端页面渲染失败", "error", err)
	}
}

// staticFallback 返回未匹配路由的处理器：/static 之外的静态文件优先服务，
// 其余回退渲染 index 模板（SPA 单页应用回退）
func (s *Server) staticFallback() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		// API / 实时 / 上传 / 指标 / 静态资源等未命中路径返回 JSON 404，避免误回退到前端
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/files/upload") ||
			strings.HasPrefix(p, "/metrics") || strings.HasPrefix(p, "/dav") ||
			strings.HasPrefix(p, "/static/") ||
			p == "/ws" || p == "/healthz" {
			c.JSON(http.StatusNotFound, types.Fail(types.CodeServerError, "接口不存在"))
			return
		}
		if s.webTmpl == nil {
			c.JSON(http.StatusNotFound, types.Fail(types.CodeServerError, "接口不存在"))
			return
		}
		// 兼容旧路径：/favicon.ico 等根级静态文件（如后续新增，放 web/static 并改引用即可）
		fp := filepath.Join("web/static", filepath.FromSlash(strings.TrimPrefix(p, "/")))
		if fi, err := os.Stat(fp); err == nil && !fi.IsDir() {
			c.File(fp)
			return
		}
		s.webRender(c)
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