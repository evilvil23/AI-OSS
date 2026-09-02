package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"smart-nas/internal/auth"
	"smart-nas/internal/config"
	"smart-nas/internal/plugin"
	"smart-nas/internal/security"
	"smart-nas/internal/settings"
	"smart-nas/internal/storage"
	"smart-nas/internal/transport"
	"smart-nas/internal/user"
	"smart-nas/internal/ws"
)

func testArgon() security.Argon2Params {
	return security.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
}

// newTestServer 构造一个内存/临时目录下的完整服务（不监听端口，仅用于 httptest）
func newTestServer(t *testing.T) *Server {
	t.Helper()
	base := t.TempDir()
	diskDir := filepath.Join(base, "disk1")
	metaDir := filepath.Join(base, "db")

	storageRepo, err := storage.NewRepository(filepath.Join(metaDir, "metadata.db"), metaDir)
	if err != nil {
		t.Fatalf("storage.NewRepository: %v", err)
	}
	t.Cleanup(func() { _ = storageRepo.Close() })
	storageSvc, err := storage.NewService(storageRepo, filepath.Join(base, "root"),
		config.StorageConfig{Disks: []string{diskDir}}, testArgon())
	if err != nil {
		t.Fatalf("storage.NewService: %v", err)
	}

	userRepo, err := user.NewRepository(metaDir)
	if err != nil {
		t.Fatalf("user.NewRepository: %v", err)
	}
	userSvc := user.NewService(userRepo, testArgon())
	if err := userSvc.EnsureMaster("admin", "admin123"); err != nil {
		t.Fatalf("EnsureMaster: %v", err)
	}

	authCfg := config.AuthConfig{
		JWTSecret:     "test-secret",
		JWTExpire:     86400,
		Argon2Time:    1,
		Argon2Memory:  8 * 1024,
		Argon2Threads: 1,
		Argon2KeyLen:  32,
		Argon2SaltLen: 16,
		AdminUsername: "admin",
		AdminPassword: "admin123",
	}
	authSvc := auth.NewService(userSvc, authCfg)

	storageSvc.SetPermFn(func(uid uint, path string, write bool) bool {
		return userSvc.CanAccess(uid, path, write)
	})
	storageSvc.SetPermScopeFn(func(uid uint, dir string) bool {
		return userSvc.HasScopeUnder(uid, dir)
	})

	settingsSvc, err := settings.NewService(metaDir)
	if err != nil {
		t.Fatalf("settings.NewService: %v", err)
	}
	hub := ws.NewHub(authSvc)
	tm := transport.NewManager(storageSvc, filepath.Join(base, "root"), hub, nil)
	pluginMgr := plugin.NewManager(config.PluginConfig{}, filepath.Join(base, "plugins"))

	return New(Deps{
		Auth:      authSvc,
		Users:     userSvc,
		Storage:   storageSvc,
		Settings:  settingsSvc,
		Transport: tm,
		Hub:       hub,
		Plugins:   pluginMgr,
	})
}

func doJSON(t *testing.T, s *Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Engine().ServeHTTP(w, req)
	return w
}

func login(t *testing.T, s *Server, username, password string) string {
	t.Helper()
	w := doJSON(t, s, http.MethodPost, "/api/auth/login", "",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", username, w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("login 解析失败: %v", err)
	}
	return resp.Data.Token
}

func createUser(t *testing.T, s *Server, token, username, password, role string) {
	t.Helper()
	w := doJSON(t, s, http.MethodPost, "/api/admin/users", token,
		`{"username":"`+username+`","password":"`+password+`","role":"`+role+`","permissions":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("createUser %s: %d %s", username, w.Code, w.Body.String())
	}
}

// TestAdminSaveSettingsMasterOnly：设置仅主人可修改；管理员/普通用户被拒绝
func TestAdminSaveSettingsMasterOnly(t *testing.T) {
	s := newTestServer(t)
	masterToken := login(t, s, "admin", "admin123")

	// 主人保存成功
	w := doJSON(t, s, http.MethodPut, "/api/admin/settings", masterToken,
		`{"cpu_refresh_seconds":10,"disk_refresh_seconds":90,"trash_path":"D:/nas-trash"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("主人保存设置应成功: %d %s", w.Code, w.Body.String())
	}

	// 管理员保存被拒（403）
	createUser(t, s, masterToken, "boss", "boss123", "admin")
	adminToken := login(t, s, "boss", "boss123")
	w2 := doJSON(t, s, http.MethodPut, "/api/admin/settings", adminToken,
		`{"cpu_refresh_seconds":20}`)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("管理员保存设置应 403, got %d %s", w2.Code, w2.Body.String())
	}

	// 普通用户访问 admin 路由被拒（403）
	createUser(t, s, masterToken, "tom", "tom123", "user")
	tomToken := login(t, s, "tom", "tom123")
	w3 := doJSON(t, s, http.MethodGet, "/api/admin/settings", tomToken, "")
	if w3.Code != http.StatusForbidden {
		t.Fatalf("普通用户访问 admin 应 403, got %d", w3.Code)
	}
}

// TestListFilesDisksIntegration：parent_id=0 返回磁盘根节点（盘符显示后端链路）
func TestListFilesDisksIntegration(t *testing.T) {
	s := newTestServer(t)
	token := login(t, s, "admin", "admin123")
	w := doJSON(t, s, http.MethodGet, "/api/files?parent_id=0", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("列出磁盘失败: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data []struct {
			ID          uint   `json:"id"`
			Name        string `json:"name"`
			IsDir       bool   `json:"is_dir"`
			ParentID    uint   `json:"parent_id"`
			StoragePath string `json:"storage_path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if resp.Code != 0 || len(resp.Data) != 1 {
		t.Fatalf("应返回 1 个磁盘节点, got code=%d data=%+v", resp.Code, resp.Data)
	}
	d := resp.Data[0]
	if !d.IsDir || d.ParentID != 0 || d.StoragePath == "" {
		t.Fatalf("磁盘节点字段不符: %+v", d)
	}
}
