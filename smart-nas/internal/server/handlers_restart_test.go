// handlers_restart_test.go 进程级重启端点测试（v0.23）。
//
// 验证：管理员可触发重启（RestartCh 收到信号）、普通用户 403、
// 通道满时 409、未注入 RestartCh 时 503。
package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestAdminRestartByMaster 主人触发重启 → 200 且 RestartCh 收到信号
func TestAdminRestartByMaster(t *testing.T) {
	s := newTestServer(t)
	s.deps.RestartCh = make(chan struct{}, 1)
	token := login(t, s, "admin", "admin123")

	w := doJSON(t, s, http.MethodPost, "/api/admin/restart", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("主人重启请求应 200: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "restarting") {
		t.Fatalf("响应应包含 restarting 标记: %s", w.Body.String())
	}
	select {
	case <-s.deps.RestartCh:
	case <-time.After(time.Second):
		t.Fatal("RestartCh 未收到重启信号")
	}
}

// TestAdminRestartForbidden 普通用户请求重启 → 403
func TestAdminRestartForbidden(t *testing.T) {
	s := newTestServer(t)
	s.deps.RestartCh = make(chan struct{}, 1)
	master := login(t, s, "admin", "admin123")
	createUser(t, s, master, "tom", "tom123", "user")
	tom := login(t, s, "tom", "tom123")

	w := doJSON(t, s, http.MethodPost, "/api/admin/restart", tom, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通用户重启请求应 403: %d %s", w.Code, w.Body.String())
	}
}

// TestAdminRestartConflict 通道满（重启已在进行中）→ 409
func TestAdminRestartConflict(t *testing.T) {
	s := newTestServer(t)
	s.deps.RestartCh = make(chan struct{}, 1) // 预先塞满，模拟重启进行中
	s.deps.RestartCh <- struct{}{}
	token := login(t, s, "admin", "admin123")

	w := doJSON(t, s, http.MethodPost, "/api/admin/restart", token, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("通道满时重启请求应 409: %d %s", w.Code, w.Body.String())
	}
}

// TestAdminRestartNotSupported 未注入 RestartCh → 503
func TestAdminRestartNotSupported(t *testing.T) {
	s := newTestServer(t) // RestartCh 为 nil
	token := login(t, s, "admin", "admin123")

	w := doJSON(t, s, http.MethodPost, "/api/admin/restart", token, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("未注入 RestartCh 时应 503: %d %s", w.Code, w.Body.String())
	}
}
