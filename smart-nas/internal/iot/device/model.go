// Package device IoT 设备模型与注册表。
//
// 作为 IoT 能力的统一抽象，覆盖米家、MQTT、Home Assistant 及本地插件
// 等多种来源，后续扩展时只需实现对应的 "sink" 即可接入。
package device

import "time"

// Device 统一设备模型
type Device struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Type      string                 `json:"type"`   // switch / light / sensor / climate ...
	Room      string                 `json:"room"`   // 所在房间
	Source    string                 `json:"source"` // mihome / mqtt / ha / local
	Online    bool                   `json:"online"`
	Status    map[string]interface{} `json:"status"`
	Props     map[string]interface{} `json:"props"` // 设备能力/属性描述
	LastSeen  time.Time              `json:"last_seen"`
	CreatedAt time.Time              `json:"created_at"`
}