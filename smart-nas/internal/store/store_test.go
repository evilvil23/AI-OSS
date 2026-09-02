package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testRec struct {
	Name string     `toml:"name"`
	At   *time.Time `toml:"at"` // 指针时间：go-toml 对亚毫秒精度会退化为字符串，重点覆盖
}

func TestStoreTOMLPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.toml")
	s, err := NewStore[string, testRec](path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	at := time.Unix(1700000000, 0).UTC()
	if err := s.Set("1", testRec{Name: "a", At: &at}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set("2", testRec{Name: "b"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// 重新加载：数据应完整读回
	s2, err := NewStore[string, testRec](path)
	if err != nil {
		t.Fatalf("重新加载: %v", err)
	}
	if s2.Size() != 2 {
		t.Fatalf("应有 2 条记录, got %d", s2.Size())
	}
	v, ok := s2.Get("1")
	if !ok || v.Name != "a" {
		t.Fatalf("记录 1 不符: %+v ok=%v", v, ok)
	}
	if v.At == nil || !v.At.Equal(time.Unix(1700000000, 0).UTC()) {
		t.Fatalf("时间字段往返不一致: %v", v.At)
	}
	if err := s2.Delete("2"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s2.Size() != 1 {
		t.Fatalf("删除后应剩 1 条, got %d", s2.Size())
	}
}

// TestStoreNanosecondTimeRoundTrip：纳秒精度时间持久化往返（TOML 日期时间仅支持毫秒）
func TestStoreNanosecondTimeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "times.toml")
	s, err := NewStore[string, testRec](path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// time.Now() 带纳秒精度；指针时间字段是 go-toml 退化为字符串的路径
	now := time.Now()
	rec := testRec{Name: "ns", At: &now}
	if err := s.Set("1", rec); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s2, err := NewStore[string, testRec](path)
	if err != nil {
		t.Fatalf("重新加载（纳秒时间应自动截断为毫秒）: %v", err)
	}
	v, ok := s2.Get("1")
	if !ok {
		t.Fatal("记录丢失")
	}
	if v.At == nil || v.At.Nanosecond()%1e6 != 0 {
		t.Fatalf("时间应截断到毫秒: %v", v.At)
	}
}

// TestStoreRecoversDegradedDatetime：历史版本写入的“字符串形式时间”可被自动修复读取
func TestStoreRecoversDegradedDatetime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	// 模拟 go-toml 对亚毫秒时间退化成的字符串写法
	bad := `['1']
Name = '旧记录'
At = '2026-08-22T20:29:28.3149446+08:00'
`
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore[string, testRec](path)
	if err != nil {
		t.Fatalf("应自动修复字符串时间并加载: %v", err)
	}
	v, ok := s.Get("1")
	if !ok || v.Name != "旧记录" {
		t.Fatalf("修复后数据不符: %+v ok=%v", v, ok)
	}
	if v.At == nil || v.At.IsZero() {
		t.Fatal("时间字段未恢复")
	}
}

// TestStoreMigratesLegacyJSON：仅有旧 *.json 时自动迁移为 *.toml 并删除旧文件
func TestStoreMigratesLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "data.json")
	tomlPath := filepath.Join(dir, "data.toml")
	jsonData := `{"1": {"name": "旧数据", "at": "2026-08-22T10:00:00Z"}}`
	if err := os.WriteFile(legacy, []byte(jsonData), 0o644); err != nil {
		t.Fatalf("写入旧 JSON: %v", err)
	}
	s, err := NewStore[string, testRec](tomlPath)
	if err != nil {
		t.Fatalf("NewStore（迁移）: %v", err)
	}
	v, ok := s.Get("1")
	if !ok || v.Name != "旧数据" {
		t.Fatalf("迁移后数据不符: %+v ok=%v", v, ok)
	}
	if _, err := os.Stat(tomlPath); err != nil {
		t.Fatalf("应生成 TOML 文件: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("旧 JSON 应被删除, err=%v", err)
	}
	// 再次加载：从 TOML 读取（不再依赖 JSON）
	s2, err := NewStore[string, testRec](tomlPath)
	if err != nil {
		t.Fatalf("再次加载: %v", err)
	}
	if _, ok := s2.Get("1"); !ok {
		t.Fatal("TOML 中应保留迁移数据")
	}
}
