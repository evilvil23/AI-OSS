// Package store 提供基于 TOML 文件落盘的通用键值存储，用于替代
// GORM + SQLite（当前离线环境不可用）。接口保持与文档一致，未来可
// 平滑替换为 GORM + PostgreSQL/SQLite（实现 Repository/MetadataStore 接口）。
//
// 历史版本曾以 JSON 落盘（*.json）；启动时会自动将旧 JSON 数据迁移为
// TOML（*.toml）并删除旧文件，迁移过程无需人工干预。
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Store 通用 TOML 文件表存储（key 为 uint/string 主键）
type Store[K comparable, V any] struct {
	path string
	mu   sync.RWMutex
	data map[K]V
}

// NewStore 创建存储并加载已有数据（优先 *.toml；仅存在旧 *.json 时自动迁移）
func NewStore[K comparable, V any](path string) (*Store[K, V], error) {
	s := &Store[K, V]{path: path, data: make(map[K]V)}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := unmarshalTOML(data, &s.data); err != nil {
			return nil, err
		}
		return s, nil
	}
	// 旧版 JSON 数据迁移：读取后立即转存 TOML 并移除旧文件
	legacy := strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
	if data, err := os.ReadFile(legacy); err == nil {
		if err := json.Unmarshal(data, &s.data); err != nil {
			return nil, err
		}
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
		_ = os.Remove(legacy)
	}
	return s, nil
}

// Get 按主键读取
func (s *Store[K, V]) Get(k K) (V, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[k]
	return v, ok
}

// Set 写入并立即落盘
func (s *Store[K, V]) Set(k K, v V) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[k] = v
	return s.saveLocked()
}

// Delete 删除并落盘
func (s *Store[K, V]) Delete(k K) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[k]; !ok {
		return nil
	}
	delete(s.data, k)
	return s.saveLocked()
}

// All 返回全部数据副本
func (s *Store[K, V]) All() map[K]V {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[K]V, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// Size 返回记录数
func (s *Store[K, V]) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

func (s *Store[K, V]) saveLocked() error {
	tmp := s.path + ".tmp"
	data, err := toml.Marshal(s.data)
	if err != nil {
		return err
	}
	// go-toml 无法将亚毫秒精度的 time.Time 编码为 TOML 日期时间（会退化成
	// 字符串，且重新加载时无法回读），写入前把退化的字符串还原为毫秒精度
	// 日期时间字面量。
	fixed := fixDegradedDatetimes(string(data))
	if err := os.WriteFile(tmp, []byte(fixed), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Close 无资源需要释放（预留）
func (s *Store[K, V]) Close() error { return nil }

// datetimeStrRe 匹配被引号包裹的 RFC3339 时间（含退化字符串形式）
var datetimeStrRe = regexp.MustCompile(`'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?)'`)

// degradedFracRe 仅匹配“非毫秒精度”小数秒（1-2 位或 4 位以上）的字符串时间：
// go-toml 对毫秒精度/整秒时间会正常编码为 TOML 日期时间，只有亚毫秒精度才
// 退化为字符串；以此区分真正的退化时间与恰好形如时间的普通字符串字段，
// 避免误改业务字符串。
var degradedFracRe = regexp.MustCompile(`'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.(?:\d{1,2}|\d{4,})(?:Z|[+-]\d{2}:\d{2})?'`)

// fixDegradedDatetimes 把字符串形式的退化时间还原为毫秒精度 TOML 日期时间
func fixDegradedDatetimes(s string) string {
	return degradedFracRe.ReplaceAllStringFunc(s, unquoteDatetime)
}

func unquoteDatetime(m string) string {
	inner := strings.Trim(m, "'")
	t, err := time.Parse(time.RFC3339Nano, inner)
	if err != nil {
		return m
	}
	return t.Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z07:00")
}

// unmarshalTOML 解码 TOML；若因“时间被编码为字符串”的旧数据解码失败，
// 则将引号内的时间还原为 TOML 日期时间（毫秒精度）后重试。
func unmarshalTOML(data []byte, out any) error {
	err := toml.Unmarshal(data, out)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "time.Time") {
		return err
	}
	fixed := datetimeStrRe.ReplaceAllStringFunc(string(data), unquoteDatetime)
	if rerr := toml.Unmarshal([]byte(fixed), out); rerr != nil {
		return fmt.Errorf("%w（旧数据时间格式修复后仍失败: %v）", err, rerr)
	}
	return nil
}
