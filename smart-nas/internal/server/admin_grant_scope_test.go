package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func slashPath(p string) string { return strings.ReplaceAll(p, "\\", "/") }

// TestAdminGrantScope：管理员只能授予自身权限范围内的目录权限（v0.13）
func TestAdminGrantScope(t *testing.T) {
	s := newTestServer(t)
	masterToken := login(t, s, "admin", "admin123")
	_, diskDir := firstDisk(t, s)
	permPath := slashPath(diskDir)

	// 主人为管理员创建只读权限（指向测试磁盘）
	w := doJSON(t, s, http.MethodPost, "/api/admin/users", masterToken,
		`{"username":"scoped-admin","password":"pass1234","role":"admin","permissions":[{"path":"`+permPath+`","read":true,"write":false}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("创建受限管理员失败: %d %s", w.Code, w.Body.String())
	}
	adminToken := login(t, s, "scoped-admin", "pass1234")

	// 受限管理员（只读整盘）创建普通用户：
	// 1) 授予自身范围内目录的只读 → 成功
	w = doJSON(t, s, http.MethodPost, "/api/admin/users", adminToken,
		`{"username":"u-ok","password":"pass1234","role":"user","permissions":[{"path":"`+permPath+`","read":true,"write":false}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("授予范围内只读应成功: %d %s", w.Code, w.Body.String())
	}
	// 2) 授予写权限（超出自身只读级别）→ 403
	w = doJSON(t, s, http.MethodPost, "/api/admin/users", adminToken,
		`{"username":"u-bad","password":"pass1234","role":"user","permissions":[{"path":"`+permPath+`","read":true,"write":true}]}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("超出自身级别的授予应 403: %d %s", w.Code, w.Body.String())
	}
	// 3) 授予范围外目录 → 403
	w = doJSON(t, s, http.MethodPost, "/api/admin/users", adminToken,
		`{"username":"u-bad2","password":"pass1234","role":"user","permissions":[{"path":"Q:\\outside","read":true}]}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("范围外授予应 403: %d %s", w.Code, w.Body.String())
	}

	// 权限接口同样校验：给 u-ok 追加范围外路径 → 403
	w = doJSON(t, s, http.MethodGet, "/api/admin/users", adminToken, "")
	var users struct {
		Data []struct {
			ID       int    `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &users); err != nil {
		t.Fatal(err)
	}
	var uokID int
	for _, u := range users.Data {
		if u.Username == "u-ok" {
			uokID = u.ID
		}
	}
	if uokID == 0 {
		t.Fatal("未找到 u-ok")
	}
	w = doJSON(t, s, http.MethodPut, "/api/admin/users/"+strconv.Itoa(uokID)+"/permissions", adminToken,
		`{"permissions":[{"path":"Q:\\outside","read":true}]}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("权限接口范围外授予应 403: %d %s", w.Code, w.Body.String())
	}

	// 主人不受限制
	w = doJSON(t, s, http.MethodPost, "/api/admin/users", masterToken,
		`{"username":"u-master","password":"pass1234","role":"user","permissions":[{"path":"Q:\\anywhere","read":true,"write":true}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("主人授予不受限制: %d %s", w.Code, w.Body.String())
	}
}

// TestSettingsTrashPathValidation：回收站路径格式校验
func TestSettingsTrashPathValidation(t *testing.T) {
	s := newTestServer(t)
	masterToken := login(t, s, "admin", "admin123")
	// 非法路径（相对路径）→ 400
	w := doJSON(t, s, http.MethodPut, "/api/admin/settings", masterToken,
		`{"trash_path":"relative/path"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("相对路径应 400: %d %s", w.Code, w.Body.String())
	}
	// 正斜杠绝对路径 → 归一化接受
	w = doJSON(t, s, http.MethodPut, "/api/admin/settings", masterToken,
		`{"trash_path":"C:/nas-trash-test"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("正斜杠路径应归一化接受: %d %s", w.Code, w.Body.String())
	}
	// 恢复默认，避免影响其他用例
	_ = doJSON(t, s, http.MethodPut, "/api/admin/settings", masterToken, `{"trash_path":""}`)
}
