// Package ha HomeAssistant REST API 客户端（v0.21 M7）。
//
// 覆盖实体查询 / 状态读取 / 服务调用（开关、亮度、温度等）与连通性自检。
// 当前无 HA 设备时仅交付配置 + 自检，接入后由 AI 工具调用（见 tools.go）。
package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client HomeAssistant REST 客户端
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient 创建客户端；baseURL 形如 http://homeassistant.local:8123
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// State HA 实体状态
type State struct {
	EntityID    string                 `json:"entity_id"`
	State       string                 `json:"state"`
	Attributes  map[string]interface{} `json:"attributes,omitempty"`
	LastChanged time.Time              `json:"last_changed,omitempty"`
	FriendlyName string                `json:"-"`
}

// CheckConnection 连通性自检：GET /api/ 携带 token 校验
func (c *Client) CheckConnection(ctx context.Context) error {
	var out map[string]interface{}
	if err := c.do(ctx, http.MethodGet, "/api/", nil, &out); err != nil {
		return err
	}
	return nil
}

// ListStates 拉取实体列表；domain 非空时按前缀过滤（如 light / switch / climate）
func (c *Client) ListStates(ctx context.Context, domain string) ([]State, error) {
	var states []State
	if err := c.do(ctx, http.MethodGet, "/api/states", nil, &states); err != nil {
		return nil, err
	}
	if domain == "" {
		return states, nil
	}
	prefix := strings.TrimSuffix(domain, ".") + "."
	filtered := make([]State, 0, len(states))
	for _, s := range states {
		if strings.HasPrefix(s.EntityID, prefix) {
			filtered = append(filtered, s)
		}
	}
	return filtered, nil
}

// GetState 查询单个实体状态
func (c *Client) GetState(ctx context.Context, entityID string) (*State, error) {
	var st State
	if err := c.do(ctx, http.MethodGet, "/api/states/"+entityID, nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// CallService 调用 HA 服务（如 light/turn_on、switch/turn_off、climate/set_temperature）
func (c *Client) CallService(ctx context.Context, domain, service, entityID string, data map[string]interface{}) error {
	if data == nil {
		data = map[string]interface{}{}
	}
	data["entity_id"] = entityID
	var out []map[string]interface{}
	return c.do(ctx, http.MethodPost, "/api/services/"+domain+"/"+service, data, &out)
}

// do 请求 HA REST API
func (c *Client) do(ctx context.Context, method, path string, body, out interface{}) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("HA 请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("HA 认证失败（401）：请检查 [ai.homeassistant].token")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HA %s 状态码 %d: %s", path, resp.StatusCode, truncate(string(data), 300))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return err
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
