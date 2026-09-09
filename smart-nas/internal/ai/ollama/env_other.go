//go:build !windows

package ollama

import "os"

// ollamaModelsEnv 解析拉起 ollama serve 时应注入的 OLLAMA_MODELS。
// 非 Windows 平台仅读取进程环境（由 shell / systemd 保证），返回空表示无需注入。
func ollamaModelsEnv() string {
	return os.Getenv("OLLAMA_MODELS")
}
