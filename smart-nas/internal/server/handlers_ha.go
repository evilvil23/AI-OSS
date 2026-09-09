// handlers_ha.go HomeAssistant 智能家居设备管理 API（v0.23）。
//
// 与 AI 工具（internal/ai/ha/tools.go）共用同一个 HA 客户端（Deps.HAClient），
// 为前端「智能家居」Tab 提供实体列表 / 状态查询 / 服务调用能力；
// 所有端点登录用户可用（家庭内网场景，与 AI 对话权限一致）。
// HAClient 为 nil（未配置地址 / 令牌）时统一返回 503 降级提示。
package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/api/types"
	"smart-nas/internal/ai/ha"
)

// registerHARoutes 注册 HomeAssistant 设备管理路由（挂载在 /api/ai/ha 下）
func (s *Server) registerHARoutes(authed *gin.RouterGroup) {
	g := authed.Group("/ai/ha")
	g.GET("/status", s.haStatus)
	g.GET("/devices", s.haDevices)
	g.POST("/service", s.haServiceCall)
}

// requireHA 返回 HA 客户端（未配置时以 503 响应并返回 false）
func (s *Server) requireHA(c *gin.Context) *ha.Client {
	if s.deps.HAClient == nil {
		c.JSON(http.StatusServiceUnavailable, types.Fail(types.CodeServerError,
			"HomeAssistant 未配置（需在 config.toml [ai.homeassistant] 配置地址与令牌）"))
		return nil
	}
	return s.deps.HAClient
}

// haStatus GET /api/ai/ha/status
// 返回 {enabled, connected, base_url}：connected 现场探测（3 秒超时），供页面徽标展示
func (s *Server) haStatus(c *gin.Context) {
	cli := s.requireHA(c)
	if cli == nil {
		return
	}
	cfg := s.deps.Cfg.GetConfig().AI.HomeAssistant
	ctx, cancel := contextWithTimeout(c, 3*time.Second)
	defer cancel()
	connected := cli.CheckConnection(ctx) == nil
	c.JSON(http.StatusOK, types.OK(gin.H{
		"enabled":   cfg.Enabled,
		"connected": connected,
		"base_url":  cfg.BaseURL,
	}))
}

// haDevices GET /api/ai/ha/devices?domain=light
// 列出 HA 实体（domain 可选过滤）：{connected, total, devices: [...]}；
// name 取 attributes.friendly_name（ha.State 顶层 FriendlyName 不序列化），domain 由 entity_id 拆分
func (s *Server) haDevices(c *gin.Context) {
	cli := s.requireHA(c)
	if cli == nil {
		return
	}
	domain := strings.TrimSpace(c.Query("domain"))
	states, err := cli.ListStates(c.Request.Context(), domain)
	if err != nil {
		c.JSON(http.StatusBadGateway, types.Fail(types.CodeServerError, err.Error()))
		return
	}
	devices := make([]gin.H, 0, len(states))
	for _, st := range states {
		entity, dom := st.EntityID, ""
		if i := strings.Index(st.EntityID, "."); i >= 0 {
			dom, entity = st.EntityID[:i], st.EntityID[i+1:]
		}
		name, _ := st.Attributes["friendly_name"].(string)
		if name == "" {
			name = st.EntityID
		}
		devices = append(devices, gin.H{
			"entity_id":   st.EntityID,
			"domain":      dom,
			"entity":      entity,
			"name":        name,
			"state":       st.State,
			"attributes":  st.Attributes,
			"last_changed": st.LastChanged,
		})
	}
	c.JSON(http.StatusOK, types.OK(gin.H{
		"connected": true,
		"total":     len(devices),
		"devices":   devices,
	}))
}

// haServiceCall POST /api/ai/ha/service {domain, service, entity_id, data?}
// 调用 HA 服务（如 light/turn_on、switch/turn_off、climate/set_temperature）
func (s *Server) haServiceCall(c *gin.Context) {
	cli := s.requireHA(c)
	if cli == nil {
		return
	}
	var req struct {
		Domain   string                 `json:"domain" binding:"required"`
		Service  string                 `json:"service" binding:"required"`
		EntityID string                 `json:"entity_id" binding:"required"`
		Data     map[string]interface{} `json:"data"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	if err := cli.CallService(c.Request.Context(), req.Domain, req.Service, req.EntityID, req.Data); err != nil {
		c.JSON(http.StatusBadGateway, types.Fail(types.CodeServerError, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(gin.H{
		"result":    "ok",
		"entity_id": req.EntityID,
		"service":   req.Domain + "/" + req.Service,
	}))
}

// contextWithTimeout 基于请求上下文派生超时上下文（请求断开时提前取消）
func contextWithTimeout(c *gin.Context, d time.Duration) (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), d)
}
