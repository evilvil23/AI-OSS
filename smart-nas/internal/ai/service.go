// Package ai AI 能力服务：对话（工具循环）、RAG 检索、对话管理。
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"smart-nas/internal/ai/conversation"
	"smart-nas/internal/ai/ollama"
	"smart-nas/internal/ai/rag"
	"smart-nas/internal/ai/tools"
	"smart-nas/internal/config"
	"smart-nas/pkg/logger"
)

// 系统提示词（对应文档 §2.5.2）
const systemPrompt = `你是一个智能家庭管家，运行在家庭 NAS 上。你的职责包括：
1. 文件管理：帮助用户查找、整理、总结文件。
2. 设备控制：管理智能家居设备。
3. 信息查询：回答日常问题，利用本地知识库。
4. 场景联动：根据用户描述创建自动化场景。

约束：
- 所有操作必须在用户授权范围内执行。
- 不确定时主动询问。
- 回答简洁明了，符合中文表达习惯。
- 涉及危险操作时需二次确认。`

// 最大工具调用轮数，防止无限循环
const maxToolRounds = 3

// StreamEvent 流式对话事件
type StreamEvent struct {
	Delta   string `json:"delta,omitempty"`
	Content string `json:"content,omitempty"`
	Done    bool   `json:"done"`
	Error   string `json:"error,omitempty"`
}

// Service AI 服务
type Service struct {
	cfg       config.AIConfig
	client    *ollama.Client
	convs     *conversation.Manager
	tools     *tools.Registry
	retriever *rag.Retriever
	indexer   *rag.Indexer
}

// NewService 创建 AI 服务
func NewService(cfg config.AIConfig, client *ollama.Client, convs *conversation.Manager,
	toolsReg *tools.Registry, retriever *rag.Retriever, indexer *rag.Indexer) *Service {
	return &Service{cfg: cfg, client: client, convs: convs, tools: toolsReg, retriever: retriever, indexer: indexer}
}

// ---- 模型与对话管理 ----

// ListModels 列出本地 Ollama 模型
func (s *Service) ListModels(ctx context.Context) ([]ollama.ModelInfo, error) {
	return s.client.ListModels(ctx)
}

// CreateConversation 创建会话
func (s *Service) CreateConversation(userID uint, title string) *conversation.Conversation {
	return s.convs.Create(userID, title)
}

// ListConversations 会话列表
func (s *Service) ListConversations(userID uint) []*conversation.Conversation {
	return s.convs.List(userID)
}

// GetConversation 会话详情
func (s *Service) GetConversation(id string) (*conversation.Conversation, bool) {
	return s.convs.Get(id)
}

// DeleteConversation 删除会话
func (s *Service) DeleteConversation(id string) {
	s.convs.Delete(id)
}

// ---- 对话 ----

// Chat 非流式对话（含工具调用循环与 RAG 上下文注入）
func (s *Service) Chat(ctx context.Context, userID uint, convID, content string) (string, error) {
	if content == "" {
		return "", errors.New("消息不能为空")
	}
	if convID == "" {
		convID = s.convs.Create(userID, truncateTitle(content)).ID
	}
	s.convs.Append(convID, "user", content, nil)
	ctx = tools.WithUserID(ctx, userID)

	messages := s.buildMessages(ctx, userID, convID)
	resp, err := s.chatWithTools(ctx, userID, messages)
	if err != nil {
		return "", err
	}
	s.convs.Append(convID, "assistant", resp.Message.Content, nil)
	return resp.Message.Content, nil
}

// StreamChat 流式对话；返回事件通道，读取完自动关闭
func (s *Service) StreamChat(ctx context.Context, userID uint, convID, content string) (<-chan StreamEvent, error) {
	if content == "" {
		return nil, errors.New("消息不能为空")
	}
	if convID == "" {
		convID = s.convs.Create(userID, truncateTitle(content)).ID
	}
	s.convs.Append(convID, "user", content, nil)
	ctx = tools.WithUserID(ctx, userID)

	messages := s.buildMessages(ctx, userID, convID)
	events := make(chan StreamEvent, 32)
	go func() {
		defer close(events)
		final, err := s.streamWithTools(ctx, userID, messages, events)
		if err != nil {
			events <- StreamEvent{Error: err.Error(), Done: true}
			return
		}
		s.convs.Append(convID, "assistant", final, nil)
		events <- StreamEvent{Content: final, Done: true}
	}()
	return events, nil
}

// buildMessages 组装 Ollama 消息：system（含 RAG 上下文）+ 历史
func (s *Service) buildMessages(ctx context.Context, userID uint, convID string) []ollama.ChatMessage {
	sys := systemPrompt
	if s.cfg.RAG.Enabled && s.retriever != nil {
		last, _ := lastUserMessage(s.convs, convID)
		if last != "" {
			if chunks, err := s.retriever.Search(ctx, last, userID, 0); err == nil && len(chunks) > 0 {
				sys = s.retriever.FormatContext(chunks, last) + "\n\n" + sys
			}
		}
	}
	messages := []ollama.ChatMessage{{Role: "system", Content: sys}}
	messages = append(messages, s.convs.History(convID)...)
	return messages
}

// chatWithTools 非流式工具循环
func (s *Service) chatWithTools(ctx context.Context, userID uint, messages []ollama.ChatMessage) (*ollama.ChatResponse, error) {
	current := messages
	for round := 0; round < maxToolRounds; round++ {
		req := &ollama.ChatRequest{
			Model:    s.cfg.DefaultModel,
			Messages: current,
			Tools:    s.ollamaTools(),
			Options:  s.chatOptions(),
		}
		resp, err := s.client.Chat(ctx, req)
		if err != nil {
			return nil, err
		}
		if len(resp.Message.ToolCalls) == 0 {
			return resp, nil
		}
		current = append(current, resp.Message)
		results := s.tools.ExecuteParallel(ctx, resp.Message.ToolCalls)
		current = append(current, toolResultMessages(resp.Message.ToolCalls, results)...)
	}
	return nil, errors.New("工具调用次数过多，已停止")
}

// streamWithTools 流式工具循环：每个工具轮均流式输出文本
func (s *Service) streamWithTools(ctx context.Context, userID uint, messages []ollama.ChatMessage, events chan<- StreamEvent) (string, error) {
	current := messages
	for round := 0; round < maxToolRounds; round++ {
		req := &ollama.ChatRequest{
			Model:    s.cfg.DefaultModel,
			Messages: current,
			Tools:    s.ollamaTools(),
			Options:  s.chatOptions(),
		}
		chunks, err := s.client.ChatStream(ctx, req)
		if err != nil {
			return "", err
		}
		var content string
		var toolCalls []ollama.ToolCall
		for chunk := range chunks {
			if chunk.Done {
				break
			}
			if chunk.Message.Content != "" {
				content += chunk.Message.Content
				select {
				case events <- StreamEvent{Delta: chunk.Message.Content}:
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			toolCalls = append(toolCalls, chunk.Message.ToolCalls...)
		}
		if len(toolCalls) == 0 {
			return content, nil
		}
		// 工具调用轮：把已输出的中间文本消息并入历史，继续下一轮
		current = append(current, ollama.ChatMessage{Role: "assistant", Content: content, ToolCalls: toolCalls})
		results := s.tools.ExecuteParallel(ctx, toolCalls)
		current = append(current, toolResultMessages(toolCalls, results)...)
	}
	return "", errors.New("工具调用次数过多，已停止")
}

// ollamaTools 将注册表工具转为 Ollama 请求格式
func (s *Service) ollamaTools() []ollama.Tool {
	schemas := s.tools.ToolSchemas()
	out := make([]ollama.Tool, 0, len(schemas))
	for _, raw := range schemas {
		var t ollama.Tool
		if err := json.Unmarshal(raw, &t); err == nil {
			out = append(out, t)
		}
	}
	return out
}

func (s *Service) chatOptions() *ollama.ChatOptions {
	return &ollama.ChatOptions{
		Temperature: s.cfg.Temperature,
		NumPredict:  s.cfg.ConversationMaxTokens,
	}
}

// ---- RAG ----

// IndexFile 异步索引文件（供上传完成事件调用）
func (s *Service) IndexFile(fileID, userID uint) {
	if !s.cfg.RAG.Enabled || s.indexer == nil {
		return
	}
	if err := s.indexer.IndexFile(context.Background(), fileID, userID); err != nil {
		logger.Warn("RAG 文件索引失败", "file_id", fileID, "error", err)
	}
}

// DeleteFileIndex 删除文件索引（供删除事件调用，后台执行）
func (s *Service) DeleteFileIndex(fileID uint) {
	if s.indexer == nil {
		return
	}
	if err := s.indexer.DeleteFile(context.Background(), fileID); err != nil {
		logger.Warn("RAG 索引清理失败", "file_id", fileID, "error", err)
	}
}

// IndexStatus 索引状态
func (s *Service) IndexStatus() map[string]interface{} {
	if s.indexer == nil {
		return map[string]interface{}{"enabled": false}
	}
	return map[string]interface{}{
		"enabled": s.cfg.RAG.Enabled,
		"chunks":  s.indexer.Count(),
		"store_type": s.cfg.RAG.StoreType,
	}
}

// ---- 辅助 ----

func lastUserMessage(convs *conversation.Manager, convID string) (string, error) {
	c, ok := convs.Get(convID)
	if !ok {
		return "", errors.New("会话不存在")
	}
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "user" {
			return c.Messages[i].Content, nil
		}
	}
	return "", nil
}

// toolResultMessages 将工具执行结果转为 role=tool 消息
func toolResultMessages(calls []ollama.ToolCall, results map[string]any) []ollama.ChatMessage {
	var out []ollama.ChatMessage
	for _, call := range calls {
		name := call.Function.Name
		var content string
		if v, ok := results[name]; ok {
			switch t := v.(type) {
			case json.RawMessage:
				content = string(t)
			case string:
				content = t
			default:
				b, _ := json.Marshal(v)
				content = string(b)
			}
		} else {
			content = "{}"
		}
		out = append(out, ollama.ChatMessage{Role: "tool", Content: fmt.Sprintf("工具 %s 结果: %s", name, content)})
	}
	return out
}

func truncateTitle(s string) string {
	r := []rune(s)
	if len(r) > 20 {
		return string(r[:20]) + "…"
	}
	return s
}