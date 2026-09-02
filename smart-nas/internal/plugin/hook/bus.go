// Package hook 进程内事件钩子总线。
//
// 允许插件/模块注册事件处理器，按优先级顺序触发，支持短路传播。
// 供插件管理器(Manager)实现 transport.HookEmitter 接口。
package hook

import (
	"context"
	"sync"
)

// Event 事件对象
type Event struct {
	Name     string                 `json:"name"`
	Payload  interface{}            `json:"payload,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// Result 处理器返回结果
type Result struct {
	StopPropagation bool                   // 终止后续处理器
	Modified        map[string]interface{} // 修改后的事件数据
	Data            interface{}            // 处理产生的数据
}

// Handler 事件处理器
type Handler func(ctx context.Context, event Event) (Result, error)

// handlerEntry 带优先级的处理器
type handlerEntry struct {
	name     string
	priority int
	handler  Handler
}

// Bus 事件总线
type Bus struct {
	mu       sync.RWMutex
	handlers map[string][]handlerEntry
}

// NewBus 创建事件总线
func NewBus() *Bus {
	return &Bus{handlers: make(map[string][]handlerEntry)}
}

// Register 注册事件处理器（priority 数值越小优先级越高）
func (b *Bus) Register(event, name string, priority int, h Handler) {
	if event == "" || h == nil {
		return
	}
	entry := handlerEntry{name: name, priority: priority, handler: h}
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.handlers[event]
	idx := len(list)
	for i, e := range list {
		if priority < e.priority {
			idx = i
			break
		}
	}
	list = append(list, handlerEntry{})
	copy(list[idx+1:], list[idx:])
	list[idx] = entry
	b.handlers[event] = list
}

// Unregister 移除指定处理器
func (b *Bus) Unregister(event, name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.handlers[event]
	out := list[:0]
	for _, e := range list {
		if e.name != name {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		delete(b.handlers, event)
		return
	}
	b.handlers[event] = out
}

// Emit 触发事件；按优先级执行所有处理器，直到 StopPropagation 或 context 取消
func (b *Bus) Emit(ctx context.Context, event Event) ([]Result, error) {
	b.mu.RLock()
	list := b.handlers[event.Name]
	// 拷贝避免执行期间注册修改
	handlers := make([]handlerEntry, len(list))
	copy(handlers, list)
	b.mu.RUnlock()

	var results []Result
	for _, e := range handlers {
		if ctx != nil && ctx.Err() != nil {
			return results, ctx.Err()
		}
		res, err := e.handler(ctx, event)
		if err != nil {
			return results, err
		}
		results = append(results, res)
		if res.Modified != nil {
			event.Payload = res.Modified
		}
		if res.StopPropagation {
			break
		}
	}
	return results, nil
}

// Names 某事件已注册的处理器名（调试用）
func (b *Bus) Names(event string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []string
	for _, e := range b.handlers[event] {
		out = append(out, e.name)
	}
	return out
}