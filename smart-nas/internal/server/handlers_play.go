package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/api/types"
)

// playCreateTicket POST /api/play/ticket {file_id}
// 创建播放凭证：校验文件存在、读权限与可播放性，返回 token 与视频信息
func (s *Server) playCreateTicket(c *gin.Context) {
	var req struct {
		FileID uint `json:"file_id" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	t, info, err := s.deps.Play.CreateTicket(currentUID(c), req.FileID, c.ClientIP())
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{
		"token": t.Token,
		"url":   "/api/video?file=" + itoa64(req.FileID) + "&token=" + t.Token,
		"video": info,
	}))
}

// playReleaseTicket DELETE /api/play/ticket/:token 释放播放凭证
func (s *Server) playReleaseTicket(c *gin.Context) {
	token := c.Param("token")
	s.deps.Play.ReleaseTicket(token)
	c.JSON(http.StatusOK, types.OK(map[string]interface{}{"released": true}))
}

// playVideoInfo GET /api/video/info?file=xxx
// 查询视频信息（分辨率 / 可播放性），供文件列表渲染与 2K 拦截
func (s *Server) playVideoInfo(c *gin.Context) {
	fileID, err := parseUint(c.Query("file"))
	if err != nil || fileID == 0 {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "缺少有效的文件 ID"))
		return
	}
	info, err := s.deps.Play.VideoInfo(currentUID(c), fileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	c.JSON(http.StatusOK, types.OK(info))
}

// playVideoStream GET /api/video?file=xxx&token=xxx
// 输出视频流（Range 支持）。由播放凭证（Ticket）认证，<video> 标签可直接加载
func (s *Server) playVideoStream(c *gin.Context) {
	fileID, err := parseUint(c.Query("file"))
	if err != nil || fileID == 0 {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, "缺少有效的文件 ID"))
		return
	}
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, types.Fail(types.CodeUnauthorized, "缺少播放凭证"))
		return
	}
	t, valid := s.deps.Play.ValidateTicket(token, c.ClientIP())
	if !valid {
		c.JSON(http.StatusUnauthorized, types.Fail(types.CodeUnauthorized, "播放凭证无效或已过期"))
		return
	}
	if t.FileID != fileID {
		c.JSON(http.StatusForbidden, types.Fail(types.CodeForbidden, "播放凭证与文件不匹配"))
		return
	}
	f, err := s.deps.Storage.GetFileMeta(fileID)
	if err != nil {
		c.JSON(http.StatusNotFound, types.Fail(types.CodeFileNotFound, "视频文件已移动或删除"))
		return
	}
	info, err := s.deps.Play.VideoInfo(t.UserID, fileID)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.Fail(types.CodeBadRequest, err.Error()))
		return
	}
	s.deps.Play.Stream(c, f, info)
}

// itoa64 uint64 转字符串（避免引入 strconv 噪音）
func itoa64(n uint) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}