// Package hardware 硬件检测与模型调优预设（v0.21 M2）。
//
// 启动时检测 NVIDIA GPU 显存、总内存与 CPU 型号，按可配置规则生成
// 「硬件配置预设」（模型、上下文窗口、驻留时长、并行数）。
// config.toml [ai.tune] 的显式配置始终优先于自动检测结果。
package hardware

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"smart-nas/internal/config"
	"smart-nas/pkg/logger"
)

// Info 本机硬件信息
type Info struct {
	GPUName       string  `json:"gpu_name"`        // NVIDIA GPU 名称（空 = 无独立 GPU 或驱动不可用）
	GPUMemoryMB   int64   `json:"gpu_memory_mb"`   // 总显存（MiB，0 = 无 GPU）
	TotalMemoryGB float64 `json:"total_memory_gb"` // 总内存
	CPUModel      string  `json:"cpu_model"`
	OS            string  `json:"os"`
}

// Preset 硬件配置预设（M2 产物，记录实际生效值）
type Preset struct {
	Model       string `json:"model"`        // 默认模型
	NumCtx      int    `json:"num_ctx"`      // 上下文窗口
	KeepAlive   string `json:"keep_alive"`   // 模型驻留时长（"-1" 常驻）
	NumParallel int    `json:"num_parallel"` // 并行推理数
	Source      string `json:"source"`       // "config"（用户显式配置）| "auto"（硬件自适应）
}

// 自动选择规则对应的默认模型 tag（Ollama 官方库，均为 Q4_K_M 量化）：
//   - deepseek-r1:7b —— DeepSeek-R1-Distill-Qwen-7B，官方默认即 q4_K_M
//     （约 4.7GB，可全量加载进 12GB 显存）
//   - qwen3.5:9b —— 低配 CPU 推理兜底（需求中的 qwen3.5-7b-instruct:q4_K_M
//     在官方库不存在该精确 tag，取最接近的 7B~9B 级 Q4_K_M 量化模型）
const (
	modelGPUPref = "deepseek-r1:7b"
	modelCPUPref = "qwen3.5:9b"
)

// Detect 检测本机硬件；单项失败不阻塞，仅记日志并按无 GPU 处理
func Detect(ctx context.Context) Info {
	info := Info{OS: detectOS()}
	info.GPUName, info.GPUMemoryMB = detectNvidiaGPU(ctx)
	info.TotalMemoryGB = detectTotalMemory()
	info.CPUModel = detectCPUModel()
	logger.Info("硬件检测完成",
		"gpu", info.GPUName,
		"gpu_memory_mb", info.GPUMemoryMB,
		"memory_gb", info.TotalMemoryGB,
		"cpu", info.CPUModel)
	return info
}

// HasGPU 显存是否满足独立推理的最低门槛
func (i Info) HasGPU() bool { return i.GPUMemoryMB >= 8*1024 }

// Resolve 生成生效预设：config.toml [ai.tune] 显式值优先，零值字段按硬件规则补齐。
// fallbackModel 为 config.ai.default_model，作为 tune.model 为空且硬件规则不可用时兜底。
func (i Info) Resolve(tune config.TuneConfig, fallbackModel string) Preset {
	p := Preset{Source: "auto"}
	if i.HasGPU() {
		p.Model = modelGPUPref
		p.NumCtx = 8192
		p.KeepAlive = "-1" // 常驻显存
		p.NumParallel = 2
	} else {
		p.Model = modelCPUPref
		p.NumCtx = 4096
		p.KeepAlive = "5m"
		p.NumParallel = 1
	}
	if fallbackModel != "" && !i.HasGPU() && tune.Model == "" && i.TotalMemoryGB > 0 && i.TotalMemoryGB < 12 {
		// 低内存设备（如 8GB N100）兜底：用配置的 default_model，避免 9B 模型挤占内存
		p.Model = fallbackModel
	}
	// config.toml 显式配置覆盖（禁止写死，配置优先）
	if tune.Model != "" {
		p.Model = tune.Model
		p.Source = "config"
	}
	if tune.NumCtx > 0 {
		p.NumCtx = tune.NumCtx
		p.Source = "config"
	}
	if tune.KeepAlive != "" {
		p.KeepAlive = tune.KeepAlive
		p.Source = "config"
	}
	if tune.NumParallel > 0 {
		p.NumParallel = tune.NumParallel
		p.Source = "config"
	}
	logger.Info("硬件调优预设生效",
		"model", p.Model, "num_ctx", p.NumCtx,
		"keep_alive", p.KeepAlive, "num_parallel", p.NumParallel, "source", p.Source)
	return p
}

// detectNvidiaGPU 通过 nvidia-smi 检测 NVIDIA GPU 与总显存
func detectNvidiaGPU(ctx context.Context) (string, int64) {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return "", 0
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin,
		"--query-gpu=name,memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		logger.Warn("nvidia-smi 查询失败，按无 GPU 处理", "error", err)
		return "", 0
	}
	line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	parts := strings.Split(line, ",")
	if len(parts) < 2 {
		return "", 0
	}
	name := strings.TrimSpace(parts[0])
	mb, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return name, 0
	}
	return name, mb
}
