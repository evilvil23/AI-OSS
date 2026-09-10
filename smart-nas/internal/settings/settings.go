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
	TrashPath          string `toml:"trash_path" json:"trash_path"`                     // 全局回收站目录（空=运行目录 data/trash）
	LogPath            string `toml:"log_path" json:"log_path"`                         // 日志文件路径（空=使用 config.toml）
	LogMaxSize         int    `toml:"log_max_size" json:"log_max_size"`                 // 单个日志文件最大大小（MB）
	LogMaxAge          int    `toml:"log_max_age" json:"log_max_age"`                   // 日志保留天数
	BackupOutputDir    string `toml:"backup_output_dir" json:"backup_output_dir"`         // 备份全局默认存放目录（新建任务默认值）
	BackupCompressLevel int   `toml:"backup_compress_level" json:"backup_compress_level"` // 备份全局默认压缩级别（1-9，默认 6）
	// 备份全局默认排除规则：独立存储于数据目录下 exclude-list.txt（每行一条，支持 # 注释），
	// 不再写入 settings.toml；内存中保留供 API 与备份预填使用
	BackupExcludeRules []string `toml:"-" json:"backup_exclude_rules"`
}

// DefaultBackupExcludes 备份全局排除规则默认预设（v0.21.2）。
// 覆盖 Windows / Linux 系统目录、开发项目产物、NAS / 跨平台临时文件；
// 可直接用作 exclude-list.txt，支持修改。
// 规则语义与任务级排除一致（Windows 下大小写不敏感）：
//   - 精确名称 / 通配符（*.tmp）：按文件（目录）名匹配，任意层级生效
//   - 带斜杠的目录前缀（proc/）：仅匹配备份源根目录下的顶层路径
//   - 「#」开头为注释行，引擎自动忽略，仅作分组说明
func DefaultBackupExcludes() []string {
	return []string{
	"# 虚拟内存、休眠文件",
		"pagefile.sys",
		"hiberfil.sys",
		"swapfile.sys",
		"",
		"# 回收站、系统还原点",
		"$RECYCLE.BIN",
		"System Volume Information",
		"",
		"# 系统临时目录、缓存（按目录名匹配，任意层级生效）",
		"Temp",
		"tmp",
		"cache",
		"Cache",
		"Prefetch",
		"Minidump",
		"PerfLogs",
		"LocalLow",
		"",
		"# Linux 伪文件系统 / 挂载点（仅源根目录顶层，避免误伤同名数据目录）",
		"proc/",
		"sys/",
		"dev/",
		"run/",
		"mnt/",
		"media/",
		"lost+found/",
		"",
		"# Windows 更新旧系统",
		"Windows.old",
		"",
		"# 缩略图、系统生成文件",
		"Thumbs.db",
		"desktop.ini",
		"~$*",
		"",
		"# 虚拟机镜像、快照",
		"*.vmdk",
		"*.vhd",
		"*.vhdx",
		"*.ova",
		"",
		"# Outlook 缓存",
		"*.ost",
		"",
		"# NAS 内部元数据、回收站快照",
		"@Recycle",
		"@Recently-Snapshot",
		"@eadir",
		"*.snapshots",
		"",
		"# 全部 . 点开头文件/目录",
		".*",
		"",
		"# 包依赖与构建产物",
		"node_modules",
		".git",
		"vendor",
		"venv",
		"__pycache__",
		"dist",
		"build",
		"out",
		"",
		"# IDE / 工具缓存",
		".pytest_cache",
		"",
		"# 日志、临时、编译产物",
		"*.log",
		"*.tmp",
		"*.swp",
		"*.swo",
		"*.bak",
		"*.cache",
		"",
		"# 各类程序崩溃转储",
		"*.dmp",
		"",
		"# 压缩软件临时文件",
		"*.part",
		"",
		"# rsync 锁文件",
		".rsync-lock",
		"",
		"# npm/yarn 本地缓存",
		".npm",
		".yarn-cache",
		"",
		"# python linter 缓存",
		".ruff_cache",
		".mypy_cache",
	}
}

// Default 返回默认设置
func Default() *Settings {
	return &Settings{
		CPURefreshSeconds:   5,
		DiskRefreshSeconds:  60,
		LogMaxSize:          100,
		LogMaxAge:           30,
		BackupCompressLevel: 6,
		BackupExcludeRules:  DefaultBackupExcludes(),
	}
}

// Service 设置存取服务（基于 TOML 文件持久化；排除规则独立存 exclude-list.txt）
type Service struct {
	path        string
	excludePath string
	mu          sync.RWMutex
	cfg         *Settings
}

// NewService 创建设置服务
func NewService(dataDir string) (*Service, error) {
	path := filepath.Join(dataDir, "settings.toml")
	s := &Service{
		path:        path,
		excludePath: filepath.Join(dataDir, "exclude-list.txt"),
		cfg:         Default(),
	}
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
	// 排除规则独立文件：不存在时优先从旧版 settings.toml 内联数组迁移，否则写入默认预设
	if err := s.loadOrCreateExcludes(); err != nil {
		return nil, err
	}
	return s, nil
}

// loadOrCreateExcludes 从 exclude-list.txt 加载排除规则；文件不存在时
// 迁移旧版 settings.toml 的 backup_exclude_rules 内联数组（或采用默认预设）并创建文件。
func (s *Service) loadOrCreateExcludes() error {
	if data, err := os.ReadFile(s.excludePath); err == nil {
		s.cfg.BackupExcludeRules = parseExcludeLines(string(data))
		s.cfg.ensureValid()
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rules := s.migrateLegacyExcludes()
	if rules == nil {
		rules = DefaultBackupExcludes()
	}
	s.cfg.BackupExcludeRules = rules
	return s.saveExcludeFile(rules)
}

// migrateLegacyExcludes v0.21.3 及以前：排除规则内联在 settings.toml 中。
// 读取成功返回规则数组（可能含 # 注释行）；无旧数据返回 nil。
func (s *Service) migrateLegacyExcludes() []string {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var legacy struct {
		Rules []string `toml:"backup_exclude_rules"`
	}
	if toml.Unmarshal(data, &legacy) != nil || len(legacy.Rules) == 0 {
		return nil
	}
	return legacy.Rules
}

// saveExcludeFile 排除规则写入 exclude-list.txt（每行一条，UTF-8）
func (s *Service) saveExcludeFile(rules []string) error {
	return os.WriteFile(s.excludePath, []byte(strings.Join(rules, "\n")+"\n"), 0o644)
}

// parseExcludeLines 按行解析排除规则：保留 # 注释行与规则行，去空行与行尾 \r
func parseExcludeLines(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
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

// Reset 重置为默认设置并持久化（v0.21.3；v0.21.4 起排除规则恢复默认预设文件）
func (s *Service) Reset() (*Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = Default()
	if err := s.saveExcludeFile(s.cfg.BackupExcludeRules); err != nil {
		return nil, err
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	cp := *s.cfg
	return &cp, nil
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
		case "backup_exclude_rules":
			rules, err := toStringSlice(v)
			if err != nil {
				return nil, errors.New("排除规则应为字符串数组")
			}
			if len(rules) > 200 {
				return nil, errors.New("排除规则最多 200 条")
			}
			cur.BackupExcludeRules = rules
		default:
			return nil, errors.New("未知设置项: " + k)
		}
	}
	cur.ensureValid()
	// 排除规则持久化到独立文件 exclude-list.txt（仅当本次更新包含该字段时重写）
	if _, touched := patch["backup_exclude_rules"]; touched {
		if err := s.saveExcludeFile(cur.BackupExcludeRules); err != nil {
			return nil, errors.New("写入 exclude-list.txt 失败: " + err.Error())
		}
	}
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
	// 排除规则：从未设置（nil，如旧配置迁移）时套用默认预设；显式清空（空数组）则尊重用户
	if c.BackupExcludeRules == nil {
		c.BackupExcludeRules = DefaultBackupExcludes()
	}
	if len(c.BackupExcludeRules) > 200 {
		c.BackupExcludeRules = c.BackupExcludeRules[:200]
	}
	cleaned := make([]string, 0, len(c.BackupExcludeRules))
	for _, r := range c.BackupExcludeRules {
		r = strings.TrimSpace(r)
		if r != "" {
			cleaned = append(cleaned, r)
		}
	}
	c.BackupExcludeRules = cleaned
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

// toStringSlice JSON 反序列化出的 []interface{} / TOML 的 []string 统一转 []string（逐条 trim、去空）
func toStringSlice(v interface{}) ([]string, error) {
	// 逐条 trim 后滤掉空串（泛型工具见 internal/util/slice.go）
	clean := func(strs []string) []string {
		return util.Filter(util.Map(strs, strings.TrimSpace), func(s string) bool { return s != "" })
	}
	switch t := v.(type) {
	case []interface{}:
		strs := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("排除规则应为字符串数组")
			}
			strs = append(strs, s)
		}
		return clean(strs), nil
	case []string:
		return clean(t), nil
	case nil:
		return []string{}, nil
	default:
		return nil, errors.New("排除规则应为字符串数组")
	}
}
