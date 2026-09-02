// Package transport 传输任务管理：文件上传/下载任务、进度跟踪、WebSocket 推送。
package transport

import (
	"context"
	"errors"
	"sync"
	"time"

	"smart-nas/internal/api/types"
	"smart-nas/internal/storage"
	"smart-nas/internal/util"
)

// FileTransferTask 传输任务
type FileTransferTask struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"` // upload / download
	Status      string    `json:"status"`
	Filename    string    `json:"filename"`
	StoragePath string    `json:"-"`
	TotalSize   int64     `json:"total_size"`
	Uploaded    int64     `json:"uploaded"`
	Speed       int64     `json:"speed"`
	StartedAt   time.Time `json:"started_at"`
	UserID      uint      `json:"-"`
	TusURL      string    `json:"-"`
}

// Publisher 实时推送接口（由 ws.Hub 实现）
type Publisher interface {
	Broadcast(msg types.WSMessage)
	BroadcastToUser(userID uint, msg types.WSMessage)
}

// HookEmitter 插件事件发射接口（由 plugin.Manager 实现）
type HookEmitter interface {
	Emit(ctx context.Context, eventName string, payload interface{}) error
}

// Manager 传输任务管理器
type Manager struct {
	tasks       map[string]*FileTransferTask
	mu          sync.RWMutex
	publisher   Publisher
	storage     *storage.Service
	storageRoot string
	hook        HookEmitter
}

// NewManager 创建传输管理器
func NewManager(storageSvc *storage.Service, storageRoot string, pub Publisher, hook HookEmitter) *Manager {
	return &Manager{
		tasks:       make(map[string]*FileTransferTask),
		publisher:   pub,
		storage:     storageSvc,
		storageRoot: storageRoot,
		hook:        hook,
	}
}

// InitUploadRequest 初始化上传请求
type InitUploadRequest struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	MD5      string `json:"md5,omitempty"`
	ParentID uint   `json:"parent_id"`
	UserID   uint   `json:"-"`
}

// InitUploadResponse 初始化上传响应
type InitUploadResponse struct {
	UploadURL string `json:"upload_url"`
	TaskID    string `json:"task_id"`
	Dedup     bool   `json:"dedup"`
	FileID    uint   `json:"file_id,omitempty"`
}

// InitUpload 初始化上传：先做秒传去重检测，未命中则返回 tus 上传 URL
func (m *Manager) InitUpload(req *InitUploadRequest) (*InitUploadResponse, error) {
	if req.Filename == "" || req.Size <= 0 {
		return nil, errors.New("文件名或大小无效")
	}
	if req.MD5 != "" {
		if fileID, ok := m.storage.CheckDedup(req.MD5, req.Size); ok {
			// 秒传：直接登记元数据关联（软硬链接不做，仅返回已存在文件）
			return &InitUploadResponse{
				UploadURL: "",
				TaskID:    "",
				Dedup:     true,
				FileID:    fileID,
			}, nil
		}
	}
	taskID := util.NewUUIDCompact()
	task := &FileTransferTask{
		ID:        taskID,
		Type:      "upload",
		Status:    "pending",
		Filename:  req.Filename,
		TotalSize: req.Size,
		StartedAt: time.Now(),
		UserID:    req.UserID,
	}
	m.mu.Lock()
	m.tasks[taskID] = task
	m.mu.Unlock()
	return &InitUploadResponse{
		UploadURL: "/files/upload/" + taskID,
		TaskID:    taskID,
		Dedup:     false,
	}, nil
}

// GetTask 获取任务
func (m *Manager) GetTask(taskID string) (*FileTransferTask, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[taskID]
	return t, ok
}

// UpdateProgress 更新上传进度并推送
func (m *Manager) UpdateProgress(taskID string, uploaded int64) {
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	if ok {
		prev := t.Uploaded
		t.Uploaded = uploaded
		elapsed := time.Since(t.StartedAt).Seconds()
		if elapsed > 0 {
			t.Speed = int64(float64(t.Uploaded) / elapsed)
		}
		total := t.TotalSize
		m.mu.Unlock()
		if m.publisher != nil && (prev != uploaded || uploaded >= total) {
			m.publisher.Broadcast(types.NewWSMessage("file_progress", map[string]interface{}{
				"task_id":  taskID,
				"uploaded": uploaded,
				"total":    total,
				"speed":    t.Speed,
			}))
		}
		m.notifyUser(t, "file_progress", uploaded, total)
		return
	}
	m.mu.Unlock()
}

// notifyUser 向任务所属用户推送进度
func (m *Manager) notifyUser(t *FileTransferTask, typ string, uploaded, total int64) {
	if m.publisher == nil {
		return
	}
	m.publisher.BroadcastToUser(t.UserID, types.NewWSMessage(typ, map[string]interface{}{
		"task_id":  t.ID,
		"uploaded": uploaded,
		"total":    total,
		"speed":    t.Speed,
	}))
}

// CompleteTask 任务完成
func (m *Manager) CompleteTask(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[taskID]; ok {
		t.Status = "done"
		t.Uploaded = t.TotalSize
	}
}

// FailTask 任务失败
func (m *Manager) FailTask(taskID string, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[taskID]; ok {
		t.Status = "failed"
	}
	if m.publisher != nil {
		m.publisher.Broadcast(types.NewWSMessage("notification", map[string]interface{}{
			"level":   "error",
			"title":   "上传失败",
			"content": reason,
		}))
	}
}

// ListTasks 任务列表
func (m *Manager) ListTasks(userID uint) []*FileTransferTask {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*FileTransferTask
	for _, t := range m.tasks {
		if t.UserID == userID {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out
}