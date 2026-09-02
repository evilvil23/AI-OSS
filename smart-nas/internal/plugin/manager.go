// Package plugin 插件系统总控。
//
// 三级插件架构（对应文档 §2.9）：
//  1. Hook 钩子（进程内，事件总线，已实现）
//  2. goplugin 进程外插件（简化实现：清单 + 子进程）
//  3. Goja 脚本（预留 Runner 接口，默认引擎未启用）
//
// Manager 实现 transport.HookEmitter 接口，供 tus/任务等模块触发事件。
package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"smart-nas/internal/config"
	"smart-nas/internal/plugin/goplugin"
	"smart-nas/internal/plugin/hook"
	"smart-nas/internal/plugin/script"
	"smart-nas/pkg/logger"
)

// Manager 插件管理器
type Manager struct {
	cfg            config.PluginConfig
	hookBus        *hook.Bus
	scriptRuntime  *script.Runtime
	externalPlugin map[string]*goplugin.ExternalPlugin
	dataDir        string
	mu             sync.RWMutex
}

// NewManager 创建插件管理器；dataDir 为插件独立数据目录（当前未用，预留）
func NewManager(cfg config.PluginConfig, dataDir string) *Manager {
	return &Manager{
		cfg:            cfg,
		hookBus:        hook.NewBus(),
		scriptRuntime:  script.NewRuntime(),
		externalPlugin: make(map[string]*goplugin.ExternalPlugin),
		dataDir:        dataDir,
	}
}

// HookBus 事件总线（供注册 Hook 插件）
func (m *Manager) HookBus() *hook.Bus { return m.hookBus }

// ScriptRuntime 脚本运行时
func (m *Manager) ScriptRuntime() *script.Runtime { return m.scriptRuntime }

// RegisterHandler 注册 Hook 事件处理器
func (m *Manager) RegisterHandler(event, name string, priority int, h hook.Handler) {
	m.hookBus.Register(event, name, priority, h)
}

// LoadPlugins 扫描插件目录：加载 goplugin 清单与 scripts 目录脚本
func (m *Manager) LoadPlugins() error {
	if !m.cfg.Enabled {
		logger.Info("插件系统未启用")
		return nil
	}
	root := m.cfg.Dir
	if root == "" {
		return nil
	}
	// 1. 进程外插件：plugins/<id>/plugin.toml
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	loaded := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "plugin.toml")); err != nil {
			continue
		}
		p, err := goplugin.Load(dir)
		if err != nil {
			logger.Warn("跳过无效插件", "dir", e.Name(), "error", err)
			continue
		}
		m.mu.Lock()
		m.externalPlugin[p.ID()] = p
		m.mu.Unlock()
		loaded++
		logger.Info("已加载进程外插件", "id", p.ID(), "enabled", p.Enabled())
	}
	// 2. 脚本插件：plugins/scripts/*.js（或根目录 scripts/）
	scriptDirs := []string{filepath.Join(root, "scripts"), filepath.Join(root, "js")}
	for _, sd := range scriptDirs {
		if err := m.loadScripts(sd); err != nil {
			logger.Warn("脚本目录加载失败", "dir", sd, "error", err)
		}
	}
	logger.Info("插件加载完成", "goplugin", loaded, "scripts", len(m.scriptRuntime.Scripts()))
	return nil
}

func (m *Manager) loadScripts(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		code, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".js")
		m.scriptRuntime.LoadScript(&script.Script{
			ID: id, Name: id, Code: string(code), Enabled: true,
		})
		logger.Info("已加载脚本插件", "id", id)
	}
	return nil
}

// Emit 触发事件：Hook 顺序执行 → 进程外插件并行触发 → 脚本执行
// 实现 transport.HookEmitter 接口
func (m *Manager) Emit(ctx context.Context, eventName string, payload interface{}) error {
	// 1. Hook 事件总线
	if _, err := m.hookBus.Emit(ctx, hook.Event{Name: eventName, Payload: payload}); err != nil {
		logger.Warn("Hook 执行异常", "event", eventName, "error", err)
		return err
	}
	// 2. 进程外插件（并行触发）
	var wg sync.WaitGroup
	m.mu.RLock()
	plugins := make([]*goplugin.ExternalPlugin, 0, len(m.externalPlugin))
	for _, p := range m.externalPlugin {
		if p.Enabled() {
			if _, ok := p.Manifest.Events[eventName]; ok {
				plugins = append(plugins, p)
			}
		}
	}
	m.mu.RUnlock()
	for _, p := range plugins {
		wg.Add(1)
		go func(p *goplugin.ExternalPlugin) {
			defer wg.Done()
			ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if _, err := p.Invoke(ectx, eventName, payload); err != nil {
				logger.Warn("插件事件处理失败", "plugin", p.ID(), "event", eventName, "error", err)
			}
		}(p)
	}
	wg.Wait()
	// 3. 脚本插件
	var eventData map[string]interface{}
	if m, ok := payload.(map[string]interface{}); ok {
		eventData = m
	} else {
		eventData = map[string]interface{}{"payload": payload}
	}
	for _, s := range m.scriptRuntime.ForEvent(eventName) {
		if _, err := m.scriptRuntime.Execute(ctx, s, eventData); err != nil {
			logger.Warn("脚本执行失败", "script", s.ID, "error", err)
		}
	}
	return nil
}

// Enable 启用插件
func (m *Manager) Enable(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.externalPlugin[id]
	if !ok {
		return nil
	}
	p.Manifest.Plugin.Enabled = true
	return nil
}

// Disable 停用插件
func (m *Manager) Disable(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.externalPlugin[id]; ok {
		p.Manifest.Plugin.Enabled = false
	}
	return nil
}

// Reload 重新加载插件
func (m *Manager) Reload(id string) error {
	m.mu.RLock()
	p, ok := m.externalPlugin[id]
	dir := p.Dir
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	np, err := goplugin.Load(dir)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.externalPlugin[id] = np
	m.mu.Unlock()
	return nil
}

// Plugins 插件清单快照
func (m *Manager) Plugins() []map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []map[string]interface{}
	for id, p := range m.externalPlugin {
		out = append(out, map[string]interface{}{
			"id": id, "name": p.Manifest.Plugin.Name, "version": p.Manifest.Plugin.Version,
			"enabled": p.Enabled(),
		})
	}
	return out
}