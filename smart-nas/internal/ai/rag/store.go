// Package rag 检索增强生成：向量存储、内容索引、语义检索。
//
// 说明：文档选用 chromem-go / Qdrant，但当前离线环境不可用；
// 这里实现 VectorStore 接口（内存 + 文件持久化 + 余弦相似度），
// 接口签名与文档一致，后续可平滑替换为 Qdrant 适配器。
package rag

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Chunk 检索结果片段
type Chunk struct {
	ID       string                 `json:"id"`
	Content  string                 `json:"content"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Score    float64                `json:"score"`
}

// VectorStore 向量存储抽象接口
type VectorStore interface {
	Add(ctx context.Context, id string, vector []float32, metadata map[string]interface{}, content string) error
	Query(ctx context.Context, vector []float32, topK int, filter map[string]interface{}) ([]Chunk, error)
	Delete(ctx context.Context, id string) error
	Count() int
	Close() error
}

// memItem 内存向量条目
type memItem struct {
	ID       string                 `json:"id"`
	Vector   []float32              `json:"vector"`
	Content  string                 `json:"content"`
	Metadata map[string]interface{} `json:"metadata"`
}

// InMemoryStore 内存向量存储（可持久化到 JSON 文件）
type InMemoryStore struct {
	mu   sync.RWMutex
	item map[string]*memItem
	file string // 空表示不持久化
}

// NewInMemoryStore 创建内存向量存储；file 非空时从文件加载并支持 Save()
func NewInMemoryStore(file string) (*InMemoryStore, error) {
	s := &InMemoryStore{item: make(map[string]*memItem), file: file}
	if file != "" {
		if err := s.load(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *InMemoryStore) load() error {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var items []*memItem
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	for _, it := range items {
		s.item[it.ID] = it
	}
	return nil
}

// Save 持久化到文件（原子写：先写临时文件再 rename）
func (s *InMemoryStore) Save() error {
	if s.file == "" {
		return nil
	}
	s.mu.RLock()
	items := make([]*memItem, 0, len(s.item))
	for _, it := range s.item {
		items = append(items, it)
	}
	s.mu.RUnlock()
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return err
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.file)
}

// Add 添加向量
func (s *InMemoryStore) Add(_ context.Context, id string, vector []float32, metadata map[string]interface{}, content string) error {
	s.mu.Lock()
	s.item[id] = &memItem{ID: id, Vector: vector, Content: content, Metadata: metadata}
	s.mu.Unlock()
	return nil
}

// Query 余弦相似度检索，支持按 metadata 过滤（仅支持等值匹配）
func (s *InMemoryStore) Query(_ context.Context, vector []float32, topK int, filter map[string]interface{}) ([]Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type scored struct {
		chunk Chunk
		score float64
	}
	var results []scored
	for _, it := range s.item {
		if !matchFilter(it.Metadata, filter) {
			continue
		}
		sim := cosine(vector, it.Vector)
		results = append(results, scored{chunk: Chunk{
			ID: it.ID, Content: it.Content, Metadata: it.Metadata, Score: sim,
		}, score: sim})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })
	if topK <= 0 || topK > len(results) {
		topK = len(results)
	}
	out := make([]Chunk, 0, topK)
	for _, r := range results[:topK] {
		out = append(out, r.chunk)
	}
	return out, nil
}

// Delete 删除向量
func (s *InMemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.item, id)
	s.mu.Unlock()
	return nil
}

// Count 向量数量
func (s *InMemoryStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.item)
}

// Close 关闭时持久化
func (s *InMemoryStore) Close() error {
	return s.Save()
}

// matchFilter metadata 等值过滤
func matchFilter(meta map[string]interface{}, filter map[string]interface{}) bool {
	for k, want := range filter {
		v, ok := meta[k]
		if !ok {
			return false
		}
		if want == nil {
			continue
		}
		wantStr, _ := json.Marshal(want)
		vStr, _ := json.Marshal(v)
		if string(wantStr) != string(vStr) {
			return false
		}
	}
	return true
}

// cosine 向量余弦相似度
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}