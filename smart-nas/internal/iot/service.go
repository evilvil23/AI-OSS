// Package iot IoT 能力服务：统一设备注册、MQTT/米家接入、自动化引擎。
//
// 实现 tools.DeviceProvider 接口，供 AI Function Calling 调用。
// MQTT / 米家当前为基础骨架，后续可平滑扩展协议解析与设备下发。
package iot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"smart-nas/internal/api/types"
	"smart-nas/internal/config"
	"smart-nas/internal/iot/automation"
	"smart-nas/internal/iot/device"
	"smart-nas/internal/iot/mihome"
	"smart-nas/internal/iot/mqtt"
	"smart-nas/pkg/logger"
)

// Publisher WebSocket 推送接口（与 transport/ws 对齐）
type Publisher interface {
	Broadcast(msg types.WSMessage)
	BroadcastToUser(userID uint, msg types.WSMessage)
}

// Service IoT 服务
type Service struct {
	cfg      config.IoTConfig
	registry *device.Registry
	mqtt     *mqtt.Client
	mihome   *mihome.Client
	engine   *automation.Engine
	pub      Publisher
}

// NewService 创建 IoT 服务
func NewService(cfg config.IoTConfig, pub Publisher) *Service {
	s := &Service{cfg: cfg, registry: device.NewRegistry(), pub: pub}
	s.mihome = mihome.NewClient(cfg.Mihome.ClientID, cfg.Mihome.ClientSecret, cfg.Mihome.RedirectURI, cfg.Mihome.APIBase)
	s.engine = automation.NewEngine(s.registry, func(ctx context.Context, a automation.Action) error {
		return s.executeAction(ctx, a)
	})
	return s
}

// Registry 暴露设备注册表（供插件/事件使用）
func (s *Service) Registry() *device.Registry { return s.registry }

// Engine 暴露自动化引擎（供插件/事件使用）
func (s *Service) Engine() *automation.Engine { return s.engine }

// Start 启动 IoT：连接 MQTT、启动自动化调度
func (s *Service) Start() error {
	if s.cfg.MQTT.Enabled && s.cfg.MQTT.Broker != "" {
		if err := s.connectMQTT(); err != nil {
			logger.Warn("MQTT 连接失败（降级运行，仅支持本地设备）", "broker", s.cfg.MQTT.Broker, "error", err)
		}
	}
	s.engine.Start()
	logger.Info("IoT 服务已启动")
	return nil
}

// Stop 停止 IoT 服务
func (s *Service) Stop() {
	s.engine.Stop()
	if s.mqtt != nil {
		s.mqtt.Disconnect()
	}
}

func (s *Service) connectMQTT() error {
	broker := s.cfg.MQTT.Broker
	if strings.HasPrefix(broker, "ssl://") || strings.HasPrefix(broker, "tls://") {
		// TLS 支持后续实现
		return errors.New("TLS MQTT 暂未支持，请使用 tcp://")
	}
	c, err := mqtt.Dial(strings.TrimPrefix(broker, "tcp://"))
	if err != nil {
		return err
	}
	c.OnMessage = s.handleMQTTMessage
	if err := c.Connect(context.Background(), s.cfg.MQTT.ClientID, s.cfg.MQTT.Username, s.cfg.MQTT.Password, 60); err != nil {
		return err
	}
	// 订阅设备状态上报主题
	if err := c.Subscribe("smart-nas/device/+/status", "smart-nas/device/+/event"); err != nil {
		return err
	}
	s.mqtt = c
	logger.Info("MQTT 已连接", "broker", broker)
	return nil
}

// handleMQTTMessage 处理设备状态上报：登记设备、更新状态、触发自动化并推送
func (s *Service) handleMQTTMessage(topic string, payload []byte) {
	// topic 形如 smart-nas/device/{id}/status 或 .../event
	segs := strings.Split(topic, "/")
	if len(segs) < 4 {
		return
	}
	deviceID := segs[2]
	kind := segs[3]
	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		return
	}
	switch kind {
	case "status":
		s.registry.UpdateStatus(deviceID, true, data)
		s.engine.OnDeviceEvent(deviceID, data)
		if s.pub != nil {
			s.pub.Broadcast(types.NewWSMessage("device_status", map[string]interface{}{
				"device_id": deviceID, "status": data,
			}))
		}
	case "event":
		s.registry.Touch(deviceID)
		s.engine.OnDeviceEvent(deviceID, data)
	}
}

// executeAction 执行自动化单个动作
func (s *Service) executeAction(ctx context.Context, a automation.Action) error {
	_, err := s.doControl(ctx, a.DeviceID, a.Action, a.Params)
	return err
}

// ---- tools.DeviceProvider 实现 ----

// Control 控制设备（AI 工具调用）
func (s *Service) Control(ctx context.Context, deviceID, action string, params map[string]interface{}) (string, error) {
	return s.doControl(ctx, deviceID, action, params)
}

// Status 查询设备状态（AI 工具调用）
func (s *Service) Status(ctx context.Context, deviceID string) (string, error) {
	d, ok := s.registry.Get(deviceID)
	if !ok {
		return "", fmt.Errorf("设备不存在: %s", deviceID)
	}
	return jsonString(map[string]interface{}{
		"device_id": d.ID, "name": d.Name, "type": d.Type,
		"online": d.Online, "status": d.Status,
	}), nil
}

// List 列出设备（AI 工具调用）
func (s *Service) List(ctx context.Context, room, typ string) (string, error) {
	devices := s.registry.List(room, typ)
	return jsonString(map[string]interface{}{"count": len(devices), "devices": devices}), nil
}

// CreateAutomation 创建自动化场景（AI 工具调用），返回场景 ID
func (s *Service) CreateAutomation(ctx context.Context, name string, trigger, actions interface{}) (string, error) {
	a := &automation.Automation{
		Name:    name,
		Enabled: true,
		Trigger: parseTrigger(trigger),
		Actions: parseActions(actions),
	}
	if a.Trigger.Type == "" {
		a.Trigger.Type = "time"
	}
	created, err := s.engine.Add(a)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// doControl 设备控制：在线则通过 MQTT 下发或本地模拟
func (s *Service) doControl(ctx context.Context, deviceID, action string, params map[string]interface{}) (string, error) {
	d, ok := s.registry.Get(deviceID)
	if !ok {
		return "", fmt.Errorf("设备不存在: %s", deviceID)
	}
	if !d.Online {
		return "", errors.New("设备离线")
	}
	if params == nil {
		params = map[string]interface{}{}
	}
	params["action"] = action
	// 1) 优先通过 MQTT 下发指令
	if s.mqtt != nil {
		topic := fmt.Sprintf("smart-nas/device/%s/cmd", deviceID)
		if payload, err := json.Marshal(params); err == nil {
			_ = s.mqtt.Publish(topic, payload)
		}
	}
	// 2) 本地状态模拟
	s.applyLocalState(d, action, params)
	return fmt.Sprintf("指令 %s 已下发至设备 %s", action, deviceID), nil
}

// applyLocalState 本地更新设备状态（模拟真实下发后的结果）
func (s *Service) applyLocalState(d *device.Device, action string, params map[string]interface{}) {
	kv := make(map[string]interface{})
	switch action {
	case "power_on", "turn_on":
		kv["power"] = true
	case "power_off", "turn_off":
		kv["power"] = false
	}
	for k, v := range params {
		if k != "action" {
			kv[k] = v
		}
	}
	if len(kv) > 0 {
		s.registry.UpdateStatus(d.ID, true, kv)
		if s.pub != nil {
			s.pub.Broadcast(types.NewWSMessage("device_status", map[string]interface{}{
				"device_id": d.ID, "status": kv,
			}))
		}
	}
}

// ---- 辅助 ----

func parseTrigger(raw interface{}) automation.Trigger {
	data, _ := json.Marshal(raw)
	var t automation.Trigger
	_ = json.Unmarshal(data, &t)
	return t
}

func parseActions(raw interface{}) []automation.Action {
	data, _ := json.Marshal(raw)
	var acts []automation.Action
	_ = json.Unmarshal(data, &acts)
	return acts
}

func jsonString(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// WaitConnected 预留：等待 MQTT 就绪（后续扩展）
func (s *Service) WaitConnected(timeout time.Duration) bool {
	_ = timeout
	return true
}