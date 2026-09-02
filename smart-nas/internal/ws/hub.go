// Package ws 实时通信 Hub：管理 WebSocket 连接，支持全局广播与按用户定向推送。
//
// 说明：文档选用 coder/websocket，但当前离线环境不可用；这里基于
// golang.org/x/net/websocket 实现等价能力（端点 /ws?token=<jwt>）。
// 为避免 x/net/websocket 不支持并发写的问题，每个连接由独立的
// sender goroutine 串行写入。
package ws

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/websocket"

	"smart-nas/internal/api/types"
	"smart-nas/internal/auth"
	"smart-nas/pkg/logger"
)

// Client 单个 WebSocket 连接
type Client struct {
	conn        *websocket.Conn
	userID      uint
	username    string
	role        string
	device      string // 登录设备类型：pc / mobile（由 User-Agent 判定）
	connectedAt time.Time
	send        chan []byte
	hub         *Hub
}

// OnlineUser 在线用户信息（按用户聚合登录设备）
type OnlineUser struct {
	UserID      uint     `json:"user_id"`
	Username    string   `json:"username"`
	Role        string   `json:"role"`
	Devices     []string `json:"devices"`     // 登录设备类型（去重）：pc / mobile
	Connections int      `json:"connections"` // 原始连接数（诊断用）
}

// Hub WebSocket 连接管理器
type Hub struct {
	mu       sync.RWMutex
	clients  map[*Client]struct{}
	byUser   map[uint]map[*Client]struct{}
	auth     *auth.Service
	closed   bool
	pingTick time.Duration
}

// NewHub 创建连接管理器（authSvc 用于校验连接 token）
func NewHub(authSvc *auth.Service) *Hub {
	return &Hub{
		clients:  make(map[*Client]struct{}),
		byUser:   make(map[uint]map[*Client]struct{}),
		auth:     authSvc,
		pingTick: 30 * time.Second,
	}
}

// SetPingInterval 设置心跳间隔（默认 30s）
func (h *Hub) SetPingInterval(d time.Duration) { h.pingTick = d }

// Handle 升级为 WebSocket：校验 token 后接入
func (h *Hub) Handle(c *gin.Context) {
	uid, username, role, ok := h.authenticate(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, types.Fail(types.CodeUnauthorized, "无效的 Token"))
		return
	}
	device := DeviceType(c.GetHeader("User-Agent"))
	handler := websocket.Handler(func(conn *websocket.Conn) {
		h.serveConn(conn, uid, username, role, device)
	})
	handler.ServeHTTP(c.Writer, c.Request)
}

// DeviceType 根据 User-Agent 识别登录设备类型：
// 手机/平板浏览器返回 mobile，其余（桌面浏览器、桌面客户端）返回 pc。
func DeviceType(userAgent string) string {
	ua := strings.ToLower(userAgent)
	if strings.Contains(ua, "mobile") || strings.Contains(ua, "android") ||
		strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad") ||
		strings.Contains(ua, "ipod") || strings.Contains(ua, "harmonyos") ||
		strings.Contains(ua, "huaweibrowser") || strings.Contains(ua, "miuibrowser") {
		return "mobile"
	}
	return "pc"
}

// authenticate 从 token 查询参数或 Authorization 头提取并校验 JWT
func (h *Hub) authenticate(c *gin.Context) (uint, string, string, bool) {
	token := c.Query("token")
	if token == "" {
		token = c.GetHeader("Authorization")
		token = strings.TrimPrefix(token, "Bearer ")
	}
	if token == "" {
		return 0, "", "", false
	}
	u, claims, err := h.auth.ValidateToken(token)
	if err != nil {
		return 0, "", "", false
	}
	return u.ID, claims.Username, claims.Role, true
}

// serveConn 连接生命周期：注册、接收循环、发送循环
func (h *Hub) serveConn(conn *websocket.Conn, userID uint, username, role, device string) {
	client := &Client{
		conn:        conn,
		userID:      userID,
		username:    username,
		role:        role,
		device:      device,
		connectedAt: time.Now(),
		send:        make(chan []byte, 256),
		hub:         h,
	}
	h.register(client)
	defer h.unregister(client)

	go h.writePump(client)
	h.readPump(client)
}

// readPump 读取客户端消息（心跳应答等）
func (h *Hub) readPump(client *Client) {
	defer client.conn.Close()
	for {
		var msg types.WSMessage
		if err := websocket.JSON.Receive(client.conn, &msg); err != nil {
			return
		}
		switch msg.Type {
		case "ping":
			client.enqueue(types.NewWSMessage("pong", nil))
		case "subscribe":
			// 预留：客户端可订阅自定义频道（当前仅按用户定向）
			client.enqueue(types.NewWSMessage("subscribed", map[string]interface{}{"ok": true}))
		}
	}
}

// writePump 串行发送队列中的消息，并周期性发送心跳
func (h *Hub) writePump(client *Client) {
	ticker := time.NewTicker(h.pingTick)
	defer func() {
		ticker.Stop()
		client.conn.Close()
	}()
	for {
		select {
		case data, more := <-client.send:
			if !more {
				return
			}
			if _, err := client.conn.Write(data); err != nil {
				return
			}
		case <-ticker.C:
			data, _ := json.Marshal(types.NewWSMessage("ping", nil))
			if _, err := client.conn.Write(data); err != nil {
				return
			}
		}
	}
}

// enqueue 非阻塞写入发送队列（队满丢弃，避免阻塞业务）
func (c *Client) enqueue(msg types.WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}

func (h *Hub) register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		c.conn.Close()
		return
	}
	h.clients[c] = struct{}{}
	if h.byUser[c.userID] == nil {
		h.byUser[c.userID] = make(map[*Client]struct{})
	}
	h.byUser[c.userID][c] = struct{}{}
}

func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	if m := h.byUser[c.userID]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(h.byUser, c.userID)
		}
	}
	close(c.send)
}

// Broadcast 广播到所有连接（实现 transport.Publisher）
func (h *Hub) Broadcast(msg types.WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	for c := range h.clients {
		select {
		case c.send <- data:
		default:
		}
	}
	h.mu.RUnlock()
}

// BroadcastToUser 推送给指定用户的全部连接（实现 transport.Publisher）
func (h *Hub) BroadcastToUser(userID uint, msg types.WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	for c := range h.byUser[userID] {
		select {
		case c.send <- data:
		default:
		}
	}
	h.mu.RUnlock()
}

// Count 当前连接数
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// OnlineUsers 在线用户列表（按用户聚合登录设备与连接数）
func (h *Hub) OnlineUsers() []OnlineUser {
	h.mu.RLock()
	defer h.mu.RUnlock()
	agg := make(map[uint]*OnlineUser)
	for c := range h.clients {
		u := agg[c.userID]
		if u == nil {
			u = &OnlineUser{UserID: c.userID, Username: c.username, Role: c.role, Devices: []string{}}
			agg[c.userID] = u
		}
		u.Connections++
		found := false
		for _, d := range u.Devices {
			if d == c.device {
				found = true
				break
			}
		}
		if !found && c.device != "" {
			u.Devices = append(u.Devices, c.device)
		}
	}
	out := make([]OnlineUser, 0, len(agg))
	for _, u := range agg {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// Shutdown 关闭所有连接
func (h *Hub) Shutdown() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	for c := range h.clients {
		c.conn.Close()
	}
	h.mu.Unlock()
	logger.Info("WebSocket Hub 已关闭")
}