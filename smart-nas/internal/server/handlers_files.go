package server

import (
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/api/types"
	"smart-nas/internal/storage"
	"smart-nas/internal/transport"
	"smart-nas/pkg/logger"
)

// registerFileRoutes 注册文件 / 存储 / 共享相关路由
func (s *Server) registerFileRoutes(authed *gin.RouterGroup) {
	r := authed.Group("/files")
	r.GET("", s.listFiles)
	r.GET("/trash", s.listTrash)
	r.POST("/trash/purge", s.trashPurge)
	r.POST("/trash/clear", s.trashClear)
	r.POST("/trash/restore-all", s.trashRestoreAll)
	r.GET("/:id", s.fileDetail)
	r.POST("/mkdir", s.mkdir)
	r.POST("/upload/init", s.uploadInit)
	r.GET("/:id/download", s.download)
	r.PUT("/:id/rename", s.rename)
	r.POST("/move", s.move)
	r.POST("/copy", s.copy)
	r.DELETE("", s.delete)
	r.POST("/restore", s.restore)
	r.POST("/:id/share", s.createShare)
	r.GET("/:id/versions", s.listVersions)

	authed.GET("/storage/stats", s.storageStats)
}

// listFiles GET /api/files?parent_id=&keyword=
func (s *Server) listFiles(c *gin.Context) {
	var parentID uint
	if p := c.Query("parent_id"); p != "" {
		if n, err := parseUint(p); err == nil {
			parentID = n
		}
	}
	files, err := s.deps.Storage.ListFiles(currentUID(c), parentID, c.Query("keyword"))
	if err != nil {
		errJSON(c, err)
		return
	}
	out := make([]*storage.FileMeta, 0, len(files))
	for i := range files {
		out = append(out, files[i].Public())
	}
	c.JSON(http.StatusOK, types.OK(out))
}

// fileDetail GET /api/files/:id
func (s *Server) fileDetail(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的文件 ID"))
		return
	}
	f, err := s.deps.Storage.GetFileMeta(id)
	if err != nil {
		c.JSON(http.StatusNotFound, types.Fail(types.CodeFileNotFound, "文件不存在"))
		return
	}
	if !s.deps.Storage.CheckAccess(currentUID(c), id, false) {
		// 权限不足时说明缺读还是缺写；不存在/被屏蔽仍按 404 处理
		if reason := s.deps.Storage.DenyReason(currentUID(c), id, false); reason != "" {
			c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, reason))
			return
		}
		c.JSON(http.StatusNotFound, types.Fail(types.CodeFileNotFound, "文件不存在"))
		return
	}
	c.JSON(http.StatusOK, types.OK(f.PublicDetail()))
}

// mkdir POST /api/files/mkdir {name, parent_id}
func (s *Server) mkdir(c *gin.Context) {
	var req struct {
		Name     string `json:"name" binding:"required"`
		ParentID uint   `json:"parent_id"`
	}
	if !bindJSON(c, &req) {
		return
	}
	f, err := s.deps.Storage.CreateDir(currentUID(c), req.Name, req.ParentID)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(f.Public()))
}

// uploadInit POST /api/files/upload/init {filename,size,md5,parent_id}
// 秒传去重检测；未命中返回 tus 上传地址
func (s *Server) uploadInit(c *gin.Context) {
	var req transport.InitUploadRequest
	if !bindJSON(c, &req) {
		return
	}
	req.UserID = currentUID(c)
	res, err := s.deps.Transport.InitUpload(&req)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(res))
}

// download GET /api/files/:id/download (支持 Range)
func (s *Server) download(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的文件 ID"))
		return
	}
	f, err := s.deps.Storage.GetFileMeta(id)
	if err != nil {
		c.JSON(http.StatusNotFound, types.Fail(types.CodeFileNotFound, "文件不存在"))
		return
	}
	if !s.deps.Storage.CheckAccess(currentUID(c), id, false) {
		if reason := s.deps.Storage.DenyReason(currentUID(c), id, false); reason != "" {
			c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, reason))
			return
		}
		c.JSON(http.StatusNotFound, types.Fail(types.CodeFileNotFound, "文件不存在"))
		return
	}
	if err := s.deps.Transport.DownloadFile(c.Writer, c.Request, f); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
}

// rename PUT /api/files/:id/rename {name}
func (s *Server) rename(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的文件 ID"))
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if err := s.deps.Storage.RenameFile(currentUID(c), id, req.Name); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"id": id, "name": req.Name}))
}

// move POST /api/files/move {ids, target_dir_id}
func (s *Server) move(c *gin.Context) {
	var req struct {
		IDs         []uint `json:"ids"`
		TargetDirID uint   `json:"target_dir_id"`
	}
	if !bindJSON(c, &req) || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "参数错误"))
		return
	}
	if err := s.deps.Storage.MoveFile(currentUID(c), req.IDs, req.TargetDirID); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"moved": len(req.IDs)}))
}

// copy POST /api/files/copy {ids, target_dir_id}
func (s *Server) copy(c *gin.Context) {
	var req struct {
		IDs         []uint `json:"ids"`
		TargetDirID uint   `json:"target_dir_id"`
	}
	if !bindJSON(c, &req) || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "参数错误"))
		return
	}
	if err := s.deps.Storage.CopyFile(currentUID(c), req.IDs, req.TargetDirID); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"copied": len(req.IDs)}))
}

// delete DELETE /api/files body {ids}
func (s *Server) delete(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if !bindJSON(c, &req) || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "参数错误"))
		return
	}
	if err := s.deps.Storage.DeleteFile(currentUID(c), req.IDs); err != nil {
		errJSON(c, err)
		return
	}
	// 联动清理 RAG 索引
	for _, id := range req.IDs {
		if s.deps.AI != nil {
			s.deps.AI.DeleteFileIndex(id)
		}
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"deleted": len(req.IDs)}))
}

// listTrash GET /api/files/trash
func (s *Server) listTrash(c *gin.Context) {
	files := s.deps.Storage.ListTrash(currentUID(c))
	c.JSON(http.StatusOK, types.OK(storage.PublicList(files)))
}

// trashPurge POST /api/files/trash/purge {ids} 从回收站物理删除（不可恢复）
func (s *Server) trashPurge(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if !bindJSON(c, &req) || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "参数错误"))
		return
	}
	n, err := s.deps.Storage.PurgeFiles(currentUID(c), req.IDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"purged": n}))
}

// trashClear POST /api/files/trash/clear 一键清空回收站
func (s *Server) trashClear(c *gin.Context) {
	n, err := s.deps.Storage.ClearTrash(currentUID(c))
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"cleared": n}))
}

// trashRestoreAll POST /api/files/trash/restore-all 一键还原回收站全部条目
func (s *Server) trashRestoreAll(c *gin.Context) {
	n, err := s.deps.Storage.RestoreAllTrash(currentUID(c))
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"restored": n}))
}

// restore POST /api/files/restore {ids}
func (s *Server) restore(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if !bindJSON(c, &req) || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "参数错误"))
		return
	}
	if err := s.deps.Storage.RestoreFromTrash(currentUID(c), req.IDs); err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"restored": len(req.IDs)}))
}

// createShare POST /api/files/:id/share {expire_hours, password}
func (s *Server) createShare(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的文件 ID"))
		return
	}
	var req struct {
		ExpireHours int    `json:"expire_hours"`
		Password    string `json:"password"`
	}
	if !bindJSON(c, &req) {
		return
	}
	f, token, err := s.deps.Storage.CreateShareLink(currentUID(c), id, req.ExpireHours, req.Password)
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{
		"token": token, "file_id": f.ID, "name": f.Name, "url": "/api/files/share/" + token,
	}))
}

// shareDownload GET /api/files/share/:token?password= （公开，无认证）
func (s *Server) shareDownload(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "缺少共享令牌"))
		return
	}
	f, err := s.deps.Storage.ResolveShare(token, c.Query("password"))
	if err != nil {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, err.Error()))
		return
	}
	rc, size, err := s.deps.Storage.OpenShare(f)
	if err != nil {
		errJSON(c, err)
		return
	}
	defer rc.Close()
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+urlEncode(f.Name))
	c.Header("Content-Length", fmt.Sprintf("%d", size))
	if _, err := io.Copy(c.Writer, rc); err != nil {
		logger.Warn("共享文件下载中断", "file_id", f.ID, "error", err)
	}
}

// storageStats GET /api/storage/stats
func (s *Server) storageStats(c *gin.Context) {
	st, err := s.deps.Storage.GetStorageStats(currentUID(c))
	if err != nil {
		errJSON(c, err)
		return
	}
	c.JSON(http.StatusOK, types.OK(st))
}

// listVersions GET /api/files/:id/versions
func (s *Server) listVersions(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "无效的文件 ID"))
		return
	}
	if !s.deps.Storage.CheckAccess(currentUID(c), id, false) {
		if reason := s.deps.Storage.DenyReason(currentUID(c), id, false); reason != "" {
			c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, reason))
			return
		}
		c.JSON(http.StatusNotFound, types.Fail(types.CodeFileNotFound, "文件不存在"))
		return
	}
	versions := s.deps.Storage.ListVersions(id)
	out := make([]*storage.FileVersion, 0, len(versions))
	for i := range versions {
		out = append(out, versions[i].Public())
	}
	c.JSON(http.StatusOK, types.OK(out))
}

// ---- 辅助 ----

func parseUint(s string) (uint, error) {
	var n uint
	if _, err := fmt.Sscan(s, &n); err != nil {
		return 0, err
	}
	return n, nil
}

// urlEncode URL 编码文件名（非 ASCII 与保留字符转 %XX）
func urlEncode(name string) string {
	const hex = "0123456789ABCDEF"
	var out []byte
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			out = append(out, c)
		} else {
			out = append(out, '%', hex[c>>4], hex[c&0xf])
		}
	}
	return string(out)
}