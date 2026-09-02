package rag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"smart-nas/internal/ai/ollama"
	"smart-nas/internal/config"
	"smart-nas/internal/storage"
	"smart-nas/pkg/logger"
)

// Indexer RAG 索引器：文件文本提取 → 分块 → 向量化 → 入库
type Indexer struct {
	store      VectorStore
	ollama     *ollama.Client
	cfg        config.RAGConfig
	embedModel string
	storage    *storage.Service
}

// NewIndexer 创建索引器；embedModel 为 embedding 模型名（来自 ai.embedding_model）
func NewIndexer(store VectorStore, client *ollama.Client, svc *storage.Service, cfg config.RAGConfig, embedModel string) *Indexer {
	return &Indexer{store: store, ollama: client, cfg: cfg, embedModel: embedModel, storage: svc}
}

// 可提取文本的扩展名集合
var textExts = map[string]bool{
	".txt": true, ".md": true, ".log": true, ".csv": true,
	".json": true, ".toml": true, ".yaml": true, ".yml": true,
	".xml": true, ".html": true, ".css": true, ".js": true,
	".go": true, ".py": true, ".sh": true, ".ini": true, ".conf": true,
}

// IndexFile 索引单个文件；返回是否成功提取文本
func (i *Indexer) IndexFile(ctx context.Context, fileID, userID uint) error {
	f, err := i.storage.GetFileMeta(fileID)
	if err != nil {
		return err
	}
	if f.IsDir {
		return nil
	}
	text, err := extractText(f.StoragePath)
	if err != nil || strings.TrimSpace(text) == "" {
		return nil // 非文本或空内容，跳过
	}
	chunks := chunkText(text, i.cfg.ChunkSize, i.cfg.ChunkOverlap)
	for idx, c := range chunks {
		vec, err := i.ollama.GenerateEmbedding(ctx, i.embedModel, c)
		if err != nil {
			return fmt.Errorf("向量化块 %d 失败: %w", idx, err)
		}
		id := fmt.Sprintf("%d_%d", fileID, idx)
		meta := map[string]interface{}{
			"file_id": fileID, "user_id": userID, "filename": f.Name,
			"chunk_index": idx, "chunk_total": len(chunks),
		}
		if err := i.store.Add(ctx, id, vec, meta, c); err != nil {
			return err
		}
	}
	logger.Info("文件已索引", "file_id", fileID, "chunks", len(chunks))
	return nil
}

// DeleteFile 删除文件的全部向量并持久化
func (i *Indexer) DeleteFile(ctx context.Context, fileID uint) error {
	// InMemoryStore 需按 file_id 过滤后逐条删除
	if ms, ok := i.store.(*InMemoryStore); ok {
		ms.mu.Lock()
		ids := make([]string, 0)
		for id, it := range ms.item {
			if v, ok := it.Metadata["file_id"]; ok && fmt.Sprint(v) == fmt.Sprint(fileID) {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			delete(ms.item, id)
		}
		ms.mu.Unlock()
		_ = ms.Save()
	}
	return nil
}

// Count 已索引向量块数量
func (i *Indexer) Count() int { return i.store.Count() }

// extractText 读取文件文本内容（仅支持常见文本格式，大小上限 2MB）
func extractText(path string) (string, error) {
	ext := strings.ToLower(filepathExt(path))
	if !textExts[ext] {
		return "", errors.New("非文本格式")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	const maxSize = 2 * 1024 * 1024
	if info.Size() > maxSize {
		return "", errors.New("文件过大，跳过索引")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", errors.New("非 UTF-8 文本，跳过")
	}
	return string(data), nil
}

// filepathExt 小写扩展名（避免额外 import path/filepath 重复）
func filepathExt(path string) string {
	idx := strings.LastIndexByte(path, '.')
	if idx < 0 {
		return ""
	}
	name := path[idx:]
	if strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return ""
	}
	return name
}

// chunkText 按最大长度切分文本，相邻块保留 overlap 重叠
func chunkText(text string, size, overlap int) []string {
	if size <= 0 {
		size = 512
	}
	if overlap >= size {
		overlap = size / 4
	}
	runes := []rune(text)
	var chunks []string
	start := 0
	for start < len(runes) {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
		start = end - overlap
	}
	return chunks
}