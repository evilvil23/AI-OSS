// chatmodel.go Eino ChatModel 组件：封装 Ollama REST 客户端。
//
// 实现 model.ToolCallingChatModel（Generate / Stream / WithTools），
// WithTools 返回不可变副本，可安全并发共享。生成参数通过 Eino
// model.Option（WithTemperature / WithMaxTokens / WithModel）覆盖默认值。
package eino

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"smart-nas/internal/ai/ollama"
)

// 确保 ChatModel 实现 Eino 接口
var _ model.ToolCallingChatModel = (*ChatModel)(nil)

// ChatModel Ollama 的 Eino ChatModel 组件
type ChatModel struct {
	client      *ollama.Client
	model       string
	temperature float64
	maxTokens   int
	keepAlive   string
	tools       []*schema.ToolInfo
}

// NewChatModel 创建组件；model 为模型名，temperature/maxTokens 为默认生成参数，
// keepAlive 为模型驻留时长（空 = 服务端默认）
func NewChatModel(client *ollama.Client, modelName string, temperature float64, maxTokens int, keepAlive string) *ChatModel {
	return &ChatModel{
		client:      client,
		model:       modelName,
		temperature: temperature,
		maxTokens:   maxTokens,
		keepAlive:   keepAlive,
	}
}

// Generate 非流式生成（阻塞至完整响应）
func (c *ChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	req, err := c.buildRequest(input, opts)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return fromOllamaMessage(resp.Message), nil
}

// Stream 流式生成；返回增量消息流（调用方负责 Close 与聚合）
func (c *ChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	req, err := c.buildRequest(input, opts)
	if err != nil {
		return nil, err
	}
	chunks, err := c.client.ChatStream(ctx, req)
	if err != nil {
		return nil, err
	}
	sr, sw := schema.Pipe[*schema.Message](16)
	go func() {
		defer sw.Close()
		for chunk := range chunks {
			msg, cerr := chunkToMessage(chunk)
			if closed := sw.Send(msg, cerr); closed {
				return // 下游已关闭（Send 返回 true = 流已关闭）
			}
			if chunk.Done {
				return
			}
		}
	}()
	return sr, nil
}

// WithTools 绑定工具，返回不可变副本（Eino Tool 协议要求）
func (c *ChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	cp := *c
	cp.tools = tools
	return &cp, nil
}

// buildRequest 组装 Ollama 请求：默认参数 ← Eino Option 覆盖
func (c *ChatModel) buildRequest(input []*schema.Message, opts []model.Option) (*ollama.ChatRequest, error) {
	common := model.GetCommonOptions(&model.Options{}, opts...)
	modelName := c.model
	if common.Model != nil && *common.Model != "" {
		modelName = *common.Model
	}
	temp := c.temperature
	if common.Temperature != nil {
		temp = float64(*common.Temperature)
	}
	maxTok := c.maxTokens
	if common.MaxTokens != nil {
		maxTok = *common.MaxTokens
	}

	req := &ollama.ChatRequest{
		Model:    modelName,
		Messages: toOllamaMessages(input),
		Options: &ollama.ChatOptions{
			Temperature: temp,
			NumPredict:  maxTok,
		},
	}
	if c.keepAlive != "" {
		req.KeepAlive = ollama.KeepAlive(c.keepAlive)
	}
	for _, t := range c.tools {
		ot, err := toolInfoToOllama(t)
		if err != nil {
			return nil, err
		}
		req.Tools = append(req.Tools, ot)
	}
	return req, nil
}
