package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/api/types"
)

// registerPluginRoutes 注册插件管理路由
func (s *Server) registerPluginRoutes(authed *gin.RouterGroup) {
	r := authed.Group("/plugins")
	r.GET("", s.pluginList)
	r.GET("/:id", s.pluginDetail)
	r.POST("/:id/enable", s.pluginEnable)
	r.POST("/:id/disable", s.pluginDisable)
	r.POST("/:id/reload", s.pluginReload)
}

// pluginList GET /api/plugins
func (s *Server) pluginList(c *gin.Context) {
	c.JSON(http.StatusOK, types.OK(s.deps.Plugins.Plugins()))
}

// pluginDetail GET /api/plugins/:id
func (s *Server) pluginDetail(c *gin.Context) {
	id := c.Param("id")
	for _, p := range s.deps.Plugins.Plugins() {
		if p["id"] == id {
			c.JSON(http.StatusOK, types.OK(p))
			return
		}
	}
	c.JSON(http.StatusNotFound, types.Fail(types.CodeServerError, "插件不存在"))
}

// pluginEnable POST /api/plugins/:id/enable
func (s *Server) pluginEnable(c *gin.Context) {
	if err := s.deps.Plugins.Enable(c.Param("id")); err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"enabled": true}))
}

// pluginDisable POST /api/plugins/:id/disable
func (s *Server) pluginDisable(c *gin.Context) {
	if err := s.deps.Plugins.Disable(c.Param("id")); err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"disabled": true}))
}

// pluginReload POST /api/plugins/:id/reload
func (s *Server) pluginReload(c *gin.Context) {
	if err := s.deps.Plugins.Reload(c.Param("id")); err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"reloaded": true}))
}