package device

import (
	"sync"
	"time"
)

// Registry 设备注册表（内存态，可扩展持久化）
type Registry struct {
	mu      sync.RWMutex
	devices map[string]*Device
}

// NewRegistry 创建设备注册表
func NewRegistry() *Registry {
	return &Registry{devices: make(map[string]*Device)}
}

// Upsert 新增或更新设备
func (r *Registry) Upsert(d *Device) {
	if d == nil {
		return
	}
	r.mu.Lock()
	old, exists := r.devices[d.ID]
	if !exists {
		d.CreatedAt = time.Now()
		d.LastSeen = time.Now()
	} else {
		// 保留创建时间
		d.CreatedAt = old.CreatedAt
	}
	r.devices[d.ID] = d
	r.mu.Unlock()
}

// Get 获取设备
func (r *Registry) Get(id string) (*Device, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[id]
	return d, ok
}

// Remove 删除设备
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	delete(r.devices, id)
	r.mu.Unlock()
}

// List 按房间/类型过滤列出设备；room/typ 为空表示不过滤
func (r *Registry) List(room, typ string) []*Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Device
	for _, d := range r.devices {
		if room != "" && d.Room != room {
			continue
		}
		if typ != "" && d.Type != typ {
			continue
		}
		cp := *d
		out = append(out, &cp)
	}
	return out
}

// UpdateStatus 更新设备在线状态与状态字段，并刷新心跳时间
func (r *Registry) UpdateStatus(id string, online bool, kv map[string]interface{}) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return false
	}
	d.Online = online
	d.LastSeen = time.Now()
	if d.Status == nil {
		d.Status = make(map[string]interface{})
	}
	for k, v := range kv {
		d.Status[k] = v
	}
	return true
}

// Touch 刷新设备心跳时间
func (r *Registry) Touch(id string) {
	r.mu.Lock()
	if d, ok := r.devices[id]; ok {
		d.LastSeen = time.Now()
	}
	r.mu.Unlock()
}

// Count 设备数量
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.devices)
}