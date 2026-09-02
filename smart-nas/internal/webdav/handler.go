package webdav

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/webdav"

	"smart-nas/internal/auth"
)

// Handler WebDAV 处理器：Basic Auth 认证 + 用户目录隔离
type Handler struct {
	auth   *auth.Service
	root   string
	prefix string
	dav    *webdav.Handler
}

// NewHandler 创建 WebDAV 处理器
func NewHandler(authSvc *auth.Service, root string) *Handler {
	return &Handler{
		auth: authSvc,
		root: root,
		dav:  &webdav.Handler{LockSystem: webdav.NewMemLS()},
	}
}

// davMethods WebDAV 所需的完整方法集。gin 的 Any() 仅注册标准 HTTP 方法，
// 不含 PROPFIND/MKCOL/COPY/MOVE/LOCK 等 WebDAV 扩展方法，需逐个显式注册，
// 否则资源管理器挂载（PROPFIND 列目录）会 404。
var davMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions,
	"PROPFIND", "PROPPATCH", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK",
}

// Mount 将 WebDAV 挂载到 gin 路由（prefix 如 /dav）
func (h *Handler) Mount(r gin.IRoutes, prefix string) {
	h.prefix = prefix
	h.dav.Prefix = prefix
	// 同时注册前缀本身与子路径，保证 /dav 与 /dav/xxx 均可访问
	for _, m := range davMethods {
		r.Handle(m, prefix, h.handle)
		r.Handle(m, prefix+"/*path", h.handle)
	}
}

// handle 统一入口：Basic Auth 认证后交由 x/net/webdav 处理
// （通过 permDir 包装，每次操作前校验用户目录权限）
func (h *Handler) handle(c *gin.Context) {
	userID, ok := h.basicAuth(c)
	if !ok {
		c.Header("WWW-Authenticate", `Basic realm="SmartNAS WebDAV"`)
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	dir, err := userRoot(h.root, userID)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	h.dav.FileSystem = permDir{
		Dir:    webdav.Dir(dir),
		userID: userID,
		can: func(uid uint, path string, write bool) bool {
			return h.auth.CanAccessPath(uid, path, write)
		},
	}
	h.dav.ServeHTTP(c.Writer, c.Request)
}

// basicAuth 解析 Authorization: Basic 头并校验用户名密码
func (h *Handler) basicAuth(c *gin.Context) (uint, bool) {
	authHeader := c.GetHeader("Authorization")
	if !strings.HasPrefix(authHeader, "Basic ") {
		return 0, false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
	if err != nil {
		return 0, false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	uid, err := h.auth.CheckPassword(parts[0], parts[1])
	if err != nil {
		return 0, false
	}
	return uid, true
}