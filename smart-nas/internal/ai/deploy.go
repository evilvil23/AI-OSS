// deploy.go 部署模式与主备双机（v0.21 M6）。
//
// 三种形态互不依赖，切换只改 config.toml，不涉及代码改动：
//   - single（默认）：本机独立运行，按硬件自适应选择本机模型，RAG 用本机索引；
//   - dual + primary：本机为常驻主服务（通常 N100），低配模型 + RAG 默认开启；
//   - dual + auxiliary：本机为辅助机（通常大主机），启动时探测远端 Ollama——
//     可达则自动切换为远端模型并调用主服务侧 RAG（不重复建立向量库），
//     不可达则优雅降级回本机模型并记录告警；运行中周期探测自动切换。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"smart-nas/internal/ai/ollama"
	"smart-nas/internal/ai/rag"
	"smart-nas/internal/config"
	"smart-nas/pkg/logger"
)

// deployState 部署模式运行时状态
type deployState struct {
	mu           sync.RWMutex
	mode         string // single | dual
	role         string // primary | auxiliary
	remoteHost   string
	remoteAPI    string
	username     string
	password     string
	autoSwitch   bool
	checkSeconds int

	remoteModel  string         // 远端生效模型
	remoteClient *ollama.Client // 远端 Ollama 客户端（auxiliary 用）
	remoteActive bool           // auxiliary 当前是否走远端
}

// newDeployState 初始化部署状态并按配置预置远端客户端
func newDeployState(cfg config.AIConfig) *deployState {
	d := &deployState{
		mode:         cfg.Deploy.Mode,
		role:         cfg.Deploy.Role,
		remoteHost:   cfg.Deploy.RemoteHost,
		remoteAPI:    cfg.Deploy.RemoteAPI,
		username:     cfg.Deploy.RemoteUsername,
		password:     cfg.Deploy.RemotePassword,
		autoSwitch:   cfg.Deploy.AutoSwitch,
		checkSeconds: cfg.Deploy.CheckInterval,
		remoteModel:  cfg.Deploy.RemoteModel,
	}
	if d.remoteModel == "" {
		d.remoteModel = cfg.DefaultModel
	}
	if d.mode == "dual" && d.role == "auxiliary" && d.remoteHost != "" {
		d.remoteClient = ollama.NewClient(d.remoteHost, 90*time.Second)
	}
	return d
}

// IsAuxiliary 是否为双机模式下的辅助机
func (d *deployState) IsAuxiliary() bool {
	return d.mode == "dual" && d.role == "auxiliary"
}

// Snapshot 部署状态快照（供状态 API）
func (d *deployState) Snapshot() map[string]interface{} {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return map[string]interface{}{
		"mode":          d.mode,
		"role":          d.role,
		"remote_host":   d.remoteHost,
		"remote_model":  d.remoteModel,
		"remote_active": d.remoteActive,
		"auto_switch":   d.autoSwitch,
	}
}

// UpdateConfig 热更新部署参数（v0.23，配置热重载回调调用）。
// mode/role 变更属启动期装配项、需重启生效，不在此处理（运行态身份以启动解析为准）；
// 远端地址 / 账号 / 自动切换 / 探测间隔等运行期参数即时生效，
// auxiliary 且 remote_host 变化时重建远端客户端并复位切换状态。
func (d *deployState) UpdateConfig(cfg config.AIConfig) {
	d.mu.Lock()
	remoteChanged := d.remoteHost != cfg.Deploy.RemoteHost
	d.remoteHost = cfg.Deploy.RemoteHost
	d.remoteAPI = cfg.Deploy.RemoteAPI
	d.username = cfg.Deploy.RemoteUsername
	d.password = cfg.Deploy.RemotePassword
	d.autoSwitch = cfg.Deploy.AutoSwitch
	d.checkSeconds = cfg.Deploy.CheckInterval
	d.remoteModel = cfg.Deploy.RemoteModel
	if d.remoteModel == "" {
		d.remoteModel = cfg.DefaultModel
	}
	// 角色已切换为辅助机但尚无远端客户端（如启动时地址为空），此处补建
	if d.role == "auxiliary" && d.remoteHost != "" && (d.remoteClient == nil || remoteChanged) {
		d.remoteClient = ollama.NewClient(d.remoteHost, 90*time.Second)
		d.remoteActive = false // 待下次探测确认可达性
	}
	d.mu.Unlock()
}

// StartDeployWatch 启动部署模式巡检（仅 auxiliary 生效）：
// 启动时立即探测一次，之后按 check_interval 周期探测并自动切换 / 降级。
func (s *Service) StartDeployWatch(ctx context.Context) {
	d := s.deploy
	if d == nil || !d.IsAuxiliary() {
		return
	}
	if d.remoteHost == "" {
		logger.Warn("双机模式辅助机未配置 remote_host，将始终使用本机模型")
		return
	}
	go func() {
		interval := time.Duration(d.checkSeconds) * time.Second
		if interval <= 0 {
			interval = 30 * time.Second
		}
		s.probeRemote(ctx, true)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if d.autoSwitch {
					s.probeRemote(ctx, false)
				}
			}
		}
	}()
}

// probeRemote 探测远端 Ollama 可达性并执行切换 / 降级
func (s *Service) probeRemote(ctx context.Context, initial bool) {
	d := s.deploy
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reachable := d.remoteClient.Ping(pctx) == nil

	d.mu.Lock()
	was := d.remoteActive
	if reachable {
		d.remoteActive = true
	} else {
		d.remoteActive = false
	}
	d.mu.Unlock()

	switch {
	case reachable && !was:
		logger.Info("远端主服务 Ollama 可达，已切换为远端模型（卸载本地模型，RAG 走主服务）",
			"remote_host", d.remoteHost, "remote_model", d.remoteModel)
		s.unloadLocalModel()
	case !reachable && was:
		logger.Warn("远端主服务不可达，降级回本机模型（大主机高级模式）", "remote_host", d.remoteHost)
	case initial && !reachable:
		logger.Warn("远端主服务不可达，使用本机模型（远端恢复后将自动切换）",
			"remote_host", d.remoteHost)
	case initial && reachable:
		logger.Info("辅助机已连接远端主服务，使用远端模型",
			"remote_host", d.remoteHost, "remote_model", d.remoteModel)
	}
}

// unloadLocalModel 切换远端时卸载本机模型，释放资源
func (s *Service) unloadLocalModel() {
	model := s.Preset().Model
	if model == "" || s.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.client.UnloadModel(ctx, model); err != nil {
		logger.Warn("卸载本地模型失败", "model", model, "error", err)
	} else {
		logger.Info("本地模型已卸载", "model", model)
	}
}

// DeployStatus 部署状态（供 /api/ai/status）
func (s *Service) DeployStatus() map[string]interface{} {
	if s.deploy == nil {
		return map[string]interface{}{"mode": "single"}
	}
	return s.deploy.Snapshot()
}

// RemoteActive 辅助机当前是否走远端（供路由层展示 / 调试）
func (s *Service) RemoteActive() bool {
	if s.deploy == nil {
		return false
	}
	s.deploy.mu.RLock()
	defer s.deploy.mu.RUnlock()
	return s.deploy.remoteActive
}

// ---- 远程 RAG（辅助机调用主服务侧检索） ----

// remoteRetriever 通过主服务 HTTP API 检索（辅助机不建立独立向量库）
type remoteRetriever struct {
	api      string
	username string
	password string
	topK     int

	mu    sync.Mutex
	token string
}

// NewRemoteRetriever 创建远程 RAG 检索器（dual 模式 auxiliary 用）
func NewRemoteRetriever(api, username, password string, topK int) *remoteRetriever {
	return &remoteRetriever{api: strings.TrimRight(api, "/"), username: username, password: password, topK: topK}
}

// ragSearchRequest /api/ai/rag/search 请求体
type ragSearchRequest struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
	UserID uint  `json:"-"`
}

// Search 远程检索：登录获取 JWT → 查询主服务 RAG
func (r *remoteRetriever) Search(ctx context.Context, query string, userID uint, topK int) ([]rag.Chunk, error) {
	if topK <= 0 {
		topK = r.topK
	}
	token, err := r.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	chunks, err := r.doSearch(ctx, token, query, topK)
	if errors.Is(err, errRemoteUnauthorized) {
		// token 过期：重登一次
		r.mu.Lock()
		r.token = ""
		r.mu.Unlock()
		if token, err = r.ensureToken(ctx); err != nil {
			return nil, err
		}
		return r.doSearch(ctx, token, query, topK)
	}
	return chunks, err
}

var errRemoteUnauthorized = errors.New("远程认证失败")

func (r *remoteRetriever) doSearch(ctx context.Context, token, query string, topK int) ([]rag.Chunk, error) {
	body, _ := json.Marshal(ragSearchRequest{Query: query, TopK: topK})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.api+"/api/ai/rag/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("主服务 RAG 查询失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errRemoteUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("主服务 RAG 查询状态码 %d: %s", resp.StatusCode, truncateStr(string(data), 200))
	}
	var out struct {
		Chunks []rag.Chunk `json:"chunks"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out.Chunks, nil
}

// ensureToken 登录主服务获取 JWT（缓存复用）
func (r *remoteRetriever) ensureToken(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.token != "" {
		return r.token, nil
	}
	body, _ := json.Marshal(map[string]string{"username": r.username, "password": r.password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.api+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("主服务登录失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("主服务登录状态码 %d: %s", resp.StatusCode, truncateStr(string(data), 200))
	}
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Data.Token == "" {
		return "", errors.New("主服务登录响应缺少 token")
	}
	r.token = out.Data.Token
	return r.token, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
