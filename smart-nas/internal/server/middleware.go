// Package server HTTP 服务层：中间件、路由、API 处理器。
package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/api/types"
	"smart-nas/internal/auth"
	"smart-nas/internal/util"
	"smart-nas/pkg/logger"
)

// ---- TraceID ----

// TraceID 生成 / 透传请求追踪 ID
func TraceID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			id = util.RandomHex(12)
		}
		c.Set("trace_id", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// ---- 访问日志 ----

// AccessLog 记录每个 HTTP 请求
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Info("HTTP 访问",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency", time.Since(start).String(),
			"ip", c.ClientIP(),
			"trace_id", c.GetString("trace_id"),
		)
	}
}

// ---- CORS ----

// CORS 允许跨域（家庭内网场景放开全部来源）
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			origin = "*"
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS,PATCH,HEAD")
		c.Header("Access-Control-Allow-Headers", "Content-Type,Authorization,Upload-Length,Upload-Metadata,Upload-Offset,Tus-Resumable,X-Request-ID,Range")
		c.Header("Access-Control-Expose-Headers", "Location,Upload-Offset,Upload-Length,Upload-Metadata,Tus-Resumable,Content-Disposition,X-Request-ID")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Max-Age", "86400")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// ---- JWT 认证 ----

// Auth 认证中间件：解析 Bearer Token 并写入上下文
func Auth(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, types.Fail(types.CodeUnauthorized, "未登录"))
			return
		}
		u, claims, err := authSvc.ValidateToken(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, types.Fail(types.CodeUnauthorized, "登录已过期，请重新登录"))
			return
		}
		c.Set("userID", u.ID)
		c.Set("username", claims.Username)
		c.Set("role", u.Role) // 使用数据库中的实时角色，角色变更立即生效
		c.Set("authUser", u)
		c.Next()
	}
}

// extractToken 从 Authorization 头或 token 参数提取 JWT
func extractToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	if t := c.Query("token"); t != "" {
		return t
	}
	return ""
}

// RequireAdmin 管理权限校验（需在 Auth 之后；主人/管理员可访问）
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, ok := c.Get("role")
		if !ok || (role != "admin" && role != "master") {
			c.AbortWithStatusJSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "需要管理员权限"))
			return
		}
		c.Next()
	}
}