// Package ai AI 能力服务：对话（Eino 工具循环）、RAG 检索、对话管理。
//
// v0.21 起内部实现切换为字节 Eino 框架（ChatModel 组件 + Tool 协议 +
// ToolsNode 执行），对外 API（Chat / StreamChat / 会话管理等）保持不变。
package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"smart-nas/internal/ai/conversation"
	"smart-nas/internal/ai/eino"
	"smart-nas/internal/ai/hardware"
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
	ConversationID string `json:"conversation_id,omitempty"` // 新建会话时回传（v0.23 前端续聊需要）
	Delta          string `json:"delta,omitempty"`
	Content        string `json:"content,omitempty"`
	Done           bool   `json:"done"`
	Error          string `json:"error,omitempty"`
}

// RagRetriever RAG 检索抽象（本地 chromem 实现 / 双机模式远程主服务实现）
type RagRetriever interface {
	Search(ctx context.Context, query string, userID uint, topK int) ([]rag.Chunk, error)
}

// Service AI 服务
type Service struct {
	mu     sync.RWMutex // 保护 cfg / preset / deployState（热重载与部署切换并发安全）
	cfg    config.AIConfig
	preset hardware.Preset

	client    *ollama.Client
	convs     *conversation.Manager
	registry  *tools.Registry
	retriever RagRetriever
	indexer   *rag.Indexer

	// Eino 组件（M3）
	einoTools []tool.BaseTool
	toolNode  *compose.ToolsNode
	toolInfosCache []*schema.ToolInfo // 工具定义缓存（启动时构建）

	deploy *deployState // M6 部署模式状态（single / dual）
}

// Options 服务装配参数
type Options struct {
	Config    config.AIConfig
	Preset    hardware.Preset   // 硬件调优预设（M2；零值时回退 config）
	Client    *ollama.Client
	Convs     *conversation.Manager
	Registry  *tools.Registry
	Retriever RagRetriever      // 本地实现或远程实现（辅助机）
	Indexer   *rag.Indexer
}

// NewService 创建 AI 服务并初始化 Eino 组件
func NewService(opt Options) (*Service, error) {
	if opt.Preset.Model == "" {
		opt.Preset.Model = opt.Config.DefaultModel
	}
	s := &Service{
		cfg:       opt.Config,
		preset:    opt.Preset,
		client:    opt.Client,
		convs:     opt.Convs,
		registry:  opt.Registry,
		retriever: opt.Retriever,
		indexer:   opt.Indexer,
	}
	// Eino 工具适配与 ToolsNode（工具执行走 Eino Tool 协议）
	s.einoTools = eino.AdaptTools(opt.Registry)
	if len(s.einoTools) > 0 {
		tn, err := eino.NewToolsNode(context.Background(), s.einoTools)
		if err != nil {
			return nil, err
		}
		s.toolNode = tn
		infos := make([]*schema.ToolInfo, 0, len(s.einoTools))
		for _, t := range s.einoTools {
			info, ierr := t.Info(context.Background())
			if ierr != nil {
				return nil, ierr
			}
			infos = append(infos, info)
		}
		s.toolInfosCache = infos
	}
	s.deploy = newDeployState(opt.Config)
	return s, nil
}

// ---- 模型与对话管理 ----

// ListModels 列出本地 Ollama 模型
func (s *Service) ListModels(ctx context.Context) ([]ollama.ModelInfo, error) {
	return s.client.ListModels(ctx)
}

// Client 暴露 Ollama 客户端（供管理 API 使用）
func (s *Service) Client() *ollama.Client { return s.client }

// Preset 当前硬件调优预设
func (s *Service) Preset() hardware.Preset {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.preset
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
	resp, err := s.chatWithTools(ctx, messages)
	if err != nil {
		return "", err
	}
	s.convs.Append(convID, "assistant", resp.Content, nil)
	return resp.Content, nil
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
            events <- StreamEvent{ConversationID: convID} // 首帧回传会话 ID（新建时前端续聊）
            final, err := s.streamWithTools(ctx, messages, events)
		if err != nil {
			events <- StreamEvent{Error: err.Error(), Done: true}
			return
		}
		s.convs.Append(convID, "assistant", final, nil)
		events <- StreamEvent{Content: final, Done: true}
	}()
	return events, nil
}

// buildMessages 组装 Eino 消息：system（含 RAG 上下文）+ 历史
func (s *Service) buildMessages(ctx context.Context, userID uint, convID string) []*schema.Message {
	sys := systemPrompt
	s.mu.RLock()
	ragEnabled := s.cfg.RAG.Enabled
	s.mu.RUnlock()
	if ragEnabled && s.retriever != nil {
		last, _ := lastUserMessage(s.convs, convID)
		if last != "" {
			if chunks, err := s.retriever.Search(ctx, last, userID, 0); err == nil && len(chunks) > 0 {
				sys = formatRAGContext(chunks, last) + "\n\n" + sys
			}
		}
	}
	messages := []*schema.Message{{Role: schema.System, Content: sys}}
	for _, m := range s.convs.History(convID) {
		messages = append(messages, &schema.Message{
			Role:      schema.RoleType(m.Role),
			Content:   m.Content,
			ToolCalls: fromOllamaToolCalls(m.ToolCalls),
		})
	}
	return messages
}

// fromOllamaToolCalls 历史消息中的工具调用转为 Eino 格式
func fromOllamaToolCalls(calls []ollama.ToolCall) []schema.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, 0, len(calls))
	for i, tc := range calls {
		idx := i
		out = append(out, schema.ToolCall{
			Index:    &idx,
			ID:       "call_hist_" + tc.Function.Name,
			Type:     "function",
			Function: schema.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	return out
}

// chatWithTools 非流式工具循环（Eino ChatModel + ToolsNode）
func (s *Service) chatWithTools(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
	cmw, err := s.boundChatModel()
	if err != nil {
		return nil, err
	}
	current := messages
	for round := 0; round < maxToolRounds; round++ {
		msg, err := cmw.Generate(ctx, current)
		if err != nil {
			return nil, err
		}
		if len(msg.ToolCalls) == 0 || s.toolNode == nil {
			return msg, nil
		}
		current = append(current, msg)
		toolMsgs, err := s.toolNode.Invoke(ctx, msg)
		if err != nil {
			return nil, err
		}
		current = append(current, toolMsgs...)
	}
	return nil, errors.New("工具调用次数过多，已停止")
}

// streamWithTools 流式工具循环：每个工具轮均流式输出文本（Eino Stream + ToolsNode）
func (s *Service) streamWithTools(ctx context.Context, messages []*schema.Message, events chan<- StreamEvent) (string, error) {
	cmw, err := s.boundChatModel()
	if err != nil {
		return "", err
	}
	current := messages
	for round := 0; round < maxToolRounds; round++ {
		sr, err := cmw.Stream(ctx, current)
		if err != nil {
			return "", err
		}
		var content string
		agg := map[int]*schema.ToolCall{}
		for {
			chunk, rerr := sr.Recv()
			if errors.Is(rerr, io.EOF) {
				break
			}
			if rerr != nil {
				sr.Close()
				return "", rerr
			}
			if chunk.Content != "" {
				content += chunk.Content
				select {
				case events <- StreamEvent{Delta: chunk.Content}:
				case <-ctx.Done():
					sr.Close()
					return "", ctx.Err()
				}
			}
			agg = mergeToolCallChunks(agg, chunk.ToolCalls)
		}
		sr.Close()
		toolCalls := eino.MergeToolCalls(agg, nil)
		if len(toolCalls) == 0 || s.toolNode == nil {
			return content, nil
		}
		// 工具调用轮：把已输出的中间文本消息并入历史，继续下一轮
		asstMsg := &schema.Message{Role: schema.Assistant, Content: content, ToolCalls: toolCalls}
		current = append(current, asstMsg)
		toolMsgs, err := s.toolNode.Invoke(ctx, asstMsg)
		if err != nil {
			return "", err
		}
		current = append(current, toolMsgs...)
	}
	return "", errors.New("工具调用次数过多，已停止")
}

// mergeToolCallChunks 增量合并流式工具调用片段
func mergeToolCallChunks(agg map[int]*schema.ToolCall, chunk []schema.ToolCall) map[int]*schema.ToolCall {
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

// boundChatModel 构造绑定工具的 ChatModel（按当前生效模型与部署状态）。
// 每次请求构造轻量实例，保证热重载 / 部署切换后的请求使用最新配置。
func (s *Service) boundChatModel() (model.ToolCallingChatModel, error) {
	s.mu.RLock()
	temperature := s.cfg.Temperature
	maxTokens := s.cfg.ConversationMaxTokens
	modelName := s.preset.Model
	keepAlive := s.preset.KeepAlive
	remoteActive := s.deploy != nil && s.deploy.remoteActive
	client := s.client
	s.mu.RUnlock()
	if remoteActive {
		if dc := s.deploy.remoteClient; dc != nil {
			client = dc
		}
		if rm := s.deploy.remoteModel; rm != "" {
			modelName = rm
		}
	}
	cm := eino.NewChatModel(client, modelName, temperature, maxTokens, keepAlive)
	// 工具定义：从 Eino 适配器取 Info（缓存于注册时构建）
	infos, err := s.toolInfos()
	if err != nil {
		return nil, err
	}
	return cm.WithTools(infos)
}

// toolInfos 缓存的 Eino 工具定义
func (s *Service) toolInfos() ([]*schema.ToolInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.toolInfosCache) == 0 {
		infos := make([]*schema.ToolInfo, 0, len(s.einoTools))
		for _, t := range s.einoTools {
			info, err := t.Info(context.Background())
			if err != nil {
				return nil, err
			}
			infos = append(infos, info)
		}
		return infos, nil
	}
	return s.toolInfosCache, nil
}

// ---- RAG ----

// IndexFile 异步索引文件（供上传完成事件调用）
func (s *Service) IndexFile(fileID, userID uint) {
	s.mu.RLock()
	enabled := s.cfg.RAG.Enabled
	s.mu.RUnlock()
	if !enabled || s.indexer == nil {
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
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	return map[string]interface{}{
		"enabled":    cfg.RAG.Enabled,
		"chunks":     s.indexer.Count(),
		"store_type": cfg.RAG.StoreType,
	}
}

// SearchRAG 对外检索接口（供 /api/ai/rag/search，双机模式辅助机经此查询主服务）
func (s *Service) SearchRAG(ctx context.Context, query string, userID uint, topK int) ([]rag.Chunk, error) {
	if s.retriever == nil {
		return nil, errors.New("RAG 未启用")
	}
	return s.retriever.Search(ctx, query, userID, topK)
}

// ReloadConfig 热重载回调：更新模型 / 调优 / 部署配置（config.toml 修改即时生效）
func (s *Service) ReloadConfig(cfg config.AIConfig, preset hardware.Preset) {
	s.mu.Lock()
	s.cfg = cfg
	if preset.Model != "" {
		s.preset = preset
	}
	s.mu.Unlock()
	if s.deploy != nil {
		s.deploy.UpdateConfig(cfg) // v0.23：远端地址 / 自动切换等运行期参数热更新
	}
	logger.Info("AI 配置已热重载", "model", s.Preset().Model, "deploy_mode", cfg.Deploy.Mode)
}

// Shutdown 优雅关闭：卸载模型释放显存 / 内存（进程停止由 Lifecycle 负责）
func (s *Service) Shutdown(ctx context.Context, model string) {
	s.unloadLocalModelByName(ctx, model)
}

func (s *Service) unloadLocalModelByName(ctx context.Context, model string) {
	if model == "" || s.client == nil {
		return
	}
	uctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.client.UnloadModel(uctx, model); err != nil {
		logger.Warn("卸载模型失败", "model", model, "error", err)
	} else {
		logger.Info("模型已卸载，显存 / 内存已释放", "model", model)
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

// formatRAGContext 将检索结果组装为系统提示上下文（本地与远程实现共用）
func formatRAGContext(chunks []rag.Chunk, userQuery string) string {
	if len(chunks) == 0 {
		return ""
	}
	var content string
	for i, c := range chunks {
		content += fmt.Sprintf("[%d] score=%.2f\n%s\n", i+1, c.Score, c.Content)
	}
	return fmt.Sprintf("以下是从用户文件中检索到的相关内容，请基于这些内容回答：\n%s\n---\n用户问题：%s", content, userQuery)
}

func truncateTitle(s string) string {
	r := []rune(s)
	if len(r) > 20 {
		return string(r[:20]) + "…"
	}
	return s
}
