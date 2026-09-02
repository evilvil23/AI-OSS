package play

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"smart-nas/internal/storage"
	"smart-nas/pkg/logger"
)

// runProbe 调用 ffprobe 获取视频流宽高与编码（JSON 输出，解析稳定）
func runProbe(ffprobe, path string) (probeResult, error) {
	cmd := exec.Command(ffprobe,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,codec_name,duration",
		"-of", "json",
		path,
	)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if errBuf.Len() > 0 {
			return probeResult{}, errors.New(strings.TrimSpace(errBuf.String()))
		}
		return probeResult{}, err
	}
	return parseProbeOutput(out.Bytes())
}

// parseProbeOutput 解析 ffprobe JSON 输出（纯函数，便于单元测试）
func parseProbeOutput(data []byte) (probeResult, error) {
	var parsed struct {
		Streams []struct {
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Codec    string `json:"codec_name"`
			Duration string `json:"duration"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return probeResult{}, err
	}
	if len(parsed.Streams) == 0 {
		return probeResult{}, errors.New("未检测到视频流")
	}
	st := parsed.Streams[0]
	pr := probeResult{Width: st.Width, Height: st.Height, Codec: st.Codec}
	if st.Duration != "" {
		if d, err := strconv.ParseFloat(st.Duration, 64); err == nil {
			pr.Duration = d
		}
	}
	return pr, nil
}

// Stream 根据播放模式输出视频流（调用方已完成凭证校验）
func (s *Service) Stream(c *gin.Context, f *storage.FileMeta, info VideoInfo) {
	switch info.Mode {
	case ModeDirect:
		contentType := "video/mp4"
		if info.IsAudio {
			contentType = audioMime(f.Name)
		}
		if err := s.serveFile(c, f, contentType); err != nil {
			logger.Warn("视频直接播放失败", "file_id", f.ID, "error", err)
		}
	case ModeRemux:
		out, err := s.ensureRemux(f)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 5000, "message": "视频文件无法转封装，请检查文件是否完整或尝试下载后播放"})
			return
		}
		if err := s.serveFile(c, f, "video/mp4", out); err != nil {
			logger.Warn("转封装视频播放中断", "file_id", f.ID, "error", err)
		}
	case ModeTranscode:
		s.transcodeStream(c, f)
	default:
		c.JSON(http.StatusForbidden, gin.H{"code": 1002, "message": info.reasonText()})
	}
}

// serveFile 以 HTTP Range 方式输出文件（http.ServeContent 原生支持 206 / Content-Range）
// path 为空时使用文件元数据的真实路径；remux 模式传入转封装产物路径并伪装原文件名
func (s *Service) serveFile(c *gin.Context, f *storage.FileMeta, contentType string, paths ...string) error {
	path := f.StoragePath
	if len(paths) > 0 && paths[0] != "" {
		path = paths[0]
	}
	if path == "" {
		return errors.New("文件路径为空")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if stat.IsDir() {
		return errors.New("目录不支持播放")
	}
	name := f.Name
	if len(paths) > 0 && paths[0] != "" {
		// 转封装产物为随机文件名，伪装原始文件名以便浏览器正确识别类型
		name = strings.TrimSuffix(f.Name, filepath.Ext(f.Name)) + ".mp4"
	}
	c.Header("Content-Type", contentType)
	c.Header("Accept-Ranges", "bytes")
	// remux 产物不做下载附件，直接内联播放
	c.Header("Content-Disposition", "inline; filename*=UTF-8''"+urlEncode(name))
	http.ServeContent(c.Writer, c.Request, name, stat.ModTime(), file)
	return nil
}

// urlEncode 简单 URL 编码文件名（非 ASCII 与安全集外字符转 %XX）
func urlEncode(name string) string {
	const hex = "0123456789ABCDEF"
	var out []byte
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			out = append(out, ch)
		} else {
			out = append(out, '%', hex[ch>>4], hex[ch&0xf])
		}
	}
	return string(out)
}

// audioMime 根据音频扩展名返回 MIME 类型（供直接 Range 流使用）
func audioMime(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".wav":
		return "audio/wav"
	case ".aac":
		return "audio/aac"
	case ".ogg":
		return "audio/ogg"
	case ".m4a":
		return "audio/mp4"
	case ".opus":
		return "audio/opus"
	case ".wma":
		return "audio/x-ms-wma"
	default:
		return "audio/mpeg"
	}
}

// remuxKey 转封装缓存键：路径+大小+修改时间
func remuxKey(f *storage.FileMeta) string {
	p := f.StoragePath
	if fi, err := os.Stat(p); err == nil {
		return fmt.Sprintf("%x", hashKey(fmt.Sprintf("%s|%d|%d", p, fi.Size(), fi.ModTime().UnixNano())))
	}
	return fmt.Sprintf("%x", hashKey(fmt.Sprintf("%s|%d", p, f.Size)))
}

func hashKey(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// ensureRemux 确保转封装产物存在：已缓存直接返回；进行中则等待；否则按并发上限启动任务。
// 并发重复请求共享同一任务，避免对同一文件重复转封装。
func (s *Service) ensureRemux(f *storage.FileMeta) (string, error) {
	key := remuxKey(f)
	out := filepath.Join(s.cacheDir, key+".mp4")
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		return out, nil
	}

	s.mu.Lock()
	if t, ok := s.remuxTasks[key]; ok {
		s.mu.Unlock()
		<-t.done
		if t.err != nil {
			return "", t.err
		}
		return out, nil
	}
	t := &remuxTask{done: make(chan struct{})}
	s.remuxTasks[key] = t
	s.mu.Unlock()

	t.err = s.runRemux(f.StoragePath, out)
	close(t.done)
	s.mu.Lock()
	delete(s.remuxTasks, key)
	s.mu.Unlock()
	if t.err != nil {
		return "", t.err
	}
	logger.Info("转封装完成", "file_id", f.ID, "name", f.Name)
	return out, nil
}

// runRemux 转封装：-c copy 拷贝流，仅换封装容器（I/O 密集，CPU 占用低）
func (s *Service) runRemux(src, dst string) error {
	s.remuxSem <- struct{}{}
	defer func() { <-s.remuxSem }()

	tmp := dst + ".tmp." + strconv.Itoa(os.Getpid())
	defer os.Remove(tmp)
	// 2K 及以下仅转封装；产物统一为 frag mp4（faststart 便于浏览器立即播放 + 拖拽定位）
	cmd := exec.Command(s.cfg.FFmpegPath,
		"-y",
		"-i", src,
		"-map", "0:v:0?", "-map", "0:a:0?",
		"-c", "copy",
		"-movflags", "+faststart",
		"-f", "mp4",
		tmp,
	)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		logger.Warn("转封装失败", "src", src, "error", err, "stderr", errBuf.String())
		return errors.New("转封装失败")
	}
	if fi, err := os.Stat(tmp); err != nil || fi.Size() == 0 {
		return errors.New("转封装产物为空")
	}
	return os.Rename(tmp, dst)
}

// transcodeStream 实时转码（浏览器不支持原编码时的降级路径）：
// 输出 MPEG-TS/frag MP4 流式响应，CPU 受限（threads 指定、并发 1），不保证 Range。
func (s *Service) transcodeStream(c *gin.Context, f *storage.FileMeta) {
	select {
	case s.transSem <- struct{}{}:
		defer func() { <-s.transSem }()
	case <-c.Request.Context().Done():
		return
	}
	threads := strconv.Itoa(s.cfg.TranscodeThreads)
	cmd := exec.Command(s.cfg.FFmpegPath,
		"-i", f.StoragePath,
		"-map", "0:v:0?", "-map", "0:a:0?",
		"-c:v", "libx264", "-preset", "veryfast",
		"-threads", threads,
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "frag_keyframe+empty_moov",
		"-f", "mp4",
		"pipe:1",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 5000, "message": "转码服务不可用"})
		return
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 5000, "message": "转码服务不可用"})
		return
	}

	c.Header("Content-Type", "video/mp4")
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", "inline; filename*=UTF-8''"+urlEncode(strings.TrimSuffix(f.Name, filepath.Ext(f.Name))+".mp4"))
	c.Status(http.StatusOK)
	_, copyErr := io.Copy(c.Writer, stdout)
	_ = cmd.Wait()
	if copyErr != nil {
		logger.Warn("转码流中断", "file_id", f.ID, "error", copyErr, "ffmpeg", errBuf.String())
	}
}