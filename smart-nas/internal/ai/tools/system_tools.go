package tools

import (
	"context"
	"encoding/json"
	"errors"

	"smart-nas/internal/util"
)

// ---- get_system_info 查询系统状态 ----

type systemInfoTool struct{}

func (t *systemInfoTool) Name() string        { return "get_system_info" }
func (t *systemInfoTool) Description() string { return "查询 NAS 主机系统状态（CPU、内存、磁盘、运行时长）" }
func (t *systemInfoTool) Parameters() json.RawMessage {
	return objectSchema(map[string]property{}, nil)
}

func (t *systemInfoTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	st, err := util.GetSystemStatus()
	if err != nil {
		return "", err
	}
	return jsonString(map[string]interface{}{
		"hostname": st.Hostname, "os": st.OS, "arch": st.Arch,
		"cpu_usage": round2(st.CPUUsage), "cpu_cores": st.CPUCores,
		"memory_usage": round2(st.MemoryUsage),
		"memory_used_bytes": st.MemoryUsed, "memory_total_bytes": st.MemoryTotal,
		"uptime": st.Uptime, "lan_ip": st.LANIP, "disks": st.Disks,
	}), nil
}

// ---- web_search 联网搜索（可选；未配置搜索服务时返回不可用） ----

type webSearchTool struct{}

func (t *webSearchTool) Name() string        { return "web_search" }
func (t *webSearchTool) Description() string { return "联网搜索网络信息（可选能力）" }
func (t *webSearchTool) Parameters() json.RawMessage {
	return objectSchema(map[string]property{
		"query": strProperty("搜索关键词"),
	}, []string{"query"})
}

func (t *webSearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
	}
	if err := argsTo(t, args, &p); err != nil {
		return "", err
	}
	return "", errors.New("当前未配置联网搜索服务")
}

// SystemTools 返回系统相关工具
func SystemTools() []Tool {
	return []Tool{
		&systemInfoTool{},
		&webSearchTool{},
	}
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}