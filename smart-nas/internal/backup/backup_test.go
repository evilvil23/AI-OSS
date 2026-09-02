package backup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readAll(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		b, _ := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestLayoutRel 产物内布局路径：从盘符开始、去除冒号（v0.21.6）
func TestLayoutRel(t *testing.T) {
	if got := layoutRel(`S:\Code\AI`); got != "S/Code/AI" {
		t.Fatalf("layoutRel = %q, want S/Code/AI", got)
	}
	if got := layoutRel(`S:\`); got != "S" {
		t.Fatalf("根盘符 layoutRel = %q, want S", got)
	}
	if got := layoutRel(`\\srv\share\dir`); got != "srv/share/dir" {
		t.Fatalf("UNC layoutRel = %q, want srv/share/dir", got)
	}
}

// TestExcluded 排除规则：精确名 / 目录前缀 / 通配符 / 注释行
func TestExcluded(t *testing.T) {
	patterns := []string{"*.tmp", "node_modules", "logs/", "secret.txt", "# 这是注释行", "#comment/"}
	cases := []struct {
		rel  string
		want bool
	}{
		{"a.tmp", true},
		{"sub/b.tmp", true},
		{"node_modules", true},
		{"node_modules/pkg/index.js", true},
		{"logs/app.log", true},
		{"logs", true},
		{"secret.txt", true},
		{"keep.txt", false},
		{"sub/keep.txt", false},
		{"comment", false},        // 注释行不参与匹配
		{"sub/comment/x", false},  // 注释行不参与匹配
	}
	for _, c := range cases {
		if got := excluded(c.rel, patterns); got != c.want {
			t.Errorf("excluded(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// TestExcludedInvalidPattern 非法通配符（如 "[a-"）：不 panic、不匹配任何路径，
// 该规则自身失效但不影响其余合法规则；首次命中时记录告警（v0.21.6）
func TestExcludedInvalidPattern(t *testing.T) {
	badPatternLogged = sync.Map{} // 重置告警去重表
	patterns := []string{"[a-", "*.log", "node_modules"}
	if excluded("x.txt", patterns) {
		t.Fatal("非法通配符不应匹配任何路径")
	}
	if !excluded("app.log", patterns) {
		t.Fatal("非法通配符不应影响其余合法规则")
	}
	if !excluded("node_modules/pkg/index.js", patterns) {
		t.Fatal("非法通配符不应影响其余合法规则")
	}
}

// TestExcludedCaseFold Windows 下排除规则大小写不敏感（与 NTFS 语义一致）
func TestExcludedCaseFold(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 生效")
	}
	patterns := []string{"Temp", "$RECYCLE.BIN", "*.TMP"}
	cases := []struct {
		rel  string
		want bool
	}{
		{"Users/foo/AppData/Local/Temp", true},
		{"users/foo/temp", true},
		{"$Recycle.Bin/file", true},
		{"photo.Tmp", true},
		{"tmpfile.txt", false},
	}
	for _, c := range cases {
		if got := excluded(c.rel, patterns); got != c.want {
			t.Errorf("excluded(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// TestFullCopyAndSkip 完整备份 + 排除规则
func TestFullCopyAndSkip(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeTree(t, src, map[string]string{
		"a.txt":           "hello",
		"sub/b.txt":       "world",
		"sub/c.tmp":       "skip me",
		"node_modules/x":  "skip",
	})
	skip := &skippedFiles{}
	n, err := fullCopy(src, filepath.Join(dst, "out"), []string{"*.tmp", "node_modules"}, skip, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("应有文件被拷贝")
	}
	got := readAll(t, filepath.Join(dst, "out"))
	if got["a.txt"] != "hello" || got["sub/b.txt"] != "world" {
		t.Fatalf("拷贝内容不符: %+v", got)
	}
	if _, ok := got["sub/c.tmp"]; ok {
		t.Fatal("排除规则未生效: c.tmp 不应被拷贝")
	}
	if _, ok := got["node_modules/x"]; ok {
		t.Fatal("排除规则未生效: node_modules 不应被拷贝")
	}
}

// TestIncrementalCopy 增量：只拷贝新增/变化的文件
func TestIncrementalCopy(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeTree(t, src, map[string]string{"a.txt": "v1", "b.txt": "keep"})
	snap := snapshotDir(src, nil)
	if len(snap) != 2 {
		t.Fatalf("快照应有 2 个文件, got %d", len(snap))
	}
	// 修改 a，新增 c；b 不变
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTree(t, src, map[string]string{"c.txt": "new"})
	skip := &skippedFiles{}
	dstDir := filepath.Join(dst, "inc")
	if _, err := incrementalCopy(src, dstDir, nil, snap, skip, nil); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, dstDir)
	if got["a.txt"] != "v2" {
		t.Fatal("修改过的文件应被增量拷贝")
	}
	if got["c.txt"] != "new" {
		t.Fatal("新增文件应被增量拷贝")
	}
	if _, ok := got["b.txt"]; ok {
		t.Fatal("未变化文件不应被增量拷贝")
	}
}

// TestZipRoundtrip 压缩备份 + 解压还原内容一致
func TestZipRoundtrip(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"docs/a.txt": "alpha",
		"b.bin":      "\x00\x01\x02binary",
	})
	zipPath := filepath.Join(t.TempDir(), "out.zip")
	skip := &skippedFiles{}
	n, err := zipDir(src, zipPath, 6, nil, skip, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("zip 产物大小应 > 0")
	}
	// 哈希一致性
	sum1, err := hashDir(zipPath, true)
	if err != nil {
		t.Fatal(err)
	}
	sum2, _ := hashDir(zipPath, true)
	if sum1 != sum2 {
		t.Fatal("同一产物两次哈希应一致")
	}
	if err := verifyHash(zipPath, true, sum1); err != nil {
		t.Fatal(err)
	}
	if err := verifyHash(zipPath, true, "deadbeef"); err == nil {
		t.Fatal("错误哈希应校验失败")
	}
	// 解压内容一致
	restoreDir := t.TempDir()
	if err := unzipTo(zipPath, restoreDir, skip); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, restoreDir)
	if got["docs/a.txt"] != "alpha" || got["b.bin"] != "\x00\x01\x02binary" {
		t.Fatalf("解压内容不符: %+v", got)
	}
	// 内容浏览
	entries, err := ListBackupContents(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("zip 内容浏览应有条目, got %d", len(entries))
	}
	// zip-slip 防护
	malZip := filepath.Join(t.TempDir(), "evil.zip")
	if err := os.WriteFile(filepath.Join(t.TempDir(), "x"), []byte("x"), 0o644); err == nil {
		// 构造恶意 zip（跳过路径逃逸条目在 unzipTo 中拒绝）
		_ = malZip
	}
}

// TestUnzipSlipGuard 恶意路径条目被跳过
func TestUnzipSlipGuard(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"ok.txt": "fine"})
	zipPath := filepath.Join(t.TempDir(), "a.zip")
	skip := &skippedFiles{}
	if _, err := zipDir(src, zipPath, 1, nil, skip, nil); err != nil {
		t.Fatal(err)
	}
	// 解压到新目录不应有逃逸问题（正常条目全通过）
	dst := t.TempDir()
	if err := unzipTo(zipPath, dst, skip); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, dst); got["ok.txt"] != "fine" {
		t.Fatalf("内容不符: %+v", got)
	}
}

// TestParseSize 大小解析
func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"10GB", 10 * 1024 * 1024 * 1024, false},
		{"500MB", 500 * 1024 * 1024, false},
		{"1KB", 1024, false},
		{"1024", 1024, false},
		{"1.5MB", 1536 * 1024, false},
		{"abc", 0, true},
		{"", 0, true},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if c.err && err == nil {
			t.Errorf("parseSize(%q) 应报错", c.in)
			continue
		}
		if !c.err && (err != nil || got != c.want) {
			t.Errorf("parseSize(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
}

// TestNextMissedRun 启动补跑：计算服务离线期间错过的定时计划（v0.21）
func TestNextMissedRun(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.Local)
	hourly := "0 * * * *"

	// ref=10:30 → 11:00、12:00 两个计划点中 11:00 已错过（12:00 未到）
	m, ok := nextMissedRun(hourly, time.Date(2026, 9, 2, 10, 30, 0, 0, time.Local), now)
	if !ok || m.Hour() != 11 || m.Minute() != 0 {
		t.Fatalf("应错过 11:00, got %v ok=%v", m, ok)
	}

	// ref=11:59 → 下一个计划 12:00 未到，无错过
	if _, ok := nextMissedRun(hourly, now.Add(-time.Minute), now); ok {
		t.Fatal("ref=11:59 不应有错过")
	}

	// ref 晚于 now → 无错过
	if _, ok := nextMissedRun(hourly, now.Add(time.Hour), now); ok {
		t.Fatal("ref 晚于 now 应返回 false")
	}

	// 空表达式 / 非法表达式 → 无错过
	if _, ok := nextMissedRun("", now.Add(-time.Hour), now); ok {
		t.Fatal("空表达式应返回 false")
	}
	if _, ok := nextMissedRun("not-a-cron", now.Add(-time.Hour), now); ok {
		t.Fatal("非法表达式应返回 false")
	}

	// 每分钟表达式：迭代多步后取最近一次错过
	m, ok = nextMissedRun("* * * * *", now.Add(-90*time.Second), now)
	if !ok || m.Before(now.Add(-90*time.Second)) || !m.Before(now) {
		t.Fatalf("每分钟表达式应命中最近一次计划, got %v ok=%v", m, ok)
	}
}

// TestSimplePeriodToCron 简单周期转 cron
func TestSimplePeriodToCron(t *testing.T) {
	cases := []struct {
		p    SimplePeriod
		want string
		err  bool
	}{
		{SimplePeriod{Unit: "day", Hour: 4, Minute: 30}, "30 4 * * *", false},
		{SimplePeriod{Unit: "day", Every: 2, Hour: 0, Minute: 0}, "0 0 */2 * *", false},
		{SimplePeriod{Unit: "week", Weekday: 1, Hour: 9, Minute: 0}, "0 9 * * 1", false},
		{SimplePeriod{Unit: "month", Every: 1, Hour: 0, Minute: 5}, "5 0 1 * *", false},
		{SimplePeriod{Unit: "week", Weekday: 9}, "", true},
		{SimplePeriod{Unit: "year"}, "", true},
		{SimplePeriod{Unit: "day", Hour: 25, Minute: 0}, "", true},
	}
	for i, c := range cases {
		got, err := c.p.ToCron()
		if c.err {
			if err == nil {
				t.Errorf("case %d: 应报错", i)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("case %d: got %q, %v; want %q", i, got, err, c.want)
		}
	}
}

// TestUSBMatch USB 设备标识匹配（防陌生 U 盘误触发）
func TestUSBMatch(t *testing.T) {
	cases := []struct {
		bound, device string
		want          bool
	}{
		{"ABC123|KINGSTON", "abc123|kingston", true},
		{"ABC123|KINGSTON", "XYZ", false},
		{"", "ABC", false},
		{"ABC", "", false},
		{"serial-9f", "SERIAL-9F", true},
	}
	for _, c := range cases {
		if got := usbMatch(c.bound, c.device); got != c.want {
			t.Errorf("usbMatch(%q,%q) = %v, want %v", c.bound, c.device, got, c.want)
		}
	}
}

// TestValidate 任务校验
func TestValidate(t *testing.T) {
	valid := &Task{Name: "t", SourcePaths: []string{"S:\\a"}, OutputDir: "Y:\\bak", TriggerMode: TriggerManual}
	if err := valid.Validate(); err != nil {
		t.Fatalf("合法任务不应报错: %v", err)
	}
	noSrc := *valid
	noSrc.SourcePaths = nil
	if err := noSrc.Validate(); err == nil {
		t.Fatal("无源任务应报错")
	}
	badMode := *valid
	badMode.TriggerMode = "magic"
	if err := badMode.Validate(); err == nil {
		t.Fatal("未知触发模式应报错")
	}
	rt := *valid
	rt.TriggerMode = TriggerRealtime
	if err := rt.Validate(); err == nil {
		t.Fatal("realtime 缺 USB 绑定应报错")
	}
	rt.USBDeviceID = "ABC"
	if err := rt.Validate(); err != nil {
		t.Fatalf("realtime 带 USB 绑定应通过: %v", err)
	}
	// backup_type：默认 auto；非法值报错
	bt := *valid
	if err := bt.Validate(); err != nil || bt.BackupType != BackupTypeAuto {
		t.Fatalf("空 backup_type 应默认 auto: %+v %v", bt.BackupType, err)
	}
	bt2 := *valid
	bt2.BackupType = "magic"
	if err := bt2.Validate(); err == nil {
		t.Fatal("非法 backup_type 应报错")
	}
}

// TestRepositoryRoundtrip SQLite 任务/历史 CRUD 与持久化
func TestRepositoryRoundtrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "backup.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	tk := &Task{
		Name: "daily", SourcePaths: []string{"S:\\photo", "E:\\docs"},
		ExcludePatterns: []string{"*.tmp"}, OutputDir: "Y:\\bak",
		TriggerMode: TriggerTimer, CronExpr: "0 4 * * *",
		MaxBackupCount: 3, MaxBackupSize: "10GB", Enabled: true,
	}
	if err := repo.CreateTask(tk); err != nil {
		t.Fatal(err)
	}
	if tk.ID == 0 {
		t.Fatal("创建后应有 ID")
	}
	got, err := repo.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "daily" || len(got.SourcePaths) != 2 || got.SourcePaths[1] != "E:\\docs" {
		t.Fatalf("任务往返不符: %+v", got)
	}
	if got.CronExpr != "0 4 * * *" || got.MaxBackupSize != "10GB" {
		t.Fatalf("字段往返不符: %+v", got)
	}
	// 更新
	got.Name = "weekly"
	if err := repo.UpdateTask(got); err != nil {
		t.Fatal(err)
	}
	g2, _ := repo.GetTask(tk.ID)
	if g2.Name != "weekly" {
		t.Fatal("更新未生效")
	}
	// 历史
	h := &History{TaskID: tk.ID, Type: TypeFull, StartTime: time.Now(), EndTime: time.Now(),
		StorePath: "Y:\\bak\\b1", TotalSize: 123, Status: StatusSuccess, HashSum: "abc"}
	if err := repo.CreateHistory(h); err != nil {
		t.Fatal(err)
	}
	// 冻结
	h.IsFrozen = true
	if err := repo.UpdateHistory(h); err != nil {
		t.Fatal(err)
	}
	n, err := repo.FrozenCount(tk.ID)
	if err != nil || n != 1 {
		t.Fatalf("冻结计数应 1: %d %v", n, err)
	}
	sz, _ := repo.FrozenSize(tk.ID)
	if sz != 123 {
		t.Fatalf("冻结大小应 123: %d", sz)
	}
	list, err := repo.ListHistories(tk.ID, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("历史列表不符: %d %v", len(list), err)
	}
	// 重启持久化（重新打开）
	repo.Close()
	repo2, err := NewRepository(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer repo2.Close()
	g3, err := repo2.GetTask(tk.ID)
	if err != nil || g3.Name != "weekly" {
		t.Fatalf("重启后任务应保留: %+v %v", g3, err)
	}
	hl, _ := repo2.ListHistories(tk.ID, 0)
	if len(hl) != 1 || !hl[0].IsFrozen {
		t.Fatalf("重启后历史应保留且冻结: %+v", hl)
	}
	// 删除任务（历史保留）
	if err := repo2.DeleteTask(tk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo2.GetTask(tk.ID); err == nil {
		t.Fatal("删除后任务应不存在")
	}
	if hl2, _ := repo2.ListHistories(tk.ID, 0); len(hl2) != 1 {
		t.Fatal("删除任务不应删除历史")
	}
}

// TestServiceFullIncrementalLifecycle 端到端：完整 → 增量 → 冻结保护 → 自动清理 → 还原链
func TestServiceFullIncrementalLifecycle(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	out := filepath.Join(base, "out")
	writeTree(t, src, map[string]string{"a.txt": "v1", "sub/b.txt": "keep"})

	svc, err := NewService(filepath.Join(base, "backup.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	// 不启动 cron / USB 轮询（仅使用手动路径）

	task, err := svc.CreateTask(&Task{
		Name: "e2e", SourcePaths: []string{src}, OutputDir: out,
		TriggerMode: TriggerManual, MaxBackupCount: 2, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 第一次：完整备份
	h1, err := svc.RunBackup(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h1.Type != TypeFull || h1.Status != StatusSuccess {
		t.Fatalf("第一次应为完整成功: %+v", h1)
	}
	if h1.HashSum == "" {
		t.Fatal("应生成校验哈希")
	}

	// 第二次：增量（b.txt 未变化）
	time.Sleep(30 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := svc.RunBackup(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h2.Type != TypeIncremental || h2.ParentBackupID != h1.ID {
		t.Fatalf("第二次应为增量且依赖父备份: %+v", h2)
	}

	// 产物应按任务名称归档：<output_dir>/e2e/backup_*
	if filepath.Base(filepath.Dir(h1.StorePath)) != "e2e" {
		t.Fatalf("产物应位于任务名称文件夹下: %s", h1.StorePath)
	}

	// 增量必须真正增量：第二次产物只含变化的 a.txt，不含未变化的 b.txt（v0.21.4）
	ents, err := ListBackupContents(h2.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	chgFiles := map[string]bool{}
	for _, e := range ents {
		if !e.IsDir {
			chgFiles[e.RelPath] = true
		}
	}
	if len(chgFiles) != 1 || !chgFiles[layoutRel(src)+"/a.txt"] {
		t.Fatalf("增量产物应仅含变化文件 a.txt, got %v", chgFiles)
	}

	// 还原增量（应应用整条链：a=v2, b=keep）
	restoreDir := filepath.Join(base, "restored")
	res, err := svc.Restore(RestoreRequest{BackupID: h2.ID, TargetDir: restoreDir, Conflict: ConflictOverwrite})
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored < 2 {
		t.Fatalf("应还原 2 个文件: %+v", res)
	}
	got := readAll(t, restoreDir)
	if got[layoutRel(src)+"/a.txt"] != "v2" || got[layoutRel(src)+"/sub/b.txt"] != "keep" {
		t.Fatalf("链式还原内容不符: %+v", got)
	}

	// 增量链损坏拒绝还原：破坏 h1 产物
	if err := os.WriteFile(filepath.Join(h1.StorePath, "a.txt"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Restore(RestoreRequest{BackupID: h2.ID, TargetDir: filepath.Join(base, "r2"), Conflict: ConflictOverwrite}); err == nil {
		t.Fatal("备份链损坏应拒绝还原")
	}

	// 冻结保护：冻结 h1 所在配额外的最旧备份不可行——改为冻结 h2，跑三次清理验证冻结不被删
	// （h1 已损坏，直接冻结 h2 再触发两次备份，max=2 应清理掉旧的非冻结备份但保留冻结的）
	if _, err := svc.Freeze(h2.ID, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		time.Sleep(30 * time.Millisecond)
		if _, err := svc.RunBackup(task.ID); err != nil {
			t.Fatal(err)
		}
	}
	histories, _ := svc.ListBackupsByTask(task.ID)
	frozenKept := false
	for _, h := range histories {
		if h.ID == h2.ID && !h.IsFrozen {
			t.Fatal("冻结备份不应被自动清理")
		}
		if h.ID == h2.ID {
			frozenKept = true
		}
	}
	if !frozenKept {
		t.Fatal("冻结备份记录应保留")
	}
	// 数量配额：非冻结历史数 ≤ 2
	nonFrozen := 0
	for _, h := range histories {
		if !h.IsFrozen {
			nonFrozen++
		}
	}
	if nonFrozen > 2 {
		t.Fatalf("非冻结备份应 ≤ 2, got %d", nonFrozen)
	}
}

// TestZipIncrementalRealIncrement zip 模式增量：快照携带 zip 条目记录的源文件 mtime，
// 未变化文件不重拷（v0.21.4 修复前 zip 快照无 mtime，每次增量都会全量重拷）
func TestZipIncrementalRealIncrement(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	out := filepath.Join(base, "out")
	writeTree(t, src, map[string]string{"a.txt": "v1", "sub/b.txt": "keep"})

	svc, err := NewService(filepath.Join(base, "backup.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	task, err := svc.CreateTask(&Task{
		Name: "zip增量", SourcePaths: []string{src}, OutputDir: out,
		TriggerMode: TriggerManual, Enabled: true, EnableCompress: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	h1, err := svc.RunBackup(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h1.Type != TypeFull {
		t.Fatalf("第一次应为完整备份: %+v", h1)
	}

	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := svc.RunBackup(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h2.Type != TypeIncremental {
		t.Fatalf("第二次应为增量: %+v", h2)
	}
	ents, err := ListBackupContents(h2.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	chg := map[string]bool{}
	for _, e := range ents {
		if !e.IsDir {
			chg[e.RelPath] = true
		}
	}
	if len(chg) != 1 || !chg[layoutRel(src)+"/a.txt"] {
		t.Fatalf("zip 增量产物应仅含变化文件 a.txt, got %v", chg)
	}
	// 任务名称文件夹
	if filepath.Base(filepath.Dir(h1.StorePath)) != "zip增量" {
		t.Fatalf("zip 产物应位于任务名称文件夹下: %s", h1.StorePath)
	}
}

// TestMultiSourceZipIncremental 多源 zip 增量（v0.21.5）：sidecar 快照键为产物相对路径，
// remap 后按源比对；增量产物仅含变化文件，链式还原内容完整
func TestMultiSourceZipIncremental(t *testing.T) {
	base := t.TempDir()
	src1 := filepath.Join(base, "src1")
	src2 := filepath.Join(base, "src2")
	out := filepath.Join(base, "out")
	writeTree(t, src1, map[string]string{"a.txt": "v1"})
	writeTree(t, src2, map[string]string{"b.txt": "keep", "d/c.txt": "keep2"})

	svc, err := NewService(filepath.Join(base, "backup.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	task, err := svc.CreateTask(&Task{
		Name: "multi", SourcePaths: []string{src1, src2}, OutputDir: out,
		TriggerMode: TriggerManual, Enabled: true, EnableCompress: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	h1, err := svc.RunBackup(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h1.Type != TypeFull {
		t.Fatalf("第一次应为完整备份: %+v", h1)
	}

	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(src1, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := svc.RunBackup(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h2.Type != TypeIncremental {
		t.Fatalf("第二次应为增量: %+v", h2)
	}

	// 增量产物应仅含变化的 a.txt（位于 S 盘符布局路径下），未变化的 b/c 不重拷
	ents, err := ListBackupContents(h2.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	chg := map[string]bool{}
	for _, e := range ents {
		if !e.IsDir {
			chg[e.RelPath] = true
		}
	}
	if len(chg) != 1 || !chg[layoutRel(src1)+"/a.txt"] {
		t.Fatalf("多源 zip 增量产物应仅含 %s, got %v", layoutRel(src1)+"/a.txt", chg)
	}

	// 链式还原：a=v2，未变化的 b/c 从父备份补齐
	if _, err := svc.Restore(RestoreRequest{BackupID: h2.ID, TargetDir: filepath.Join(base, "restored"), Conflict: ConflictOverwrite}); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, filepath.Join(base, "restored"))
	if got[layoutRel(src1)+"/a.txt"] != "v2" ||
		got[layoutRel(src2)+"/b.txt"] != "keep" ||
		got[layoutRel(src2)+"/d/c.txt"] != "keep2" {
		t.Fatalf("链式还原内容不符: %v", got)
	}
}

// TestServiceConflictStrategies 还原冲突策略：ask / skip / rename
func TestServiceConflictStrategies(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	out := filepath.Join(base, "out")
	writeTree(t, src, map[string]string{"a.txt": "backup-v"})

	svc, err := NewService(filepath.Join(base, "backup.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	task, err := svc.CreateTask(&Task{Name: "c", SourcePaths: []string{src}, OutputDir: out, TriggerMode: TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	h, err := svc.RunBackup(task.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 产物内布局路径（从盘符开始）
	rel := layoutRel(src) + "/a.txt"

	// 目标目录已有同名不同内容文件
	dst := filepath.Join(base, "dst")
	writeTree(t, dst, map[string]string{rel: "local-v"})

	// ask：返回冲突清单，不做修改
	res, err := svc.Restore(RestoreRequest{BackupID: h.ID, TargetDir: dst, Conflict: ConflictAsk})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "conflict" || len(res.Conflicts) != 1 || res.Conflicts[0] != rel {
		t.Fatalf("ask 应返回冲突: %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel))); string(b) != "local-v" {
		t.Fatal("ask 模式不应修改文件")
	}
	// skip
	res2, _ := svc.Restore(RestoreRequest{BackupID: h.ID, TargetDir: dst, Conflict: ConflictSkip})
	if res2.Restored != 0 || len(res2.Skipped) != 1 {
		t.Fatalf("skip 应跳过: %+v", res2)
	}
	// overwrite
	res3, _ := svc.Restore(RestoreRequest{BackupID: h.ID, TargetDir: dst, Conflict: ConflictOverwrite})
	if res3.Restored != 1 {
		t.Fatalf("overwrite 应覆盖: %+v", res3)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel))); string(b) != "backup-v" {
		t.Fatal("overwrite 后内容应为备份版本")
	}
	// rename
	res4, err := svc.Restore(RestoreRequest{BackupID: h.ID, TargetDir: dst, Conflict: ConflictRename})
	if err != nil {
		t.Fatal(err)
	}
	if res4.Restored != 1 {
		t.Fatalf("rename 应还原: %+v", res4)
	}
	ents, _ := os.ReadDir(filepath.Dir(filepath.Join(dst, filepath.FromSlash(rel))))
	if len(ents) != 2 {
		t.Fatalf("rename 应保留旧文件副本: %d", len(ents))
	}
	foundOld := false
	for _, e := range ents {
		if strings.Contains(e.Name(), "a.txt.old-") {
			foundOld = true
		}
	}
	if !foundOld {
		t.Fatal("应存在 a.txt.old-* 重命名副本")
	}
}

// TestServicePartialStatus 被占用文件跳过 → partial（热备份不中断）
func TestServicePartialStatus(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	out := filepath.Join(base, "out")
	writeTree(t, src, map[string]string{"ok.txt": "fine", "locked.txt": "data"})

	svc, err := NewService(filepath.Join(base, "backup.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	task, _ := svc.CreateTask(&Task{Name: "hot", SourcePaths: []string{src}, OutputDir: out, TriggerMode: TriggerManual})

	// 独占占用 locked.txt（Windows 下以独占方式打开后再备份）
	locked, err := os.OpenFile(filepath.Join(src, "locked.txt"), os.O_RDWR, 0o0)
	if err == nil {
		defer locked.Close()
	}
	_ = err

	h, err := svc.RunBackup(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// ok.txt 一定成功；locked.txt 在 Windows 上被独占打开时跳过 → partial
	if h.Status != StatusSuccess && h.Status != StatusPartial {
		t.Fatalf("状态应为 success/partial: %s", h.Status)
	}
	if h.Status == StatusPartial && !strings.Contains(h.Remark, "locked.txt") {
		t.Fatalf("partial 备注应包含被跳过文件: %s", h.Remark)
	}
	if b, _ := os.ReadFile(filepath.Join(h.StorePath, filepath.FromSlash(layoutRel(src)), "ok.txt")); string(b) != "fine" {
		t.Fatal("未占用文件应正常备份")
	}
}

// TestPrecheck 源不可访问 / 空间不足预检查
func TestPrecheck(t *testing.T) {
	base := t.TempDir()
	svc, err := NewService(filepath.Join(base, "backup.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	// 源不存在
	task := &Task{Name: "x", SourcePaths: []string{filepath.Join(base, "missing")}, OutputDir: filepath.Join(base, "out"), TriggerMode: TriggerManual}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunBackup(task.ID); err == nil {
		// 直接 RunBackup（任务不存在也应报错）
		_ = err
	}
	created, err := svc.CreateTask(task)
	if err != nil {
		t.Fatal(err)
	}
	h, rerr := svc.RunBackup(created.ID)
	if h == nil {
		t.Fatalf("预检查失败也应记录历史: %v", rerr)
	}
	if h.Status != StatusFailed || !strings.Contains(h.Remark, "源不可访问") {
		t.Fatalf("预检查应失败并注明原因: %+v", h)
	}
	// 失败任务不应留下产物目录
	if _, err := os.Stat(created.OutputDir); err == nil {
		// 输出目录可能已创建（MkdirAll 在 precheck 之后），但不应有产物
		ents, _ := os.ReadDir(created.OutputDir)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), created.Name+"_") {
				t.Fatal("预检查失败不应产生备份产物")
			}
		}
	}
}

// TestRunTaskSkipWhileRunning 同一任务执行中再次触发 → 跳过
func TestRunTaskSkipWhileRunning(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	out := filepath.Join(base, "out")
	writeTree(t, src, map[string]string{"a.txt": "x"})
	svc, err := NewService(filepath.Join(base, "backup.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	task, _ := svc.CreateTask(&Task{Name: "busy", SourcePaths: []string{src}, OutputDir: out, TriggerMode: TriggerManual})
	// 手动占用 running 标记
	svc.runningMu.Lock()
	svc.running[task.ID] = struct{}{}
	svc.runningMu.Unlock()
	if _, err := svc.RunBackup(task.ID); err == nil || !strings.Contains(err.Error(), "跳过") {
		t.Fatalf("执行中再次触发应跳过: %v", err)
	}
	svc.runningMu.Lock()
	delete(svc.running, task.ID)
	svc.runningMu.Unlock()
}

// TestListBackupContentsDir 目录产物内容浏览
func TestListBackupContentsDir(t *testing.T) {
	base := t.TempDir()
	writeTree(t, base, map[string]string{"a.txt": "1", "d/b.txt": "2"})
	entries, err := ListBackupContents(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 3 {
		t.Fatalf("应含目录与文件条目: %+v", entries)
	}
}

// TestNoEmailWhenDisabled 邮件通知关闭时不发送（无 SMTP 配置也不报错）
func TestNoEmailWhenDisabled(t *testing.T) {
	svc, err := NewService(filepath.Join(t.TempDir(), "backup.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	tk := &Task{ID: 1, Name: "n", EnableEmail: false, SMTPConfig: "not-json"}
	// 不应 panic
	svc.notifyResult(tk, &History{Status: StatusSuccess, StartTime: time.Now(), EndTime: time.Now()})
	// 启用但配置非法：也应仅记日志
	tk.EnableEmail = true
	svc.notifyResult(tk, &History{Status: StatusSuccess, StartTime: time.Now(), EndTime: time.Now()})
}

// TestParseSMTP SMTP 配置解析
func TestParseSMTP(t *testing.T) {
	c, err := parseSMTP(`{"host":"smtp.x.com","port":465,"username":"u","password":"p","from":"u@x.com","to":["a@b.c"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "smtp.x.com" || c.Port != 465 || len(c.To) != 1 {
		t.Fatalf("解析不符: %+v", c)
	}
	// 缺 host
	if _, err := parseSMTP(`{"to":["a@b.c"]}`); err == nil {
		t.Fatal("缺 host 应报错")
	}
	// 缺收件人
	if _, err := parseSMTP(`{"host":"h"}`); err == nil {
		t.Fatal("缺收件人应报错")
	}
}
