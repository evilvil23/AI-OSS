// admin.go Ollama 管理扩展接口（v0.21）：
// 已加载模型查询（等价 ollama ps）、模型加载 / 卸载、拉取与删除。
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// API 路径常量
const (
	psPath         = "/api/ps"
	generatePath   = "/api/generate"
	pullPath       = "/api/pull"
	deletePath     = "/api/delete"
	showPath       = "/api/show"
	embeddingsPath = "/api/embeddings"
)

// RunningModel 已加载到内存 / 显存的模型（等价 ollama ps 输出）
type RunningModel struct {
	Name       string    `json:"name"`
	Model      string    `json:"model"`
	Size       int64     `json:"size"`      // 模型整体占用（字节）
	SizeVRAM   int64     `json:"size_vram"` // 显存占用（字节；CPU 模式为 0 或部分）
	Digest     string    `json:"digest"`
	Expiration time.Time `json:"expiration"` // 自动卸载时间（常驻时为零值附近）
}

// RunningModels 查询已加载模型
func (c *Client) RunningModels(ctx context.Context) ([]RunningModel, error) {
	var out struct {
		Models []RunningModel `json:"models"`
	}
	if err := c.doJSON(ctx, http.MethodGet, psPath, nil, &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

// GenerateRequest 生成请求体（空提示预热 / 卸载模型共用）。
// keep_alive 复用 KeepAlive 类型：纯数字输出为 JSON number，避免
// Ollama 按 duration 解析报 "time: missing unit"。
type GenerateRequest struct {
	Model     string    `json:"model"`
	Prompt    string    `json:"prompt"`
	Stream    bool      `json:"stream"`
	KeepAlive KeepAlive `json:"keep_alive,omitempty"` // 模型驻留时长（"-1" 常驻 / "0" 卸载）
}

// Warmup 触发模型加载（空提示生成）；keepAlive 为空时使用服务端默认。
// 大模型加载耗时较长，使用独立长超时客户端，由 ctx 控制上限。
func (c *Client) Warmup(ctx context.Context, model, keepAlive string) error {
	req := GenerateRequest{Model: model, Prompt: "", Stream: false}
	if keepAlive != "" {
		req.KeepAlive = KeepAlive(keepAlive)
	}
	var out map[string]interface{}
	return c.doJSONLong(ctx, http.MethodPost, generatePath, req, &out)
}

// UnloadModel 以 keep_alive=0 请求卸载模型，释放显存 / 内存
func (c *Client) UnloadModel(ctx context.Context, model string) error {
	req := GenerateRequest{Model: model, Prompt: "", KeepAlive: KeepAlive("0")}
	var out map[string]interface{}
	return c.doJSONLong(ctx, http.MethodPost, generatePath, req, &out)
}

// PullProgress 拉取模型进度（NDJSON 流单行）
type PullProgress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
}

// PullModel 拉取模型，进度逐行推送到 progress 通道（通道由调用方创建；函数返回后关闭）。
// 拉取耗时不确定，同样使用长超时客户端。
func (c *Client) PullModel(ctx context.Context, model string, progress chan<- PullProgress) error {
	defer close(progress)
	req := map[string]interface{}{"model": model, "stream": true}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+pullPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.longClient.Do(hreq)
	if err != nil {
		return fmt.Errorf("ollama pull 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ollama pull 状态码 %d: %s", resp.StatusCode, truncate(string(b), 300))
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var p PullProgress
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		if p.Status == "error" {
			return fmt.Errorf("ollama pull 失败: %s", line)
		}
		select {
		case progress <- p:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return scanner.Err()
}

// DeleteModel 删除本地模型
func (c *Client) DeleteModel(ctx context.Context, model string) error {
	req := map[string]string{"model": model}
	var out map[string]interface{}
	return c.doJSON(ctx, http.MethodDelete, deletePath, req, &out)
}

// ShowModel 查看模型详情（量化信息等）
func (c *Client) ShowModel(ctx context.Context, model string) (map[string]interface{}, error) {
	req := map[string]string{"model": model}
	var out map[string]interface{}
	if err := c.doJSON(ctx, http.MethodPost, showPath, req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// doJSONLong 与 doJSON 相同，但使用无超时的长客户端（大模型加载 / 拉取场景），上限由 ctx 控制
func (c *Client) doJSONLong(ctx context.Context, method, path string, body, out interface{}) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	hreq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.longClient.Do(hreq)
	if err != nil {
		return fmt.Errorf("ollama 请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama %s 状态码 %d: %s", path, resp.StatusCode, truncate(string(data), 300))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	return nil
}
