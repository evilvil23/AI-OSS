// Package settings 系统设置：可不通过配置文件在界面中调整的运行参数。
//
// 持久化采用 TOML（settings.toml），与 config.toml 保持一致风格。
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"

	"smart-nas/internal/util"
)

// Settings 系统设置项
type Settings struct {
	CPURefreshSeconds  int    `toml:"cpu_refresh_seconds" json:"cpu_refresh_seconds"`   // CPU/内存刷新频率（秒）
	DiskRefreshSeconds int    `toml:"disk_refresh_seconds" json:"disk_refresh_seconds"` // 磁盘使用刷新频率（秒）
	TrashPath          string `toml:"trash_path" json:"trash_path"`                     // 全局回收站目录（空=各磁盘 .trash）
	LogPath            string `toml:"log_path" json:"log_path"`                         // 日志文件路径（空=使用 config.toml）
	LogMaxSize         int    `toml:"log_max_size" json:"log_max_size"`                 // 单个日志文件最大大小（MB）
	LogMaxAge          int    `toml:"log_max_age" json:"log_max_age"`                   // 日志保留天数
	BackupOutputDir    string `toml:"backup_output_dir" json:"backup_output_dir"`       // 备份全局默认存放目录（新建任务默认值）
	BackupCompressLevel int   `toml:"backup_compress_level" json:"backup_compress_level"` // 备份全局默认压缩级别（1-9，默认 6）
}

// Default 返回默认设置
func Default() *Settings {
	return &Settings{
		CPURefreshSeconds:   5,
		DiskRefreshSeconds:  60,
		LogMaxSize:          100,
		LogMaxAge:           30,
		BackupCompressLevel: 6,
	}
}

// Service 设置存取服务（基于 TOML 文件持久化）
type Service struct {
	path string
	mu   sync.RWMutex
	cfg  *Settings
}

// NewService 创建设置服务
func NewService(dataDir string) (*Service, error) {
	path := filepath.Join(dataDir, "settings.toml")
	s := &Service{path: path, cfg: Default()}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, s.cfg); err != nil {
			return nil, err
		}
		s.cfg.ensureValid()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else if s.migrateLegacy(dataDir) {
		// 旧版 settings.json 已迁移为 settings.toml
	} else if err := s.save(); err != nil {
		return nil, err
	}
	return s, nil
}

// migrateLegacy 迁移旧版 settings.json（v0.06 及以前）：读取成功后转存
// TOML 并删除旧文件；不存在或解析失败时返回 false（沿用默认值）。
func (s *Service) migrateLegacy(dataDir string) bool {
	legacy := filepath.Join(dataDir, "settings.json")
	data, err := os.ReadFile(legacy)
	if err != nil {
		return false
	}
	var old struct {
		Settings Settings `json:"settings"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		return false
	}
	s.cfg = &old.Settings
	s.cfg.ensureValid()
	if err := s.save(); err != nil {
		return false
	}
	_ = os.Remove(legacy)
	return true
}

// Get 返回当前设置副本
func (s *Service) Get() *Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := *s.cfg
	cp.ensureValid()
	return &cp
}

// Update 部分更新设置，返回更新后的完整设置
func (s *Service) Update(patch map[string]interface{}) (*Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := *s.cfg
	for k, v := range patch {
		switch k {
		case "cpu_refresh_seconds":
			n, err := toInt(v)
			if err != nil || n < 2 {
				return nil, errors.New("CPU/内存刷新频率至少为 2 秒")
			}
			cur.CPURefreshSeconds = n
		case "disk_refresh_seconds":
			n, err := toInt(v)
			if err != nil {
				return nil, errors.New("磁盘刷新频率无效")
			}
			cur.DiskRefreshSeconds = n
		case "trash_path":
			if str, ok := v.(string); ok {
				str = strings.TrimSpace(str)
				// 保存前校验：须为盘符开头的合法 Windows 绝对路径（v0.13）
				if str != "" {
					str = util.NormalizeWinPath(str)
					if !util.IsValidAbsPath(str) {
						return nil, errors.New("回收站路径不合法：应为盘符开头的绝对路径，如 Y:\\nas\\trash")
					}
				}
				cur.TrashPath = str
			}
		case "log_path":
			if str, ok := v.(string); ok {
				str = strings.TrimSpace(str)
				// 保存前校验目录可创建，避免无效路径导致重启后日志初始化失败
				if str != "" {
					if err := os.MkdirAll(filepath.Dir(str), 0o755); err != nil {
						return nil, errors.New("日志目录不可用: " + err.Error())
					}
				}
				cur.LogPath = str
			}
		case "log_max_size":
			n, err := toInt(v)
			if err != nil || n < 1 {
				return nil, errors.New("日志最大大小至少为 1 MB")
			}
			cur.LogMaxSize = n
		case "log_max_age":
			n, err := toInt(v)
			if err != nil || n < 1 {
				return nil, errors.New("日志保留天数至少为 1 天")
			}
			cur.LogMaxAge = n
		case "backup_output_dir":
			if str, ok := v.(string); ok {
				str = strings.TrimSpace(str)
				if str != "" {
					str = util.NormalizeWinPath(str)
					if !util.IsValidAbsPath(str) {
						return nil, errors.New("备份存放目录不合法：应为盘符开头的绝对路径，如 Y:\\nas-backup")
					}
				}
				cur.BackupOutputDir = str
			}
		case "backup_compress_level":
			n, err := toInt(v)
			if err != nil || n < 1 || n > 9 {
				return nil, errors.New("压缩级别应为 1-9")
			}
			cur.BackupCompressLevel = n
		default:
			return nil, errors.New("未知设置项: " + k)
		}
	}
	cur.ensureValid()
	if err := s.saveWith(&cur); err != nil {
		return nil, err
	}
	s.cfg = &cur
	return &cur, nil
}

func (s *Service) save() error {
	return s.saveWith(s.cfg)
}

func (s *Service) saveWith(c *Settings) error {
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

func (c *Settings) ensureValid() {
	if c.CPURefreshSeconds < 2 {
		c.CPURefreshSeconds = 5
	}
	if c.DiskRefreshSeconds < 5 {
		c.DiskRefreshSeconds = 60
	}
	if c.LogMaxSize < 1 {
		c.LogMaxSize = 100
	}
	if c.LogMaxAge < 1 {
		c.LogMaxAge = 30
	}
	if c.BackupCompressLevel < 1 || c.BackupCompressLevel > 9 {
		c.BackupCompressLevel = 6
	}
}

func toInt(v interface{}) (int, error) {
	switch t := v.(type) {
	case float64:
		return int(t), nil
	case int:
		return t, nil
	case string:
		return strconv.Atoi(t)
	default:
		return 0, errors.New("无效数值")
	}
}
