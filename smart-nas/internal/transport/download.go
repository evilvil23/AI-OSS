package transport

import (
	"errors"
	"net/http"
	"os"
	"path"
	"time"

	"smart-nas/internal/storage"
)

// DownloadFile 下载文件（支持 HTTP Range 断点续传）
// f 为待下载文件元数据（已做权限校验）
func (m *Manager) DownloadFile(w http.ResponseWriter, r *http.Request, f *storage.FileMeta) error {
	if f.IsDir {
		return errors.New("目录不支持直接下载")
	}
	path, err := m.storage.ResolveDownloadPath(f)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("文件已不存在")
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}

	// 传输任务记录（支持查询）
	taskID := m.newTask(f, stat.Size())
	defer m.CompleteTask(taskID)

	w.Header().Set("Content-Type", contentType(f.Name))
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEncodeFileName(f.Name))
	http.ServeContent(w, r, f.Name, f.UpdatedAt, file)
	return nil
}

func (m *Manager) newTask(f *storage.FileMeta, size int64) string {
	task := &FileTransferTask{
		ID:          "dl-" + f.Name + "-" + time.Now().Format("20060102150405"),
		Type:        "download",
		Status:      "running",
		Filename:    f.Name,
		TotalSize:   size,
		Uploaded:    0,
		StartedAt:   time.Now(),
		UserID:      f.OwnerID,
		StoragePath: f.StoragePath,
	}
	m.mu.Lock()
	m.tasks[task.ID] = task
	m.mu.Unlock()
	return task.ID
}

func contentType(filename string) string {
	ext := path.Ext(filename)
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md", ".log", ".go", ".py", ".js", ".json", ".toml", ".yaml", ".yml", ".xml", ".html", ".css":
		return "text/plain; charset=utf-8"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4", ".mkv", ".avi", ".mov":
		return "video/mp4"
	case ".mp3", ".wav", ".flac", ".ogg":
		return "audio/mpeg"
	case ".zip", ".tar", ".gz", ".7z", ".rar":
		return "application/zip"
	case ".doc", ".docx":
		return "application/msword"
	case ".xls", ".xlsx":
		return "application/vnd.ms-excel"
	case ".ppt", ".pptx":
		return "application/vnd.ms-powerpoint"
	default:
		return "application/octet-stream"
	}
}

func urlEncodeFileName(name string) string {
	// 简单的 RFC 5987 编码（非 ASCII 字符转为 %XX）
	var out []byte
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 0x20 && c < 0x7f && c != '%' && c != '"' {
			out = append(out, c)
		} else {
			out = append(out, '%', hexDigit(c>>4), hexDigit(c&0xf))
		}
	}
	return string(out)
}

func hexDigit(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + b - 10
}