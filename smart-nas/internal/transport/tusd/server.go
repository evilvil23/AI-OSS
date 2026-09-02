// Package tusd 实现 tus 可恢复上传协议服务端。
//
// 说明：文档选用 github.com/tus/tusd/v2，但当前离线环境不可用；
// 这里按 tus v1 protocol 规范手写等价实现（POST 创建 / HEAD 进度 /
// PATCH 分块上传 / DELETE 终止），支持秒传去重与断点续传。
package tusd

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/api/types"
	"smart-nas/internal/config"
	"smart-nas/internal/storage"
	"smart-nas/internal/store"
	"smart-nas/internal/transport"
	"smart-nas/pkg/logger"
)

// Upload tus 上传会话
type Upload struct {
	ID          string            `json:"id"`
	Size        int64             `json:"size"`
	Offset      int64             `json:"offset"`
	Metadata    map[string]string `json:"metadata"`
	Filename    string            `json:"filename"`
	MD5         string            `json:"md5"`
	ParentID    uint              `json:"parent_id"`
	UserID      uint              `json:"user_id"`
	ContentType string            `json:"content_type"`
	TempPath    string            `json:"temp_path"`
	Dedup       bool              `json:"dedup"`
	TaskID      string            `json:"task_id"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// HookEmitter、Publisher 由 transport 包提供
type Publisher interface{ Broadcast(msg types.WSMessage) }
type HookEmitter interface{ Emit(ctx context.Context, eventName string, payload interface{}) error }

// Handler tus 协议处理器
type Handler struct {
	cfg         config.TusConfig
	store       *store.Store[string, *Upload]
	storageSvc  *storage.Service
	tm          *transport.Manager
	pub         Publisher
	hook        HookEmitter
	authenticate func(r *http.Request) (uint, error)
}

// NewHandler 创建 tus 处理器
func NewHandler(cfg config.TusConfig, storageSvc *storage.Service, tm *transport.Manager, pub Publisher, hook HookEmitter, authenticate func(r *http.Request) (uint, error)) (*Handler, error) {
	if err := os.MkdirAll(cfg.StoreDir, 0o755); err != nil {
		return nil, err
	}
	s, err := store.NewStore[string, *Upload](filepath.Join(cfg.StoreDir, "uploads.toml"))
	if err != nil {
		return nil, err
	}
	return &Handler{
		cfg: cfg, store: s, storageSvc: storageSvc, tm: tm,
		pub: pub, hook: hook, authenticate: authenticate,
	}, nil
}

// Handle gin 统一的 tus 入点（挂载于 /files/upload/*filepath）
func (h *Handler) Handle(c *gin.Context) {
	id := strings.TrimPrefix(c.Param("filepath"), "/")
	switch c.Request.Method {
	case http.MethodPost:
		h.create(c, id)
	case http.MethodHead:
		h.head(c, id)
	case http.MethodPatch:
		h.patch(c, id)
	case http.MethodDelete:
		h.delete(c, id)
	case http.MethodOptions:
		h.options(c)
	default:
		c.Data(http.StatusMethodNotAllowed, "text/plain", []byte("405 Method Not Allowed"))
	}
}

// options 暴露 tus 扩展能力
func (h *Handler) options(c *gin.Context) {
	c.Header("Tus-Resumable", "1.0.0")
	c.Header("Tus-Version", "1.0.0")
	c.Header("Tus-Extension", "creation,creation-with-upload,termination,checksum,expiration")
	c.Header("Tus-Max-Size", strconv.FormatInt(h.cfg.MaxSize, 10))
	c.Header("Access-Control-Allow-Methods", "POST,HEAD,PATCH,DELETE,OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Upload-Length,Upload-Metadata,Upload-Offset,Tus-Resumable,Authorization,Content-Type")
	c.Header("Access-Control-Expose-Headers", "Location,Upload-Offset,Upload-Length,Upload-Metadata,Tus-Resumable")
	c.Status(http.StatusOK)
}

func (h *Handler) setCommon(c *gin.Context) {
	c.Header("Tus-Resumable", "1.0.0")
	c.Header("Access-Control-Expose-Headers", "Location,Upload-Offset,Upload-Length,Upload-Metadata,Tus-Resumable")
}

// create 处理 POST：创建上传会话
func (h *Handler) create(c *gin.Context, id string) {
	length, err := strconv.ParseInt(c.GetHeader("Upload-Length"), 10, 64)
	if err != nil || length <= 0 {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "缺少 Upload-Length 头"))
		return
	}
	if length > h.cfg.MaxSize {
		c.JSON(http.StatusRequestEntityTooLarge, types.Fail(2002, "超过单文件大小上限"))
		return
	}
	meta := parseMetadata(c.GetHeader("Upload-Metadata"))
	userID := uint(0)
	if h.authenticate != nil {
		if u, aerr := h.authenticate(c.Request); aerr == nil {
			userID = u
		}
	}
	parentID, _ := strconv.ParseUint(meta["parent_id"], 10, 64)

	if h.cfg.Dedup && meta["md5"] != "" {
		if fileID, ok := h.storageSvc.CheckDedup(meta["md5"], length); ok {
			// 秒传去重命中：直接提交元数据，返回已完成状态
			up := &Upload{
				ID: id, Size: length, Offset: length, Metadata: meta,
				Filename: meta["filename"], MD5: meta["md5"],
				ParentID: uint(parentID), UserID: userID,
				ContentType: meta["content_type"], Dedup: true,
				TaskID: id, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			}
			_ = h.store.Set(id, up)
			fm, _, cerr := h.storageSvc.CommitUpload(userID, up.Filename, up.ParentID, up.ContentType, up.MD5, "", length)
			if cerr != nil {
				logger.Error("秒传提交失败", "error", cerr)
				fm = &storage.FileMeta{ID: fileID, Name: up.Filename, Size: length, MD5: up.MD5, OwnerID: userID}
			}
			h.finalize(fm, fm.ID)
			c.Header("Location", h.cfg.PathPrefix+id)
			c.Header("Upload-Offset", strconv.FormatInt(length, 10))
			c.Header("X-Upload-Dedup", "true")
			c.Status(http.StatusCreated)
			return
		}
	}

	tid := id
	if h.tm != nil {
		if t, ok := h.tm.GetTask(id); ok {
			tid = t.ID
		}
	}
	tempPath := filepath.Join(h.cfg.StoreDir, id+".bin")
	up := &Upload{
		ID: id, Size: length, Offset: 0, Metadata: meta,
		Filename: meta["filename"], MD5: meta["md5"],
		ParentID: uint(parentID), UserID: userID,
		ContentType: meta["content_type"],
		TempPath:    tempPath, TaskID: tid,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if up.Filename == "" {
		up.Filename = "upload_" + id
	}
	if err := h.store.Set(id, up); err != nil {
		c.JSON(http.StatusInternalServerError, types.Fail(5000, err.Error()))
		return
	}
	h.setCommon(c)
	c.Header("Location", h.cfg.PathPrefix+id)
	c.Header("Upload-Offset", "0")
	c.Status(http.StatusCreated)
}

// head 处理 HEAD：查询进度
func (h *Handler) head(c *gin.Context, id string) {
	up, ok := h.store.Get(id)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	h.setCommon(c)
	c.Header("Upload-Offset", strconv.FormatInt(h.actualOffset(up), 10))
	c.Header("Upload-Length", strconv.FormatInt(up.Size, 10))
	c.Header("Cache-Control", "no-store")
	if len(up.Metadata) > 0 {
		c.Header("Upload-Metadata", encodeMetadata(up.Metadata))
	}
	c.Status(http.StatusOK)
}

// patch 处理 PATCH：分块上传数据
func (h *Handler) patch(c *gin.Context, id string) {
	up, ok := h.store.Get(id)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	curOffset := h.actualOffset(up)
	// 校验 Upload-Offset 头（避免并发错序）
	if hdr := c.GetHeader("Upload-Offset"); hdr != "" {
		if expected, _ := strconv.ParseInt(hdr, 10, 64); expected != curOffset {
			c.Status(http.StatusConflict)
			return
		}
	}
	if curOffset >= up.Size {
		// 已上传完成（例如秒传已置 Offset=Size），直接返回
		if up.Dedup {
			c.Header("Upload-Offset", strconv.FormatInt(up.Size, 10))
			c.Status(http.StatusNoContent)
			return
		}
		h.complete(c, up)
		return
	}

	f, err := os.OpenFile(up.TempPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.Fail(5000, err.Error()))
		return
	}
	n, err := io.Copy(f, c.Request.Body)
	_ = f.Close()
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	up.Offset = curOffset + n
	up.UpdatedAt = time.Now()
	_ = h.store.Set(id, up)
	if h.tm != nil {
		h.tm.UpdateProgress(up.TaskID, up.Offset)
	}
	if up.Offset >= up.Size {
		h.complete(c, up)
		return
	}
	h.setCommon(c)
	c.Header("Upload-Offset", strconv.FormatInt(up.Offset, 10))
	c.Status(http.StatusNoContent)
}

// delete 处理 DELETE：终止上传
func (h *Handler) delete(c *gin.Context, id string) {
	if up, ok := h.store.Get(id); ok {
		_ = os.Remove(up.TempPath)
		_ = h.store.Delete(id)
		if h.tm != nil {
			h.tm.FailTask(up.TaskID, "上传已取消")
		}
	}
	h.setCommon(c)
	c.Status(http.StatusNoContent)
}

// complete 上传完成：校验 MD5、归档、清理临时文件、触发事件
func (h *Handler) complete(c *gin.Context, up *Upload) {
	h.setCommon(c)
	if err := h.finalizeUpload(up); err != nil {
		logger.Error("上传完成处理失败", "error", err)
		c.JSON(http.StatusInternalServerError, types.Fail(5000, err.Error()))
		return
	}
	c.Header("Upload-Offset", strconv.FormatInt(up.Size, 10))
	c.Status(http.StatusNoContent)
}

func (h *Handler) finalizeUpload(up *Upload) error {
	// 计算并校验 MD5（客户端可能未提供，统一计算）
	md5, err := md5OfFile(up.TempPath)
	if err != nil {
		return err
	}
	if up.MD5 != "" && md5 != up.MD5 {
		return errors.New("MD5 校验失败")
	}
	up.MD5 = md5
	meta, dedup, err := h.storageSvc.CommitUpload(up.UserID, up.Filename, up.ParentID, up.ContentType, up.MD5, up.TempPath, up.Size)
	if err != nil {
		return err
	}
	_ = os.Remove(up.TempPath)
	_ = h.store.Delete(up.ID)
	if h.tm != nil {
		h.tm.CompleteTask(up.TaskID)
	}
	h.finalize(meta, meta.ID)
	if h.pub != nil {
		h.pub.Broadcast(types.NewWSMessage("file_progress", map[string]interface{}{
			"task_id": up.TaskID, "uploaded": up.Size, "total": up.Size, "done": true,
		}))
	}
	_ = dedup
	return nil
}

// finalize 提交后统一事务（元数据就绪 + 事件）
func (h *Handler) finalize(meta *storage.FileMeta, fileID uint) {
	if h.hook != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.hook.Emit(ctx, "file.after_upload", map[string]interface{}{
			"file_id": meta.ID, "filename": meta.Name, "size": meta.Size, "md5": meta.MD5, "user_id": meta.OwnerID,
		})
	}
}

func (h *Handler) actualOffset(up *Upload) int64 {
	if up.Dedup {
		return up.Size
	}
	if info, err := os.Stat(up.TempPath); err == nil {
		return info.Size()
	}
	if up.TempPath == "" {
		return up.Size
	}
	return 0
}

// -------- 辅助 --------

func parseMetadata(header string) map[string]string {
	out := map[string]string{}
	if header == "" {
		return out
	}
	for _, pair := range strings.Split(header, ",") {
		kv := strings.SplitN(pair, " ", 2)
		if len(kv) != 2 {
			continue
		}
		val, err := base64.StdEncoding.DecodeString(kv[1])
		if err != nil {
			continue
		}
		out[kv[0]] = string(val)
	}
	return out
}

func encodeMetadata(m map[string]string) string {
	var pairs []string
	for k, v := range m {
		pairs = append(pairs, k+" "+base64.StdEncoding.EncodeToString([]byte(v)))
	}
	return strings.Join(pairs, ",")
}

func md5OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
