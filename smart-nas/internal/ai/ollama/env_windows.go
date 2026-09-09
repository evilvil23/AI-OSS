//go:build windows

package ollama

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// ollamaModelsEnv 解析拉起 ollama serve 时应注入的 OLLAMA_MODELS。
// 查找顺序：当前进程环境 → Windows 用户级注册表 → 系统级注册表。
// 返回空表示无需注入（由 Ollama 使用默认目录）。
func ollamaModelsEnv() string {
	if v := os.Getenv("OLLAMA_MODELS"); v != "" {
		return v
	}
	// 用户级环境变量（HKCU\Environment），对新进程自动生效但不会传播到
	// 已运行进程（如终端 / 服务）的子进程，故主动读取
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err == nil {
		if v, _, err := k.GetStringValue("OLLAMA_MODELS"); err == nil && v != "" {
			k.Close()
			return v
		}
		k.Close()
	}
	// 系统级环境变量（HKLM\...\Session Manager\Environment）
	k2, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE)
	if err == nil {
		if v, _, err := k2.GetStringValue("OLLAMA_MODELS"); err == nil && v != "" {
			k2.Close()
			return v
		}
		k2.Close()
	}
	return ""
}
