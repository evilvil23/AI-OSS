// Package goplugin 进程外插件加载器（简化实现）。
//
// 说明：文档选用 hashicorp/go-plugin，但当前离线环境不可用；
// 这里实现等价简化：解析 plugin.toml 清单，事件触发时通过 stdin/stdout
// 调用独立可执行文件（entrypoint），保持"进程隔离 + 崩溃不影响主程序"
// 的核心目标。需要完整 gRPC 插件协议时可替换为 go-plugin。
package goplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Manifest 插件清单（对应用户 plugin.toml）
type Manifest struct {
	Plugin struct {
		ID         string                 `toml:"id"`
		Name       string                 `toml:"name"`
		Version    string                 `toml:"version"`
		Type       string                 `toml:"type"` // goplugin
		APIVersion string                 `toml:"api_version"`
		Entrypoint string                 `toml:"entrypoint"`
		Enabled    bool                   `toml:"enabled"`
		Config     map[string]interface{} `toml:"config"`
	} `toml:"plugin"`

	Permissions struct {
		FileRead  bool `toml:"file_read"`
		FileWrite bool `toml:"file_write"`
		Network   bool `toml:"network"`
	} `toml:"permissions"`

	// Events 监听的事件名 → 优先级
	Events map[string]int `toml:"events"`
}

// ExternalPlugin 进程外插件实例
type ExternalPlugin struct {
	Manifest Manifest
	Dir      string
	DataDir  string
}

// Load 扫描插件目录，解析 plugin.toml 并构建插件实例
func Load(dir string) (*ExternalPlugin, error) {
	manifestPath := filepath.Join(dir, "plugin.toml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", manifestPath, err)
	}
	if m.Plugin.Type != "" && m.Plugin.Type != "goplugin" {
		return nil, fmt.Errorf("插件类型 %q 不受 goplugin 加载器支持", m.Plugin.Type)
	}
	dataDir := filepath.Join(dir, "data")
	_ = os.MkdirAll(dataDir, 0o755)
	p := &ExternalPlugin{Manifest: m, Dir: dir, DataDir: dataDir}
	// 预构建插件进程（校验 entrypoint 存在）
	if m.Plugin.Enabled && m.Plugin.Entrypoint != "" {
		if _, err := os.Stat(p.entryPoint()); err != nil {
			return nil, fmt.Errorf("插件入口不存在: %w", err)
		}
	}
	return p, nil
}

func (p *ExternalPlugin) entryPoint() string {
	return filepath.Join(p.Dir, p.Manifest.Plugin.Entrypoint)
}

// ID 插件唯一标识
func (p *ExternalPlugin) ID() string { return p.Manifest.Plugin.ID }

// Enabled 是否启用
func (p *ExternalPlugin) Enabled() bool { return p.Manifest.Plugin.Enabled }

// Invoke 触发事件：以 stdin 传入事件 JSON，读取 stdout 结果
func (p *ExternalPlugin) Invoke(ctx context.Context, eventName string, payload interface{}) ([]byte, error) {
	if !p.Enabled() {
		return nil, nil
	}
	msg, err := json.Marshal(map[string]interface{}{
		"event": eventName, "payload": payload, "data_dir": p.DataDir,
	})
	if err != nil {
		return nil, err
	}
	exe := p.entryPoint()
	if _, err := os.Stat(exe); err != nil {
		return nil, errors.New("插件入口缺失")
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, exe)
	cmd.Stdin = bytes.NewReader(msg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("插件 %s 执行失败: %v: %s", p.ID(), err, stderr.String())
	}
	return stdout.Bytes(), nil
}