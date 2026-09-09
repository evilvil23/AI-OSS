// Package eino 字节 Eino 框架与 Ollama 的适配层（v0.21 M3）。
//
// 将现有 Ollama REST 客户端封装为 Eino ChatModel 组件（实现
// model.ToolCallingChatModel 接口），工具走 Eino Tool 协议（tool.InvokableTool
// + compose.ToolsNode）。此改造为内部实现替换，对外 AI 服务 API 不变。
package eino

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"

	"smart-nas/internal/ai/ollama"
)

// toOllamaMessages 将 Eino 消息列表转为 Ollama 请求消息
func toOllamaMessages(msgs []*schema.Message) []ollama.ChatMessage {
	out := make([]ollama.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		om := ollama.ChatMessage{Role: string(m.Role), Content: m.Content}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, ollama.ToolCall{
				Function: ollama.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
			})
		}
		out = append(out, om)
	}
	return out
}

// fromOllamaMessage 将 Ollama 非流式响应消息转为 Eino 消息
func fromOllamaMessage(m ollama.ChatMessage) *schema.Message {
	msg := &schema.Message{Role: schema.RoleType(m.Role), Content: m.Content}
	for i, tc := range m.ToolCalls {
		idx := i
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			Index:    &idx,
			ID:       fmt.Sprintf("call_%d", i),
			Type:     "function",
			Function: schema.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	return msg
}

// chunkToMessage 将 Ollama 流式单块转为 Eino 增量消息；
// tool_calls 按块内出现顺序分配 Index（调用方按 Index 聚合片段）。
func chunkToMessage(c ollama.ChatChunk) (*schema.Message, error) {
	msg := &schema.Message{Role: schema.Assistant, Content: c.Message.Content}
	for i, tc := range c.Message.ToolCalls {
		idx := i
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			Index:    &idx,
			ID:       fmt.Sprintf("call_%d", i),
			Type:     "function",
			Function: schema.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	return msg, nil
}

// MergeToolCalls 按 Index 聚合流式工具调用片段（Name 取首个非空，Arguments 追加）。
// 返回聚合后的完整工具调用列表。
func MergeToolCalls(agg map[int]*schema.ToolCall, chunk []schema.ToolCall) []schema.ToolCall {
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
	out := make([]schema.ToolCall, 0, len(agg))
	for i := 0; i < len(agg); i++ {
		if tc, ok := agg[i]; ok {
			out = append(out, *tc)
		}
	}
	return out
}

// toolInfoToOllama 将 Eino ToolInfo 转为 Ollama 请求携带的工具定义。
// 适配器统一以 JSON Schema 形式构造 ToolInfo（见 tools.go），此处从
// MarshalJSON 结果中提取 json_schema 作为 parameters。
func toolInfoToOllama(t *schema.ToolInfo) (ollama.Tool, error) {
	data, err := t.MarshalJSON()
	if err != nil {
		return ollama.Tool{}, err
	}
	var raw struct {
		Name       string          `json:"name"`
		Desc       string          `json:"desc"`
		JSONSchema json.RawMessage `json:"json_schema"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ollama.Tool{}, err
	}
	params := raw.JSONSchema
	if len(params) == 0 {
		params = json.RawMessage(`{"type":"object"}`)
	}
	return ollama.Tool{
		Type: "function",
		Function: ollama.FunctionTool{
			Name:        raw.Name,
			Description: raw.Desc,
			Parameters:  params,
		},
	}, nil
}
