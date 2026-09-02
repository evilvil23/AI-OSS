package rag

import (
	"context"
	"fmt"

	"smart-nas/internal/ai/ollama"
	"smart-nas/internal/config"
)

// Retriever RAG 检索器：查询向量化 → 向量检索 → 结果
type Retriever struct {
	store       VectorStore
	ollama      *ollama.Client
	cfg         config.RAGConfig
	embedModel  string
}

// NewRetriever 创建检索器；embedModel 为 embedding 模型名（来自 ai.embedding_model）
func NewRetriever(store VectorStore, client *ollama.Client, cfg config.RAGConfig, embedModel string) *Retriever {
	return &Retriever{store: store, ollama: client, cfg: cfg, embedModel: embedModel}
}

// Search 语义检索；userID 为 0 时不按用户过滤
func (r *Retriever) Search(ctx context.Context, query string, userID uint, topK int) ([]Chunk, error) {
	if topK <= 0 {
		topK = r.cfg.TopK
	}
	vec, err := r.ollama.GenerateEmbedding(ctx, r.embedModel, query)
	if err != nil {
		return nil, fmt.Errorf("生成查询向量失败: %w", err)
	}
	var filter map[string]interface{}
	if userID > 0 {
		filter = map[string]interface{}{"user_id": userID}
	}
	return r.store.Query(ctx, vec, topK, filter)
}

// FormatContext 将检索结果组装为系统提示上下文
func (r *Retriever) FormatContext(chunks []Chunk, userQuery string) string {
	if len(chunks) == 0 {
		return ""
	}
	var content string
	for i, c := range chunks {
		content += fmt.Sprintf("[%d] score=%.2f\n%s\n", i+1, c.Score, c.Content)
	}
	return fmt.Sprintf("以下是从用户文件中检索到的相关内容，请基于这些内容回答：\n%s\n---\n用户问题：%s", content, userQuery)
}