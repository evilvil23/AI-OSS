// Package config 配置管理模块。
//
// 说明：文档选用 viper，但当前离线环境不可用；这里基于 go-toml/v2 实现
// 等价能力：TOML 配置文件、环境变量覆盖、运行时热重载（通过 WatchConfig 轮询）。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Config 顶层配置，与 config.toml 一一对应
type Config struct {
	Server      ServerConfig      `toml:"server"`
	Storage     StorageConfig     `toml:"storage"`
	Tus         TusConfig         `toml:"tus"`
	Auth        AuthConfig        `toml:"auth"`
	AI          AIConfig          `toml:"ai"`
	IoT         IoTConfig         `toml:"iot"`
	Plugin      PluginConfig      `toml:"plugin"`
	Log         LogConfig         `toml:"log"`
	Degradation DegradationConfig `toml:"degradation"`
	Play        PlayConfig        `toml:"play"`
	Backup      BackupConfig      `toml:"backup"`
}

// ---------- Backup（v0.20 备份还原） ----------
type BackupConfig struct {
	Enabled         bool   `toml:"enabled"`           // 备份功能总开关
	DBPath          string `toml:"db_path"`           // 备份元数据库（空 = data/backup/backup.db）
	LogPath         string `toml:"log_path"`          // 独立 backup.log（空 = data/logs/backup.log）
	WatchInterval   int    `toml:"watch_interval"`    // 任务表重载间隔（秒，默认 30）
	USBPollInterval int    `toml:"usb_poll_interval"` // USB 设备轮询间隔（秒，默认 3）
}

// ---------- Server ----------
type ServerConfig struct {
	Port         int    `toml:"port"`
	Mode         string `toml:"mode"`
	ReadTimeout  int    `toml:"read_timeout"`
	WriteTimeout int    `toml:"write_timeout"`
}

// ---------- Storage ----------
type StorageConfig struct {
	Root          string       `toml:"root"`
	Disks         []string     `toml:"disks"` // 允许 NAS 访问的磁盘根目录（为空时自动发现盘符，再回退到 root）
	TrashPath     string       `toml:"trash_path"` // 全局回收站目录（为空时默认 root/trash）
	MetadataDB    string       `toml:"metadata_db"` // 元数据库 SQLite 文件位置（为空时默认 root/metadata.db）
	VersionKeep   int          `toml:"version_keep"`
	TrashDays     int          `toml:"trash_days"`
	WebDAVEnabled bool         `toml:"webdav_enabled"`
	WebDAVPrefix  string       `toml:"webdav_prefix"`
	Rclone        RcloneConfig `toml:"rclone"`
}

type RcloneConfig struct {
	Enabled  bool   `toml:"enabled"`
	Remote   string `toml:"remote"`
	Schedule string `toml:"schedule"`
}

// ---------- Tus ----------
type TusConfig struct {
	Enabled           bool   `toml:"enabled"`
	PathPrefix        string `toml:"path_prefix"`
	ChunkSize         int64  `toml:"chunk_size"`
	MaxSize           int64  `toml:"max_size"`
	StoreDir          string `toml:"store_dir"`
	ConcurrentUploads bool   `toml:"concurrent_uploads"`
	Dedup             bool   `toml:"dedup"`
}

// ---------- Auth ----------
type AuthConfig struct {
	JWTSecret    string `toml:"jwt_secret"`
	JWTExpire    int    `toml:"jwt_expire"`
	Argon2Time   uint32 `toml:"argon2_time"`
	Argon2Memory uint32 `toml:"argon2_memory"`
	Argon2Threads uint8 `toml:"argon2_threads"`
	Argon2KeyLen uint32 `toml:"argon2_key_len"`
	Argon2SaltLen uint32 `toml:"argon2_salt_len"`
	AdminUsername string `toml:"admin_username"` // 首次启动自动创建的管理员用户名
	AdminPassword string `toml:"admin_password"` // 首次启动自动创建的管理员密码
}

// ---------- AI ----------
type AIConfig struct {
	Enabled               bool      `toml:"enabled"` // AI 功能总开关（false 时服务启动跳过 AI 初始化，前端 AI 菜单给出禁用提示）
	OllamaHost            string    `toml:"ollama_host"`
	DefaultModel          string    `toml:"default_model"`
	EmbeddingModel        string    `toml:"embedding_model"`
	ConversationMaxTokens int       `toml:"conversation_max_tokens"`
	Temperature           float64   `toml:"temperature"`
	RAG                   RAGConfig `toml:"rag"`
	Ollama                OllamaConfig `toml:"ollama"`       // v0.21 Ollama 进程生命周期管理
	Tune                  TuneConfig   `toml:"tune"`         // v0.21 硬件自适应调优预设（零值 = 自动检测）
	Deploy                DeployConfig `toml:"deploy"`       // v0.21 部署模式（single / dual 主备双机）
	HomeAssistant         HAConfig     `toml:"homeassistant"` // v0.21 HomeAssistant 智能家居接入
}

// OllamaConfig Ollama 进程生命周期（v0.21 M1）
type OllamaConfig struct {
	Managed      bool   `toml:"managed"`       // 由本服务拉起 / 停止 Ollama（false = 用户自行管理）
	Binary       string `toml:"binary"`        // 可执行文件路径（空 = 自动探测 PATH 与常见安装位置）
	BindHost     string `toml:"bind_host"`     // 拉起时注入 OLLAMA_HOST（局域网调用设 0.0.0.0:11434）
	StartTimeout int    `toml:"start_timeout"` // 拉起后等待就绪的最长秒数
	AutoWarmup   bool   `toml:"auto_warmup"`   // 就绪后空对话预热，触发默认模型加载
}

// TuneConfig 硬件调优预设（v0.21 M2）；字段为零值时按硬件自适应规则取默认
type TuneConfig struct {
	Model       string `toml:"model"`         // 覆盖自动模型选择（config 优先，禁止写死）
	NumCtx      int    `toml:"num_ctx"`       // 上下文窗口
	KeepAlive   string `toml:"keep_alive"`    // 如 "5m"、"-1"（常驻显存 / 内存）
	NumParallel int    `toml:"num_parallel"`  // 并行推理数（拉起时注入 OLLAMA_NUM_PARALLEL）
}

// DeployConfig 部署模式（v0.21 M6）：三种形态互不依赖，切换无需改代码
// v0.23 mode 支持 auto：启动时按 remote_host 可达性自动选边（可达→auxiliary，否则→primary），
// 解析结果仅存在于运行态（持久化保留 auto 原值），运行中不因断连切换身份
type DeployConfig struct {
	Mode           string `toml:"mode"`            // auto（启动时自动判定）| single（默认）| dual
	Role           string `toml:"role"`            // dual 生效：primary | auxiliary
	RemoteHost     string `toml:"remote_host"`     // auxiliary：远端（主服务）Ollama 地址
	RemoteModel    string `toml:"remote_model"`    // auxiliary：远端使用的模型（空 = 沿用 default_model）
	RemoteAPI      string `toml:"remote_api"`      // auxiliary：主服务 smart-nas API 根地址（RAG 走主服务）
	RemoteUsername string `toml:"remote_username"` // auxiliary：主服务登录账号（获取 JWT 调 RAG）
	RemotePassword string `toml:"remote_password"`
	AutoSwitch     bool   `toml:"auto_switch"`    // 辅助机自动切换远端 / 降级本机
	CheckInterval  int    `toml:"check_interval"` // 远端可达性探测间隔（秒）
}

// HAConfig HomeAssistant 接入（v0.21 M7）
type HAConfig struct {
	Enabled bool   `toml:"enabled"`
	BaseURL string `toml:"base_url"` // 如 http://homeassistant.local:8123
	Token   string `toml:"token"`    // 长期访问令牌（长期访问令牌，个人资料 → 安全）
}

type RAGConfig struct {
	Enabled      bool         `toml:"enabled"`
	StoreType    string       `toml:"store_type"`
	ChunkSize    int          `toml:"chunk_size"`
	ChunkOverlap int          `toml:"chunk_overlap"`
	TopK         int          `toml:"top_k"`
	Qdrant       QdrantConfig `toml:"qdrant"`
}

type QdrantConfig struct {
	Host           string `toml:"host"`
	Port           int    `toml:"port"`
	APIKey         string `toml:"api_key"`
	CollectionName string `toml:"collection_name"`
}

// ---------- IoT ----------
type IoTConfig struct {
	Mihome MihomeConfig `toml:"mihome"`
	MQTT   MQTTConfig   `toml:"mqtt"`
}

type MihomeConfig struct {
	Enabled      bool   `toml:"enabled"`
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
	RedirectURI  string `toml:"redirect_uri"`
	APIBase      string `toml:"api_base"`
}

type MQTTConfig struct {
	Enabled  bool   `toml:"enabled"`
	Broker   string `toml:"broker"`
	ClientID string `toml:"client_id"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

// ---------- Plugin ----------
type PluginConfig struct {
	Enabled  bool   `toml:"enabled"`
	Dir      string `toml:"dir"`
	AutoLoad bool   `toml:"auto_load"`
	Timeout  int    `toml:"timeout"`
}

// ---------- Log ----------
type LogConfig struct {
	Level      string `toml:"level"`
	Path       string `toml:"path"`
	MaxSize    int    `toml:"max_size"`
	MaxBackups int    `toml:"max_backups"`
	MaxAge     int    `toml:"max_age"`
}

// ---------- Degradation ----------
type DegradationConfig struct {
	AutoDisableRAGOnHighCPU bool    `toml:"auto_disable_rag_on_high_cpu"`
	CPUThreshold            float64 `toml:"cpu_threshold"`
}

// ---------- Play（视频在线播放） ----------
type PlayConfig struct {
	Enabled              bool   `toml:"enabled"`
	MaxOnlineHeight      int    `toml:"max_online_height"`  // 最高在线播放分辨率高度（默认 1440 = 2K，超出提示下载）
	FFmpegPath           string `toml:"ffmpeg_path"`        // 留空自动从 PATH 查找
	FFprobePath          string `toml:"ffprobe_path"`
	RemuxConcurrency     int    `toml:"remux_concurrency"`  // 转封装（-c copy）最大并发
	TranscodeConcurrency int    `toml:"transcode_concurrency"` // 实时转码最大并发（大开销，默认 1）
	TranscodeThreads     int    `toml:"transcode_threads"`  // 单个转码任务使用的线程数（默认 2）
	TicketTTLMinutes     int    `toml:"ticket_ttl_minutes"` // 播放凭证无 Range 请求自动失效时长
	CacheDir             string `toml:"cache_dir"`          // 转封装产物缓存目录
	IPBind               bool   `toml:"ip_bind"`            // 播放凭证是否与请求 IP 绑定（防盗链）
}

// DefaultPlayConfig 视频播放默认值
func DefaultPlayConfig() PlayConfig {
	return PlayConfig{
		Enabled:              true,
		MaxOnlineHeight:      1440,
		FFmpegPath:           "ffmpeg",
		FFprobePath:          "ffprobe",
		RemuxConcurrency:     3,
		TranscodeConcurrency: 1,
		TranscodeThreads:     2,
		TicketTTLMinutes:     30,
		CacheDir:             "./data/cache/video",
		IPBind:               true,
	}
}

// DefaultConfig 返回带默认值的配置（确保字段均有效）
func DefaultConfig() *Config {
	c := &Config{}
	c.Server.Port = 8080
	c.Server.Mode = "release"
	c.Server.ReadTimeout = 60
	c.Server.WriteTimeout = 300

	c.Storage.Root = "./data/files"
	c.Storage.VersionKeep = 5
	c.Storage.TrashDays = 30
	c.Storage.WebDAVEnabled = true
	c.Storage.WebDAVPrefix = "/dav"

	c.Tus.Enabled = true
	c.Tus.PathPrefix = "/files/upload/"
	c.Tus.ChunkSize = 5 * 1024 * 1024
	c.Tus.MaxSize = 10 * 1024 * 1024 * 1024
	c.Tus.StoreDir = "./data/tus"
	c.Tus.ConcurrentUploads = true
	c.Tus.Dedup = true

	c.Auth.JWTSecret = "change-me-in-production"
	c.Auth.JWTExpire = 86400
	c.Auth.Argon2Time = 3
	c.Auth.Argon2Memory = 65536
	c.Auth.Argon2Threads = 4
	c.Auth.Argon2KeyLen = 32
	c.Auth.Argon2SaltLen = 16
	c.Auth.AdminUsername = "admin"
	c.Auth.AdminPassword = "admin123"

	c.AI.Enabled = false
	c.AI.OllamaHost = "http://localhost:11434"
	c.AI.DefaultModel = "qwen2:7b"
	c.AI.EmbeddingModel = "nomic-embed-text"
	c.AI.ConversationMaxTokens = 4096
	c.AI.Temperature = 0.7
	c.AI.RAG.Enabled = true
	c.AI.RAG.StoreType = "chromem"
	c.AI.RAG.ChunkSize = 512
	c.AI.RAG.ChunkOverlap = 50
	c.AI.RAG.TopK = 5
	c.AI.RAG.Qdrant.Host = "localhost"
	c.AI.RAG.Qdrant.Port = 6334
	c.AI.RAG.Qdrant.CollectionName = "file_chunks"

	c.AI.Ollama.Managed = true
	c.AI.Ollama.Binary = ""
	c.AI.Ollama.BindHost = "127.0.0.1:11434"
	c.AI.Ollama.StartTimeout = 60
	c.AI.Ollama.AutoWarmup = true

	c.AI.Tune.Model = ""
	c.AI.Tune.NumCtx = 0
	c.AI.Tune.KeepAlive = ""
	c.AI.Tune.NumParallel = 0

	c.AI.Deploy.Mode = "single"
	c.AI.Deploy.Role = "primary"
	c.AI.Deploy.CheckInterval = 30
	c.AI.Deploy.AutoSwitch = true

	c.AI.HomeAssistant.Enabled = false
	c.AI.HomeAssistant.BaseURL = "http://homeassistant.local:8123"
	c.AI.HomeAssistant.Token = ""

	c.IoT.MQTT.Enabled = true
	c.IoT.MQTT.Broker = "tcp://localhost:1883"
	c.IoT.MQTT.ClientID = "smart-nas"

	c.Plugin.Enabled = true
	c.Plugin.Dir = "./plugins"
	c.Plugin.AutoLoad = true
	c.Plugin.Timeout = 30

	c.Log.Level = "info"
	c.Log.Path = "./data/logs/smart-nas.log"
	c.Log.MaxSize = 100
	c.Log.MaxBackups = 7
	c.Log.MaxAge = 30

	c.Degradation.AutoDisableRAGOnHighCPU = true
	c.Degradation.CPUThreshold = 0.85

	c.Play = DefaultPlayConfig()

	c.Backup.Enabled = true
	c.Backup.DBPath = "./data/backup/backup.db"
	c.Backup.LogPath = "./data/logs/backup.log"
	c.Backup.WatchInterval = 30
	c.Backup.USBPollInterval = 3
	return c
}

// Manager 配置管理器：加载、读取、更新、热重载
type Manager struct {
	path     string
	cfg      *Config
	mu       sync.RWMutex
	stop     chan struct{}
	onUpdate func(c *Config)

	// restartRequired 是否存在「已保存但需重启服务才能生效」的变更（v0.26）。
	// 启动期装配的配置项（AI 开关 / 模型 / 部署模式等）运行时热更新不改变既有行为，
	// 置位后由前端展示待重启提示；服务重启即新进程，该标志自然重置
	restartRequired bool
}

// NewManager 加载配置文件；文件不存在时生成默认并写盘
func NewManager(configPath string) (*Manager, error) {
	m := &Manager{path: configPath, stop: make(chan struct{})}
	cfg := DefaultConfig()
	found := false
	if configPath != "" {
		if data, err := os.ReadFile(configPath); err == nil {
			if err := toml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("解析配置文件失败: %w", err)
			}
			found = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		} else if dir := filepath.Dir(configPath); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
	}
	applyEnvOverrides(cfg)
	m.cfg = cfg
	if !found && configPath != "" {
		if err := m.Save(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// applyEnvOverrides 环境变量覆盖：前缀 SMARTNAS_，双下划线表示嵌套层级
// 例：SMARTNAS_SERVER_PORT=9090、SMARTNAS_AI__RAG__ENABLED=false
func applyEnvOverrides(c *Config) {
	envs := os.Environ()
	for _, kv := range envs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, "SMARTNAS_") {
			continue
		}
		path := strings.ToLower(strings.TrimPrefix(k, "SMARTNAS_"))
		path = strings.ReplaceAll(path, "__", ".")
		_ = setByPath(c, path, v)
	}
}

// ---------- 配置项调度表 ----------
//
// v0.26 重构：原 setByPath 内 100+ 分支的巨型 switch 改为映射表（调度表）。
// 新增配置项只需在 configSetters 登记，setByPath 无需改动。
// 三类 helper 对应三种赋值形态：
//   - strSetter   字符串直赋
//   - parseSetter 数值 / 布尔解析（解析失败报"无效的配置值"）
//   - csvSetter   逗号分隔列表

func strSetter(f func(*Config, string)) func(*Config, string) error {
	return func(c *Config, v string) error { f(c, v); return nil }
}

func parseSetter[T any](f func(*Config, T)) func(*Config, string) error {
	return func(c *Config, v string) error {
		var t T
		if _, err := fmt.Sscan(v, &t); err != nil {
			return fmt.Errorf("无效的配置值 %q", v)
		}
		f(c, t)
		return nil
	}
}

func csvSetter(f func(*Config, []string)) func(*Config, string) error {
	return func(c *Config, v string) error { f(c, splitCSV(v)); return nil }
}

var configSetters = map[string]func(*Config, string) error{
	// ---------- Server ----------
	"server.port":          parseSetter(func(c *Config, v int) { c.Server.Port = v }),
	"server.mode":          strSetter(func(c *Config, v string) { c.Server.Mode = v }),
	"server.read_timeout":  parseSetter(func(c *Config, v int) { c.Server.ReadTimeout = v }),
	"server.write_timeout": parseSetter(func(c *Config, v int) { c.Server.WriteTimeout = v }),

	// ---------- Storage ----------
	"storage.root":           strSetter(func(c *Config, v string) { c.Storage.Root = v }),
	"storage.disks":          csvSetter(func(c *Config, v []string) { c.Storage.Disks = v }),
	"storage.trash_path":     strSetter(func(c *Config, v string) { c.Storage.TrashPath = v }),
	"storage.metadata_db":    strSetter(func(c *Config, v string) { c.Storage.MetadataDB = v }),
	"storage.version_keep":   parseSetter(func(c *Config, v int) { c.Storage.VersionKeep = v }),
	"storage.trash_days":     parseSetter(func(c *Config, v int) { c.Storage.TrashDays = v }),
	"storage.webdav_enabled": parseSetter(func(c *Config, v bool) { c.Storage.WebDAVEnabled = v }),
	"storage.webdav_prefix":  strSetter(func(c *Config, v string) { c.Storage.WebDAVPrefix = v }),

	"storage.rclone.enabled":  parseSetter(func(c *Config, v bool) { c.Storage.Rclone.Enabled = v }),
	"storage.rclone.remote":   strSetter(func(c *Config, v string) { c.Storage.Rclone.Remote = v }),
	"storage.rclone.schedule": strSetter(func(c *Config, v string) { c.Storage.Rclone.Schedule = v }),

	// ---------- Tus ----------
	"tus.enabled":            parseSetter(func(c *Config, v bool) { c.Tus.Enabled = v }),
	"tus.path_prefix":        strSetter(func(c *Config, v string) { c.Tus.PathPrefix = v }),
	"tus.chunk_size":         parseSetter(func(c *Config, v int64) { c.Tus.ChunkSize = v }),
	"tus.max_size":           parseSetter(func(c *Config, v int64) { c.Tus.MaxSize = v }),
	"tus.store_dir":          strSetter(func(c *Config, v string) { c.Tus.StoreDir = v }),
	"tus.concurrent_uploads": parseSetter(func(c *Config, v bool) { c.Tus.ConcurrentUploads = v }),
	"tus.dedup":              parseSetter(func(c *Config, v bool) { c.Tus.Dedup = v }),

	// ---------- Auth ----------
	"auth.jwt_secret":     strSetter(func(c *Config, v string) { c.Auth.JWTSecret = v }),
	"auth.jwt_expire":     parseSetter(func(c *Config, v int) { c.Auth.JWTExpire = v }),
	"auth.argon2_time":    parseSetter(func(c *Config, v uint32) { c.Auth.Argon2Time = v }),
	"auth.argon2_memory":  parseSetter(func(c *Config, v uint32) { c.Auth.Argon2Memory = v }),
	"auth.argon2_threads": parseSetter(func(c *Config, v uint8) { c.Auth.Argon2Threads = v }),
	"auth.argon2_key_len": parseSetter(func(c *Config, v uint32) { c.Auth.Argon2KeyLen = v }),
	"auth.argon2_salt_len": parseSetter(func(c *Config, v uint32) { c.Auth.Argon2SaltLen = v }),
	"auth.admin_username":  strSetter(func(c *Config, v string) { c.Auth.AdminUsername = v }),
	"auth.admin_password":  strSetter(func(c *Config, v string) { c.Auth.AdminPassword = v }),

	// ---------- AI ----------
	"ai.enabled":               parseSetter(func(c *Config, v bool) { c.AI.Enabled = v }),
	"ai.ollama_host":           strSetter(func(c *Config, v string) { c.AI.OllamaHost = v }),
	"ai.default_model":         strSetter(func(c *Config, v string) { c.AI.DefaultModel = v }),
	"ai.embedding_model":       strSetter(func(c *Config, v string) { c.AI.EmbeddingModel = v }),
	"ai.conversation_max_tokens": parseSetter(func(c *Config, v int) { c.AI.ConversationMaxTokens = v }),
	"ai.temperature":           parseSetter(func(c *Config, v float64) { c.AI.Temperature = v }),

	"ai.rag.enabled":       parseSetter(func(c *Config, v bool) { c.AI.RAG.Enabled = v }),
	"ai.rag.store_type":    strSetter(func(c *Config, v string) { c.AI.RAG.StoreType = v }),
	"ai.rag.chunk_size":    parseSetter(func(c *Config, v int) { c.AI.RAG.ChunkSize = v }),
	"ai.rag.chunk_overlap": parseSetter(func(c *Config, v int) { c.AI.RAG.ChunkOverlap = v }),
	"ai.rag.top_k":         parseSetter(func(c *Config, v int) { c.AI.RAG.TopK = v }),

	"ai.rag.qdrant.host":           strSetter(func(c *Config, v string) { c.AI.RAG.Qdrant.Host = v }),
	"ai.rag.qdrant.port":           parseSetter(func(c *Config, v int) { c.AI.RAG.Qdrant.Port = v }),
	"ai.rag.qdrant.api_key":        strSetter(func(c *Config, v string) { c.AI.RAG.Qdrant.APIKey = v }),
	"ai.rag.qdrant.collection_name": strSetter(func(c *Config, v string) { c.AI.RAG.Qdrant.CollectionName = v }),

	"ai.ollama.managed":       parseSetter(func(c *Config, v bool) { c.AI.Ollama.Managed = v }),
	"ai.ollama.binary":        strSetter(func(c *Config, v string) { c.AI.Ollama.Binary = v }),
	"ai.ollama.bind_host":     strSetter(func(c *Config, v string) { c.AI.Ollama.BindHost = v }),
	"ai.ollama.start_timeout": parseSetter(func(c *Config, v int) { c.AI.Ollama.StartTimeout = v }),
	"ai.ollama.auto_warmup":   parseSetter(func(c *Config, v bool) { c.AI.Ollama.AutoWarmup = v }),

	"ai.tune.model":        strSetter(func(c *Config, v string) { c.AI.Tune.Model = v }),
	"ai.tune.num_ctx":      parseSetter(func(c *Config, v int) { c.AI.Tune.NumCtx = v }),
	"ai.tune.keep_alive":   strSetter(func(c *Config, v string) { c.AI.Tune.KeepAlive = v }),
	"ai.tune.num_parallel": parseSetter(func(c *Config, v int) { c.AI.Tune.NumParallel = v }),

	"ai.deploy.mode":            strSetter(func(c *Config, v string) { c.AI.Deploy.Mode = v }),
	"ai.deploy.role":            strSetter(func(c *Config, v string) { c.AI.Deploy.Role = v }),
	"ai.deploy.remote_host":     strSetter(func(c *Config, v string) { c.AI.Deploy.RemoteHost = v }),
	"ai.deploy.remote_model":    strSetter(func(c *Config, v string) { c.AI.Deploy.RemoteModel = v }),
	"ai.deploy.remote_api":      strSetter(func(c *Config, v string) { c.AI.Deploy.RemoteAPI = v }),
	"ai.deploy.remote_username": strSetter(func(c *Config, v string) { c.AI.Deploy.RemoteUsername = v }),
	"ai.deploy.remote_password": strSetter(func(c *Config, v string) { c.AI.Deploy.RemotePassword = v }),
	"ai.deploy.auto_switch":     parseSetter(func(c *Config, v bool) { c.AI.Deploy.AutoSwitch = v }),
	"ai.deploy.check_interval":  parseSetter(func(c *Config, v int) { c.AI.Deploy.CheckInterval = v }),

	"ai.homeassistant.enabled":  parseSetter(func(c *Config, v bool) { c.AI.HomeAssistant.Enabled = v }),
	"ai.homeassistant.base_url": strSetter(func(c *Config, v string) { c.AI.HomeAssistant.BaseURL = v }),
	"ai.homeassistant.token":    strSetter(func(c *Config, v string) { c.AI.HomeAssistant.Token = v }),

	// ---------- IoT ----------
	"iot.mihome.enabled":       parseSetter(func(c *Config, v bool) { c.IoT.Mihome.Enabled = v }),
	"iot.mihome.client_id":     strSetter(func(c *Config, v string) { c.IoT.Mihome.ClientID = v }),
	"iot.mihome.client_secret": strSetter(func(c *Config, v string) { c.IoT.Mihome.ClientSecret = v }),
	"iot.mihome.redirect_uri":  strSetter(func(c *Config, v string) { c.IoT.Mihome.RedirectURI = v }),
	"iot.mihome.api_base":      strSetter(func(c *Config, v string) { c.IoT.Mihome.APIBase = v }),

	"iot.mqtt.enabled":   parseSetter(func(c *Config, v bool) { c.IoT.MQTT.Enabled = v }),
	"iot.mqtt.broker":    strSetter(func(c *Config, v string) { c.IoT.MQTT.Broker = v }),
	"iot.mqtt.client_id": strSetter(func(c *Config, v string) { c.IoT.MQTT.ClientID = v }),
	"iot.mqtt.username":  strSetter(func(c *Config, v string) { c.IoT.MQTT.Username = v }),
	"iot.mqtt.password":  strSetter(func(c *Config, v string) { c.IoT.MQTT.Password = v }),

	// ---------- Plugin ----------
	"plugin.enabled":   parseSetter(func(c *Config, v bool) { c.Plugin.Enabled = v }),
	"plugin.dir":       strSetter(func(c *Config, v string) { c.Plugin.Dir = v }),
	"plugin.auto_load": parseSetter(func(c *Config, v bool) { c.Plugin.AutoLoad = v }),
	"plugin.timeout":   parseSetter(func(c *Config, v int) { c.Plugin.Timeout = v }),

	// ---------- Log ----------
	"log.level":       strSetter(func(c *Config, v string) { c.Log.Level = v }),
	"log.path":        strSetter(func(c *Config, v string) { c.Log.Path = v }),
	"log.max_size":    parseSetter(func(c *Config, v int) { c.Log.MaxSize = v }),
	"log.max_backups": parseSetter(func(c *Config, v int) { c.Log.MaxBackups = v }),
	"log.max_age":     parseSetter(func(c *Config, v int) { c.Log.MaxAge = v }),

	// ---------- Degradation ----------
	"degradation.auto_disable_rag_on_high_cpu": parseSetter(func(c *Config, v bool) { c.Degradation.AutoDisableRAGOnHighCPU = v }),
	"degradation.cpu_threshold":                parseSetter(func(c *Config, v float64) { c.Degradation.CPUThreshold = v }),

	// ---------- Play ----------
	"play.enabled":               parseSetter(func(c *Config, v bool) { c.Play.Enabled = v }),
	"play.max_online_height":     parseSetter(func(c *Config, v int) { c.Play.MaxOnlineHeight = v }),
	"play.ffmpeg_path":           strSetter(func(c *Config, v string) { c.Play.FFmpegPath = v }),
	"play.ffprobe_path":          strSetter(func(c *Config, v string) { c.Play.FFprobePath = v }),
	"play.remux_concurrency":     parseSetter(func(c *Config, v int) { c.Play.RemuxConcurrency = v }),
	"play.transcode_concurrency": parseSetter(func(c *Config, v int) { c.Play.TranscodeConcurrency = v }),
	"play.transcode_threads":     parseSetter(func(c *Config, v int) { c.Play.TranscodeThreads = v }),
	"play.ticket_ttl_minutes":    parseSetter(func(c *Config, v int) { c.Play.TicketTTLMinutes = v }),
	"play.cache_dir":             strSetter(func(c *Config, v string) { c.Play.CacheDir = v }),
	"play.ip_bind":               parseSetter(func(c *Config, v bool) { c.Play.IPBind = v }),

	// ---------- Backup ----------
	"backup.enabled":           parseSetter(func(c *Config, v bool) { c.Backup.Enabled = v }),
	"backup.db_path":           strSetter(func(c *Config, v string) { c.Backup.DBPath = v }),
	"backup.log_path":          strSetter(func(c *Config, v string) { c.Backup.LogPath = v }),
	"backup.watch_interval":    parseSetter(func(c *Config, v int) { c.Backup.WatchInterval = v }),
	"backup.usb_poll_interval": parseSetter(func(c *Config, v int) { c.Backup.USBPollInterval = v }),
}

// setByPath 按点分路径设置配置值：查 configSetters 调度表执行，
// 未登记的路径视为未知配置项；值解析失败由对应 setter 返回错误
func setByPath(c *Config, path, value string) error {
	if set, ok := configSetters[path]; ok {
		return set(c, value)
	}
	return fmt.Errorf("未知配置项: %s", path)
}

// GetConfig 返回当前配置（返回副本，避免并发修改）
func (m *Manager) GetConfig() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := *m.cfg
	return &cp
}

// UpdateConfig 更新配置：p 为 map[string]interfac{}，键为点分路径或嵌套 map
func (m *Manager) UpdateConfig(p map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := *m.cfg // 变更前快照（Config 各字段均为值类型，浅拷贝足够）
	if err := mergeMapIntoConfig(m.cfg, p); err != nil {
		return err
	}
	if needsRestart(&before, m.cfg) {
		m.restartRequired = true
	}
	if m.onUpdate != nil {
		m.onUpdate(m.cfg)
	}
	// 已持写锁，直接落盘；不可调用 Save()（其内部 RLock 与本函数写锁互斥，会死锁）
	return m.saveLocked()
}

// RestartRequired 是否存在已保存但需重启服务才生效的设置变更
func (m *Manager) RestartRequired() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.restartRequired
}

// needsRestart 判断配置变更是否涉及启动期装配项。
// 这些项在启动时读取并装配（AI 开关 / Ollama 地址 / 向量模型 / 部署模式与远端地址），
// 运行中热更新只改内存值，不改变已装配行为，须重启服务才真正生效
func needsRestart(before, after *Config) bool {
	return before.AI.Enabled != after.AI.Enabled ||
		before.AI.OllamaHost != after.AI.OllamaHost ||
		before.AI.EmbeddingModel != after.AI.EmbeddingModel ||
		before.AI.Deploy.Mode != after.AI.Deploy.Mode ||
		before.AI.Deploy.Role != after.AI.Deploy.Role ||
		before.AI.Deploy.RemoteHost != after.AI.Deploy.RemoteHost ||
		before.AI.Deploy.RemoteAPI != after.AI.Deploy.RemoteAPI
}

// saveLocked 落盘当前配置（调用方须已持有 m.mu 写锁）；
// 写回时保留 config.toml 中原有注释（见 writePreservingComments）
func (m *Manager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	data, err := toml.Marshal(m.cfg)
	if err != nil {
		return err
	}
	return writePreservingComments(m.path, data)
}

// mergeMapIntoConfig 将嵌套 map 合并进配置
func mergeMapIntoConfig(c *Config, patch map[string]interface{}) error {
	for k, v := range patch {
		var err error
		switch k {
		case "server":
			err = applySection(&c.Server, v)
		case "storage":
			err = applySection(&c.Storage, v)
		case "tus":
			err = applySection(&c.Tus, v)
		case "auth":
			err = applySection(&c.Auth, v)
		case "ai":
			err = applySection(&c.AI, v)
		case "iot":
			err = applySection(&c.IoT, v)
		case "plugin":
			err = applySection(&c.Plugin, v)
		case "log":
			err = applySection(&c.Log, v)
		case "degradation":
			err = applySection(&c.Degradation, v)
		default:
			// 支持点分路径作为键
			if sub, ok := toMap(v); ok {
				for sk, sv := range sub {
					err = setByPath(c, k+"."+sk, fmt.Sprintf("%v", sv))
					if err != nil {
						return err
					}
				}
			} else {
				err = setByPath(c, k, fmt.Sprintf("%v", v))
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// applySection 将 section 的字符串编码为 TOML 后解出到目标结构（保持简单、类型安全）
func applySection(dst any, v any) error {
	sub, ok := toMap(v)
	if !ok {
		return errors.New("配置段必须是对象")
	}
	// 将子段中基础字段直接写入
	b, err := toml.Marshal(sub)
	if err != nil {
		return err
	}
	return toml.Unmarshal(b, dst)
}

func toMap(v any) (map[string]interface{}, bool) {
	if t, ok := v.(map[string]interface{}); ok {
		return t, true
	}
	return nil, false
}

// OnChange 注册热重载回调
func (m *Manager) OnChange(f func(c *Config)) {
	m.mu.Lock()
	m.onUpdate = f
	m.mu.Unlock()
}

// WatchConfig 启动配置热重载（轮询文件 mtime，1 秒间隔）
func (m *Manager) WatchConfig() {
	if m.path == "" {
		return
	}
	var lastMod time.Time
	if info, err := os.Stat(m.path); err == nil {
		lastMod = info.ModTime()
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				info, err := os.Stat(m.path)
				if err != nil || info.ModTime().Equal(lastMod) {
					continue
				}
				lastMod = info.ModTime()
				data, err := os.ReadFile(m.path)
				if err != nil {
					continue
				}
				newCfg := DefaultConfig()
				if err := toml.Unmarshal(data, newCfg); err != nil {
					continue
				}
				m.mu.Lock()
				before := *m.cfg
				m.cfg = newCfg
				// 外部直接编辑文件时，涉及启动期装配项的改动同样需重启才生效
				if needsRestart(&before, newCfg) {
					m.restartRequired = true
				}
				cb := m.onUpdate
				m.mu.Unlock()
				if cb != nil {
					cb(newCfg)
				}
			}
		}
	}()
}

// Stop 停止热重载监听
func (m *Manager) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

// Save 将当前配置写回文件（保留原文件注释）
func (m *Manager) Save() error {
	if m.path == "" {
		return nil
	}
	m.mu.RLock()
	data, err := toml.Marshal(m.cfg)
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	return writePreservingComments(m.path, data)
}

// ---------- 注释保留写回 ----------
//
// 背景：toml.Marshal 只输出键值，不保留注释与排版。设置页保存 / 环境变量热更新
// 触发落盘时若直接覆盖，config.toml 中手写的中文注释会被全部抹掉。
//
// 策略（文本级键值替换）：以 Marshal 结果作为「权威键值」，逐行扫描原文件，
// 仅把对应键的值替换为新值；注释行、空行、缩进、行内注释与未知键一律原样保留；
// 原文件中缺失的新键（代码新增配置项）按所属分段追加。

// writePreservingComments 将完整 TOML 文本写回 path，尽量保留原文件注释。
// 原文件不存在（首次生成）或合并失败时回退为直接覆盖写，保证落盘不失败。
func writePreservingComments(path string, marshaled []byte) error {
	out := marshaled
	if orig, err := os.ReadFile(path); err == nil {
		out = []byte(mergeComments(string(orig), string(marshaled)))
	}
	return os.WriteFile(path, out, 0o644)
}

// mergeComments 把 marshaled 的键值合并进 orig 的文本结构，保留 orig 的注释与排版。
func mergeComments(orig, marshaled string) string {
	type kv struct{ path, val string }

	// 1. 解析 Marshal 结果：按定义顺序收集「完整路径 → 值字面量」
	var fresh []kv
	index := map[string]string{}
	section := ""
	for _, line := range strings.Split(marshaled, "\n") {
		if sec, ok := parseTOMLSection(line); ok {
			section = sec
			continue
		}
		if k, v, _, ok := splitTOMLKV(line); ok {
			p := joinKey(section, k)
			fresh = append(fresh, kv{p, v})
			index[p] = v
		}
	}

	// 2. 逐行替换原文件中的值；同时记录各分段最后一个键值行的位置（供新增键定位）
	hadTrailingNewline := strings.HasSuffix(orig, "\n")
	lines := strings.Split(strings.TrimSuffix(orig, "\n"), "\n")
	section = ""
	replaced := map[string]bool{}
	lastKeyLine := map[string]int{}
	for i, line := range lines {
		if sec, ok := parseTOMLSection(line); ok {
			section = sec
			continue
		}
		k, _, comment, ok := splitTOMLKV(line)
		if !ok {
			continue
		}
		lastKeyLine[section] = i
		p := joinKey(section, k)
		v, exists := index[p]
		if !exists {
			continue // 未知键（配置结构里没有）：原样保留，不删除用户内容
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		newLine := indent + k + " = " + v
		if comment != "" {
			newLine += "  " + comment // 行内注释原样保留
		}
		lines[i] = newLine
		replaced[p] = true
	}

	// 3. 追加原文件缺失的新键：按所属分段归组，整段插入到该分段末尾
	newBySection := map[string][]string{}
	var sectionOrder []string
	for _, item := range fresh {
		if replaced[item.path] {
			continue
		}
		sec, key := splitKeyPath(item.path)
		if _, seen := newBySection[sec]; !seen {
			sectionOrder = append(sectionOrder, sec)
		}
		newBySection[sec] = append(newBySection[sec], key+" = "+item.val)
	}
	insertAt := map[int][]string{} // 行索引 → 待插入行
	var tail []string              // 原文件无该分段时追加到文件尾
	for _, sec := range sectionOrder {
		rows := newBySection[sec]
		if idx, ok := lastKeyLine[sec]; ok && sec != "" {
			insertAt[idx+1] = append(insertAt[idx+1], rows...)
			continue
		}
		if sec != "" {
			tail = append(tail, "", "["+sec+"]")
		}
		tail = append(tail, rows...)
	}

	// 4. 重组文本
	out := make([]string, 0, len(lines)+len(tail))
	for i := 0; i <= len(lines); i++ {
		if extra, ok := insertAt[i]; ok {
			out = append(out, extra...)
		}
		if i < len(lines) {
			out = append(out, lines[i])
		}
	}
	out = append(out, tail...)
	result := strings.Join(out, "\n")
	if hadTrailingNewline {
		result += "\n"
	}
	return result
}

// parseTOMLSection 解析分段表头行（如 "[ai.rag]"、"[ai] # 注释"），返回分段名
func parseTOMLSection(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "[") {
		return "", false
	}
	end := strings.Index(s, "]")
	if end <= 1 {
		return "", false
	}
	return strings.TrimSpace(s[1:end]), true
}

// splitTOMLKV 解析键值行，返回键、值、行内注释（含 "#" 起的部分）。
// 忽略空行、注释行与分段表头；值内的引号与转义不会误判。
func splitTOMLKV(line string) (key, val, comment string, ok bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "[") {
		return "", "", "", false
	}
	eq := indexOutsideQuotes(s, '=')
	if eq <= 0 {
		return "", "", "", false
	}
	key = strings.TrimSpace(s[:eq])
	rest := s[eq+1:]
	if hash := indexOutsideQuotes(rest, '#'); hash >= 0 {
		comment = strings.TrimSpace(rest[hash:])
		rest = rest[:hash]
	}
	val = strings.TrimSpace(rest)
	if key == "" || val == "" {
		return "", "", "", false
	}
	return key, val, comment, true
}

// indexOutsideQuotes 返回 target 在 s 中首个不在引号内的下标（未找到返回 -1）；
// 双引号内的反斜杠转义会被正确跳过
func indexOutsideQuotes(s string, target byte) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if quote == '"' && ch == '\\' {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == target {
			return i
		}
	}
	return -1
}

// joinKey 拼接完整配置路径
func joinKey(section, key string) string {
	if section == "" {
		return key
	}
	return section + "." + key
}

// splitKeyPath 从完整路径还原分段名与键名
func splitKeyPath(p string) (section, key string) {
	if i := strings.LastIndex(p, "."); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}

// ---------- 小工具 ----------

// splitCSV 将逗号分隔的字符串拆分为去空格后的切片
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}