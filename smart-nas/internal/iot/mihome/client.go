// Package mihome 米家开放平台客户端（骨架实现）。
//
// 说明：当前仅提供协议骨架与数据结构，完整接入（OAuth 授权、设备下发
// 指令、状态轮询）后续实现；接入时填充 doRequest 即可，无需改动上层。
package mihome

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Client 米家开放平台客户端
type Client struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	APIBase      string
	httpClient   *http.Client
	// accessToken 由 OAuth 授权流程获取，当前留空
	accessToken string
}

// NewClient 创建客户端
func NewClient(clientID, clientSecret, redirectURI, apiBase string) *Client {
	if apiBase == "" {
		apiBase = "https://api.io.mi.com/app"
	}
	return &Client{
		ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirectURI,
		APIBase: apiBase, httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// AuthorizeURL 生成 OAuth 授权地址（供前端跳转；后续实现）
func (c *Client) AuthorizeURL(state string) string {
	v := url.Values{}
	v.Set("client_id", c.ClientID)
	v.Set("redirect_uri", c.RedirectURI)
	v.Set("response_type", "code")
	v.Set("state", state)
	return "https://account.xiaomi.com/oauth2/authorize?" + v.Encode()
}

// ExchangeCode 用授权码换取 access_token（骨架）
func (c *Client) ExchangeCode(ctx context.Context, code string) (string, error) {
	return "", errors.New("米家 OAuth 接入待实现（ExchangeCode）")
}

// Control 下发设备控制指令（骨架）
func (c *Client) Control(ctx context.Context, deviceID, action string, params map[string]interface{}) error {
	return fmt.Errorf("米家设备控制待实现: %s/%s", deviceID, action)
}

// RefreshDevices 拉取用户设备列表（骨架）
func (c *Client) RefreshDevices(ctx context.Context) error {
	return errors.New("米家设备同步待实现（RefreshDevices）")
}

// Enabled 米家模块是否启用
func (c *Client) Enabled() bool { return c.ClientID != "" && c.ClientSecret != "" }