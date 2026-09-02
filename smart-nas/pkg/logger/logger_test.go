package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resetLogger 恢复为仅 stdout 的全局日志器；以 defer 调用（先于 TempDir 清理执行，
// 避免临时目录中的日志文件仍被占用导致删除失败）。
func resetLogger() { _ = Init("info", "", 0, 0, 0) }

func TestInitAndCurrent(t *testing.T) {
	defer resetLogger()
	dir := t.TempDir()
	path := filepath.Join(dir, "nas.log")
	if err := Init("debug", path, 10, 3, 30); err != nil {
		t.Fatalf("Init: %v", err)
	}
	Info("hello", "k", "v")
	cur, size, backups, age := Current()
	if cur != path || size != 10 || backups != 3 || age != 30 {
		t.Fatalf("Current 不符: path=%q size=%d backups=%d age=%d", cur, size, backups, age)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("日志文件未创建: %v", err)
	}
	// Reconfigure 切换位置后立即生效
	path2 := filepath.Join(dir, "nas2.log")
	if err := Reconfigure("info", path2, 20, 5, 10); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}
	cur, size, _, age = Current()
	if cur != path2 || size != 20 || age != 10 {
		t.Fatalf("重配后 Current 不符: path=%q size=%d age=%d", cur, size, age)
	}
	// 切换前旧文件应已关闭（可删除）
	if err := os.Remove(path); err != nil {
		t.Fatalf("旧日志文件应已关闭可删: %v", err)
	}
}

// TestRotateAndCleanupByMaxBackups：超过最大大小滚动；备份按数量上限清理。
// 直接驱动 rollingFile，避免大体积日志走 stdout。
func TestRotateAndCleanupByMaxBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nas.log")
	rf, err := newRollingFile(path, 1, 2, 0) // 1MB 上限，保留 2 个备份
	if err != nil {
		t.Fatalf("newRollingFile: %v", err)
	}
	defer rf.Close()
	chunk := make([]byte, 600*1024) // 每条约 600KB，两条即超过 1MB 触发滚动
	for i := 0; i < 8; i++ {
		if _, err := rf.Write(chunk); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	backups := 0
	for _, e := range entries {
		if len(e.Name()) > len("nas.log.") && e.Name() != "nas.log.tmp" {
			backups++
		}
	}
	if backups > 2 {
		t.Fatalf("备份数应受 max_backups=2 约束, got %d", backups)
	}
}

// TestCleanupExpiredByMaxAge：超过保留天数的备份在启动清理时删除
func TestCleanupExpiredByMaxAge(t *testing.T) {
	defer resetLogger()
	dir := t.TempDir()
	path := filepath.Join(dir, "nas.log")
	// 预置一个 40 天前的过期备份与一个新备份
	old := path + ".20260101-000000"
	recent := path + ".20260822-000000"
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recent, []byte("recent"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().AddDate(0, 0, -40)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	// Init 时立即执行一次过期清理（保留 30 天）
	if err := Init("info", path, 10, 0, 30); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("过期备份应被删除, err=%v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("未过期备份应保留: %v", err)
	}
}

