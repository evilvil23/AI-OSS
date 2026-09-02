// Package logger 提供结构化日志能力。
//
// 说明：文档中选用 zap + lumberjack，但当前离线环境不可用。
// 这里基于标准库 log/slog 实现等价能力（级别控制、JSON 文本输出、
// 文件输出与按大小滚动），对外接口保持 zap 风格，便于后续平滑替换。
package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Config 对应 config.toml 中 [log] 段
type Config struct {
	Level      string `toml:"level"`
	Path       string `toml:"path"`
	MaxSize    int    `toml:"max_size"`    // MB
	MaxBackups int    `toml:"max_backups"`
	MaxAge     int    `toml:"max_age"`     // days
}

var (
	mu       sync.RWMutex
	global   *slog.Logger
	file     *rollingFile
	cleanMu  sync.Mutex
	cleaner  *time.Ticker
	cleanQuit chan struct{}
)

// Init 初始化全局日志器。
// level: debug/info/warn/error；path 为空时仅输出到 stdout。
// 同时启动每日一次的过期日志清理任务（保留最近 maxAge 天）。
func Init(level, path string, maxSizeMB, maxBackups, maxAge int) error {
	mu.Lock()
	defer mu.Unlock()

	lvl := parseLevel(level)
	var w io.Writer = os.Stdout
	// 关闭旧文件句柄（含重置为仅 stdout 的场景），避免句柄泄漏占用文件
	if file != nil {
		_ = file.Close()
		file = nil
	}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		f, err := newRollingFile(path, maxSizeMB, maxBackups, maxAge)
		if err != nil {
			return err
		}
		// 启动时立即清理一次过期备份
		f.cleanup()
		file = f
		w = io.MultiWriter(os.Stdout, f)
	}
	global = slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(global)
	startCleaner(path)
	return nil
}

// startCleaner 每日执行一次过期日志清理；path 为空（仅 stdout）时不启动。
func startCleaner(path string) {
	cleanMu.Lock()
	defer cleanMu.Unlock()
	stopCleanerLocked()
	if path == "" {
		return
	}
	cleanQuit = make(chan struct{})
	cleaner = time.NewTicker(24 * time.Hour)
	go func(path string, quit <-chan struct{}) {
		for {
			select {
			case <-cleaner.C:
				mu.RLock()
				f := file
				mu.RUnlock()
				if f != nil {
					f.cleanup()
				}
			case <-quit:
				return
			}
		}
	}(path, cleanQuit)
}

func stopCleanerLocked() {
	if cleaner != nil {
		cleaner.Stop()
		close(cleanQuit)
		cleaner = nil
		cleanQuit = nil
	}
}

// Current 返回当前文件日志路径与滚动参数（测试 / 诊断用）。
// 未启用文件日志时 path 为空。
func Current() (path string, maxSizeMB, maxBackups, maxAge int) {
	mu.RLock()
	defer mu.RUnlock()
	if file == nil {
		return "", 0, 0, 0
	}
	return file.path, int(file.maxSize / 1024 / 1024), file.maxBackups, file.maxAge
}

// Reconfigure 运行时重配日志器（日志路径/保留天数/最大大小/级别）。
// 用于“设置页”在不重启的情况下切换日志输出位置与清理策略。
// 新路径不可用时返回错误且不影响当前日志器继续工作。
func Reconfigure(level, path string, maxSizeMB, maxBackups, maxAge int) error {
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("日志目录不可用: %w", err)
		}
	}
	mu.Lock()
	old := file
	file = nil
	mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return Init(level, path, maxSizeMB, maxBackups, maxAge)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// L 返回全局日志器
func L() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	if global == nil {
		return slog.Default()
	}
	return global
}

// Info / Debug / Warn / Error 快捷方法
func Info(msg string, args ...any)  { L().Info(msg, args...) }
func Debug(msg string, args ...any) { L().Debug(msg, args...) }
func Warn(msg string, args ...any)  { L().Warn(msg, args...) }
func Error(msg string, args ...any) { L().Error(msg, args...) }

// WithTrace 返回带 trace_id 的子日志器
func WithTrace(ctx context.Context) *slog.Logger {
	if id, ok := ctx.Value(traceKey{}).(string); ok && id != "" {
		return L().With("trace_id", id)
	}
	return L()
}

type traceKey struct{}

// Close 关闭文件日志（滚动文件）并停止周期清理
func Close() error {
	cleanMu.Lock()
	stopCleanerLocked()
	cleanMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		return file.Close()
	}
	return nil
}

// rollingFile 简单的按大小滚动文件日志
type rollingFile struct {
	path       string
	maxSize    int64 // bytes
	maxBackups int
	maxAge     int
	mu         sync.Mutex
	cleanMu    sync.Mutex // 序列化清理，避免与滚动并发清理竞态
	file       *os.File
	size       int64
	createdAt  time.Time
}

func newRollingFile(path string, maxSizeMB, maxBackups, maxAge int) (*rollingFile, error) {
	rf := &rollingFile{
		path:       path,
		maxSize:    int64(maxSizeMB) * 1024 * 1024,
		maxBackups: maxBackups,
		maxAge:     maxAge,
	}
	if err := rf.open(); err != nil {
		return nil, err
	}
	return rf, nil
}

func (r *rollingFile) open() error {
	info, err := os.Stat(r.path)
	if err == nil {
		r.size = info.Size()
		r.createdAt = info.ModTime()
	} else {
		r.size = 0
		r.createdAt = time.Now()
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	r.file = f
	return nil
}

func (r *rollingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return 0, nil
	}
	if r.maxSize > 0 && r.size+int64(len(p)) > r.maxSize {
		r.rotate()
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rollingFile) rotate() {
	_ = r.file.Close()
	ts := time.Now().Format("20060102-150405")
	newPath := r.path + "." + ts
	_ = os.Rename(r.path, newPath)
	_ = r.open()
	r.cleanup() // 同步清理，保证备份数量/保留天数约束即时生效
}

func (r *rollingFile) cleanup() {
	r.cleanMu.Lock()
	defer r.cleanMu.Unlock()
	// 清理过期备份（按修改时间）
	dir := filepath.Dir(r.path)
	base := filepath.Base(r.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var backups []string
	for _, e := range entries {
		if e.IsDir() || !startsWith(e.Name(), base+".") {
			continue
		}
		backups = append(backups, filepath.Join(dir, e.Name()))
	}
	// 保留最近 maxBackups 个
	if r.maxBackups > 0 && len(backups) > r.maxBackups {
		// 简单按名称排序（名称含时间戳，字典序即时间序）
		for i := 0; i < len(backups)-r.maxBackups; i++ {
			_ = os.Remove(backups[i])
		}
	}
	if r.maxAge > 0 {
		cutoff := time.Now().AddDate(0, 0, -r.maxAge)
		for _, p := range backups {
			if info, err := os.Stat(p); err == nil && info.ModTime().Before(cutoff) {
				_ = os.Remove(p)
			}
		}
	}
}

func (r *rollingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		return err
	}
	return nil
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// ensure 标准日志库仍可用
var _ = log.Printf