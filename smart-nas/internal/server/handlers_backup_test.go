package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"smart-nas/internal/backup"
)

// newBackupServer 在 newTestServer 基础上注入备份服务
func newBackupServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	s := newTestServer(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	out := filepath.Join(base, "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello-backup"), 0o644); err != nil {
		t.Fatal(err)
	}
	bSvc, err := backup.NewService(filepath.Join(base, "backup.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bSvc.Close() })
	s.deps.Backup = bSvc
	// 备份路由挂在已认证分组下（与 setupRoutes 中一致：/api + Auth 中间件）
	s.registerBackupRoutes(s.engine.Group("/api", Auth(s.deps.Auth)))
	return s, src, out
}

// TestBackupUnauthorized 未登录访问备份接口 → 401
func TestBackupUnauthorized(t *testing.T) {
	s, _, _ := newBackupServer(t)
	w := doJSON(t, s, http.MethodGet, "/api/backup/tasks", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, got %d", w.Code)
	}
}

// TestBackupAdminGate 普通用户可查看；写操作要求主人/管理员
func TestBackupAdminGate(t *testing.T) {
	s, src, out := newBackupServer(t)
	master := login(t, s, "admin", "admin123")
	createUser(t, s, master, "tom", "tom123", "user")
	tom := login(t, s, "tom", "tom123")

	// 普通用户：读接口放行
	w := doJSON(t, s, http.MethodGet, "/api/backup/tasks", tom, "")
	if w.Code != http.StatusOK {
		t.Fatalf("普通用户查看任务应 200, got %d %s", w.Code, w.Body.String())
	}
	// 普通用户：创建任务被拒（403）
	body := `{"task_name":"t","source_paths":[` + jsonPath(src) + `],"output_dir":` + jsonPath(out) + `,"trigger_mode":"manual"}`
	w2 := doJSON(t, s, http.MethodPost, "/api/backup/tasks", tom, body)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("普通用户创建任务应 403, got %d %s", w2.Code, w2.Body.String())
	}
}

// TestBackupTaskLifecycle 接口链路：创建 → 触发 → 历史 → 内容浏览 → 还原 → 冻结 → 删除
func TestBackupTaskLifecycle(t *testing.T) {
	s, src, out := newBackupServer(t)
	master := login(t, s, "admin", "admin123")

	// 1. 创建任务（简单周期转 cron）
	body := `{"task_name":"e2e","source_paths":[` + jsonPath(src) + `],"output_dir":` + jsonPath(out) + `,` +
		`"trigger_mode":"timer","simple_period":{"unit":"day","hour":4,"minute":30},"max_backup_count":5}`
	w := doJSON(t, s, http.MethodPost, "/api/backup/tasks", master, body)
	if w.Code != http.StatusOK {
		t.Fatalf("创建任务失败: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Data backup.Task `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.CronExpr != "30 4 * * *" {
		t.Fatalf("简单周期应转 cron 30 4 * * *, got %q", created.Data.CronExpr)
	}
	taskID := created.Data.ID

	// 2. 触发备份
	w2 := doJSON(t, s, http.MethodPost, `/api/backup/tasks/`+uitoa(uint(taskID))+`/run`, master, "")
	if w2.Code != http.StatusOK {
		t.Fatalf("触发备份失败: %d %s", w2.Code, w2.Body.String())
	}
	var runRes struct {
		Data backup.History `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &runRes); err != nil {
		t.Fatal(err)
	}
	h := runRes.Data
	if h.Type != backup.TypeFull || h.Status != backup.StatusSuccess || h.HashSum == "" {
		t.Fatalf("备份结果不符: %+v", h)
	}

	// 3. 历史列表
	w3 := doJSON(t, s, http.MethodGet, `/api/backup/tasks/`+uitoa(uint(taskID))+`/history`, master, "")
	if w3.Code != http.StatusOK {
		t.Fatalf("历史列表失败: %d", w3.Code)
	}
	var histRes struct {
		Data []backup.History `json:"data"`
	}
	if err := json.Unmarshal(w3.Body.Bytes(), &histRes); err != nil {
		t.Fatal(err)
	}
	if len(histRes.Data) != 1 {
		t.Fatalf("应有 1 条历史, got %d", len(histRes.Data))
	}

	// 4. 内容浏览
	w4 := doJSON(t, s, http.MethodGet, `/api/backup/backups/`+uitoa(uint(h.ID))+`/contents`, master, "")
	if w4.Code != http.StatusOK {
		t.Fatalf("内容浏览失败: %d %s", w4.Code, w4.Body.String())
	}

	// 5. 还原到指定目录（覆盖策略）
	target := filepath.Join(filepath.Dir(out), "restored")
	w5 := doJSON(t, s, http.MethodPost, `/api/backup/backups/`+uitoa(uint(h.ID))+`/restore`, master,
		`{"target_dir":`+jsonPath(target)+`,"conflict":"overwrite"}`)
	if w5.Code != http.StatusOK {
		t.Fatalf("还原失败: %d %s", w5.Code, w5.Body.String())
	}
	var restRes struct {
		Data backup.RestoreResult `json:"data"`
	}
	if err := json.Unmarshal(w5.Body.Bytes(), &restRes); err != nil {
		t.Fatal(err)
	}
	if restRes.Data.Restored != 1 {
		t.Fatalf("应还原 1 个文件: %+v", restRes.Data)
	}
	b, err := os.ReadFile(filepath.Join(target, "a.txt"))
	if err != nil || string(b) != "hello-backup" {
		t.Fatalf("还原内容不符: %s %v", string(b), err)
	}

	// 6. 冻结 / 解冻
	w6 := doJSON(t, s, http.MethodPost, `/api/backup/backups/`+uitoa(uint(h.ID))+`/freeze`, master, `{"frozen":true}`)
	if w6.Code != http.StatusOK {
		t.Fatalf("冻结失败: %d", w6.Code)
	}
	w7 := doJSON(t, s, http.MethodPost, `/api/backup/backups/`+uitoa(uint(h.ID))+`/freeze`, master, `{"frozen":false}`)
	if w7.Code != http.StatusOK {
		t.Fatalf("解冻失败: %d", w7.Code)
	}

	// 7. 删除备份
	w8 := doJSON(t, s, http.MethodDelete, `/api/backup/backups/`+uitoa(uint(h.ID)), master, "")
	if w8.Code != http.StatusOK {
		t.Fatalf("删除备份失败: %d", w8.Code)
	}
	if _, err := os.Stat(h.StorePath); !os.IsNotExist(err) {
		t.Fatal("删除备份后产物应被移除")
	}
}

// TestBackupRestoreDamagedChain 链损坏 → 400 拒绝
func TestBackupRestoreDamagedChain(t *testing.T) {
	s, src, out := newBackupServer(t)
	master := login(t, s, "admin", "admin123")
	body := `{"task_name":"x","source_paths":[` + jsonPath(src) + `],"output_dir":` + jsonPath(out) + `,"trigger_mode":"manual"}`
	w := doJSON(t, s, http.MethodPost, "/api/backup/tasks", master, body)
	var created struct {
		Data backup.Task `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	w2 := doJSON(t, s, http.MethodPost, `/api/backup/tasks/`+uitoa(uint(created.Data.ID))+`/run`, master, "")
	var runRes struct {
		Data backup.History `json:"data"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &runRes)
	// 篡改产物
	if err := os.WriteFile(filepath.Join(runRes.Data.StorePath, "a.txt"), []byte("evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	w3 := doJSON(t, s, http.MethodPost, `/api/backup/backups/`+uitoa(uint(runRes.Data.ID))+`/restore`, master, `{"conflict":"overwrite"}`)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("链损坏还原应 400, got %d %s", w3.Code, w3.Body.String())
	}
}

// TestBackupUSBDevices USB 设备列表接口
func TestBackupUSBDevices(t *testing.T) {
	s, _, _ := newBackupServer(t)
	master := login(t, s, "admin", "admin123")
	w := doJSON(t, s, http.MethodGet, "/api/backup/usb-devices", master, "")
	if w.Code != http.StatusOK {
		t.Fatalf("USB 设备列表应 200, got %d", w.Code)
	}
}

// TestBackupCreateAppliesGlobalDefaults 新建任务：目录/压缩级别留空时套用全局默认；
// backup_type=full 被保存；触发后为完全备份
func TestBackupCreateAppliesGlobalDefaults(t *testing.T) {
	s, src, _ := newBackupServer(t)
	master := login(t, s, "admin", "admin123")

	// 全局默认目录使用临时盘符路径（如 Z:\TEMP\...），避免真实盘副作用
	globalDir := t.TempDir()

	// 配置全局默认：存放目录 + 压缩级别 9（默认）
	wSet := doJSON(t, s, http.MethodPut, "/api/admin/settings", master,
		`{"backup_output_dir":`+jsonPath(globalDir)+`,"backup_compress_level":9}`)
	if wSet.Code != http.StatusOK {
		t.Fatalf("保存全局设置失败: %d %s", wSet.Code, wSet.Body.String())
	}

	// /api/backup/defaults 返回全局默认
	wDfl := doJSON(t, s, http.MethodGet, "/api/backup/defaults", master, "")
	if wDfl.Code != http.StatusOK {
		t.Fatalf("defaults 接口失败: %d", wDfl.Code)
	}
	var dflRes struct {
		Data struct {
			OutputDir     string `json:"output_dir"`
			CompressLevel int    `json:"compress_level"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wDfl.Body.Bytes(), &dflRes); err != nil {
		t.Fatal(err)
	}
	if dflRes.Data.OutputDir != globalDir || dflRes.Data.CompressLevel != 9 {
		t.Fatalf("defaults 不符: %+v", dflRes.Data)
	}

	// 创建任务：不传 output_dir 与 compress_level（0）→ 套用全局默认
	body := `{"task_name":"dfl","source_paths":[` + jsonPath(src) + `],"enable_compress":true,` +
		`"trigger_mode":"manual","backup_type":"full"}`
	w := doJSON(t, s, http.MethodPost, "/api/backup/tasks", master, body)
	if w.Code != http.StatusOK {
		t.Fatalf("创建任务失败: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Data backup.Task `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.OutputDir != globalDir {
		t.Fatalf("应套用全局存放目录: %+v", created.Data)
	}
	if created.Data.CompressLevel != 9 {
		t.Fatalf("应套用全局压缩级别 9, got %d", created.Data.CompressLevel)
	}
	if created.Data.BackupType != "full" {
		t.Fatalf("backup_type 应为 full: %+v", created.Data)
	}

	// 触发一次备份（应为 full 类型：任务显式选择完全备份）
	wRun := doJSON(t, s, http.MethodPost, `/api/backup/tasks/`+uitoa(uint(created.Data.ID))+`/run`, master, "")
	if wRun.Code != http.StatusOK {
		t.Fatalf("触发备份失败: %d %s", wRun.Code, wRun.Body.String())
	}
	var runRes struct {
		Data backup.History `json:"data"`
	}
	_ = json.Unmarshal(wRun.Body.Bytes(), &runRes)
	if runRes.Data.Type != backup.TypeFull {
		t.Fatalf("backup_type=full 任务应每次完全备份, got %s", runRes.Data.Type)
	}
}

// TestBackupEmptyListNotNull 空任务列表应返回 [] 而非 null（前端 length 防御回归）
func TestBackupEmptyListNotNull(t *testing.T) {
	s, _, _ := newBackupServer(t)
	master := login(t, s, "admin", "admin123")
	// 不创建任何任务：data 必须是 []（JSON 数组），不能是 null
	w := doJSON(t, s, http.MethodGet, "/api/backup/tasks", master, "")
	if w.Code != http.StatusOK {
		t.Fatalf("任务列表应 200, got %d", w.Code)
	}
	if got := w.Body.String(); !strings.Contains(got, `"data":[]`) || strings.Contains(got, `"data":null`) {
		t.Fatalf("空任务列表应编码为 []，got: %s", got)
	}
}

// ---- 辅助 ----

// jsonPath JSON 字符串化路径（反斜杠转义）
func jsonPath(p string) string {
	b, _ := json.Marshal(p)
	return string(b)
}

// uitoa uint 转十进制字符串
func uitoa(n uint) string {
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
