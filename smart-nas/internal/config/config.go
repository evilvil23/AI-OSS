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
	OllamaHost            string    `toml:"ollama_host"`
	DefaultModel          string    `toml:"default_model"`
	EmbeddingModel        string    `toml:"embedding_model"`
	ConversationMaxTokens int       `toml:"conversation_max_tokens"`
	Temperature           float64   `toml:"temperature"`
	RAG                   RAGConfig `toml:"rag"`
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

func knownFields() []string {
	return []string{
		"server.port", "server.mode", "server.read_timeout", "server.write_timeout",
		"storage.root", "storage.disks", "storage.trash_path", "storage.metadata_db", "storage.version_keep", "storage.trash_days",
		"storage.webdav_enabled", "storage.webdav_prefix",
		"storage.rclone.enabled", "storage.rclone.remote", "storage.rclone.schedule",
		"tus.enabled", "tus.path_prefix", "tus.chunk_size", "tus.max_size",
		"tus.store_dir", "tus.concurrent_uploads", "tus.dedup",
		"auth.jwt_secret", "auth.jwt_expire", "auth.argon2_time", "auth.argon2_memory",
		"auth.argon2_threads", "auth.argon2_key_len", "auth.argon2_salt_len",
		"auth.admin_username", "auth.admin_password",
		"ai.ollama_host", "ai.default_model", "ai.embedding_model",
		"ai.conversation_max_tokens", "ai.temperature",
		"ai.rag.enabled", "ai.rag.store_type", "ai.rag.chunk_size",
		"ai.rag.chunk_overlap", "ai.rag.top_k",
		"ai.rag.qdrant.host", "ai.rag.qdrant.port", "ai.rag.qdrant.api_key",
		"ai.rag.qdrant.collection_name",
		"iot.mihome.enabled", "iot.mihome.client_id", "iot.mihome.client_secret",
		"iot.mihome.redirect_uri", "iot.mihome.api_base",
		"iot.mqtt.enabled", "iot.mqtt.broker", "iot.mqtt.client_id",
		"iot.mqtt.username", "iot.mqtt.password",
		"plugin.enabled", "plugin.dir", "plugin.auto_load", "plugin.timeout",
		"log.level", "log.path", "log.max_size", "log.max_backups", "log.max_age",
		"degradation.auto_disable_rag_on_high_cpu", "degradation.cpu_threshold",
		"play.enabled", "play.max_online_height", "play.ffmpeg_path", "play.ffprobe_path",
		"play.remux_concurrency", "play.transcode_concurrency", "play.transcode_threads",
		"play.ticket_ttl_minutes", "play.cache_dir", "play.ip_bind",
		"backup.enabled", "backup.db_path", "backup.log_path",
		"backup.watch_interval", "backup.usb_poll_interval",
	}
}

// setByPath 按点分路径设置配置值（支持基础类型）
func setByPath(c *Config, path, value string) error {
	if !containsStr(knownFields(), path) {
		return fmt.Errorf("未知配置项: %s", path)
	}
	var ok bool
	switch path {
	case "server.port":
		ok = parseSet(&c.Server.Port, value)
	case "server.mode":
		c.Server.Mode = value
		ok = true
	case "server.read_timeout":
		ok = parseSet(&c.Server.ReadTimeout, value)
	case "server.write_timeout":
		ok = parseSet(&c.Server.WriteTimeout, value)
	case "storage.root":
		c.Storage.Root = value
		ok = true
	case "storage.disks":
		c.Storage.Disks = splitCSV(value)
		ok = true
	case "storage.trash_path":
		c.Storage.TrashPath = value
	case "storage.metadata_db":
		c.Storage.MetadataDB = value
		ok = true
	case "storage.version_keep":
		ok = parseSet(&c.Storage.VersionKeep, value)
	case "storage.trash_days":
		ok = parseSet(&c.Storage.TrashDays, value)
	case "storage.webdav_enabled":
		ok = parseSet(&c.Storage.WebDAVEnabled, value)
	case "storage.webdav_prefix":
		c.Storage.WebDAVPrefix = value
		ok = true
	case "storage.rclone.enabled":
		ok = parseSet(&c.Storage.Rclone.Enabled, value)
	case "storage.rclone.remote":
		c.Storage.Rclone.Remote = value
		ok = true
	case "storage.rclone.schedule":
		c.Storage.Rclone.Schedule = value
		ok = true
	case "tus.enabled":
		ok = parseSet(&c.Tus.Enabled, value)
	case "tus.path_prefix":
		c.Tus.PathPrefix = value
		ok = true
	case "tus.chunk_size":
		ok = parseSet(&c.Tus.ChunkSize, value)
	case "tus.max_size":
		ok = parseSet(&c.Tus.MaxSize, value)
	case "tus.store_dir":
		c.Tus.StoreDir = value
		ok = true
	case "tus.concurrent_uploads":
		ok = parseSet(&c.Tus.ConcurrentUploads, value)
	case "tus.dedup":
		ok = parseSet(&c.Tus.Dedup, value)
	case "auth.jwt_secret":
		c.Auth.JWTSecret = value
		ok = true
	case "auth.jwt_expire":
		ok = parseSet(&c.Auth.JWTExpire, value)
	case "auth.argon2_time":
		ok = parseSet(&c.Auth.Argon2Time, value)
	case "auth.argon2_memory":
		ok = parseSet(&c.Auth.Argon2Memory, value)
	case "auth.argon2_threads":
		ok = parseSet(&c.Auth.Argon2Threads, value)
	case "auth.argon2_key_len":
		ok = parseSet(&c.Auth.Argon2KeyLen, value)
	case "auth.argon2_salt_len":
		ok = parseSet(&c.Auth.Argon2SaltLen, value)
	case "auth.admin_username":
		c.Auth.AdminUsername = value
		ok = true
	case "auth.admin_password":
		c.Auth.AdminPassword = value
		ok = true
	case "ai.ollama_host":
		c.AI.OllamaHost = value
		ok = true
	case "ai.default_model":
		c.AI.DefaultModel = value
		ok = true
	case "ai.embedding_model":
		c.AI.EmbeddingModel = value
		ok = true
	case "ai.conversation_max_tokens":
		ok = parseSet(&c.AI.ConversationMaxTokens, value)
	case "ai.temperature":
		ok = parseSet(&c.AI.Temperature, value)
	case "ai.rag.enabled":
		ok = parseSet(&c.AI.RAG.Enabled, value)
	case "ai.rag.store_type":
		c.AI.RAG.StoreType = value
		ok = true
	case "ai.rag.chunk_size":
		ok = parseSet(&c.AI.RAG.ChunkSize, value)
	case "ai.rag.chunk_overlap":
		ok = parseSet(&c.AI.RAG.ChunkOverlap, value)
	case "ai.rag.top_k":
		ok = parseSet(&c.AI.RAG.TopK, value)
	case "ai.rag.qdrant.host":
		c.AI.RAG.Qdrant.Host = value
		ok = true
	case "ai.rag.qdrant.port":
		ok = parseSet(&c.AI.RAG.Qdrant.Port, value)
	case "ai.rag.qdrant.api_key":
		c.AI.RAG.Qdrant.APIKey = value
		ok = true
	case "ai.rag.qdrant.collection_name":
		c.AI.RAG.Qdrant.CollectionName = value
		ok = true
	case "iot.mihome.enabled":
		ok = parseSet(&c.IoT.Mihome.Enabled, value)
	case "iot.mihome.client_id":
		c.IoT.Mihome.ClientID = value
		ok = true
	case "iot.mihome.client_secret":
		c.IoT.Mihome.ClientSecret = value
		ok = true
	case "iot.mihome.redirect_uri":
		c.IoT.Mihome.RedirectURI = value
		ok = true
	case "iot.mihome.api_base":
		c.IoT.Mihome.APIBase = value
		ok = true
	case "iot.mqtt.enabled":
		ok = parseSet(&c.IoT.MQTT.Enabled, value)
	case "iot.mqtt.broker":
		c.IoT.MQTT.Broker = value
		ok = true
	case "iot.mqtt.client_id":
		c.IoT.MQTT.ClientID = value
		ok = true
	case "iot.mqtt.username":
		c.IoT.MQTT.Username = value
		ok = true
	case "iot.mqtt.password":
		c.IoT.MQTT.Password = value
		ok = true
	case "plugin.enabled":
		ok = parseSet(&c.Plugin.Enabled, value)
	case "plugin.dir":
		c.Plugin.Dir = value
		ok = true
	case "plugin.auto_load":
		ok = parseSet(&c.Plugin.AutoLoad, value)
	case "plugin.timeout":
		ok = parseSet(&c.Plugin.Timeout, value)
	case "log.level":
		c.Log.Level = value
		ok = true
	case "log.path":
		c.Log.Path = value
		ok = true
	case "log.max_size":
		ok = parseSet(&c.Log.MaxSize, value)
	case "log.max_backups":
		ok = parseSet(&c.Log.MaxBackups, value)
	case "log.max_age":
		ok = parseSet(&c.Log.MaxAge, value)
	case "degradation.auto_disable_rag_on_high_cpu":
		ok = parseSet(&c.Degradation.AutoDisableRAGOnHighCPU, value)
	case "degradation.cpu_threshold":
		ok = parseSet(&c.Degradation.CPUThreshold, value)
	case "play.enabled":
		ok = parseSet(&c.Play.Enabled, value)
	case "play.max_online_height":
		ok = parseSet(&c.Play.MaxOnlineHeight, value)
	case "play.ffmpeg_path":
		c.Play.FFmpegPath = value
		ok = true
	case "play.ffprobe_path":
		c.Play.FFprobePath = value
		ok = true
	case "play.remux_concurrency":
		ok = parseSet(&c.Play.RemuxConcurrency, value)
	case "play.transcode_concurrency":
		ok = parseSet(&c.Play.TranscodeConcurrency, value)
	case "play.transcode_threads":
		ok = parseSet(&c.Play.TranscodeThreads, value)
	case "play.ticket_ttl_minutes":
		ok = parseSet(&c.Play.TicketTTLMinutes, value)
	case "play.cache_dir":
		c.Play.CacheDir = value
		ok = true
	case "play.ip_bind":
		ok = parseSet(&c.Play.IPBind, value)
	case "backup.enabled":
		ok = parseSet(&c.Backup.Enabled, value)
	case "backup.db_path":
		c.Backup.DBPath = value
		ok = true
	case "backup.log_path":
		c.Backup.LogPath = value
		ok = true
	case "backup.watch_interval":
		ok = parseSet(&c.Backup.WatchInterval, value)
	case "backup.usb_poll_interval":
		ok = parseSet(&c.Backup.USBPollInterval, value)
	}
	if !ok {
		return fmt.Errorf("无效的配置值 %s=%s", path, value)
	}
	return nil
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
	if err := mergeMapIntoConfig(m.cfg, p); err != nil {
		return err
	}
	if m.onUpdate != nil {
		m.onUpdate(m.cfg)
	}
	return m.Save()
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
				m.cfg = newCfg
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

// Save 将当前配置写回文件
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
	return os.WriteFile(m.path, data, 0o644)
}

// ---------- 小工具 ----------
func parseSet[T any](dst *T, s string) bool {
	v := new(T)
	if _, err := fmt.Sscan(s, v); err != nil {
		return false
	}
	*dst = *v
	return true
}

func containsStr(list []string, s string) bool {
	for _, it := range list {
		if it == s {
			return true
		}
	}
	return false
}

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