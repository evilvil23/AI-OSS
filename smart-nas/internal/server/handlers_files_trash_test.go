package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// trashTestFile 在磁盘下准备一个真实文件并返回其 ID（通过列表同步）
func trashTestFile(t *testing.T, s *Server, diskID uint, diskDir, name string) uint {
	t.Helper()
	if err := os.WriteFile(filepath.Join(diskDir, name), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	token := login(t, s, "admin", "admin123")
	w := doJSON(t, s, http.MethodGet, fmt.Sprintf("/api/files?parent_id=%d", diskID), token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("列目录失败: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			ID    uint   `json:"id"`
			Name  string `json:"name"`
			IsDir bool   `json:"is_dir"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, f := range resp.Data {
		if f.Name == name && !f.IsDir {
			return f.ID
		}
	}
	t.Fatalf("未同步到测试文件 %s", name)
	return 0
}

func firstDisk(t *testing.T, s *Server) (uint, string) {
	t.Helper()
	token := login(t, s, "admin", "admin123")
	w := doJSON(t, s, http.MethodGet, "/api/files?parent_id=0", token, "")
	var resp struct {
		Data []struct {
			ID          uint   `json:"id"`
			StoragePath string `json:"storage_path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || len(resp.Data) == 0 {
		t.Fatalf("磁盘列表异常: %v %s", err, w.Body.String())
	}
	return resp.Data[0].ID, resp.Data[0].StoragePath
}

// TestTrashEndpoints：回收站 删除→purge→restore-all→clear 接口链路
func TestTrashEndpoints(t *testing.T) {
	s := newTestServer(t)
	token := login(t, s, "admin", "admin123")
	diskID, diskDir := firstDisk(t, s)

	// 1) 删除两个文件 → 批量物理删除一个
	idA := trashTestFile(t, s, diskID, diskDir, "ta.txt")
	idB := trashTestFile(t, s, diskID, diskDir, "tb.txt")
	w := doJSON(t, s, http.MethodDelete, "/api/files", token, fmt.Sprintf(`{"ids":[%d,%d]}`, idA, idB))
	if w.Code != http.StatusOK {
		t.Fatalf("删除失败: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(t, s, http.MethodPost, "/api/files/trash/purge", token, fmt.Sprintf(`{"ids":[%d]}`, idA))
	if w.Code != http.StatusOK {
		t.Fatalf("purge 失败: %d %s", w.Code, w.Body.String())
	}
	var purged struct {
		Data struct {
			Purged int `json:"purged"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &purged); err != nil || purged.Data.Purged != 1 {
		t.Fatalf("purge 数量不符: %s", w.Body.String())
	}

	// 2) 再删一个 → 一键还原
	idC := trashTestFile(t, s, diskID, diskDir, "tc.txt")
	w = doJSON(t, s, http.MethodDelete, "/api/files", token, fmt.Sprintf(`{"ids":[%d]}`, idC))
	if w.Code != http.StatusOK {
		t.Fatalf("删除失败: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(t, s, http.MethodPost, "/api/files/trash/restore-all", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("restore-all 失败: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(diskDir, "tc.txt")); err != nil {
		t.Fatalf("一键还原后文件应回到原位置: %v", err)
	}
	if _, err := os.Stat(filepath.Join(diskDir, "tb.txt")); err != nil {
		t.Fatalf("tb.txt 应已还原: %v", err)
	}

	// 3) 再删两个 → 一键清空
	idD := trashTestFile(t, s, diskID, diskDir, "td.txt")
	idE := trashTestFile(t, s, diskID, diskDir, "te.txt")
	w = doJSON(t, s, http.MethodDelete, "/api/files", token, fmt.Sprintf(`{"ids":[%d,%d]}`, idD, idE))
	if w.Code != http.StatusOK {
		t.Fatalf("删除失败: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(t, s, http.MethodPost, "/api/files/trash/clear", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("clear 失败: %d %s", w.Code, w.Body.String())
	}
	var cleared struct {
		Data struct {
			Cleared int `json:"cleared"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cleared); err != nil || cleared.Data.Cleared < 1 {
		t.Fatalf("clear 数量不符: %s", w.Body.String())
	}
	w = doJSON(t, s, http.MethodGet, "/api/files/trash", token, "")
	var trash struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &trash); err != nil || len(trash.Data) != 0 {
		t.Fatalf("清空后回收站应为空: %s", w.Body.String())
	}
}

// TestListFilesHidesProtectedEntries：列表接口过滤隐藏/系统文件（$RECYCLE.BIN 等）
func TestListFilesHidesProtectedEntries(t *testing.T) {
	s := newTestServer(t)
	token := login(t, s, "admin", "admin123")
	diskID, diskDir := firstDisk(t, s)
	for _, name := range []string{"$RECYCLE.BIN", "System Volume Information"} {
		if err := os.MkdirAll(filepath.Join(diskDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(diskDir, "pagefile.sys"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(diskDir, "普通文件.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := doJSON(t, s, http.MethodGet, fmt.Sprintf("/api/files?parent_id=%d", diskID), token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("列目录失败: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, f := range resp.Data {
		if f.Name == "$RECYCLE.BIN" || f.Name == "System Volume Information" || f.Name == "pagefile.sys" {
			t.Fatalf("受保护条目不应出现在列表中: %s", f.Name)
		}
	}
	found := false
	for _, f := range resp.Data {
		if f.Name == "普通文件.txt" {
			found = true
		}
	}
	if !found {
		t.Fatal("普通文件应出现在列表中")
	}
}
