// tools.go 将 HomeAssistant 能力注册为 AI Function Calling 工具。
//
// 未配置 / 自检失败时不注册（AI 对话中自然降级为「暂不支持设备控制」）。
// 实现与 tools.Tool 接口（internal/ai/tools）签名一致，可直接注册。
package ha

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"smart-nas/internal/util"
)

// objectSchema 快速构造 object 类型 JSON Schema
func objectSchema(properties map[string]interface{}, required []string) json.RawMessage {
	s := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	data, _ := json.Marshal(s)
	return data
}

func strProperty(desc string) map[string]string {
	return map[string]string{"type": "string", "description": desc}
}

func jsonString(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- 工具 1：ha_list_devices 查询设备列表 ----

type listDevicesTool struct{ client *Client }

func (t *listDevicesTool) Name() string { return "ha_list_devices" }
func (t *listDevicesTool) Description() string {
	return "查询 HomeAssistant 智能家居设备列表。domain 为设备类型过滤（light=灯、switch=开关、climate=空调温控、sensor=传感器等），留空返回全部。"
}
func (t *listDevicesTool) Parameters() json.RawMessage {
	return objectSchema(map[string]interface{}{
		"domain": strProperty("设备类型过滤，如 light/switch/climate/sensor，留空返回全部"),
	}, nil)
}
func (t *listDevicesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Domain string `json:"domain"`
	}
	_ = json.Unmarshal(args, &a)
	states, err := t.client.ListStates(ctx, a.Domain)
	if err != nil {
		return "", err
	}
	type item struct {
		EntityID string `json:"entity_id"`
		Name     string `json:"name"`
		State    string `json:"state"`
	}
	items := util.Map(states, func(s State) item {
		name, _ := s.Attributes["friendly_name"].(string)
		return item{EntityID: s.EntityID, Name: name, State: s.State}
	})
	return jsonString(items), nil
}

// ---- 工具 2：ha_get_state 查询设备状态 ----

type getStateTool struct{ client *Client }

func (t *getStateTool) Name() string { return "ha_get_state" }
func (t *getStateTool) Description() string {
	return "查询指定 HomeAssistant 设备的当前状态与属性。entity_id 形如 light.living_room、sensor.temperature。"
}
func (t *getStateTool) Parameters() json.RawMessage {
	return objectSchema(map[string]interface{}{
		"entity_id": strProperty("实体 ID，如 light.living_room"),
	}, []string{"entity_id"})
}
func (t *getStateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		EntityID string `json:"entity_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.EntityID == "" {
		return "", fmt.Errorf("缺少 entity_id 参数")
	}
	st, err := t.client.GetState(ctx, a.EntityID)
	if err != nil {
		return "", err
	}
	return jsonString(st), nil
}

// ---- 工具 3：ha_call_service 控制设备 ----

type callServiceTool struct{ client *Client }

func (t *callServiceTool) Name() string { return "ha_call_service" }
func (t *callServiceTool) Description() string {
	return "调用 HomeAssistant 服务控制设备。domain 与 service 组成服务名，如 light/turn_on、light/turn_off、switch/toggle、climate/set_temperature；data 为附加参数（如 brightness 亮度 0-255、temperature 温度）。涉及危险操作前需与用户二次确认。"
}
func (t *callServiceTool) Parameters() json.RawMessage {
	dataSchema := map[string]interface{}{
		"type":        "object",
		"description": "服务附加参数，如 {\"brightness\":128} 或 {\"temperature\":26}",
	}
	return objectSchema(map[string]interface{}{
		"domain":    strProperty("服务域，如 light/switch/climate/cover"),
		"service":   strProperty("服务名，如 turn_on/turn_off/toggle/set_temperature"),
		"entity_id": strProperty("目标实体 ID，如 light.living_room"),
		"data":      dataSchema,
	}, []string{"domain", "service", "entity_id"})
}
func (t *callServiceTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Domain   string                 `json:"domain"`
		Service  string                 `json:"service"`
		EntityID string                 `json:"entity_id"`
		Data     map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if a.Domain == "" || a.Service == "" || a.EntityID == "" {
		return "", fmt.Errorf("缺少 domain/service/entity_id 参数")
	}
	// entity_id 形如 light.living_room，domain 填错时自动纠正
	if d, _, ok := strings.Cut(a.EntityID, "."); ok && d != a.Domain {
		a.Domain = d
	}
	if err := t.client.CallService(ctx, a.Domain, a.Service, a.EntityID, a.Data); err != nil {
		return "", err
	}
	return jsonString(map[string]string{"result": "ok", "entity_id": a.EntityID, "service": a.Domain + "." + a.Service}), nil
}

// Tool 工具接口（与 internal/ai/tools.Tool 一致，避免包间依赖环）
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Tools 返回 HA 全部 AI 工具；client 为 nil 时返回空（未配置降级）
func Tools(client *Client) []Tool {
	if client == nil {
		return nil
	}
	return []Tool{
		&listDevicesTool{client},
		&getStateTool{client},
		&callServiceTool{client},
	}
}
