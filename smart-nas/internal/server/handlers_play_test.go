package server

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"smart-nas/internal/auth"
	"smart-nas/internal/config"
	"smart-nas/internal/play"
	"smart-nas/internal/plugin"
	"smart-nas/internal/settings"
	"smart-nas/internal/storage"
	"smart-nas/internal/transport"
	"smart-nas/internal/user"
	"smart-nas/internal/ws"
)

// newPlayTestServer 构造带视频播放服务的测试服务
func newPlayTestServer(t *testing.T) *Server {
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
		JWTSecret: "test-secret", JWTExpire: 86400,
		Argon2Time: 1, Argon2Memory: 8 * 1024, Argon2Threads: 1, Argon2KeyLen: 32, Argon2SaltLen: 16,
		AdminUsername: "admin", AdminPassword: "admin123",
	}
	authSvc := auth.NewService(userSvc, authCfg)
	storageSvc.SetPermFn(func(uid uint, path string, write bool) bool { return userSvc.CanAccess(uid, path, write) })
	storageSvc.SetPermScopeFn(func(uid uint, dir string) bool { return userSvc.HasScopeUnder(uid, dir) })

	settingsSvc, err := settings.NewService(metaDir)
	if err != nil {
		t.Fatalf("settings.NewService: %v", err)
	}
	hub := ws.NewHub(authSvc)
	tm := transport.NewManager(storageSvc, filepath.Join(base, "root"), hub, nil)
	pluginMgr := plugin.NewManager(config.PluginConfig{}, filepath.Join(base, "plugins"))

	playSvc := play.NewService(config.PlayConfig{
		Enabled:         true,
		MaxOnlineHeight: 1440,
		FFmpegPath:      "ffmpeg",
		FFprobePath:     "ffprobe",
		CacheDir:        filepath.Join(base, "cache"),
		IPBind:          false,
	}, storageSvc)

	return New(Deps{
		Auth:      authSvc,
		Users:     userSvc,
		Storage:   storageSvc,
		Settings:  settingsSvc,
		Transport: tm,
		Hub:       hub,
		Plugins:   pluginMgr,
		Play:      playSvc,
	})
}

// 播放路由：认证与凭证校验
func TestPlayRoutesAuth(t *testing.T) {
	s := newPlayTestServer(t)
	token := login(t, s, "admin", "admin123")

	// 未认证访问视频信息 → 401
	if w := doJSON(t, s, http.MethodGet, "/api/video/info?file=1", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("info 未认证: %d", w.Code)
	}
	// 已认证但文件不存在 → 400
	if w := doJSON(t, s, http.MethodGet, "/api/video/info?file=9999", token, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("info 不存在文件: %d %s", w.Code, w.Body.String())
	}
	// 创建凭证缺少 file_id → 400
	if w := doJSON(t, s, http.MethodPost, "/api/play/ticket", token, `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("ticket 缺参: %d", w.Code)
	}
	// 创建凭证：文件不存在 → 400
	if w := doJSON(t, s, http.MethodPost, "/api/play/ticket", token, `{"file_id":9999}`); w.Code != http.StatusBadRequest {
		t.Fatalf("ticket 不存在文件: %d %s", w.Code, w.Body.String())
	}
	// 视频流：无凭证 → 401
	if w := doJSON(t, s, http.MethodGet, "/api/video?file=1", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("video 无 token: %d", w.Code)
	}
	// 视频流：伪造凭证 → 401
	if w := doJSON(t, s, http.MethodGet, "/api/video?file=1&token=fake-token-123", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("video 伪造 token: %d", w.Code)
	}
	// 释放凭证：幂等成功
	if w := doJSON(t, s, http.MethodDelete, "/api/play/ticket/fake-token-123", token, ""); w.Code != http.StatusOK {
		t.Fatalf("释放凭证: %d %s", w.Code, w.Body.String())
	}
}

// TestPlayTicketProbeFail 无 ffprobe 时：视频探测失败 → 创建凭证被拒并提示下载
func TestPlayTicketProbeFail(t *testing.T) {
	s := newPlayTestServer(t)
	token := login(t, s, "admin", "admin123")
	diskID, diskDir := firstDisk(t, s)
	fileID := trashTestFile(t, s, diskID, diskDir, "demo.mp4")

	w := doJSON(t, s, http.MethodPost, "/api/play/ticket", token, `{"file_id":`+itoa64(fileID)+`}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("无 ffprobe 时应拒绝创建凭证: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "播放") && !strings.Contains(w.Body.String(), "下载") {
		t.Fatalf("错误信息应提示不可播: %s", w.Body.String())
	}
}

// TestPlayAudioTicket 无 ffprobe 时：音频文件无需探测，仍可创建播放凭证并返回流地址
func TestPlayAudioTicket(t *testing.T) {
	s := newPlayTestServer(t)
	token := login(t, s, "admin", "admin123")
	diskID, diskDir := firstDisk(t, s)
	fileID := trashTestFile(t, s, diskID, diskDir, "demo.mp3")

	w := doJSON(t, s, http.MethodPost, "/api/play/ticket", token, `{"file_id":`+itoa64(fileID)+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("音频文件应可创建凭证: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "/api/video?file=") {
		t.Fatalf("应返回播放流地址: %s", w.Body.String())
	}
}