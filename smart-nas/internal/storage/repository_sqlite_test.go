package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := NewRepository(filepath.Join(dir, "metadata.db"), dir)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, dir
}

func TestSQLiteCRUDRoundtrip(t *testing.T) {
	r, _ := newTestRepo(t)
	f := &FileMeta{Name: "照片", ParentID: 1, IsDir: true, StoragePath: `D:\照片`}
	if err := r.Create(f); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if f.ID == 0 {
		t.Fatal("ID 应由 SQLite 自增分配")
	}
	got, err := r.Get(f.ID)
	if err != nil || got.Name != "照片" || got.ParentID != 1 {
		t.Fatalf("Get 不符: %+v err=%v", got, err)
	}
	// 更新：created_at 保持、deleted_at 字段往返生效
	created := got.CreatedAt
	got.Name = "图片"
	got.DeletedAt = time.Now()
	if err := r.Update(got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2, err := r.Get(f.ID)
	if err != nil {
		t.Fatalf("Update 后 Get 失败: %v", err)
	}
	if got2.Name != "图片" || got2.DeletedAt.IsZero() || !got2.CreatedAt.Equal(created) {
		t.Fatalf("Update 后不符: %+v", got2)
	}
	got2.DeletedAt = time.Time{}
	if err := r.Update(got2); err != nil {
		t.Fatalf("清除删除标记失败: %v", err)
	}
	// 删除（软）→ 回收站可见；物理清除
	if err := r.Delete(got2); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(r.InTrash()) != 1 {
		t.Fatal("软删除后应进入回收站")
	}
	if _, err := r.Get(f.ID); err != nil {
		// Get 仍应能取到（含回收站条目，供恢复使用）
		t.Fatalf("回收站条目应可 Get: %v", err)
	}
	if err := r.Purge(f.ID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if len(r.InTrash()) != 0 {
		t.Fatal("Purge 后回收站应为空")
	}
}

func TestSQLiteIndexedQueries(t *testing.T) {
	r, _ := newTestRepo(t)
	mk := func(name string, parent uint, dir bool, size int64, md string) *FileMeta {
		f := &FileMeta{Name: name, ParentID: parent, IsDir: dir, Size: size, MD5: md, StoragePath: `D:\` + name}
		if err := r.Create(f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	mk("dirA", 1, true, 0, "")
	a := mk("a.txt", 2, false, 10, "md5a")
	mk("b.txt", 2, false, 20, "md5b")
	mk("abc.txt", 3, false, 10, "md5a")

	// ByParent：目录优先、按名排序
	bp := r.ByParent(2)
	if len(bp) != 2 || bp[0].Name != "a.txt" {
		t.Fatalf("ByParent 不符: %+v", bp)
	}
	// ByParentName
	if f := r.ByParentName(2, "b.txt"); f == nil || f.ID == 0 {
		t.Fatal("ByParentName 未命中")
	}
	// ByMD5：同 MD5 同大小命中，不同大小不命中
	if f := r.ByMD5("md5a", 10); f == nil || f.ID != a.ID {
		t.Fatalf("ByMD5 不符: %+v", f)
	}
	if f := r.ByMD5("md5a", 999); f != nil {
		t.Fatal("ByMD5 大小不同不应命中")
	}
	// Search：instr 模糊匹配（大小写敏感，与旧实现 strings.Contains 一致）
	sr := r.Search("a")
	if len(sr) != 2 || sr[0].Name != "a.txt" {
		t.Fatalf("Search 不符: %+v", namesOf(sr))
	}
	if sr2 := r.Search("A"); len(sr2) != 1 || sr2[0].Name != "dirA" {
		t.Fatalf("Search 大写不符: %+v", namesOf(sr2))
	}
	// 回收站条目不出现在 Search/ByParent
	if err := r.Delete(a); err != nil {
		t.Fatal(err)
	}
	if len(r.Search("a.txt")) != 0 {
		t.Fatal("回收站条目不应出现在搜索")
	}
	if len(r.ByParent(2)) != 1 {
		t.Fatal("回收站条目不应出现在目录列表")
	}
}

func namesOf(list []*FileMeta) []string {
	var out []string
	for _, f := range list {
		out = append(out, f.Name)
	}
	return out
}

func TestSQLiteVersions(t *testing.T) {
	r, _ := newTestRepo(t)
	f := &FileMeta{Name: "doc.txt", ParentID: 1, StoragePath: `D:\doc.txt`}
	if err := r.Create(f); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		time.Sleep(2 * time.Millisecond) // 保证 created_at 严格递增
		if err := r.AddVersion(&FileVersion{FileID: f.ID, Version: i, Size: int64(i), StoragePath: `D:\.versions\v` + string(rune('0'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	vs := r.Versions(f.ID)
	if len(vs) != 4 || vs[0].Version != 4 {
		t.Fatalf("版本应新→旧: %+v", vs)
	}
	removed := r.TrimVersions(f.ID, 2)
	if len(removed) != 2 || len(r.Versions(f.ID)) != 2 {
		t.Fatalf("TrimVersions 不符: removed=%d left=%d", len(removed), len(r.Versions(f.ID)))
	}
	if r.VersionBytes() != 3+4 { // 剩余 v3(3) + v4(4)
		t.Fatalf("VersionBytes 不符: %d", r.VersionBytes())
	}
}

func TestSQLiteMigrateLegacyTOML(t *testing.T) {
	dir := t.TempDir()
	// 构造旧 TOML（保留原 ID）
	legacy := `
[1]
ID = 1
Name = 'old-a'
ParentID = 0
IsDir = true
Size = 0
StoragePath = 'D:\\a'
CreatedAt = 2026-08-22T10:00:00.000+08:00
UpdatedAt = 2026-08-22T10:00:00.000+08:00

[5]
ID = 5
Name = 'old-b'
ParentID = 1
IsDir = false
Size = 42
MD5 = 'abc'
StoragePath = 'D:\\a\\b.txt'
CreatedAt = 2026-08-22T11:00:00.000+08:00
UpdatedAt = 2026-08-22T11:00:00.000+08:00
`
	if err := os.WriteFile(filepath.Join(dir, "file_metas.toml"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := NewRepository(filepath.Join(dir, "metadata.db"), dir)
	if err != nil {
		t.Fatalf("迁移启动失败: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if len(r.All()) != 2 {
		t.Fatalf("应导入 2 条: %d", len(r.All()))
	}
	f, err := r.Get(5)
	if err != nil || f.Name != "old-b" || f.MD5 != "abc" {
		t.Fatalf("导入数据不符: %+v err=%v", f, err)
	}
	// 旧文件改名保留备份
	if _, err := os.Stat(filepath.Join(dir, "file_metas.toml")); !os.IsNotExist(err) {
		t.Fatal("旧 TOML 应被改名")
	}
	if _, err := os.Stat(filepath.Join(dir, "file_metas.toml.migrated")); err != nil {
		t.Fatalf("应保留 .migrated 备份: %v", err)
	}
	// 自增 ID 从导入最大值之后继续
	nf := &FileMeta{Name: "new", ParentID: 1, StoragePath: `D:\a\new`}
	if err := r.Create(nf); err != nil {
		t.Fatal(err)
	}
	if nf.ID <= 5 {
		t.Fatalf("新 ID 应大于导入最大 ID 5, got %d", nf.ID)
	}
	// 重复启动不再导入（.migrated 不被读取）
	r2, err := NewRepository(filepath.Join(dir, "metadata.db"), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if len(r2.All()) != 3 {
		t.Fatalf("重启后不应重复导入: %d", len(r2.All()))
	}
}

func TestSQLiteReopenPersistence(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "metadata.db")
	r1, err := NewRepository(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &FileMeta{Name: "persist.txt", ParentID: 0, StoragePath: `D:\persist.txt`, Size: 7, MD5: "xyz"}
	if err := r1.Create(f); err != nil {
		t.Fatal(err)
	}
	if err := r1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r2, err := NewRepository(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	got, err := r2.Get(f.ID)
	if err != nil || got.Name != "persist.txt" {
		t.Fatalf("重启后数据丢失: %+v err=%v", got, err)
	}
}
