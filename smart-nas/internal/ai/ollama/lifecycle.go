// lifecycle.go Ollama 进程生命周期管理（v0.21 M1）。
//
// 职责：
//   - 启动时检测 Ollama 是否已运行（Ping /api/tags），未运行则后台拉起
//     ollama serve（记录 PID），等待就绪后预热默认模型；
//   - 优雅关闭时先以 keep_alive=0 卸载模型释放显存 / 内存，再停止进程 ——
//     仅停止本服务拉起的实例，绝不杀掉用户自启的 Ollama；
//   - 提供进程状态 / 已加载模型 / 显存占用查询。
//
// 跨平台：Windows 用 os/exec 后台进程（隐藏窗口 + 独立进程组）并记录 PID；
// Linux / macOS 退化为 setsid 直接 exec（生产可改用 systemd 托管，
// 将 [ai.ollama].managed 设为 false 即可禁用本管理器）。
package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"smart-nas/pkg/logger"
)

// pidRecord pid 文件内容（用于服务重启后接管校验）
type pidRecord struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

// Lifecycle Ollama 进程生命周期管理器
type Lifecycle struct {
	mu      sync.Mutex
	client  *Client
	cfg     OllamaRunConfig
	pidPath string

	managed bool // 当前进程是否为本服务本次运行拉起
	cmd     *exec.Cmd
}

// OllamaRunConfig 生命周期管理参数（来自 config [ai.ollama] + 硬件预设）
type OllamaRunConfig struct {
	Managed      bool   // 由本服务拉起 / 停止
	Binary       string // 可执行文件路径（空 = 自动探测）
	BindHost     string // 拉起时注入 OLLAMA_HOST
	StartTimeout int    // 等待就绪秒数
	AutoWarmup   bool   // 就绪后预热默认模型
	NumParallel  int    // 并行推理数（>0 时注入 OLLAMA_NUM_PARALLEL）
}

// NewLifecycle 创建管理器；dataDir 用于存放 ollama.pid
func NewLifecycle(client *Client, cfg OllamaRunConfig, dataDir string) *Lifecycle {
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 60
	}
	if cfg.BindHost == "" {
		cfg.BindHost = "127.0.0.1:11434"
	}
	return &Lifecycle{
		client:  client,
		cfg:     cfg,
		pidPath: filepath.Join(dataDir, "ollama.pid"),
	}
}

// EnsureRunning 确保 Ollama 就绪：已运行 → 直接返回；未运行且允许托管 → 拉起 + 等就绪 + 预热。
// 返回的 bool 表示本次是否为本服务拉起（false = 复用已运行实例或未启用托管）。
func (l *Lifecycle) EnsureRunning(ctx context.Context, defaultModel, keepAlive string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.client.Ping(ctx); err == nil {
		// Ollama 已在运行：用户自启或上一任服务拉起。若 pid 文件存在（上一任拉起且未清理），
		// 视为本服务体系实例，关闭时可接管停止；否则视为用户自启，绝不触碰。
		if rec, err := readPIDFile(l.pidPath); err == nil && processAlive(rec.PID) {
			l.managed = true
			l.cmd = nil // 无本次 exec 句柄，仅凭 pid 文件接管
			logger.Info("检测到已运行的 Ollama（上一任服务实例），已接管管理", "pid", rec.PID)
		} else {
			l.managed = false
			logger.Info("检测到已运行的 Ollama（用户自启实例），不做进程管理")
		}
		return l.managed, nil
	}

	if !l.cfg.Managed {
		return false, errors.New("Ollama 未运行且 [ai.ollama].managed=false，请手动启动 Ollama 或开启托管")
	}

	bin, err := l.resolveBinary()
	if err != nil {
		return false, err
	}
	cmd, err := spawnServe(bin, l.cfg.BindHost, l.cfg.NumParallel)
	if err != nil {
		return false, fmt.Errorf("拉起 ollama serve 失败: %w", err)
	}
	l.cmd = cmd
	l.managed = true
	if err := l.writePID(cmd.Process.Pid); err != nil {
		logger.Warn("Ollama pid 文件写入失败", "error", err)
	}
	logger.Info("已后台拉起 ollama serve", "binary", bin, "bind", l.cfg.BindHost, "pid", cmd.Process.Pid)

	// 等待就绪（最多 N 秒）
	deadline := time.Now().Add(time.Duration(l.cfg.StartTimeout) * time.Second)
	for {
		if time.Now().After(deadline) {
			return true, fmt.Errorf("等待 Ollama 就绪超时（%ds）", l.cfg.StartTimeout)
		}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := l.client.Ping(pctx)
		cancel()
		if err == nil {
			break
		}
		// 进程提前退出则立即失败
		select {
		case <-time.After(1 * time.Second):
		case <-ctx.Done():
			return true, ctx.Err()
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return true, fmt.Errorf("ollama serve 进程异常退出")
		}
	}
	logger.Info("Ollama 已就绪")

	if l.cfg.AutoWarmup && defaultModel != "" {
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Minute)
		defer wcancel()
		if err := l.client.Warmup(wctx, defaultModel, keepAlive); err != nil {
			logger.Warn("默认模型预热失败（可继续使用，首次对话时再加载）",
				"model", defaultModel, "error", err)
		} else {
			logger.Info("默认模型预热完成", "model", defaultModel, "keep_alive", keepAlive)
		}
	}
	return true, nil
}

// Shutdown 优雅关闭：先卸载模型释放显存 / 内存，再停止本服务拉起的实例。
// 用户自启的 Ollama 不会被触碰。
func (l *Lifecycle) Shutdown(ctx context.Context, model string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.managed {
		return
	}
	if model != "" {
		uctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := l.client.UnloadModel(uctx, model); err != nil {
			logger.Warn("卸载模型失败（继续停止进程）", "model", model, "error", err)
		} else {
			logger.Info("模型已卸载，显存 / 内存已释放", "model", model)
		}
		cancel()
	}
	l.stopProcess()
	_ = os.Remove(l.pidPath)
	l.managed = false
}

// Status 进程状态查询（供 /api/ai/status）
func (l *Lifecycle) Status(ctx context.Context) map[string]interface{} {
	l.mu.Lock()
	managed := l.managed
	pid := 0
	if l.cmd != nil && l.cmd.Process != nil {
		pid = l.cmd.Process.Pid
	}
	l.mu.Unlock()
	if pid == 0 {
		if rec, err := readPIDFile(l.pidPath); err == nil {
			pid = rec.PID
		}
	}

	running := l.client.Ping(ctx) == nil
	out := map[string]interface{}{
		"host":              l.client.BaseURL(),
		"running":           running,
		"managed":           l.cfg.Managed,
		"managed_by_service": managed,
		"pid":               pid,
	}
	if running {
		if models, err := l.client.RunningModels(ctx); err == nil {
			out["loaded_models"] = models
			var vram int64
			for _, m := range models {
				vram += m.SizeVRAM
			}
			out["vram_used_bytes"] = vram
		}
	}
	return out
}

// resolveBinary 探测 ollama 可执行文件：配置路径 → PATH → 常见安装位置
func (l *Lifecycle) resolveBinary() (string, error) {
	if l.cfg.Binary != "" {
		if _, err := os.Stat(l.cfg.Binary); err == nil {
			return l.cfg.Binary, nil
		}
		return "", fmt.Errorf("配置的 ollama 可执行文件不存在: %s", l.cfg.Binary)
	}
	if bin, err := exec.LookPath("ollama"); err == nil {
		return bin, nil
	}
	for _, p := range commonBinaryPaths() {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("未找到 ollama 可执行文件：请在 PATH 中安装 Ollama，或在 [ai.ollama].binary 配置完整路径")
}

func (l *Lifecycle) writePID(pid int) error {
	rec := pidRecord{PID: pid, StartedAt: time.Now().Format(time.RFC3339)}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(l.pidPath, data, 0o644)
}

func (l *Lifecycle) stopProcess() {
	// 优先用本次 exec 句柄；接管场景（无句柄）按 pid 文件停止
	if l.cmd != nil && l.cmd.Process != nil {
		logger.Info("停止本服务拉起的 Ollama 进程", "pid", l.cmd.Process.Pid)
		_ = terminateProcess(l.cmd)
		return
	}
	if rec, err := readPIDFile(l.pidPath); err == nil && processAlive(rec.PID) {
		logger.Info("停止接管的 Ollama 进程", "pid", rec.PID)
		terminateByPID(rec.PID)
	}
}

func readPIDFile(path string) (pidRecord, error) {
	var rec pidRecord
	data, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	err = json.Unmarshal(data, &rec)
	return rec, err
}
