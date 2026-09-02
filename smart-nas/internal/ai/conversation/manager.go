// Package conversation 管理 AI 对话历史（内存态，可扩展持久化）。
package conversation

import (
	"sync"
	"time"

	"smart-nas/internal/ai/ollama"
	"smart-nas/internal/util"
)

// Message 单条对话消息
type Message struct {
	ID        string               `json:"id"`
	Role      string               `json:"role"` // system / user / assistant / tool
	Content   string               `json:"content"`
	ToolCalls []ollama.ToolCall    `json:"tool_calls,omitempty"`
	ToolName  string               `json:"tool_name,omitempty"` // tool 回传时记录
	Tokens    int                  `json:"tokens"`
	CreatedAt time.Time            `json:"created_at"`
}

// Conversation 会话
type Conversation struct {
	ID        string    `json:"id"`
	UserID    uint      `json:"user_id"`
	Title     string    `json:"title"`
	Messages  []Message `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Manager 对话管理器
type Manager struct {
	mu            sync.RWMutex
	conversations map[string]*Conversation
	maxMessages   int // 保留的最大消息数（裁剪旧消息）
}

// NewManager 创建管理器；maxMessages<=0 表示不裁剪
func NewManager(maxMessages int) *Manager {
	if maxMessages <= 0 {
		maxMessages = 40
	}
	return &Manager{
		conversations: make(map[string]*Conversation),
		maxMessages:   maxMessages,
	}
}

// Create 创建新会话
func (m *Manager) Create(userID uint, title string) *Conversation {
	if title == "" {
		title = "新对话"
	}
	now := time.Now()
	c := &Conversation{
		ID:        util.NewUUID(),
		UserID:    userID,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	m.mu.Lock()
	m.conversations[c.ID] = c
	m.mu.Unlock()
	return c
}

// Get 获取会话
func (m *Manager) Get(id string) (*Conversation, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.conversations[id]
	return c, ok
}

// List 列出某用户的会话（按更新时间倒序）
func (m *Manager) List(userID uint) []*Conversation {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Conversation
	for _, c := range m.conversations {
		if c.UserID == userID {
			cp := *c
			out = append(out, &cp)
		}
	}
	// 简单插入排序（会话量小）
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].UpdatedAt.After(out[j-1].UpdatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Delete 删除会话
func (m *Manager) Delete(id string) {
	m.mu.Lock()
	delete(m.conversations, id)
	m.mu.Unlock()
}

// Count 会话总数（用于清理）
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.conversations)
}

// Append 追加消息（自动裁剪超出上限的最旧消息）
func (m *Manager) Append(id, role, content string, toolCalls []ollama.ToolCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conversations[id]
	if !ok {
		return
	}
	c.Messages = append(c.Messages, Message{
		ID:        util.NewUUIDCompact(),
		Role:      role,
		Content:   content,
		ToolCalls: toolCalls,
		CreatedAt: time.Now(),
	})
	c.UpdatedAt = time.Now()
	if m.maxMessages > 0 && len(c.Messages) > m.maxMessages {
		// 保留最近 maxMessages 条，且至少保留第一条 system
		drop := len(c.Messages) - m.maxMessages
		keep := c.Messages[drop:]
		if len(c.Messages) > 0 && c.Messages[0].Role == "system" && drop > 0 {
			keep = append(c.Messages[:1], keep...)
		}
		c.Messages = keep
	}
}

// History 导出为 Ollama 消息列表（裁剪逻辑见 Append）
func (m *Manager) History(id string) []ollama.ChatMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.conversations[id]
	if !ok {
		return nil
	}
	messages := make([]ollama.ChatMessage, 0, len(c.Messages))
	for _, msg := range c.Messages {
		messages = append(messages, ollama.ChatMessage{
			Role:      msg.Role,
			Content:   msg.Content,
			ToolCalls: msg.ToolCalls,
		})
	}
	return messages
}