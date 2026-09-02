package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"smart-nas/internal/storage"
)

// ---- search_files 语义搜索文件（当前基于文件名关键词检索） ----

type searchFilesTool struct{ svc *storage.Service }

func (t *searchFilesTool) Name() string        { return "search_files" }
func (t *searchFilesTool) Description() string { return "按文件名/关键词搜索用户文件" }
func (t *searchFilesTool) Parameters() json.RawMessage {
	return objectSchema(map[string]property{
		"query":     strProperty("搜索关键词"),
		"file_type": strProperty("过滤文件后缀，如 pdf/docx/txt（可选）"),
		"limit":     intProperty("返回数量上限（可选，默认 20）"),
	}, []string{"query"})
}

func (t *searchFilesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query    string `json:"query"`
		FileType string `json:"file_type"`
		Limit    int    `json:"limit"`
	}
	if err := argsTo(t, args, &p); err != nil {
		return "", err
	}
	uid, ok := UserIDFrom(ctx)
	if !ok {
		return "", errors.New("缺少用户上下文")
	}
	files, err := t.svc.ListFiles(uid, 0, p.Query)
	if err != nil {
		return "", err
	}
	if p.Limit <= 0 {
		p.Limit = 20
	}
	type item struct {
		ID       uint   `json:"id"`
		Name     string `json:"name"`
		MimeType string `json:"mime_type"`
		Size     int64  `json:"size"`
		Dir      bool   `json:"is_dir"`
	}
	var out []item
	for _, f := range files {
		if len(out) >= p.Limit {
			break
		}
		if p.FileType != "" && !strings.HasSuffix(strings.ToLower(f.Name), "."+strings.ToLower(p.FileType)) {
			continue
		}
		out = append(out, item{ID: f.ID, Name: f.Name, MimeType: f.MimeType, Size: f.Size, Dir: f.IsDir})
	}
	return jsonString(map[string]interface{}{"count": len(out), "files": out}), nil
}

// ---- list_files 列出目录文件 ----

type listFilesTool struct{ svc *storage.Service }

func (t *listFilesTool) Name() string        { return "list_files" }
func (t *listFilesTool) Description() string { return "列出指定目录下的文件与子目录" }
func (t *listFilesTool) Parameters() json.RawMessage {
	return objectSchema(map[string]property{
		"path": intProperty("父目录 ID，0 表示根目录"),
	}, []string{"path"})
}

func (t *listFilesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path uint `json:"path"`
	}
	if err := argsTo(t, args, &p); err != nil {
		return "", err
	}
	uid, ok := UserIDFrom(ctx)
	if !ok {
		return "", errors.New("缺少用户上下文")
	}
	files, err := t.svc.ListFiles(uid, p.Path, "")
	if err != nil {
		return "", err
	}
	return jsonString(map[string]interface{}{"parent_id": p.Path, "files": files}), nil
}

// ---- read_file 读取文件文本内容 ----

type readFileTool struct{ svc *storage.Service }

func (t *readFileTool) Name() string        { return "read_file" }
func (t *readFileTool) Description() string { return "读取文件内容（文本文件返回前 max_chars 字符）" }
func (t *readFileTool) Parameters() json.RawMessage {
	return objectSchema(map[string]property{
		"file_id":   strProperty("文件 ID"),
		"max_chars": intProperty("最大读取字符数（可选，默认 4000）"),
	}, []string{"file_id"})
}

func (t *readFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		FileID   uint   `json:"file_id"`
		MaxChars int    `json:"max_chars"`
	}
	if err := argsTo(t, args, &p); err != nil || p.FileID == 0 {
		return "", errors.New("file_id 必须是数字")
	}
	f, err := t.svc.GetFileMeta(p.FileID)
	if err != nil {
		return "", err
	}
	if f.IsDir {
		return "", errors.New("目标为目录，无法读取")
	}
	if p.MaxChars <= 0 {
		p.MaxChars = 4000
	}
	var content string
	h, err := os.Open(f.StoragePath)
	if err == nil {
		defer h.Close()
		b, rerr := io.ReadAll(io.LimitReader(h, int64(p.MaxChars)+1))
		if rerr == nil {
			content = string(b)
			if len(b) > p.MaxChars {
				content = content[:p.MaxChars] + "...(已截断)"
			}
		}
	}
	return jsonString(map[string]interface{}{
		"file_id": p.FileID, "name": f.Name,
		"size": f.Size, "truncated": len(content) > p.MaxChars, "content": content,
	}), nil
}

// ---- get_storage_stats 查询存储用量 ----

type storageStatsTool struct{ svc *storage.Service }

func (t *storageStatsTool) Name() string        { return "get_storage_stats" }
func (t *storageStatsTool) Description() string { return "查询当前用户的存储用量与配额" }
func (t *storageStatsTool) Parameters() json.RawMessage {
	return objectSchema(map[string]property{}, nil)
}

func (t *storageStatsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	uid, ok := UserIDFrom(ctx)
	if !ok {
		return "", errors.New("缺少用户上下文")
	}
	st, err := t.svc.GetStorageStats(uid)
	if err != nil {
		return "", err
	}
	return jsonString(map[string]interface{}{
		"total_files": st.TotalFiles, "used_bytes": st.UsedBytes,
		"trash_files": st.TrashFiles,
		"version_bytes": st.VersionBytes,
	}), nil
}

// ---- 设备相关（依赖 DeviceProvider 注入） ----

type controlDeviceTool struct{ r *Registry }

func (t *controlDeviceTool) Name() string { return "control_device" }
func (t *controlDeviceTool) Description() string {
	return "控制智能设备，如开/关、调节亮度温度（需设备在线）"
}
func (t *controlDeviceTool) Parameters() json.RawMessage {
	return objectSchema(map[string]property{
		"device_id": strProperty("设备 ID"),
		"action":    strProperty("动作，如 power_on/power_off/set_brightness/set_temperature"),
		"params":    property{"type": "object", "description": "动作参数（可选）"},
	}, []string{"device_id", "action"})
}

func (t *controlDeviceTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		DeviceID string                 `json:"device_id"`
		Action   string                 `json:"action"`
		Params   map[string]interface{} `json:"params"`
	}
	if err := argsTo(t, args, &p); err != nil {
		return "", err
	}
	if t.r.device == nil {
		return "", errors.New("设备控制服务未接入")
	}
	return t.r.device.Control(ctx, p.DeviceID, p.Action, p.Params)
}

type deviceStatusTool struct{ r *Registry }

func (t *deviceStatusTool) Name() string        { return "get_device_status" }
func (t *deviceStatusTool) Description() string { return "查询单个设备的实时状态" }
func (t *deviceStatusTool) Parameters() json.RawMessage {
	return objectSchema(map[string]property{
		"device_id": strProperty("设备 ID"),
	}, []string{"device_id"})
}

func (t *deviceStatusTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ DeviceID string `json:"device_id"` }
	if err := argsTo(t, args, &p); err != nil {
		return "", err
	}
	if t.r.device == nil {
		return "", errors.New("设备控制服务未接入")
	}
	return t.r.device.Status(ctx, p.DeviceID)
}

type listDevicesTool struct{ r *Registry }

func (t *listDevicesTool) Name() string        { return "list_devices" }
func (t *listDevicesTool) Description() string { return "列出所有智能设备，可按房间/类型过滤" }
func (t *listDevicesTool) Parameters() json.RawMessage {
	return objectSchema(map[string]property{
		"room": strProperty("按房间过滤（可选）"),
		"type": strProperty("按类型过滤（可选），如 switch/light/sensor"),
	}, nil)
}

func (t *listDevicesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Room string `json:"room"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(args, &p)
	if t.r.device == nil {
		return "", errors.New("设备控制服务未接入")
	}
	return t.r.device.List(ctx, p.Room, p.Type)
}

type createAutomationTool struct{ r *Registry }

func (t *createAutomationTool) Name() string { return "create_automation" }
func (t *createAutomationTool) Description() string {
	return "创建自动化场景，如 触发条件(trigger)+执行动作(actions)"
}
func (t *createAutomationTool) Parameters() json.RawMessage {
	return objectSchema(map[string]property{
		"name":    strProperty("场景名称"),
		"trigger": property{"type": "object", "description": "触发条件，如 {type:'time',at:'07:30'} 或 {type:'device',device_id:'...',value:'on'}"},
		"actions": property{"type": "array", "description": "执行动作数组，如 [{device_id:'...',action:'power_on'}]"},
	}, []string{"name", "trigger", "actions"})
}

func (t *createAutomationTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name    string      `json:"name"`
		Trigger interface{} `json:"trigger"`
		Actions interface{} `json:"actions"`
	}
	if err := argsTo(t, args, &p); err != nil {
		return "", err
	}
	if t.r.device == nil {
		return "", errors.New("自动化服务未接入")
	}
	return t.r.device.CreateAutomation(ctx, p.Name, p.Trigger, p.Actions)
}

// ---- 注册默认工具 ----

// DefaultTools 返回内置工具集合
func DefaultTools(svc *storage.Service) []Tool {
	return []Tool{
		&searchFilesTool{svc: svc},
		&listFilesTool{svc: svc},
		&readFileTool{svc: svc},
		&storageStatsTool{svc: svc},
	}
}

// DeviceTools 返回依赖设备提供者的工具（需先 SetDeviceProvider）
func DeviceTools(r *Registry) []Tool {
	return []Tool{
		&controlDeviceTool{r: r},
		&deviceStatusTool{r: r},
		&listDevicesTool{r: r},
		&createAutomationTool{r: r},
	}
}