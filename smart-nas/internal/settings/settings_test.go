package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return s
}

func TestDefaults(t *testing.T) {
	s := newTestService(t)
	st := s.Get()
	if st.CPURefreshSeconds != 5 || st.DiskRefreshSeconds != 60 {
		t.Fatalf("默认设置不符: cpu=%d disk=%d", st.CPURefreshSeconds, st.DiskRefreshSeconds)
	}
	if st.TrashPath != "" {
		t.Fatalf("默认回收站路径应为空, got %q", st.TrashPath)
	}
	// 备份默认压缩级别应为 6
	if st.BackupCompressLevel != 6 {
		t.Fatalf("备份默认压缩级别应为 6, got %d", st.BackupCompressLevel)
	}
	if st.BackupOutputDir != "" {
		t.Fatalf("备份默认存放目录应为空, got %q", st.BackupOutputDir)
	}
}

func TestUpdateValid(t *testing.T) {
	s := newTestService(t)
	st, err := s.Update(map[string]interface{}{
		"cpu_refresh_seconds": 10,
		"disk_refresh_seconds": 90,
		"trash_path":          "D:/nas-trash",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// 正斜杠路径应归一化为反斜杠（v0.13）
	if st.CPURefreshSeconds != 10 || st.DiskRefreshSeconds != 90 || st.TrashPath != `D:\nas-trash` {
		t.Fatalf("更新后设置不符: %+v", st)
	}
	// 校验持久化生效
	got := s.Get()
	if got.CPURefreshSeconds != 10 || got.TrashPath != `D:\nas-trash` {
		t.Fatalf("持久化读回不符: %+v", got)
	}
}

func TestUpdateInvalid(t *testing.T) {
	s := newTestService(t)
	// CPU 刷新频率低于下限 2 秒应被拒绝
	if _, err := s.Update(map[string]interface{}{"cpu_refresh_seconds": 1}); err == nil {
		t.Fatal("cpu_refresh_seconds=1 应报错")
	}
	// 未知设置项应被拒绝
	if _, err := s.Update(map[string]interface{}{"unknown_key": 1}); err == nil {
		t.Fatal("未知设置项应报错")
	}
	// 非数值应被拒绝
	if _, err := s.Update(map[string]interface{}{"cpu_refresh_seconds": "abc"}); err == nil {
		t.Fatal("非数值应报错")
	}
	// 失败后原设置保持不变
	st := s.Get()
	if st.CPURefreshSeconds != 5 {
		t.Fatalf("失败后设置被篡改: %+v", st)
	}
}

// TestUpdateLogSettings：日志位置 / 最大大小 / 保留天数可更新并持久化
func TestUpdateLogSettings(t *testing.T) {
	s := newTestService(t)
	validLog := filepath.Join(t.TempDir(), "logs", "nas.log")
	st, err := s.Update(map[string]interface{}{
		"log_path":     filepath.ToSlash(validLog),
		"log_max_size": 50,
		"log_max_age":  7,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if filepath.Clean(st.LogPath) != filepath.Clean(validLog) || st.LogMaxSize != 50 || st.LogMaxAge != 7 {
		t.Fatalf("日志设置不符: %+v", st)
	}
	// 无效日志路径（不存在的盘符）应被拒绝
	if _, err := s.Update(map[string]interface{}{"log_path": "Q:/no-such-drive/nas.log"}); err == nil {
		t.Fatal("无效日志路径应报错")
	}
	// 非法值应被拒绝
	if _, err := s.Update(map[string]interface{}{"log_max_size": 0}); err == nil {
		t.Fatal("log_max_size=0 应报错")
	}
	if _, err := s.Update(map[string]interface{}{"log_max_age": 0}); err == nil {
		t.Fatal("log_max_age=0 应报错")
	}
	// 默认值：100MB / 30 天
	d := newTestService(t).Get()
	if d.LogMaxSize != 100 || d.LogMaxAge != 30 {
		t.Fatalf("日志默认值不符: %+v", d)
	}
}

// TestMigrateLegacySettingsJSON：旧版 settings.json 自动迁移为 settings.toml
func TestMigrateLegacySettingsJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"settings":{"cpu_refresh_seconds":8,"disk_refresh_seconds":120,"trash_path":"E:/trash"}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("写入旧配置: %v", err)
	}
	s, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService（迁移）: %v", err)
	}
	st := s.Get()
	if st.CPURefreshSeconds != 8 || st.DiskRefreshSeconds != 120 || st.TrashPath != "E:/trash" {
		t.Fatalf("迁移后设置不符: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.toml")); err != nil {
		t.Fatalf("应生成 settings.toml: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("旧 settings.json 应被删除, err=%v", err)
	}
}

// TestUpdateBackupSettings：备份全局默认（存放目录 / 压缩级别）可更新、校验并持久化
func TestUpdateBackupSettings(t *testing.T) {
	s := newTestService(t)
	// 正斜杠归一化为反斜杠
	st, err := s.Update(map[string]interface{}{
		"backup_output_dir":     "Y:/nas-backup",
		"backup_compress_level": 6,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if st.BackupOutputDir != `Y:\nas-backup` || st.BackupCompressLevel != 6 {
		t.Fatalf("备份设置不符: %+v", st)
	}
	// 持久化读回
	got := s.Get()
	if got.BackupOutputDir != `Y:\nas-backup` || got.BackupCompressLevel != 6 {
		t.Fatalf("持久化读回不符: %+v", got)
	}
	// 非法目录被拒绝
	if _, err := s.Update(map[string]interface{}{"backup_output_dir": "not-a-path"}); err == nil {
		t.Fatal("非法备份目录应报错")
	}
	// 非法压缩级别被拒绝（0 与 10 均越界）
	if _, err := s.Update(map[string]interface{}{"backup_compress_level": 10}); err == nil {
		t.Fatal("压缩级别 10 应报错")
	}
	if _, err := s.Update(map[string]interface{}{"backup_compress_level": 0}); err == nil {
		t.Fatal("压缩级别 0 应报错")
	}
	// 失败后原设置不变
	if g := s.Get(); g.BackupCompressLevel != 6 {
		t.Fatalf("失败后设置被篡改: %+v", g)
	}
}