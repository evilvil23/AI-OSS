// Package tools 注册并执行 AI Function Calling 工具。
//
// 工具可由主程序注册，也支持插件通过 SetDeviceProvider 注入设备能力。
// 执行时从 context 中获取当前用户 ID（见 WithUserID / UserIDFrom）。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"smart-nas/internal/ai/ollama"
	"smart-nas/internal/storage"
	"smart-nas/internal/util"
	"smart-nas/pkg/logger"
)

type ctxKey int

const userIDKey ctxKey = 1

// WithUserID 将用户 ID 注入上下文（由聊天服务调用前注入）
func WithUserID(ctx context.Context, uid uint) context.Context {
	return context.WithValue(ctx, userIDKey, uid)
}

// UserIDFrom 从上下文取用户 ID
func UserIDFrom(ctx context.Context) (uint, bool) {
	uid, ok := ctx.Value(userIDKey).(uint)
	return uid, ok
}

// Tool 工具接口
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// DeviceProvider 设备控制能力（由 internal/iot.Service 注入）
type DeviceProvider interface {
	Control(ctx context.Context, deviceID, action string, params map[string]interface{}) (string, error)
	Status(ctx context.Context, deviceID string) (string, error)
	List(ctx context.Context, room, typ string) (string, error)
	CreateAutomation(ctx context.Context, name string, trigger, actions interface{}) (string, error)
}

// Registry 工具注册表
type Registry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	storage *storage.Service
	device  DeviceProvider
}

// NewRegistry 创建工具注册表
func NewRegistry(storageSvc *storage.Service) *Registry {
	return &Registry{
		tools:   make(map[string]Tool),
		storage: storageSvc,
	}
}

// Register 注册一个或多个工具
func (r *Registry) Register(ts ...Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range ts {
		r.tools[t.Name()] = t
	}
}

// SetDeviceProvider 注入设备控制提供者（由 server 装配）
func (r *Registry) SetDeviceProvider(p DeviceProvider) {
	r.device = p
}

// Execute 执行指定工具，返回字符串结果
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("未知工具: %s", name)
	}
	out, err := t.Execute(ctx, args)
	if err != nil {
		logger.Warn("工具执行失败", "tool", name, "error", err)
		return "", err
	}
	logger.Info("工具执行", "tool", name)
	return out, nil
}

// ExecuteParallel 并行执行多个工具调用，返回 map[name]结果
func (r *Registry) ExecuteParallel(ctx context.Context, calls []ollama.ToolCall) map[string]any {
	results := make(map[string]any, len(calls))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, call := range calls {
		name := call.Function.Name
		args := json.RawMessage(call.Function.Arguments)
		wg.Add(1)
		go func(name string, args json.RawMessage) {
			defer wg.Done()
			out, err := r.Execute(ctx, name, args)
			mu.Lock()
			if err != nil {
				results[name] = map[string]any{"error": err.Error()}
			} else {
				results[name] = json.RawMessage(out)
			}
			mu.Unlock()
		}(name, args)
	}
	wg.Wait()
	return results
}

// ToolSchemas 返回供 Ollama 请求携带的工具定义（JSON 数组元素）
func (r *Registry) ToolSchemas() []json.RawMessage {
	r.mu.RLock()
	defer r.mu.RUnlock()
	schemas := make([]json.RawMessage, 0, len(r.tools))
	for _, t := range r.tools {
		tool := ollama.Tool{
			Type: "function",
			Function: ollama.FunctionTool{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		}
		data, err := json.Marshal(tool)
		if err == nil {
			schemas = append(schemas, data)
		}
	}
	return schemas
}

// Names 工具名列表
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return util.Keys(r.tools)
}

// ---- 辅助 ----

// objectSchema 快速构造 object 类型 JSON Schema
func objectSchema(properties map[string]property, required []string) json.RawMessage {
	s := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	data, _ := json.Marshal(s)
	return data
}

type property = map[string]string

func strProperty(desc string) property { return property{"type": "string", "description": desc} }
func intProperty(desc string) property { return property{"type": "integer", "description": desc} }
func boolProperty(desc string) property { return property{"type": "boolean", "description": desc} }

// argsTo 将 raw args 解析为指定结构与错误
func argsTo(t Tool, args json.RawMessage, out interface{}) error {
	if len(args) == 0 {
		return fmt.Errorf("工具 %s 缺少参数", t.Name())
	}
	return json.Unmarshal(args, out)
}

func jsonString(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}