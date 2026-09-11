// handlers_ai.go AI 管家 / 知识库 / Ollama 管理 API（v0.21 M4 / M5）。
//
// 对话与状态：所有登录用户（JWT 鉴权，家庭内网场景）；
// 模型管理 / 参数调整：仅主人 / 管理员；
// OpenAI 兼容代理（/api/ai/v1/chat/completions）：登录用户，转发本机 Ollama，
// 供局域网内第三方应用经 smart-nas 鉴权调用。
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/ai/ollama"
	"smart-nas/internal/ai/rag"
	"smart-nas/internal/api/types"
	"smart-nas/pkg/logger"
)

// registerAIRoutes 注册 AI 路由
func (s *Server) registerAIRoutes(authed *gin.RouterGroup) {
	r := authed.Group("/ai")
	// 对话与会话（登录用户）
	r.POST("/chat", s.aiChat)
	r.POST("/chat/stream", s.aiChatStream)
	r.GET("/conversations", s.aiListConversations)
	r.POST("/conversations", s.aiCreateConversation)
	r.GET("/conversations/:id", s.aiGetConversation)
	r.DELETE("/conversations/:id", s.aiDeleteConversation)
	// RAG 知识库
	r.POST("/rag/search", s.aiRAGSearch) // dual 模式辅助机也经此查询主服务
	r.GET("/rag/status", s.aiRAGStatus)
	// 状态总览（进程 / 已加载模型 / 硬件预设 / 部署模式）
	r.GET("/status", s.aiStatus)
	// OpenAI 兼容代理（局域网调用入口，走 smart-nas JWT）
	r.POST("/v1/chat/completions", s.aiOpenAIProxy)

	// Ollama 管理（仅主人 / 管理员）
	admin := r.Group("")
	admin.Use(RequireAdmin())
	admin.GET("/models", s.aiListModels)
	admin.POST("/models/pull", s.aiPullModel)
	admin.GET("/models/pull/status", s.aiPullStatus)
	admin.DELETE("/models/*name", s.aiDeleteModel)
	admin.GET("/settings", s.aiGetSettings)
	admin.PUT("/settings", s.aiUpdateSettings)
}

// uid 从上下文取当前用户 ID
func uid(c *gin.Context) uint {
	if v, ok := c.Get("userID"); ok {
		if id, ok := v.(uint); ok {
			return id
		}
	}
	return 0
}

// requireAI 返回 AI 服务（未启用时以 4001 响应）
func (s *Server) requireAI(c *gin.Context) bool {
	if s.deps.AI == nil {
		c.JSON(http.StatusServiceUnavailable, types.Fail(types.CodeModelNotLoaded, "AI 模块未启用（检查 [ai] 配置与 Ollama）"))
		return false
	}
	return true
}

// ---- 对话 ----

type aiChatRequest struct {
	ConversationID string `json:"conversation_id"`
	Content        string `json:"content" binding:"required"`
}

// aiChat POST /api/ai/chat 非流式对话
func (s *Server) aiChat(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	var req aiChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	ctx := c.Request.Context()
	start := time.Now()
	reply, err := s.deps.AI.Chat(ctx, uid(c), req.ConversationID, req.Content)
	if err != nil {
		logger.Warn("AI 对话失败", "error", err)
		c.JSON(http.StatusInternalServerError, types.Fail(types.CodeInferenceDim, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(gin.H{
		"reply":       reply,
		"elapsed_ms":  time.Since(start).Milliseconds(),
		"model":       s.deps.AI.Preset().Model,
		"remote":      s.deps.AI.RemoteActive(),
	}))
}

// aiChatStream POST /api/ai/chat/stream 流式对话（SSE）
func (s *Server) aiChatStream(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	var req aiChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	events, err := s.deps.AI.StreamChat(c.Request.Context(), uid(c), req.ConversationID, req.Content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.Fail(types.CodeInferenceDim, err.Error()))
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, types.Fail(types.CodeServerError, "当前连接不支持流式响应"))
		return
	}
	clientGone := c.Request.Context().Done()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			flusher.Flush()
			if ev.Done {
				return
			}
		case <-clientGone:
			return
		}
	}
}

// ---- 会话管理 ----

// aiListConversations GET /api/ai/conversations
func (s *Server) aiListConversations(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	c.JSON(http.StatusOK, types.OK(s.deps.AI.ListConversations(uid(c))))
}

// aiCreateConversation POST /api/ai/conversations
func (s *Server) aiCreateConversation(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	_ = c.ShouldBindJSON(&req)
	conv := s.deps.AI.CreateConversation(uid(c), req.Title)
	c.JSON(http.StatusOK, types.OK(conv))
}

// aiGetConversation GET /api/ai/conversations/:id
func (s *Server) aiGetConversation(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	conv, ok := s.deps.AI.GetConversation(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, types.Fail(types.CodeBadRequest, "会话不存在"))
		return
	}
	// 仅本人可见
	if conv.UserID != uid(c) {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "无权访问该会话"))
		return
	}
	c.JSON(http.StatusOK, types.OK(conv))
}

// aiDeleteConversation DELETE /api/ai/conversations/:id
func (s *Server) aiDeleteConversation(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	s.deps.AI.DeleteConversation(c.Param("id"))
	c.JSON(http.StatusOK, types.OK(nil))
}

// ---- RAG ----

// aiRAGSearch POST /api/ai/rag/search 语义检索（dual 模式辅助机远程调用入口）
func (s *Server) aiRAGSearch(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	var req struct {
		Query string `json:"query" binding:"required"`
		TopK  int    `json:"top_k"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	chunks, err := s.deps.AI.SearchRAG(c.Request.Context(), req.Query, uid(c), req.TopK)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.Fail(types.CodeServerError, err.Error()))
		return
	}
	if chunks == nil {
		chunks = make([]rag.Chunk, 0) // 空结果编码为 [] 而非 null
	}
	c.JSON(http.StatusOK, types.OK(gin.H{"chunks": chunks}))
}

// aiRAGStatus GET /api/ai/rag/status 索引状态
func (s *Server) aiRAGStatus(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	c.JSON(http.StatusOK, types.OK(s.deps.AI.IndexStatus()))
}

// ---- 状态总览 ----

// aiStatus GET /api/ai/status
// AI 禁用时不报错，返回 enabled=false 与禁用原因（前端据此给出差异化提示）
func (s *Server) aiStatus(c *gin.Context) {
	if s.deps.AI == nil {
		reason := s.deps.AIReason
		if reason == "" {
			reason = "AI 模块未启用（检查 [ai] 配置与 Ollama）"
		}
		c.JSON(http.StatusOK, types.OK(gin.H{"enabled": false, "reason": reason}))
		return
	}
	ctx := c.Request.Context()
	out := gin.H{
		"enabled": true,
		"preset":  s.deps.AI.Preset(),
		"deploy":  s.deps.AI.DeployStatus(),
	}
	if s.deps.Lifecycle != nil {
		out["ollama"] = s.deps.Lifecycle.Status(ctx)
	}
	if hw := s.deps.Hardware; hw != nil {
		out["hardware"] = hw
	}
	c.JSON(http.StatusOK, types.OK(out))
}

// ---- OpenAI 兼容代理（M5 局域网调用） ----

// aiOpenAIProxy POST /api/ai/v1/chat/completions
// 将 OpenAI 格式请求转发至本机 Ollama 的 OpenAI 兼容端点（/v1）。
// model 缺省时使用当前生效模型；stream:true 时透明转发 SSE。
func (s *Server) aiOpenAIProxy(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 8<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "读取请求体失败"))
		return
	}
	// model 缺省时补当前生效模型
	var req map[string]interface{}
	_ = json.Unmarshal(body, &req)
	if m, _ := req["model"].(string); m == "" {
		req["model"] = s.deps.AI.Preset().Model
		body, _ = json.Marshal(req)
	}

	target := s.deps.AI.Client().BaseURL() + "/v1/chat/completions"
	preq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.Fail(types.CodeServerError, err.Error()))
		return
	}
	preq.Header.Set("Content-Type", "application/json")
	resp, err := proxyClient.Do(preq)
	if err != nil {
		c.JSON(http.StatusBadGateway, types.Fail(types.CodeServerError, "Ollama 不可达: "+err.Error()))
		return
	}
	defer resp.Body.Close()

	// 透传响应（流式 SSE 逐块冲刷）
	for k, vs := range resp.Header {
		for _, v := range vs {
			c.Writer.Header().Add(k, v)
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)
	flusher, canFlush := c.Writer.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := c.Writer.Write(buf[:n]); werr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

var proxyClient = &http.Client{Timeout: 0} // 上限由请求 ctx 控制（流式长响应）

// ---- 模型管理（M4，管理员） ----

// aiListModels GET /api/ai/models（含量化信息）
func (s *Server) aiListModels(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	models, err := s.deps.AI.ListModels(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, types.Fail(types.CodeServerError, err.Error()))
		return
	}
	if models == nil {
		models = make([]ollama.ModelInfo, 0)
	}
	c.JSON(http.StatusOK, types.OK(gin.H{
		"models":        models,
		"default_model": s.deps.AI.Preset().Model,
	}))
}

// pullTask 模型拉取任务进度
type pullTask struct {
	Model     string    `json:"model"`
	Status    string    `json:"status"`
	Total     int64     `json:"total"`
	Completed int64     `json:"completed"`
	Error     string    `json:"error,omitempty"`
	Done      bool      `json:"done"`
	StartedAt time.Time `json:"started_at"`
}

var (
	pullMu    sync.Mutex
	pullTasks = map[string]*pullTask{}
)

// aiPullModel POST /api/ai/models/pull 后台拉取模型，进度经 /models/pull/status 查询
func (s *Server) aiPullModel(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	var req struct {
		Model string `json:"model" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	pullMu.Lock()
	if t, ok := pullTasks[req.Model]; ok && !t.Done {
		pullMu.Unlock()
		c.JSON(http.StatusOK, types.OK(t))
		return
	}
	task := &pullTask{Model: req.Model, Status: "starting", StartedAt: time.Now()}
	pullTasks[req.Model] = task
	pullMu.Unlock()

	progress := make(chan ollama.PullProgress, 64)
	go func() {
		err := s.deps.AI.Client().PullModel(context.Background(), req.Model, progress)
		for p := range progress {
			pullMu.Lock()
			task.Status = p.Status
			task.Total = p.Total
			task.Completed = p.Completed
			pullMu.Unlock()
		}
		pullMu.Lock()
		task.Done = true
		if err != nil {
			task.Status = "error"
			task.Error = err.Error()
			logger.Warn("模型拉取失败", "model", req.Model, "error", err)
		} else {
			task.Status = "success"
			logger.Info("模型拉取完成", "model", req.Model)
		}
		pullMu.Unlock()
	}()
	c.JSON(http.StatusOK, types.OK(task))
}

// aiPullStatus GET /api/ai/models/pull/status
func (s *Server) aiPullStatus(c *gin.Context) {
	pullMu.Lock()
	defer pullMu.Unlock()
	list := make([]*pullTask, 0, len(pullTasks))
	for _, t := range pullTasks {
		list = append(list, t)
	}
	c.JSON(http.StatusOK, types.OK(list))
}

// aiDeleteModel DELETE /api/ai/models/:name
func (s *Server) aiDeleteModel(c *gin.Context) {
	if !s.requireAI(c) {
		return
	}
	name := c.Param("name")
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "缺少模型名"))
		return
	}
	if err := s.deps.AI.Client().DeleteModel(c.Request.Context(), name); err != nil {
		c.JSON(http.StatusBadGateway, types.Fail(types.CodeServerError, err.Error()))
		return
	}
	logger.Info("模型已删除", "model", name, "by", uid(c))
	c.JSON(http.StatusOK, types.OK(nil))
}

// ---- 推理参数（M4） ----

// aiGetSettings GET /api/ai/settings 当前生效模型与调优参数。
// AI 禁用时同样可用（设置页需要展示开关并在启用后重启服务）
func (s *Server) aiGetSettings(c *gin.Context) {
	c.JSON(http.StatusOK, types.OK(s.aiSettingsData()))
}

// aiUpdateSettings PUT /api/ai/settings 调整模型与推理参数（config.toml 热更新）
// v0.23 增加：deploy_mode（auto|server|auxiliary）、server_addr（服务端地址）；
// 部署模式 / 服务端地址 / Ollama 地址 / 向量模型等启动期装配项变更时，
// config.Manager 置位待重启标志（响应 need_restart=true），前端据此提示用户重启服务。
// v0.26 增加：enabled（AI 总开关，变更需重启）；AI 禁用时接口同样可用
//（否则开关关闭后无法再从页面启用）
func (s *Server) aiUpdateSettings(c *gin.Context) {
	var req struct {
		Enabled               *bool    `json:"enabled"` // AI 功能总开关
		Model                 *string  `json:"model"`
		NumCtx                *int     `json:"num_ctx"`
		KeepAlive             *string  `json:"keep_alive"`
		NumParallel           *int     `json:"num_parallel"`
		Temperature           *float64 `json:"temperature"`
		ConversationMaxTokens *int     `json:"conversation_max_tokens"`
		EmbeddingModel        *string  `json:"embedding_model"`
		DeployMode            *string  `json:"deploy_mode"` // auto | server | auxiliary
		ServerAddr            *string  `json:"server_addr"` // 服务端地址（host:port 或 URL）→ ai.deploy.remote_host
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	patch := map[string]interface{}{}
	aiPatch := map[string]interface{}{}
	if req.Enabled != nil {
		aiPatch["enabled"] = *req.Enabled
	}
	if req.Model != nil {
		aiPatch["default_model"] = *req.Model
	}
	if req.NumCtx != nil {
		aiPatch["tune"] = map[string]interface{}{"num_ctx": *req.NumCtx}
	}
	if req.KeepAlive != nil {
		if v, ok := aiPatch["tune"].(map[string]interface{}); ok {
			v["keep_alive"] = *req.KeepAlive
		} else {
			aiPatch["tune"] = map[string]interface{}{"keep_alive": *req.KeepAlive}
		}
	}
	if req.NumParallel != nil {
		if v, ok := aiPatch["tune"].(map[string]interface{}); ok {
			v["num_parallel"] = *req.NumParallel
		} else {
			aiPatch["tune"] = map[string]interface{}{"num_parallel": *req.NumParallel}
		}
	}
	if req.Temperature != nil {
		aiPatch["temperature"] = *req.Temperature
	}
	if req.ConversationMaxTokens != nil {
		aiPatch["conversation_max_tokens"] = *req.ConversationMaxTokens
	}
	if req.EmbeddingModel != nil {
		aiPatch["embedding_model"] = *req.EmbeddingModel
	}
	if req.DeployMode != nil {
		deploy := map[string]interface{}{}
		switch *req.DeployMode {
		case "auto": // 自动：启动时按服务端可达性判定（设置页文案「自动」）
			deploy["mode"] = "auto"
		case "server": // 服务端
			deploy["mode"] = "dual"
			deploy["role"] = "primary"
		case "auxiliary": // 辅机（由服务端 / 远端提供模型支持探测由本机发起）
			deploy["mode"] = "dual"
			deploy["role"] = "auxiliary"
		default:
			c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "deploy_mode 仅支持 auto / server / auxiliary"))
			return
		}
		aiPatch["deploy"] = deploy
	}
	if req.ServerAddr != nil {
		addr := strings.TrimSpace(*req.ServerAddr)
		if addr != "" && !strings.Contains(addr, "://") {
			addr = "http://" + addr
		}
		if v, ok := aiPatch["deploy"].(map[string]interface{}); ok {
			v["remote_host"] = addr
		} else {
			aiPatch["deploy"] = map[string]interface{}{"remote_host": addr}
		}
	}
	if len(aiPatch) == 0 {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无可更新字段"))
		return
	}
	patch["ai"] = aiPatch
	if err := s.deps.Cfg.UpdateConfig(patch); err != nil {
		c.JSON(http.StatusInternalServerError, types.Fail(types.CodeServerError, err.Error()))
		return
	}
	logger.Info("AI 参数已更新", "by", uid(c))
	// need_restart 由 aiSettingsData 一并下发（config.Manager 已按变更置位）
	c.JSON(http.StatusOK, types.OK(s.aiSettingsData()))
}

// aiSettingsData 组装当前设置视图；AI 禁用时（无 preset）以 config 持久值
// 代替运行态预设，保证设置页可展示并在重新启用后重启生效
func (s *Server) aiSettingsData() gin.H {
	cfg := s.deps.Cfg.GetConfig()
	model, numCtx, keepAlive, numParallel, presetSource := cfg.AI.DefaultModel, cfg.AI.Tune.NumCtx, cfg.AI.Tune.KeepAlive, cfg.AI.Tune.NumParallel, "config"
	if s.deps.AI != nil {
		preset := s.deps.AI.Preset()
		model, numCtx, keepAlive, numParallel, presetSource = preset.Model, preset.NumCtx, preset.KeepAlive, preset.NumParallel, preset.Source
	}
	return gin.H{
		"enabled":                 cfg.AI.Enabled,
		"model":                   model,
		"preset_source":           presetSource,
		"num_ctx":                 numCtx,
		"keep_alive":              keepAlive,
		"num_parallel":            numParallel,
		"temperature":             cfg.AI.Temperature,
		"conversation_max_tokens": cfg.AI.ConversationMaxTokens,
		"embedding_model":         cfg.AI.EmbeddingModel,
		// v0.23 部署模式：deploy_mode 为 config.toml 持久值（auto / dual / single），
		// role 为解析或显式配置的角色
		"deploy_mode": cfg.AI.Deploy.Mode,
		"role":        cfg.AI.Deploy.Role,
		"server_addr": cfg.AI.Deploy.RemoteHost,
		// v0.26 待重启标志：保存过启动期装配项后置位，服务重启后重置
		"need_restart": s.deps.Cfg.RestartRequired(),
	}
}
