// Package automation 自动化场景引擎（时间 / cron 计划 + 设备事件触发）。
//
// 规则模型支持 {time|cron|device} 三类触发器；执行器通过 ExecFunc 注入，
// 便于与 IoT service、插件解耦。当前为轻量实现，后续可扩展天气/传感器
// 联动等复杂条件。
package automation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"smart-nas/internal/iot/device"
	"smart-nas/internal/task"
	"smart-nas/internal/util"
)

// Automation 自动化场景
type Automation struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UserID    uint      `json:"user_id"`
	Enabled   bool      `json:"enabled"`
	Trigger   Trigger   `json:"trigger"`
	Actions   []Action  `json:"actions"`
	CreatedAt time.Time `json:"created_at"`
}

// Trigger 触发条件：type=time(At=HH:MM) / cron(Cron 表达式) / device(DeviceID+Value)
type Trigger struct {
	Type     string `json:"type"`
	At       string `json:"at,omitempty"`        // time 触发器：HH:MM
	Cron     string `json:"cron,omitempty"`      // cron 触发器：5 段表达式
	DeviceID string `json:"device_id,omitempty"` // device 触发器：设备 ID
	Value    string `json:"value,omitempty"`     // device 触发器：期望状态值
}

// Action 执行动作
type Action struct {
	DeviceID string                 `json:"device_id"`
	Action   string                 `json:"action"`
	Params   map[string]interface{} `json:"params,omitempty"`
}

// ExecFunc 执行单个动作的回调（由 IoT service 注入）
type ExecFunc func(ctx context.Context, a Action) error

// Engine 自动化引擎
type Engine struct {
	mu         sync.RWMutex
	automation map[string]*Automation
	device     *device.Registry
	exec       ExecFunc
	sched      *task.Scheduler
}

// NewEngine 创建引擎；exec 为空则动作仅记录不执行
func NewEngine(reg *device.Registry, exec ExecFunc) *Engine {
	return &Engine{
		automation: make(map[string]*Automation),
		device:     reg,
		exec:       exec,
	}
}

// Add 添加自动化；未指定触发器类型时默认 type=time
func (e *Engine) Add(a *Automation) (*Automation, error) {
	if a.Trigger.Type == "" {
		a.Trigger.Type = "time"
	}
	if a.Trigger.Type != "time" && a.Trigger.Type != "cron" && a.Trigger.Type != "device" {
		return nil, fmt.Errorf("不支持的触发器类型: %s", a.Trigger.Type)
	}
	a.ID = util.NewUUIDCompact()
	a.CreatedAt = time.Now()
	e.mu.Lock()
	e.automation[a.ID] = a
	e.mu.Unlock()
	// 引擎已启动时，time/cron 类型立即注册调度
	if a.Enabled && e.sched != nil && (a.Trigger.Type == "time" || a.Trigger.Type == "cron") {
		e.schedule(a)
	}
	return a, nil
}

// Remove 删除自动化
func (e *Engine) Remove(id string) {
	e.mu.Lock()
	delete(e.automation, id)
	e.mu.Unlock()
}

// Update 更新自动化
func (e *Engine) Update(a *Automation) {
	e.mu.Lock()
	if old, ok := e.automation[a.ID]; ok {
		a.CreatedAt = old.CreatedAt
		e.automation[a.ID] = a
	}
	e.mu.Unlock()
}

// Get 获取自动化
func (e *Engine) Get(id string) (*Automation, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	a, ok := e.automation[id]
	return a, ok
}

// List 自动化列表
func (e *Engine) List() []*Automation {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []*Automation
	for _, a := range e.automation {
		cp := *a
		out = append(out, &cp)
	}
	return out
}

// Start 为 time/cron 类型触发器注册调度，并后台驱动
func (e *Engine) Start() {
	e.sched = task.NewScheduler(nil)
	e.sched.Start()
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, a := range e.automation {
		if !a.Enabled {
			continue
		}
		e.schedule(a)
	}
}

// Stop 停止调度
func (e *Engine) Stop() {
	if e.sched != nil {
		e.sched.Stop()
	}
}

// schedule 注册单个自动化的定时触发
func (e *Engine) schedule(a *Automation) {
	var spec string
	switch a.Trigger.Type {
	case "time":
		spec = timeTriggerToCron(a.Trigger.At)
	case "cron":
		spec = a.Trigger.Cron
	default:
		return // device 触发器由 OnDeviceEvent 驱动
	}
	if spec == "" {
		return
	}
	_ = e.sched.AddFunc(spec, a.ID, func() {
		e.execute(a.ID)
	})
}

// OnDeviceEvent 设备状态变更事件：匹配 device 类型触发器
func (e *Engine) OnDeviceEvent(deviceID string, status map[string]interface{}) {
	e.mu.RLock()
	var matches []*Automation
	for _, a := range e.automation {
		if !a.Enabled || a.Trigger.Type != "device" || a.Trigger.DeviceID != deviceID {
			continue
		}
		if matchTriggerValue(a.Trigger.Value, status) {
			matches = append(matches, a)
		}
	}
	e.mu.RUnlock()
	for _, a := range matches {
		e.execute(a.ID)
	}
}

// matchTriggerValue 判断状态是否匹配触发值（等值或包含）
func matchTriggerValue(want string, status map[string]interface{}) bool {
	if want == "" {
		return true
	}
	for _, v := range status {
		if fmt.Sprint(v) == want {
			return true
		}
	}
	return false
}

// execute 执行自动化的全部动作
func (e *Engine) execute(id string) {
	a, ok := e.Get(id)
	if !ok || !a.Enabled {
		return
	}
	if e.exec == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, act := range a.Actions {
		if err := e.exec(ctx, act); err != nil {
			continue // 单动作失败不影响后续
		}
	}
}

// timeTriggerToCron 将 HH:MM 转为 5 段 cron 表达式
func timeTriggerToCron(at string) string {
	parts := strings.Split(at, ":")
	if len(parts) != 2 {
		return ""
	}
	return fmt.Sprintf("%s %s * * *", parts[1], parts[0])
}