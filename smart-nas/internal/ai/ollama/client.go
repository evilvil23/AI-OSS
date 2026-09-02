// Package ollama 封装 Ollama REST API 客户端（离线本地推理）。
//
// 遵循 Ollama 官方 API：/api/chat、/api/embeddings、/api/tags。
// 支持非流式、NDJSON 流式 chat、Embedding 生成与模型列表查询。
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// API 路径常量
const (
	chatPath  = "/api/chat"
	embedPath = "/api/embeddings"
	tagsPath  = "/api/tags"
)

// Client Ollama HTTP 客户端
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient 创建客户端；host 形如 http://localhost:11434
func NewClient(host string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	return &Client{
		baseURL:    strings.TrimRight(host, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// ChatMessage 对话消息
type ChatMessage struct {
	Role    string     `json:"role"`
	Content string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall 模型发起的工具调用
type ToolCall struct {
	Function FunctionCall `json:"function"`
}

// FunctionCall 工具调用详情（Arguments 为 JSON 字符串）
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatOptions 生成参数
type ChatOptions struct {
	Temperature   float64 `json:"temperature,omitempty"`
	NumPredict    int     `json:"num_predict,omitempty"`
	NumCtx        int     `json:"num_ctx,omitempty"`
	TopK          int     `json:"top_k,omitempty"`
	TopP          float64 `json:"top_p,omitempty"`
	Seed          int     `json:"seed,omitempty"`
	RepeatPenalty float64 `json:"repeat_penalty,omitempty"`
}

// Tool 请求体中携带的工具定义（JSON Schema）
type Tool struct {
	Type     string       `json:"type"`
	Function FunctionTool `json:"function"`
}

// FunctionTool 工具函数描述
type FunctionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatRequest 聊天请求体
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Tools    []Tool        `json:"tools,omitempty"`
	Options  *ChatOptions  `json:"options,omitempty"`
	Format   string        `json:"format,omitempty"` // 如 json
}

// ChatResponse 非流式响应
type ChatResponse struct {
	Model           string     `json:"model"`
	Message         ChatMessage `json:"message"`
	Done            bool        `json:"done"`
	EvalCount       int         `json:"eval_count"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	TotalDuration   int64       `json:"total_duration"`
}

// ChatChunk NDJSON 流式响单行
type ChatChunk struct {
	Message         ChatMessage `json:"message"`
	Done            bool        `json:"done"`
	EvalCount       int         `json:"eval_count"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

// ModelInfo 本地模型信息
type ModelInfo struct {
	Name       string    `json:"name"`
	Model      string    `json:"model"`
	ModifiedAt time.Time `json:"modified_at"`
	Size       int64     `json:"size"`
	Digest     string    `json:"digest"`
	Details    struct {
		Family         string   `json:"family"`
		ParameterSize  string   `json:"parameter_size"`
		Quantization   string   `json:"quantization_level"`
	} `json:"details"`
}

// Chat 非流式对话
func (c *Client) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	var out ChatResponse
	if err := c.doJSON(ctx, http.MethodPost, chatPath, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChatStream 流式对话，返回逐块（NDJSON) 通道；读取完自动关闭
func (c *Client) ChatStream(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpCtx, cancel := context.WithCancel(ctx)
	hreq, err := http.NewRequestWithContext(httpCtx, http.MethodPost, c.baseURL+chatPath, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(hreq)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("ollama chat 状态码 %d: %s", resp.StatusCode, truncate(string(b), 500))
	}

	ch := make(chan ChatChunk, 16)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		defer cancel()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var chunk ChatChunk
			if err := json.Unmarshal([]byte(line), &chunk); err != nil {
				continue
			}
			select {
			case ch <- chunk:
			case <-ctx.Done():
				return
			}
			if chunk.Done {
				return
			}
		}
	}()
	return ch, nil
}

// GenerateEmbedding 生成文本向量
func (c *Client) GenerateEmbedding(ctx context.Context, model, text string) ([]float32, error) {
	req := map[string]interface{}{"model": model, "prompt": text}
	var out struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := c.doJSON(ctx, http.MethodPost, embedPath, req, &out); err != nil {
		return nil, err
	}
	if len(out.Embedding) == 0 {
		return nil, errors.New("Ollama 返回空向量")
	}
	return out.Embedding, nil
}

// ListModels 列出本地可用模型
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var out struct {
		Models []ModelInfo `json:"models"`
	}
	if err := c.doJSON(ctx, http.MethodGet, tagsPath, nil, &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

// Ping 检测 Ollama 是否在线
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.ListModels(ctx)
	return err
}

// doJSON 请求并解析 JSON 响应
func (c *Client) doJSON(ctx context.Context, method, path string, body, out interface{}) error {
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
	resp, err := c.httpClient.Do(hreq)
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}