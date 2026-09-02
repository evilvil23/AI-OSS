package settings

import (
	"os"
	"path/filepath"
	"strings"
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

// TestBackupExcludeRules：全局排除规则默认预设、可更新、持久化（v0.21）
func TestBackupExcludeRules(t *testing.T) {
	s := newTestService(t)
	// 默认应预置常用规则（Windows/Linux/开发项目/NAS 通用）
	d := s.Get()
	if len(d.BackupExcludeRules) == 0 {
		t.Fatal("默认排除规则不应为空")
	}
	found := map[string]bool{}
	for _, r := range d.BackupExcludeRules {
		found[r] = true
	}
	for _, want := range []string{"$RECYCLE.BIN", "node_modules", ".git", "Thumbs.db", "*.tmp", "proc/"} {
		if !found[want] {
			t.Fatalf("默认规则缺少 %q，got %v", want, d.BackupExcludeRules)
		}
	}
	// 更新：JSON 数组（[]interface{}）形式 + 自动 trim/去空
	st, err := s.Update(map[string]interface{}{
		"backup_exclude_rules": []interface{}{"node_modules", "  *.log  ", "", ".git"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(st.BackupExcludeRules) != 3 || st.BackupExcludeRules[0] != "node_modules" || st.BackupExcludeRules[1] != "*.log" {
		t.Fatalf("更新后规则不符: %+v", st.BackupExcludeRules)
	}
	// 持久化读回
	if got := s.Get(); len(got.BackupExcludeRules) != 3 {
		t.Fatalf("持久化读回不符: %+v", got.BackupExcludeRules)
	}
	// 显式清空（空数组）应被尊重，不会回填预设
	if _, err := s.Update(map[string]interface{}{"backup_exclude_rules": []interface{}{}}); err != nil {
		t.Fatalf("清空规则: %v", err)
	}
	if g := s.Get(); len(g.BackupExcludeRules) != 0 {
		t.Fatalf("显式清空后应为空: %+v", g.BackupExcludeRules)
	}
	// 非字符串数组被拒绝
	if _, err := s.Update(map[string]interface{}{"backup_exclude_rules": []interface{}{1, 2}}); err == nil {
		t.Fatal("非字符串规则应报错")
	}
}

// TestExcludeListFileStorage：排除规则独立文件 exclude-list.txt（v0.21.4）
func TestExcludeListFileStorage(t *testing.T) {
	dir := t.TempDir()
	s, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	// ① 默认预设应写入 exclude-list.txt，且 settings.toml 不再内联数组
	data, err := os.ReadFile(filepath.Join(dir, "exclude-list.txt"))
	if err != nil {
		t.Fatalf("exclude-list.txt 应生成: %v", err)
	}
	if !strings.Contains(string(data), "node_modules") {
		t.Fatalf("默认预设应写入文件: %s", data)
	}
	tomlData, _ := os.ReadFile(filepath.Join(dir, "settings.toml"))
	if strings.Contains(string(tomlData), "backup_exclude_rules") {
		t.Fatalf("settings.toml 不应再存储排除规则: %s", tomlData)
	}

	// ② 更新规则 → 重写文件；重新创建服务 → 从文件读回
	if _, err := s.Update(map[string]interface{}{
		"backup_exclude_rules": []interface{}{"node_modules", "# 注释行", "*.log"},
	}); err != nil {
		t.Fatal(err)
	}
	s2, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Get().BackupExcludeRules
	if len(got) != 3 || got[0] != "node_modules" || got[1] != "# 注释行" || got[2] != "*.log" {
		t.Fatalf("重启后应从 exclude-list.txt 读回规则: %v", got)
	}

	// ③ 旧版 settings.toml 内联数组 → 首次启动迁移到 exclude-list.txt
	dir2 := t.TempDir()
	legacy := "cpu_refresh_seconds = 5\nbackup_exclude_rules = ['*.tmp', 'node_modules']\n"
	if err := os.WriteFile(filepath.Join(dir2, "settings.toml"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s3, err := NewService(dir2)
	if err != nil {
		t.Fatal(err)
	}
	mig := s3.Get().BackupExcludeRules
	if len(mig) != 2 || mig[0] != "*.tmp" || mig[1] != "node_modules" {
		t.Fatalf("旧内联数组应迁移到 exclude-list.txt: %v", mig)
	}
	if _, err := os.Stat(filepath.Join(dir2, "exclude-list.txt")); err != nil {
		t.Fatalf("迁移后应生成 exclude-list.txt: %v", err)
	}
}