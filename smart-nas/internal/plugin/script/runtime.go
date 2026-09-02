// Package script 用户脚本运行时（Goja JS 引擎接入预留）。
//
// 说明：文档选用 goja 实现 JS 脚本插件。当前离线环境 goja 的传递依赖
// 不完整无法编译，故此处先落实现与接口（Runner），并默认引擎返回
// "未启用"错误；待依赖可用时实现 GojaRunner 并 SetRunner 即可启用，
// 上层代码无需改动。
package script

import (
	"context"
	"errors"
	"sync"
)

// Script 用户脚本
type Script struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Code    string `json:"code"`
	Enabled bool   `json:"enabled"`
	Event   string `json:"event"` // 监听的事件名
}

// Runner 脚本执行器抽象（goja 等引擎实现该接口）
type Runner interface {
	// Execute 以事件数据为上下文执行脚本
	Execute(ctx context.Context, script *Script, eventData map[string]interface{}) (interface{}, error)
}

// ErrEngineUnavailable 脚本引擎未启用
var ErrEngineUnavailable = errors.New("脚本引擎未启用（接入 goja 后可用）")

// Runtime 脚本运行时管理
type Runtime struct {
	mu      sync.Mutex
	scripts map[string]*Script
	runner  Runner
}

// NewRuntime 创建脚本运行时
func NewRuntime() *Runtime {
	return &Runtime{scripts: make(map[string]*Script)}
}

// SetRunner 注入脚本执行器（如 goja 引擎实现）
func (r *Runtime) SetRunner(runner Runner) {
	r.mu.Lock()
	r.runner = runner
	r.mu.Unlock()
}

// LoadScript 加载脚本
func (r *Runtime) LoadScript(s *Script) {
	if s == nil || s.ID == "" {
		return
	}
	r.mu.Lock()
	r.scripts[s.ID] = s
	r.mu.Unlock()
}

// UnloadScript 卸载脚本
func (r *Runtime) UnloadScript(id string) {
	r.mu.Lock()
	delete(r.scripts, id)
	r.mu.Unlock()
}

// Scripts 脚本列表
func (r *Runtime) Scripts() []*Script {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Script
	for _, s := range r.scripts {
		cp := *s
		out = append(out, &cp)
	}
	return out
}

// ForEvent 返回监听指定事件的已启用脚本
func (r *Runtime) ForEvent(event string) []*Script {
	var out []*Script
	for _, s := range r.Scripts() {
		if s.Enabled && s.Event == event {
			out = append(out, s)
		}
	}
	return out
}

// Execute 执行脚本（无引擎时返回 ErrEngineUnavailable）
func (r *Runtime) Execute(ctx context.Context, s *Script, eventData map[string]interface{}) (interface{}, error) {
	r.mu.Lock()
	runner := r.runner
	r.mu.Unlock()
	if runner == nil {
		return nil, ErrEngineUnavailable
	}
	return runner.Execute(ctx, s, eventData)
}