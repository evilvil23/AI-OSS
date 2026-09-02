// lifecycle.go 备份生命周期管理：配额（数量/大小双阈值）自动清理 + 冻结保护。
//
// 铁律：自动清理逻辑绝不删除冻结备份（需求文档 §4.1）。
package backup

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// parseSize 解析人类可读大小（"10GB"、"500MB"、"1TB"、纯数字=字节）
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("大小为空")
	}
	re := regexp.MustCompile(`^([0-9.]+)\s*(B|KB|MB|GB|TB)?$`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("无法解析大小: %s", s)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	switch m[2] {
	case "KB":
		v *= 1024
	case "MB":
		v *= 1024 * 1024
	case "GB":
		v *= 1024 * 1024 * 1024
	case "TB":
		v *= 1024 * 1024 * 1024 * 1024
	}
	return int64(v), nil
}

// cleanupAfterBackup 备份完成后执行清理（仅处理非冻结备份）：
// 按最早优先删除，直到同时满足数量阈值与大小阈值（不含冻结部分）。
// 返回被清理的历史 ID 列表。
func (s *Service) cleanupAfterBackup(taskID int64) ([]int64, error) {
	t, err := s.repo.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	histories, err := s.repo.ListHistories(taskID, 0)
	if err != nil {
		return nil, err
	}

	// 冻结部分不计入配额（需求 §2.4.2）
	frozenCount, _ := s.repo.FrozenCount(taskID)
	frozenSize, _ := s.repo.FrozenSize(taskID)

	maxCount := t.MaxBackupCount
	maxSize := int64(0)
	if t.MaxBackupSize != "" {
		if maxSize, err = parseSize(t.MaxBackupSize); err != nil {
			maxSize = 0 // 配置无效时不按大小清理
		}
	}
	availableCount := maxCount - frozenCount
	availableSize := maxSize - frozenSize
	if maxCount <= 0 && maxSize <= 0 {
		return nil, nil
	}

	var removed []int64
	// 从最旧开始（列表为新→旧，倒序遍历）
	for i := len(histories) - 1; i >= 0; i-- {
		h := histories[i]
		if h.IsFrozen {
			continue // 绝不触碰冻结备份
		}
		needByCount := maxCount > 0 && len(histories)-len(removed) > availableCount
		needBySize := maxSize > 0 && h.TotalSize > 0 && totalActiveSize(histories, removedSet(removed)) > availableSize
		if !needByCount && !needBySize {
			break
		}
		// 物理删除产物（含快照 sidecar）+ 记录
		_ = os.RemoveAll(h.StorePath)
		_ = os.Remove(h.StorePath + snapshotSuffix)
		if err := s.repo.DeleteHistory(h.ID); err != nil {
			continue
		}
		removed = append(removed, h.ID)
	}
	if len(removed) > 0 {
		s.logf("任务 %d 自动清理了 %d 份非冻结备份", taskID, len(removed))
	}
	return removed, nil
}

// removedSet ID 集合
func removedSet(ids []int64) map[int64]struct{} {
	m := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// totalActiveSize 未被清理的历史总大小
func totalActiveSize(histories []*History, removed map[int64]struct{}) int64 {
	var total int64
	for _, h := range histories {
		if _, ok := removed[h.ID]; ok {
			continue
		}
		total += h.TotalSize
	}
	return total
}
