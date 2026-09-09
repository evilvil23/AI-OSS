// hardware_test.go 硬件调优预设规则单元测试。
package hardware

import (
	"testing"

	"smart-nas/internal/config"
)

func TestResolveGPUBranch(t *testing.T) {
	info := Info{GPUName: "RTX 4070", GPUMemoryMB: 12282, TotalMemoryGB: 32, CPUModel: "i7"}
	p := info.Resolve(config.TuneConfig{}, "qwen2:7b")
	if p.Model != "deepseek-r1:7b" {
		t.Fatalf("12GB 显存应选 deepseek-r1:7b，实际 %s", p.Model)
	}
	if p.NumCtx != 8192 || p.KeepAlive != "-1" || p.NumParallel != 2 {
		t.Fatalf("GPU 预设不符: %+v", p)
	}
	if p.Source != "auto" {
		t.Fatalf("未配置时 source 应为 auto: %s", p.Source)
	}
}

func TestResolveGPULowMemory(t *testing.T) {
	info := Info{GPUName: "GTX 1060", GPUMemoryMB: 6144, TotalMemoryGB: 16}
	if info.HasGPU() {
		t.Fatal("6GB 显存不应视为满足 GPU 推理门槛")
	}
	p := info.Resolve(config.TuneConfig{}, "")
	if p.Model != modelCPUPref {
		t.Fatalf("显存不足应走 CPU 分支，实际 %s", p.Model)
	}
}

func TestResolveCPUBranch(t *testing.T) {
	info := Info{TotalMemoryGB: 16, CPUModel: "N100"}
	p := info.Resolve(config.TuneConfig{}, "")
	if p.Model != "qwen3.5:9b" {
		t.Fatalf("无 GPU 应选 qwen3.5:9b，实际 %s", p.Model)
	}
	if p.NumCtx != 4096 || p.KeepAlive != "5m" || p.NumParallel != 1 {
		t.Fatalf("CPU 预设不符: %+v", p)
	}
}

func TestResolveLowMemoryFallback(t *testing.T) {
	// 8GB 内存的 N100：9B 模型偏大，应回退到配置的 default_model
	info := Info{TotalMemoryGB: 8, CPUModel: "N100"}
	p := info.Resolve(config.TuneConfig{}, "qwen2:1.5b")
	if p.Model != "qwen2:1.5b" {
		t.Fatalf("低内存设备应回退 default_model，实际 %s", p.Model)
	}
}

func TestResolveConfigOverride(t *testing.T) {
	info := Info{GPUName: "RTX 4070", GPUMemoryMB: 12282, TotalMemoryGB: 32}
	tune := config.TuneConfig{Model: "llama3:8b", NumCtx: 4096, KeepAlive: "10m", NumParallel: 1}
	p := info.Resolve(tune, "")
	if p.Model != "llama3:8b" || p.NumCtx != 4096 || p.KeepAlive != "10m" || p.NumParallel != 1 {
		t.Fatalf("config.toml 显式配置应整体覆盖: %+v", p)
	}
	if p.Source != "config" {
		t.Fatalf("显式配置时 source 应为 config: %s", p.Source)
	}
}
