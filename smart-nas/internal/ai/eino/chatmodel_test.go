// chatmodel_test.go Eino ChatModel 组件单元测试（httptest Mock Ollama）。
package eino

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"smart-nas/internal/ai/ollama"
)

// newMockOllama 启动 Mock Ollama：/api/chat 按 tools 有无返回工具调用或文本
func newMockOllama(t *testing.T, captured *map[string]interface{}) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if captured != nil {
			*captured = req
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		tools, _ := req["tools"].([]interface{})
		if len(tools) > 0 {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"model": "test-model",
				"message": map[string]interface{}{
					"role": "assistant",
					"tool_calls": []map[string]interface{}{
						{"function": map[string]interface{}{
							"name":      "list_files",
							"arguments": `{"path":"/docs"}`,
						}},
					},
				},
				"done": false,
			})
			return
		}
		if stream, _ := req["stream"].(bool); stream {
			for _, part := range []string{"你", "好"} {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"model":   "test-model",
					"message": map[string]interface{}{"role": "assistant", "content": part},
					"done":    false,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"model": "test-model", "message": map[string]interface{}{"role": "assistant", "content": ""},
				"done": true,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"model":   "test-model",
			"message": map[string]interface{}{"role": "assistant", "content": "你好，我是本地管家"},
			"done":    true,
		})
	})
	return httptest.NewServer(mux)
}

func TestGenerateConvertsMessages(t *testing.T) {
	captured := map[string]interface{}{}
	srv := newMockOllama(t, &captured)
	defer srv.Close()
	client := ollama.NewClient(srv.URL, 0)
	cm := NewChatModel(client, "test-model", 0.7, 512, "5m")

	resp, err := cm.Generate(context.Background(), []*schema.Message{
		{Role: schema.System, Content: "系统提示"},
		{Role: schema.User, Content: "在吗"},
	})
	if err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}
	if resp.Content != "你好，我是本地管家" {
		t.Fatalf("响应内容不符: %s", resp.Content)
	}
	// 校验请求转换
	if got, _ := captured["model"].(string); got != "test-model" {
		t.Fatalf("模型名不符: %v", captured["model"])
	}
	msgs, _ := captured["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("应携带 2 条消息，实际 %d", len(msgs))
	}
	first, _ := msgs[0].(map[string]interface{})
	if first["role"] != "system" || first["content"] != "系统提示" {
		t.Fatalf("system 消息转换不符: %v", first)
	}
	if ka, _ := captured["keep_alive"].(string); ka != "5m" {
		t.Fatalf("keep_alive 未透传: %v", captured["keep_alive"])
	}
}

func TestGenerateWithTools(t *testing.T) {
	captured := map[string]interface{}{}
	srv := newMockOllama(t, &captured)
	defer srv.Close()
	client := ollama.NewClient(srv.URL, 0)
	cm := NewChatModel(client, "test-model", 0.7, 512, "")

	cmw, err := cm.WithTools([]*schema.ToolInfo{{
		Name: "list_files",
		Desc: "列出文件",
		ParamsOneOf: mustJSONSchema(t, `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cmw.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "列出 /docs"}})
	if err != nil {
		t.Fatalf("带工具 Generate 失败: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "list_files" {
		t.Fatalf("工具调用转换不符: %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Function.Arguments != `{"path":"/docs"}` {
		t.Fatalf("参数不符: %s", resp.ToolCalls[0].Function.Arguments)
	}
	// 请求携带的 tools 定义
	tools, _ := captured["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("应携带 1 个工具定义: %v", captured["tools"])
	}
	t0, _ := tools[0].(map[string]interface{})
	fn, _ := t0["function"].(map[string]interface{})
	if fn["name"] != "list_files" || !strings.Contains(fmtJSON(fn["parameters"]), "required") {
		t.Fatalf("工具定义转换不符: %v", fn)
	}
}

func TestStreamAggregation(t *testing.T) {
	srv := newMockOllama(t, nil)
	defer srv.Close()
	client := ollama.NewClient(srv.URL, 0)
	cm := NewChatModel(client, "test-model", 0.7, 512, "")

	sr, err := cm.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "在吗"}})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	defer sr.Close()
	var full string
	for {
		chunk, rerr := sr.Recv()
		if errIsEOF(rerr) {
			break
		}
		if rerr != nil {
			t.Fatal(rerr)
		}
		full += chunk.Content
	}
	if full != "你好" {
		t.Fatalf("流式聚合不符: %q", full)
	}
}

func TestMergeToolCallsChunks(t *testing.T) {
	agg := map[int]*schema.ToolCall{}
	idx0, idx1 := 0, 1
	agg = mergeInto(agg, []schema.ToolCall{
		{Index: &idx0, Function: schema.FunctionCall{Name: "read_file", Arguments: `{"pa`}},
		{Index: &idx1, Function: schema.FunctionCall{Name: "list_files", Arguments: `{`}},
	})
	agg = mergeInto(agg, []schema.ToolCall{
		{Index: &idx0, Function: schema.FunctionCall{Arguments: `th":"/a.txt"}`}},
		{Index: &idx1, Function: schema.FunctionCall{Arguments: `}`}},
	})
	merged := MergeToolCalls(agg, nil)
	if len(merged) != 2 {
		t.Fatalf("应合并出 2 个调用: %d", len(merged))
	}
	if merged[0].Function.Name != "read_file" || merged[0].Function.Arguments != `{"path":"/a.txt"}` {
		t.Fatalf("片段合并不符: %+v", merged[0])
	}
	if merged[1].Function.Arguments != `{}` {
		t.Fatalf("片段合并不符: %+v", merged[1])
	}
}

// Options 覆盖：WithModel / WithTemperature 应生效
func TestOptionsOverride(t *testing.T) {
	captured := map[string]interface{}{}
	srv := newMockOllama(t, &captured)
	defer srv.Close()
	client := ollama.NewClient(srv.URL, 0)
	cm := NewChatModel(client, "default-model", 0.7, 512, "")

	temp := float32(0.2)
	_, err := cm.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		model.WithModel("override-model"), model.WithTemperature(temp), model.WithMaxTokens(128))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := captured["model"].(string); got != "override-model" {
		t.Fatalf("WithModel 未生效: %v", captured["model"])
	}
	opts, _ := captured["options"].(map[string]interface{})
	if tp, _ := opts["temperature"].(float64); tp < 0.19 || tp > 0.21 {
		t.Fatalf("temperature 覆盖不符: %v", opts["temperature"])
	}
	if opts["num_predict"] != float64(128) {
		t.Fatalf("num_predict 覆盖不符: %v", opts["num_predict"])
	}
}

func errIsEOF(err error) bool { return err != nil && err == io.EOF }

// ---- 测试辅助 ----

// mustJSONSchema 解析 JSON Schema 字符串为 Eino ParamsOneOf
func mustJSONSchema(t *testing.T, raw string) *schema.ParamsOneOf {
	t.Helper()
	var js jsonschema.Schema
	if err := json.Unmarshal([]byte(raw), &js); err != nil {
		t.Fatal(err)
	}
	return schema.NewParamsOneOfByJSONSchema(&js)
}

// fmtJSON 序列化为紧凑 JSON 字符串便于断言
func fmtJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// mergeInto 将流式工具调用片段合并进聚合器（与 service 层聚合逻辑一致）
func mergeInto(agg map[int]*schema.ToolCall, chunk []schema.ToolCall) map[int]*schema.ToolCall {
	for _, tc := range chunk {
		idx := 0
		if tc.Index != nil {
			idx = *tc.Index
		}
		cur, ok := agg[idx]
		if !ok {
			cp := tc
			agg[idx] = &cp
			continue
		}
		if tc.Function.Name != "" {
			cur.Function.Name = tc.Function.Name
		}
		cur.Function.Arguments += tc.Function.Arguments
	}
	return agg
}
