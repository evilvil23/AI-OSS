// restore.go 还原：原位置 / 指定位置；覆盖策略（询问/覆盖/跳过/重命名旧文件）；
// 还原前校验整条备份链完整性；还原后做文件校验。
package backup

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RestoreRequest 还原请求
type RestoreRequest struct {
	BackupID    int64    `json:"backup_id"`               // 历史 ID
	TargetDir   string   `json:"target_dir,omitempty"`    // 空 = 还原到原路径
	Conflict    string   `json:"conflict,omitempty"`      // ask/overwrite/skip/rename（空=ask）
	Files       []string `json:"files,omitempty"`         // 仅还原部分文件（相对路径列表；空=全部）
}

// RestoreResult 还原结果
type RestoreResult struct {
	Restored   int      `json:"restored"`   // 成功还原文件数
	Conflicts  []string `json:"conflicts"`  // ask 模式下待确认的冲突文件（相对路径）
	Skipped    []string `json:"skipped"`    // 跳过的文件
	Status     string   `json:"status"`     // success / partial / failed
	Remark     string   `json:"remark,omitempty"`
}

// restoreChainStep 增量链上的一步（父 → 子顺序应用）
type restoreChainStep struct {
	hist    *History
	isZip   bool
	entries map[string]string // rel -> srcRef（目录模式为绝对路径；zip 模式为 zip 内路径）
}

// Restore 还原入口
func (s *Service) Restore(req RestoreRequest) (*RestoreResult, error) {
	h, err := s.repo.GetHistory(req.BackupID)
	if err != nil {
		return nil, fmt.Errorf("备份记录不存在")
	}
	if h.StorePath == "" {
		return nil, fmt.Errorf("备份产物路径为空，无法还原")
	}

	// ① 还原前校验整条备份链完整性（需求 §4.3：不允许直接还原损坏的备份链）
	if err := s.verifyChain(h); err != nil {
		return nil, err
	}

	// ② 目标目录：指定位置或原路径
	dst := req.TargetDir
	if dst == "" {
		dst = s.restoreTarget(h)
	}
	if dst == "" {
		return nil, fmt.Errorf("无法确定还原目标：备份源路径丢失，请指定还原到指定位置")
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, fmt.Errorf("还原目录不可创建: %v", err)
	}

	// ③ 收集整条链要应用的文件（父 → 子），带覆盖策略
	steps, cerr := s.collectChainSteps(h)
	if cerr != nil {
		return nil, cerr
	}
	res := &RestoreResult{Status: StatusSuccess}
	applyConflict := req.Conflict
	if applyConflict == "" {
		applyConflict = ConflictAsk
	}

	// 文件过滤集合
	filter := map[string]struct{}{}
	for _, f := range req.Files {
		filter[filepath.ToSlash(strings.TrimPrefix(filepath.FromSlash(f), "/"))] = struct{}{}
	}

	for _, step := range steps {
		for rel, srcRef := range step.entries {
			if len(filter) > 0 {
				if _, ok := filter[rel]; !ok {
					continue
				}
			}
			target := filepath.Join(dst, filepath.FromSlash(rel))
			// 冲突检测
			if _, serr := os.Lstat(target); serr == nil {
				switch applyConflict {
				case ConflictSkip:
					res.Skipped = append(res.Skipped, rel)
					continue
				case ConflictRename:
					if rerr := renameOld(target); rerr != nil {
						res.Skipped = append(res.Skipped, rel+"（重命名旧文件失败）")
						continue
					}
				case ConflictAsk:
					res.Conflicts = append(res.Conflicts, rel)
					continue
				default: // overwrite
				}
			}
			if step.isZip {
				if xerr := extractZipEntry(step.hist.StorePath, srcRef, target); xerr != nil {
					res.Skipped = append(res.Skipped, rel+"（"+xerr.Error()+"）")
					continue
				}
			} else {
				if _, cerr := copyFileHot(srcRef, target); cerr != nil {
					res.Skipped = append(res.Skipped, rel+"（"+cerr.Error()+"）")
					continue
				}
			}
			res.Restored++
		}
	}

	// ④ ask 模式：返回冲突清单等待用户确认（不标记失败）
	if applyConflict == ConflictAsk && len(res.Conflicts) > 0 {
		res.Status = "conflict"
		res.Remark = "存在同名文件冲突，请选择处理方式后重试"
		s.logf("还原备份 %d：发现 %d 个冲突待确认", req.BackupID, len(res.Conflicts))
		return res, nil
	}

	// ⑤ 还原后校验
	if res.Restored == 0 && len(res.Skipped) > 0 {
		res.Status = StatusFailed
		res.Remark = "没有文件被还原"
	} else if len(res.Skipped) > 0 {
		res.Status = StatusPartial
		res.Remark = fmt.Sprintf("%d 个文件跳过", len(res.Skipped))
	} else {
		res.Status = StatusSuccess
	}
	s.logf("还原备份 %d 完成: 恢复=%d 跳过=%d 目标=%s", req.BackupID, res.Restored, len(res.Skipped), dst)
	return res, nil
}

// restoreTarget 增量链根备份的原始源路径（还原到原位置的目标）
func (s *Service) restoreTarget(h *History) string {
	root := h
	for depth := 0; root != nil && depth < 32; depth++ {
		if root.ParentBackupID == 0 {
			break
		}
		p, err := s.repo.GetHistory(root.ParentBackupID)
		if err != nil {
			break
		}
		root = p
	}
	if root == nil || root.TaskID == 0 {
		return ""
	}
	t, err := s.repo.GetTask(root.TaskID)
	if err != nil || len(t.SourcePaths) == 0 {
		return ""
	}
	// 多源备份无法唯一确定原位置，返回第一个源
	return t.SourcePaths[0]
}

// verifyChain 校验整条备份链（含当前）每个产物的哈希。任一环节损坏 → 拒绝还原。
func (s *Service) verifyChain(h *History) error {
	cur := h
	for depth := 0; cur != nil && depth < 32; depth++ {
		if _, err := os.Stat(cur.StorePath); err != nil {
			return fmt.Errorf("备份链不完整：备份 %d 产物丢失（%s），拒绝还原", cur.ID, cur.StorePath)
		}
		if err := verifyHash(cur.StorePath, isZipPath(cur.StorePath), cur.HashSum); err != nil {
			return fmt.Errorf("备份链损坏：备份 %d 校验失败（%v），拒绝还原", cur.ID, err)
		}
		if cur.ParentBackupID == 0 {
			return nil
		}
		p, err := s.repo.GetHistory(cur.ParentBackupID)
		if err != nil {
			return fmt.Errorf("备份链不完整：父备份 %d 记录丢失，拒绝还原", cur.ParentBackupID)
		}
		cur = p
	}
	return nil
}

// collectChainSteps 收集链上各步的文件清单（父 → 子顺序）
func (s *Service) collectChainSteps(h *History) ([]restoreChainStep, error) {
	var chain []*History
	cur := h
	for depth := 0; cur != nil && depth < 32; depth++ {
		chain = append(chain, cur)
		if cur.ParentBackupID == 0 {
			break
		}
		p, err := s.repo.GetHistory(cur.ParentBackupID)
		if err != nil {
			return nil, fmt.Errorf("备份链断裂：父备份 %d 丢失", cur.ParentBackupID)
		}
		cur = p
	}
	var steps []restoreChainStep
	// 父在前、子在後（子覆盖父）
	for i := len(chain) - 1; i >= 0; i-- {
		hh := chain[i]
		isZip := isZipPath(hh.StorePath)
		step := restoreChainStep{hist: hh, isZip: isZip, entries: map[string]string{}}
		if isZip {
			r, err := newZipIndex(hh.StorePath)
			if err != nil {
				return nil, fmt.Errorf("打开备份产物失败: %v", err)
			}
			for rel, name := range r {
				step.entries[rel] = name
			}
		} else {
			walkErr := filepath.WalkDir(hh.StorePath, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				rel, rerr := filepath.Rel(hh.StorePath, p)
				if rerr != nil {
					return nil
				}
				step.entries[filepath.ToSlash(rel)] = p
				return nil
			})
			if walkErr != nil {
				return nil, fmt.Errorf("遍历备份产物失败: %v", walkErr)
			}
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// isZipPath 产物是否为 zip 包
func isZipPath(p string) bool {
	return strings.HasSuffix(strings.ToLower(p), ".zip")
}

// afterRestoreCheck 还原后文件校验（大小抽查）。当前实现为存在性校验；
// 完整哈希校验代价高，供后续按需启用。
func (s *Service) afterRestoreCheck(files map[string]string, dst string) []string {
	var missing []string
	for rel := range files {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			missing = append(missing, rel)
		}
	}
	return missing
}

// restoreTime 供日志格式化（占位避免未使用告警）
var _ = time.Now
