---
type: Note
okf_version: "0.1"
created: 2026-08-20|00020T17:47
updated: 2026-08-20|00020T18:52
---


> **技术栈**：Ollama（本地大模型推理）+ Go（后端服务）+ Web 前端 + 米家 IoT 开放平台  
> **目标**：在家庭 NAS 上实现离线 AI 推理、文件语义检索、设备自动化控制，成为家庭数据与智能设备的统一控制中枢。

---

## 1. 系统总体设计

### 1.1 设计目标

| 维度     | 描述 |
| ------ | ---- |
| 核心定位   | 家庭数据中枢 + AI 智能管家 + IoT 控制中心 |
| 后端语言   | Go（Gin 框架） |
| 通信协议   | HTTP/REST + WebSocket + **tus 可恢复上传** + WebDAV |
| AI 能力  | 本地 Ollama 大模型，离线对话、文件语义检索、设备自动化 |
| IoT 接入 | 米家开放平台、Home Assistant、MQTT 通用协议 |
| 插件系统   | 三级插件架构（go-plugin 进程外 + Hook 钩子 + Goja 脚本） |
| 前端     | 响应式 Web UI（移动端/PC 自适应）+ 可选桌面端 |
| 数据存储   | SQLite（元数据）+ 文件系统（blob）+ 向量库（RAG） |
| 部署方式   | Docker Compose 一键部署，支持 x86/ARM；或单二进制 + systemd |

#### 主推：Docker Compose（容器部署）
- 使用 Compose v2，利用 `profiles` 按需启动。
- 提供 `.env` 文件配置端口、时区、数据目录等。
- 所有服务定义健康检查、资源限制、日志轮转。
- 提供多架构镜像，支持 x86/ARM。
- 文档中给出备份、恢复、升级步骤。

#### 备选：单二进制 + systemd（轻量模式）
- 通过 `make build` 生成带前端的二进制。
- 提供 `install.sh` 和 `uninstall.sh` 脚本，自动创建用户、配置 systemd、下载依赖。
- 内置可选组件：chromem-go 替代 Qdrant、嵌入式 MQTT broker 替代 Mosquitto，减少外部依赖。
- 适合资源受限或偏好传统进程管理的用户。

### 1.2 技术选型

| 层次       | 技术                                                    | 选型理由 |
| -------- | ----------------------------------------------------- | -------- |
| Web 框架   | Gin                                                   | 轻量、高性能、路由简洁，Go 生态最成熟 |
| 实时通信     | coder/websocket                                       | 文件传输进度、AI 对话流式输出、设备状态推送 |
| ORM      | GORM                                                  | 支持 SQLite/PostgreSQL 切换 |
| 文件传输     | **tus 协议** (tusd + tus-js-client)                     | 开源可恢复上传协议，原生支持断点续传、分块、并发上传 |
| 文件访问     | HTTP REST + **WebDAV** + Basic Auth 与 TLS              | REST 供 Web 端，WebDAV 供系统级挂载，强制 TLS |
| 备份同步     | rclone                                                | 支持多种后端 |
| **插件系统** | **hashicorp/go-plugin + Hook 钩子 + Goja 脚本**           | 三级插件架构，支持文件处理、AI 工具、IoT 平台、通知渠道等扩展 |
| AI 推理    | Ollama (REST API)                                     | 本地运行 Llama3/Qwen2 等模型，离线、免费、隐私安全 |
| 向量检索     | chromem-go（纯 Go 向量库）/Qdrant                           | 小规模用 chromem-go，大规模换 Qdrant |
| IoT 协议   | MQTT (paho.mqtt.golang)/Mosquitto broker + 米家 OpenAPI | 通用 MQTT 接 HomeAssistant，米家走官方 OAuth2 API |
| 任务调度     | robfig/cron                                           | 定时备份、AI 摘要、设备自动化场景 |
| 配置管理     | viper                                                 | 支持 TOML/YAML/JSON/环境变量，热重载 |
| 日志       | zap / lumberjack                                      | 高性能结构化日志 + 自动切割 |
| 认证       | JWT + Argon2id                                        | PHC 字符串格式 |
| 前端       | Vue3 + Element Plus                                   | 响应式、组件丰富、构建产物可嵌入 Go 二进制 |

### 1.3 系统分层架构

系统采用 **六层架构**：

```
┌─────────────────────────────────────────────────────────┐
│                    表现层 (Presentation)                 │
│   Web UI (Vue3)  │  移动端 PWA  │  桌面端 (Wails)       │
├─────────────────────────────────────────────────────────┤
│                   网关层 (Gateway)                       │
│   Gin Router  │  JWT 鉴权  │  限流  │  WebSocket Hub     │
├─────────────────────────────────────────────────────────┤
│                 业务逻辑层 (Business)                    │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌────────────┐  │
│  │ 文件管理  │ │ 用户权限  │ │ 存储监控  │ │ 任务调度    │  │
│  └──────────┘ └──────────┘ └──────────┘ └────────────┘  │
│  ┌──────────┐ ┌──────────────────────────────────────┐  │
│  │ IoT 控制  │ │         AI 智能管家服务              │  │
│  └──────────┘ └──────────────────────────────────────┘  │
├─────────────────────────────────────────────────────────┤
│                  插件层 (Plugin)                         │
│   go-plugin 进程外插件 │  Hook 事件钩子  │  Goja 脚本引擎   │
├─────────────────────────────────────────────────────────┤
│                 数据访问层 (Data Access)                 │
│   GORM (SQLite)  │  文件系统 IO  │  向量库 (chromem)    │
└─────────────────────────────────────────────────────────┘
│                基础设施层 (Infrastructure)               │
│   Ollama Runtime  │  MQTT Broker  │  日志  │  配置      │
└─────────────────────────────────────────────────────────┘
```

**各层职责**：
- **表现层**：提供 Web 管理界面，支持文件管理、AI 对话、设备控制三大主功能区。
- **网关层**：统一入口，负责鉴权、路由、WebSocket 连接管理、请求限流。
- **业务逻辑层**：核心业务处理，包含文件/用户/存储模块，以及 AI 管家和 IoT 控制模块。
- **插件层**：提供三级扩展机制（go-plugin 进程外插件、Hook 事件钩子、Goja 脚本引擎），支持动态扩展。
- **数据访问层**：元数据存入 SQLite，文件 blob 直接存文件系统，AI 语义向量存入嵌入式向量库。
- **基础设施层**：Ollama 运行时、MQTT 消息代理、日志、配置等底层服务。

### 1.4 部署拓扑

```
                    ┌──────────────┐
                    │  家庭路由器   │
                    └──────┬───────┘
                           │ 局域网
          ┌────────────────┼────────────────┐
          │                │                │
   ┌──────▼──────┐  ┌──────▼──────┐  ┌──────▼──────┐
   │  手机/平板   │  │  PC/笔记本   │  │  智能电视    │
   │  (Web/PWA)  │  │  (Web/桌面)  │  │  (DLNA/投屏) │
   └─────────────┘  └─────────────┘  └─────────────┘
                           │
                  ┌────────▼─────────┐
                  │   NAS 主机        │
                  │  ┌─────────────┐  │
                  │  │  Gin Server  │  │
                  │  │  :8080       │  │
                  │  │  (API+WebDAV)│  │
                  │  ├─────────────┤  │
                  │  │  插件运行时   │  │
                  │  │  (go-plugin/Goja) │  │
                  │  ├─────────────┤  │
                  │  │  Ollama      │  │
                  │  │  :11434      │  │
                  │  ├─────────────┤  │
                  │  │  MQTT Broker │  │
                  │  │  :1883       │  │
                  │  └─────────────┘  │
                  └────────┬──────────┘
                           │
          ┌────────────────┼────────────────┐
          │                │                │
   ┌──────▼──────┐  ┌──────▼──────┐  ┌──────▼──────┐
   │  米家设备    │  │  HA 设备     │  │  通用 MQTT   │
   │ (米家云API)  │  │ (本地MQTT)   │  │  设备        │
   └─────────────┘  └─────────────┘  └─────────────┘
```
### 1.5 性能指标与服务等级目标（SLO）

为保障系统在实际家庭环境中的可用性，定义以下关键性能指标（KPI）与服务等级目标（SLO）：
| 指标维度 | 具体指标 | 目标值（SLO） | 测量方式 |
| ---- | ---- | ---- | ---- |
| 文件传输 | 局域网大文件（>1GB）上传吞吐量 | ≥ 100 MB/s（千兆网络） | tus 上传日志统计 |
| | 文件列表加载时间（1000 个文件） | ≤ 500ms | API 响应时间（P95） |
| | 秒传去重命中响应时间 | ≤ 50ms | 数据库索引查询 |
| AI 推理 | 对话首字延迟（TTFT，7B 模型，CPU） | ≤ 2s | Ollama 流式响应首包时间 |
| | 对话首字延迟（TTFT，7B 模型，GPU） | ≤ 500ms | 同上 |
| | 非流式对话完整响应（512 tokens） | ≤ 15s | 从请求到响应完成 |
| | RAG 向量检索延迟（TOP 5，10万条向量） | ≤ 200ms | chromem-go 查询耗时 |
| 设备控制 | 米家设备控制指令端到端延迟 | ≤ 3s | 从用户触发到设备状态回传 |
| | MQTT 本地设备控制延迟 | ≤ 500ms | 同上 |
| 系统并发 | 最大并发 WebSocket 连接数 | ≥ 100 | 压力测试 |
| | 最大并发文件上传任务数 | ≥ 10 | tusd 任务队列 |
| 系统可用性 | 系统整体可用性（排除 Ollama 模型加载时间） | 99.9%（年停机 < 8.76 小时） | 健康检查探针 |

---

## 2. 功能模块设计

### 2.1 配置管理模块

#### 2.1.1 模块设计

采用 **viper** 统一管理，支持 **TOML** 配置文件、环境变量覆盖、运行时热重载。

配置分为以下类别：

| 配置类             | 说明                              |
| --------------- | ------------------------------- |
| `ServerConfig`  | 服务端口、运行模式、CORS 设置               |
| `StorageConfig` | 存储根目录、分块大小、配额设置、rclone 备份、WebDAV        |
| `TusConfig`     | tus 可恢复上传配置                       |
| `AuthConfig`    | JWT 密钥、Argon2id 参数、Token 过期时间   |
| `AIConfig`      | Ollama 地址、默认模型、对话参数、RAG 设置      |
| `IoTConfig`     | 米家 OAuth 凭证、MQTT Broker 地址、设备映射 |
| `PluginConfig`  | 插件目录、自动加载、超时设置                  |
| `LogConfig`     | 日志级别、输出路径、切割策略                  |
| `DegradationConfig` | 降级开关（CPU 高负载时自动禁用 RAG）        |

#### 2.1.2 配置文件结构

> **修正说明**：原配置中 `version_keep`、`trash_days`、`webdav_enabled`、`webdav_prefix` 被错误放置在 `[tus]` 表中，根据 Go 结构体定义它们应属于 `[storage]`。已修正字段归属，并整合了分散在 2.5.6 节的 `[ai.rag]` 向量库切换配置和 6.4 节的 `[degradation]` 降级开关配置。

```toml
# config.toml

# ----------------------------------------------------------
# 服务端配置
# ----------------------------------------------------------
[server]
port = 8080
mode = "release"          # debug / release
read_timeout = 60
write_timeout = 300

# ----------------------------------------------------------
# 存储配置
# ----------------------------------------------------------
[storage]
# 本地文件系统存储
root = "./data/files"
metadata_db = ""                 # 元数据库 SQLite 位置（空 = root/metadata.db，v0.11 起）
version_keep = 5                 # 保留历史版本数
trash_days = 30                  # 回收站保留天数
# WebDAV 配置
webdav_enabled = true
webdav_prefix = "/dav"

# rclone 备份配置（支持本地、S3、WebDAV、SFTP 等多种后端）
[storage.rclone]
enabled = false
remote = "backup:/data/backup"     # rclone remote:path 格式
schedule = "0 3 * * *"    # 每天凌晨3点

# ----------------------------------------------------------
# tus 可恢复分块上传配置
# ----------------------------------------------------------
[tus]
enabled = true
path_prefix = "/files/upload/"   # tus 上传端点前缀
chunk_size = 5242880             # 5MB 分块（客户端可自定义）
max_size = 10737418240           # 10GB 单文件上限
store_dir = "./data/tus"        # tus 临时存储目录
concurrent_uploads = true        # 允许并发分块上传
dedup = true                     # MD5 秒传去重

# ----------------------------------------------------------
# 认证配置
# ----------------------------------------------------------
[auth]
jwt_secret = "change-me-in-production"
jwt_expire = 86400        # 24h
# Argon2id 密码哈希参数
argon2_time = 3           # 迭代次数
argon2_memory = 65536     # 内存占用（KB），即 64MB
argon2_threads = 4        # 并行线程数
argon2_key_len = 32       # 输出密钥长度（字节）
argon2_salt_len = 16      # 盐长度（字节）

# ----------------------------------------------------------
# AI 智能管家配置
# ----------------------------------------------------------
[ai]
ollama_host = "http://localhost:11434"
default_model = "qwen2:7b"
embedding_model = "nomic-embed-text"
conversation_max_tokens = 4096
temperature = 0.7

# RAG 检索增强生成配置
[ai.rag]
enabled = true
store_type = "chromem"   # 可选: chromem, qdrant
chunk_size = 512
chunk_overlap = 50
top_k = 5

# Qdrant 向量库配置（store_type = "qdrant" 时生效）
[ai.rag.qdrant]
host = "localhost"
port = 6334
api_key = ""
collection_name = "file_chunks"

# ----------------------------------------------------------
# IoT 智能家居配置
# ----------------------------------------------------------
[iot.mihome]
enabled = true
client_id = "your-mi-client-id"
client_secret = "your-mi-client-secret"
redirect_uri = "http://nas.local:8080/api/iot/mihome/callback"
api_base = "https://api.io.mi.com/app"

[iot.mqtt]
enabled = true
broker = "tcp://localhost:1883"
client_id = "smart-nas"
username = ""
password = ""

# ----------------------------------------------------------
# 插件系统配置
# ----------------------------------------------------------
[plugin]
enabled = true
dir = "./plugins"          # 插件目录
auto_load = true           # 启动时自动扫描加载
timeout = 30               # 插件调用超时（秒）

# ----------------------------------------------------------
# 日志配置
# ----------------------------------------------------------
[log]
level = "info"
path = "./data/logs"
max_size = 100            # MB
max_backups = 7
max_age = 30              # days

# 说明（v0.08）：日志位置 / 最大大小 / 保留天数可在 Web「管理 → 设置」中调整并
# 即时生效（无需重启），界面设置（settings.toml）优先于本配置；过期日志在启动时、
# 每日定时与滚动归档时三级清理。持久化元数据（users / file_metas / file_versions /
# uploads / settings）自 v0.08 起统一为 TOML 格式，旧 *.json 启动时自动迁移并删除。

# ----------------------------------------------------------
# 降级开关配置（热更新生效）
# ----------------------------------------------------------
[degradation]
# 当检测到 CPU 高负载时，自动禁用向量检索，仅保留基础对话
auto_disable_rag_on_high_cpu = true
cpu_threshold = 0.85

# ----------------------------------------------------------
# 视频在线播放配置（v0.16）
# ----------------------------------------------------------
[play]
enabled = true
max_online_height = 1440        # 最高在线播放分辨率高度（2K=1440），超出提示下载
ffmpeg_path = "ffmpeg"          # 留空自动从 PATH 查找
ffprobe_path = "ffprobe"
remux_concurrency = 3           # 转封装（-c copy）最大并发
transcode_concurrency = 1       # 实时转码最大并发（大开销，防止 NAS 过载）
transcode_threads = 2           # 单个转码任务线程数
ticket_ttl_minutes = 30         # 播放凭证无 Range 请求自动失效时长
cache_dir = "./data/cache/video" # 转封装产物缓存目录
ip_bind = true                  # 播放凭证与请求 IP 绑定（防盗链）
```

#### 2.1.3 关键结构体与方法

> **修正说明**：原结构体缺少 `RcloneConfig`、`DegradationConfig`、`QdrantConfig` 及对应字段，且 `Config` 中未包含 `Degradation` 字段。已补充完整，确保与 config.toml 一一对应。

```go
// internal/config/config.go
package config

type Config struct {
    Server      ServerConfig      `mapstructure:"server"`
    Storage     StorageConfig     `mapstructure:"storage"`
    Tus         TusConfig         `mapstructure:"tus"`
    Auth        AuthConfig        `mapstructure:"auth"`
    AI          AIConfig          `mapstructure:"ai"`
    IoT         IoTConfig         `mapstructure:"iot"`
    Plugin      PluginConfig      `mapstructure:"plugin"`
    Log         LogConfig         `mapstructure:"log"`
    Degradation DegradationConfig `mapstructure:"degradation"`
}

// ---------- Server ----------
type ServerConfig struct {
    Port         int    `mapstructure:"port"`
    Mode         string `mapstructure:"mode"`
    ReadTimeout  int    `mapstructure:"read_timeout"`
    WriteTimeout int    `mapstructure:"write_timeout"`
}

// ---------- Storage ----------
type StorageConfig struct {
    Root          string       `mapstructure:"root"`
    VersionKeep   int          `mapstructure:"version_keep"`
    TrashDays     int          `mapstructure:"trash_days"`
    WebDAVEnabled bool         `mapstructure:"webdav_enabled"`
    WebDAVPrefix  string       `mapstructure:"webdav_prefix"`
    Rclone        RcloneConfig `mapstructure:"rclone"`
}

type RcloneConfig struct {
    Enabled  bool   `mapstructure:"enabled"`
    Remote   string `mapstructure:"remote"`
    Schedule string `mapstructure:"schedule"`
}

// ---------- Tus ----------
type TusConfig struct {
    Enabled           bool   `mapstructure:"enabled"`
    PathPrefix        string `mapstructure:"path_prefix"`
    ChunkSize         int64  `mapstructure:"chunk_size"`
    MaxSize           int64  `mapstructure:"max_size"`
    StoreDir          string `mapstructure:"store_dir"`
    ConcurrentUploads bool   `mapstructure:"concurrent_uploads"`
    Dedup             bool   `mapstructure:"dedup"`
}

// ---------- Auth ----------
type AuthConfig struct {
    JWTSecret      string `mapstructure:"jwt_secret"`
    JWTExpire      int    `mapstructure:"jwt_expire"`
    Argon2Time     uint32 `mapstructure:"argon2_time"`
    Argon2Memory   uint32 `mapstructure:"argon2_memory"`
    Argon2Threads  uint8  `mapstructure:"argon2_threads"`
    Argon2KeyLen   uint32 `mapstructure:"argon2_key_len"`
    Argon2SaltLen  uint32 `mapstructure:"argon2_salt_len"`
}

// ---------- AI ----------
type AIConfig struct {
    OllamaHost           string    `mapstructure:"ollama_host"`
    DefaultModel         string    `mapstructure:"default_model"`
    EmbeddingModel       string    `mapstructure:"embedding_model"`
    ConversationMaxTokens int      `mapstructure:"conversation_max_tokens"`
    Temperature          float64   `mapstructure:"temperature"`
    RAG                  RAGConfig `mapstructure:"rag"`
}

type RAGConfig struct {
    Enabled      bool         `mapstructure:"enabled"`
    StoreType    string       `mapstructure:"store_type"`
    ChunkSize    int          `mapstructure:"chunk_size"`
    ChunkOverlap int          `mapstructure:"chunk_overlap"`
    TopK         int          `mapstructure:"top_k"`
    Qdrant       QdrantConfig `mapstructure:"qdrant"`
}

type QdrantConfig struct {
    Host            string `mapstructure:"host"`
    Port            int    `mapstructure:"port"`
    APIKey          string `mapstructure:"api_key"`
    CollectionName  string `mapstructure:"collection_name"`
}

// ---------- IoT ----------
type IoTConfig struct {
    Mihome MihomeConfig `mapstructure:"mihome"`
    MQTT   MQTTConfig   `mapstructure:"mqtt"`
}

type MihomeConfig struct {
    Enabled      bool   `mapstructure:"enabled"`
    ClientID     string `mapstructure:"client_id"`
    ClientSecret string `mapstructure:"client_secret"`
    RedirectURI  string `mapstructure:"redirect_uri"`
    APIBase      string `mapstructure:"api_base"`
}

type MQTTConfig struct {
    Enabled  bool   `mapstructure:"enabled"`
    Broker   string `mapstructure:"broker"`
    ClientID string `mapstructure:"client_id"`
    Username string `mapstructure:"username"`
    Password string `mapstructure:"password"`
}

// ---------- Plugin ----------
type PluginConfig struct {
    Enabled  bool   `mapstructure:"enabled"`
    Dir      string `mapstructure:"dir"`
    AutoLoad bool   `mapstructure:"auto_load"`
    Timeout  int    `mapstructure:"timeout"`
}

// ---------- Log ----------
type LogConfig struct {
    Level      string `mapstructure:"level"`
    Path       string `mapstructure:"path"`
    MaxSize    int    `mapstructure:"max_size"`
    MaxBackups int    `mapstructure:"max_backups"`
    MaxAge     int    `mapstructure:"max_age"`
}

// ---------- Degradation ----------
type DegradationConfig struct {
    AutoDisableRAGOnHighCPU bool    `mapstructure:"auto_disable_rag_on_high_cpu"`
    CPUThreshold             float64 `mapstructure:"cpu_threshold"`
}

// ---------- Manager ----------
type Manager struct {
    viper *viper.Viper
    cfg   *Config
    mu    sync.RWMutex
}

func NewManager(configPath string) (*Manager, error) { ... }
func (m *Manager) GetConfig() *Config { ... }
func (m *Manager) UpdateConfig(patch map[string]interface{}) error { ... }
func (m *Manager) WatchConfig() { ... }
```

---

### 2.2 通信模块

#### 2.2.1 模块设计

通信采用 **HTTP/REST + WebSocket** 标准协议，降低客户端接入门槛，同时保留高性能文件传输能力。

通信模块分为四个子模块：

| 子模块 | 协议 | 用途 |
|--------|------|------|
| HTTP API | HTTPS (TLS) | 业务指令交互（登录、文件列表、设备控制） |
| WebSocket | WSS | 实时推送（传输进度、AI 流式输出、设备状态） |
| 文件传输 | **tus 协议** (tusd) | 可恢复分块上传、断点续传、并发上传 |
| WebDAV | HTTPS | 系统级文件挂载访问（Windows/macOS/Linux 原生支持） |

#### 2.2.2 HTTP API 通信设计

**统一响应格式**：
```json
{
  "code": 0,
  "message": "success",
  "data": { },
  "timestamp": 1718764800
}
```

**错误码规范**：

| code | 含义 |
|------|------|
| 0 | 成功 |
| 1001 | 未认证 / Token 过期 |
| 1002 | 权限不足 |
| 2001 | 文件不存在 |
| 2002 | 存储空间不足 |
| 3001 | AI 模型未加载 |
| 3002 | AI 推理超时 |
| 4001 | IoT 设备离线 |
| 4002 | 米家 API 调用失败 |
| 5000 | 服务器内部错误 |

#### 2.2.3 WebSocket 实时通道

WebSocket 端点：`/ws?token=<jwt>`

消息类型：
```json
// 文件传输进度
{ "type": "file_progress", "task_id": "uuid", "uploaded": 5242880, "total": 10485760, "speed": 1048576 }

// AI 对话流式输出
{ "type": "ai_chunk", "conversation_id": "uuid", "delta": "今天天气", "done": false }

// 设备状态变更
{ "type": "device_status", "device_id": "miot.123", "status": { "power": true, "temp": 26 } }

// 系统通知
{ "type": "notification", "level": "info", "title": "备份完成", "content": "照片目录已备份至外置硬盘" }
```

#### 2.2.4 文件传输协议（tus 可恢复上传）

采用 **tus 协议**（https://tus.io）作为文件上传方案。

**tus 协议核心流程**：
1. **创建上传（POST）**：客户端发送文件大小、元数据（filename, md5, parent_id），服务端返回上传 URL。
2. **上传数据（PATCH）**：客户端按偏移量发送分块数据，服务端更新偏移量。
3. **查询进度（HEAD）**：断点续传时获取当前偏移量。
4. **上传完成回调**：tusd 触发回调，服务端校验 MD5、移动文件、创建元数据。

**秒传去重（扩展）**：
客户端计算 MD5 → 请求初始化接口 → 若数据库命中则返回秒传成功，否则走 tus 正常上传。

**断点续传原理**：
网络中断后，客户端调用 HEAD 获取已上传偏移量，从该偏移量继续 PATCH。

**并发分块上传**：
tus 支持 `Upload-Concat` 扩展，前端 `tus-js-client` 配置 `parallelUploads` 即可启用。

**下载流程**：
标准 HTTP Range 请求，支持断点续传，WebSocket 推送进度。

**WebDAV 访问**：
端点 `https://nas.local:8080/dav/`，支持 PROPFIND、MKCOL、PUT、GET、DELETE、MOVE、COPY，可被操作系统原生挂载。

#### 2.2.5 tusd 服务端集成

```go
// internal/transport/tusd/server.go
package tusd

import (
    "github.com/tus/tusd/v2/pkg/handler"
    "github.com/tus/tusd/v2/pkg/filestore"
    "smart-nas/internal/config"
)

func NewTusHandler(cfg config.TusConfig, storageRoot string) (*handler.Handler, error) {
    store := filestore.New(cfg.StoreDir)
    store.UseDiskIsFullCheck = true

    composer := handler.NewStoreComposer()
    store.UseIn(composer)

    h, err := handler.NewHandler(handler.Config{
        BasePath:                cfg.PathPrefix,
        StoreComposer:           composer,
        MaxSize:                 cfg.MaxSize,
        RespectForwardedHeaders: true,
        PreFinishResponseCallback: func(event handler.HookEvent) error {
            return onUploadComplete(event)
        },
        PreCreateCallback: func(event handler.HookEvent) (handler.HTTPResponse, error) {
            return onUploadCreate(event)
        },
    })
    if err != nil {
        return nil, err
    }
    return h, nil
}

func onUploadCreate(event handler.HookEvent) (handler.HTTPResponse, error) {
    metadata := event.Upload.MetaData
    md5 := metadata["md5"]
    if fileID, exists := checkDedup(md5); exists {
        return handler.HTTPResponse{
            StatusCode: 200,
            Body:       []byte(`{"dedup":true,"file_id":` + fileID + `}`),
        }, nil
    }
    return handler.HTTPResponse{StatusCode: 200}, nil
}

func onUploadComplete(event handler.HookEvent) error {
    // 校验 MD5、移动文件、创建元数据、触发插件事件
    return nil
}

func RegisterTusRoutes(r *gin.Engine, h *handler.Handler) {
    r.Any("/files/upload/*filepath", gin.WrapH(h))
    r.POST("/files/upload/", gin.WrapH(h))
}
```

**前端集成（tus-js-client）**：
```javascript
import * as tus from 'tus-js-client';

export function uploadFile(file, { md5, parentId, onProgress, onSuccess, onError }) {
  const upload = new tus.Upload(file, {
    endpoint: '/files/upload/',
    retryDelays: [0, 1000, 3000, 5000],
    chunkSize: 5 * 1024 * 1024,
    parallelUploads: 3,
    metadata: {
      filename: file.name,
      content_type: file.type,
      md5: md5,
      parent_id: String(parentId),
      size: String(file.size),
    },
    onProgress: (bytesUploaded, bytesTotal) => onProgress?.(bytesUploaded, bytesTotal),
    onSuccess: () => onSuccess?.(upload.url),
    onError: (err) => onError?.(err),
  });

  upload.findPreviousUploads().then((previousUploads) => {
    if (previousUploads.length > 0) {
      upload.resumeFromPreviousUpload(previousUploads[0]);
    }
    upload.start();
  });
  return upload;
}
```

#### 2.2.6 传输任务管理

```go
// internal/transport/manager.go
type FileTransferTask struct {
    ID         string    `json:"id"`
    Type       string    `json:"type"`        // upload / download
    Status     string    `json:"status"`
    Filename   string    `json:"filename"`
    StoragePath string   `json:"-"`
    TotalSize  int64     `json:"total_size"`
    Uploaded   int64     `json:"uploaded"`
    Speed      int64     `json:"speed"`
    StartedAt  time.Time `json:"started_at"`
    UserID     uint      `json:"-"`
    TusURL     string    `json:"-"`
}

type Manager struct {
    tasks       map[string]*FileTransferTask
    mu          sync.RWMutex
    wsHub       *ws.Hub
    storageRoot string
    tusHandler  *tusd.Handler
}

func (m *Manager) InitUpload(req *InitUploadRequest) (*InitUploadResponse, error) { ... }
func (m *Manager) GetUploadProgress(uploadID string) (int64, error) { ... }
func (m *Manager) DownloadFile(fileID uint, w http.ResponseWriter, r *http.Request) error { ... }
func (m *Manager) GetTask(taskID string) (*FileTransferTask, bool) { ... }
```

---

### 2.3 文件存储 / NAS 模块

#### 2.3.1 模块设计

实现目录树、文件标签、共享链接、回收站、版本管理等企业级特性。

**核心数据模型**：
```go
// internal/storage/model.go
type FileMeta struct {
    ID           uint      `gorm:"primaryKey" json:"id"`
    Name         string    `gorm:"index;not null" json:"name"`
    ParentID     uint      `gorm:"index" json:"parent_id"`
    IsDir        bool      `gorm:"default:false" json:"is_dir"`
    Size         int64     `gorm:"default:0" json:"size"`
    MD5          string    `gorm:"index;size:32" json:"md5"`
    MimeType     string    `json:"mime_type"`
    StoragePath  string    `json:"-"`           // 不对外暴露
    OwnerID      uint      `gorm:"index" json:"owner_id"`
    Shared       bool      `gorm:"default:false" json:"shared"`
    ShareToken   string    `gorm:"index;size:32" json:"-"`
    DeletedAt    gorm.DeletedAt `gorm:"index" json:"deleted_at"`
    CreatedAt    time.Time `json:"created_at"`
    UpdatedAt    time.Time `json:"updated_at"`
}

type FileVersion struct {
    ID        uint   `gorm:"primaryKey"`
    FileID    uint   `gorm:"index"`
    Version   int
    MD5       string `gorm:"size:32"`
    StoragePath string
    Size      int64
    CreatedAt time.Time
}
```

#### 2.3.2 存储策略

- **扁平存储**：按 MD5 前两级目录分散存储，避免单目录文件过多。
- **秒传去重**：相同 MD5 文件只存一份物理副本，元数据引用计数。
- **软删除**：删除文件移入回收站（v0.10 起默认集中存放于 `data/files/trash`，不再在各盘符建 `.trash`；可在设置页自定义位置），30 天后自动清理（可配置）。
- **版本管理**：同名文件上传时保留历史版本（默认 5 个），旧版本存入 `data/versions/`。
- **临时分块**：上传中的分块存入 `data/uploads/`，完成后合并清理。
- **隐藏/系统文件屏蔽（v0.10）**：共用方法 `util.IsProtectedName / IsProtectedPath`（名称级，REST 与 WebDAV 共用）+ Windows 隐藏/系统属性检测；`$RECYCLE.BIN`、`System Volume Information`、`pagefile.sys`、`Config.Msi`、`DeliveryOptimization` 等系统条目在列表/搜索/回收站中不显示，且详情、下载、重命名、删除、移动、复制、上传、分享、恢复等全部操作统一拒绝，防止误操作破坏系统。

#### 2.3.3 关键服务方法

```go
// internal/storage/service.go
type Service interface {
    ListFiles(userID uint, parentID uint, keyword string) ([]FileMeta, error)
    GetFileMeta(fileID uint) (*FileMeta, error)
    CreateDir(userID uint, name string, parentID uint) (*FileMeta, error)
    DeleteFile(userID uint, fileIDs []uint) error
    RestoreFromTrash(userID uint, fileIDs []uint) error
    RenameFile(userID uint, fileID uint, newName string) error
    MoveFile(userID uint, fileIDs []uint, targetDirID uint) error
    CreateShareLink(fileID uint, expireHours int, password string) (string, error)
    GetStorageStats(userID uint) (*StorageStats, error)
}
```

---

### 2.4 用户认证与权限模块

#### 2.4.1 模块设计

采用 **JWT 无状态认证** + **Argon2id 密码哈希**（OWASP 推荐）。

**密码加密**：
```go
// internal/auth/password.go
package auth

import (
    "crypto/rand"
    "crypto/subtle"
    "encoding/base64"
    "golang.org/x/crypto/argon2"
)

const (
    SaltLen    = 16
    KeyLen     = 32
    Time       = 3
    Memory     = 65536 // 64MB
    Threads    = 4
)

func HashPassword(password string) (salt, hash string, err error) {
    saltBytes := make([]byte, SaltLen)
    if _, err := rand.Read(saltBytes); err != nil {
        return "", "", err
    }
    hashBytes := argon2.IDKey([]byte(password), saltBytes, Time, Memory, Threads, KeyLen)
    return base64.StdEncoding.EncodeToString(saltBytes),
           base64.StdEncoding.EncodeToString(hashBytes), nil
}

func VerifyPassword(password, saltB64, hashB64 string) bool {
    salt, _ := base64.StdEncoding.DecodeString(saltB64)
    expected, _ := base64.StdEncoding.DecodeString(hashB64)
    actual := argon2.IDKey([]byte(password), salt, Time, Memory, Threads, KeyLen)
    return subtle.ConstantTimeCompare(actual, expected) == 1
}
```

**用户模型**：
```go
type User struct {
    ID           uint   `gorm:"primaryKey" json:"id"`
    Username     string `gorm:"uniqueIndex;size:64;not null" json:"username"`
    PasswordHash string `gorm:"size:128;not null" json:"-"`
    PasswordSalt string `gorm:"size:32;not null" json:"-"`
    Role         string `gorm:"size:20;default:user" json:"role"` // admin / user
    StorageQuota int64  `gorm:"default:0" json:"storage_quota"`   // 0 = 无限
    Avatar       string `json:"avatar"`
    CreatedAt    time.Time `json:"created_at"`
    UpdatedAt    time.Time `json:"updated_at"`
}
```

**权限矩阵**（v0.04 起角色为 **master / admin / user**，且用户管理基于**目录权限（读/写）**而非配额；
v0.13 起 admin 同样受自身权限约束——未配置权限默认全量，配置后仅授权路径可访问，
且只能为他人授予自身权限范围内的目录、读写不超自身级别）：

| 操作 | master（主人） | admin（管理员） | user（普通用户） |
|------|------|-------|-------|
| 文件上传/下载/浏览 | ✓（全部磁盘） | ✓（全部磁盘） | 仅其 permissions 中授权的目录，未配置则默认全量 |
| 用户管理（增删改） | ✓（可管理所有用户，含管理员） | 仅可管理普通用户 | ✗ |
| 创建/提升管理员 | ✓ | ✗ | ✗ |
| 主人账号（密码/角色/增删） | 仅可通过配置文件修改 | ✗ | ✗ |
| 系统配置修改（设置） | ✓ | ✗（只读查看） | ✗ |
| AI 对话 | ✓ | ✓ | ✓ |
| IoT 设备控制 | ✓ | ✓ | ✓（受设备授权限制） |
| 系统状态 / 在线用户 | ✓（含在线用户明细） | ✓（含在线用户明细） | 仅连接数 |

> 说明：文件系统按真实磁盘组织（`storage.disks`，留空自动发现 Windows 盘符）；
> 用户的 `Permissions []Permission{Path, Read, Write}` 决定其对哪些目录可读/可写，
> 具备父目录完整权限时其下子目录条目自动去重。

---

### 2.5 AI 智能管家模块（核心新增）

#### 2.5.1 模块定位

基于 **Ollama 本地大模型** 实现，所有推理在家庭局域网内完成。

**三大核心能力**：
1. **自然语言交互**：通过对话管理文件、查询信息、控制设备。
2. **文件语义理解（RAG）**：对文档、照片、笔记建立向量索引，支持语义搜索和内容问答。
3. **智能自动化**：基于时间/事件/传感器触发设备联动。

#### 2.5.2 Ollama 集成设计

**Ollama 客户端封装**：
```go
// internal/ai/ollama/client.go
type Client struct {
    baseURL    string
    httpClient *http.Client
}

type ChatRequest struct {
    Model    string        `json:"model"`
    Messages []ChatMessage `json:"messages"`
    Stream   bool          `json:"stream"`
    Options  *ChatOptions  `json:"options,omitempty"`
}

type ChatMessage struct {
    Role    string `json:"role"`
    Content string `json:"content"`
    ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

func (c *Client) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
func (c *Client) ChatStream(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error)
func (c *Client) GenerateEmbedding(ctx context.Context, model, text string) ([]float32, error)
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error)
```

**系统提示词（System Prompt）**：
```
你是一个智能家庭管家，运行在家庭 NAS 上。你的职责包括：
1. 文件管理：帮助用户查找、整理、总结文件。
2. 设备控制：管理智能家居设备。
3. 信息查询：回答日常问题，利用本地知识库。
4. 场景联动：根据用户描述创建自动化场景。

约束：
- 所有操作必须在用户授权范围内执行。
- 不确定时主动询问。
- 回答简洁明了，符合中文表达习惯。
- 涉及危险操作时需二次确认。
```

#### 2.5.3 Function Calling（工具调用）设计

**已注册工具列表**：

| 工具名 | 功能 | 参数 |
|--------|------|------|
| `search_files` | 语义搜索文件 | `query: string`, `file_type?: string`, `limit?: int` |
| `read_file` | 读取文件内容 | `file_id: string`, `max_chars?: int` |
| `list_files` | 列出目录文件 | `path: string`, `recursive?: bool` |
| `control_device` | 控制智能设备 | `device_id: string`, `action: string`, `params?: object` |
| `get_device_status` | 查询设备状态 | `device_id: string` |
| `list_devices` | 列出所有设备 | `room?: string`, `type?: string` |
| `create_automation` | 创建自动化场景 | `name: string`, `trigger: object`, `actions: array` |
| `get_storage_stats` | 查询存储用量 | 无 |
| `get_system_info` | 查询系统状态 | 无 |
| `web_search` | 联网搜索（可选） | `query: string` |

**工具调用流程**：
用户消息 → AI 模型返回 tool_calls → 并行执行工具 → 结果回传模型 → 生成最终回复。

**工具执行器实现**：
```go
// internal/ai/tools/registry.go
type Tool interface {
    Name() string
    Description() string
    Parameters() json.RawMessage
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}

type Registry struct {
    tools map[string]Tool
}

func (r *Registry) Register(t Tool)
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error)
func (r *Registry) ToolSchemas() []json.RawMessage
```

#### 2.5.4 RAG（检索增强生成）设计

**索引构建流程**：
1. 文件上传完成后异步判断是否支持文本提取。
2. 提取文本内容（PDF 用 pdfcpu，Word 用 docx 库）。
3. 按 `chunk_size=512`、`overlap=50` 分块。
4. 调用 Ollama embedding 模型生成向量。
5. 存入 chromem-go 向量库，元数据关联 file_id。

**检索流程**：
```go
// internal/ai/rag/retriever.go
type Retriever struct {
    col      *chromem.Collection
    ollama   *ollama.Client
    topK     int
}

func (r *Retriever) Search(ctx context.Context, query string, topK int) ([]Chunk, error) {
    vec, _ := r.ollama.GenerateEmbedding(ctx, "nomic-embed-text", query)
    results, _ := r.col.Query(ctx, vec, topK, nil, nil)
    return chunks, nil
}
```

**对话时注入上下文**：
```go
systemContext := fmt.Sprintf(`以下是从用户文件中检索到的相关内容，请基于这些内容回答：
%s
---
用户问题：%s`, retrievedContent, userQuery)
```

#### 2.5.5 对话管理

```go
// internal/ai/conversation/manager.go
type Conversation struct {
    ID        string    `json:"id"`
    UserID    uint      `json:"user_id"`
    Title     string    `json:"title"`
    Messages  []Message `json:"messages"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type Message struct {
    ID        string          `json:"id"`
    Role      string          `json:"role"`
    Content   string          `json:"content"`
    ToolCalls []ToolCall      `json:"tool_calls,omitempty"`
    ToolResult string         `json:"tool_result,omitempty"`
    Tokens    int             `json:"tokens"`
    CreatedAt time.Time       `json:"created_at"`
}

type Manager struct {
    conversations map[string]*Conversation
    mu            sync.RWMutex
    maxHistory    int
}
```

#### 2.5.6 向量库抽象与切换方案

采用适配器模式，支持在嵌入式 chromem-go 和外部 Qdrant 之间平滑切换。

接口定义：
```go
// internal/ai/rag/store.go
type VectorStore interface {
    Add(ctx context.Context, id string, vector []float32, metadata map[string]interface{}, content string) error
    Query(ctx context.Context, vector []float32, topK int, filter map[string]interface{}) ([]Chunk, error)
    Delete(ctx context.Context, id string) error
    Close() error
}
```

Chromem 实现（默认，小规模）：
```go
type ChromemStore struct { col *chromem.Collection }
// 实现上述接口...
```

Qdrant 实现（大规模，需切换配置）：
```go
type QdrantStore struct { client *qdrant.Client }
// 实现上述接口...
```

> **配置说明**：向量库切换配置已整合到主配置文件 `config.toml` 的 `[ai.rag]` 和 `[ai.rag.qdrant]` 段，通过 `store_type` 字段选择 `chromem` 或 `qdrant`。切换时需提供数据迁移脚本（从 chromem 导出并导入 Qdrant），迁移期间系统仍提供基础文件搜索（基于文件名），RAG 功能短暂不可用。

---
### 2.6 智能家居接入模块（核心新增）

#### 2.6.1 模块定位

统一接入多种智能家居生态，优先支持 **米家（小米 IoT 开放平台）**，同时预留 MQTT 通用接口兼容 Home Assistant 及其他 MQTT 设备。

#### 2.6.2 米家开放平台接入

**认证流程（OAuth2）**：
用户点击绑定米家账号 → 重定向至米家授权页 → 用户授权 → 回调获取 code → 换取 access_token + refresh_token → 存储凭证并拉取设备列表。

**米家 API 客户端**：
```go
// internal/iot/mihome/client.go
type Client struct {
    clientID     string
    clientSecret string
    accessToken  string
    refreshToken string
    apiBase      string
    httpClient   *http.Client
    tokenMu      sync.Mutex
}

func (c *Client) GetDeviceList(ctx context.Context) ([]Device, error)
func (c *Client) GetDeviceProperties(ctx context.Context, did string, props []PropertyKey) ([]PropertyValue, error)
func (c *Client) SetDeviceProperties(ctx context.Context, did string, params []SetParam) error
func (c *Client) Action(ctx context.Context, did string, siid, aiid int, args []interface{}) error
func (c *Client) RefreshToken(ctx context.Context) error
```

**设备统一抽象层**：
```go
// internal/iot/device/device.go
type Device interface {
    ID() string
    Name() string
    Type() DeviceType
    Platform() string
    Room() string
    IsOnline() bool
    GetStatus(ctx context.Context) (map[string]interface{}, error)
    Execute(ctx context.Context, action string, params map[string]interface{}) error
    SupportedActions() []ActionInfo
}

type Registry struct {
    devices map[string]Device
    mu      sync.RWMutex
}

func (r *Registry) Register(d Device)
func (r *Registry) Get(id string) (Device, bool)
func (r *Registry) List(filters ...Filter) []Device
func (r *Registry) Execute(ctx context.Context, deviceID, action string, params map[string]interface{}) error
```

#### 2.6.3 MQTT 通用接入

```go
// internal/iot/mqtt/manager.go
type Manager struct {
    client   mqtt.Client
    devices  map[string]*MQTTDevice
    topicHandlers map[string]func(topic string, payload []byte)
}

func (m *Manager) Subscribe(topic string, handler func(string, []byte))
func (m *Manager) Publish(topic string, payload interface{}) error
```

#### 2.6.4 自动化场景引擎

```go
// internal/iot/automation/engine.go
type Trigger struct {
    Type     string                 `json:"type"`     // time / device / webhook
    Schedule string                 `json:"schedule"` // cron 表达式
    DeviceID string                 `json:"device_id"`
    Condition map[string]interface{} `json:"condition"`
}

type Action struct {
    DeviceID string                 `json:"device_id"`
    Action   string                 `json:"action"`
    Params   map[string]interface{} `json:"params"`
}

type Rule struct {
    ID       string    `json:"id"`
    Name     string    `json:"name"`
    Enabled  bool      `json:"enabled"`
    Trigger  Trigger   `json:"trigger"`
    Actions  []Action  `json:"actions"`
    CreatedAt time.Time `json:"created_at"`
}

type Engine struct {
    rules   map[string]*Rule
    cron    *cron.Cron
    devReg  *device.Registry
}
```

#### 2.6.5 设备状态实时推送

设备状态变化通过 WebSocket 推送给前端：
```go
func (m *Manager) onDeviceStatusChanged(d device.Device, status map[string]interface{}) {
    m.wsHub.Broadcast([]byte(types.ToJSON(ws.Message{
        Type: "device_status",
        Data: map[string]interface{}{
            "device_id": d.ID(),
            "status":    status,
        },
    })))
}
```

---

### 2.7 Web UI 模块

#### 2.7.1 模块设计

使用 **Vue3 + Element Plus** 响应式 Web 界面，同时提供 Wails 打包的桌面端可选。

**页面结构**：

| 页面 | 路由 | 功能 |
|------|------|------|
| 登录页 | `/login` | 用户登录、服务端地址配置 |
| 仪表盘 | `/dashboard` | 系统状态、存储用量、设备概览、AI 快捷入口 |
| 文件管理 | `/files` | 文件浏览、上传下载、共享、回收站 |
| AI 管家 | `/ai` | 对话界面、对话历史、文件问答 |
| 智能家居 | `/iot` | 设备列表、设备控制、自动化场景 |
| 插件管理 | `/plugins` | 已安装插件、插件市场、插件配置、脚本管理 |
| 用户管理 | `/admin/users` | 用户增删改、权限配置 |
| 系统设置 | `/admin/settings` | 存储配置、AI 模型配置、IoT 绑定 |

#### 2.7.2 前端技术要点

- **构建产物嵌入**：Vue 构建后的 `dist/` 通过 Go `embed` 嵌入二进制，单文件部署。
- **PWA 支持**：可添加到手机桌面，离线查看已缓存文件。
- **大文件上传**：使用 `tus-js-client`，支持断点续传、分块、并发、自动重试；Web Worker 计算 MD5。
- **AI 对话**：Markdown 渲染 + 代码高亮 + 流式打字机效果。
- **设备控制面板**：根据设备类型动态渲染控件（开关、滑块、色板等）。

```go
// 前端嵌入
//go:embed dist/*
var frontendFS embed.FS

func RegisterFrontend(r *gin.Engine) {
    r.NoRoute(func(c *gin.Context) {
        c.FileFromFS("/", http.FS(subFS))
    })
}
```

#### 2.7.3 前端状态管理与路由守卫

状态管理（Pinia Store）：
```typescript
// web/src/stores/index.ts
export const useUserStore = defineStore('user', {
  state: () => ({ token: '', userInfo: null, permissions: [] }),
  actions: {
    login(credentials) { /* ... */ },
    logout() { /* 清除 token，断开 WebSocket */ },
  },
  getters: {
    isAdmin: (state) => state.userInfo?.role === 'admin',
  },
});

export const useUploadStore = defineStore('upload', {
  state: () => ({ tasks: new Map<string, UploadTask>() }),
  actions: {
    addTask(file, parentId) { /* 创建 tus 上传实例 */ },
    resumeTask(id) { /* 断点续传 */ },
  },
});

export const useDeviceStore = defineStore('device', {
  state: () => ({ devices: [], automations: [] }),
  actions: {
    syncDevices() { /* 调用 /api/iot/devices */ },
    control(deviceId, action, params) { /* WebSocket 发送指令 */ },
  },
});
```

路由权限控制：
```typescript
// web/src/router/index.ts
const routes = [
  { path: '/admin', component: AdminLayout, meta: { requiresAdmin: true } },
  { path: '/files', component: Files, meta: { requiresAuth: true } },
  { path: '/login', component: Login, meta: { guestOnly: true } },
];

router.beforeEach((to, from, next) => {
  const userStore = useUserStore();
  if (to.meta.requiresAuth && !userStore.token) {
    return next('/login');
  }
  if (to.meta.requiresAdmin && !userStore.isAdmin) {
    return next('/dashboard');
  }
  next();
});
```

大文件上传 UI 交互：
- 拖拽上传区（支持文件夹拖拽）
- 上传队列列表（显示进度条、速度、剩余时间、取消 / 重试按钮）
- 使用 Web Worker 计算 MD5，避免 UI 卡顿
- 断网自动暂停，恢复后自动续传（tus 自带）

#### 2.7.4 国际化（i18n）预留

前端使用 vue-i18n，后端 API 通过 Accept-Language 头返回本地化错误消息。

前端配置：
```typescript
// web/src/i18n/index.ts
import { createI18n } from 'vue-i18n'
import zh from './locales/zh.json'
import en from './locales/en.json'

const i18n = createI18n({
  locale: navigator.language.startsWith('zh') ? 'zh' : 'en',
  fallbackLocale: 'en',
  messages: { zh, en },
});
```

后端错误消息本地化：
```go
// internal/util/errors.go
var errorMessages = map[string]map[string]string{
    "zh": {"1001": "未认证，请重新登录"},
    "en": {"1001": "Unauthorized, please login again"},
}
func GetLocalizedMessage(code string, lang string) string {
    if msgs, ok := errorMessages[lang]; ok {
        if msg, ok := msgs[code]; ok { return msg }
    }
    return errorMessages["en"][code]
}
```


---

### 2.8 公共工具模块

| 工具类 | Go 实现 | 说明 |
|--------|---------|------|
| `ReflectionUtils` | 标准库 `reflect` | 无需自定义反射扫描 |
| `PBKDF2Util` | `internal/auth/password.go` | 使用 Argon2id |
| `NetworkStatusUtil` | `internal/util/net.go` | 获取局域网 IP、带宽、延迟 |
| `SystemStatus` | `internal/util/system.go` | CPU/内存/磁盘使用情况 |
| MD5 校验 | `crypto/md5` | 秒传去重、完整性校验 |
| JSON 处理 | 标准库 | HTTP 协议无需自定义粘包 |

**系统状态采集**：
```go
// internal/util/system.go
type SystemStatus struct {
    Hostname     string  `json:"hostname"`
    OS           string  `json:"os"`
    Arch         string  `json:"arch"`
    CPUCores     int     `json:"cpu_cores"`
    CPUUsage     float64 `json:"cpu_usage"`
    MemoryTotal  uint64  `json:"memory_total"`
    MemoryUsed   uint64  `json:"memory_used"`
    MemoryUsage  float64 `json:"memory_usage"`
    Disks        []DiskInfo `json:"disks"`
    Uptime       string  `json:"uptime"`
    LANIP        string  `json:"lan_ip"`
}

type DiskInfo struct {
    MountPoint string  `json:"mount_point"`
    Total      uint64  `json:"total"`
    Used       uint64  `json:"used"`
    Usage      float64 `json:"usage"`
}

func GetSystemStatus() (*SystemStatus, error) { ... }
```

---

### 2.9 插件系统模块（核心新增）

#### 2.9.1 设计目标

使 NAS 功能可动态扩展，无需重新编译主程序。

**设计原则**：
- 崩溃隔离、权限可控、跨语言（Go/Python/Node.js）、热插拔、向后兼容。

#### 2.9.2 三级插件架构

| 插件类型 | 实现方式 | 适用场景 | 优点 | 缺点 |
|---------|---------|---------|------|------|
| **go-plugin 进程外插件** | 独立可执行文件 + gRPC | 复杂功能、第三方集成 | 跨语言、崩溃隔离 | IPC 开销 |
| **Hook 钩子插件** | 进程内回调函数 + 事件总线 | 简单扩展、日志、监控 | 零开销 | 需 Go 编译 |
| **Goja 脚本插件** | 嵌入式 JavaScript 引擎 | 用户自定义自动化 | 热更新、无需编译 | 性能有限 |

#### 2.9.3 插件扩展点（Extension Points）

| 扩展点事件 | 触发时机 | 典型用途 |
|-----------|---------|---------|
| `file.before_upload` | 文件上传前 | 病毒扫描、格式校验 |
| `file.after_upload` | 文件上传完成后 | 生成缩略图、AI 索引 |
| `file.before_download` | 文件下载前 | 水印添加、DRM |
| `file.after_delete` | 文件删除后 | 清理关联资源 |
| `ai.before_chat` | AI 对话前 | 提示词注入、敏感词检测 |
| `ai.after_chat` | AI 对话后 | 回复审核、日志 |
| `ai.tool_register` | AI 工具注册时 | 注册自定义工具 |
| `iot.device_event` | 设备状态变更 | 自定义自动化逻辑 |
| `iot.platform_register` | IoT 平台注册时 | 接入新平台 |
| `auth.login` | 用户登录时 | 二次验证、登录通知 |
| `notification.send` | 发送通知时 | 自定义通知渠道 |
| `system.startup` | 系统启动时 | 插件初始化 |
| `system.shutdown` | 系统关闭时 | 插件清理 |
| `cron.tick` | 定时触发 | 定时备份、同步 |

#### 2.9.4 go-plugin 进程外插件

采用 **hashicorp/go-plugin** 框架，自动管理进程生命周期、握手协商、连接重建。

**插件接口定义（SDK）**：
```go
// internal/plugin/goplugin/sdk/interface.go
package sdk

type PluginInfo struct {
    ID          string
    Name        string
    Version     string
    Author      string
    Description string
    APIVersion  string
    Events      []string
    Permissions []string
}

type HandleResult struct {
    Success          bool
    Result           []byte
    Error            string
    StopPropagation  bool
    ModifiedMetadata map[string]string
}

type Plugin interface {
    Info() (*PluginInfo, error)
    Init(configJSON string, dataDir string, hostVersion string) error
    Handle(ctx context.Context, event string, payload []byte, metadata map[string]string) (*HandleResult, error)
    Shutdown() error
}
```

**握手配置**：
```go
// internal/plugin/goplugin/sdk/plugin.go
var HandshakeConfig = plugin.HandshakeConfig{
    ProtocolVersion:  1,
    MagicCookieKey:   "SMART_NAS_PLUGIN",
    MagicCookieValue: "smart-nas-v1",
}

type SmartNASPlugin struct {
    plugin.Plugin
    Impl Plugin
}

func (p *SmartNASPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
    RegisterPluginServer(s, &GRPCServer{Impl: p.Impl})
    return nil
}

func (p *SmartNASPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
    return &GRPCClient{client: NewPluginClient(c)}, nil
}
```

**主程序端加载器**：
```go
// internal/plugin/goplugin/loader.go
type ExternalPlugin struct {
    id       string
    client   *plugin.Client
    rpcClient plugin.ClientProtocol
    impl     sdk.Plugin
    info     *sdk.PluginInfo
    config   map[string]interface{}
    dataDir  string
    enabled  bool
    execPath string
}

func (p *ExternalPlugin) Start(ctx context.Context) error {
    p.client = plugin.NewClient(&plugin.ClientConfig{
        HandshakeConfig: sdk.HandshakeConfig,
        Plugins:         sdk.PluginMap,
        Cmd:             exec.Command(p.execPath),
        AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
        AutoMTLS: true,
    })
    rpcClient, _ := p.client.Client()
    p.rpcClient = rpcClient
    raw, _ := rpcClient.Dispense("smartnas")
    p.impl = raw.(sdk.Plugin)
    info, _ := p.impl.Info()
    p.info = info
    configJSON, _ := json.Marshal(p.config)
    return p.impl.Init(string(configJSON), p.dataDir, Version)
}

func (p *ExternalPlugin) Handle(ctx context.Context, event string, payload []byte, metadata map[string]string) (*sdk.HandleResult, error) {
    return p.impl.Handle(ctx, event, payload, metadata)
}

func (p *ExternalPlugin) Stop() error {
    _ = p.impl.Shutdown()
    if p.client != nil { p.client.Kill() }
    return nil
}
```

**插件开发示例**：
```go
package main

import (
    "context"
    "encoding/json"
    "github.com/hashicorp/go-plugin"
    "smart-nas/internal/plugin/goplugin/sdk"
)

type ImageThumbnailPlugin struct { config map[string]interface{} }

func (p *ImageThumbnailPlugin) Info() (*sdk.PluginInfo, error) {
    return &sdk.PluginInfo{
        ID: "image-thumbnail", Name: "图片缩略图生成", Version: "1.0.0",
        Events: []string{"file.after_upload"},
        Permissions: []string{"file_read", "file_write"},
    }, nil
}
func (p *ImageThumbnailPlugin) Init(configJSON string, dataDir string, hostVersion string) error {
    return json.Unmarshal([]byte(configJSON), &p.config)
}
func (p *ImageThumbnailPlugin) Handle(ctx context.Context, event string, payload []byte, metadata map[string]string) (*sdk.HandleResult, error) {
    // 处理事件...
    return &sdk.HandleResult{Success: true}, nil
}
func (p *ImageThumbnailPlugin) Shutdown() error { return nil }

func main() {
    plugin.Serve(&plugin.ServeConfig{
        HandshakeConfig: sdk.HandshakeConfig,
        Plugins: map[string]plugin.Plugin{
            "smartnas": &sdk.SmartNASPlugin{Impl: &ImageThumbnailPlugin{}},
        },
        GRPCServer: plugin.DefaultGRPCServer,
    })
}
```

#### 2.9.5 Hook 钩子系统

```go
// internal/plugin/hook/bus.go
type Handler func(ctx context.Context, event Event) (Result, error)

type Event struct {
    Name     string
    Payload  interface{}
    Metadata map[string]interface{}
}

type Result struct {
    StopPropagation bool
    Modified        map[string]interface{}
    Data            interface{}
}

type Bus struct {
    handlers map[string][]handlerEntry
    mu       sync.RWMutex
}

func (b *Bus) Register(event string, name string, priority int, h Handler) {
    // 按优先级排序存储
}
func (b *Bus) Emit(ctx context.Context, event Event) ([]Result, error) {
    // 按顺序执行所有钩子
}
```

**内置钩子示例**：
```go
hookBus.Register("file.before_upload", "virus-scan", 10, func(ctx context.Context, e hook.Event) (hook.Result, error) {
    // 病毒扫描逻辑
})
hookBus.Register("file.after_upload", "thumbnail", 100, func(ctx context.Context, e hook.Event) (hook.Result, error) {
    // 缩略图生成逻辑（异步）
})
```

#### 2.9.6 Goja 脚本插件

```go
// internal/plugin/script/runtime.go
type Runtime struct {
    vm       *goja.Runtime
    scripts  map[string]*Script
    mu       sync.Mutex
}

type Script struct {
    ID      string
    Name    string
    Code    string
    Enabled bool
    Event   string
}

func (r *Runtime) Execute(ctx context.Context, script *Script, eventData map[string]interface{}) (interface{}, error) {
    vm := goja.New()
    vm.Set("log", func(msg string) { log.Printf("[script:%s] %s", script.Name, msg) })
    vm.Set("getDevice", func(id string) map[string]interface{} { /* 受限API */ })
    vm.Set("controlDevice", func(id, action string, params map[string]interface{}) error { /* 设备控制 */ })
    vm.Set("sendNotification", func(title, content string) { /* 通知 */ })
    vm.Set("event", eventData)
    return vm.RunString(script.Code)
}
```

**用户脚本示例**：
```javascript
// 回家模式
if (event.device_id === "door-sensor-001" && event.status.open === true) {
    const hour = new Date().getHours();
    if (hour >= 18 || hour < 6) {
        controlDevice("light-living", "turn_on", {brightness: 80});
        controlDevice("ac-living", "turn_on", {temperature: 26});
        sendNotification("回家模式", "已为你打开客厅灯和空调");
    }
}
```

#### 2.9.7 插件清单与目录结构

```
plugins/
├── image-thumbnail/
│   ├── plugin.toml
│   ├── thumbnail.exe
│   ├── config.toml
│   └── data/
├── notification-dingtalk/
│   ├── plugin.toml
│   └── dingtalk.exe
└── scripts/
    ├── home-mode.js
    └── auto-backup.js
```

**plugin.toml**：
```toml
[plugin]
id = "image-thumbnail"
name = "图片缩略图生成"
version = "1.0.0"
type = "goplugin"
api_version = "v1"
entrypoint = "thumbnail.exe"
enabled = true

[permissions]
file_read = true
file_write = true
network = false

[events]
"file.after_upload" = 100

[config]
sizes = ["128x128", "256x256", "512x512"]
quality = 85
```

#### 2.9.8 插件管理器总控

```go
// internal/plugin/manager.go
type Manager struct {
    cfg             config.PluginConfig
    externalPlugins map[string]*goplugin.ExternalPlugin
    hookBus         *hook.Bus
    scriptRuntime   *script.Runtime
    mu              sync.RWMutex
    dataDir         string
}

func (m *Manager) LoadPlugins() error { /* 扫描目录，加载各类型插件 */ }
func (m *Manager) EmitEvent(ctx context.Context, eventName string, payload interface{}) error {
    // 按顺序触发 Hook → 并行调用 go-plugin → 脚本
}
func (m *Manager) Enable(id string) error  { ... }
func (m *Manager) Disable(id string) error { ... }
func (m *Manager) Reload(id string) error  { ... }
```

#### 2.9.9 插件安全机制

| 安全措施 | 说明 |
|---------|------|
| 权限声明 | 插件在 plugin.toml 中声明，运行时受限 |
| 进程隔离 | go-plugin 独立进程，崩溃不影响主程序 |
| 调用超时 | 默认 30 秒超时 |
| 资源限制 | 可配置 CPU/内存上限（cgroups） |
| 脚本沙箱 | Goja VM 无默认文件系统/网络访问，仅暴露白名单 API |
| 插件签名 | 官方市场提供签名校验 |
| 配置隔离 | 每个插件独立数据目录 |
| 审计日志 | 所有插件操作记录审计日志 |

---

## 3. 数据结构设计

### 3.1 数据库表结构（SQLite）

> **实现状态（v0.11）**：`file_metas` 与 `file_versions` 已按本设计落地为真正的
> SQLite 库（`modernc.org/sqlite` 纯 Go 驱动，默认 `./data/files/metadata.db`，
> WAL 模式，`parent_id/md5/deleted_at/share_token/file_id` 建索引；时间列以
> Unix 纳秒整数存储，NULL 表示零值；旧 TOML/JSON 元数据启动时自动迁移）。
> `users / plugins / user_scripts` 等仍以 TOML 键值存储实现（离线环境替代方案），
> 字段以代码实现为准。

```sql
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username VARCHAR(64) UNIQUE NOT NULL,
    password_hash VARCHAR(128) NOT NULL,
    password_salt VARCHAR(32) NOT NULL,
    role VARCHAR(20) DEFAULT 'user',
    storage_quota INTEGER DEFAULT 0,
    avatar VARCHAR(255),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE file_metas (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name VARCHAR(255) NOT NULL,
    parent_id INTEGER DEFAULT 0,
    is_dir BOOLEAN DEFAULT 0,
    size INTEGER DEFAULT 0,
    md5 VARCHAR(32),
    mime_type VARCHAR(100),
    storage_path VARCHAR(500),
    owner_id INTEGER NOT NULL,
    shared BOOLEAN DEFAULT 0,
    share_token VARCHAR(32),
    deleted_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (owner_id) REFERENCES users(id)
);
CREATE INDEX idx_file_parent ON file_metas(parent_id);
CREATE INDEX idx_file_md5 ON file_metas(md5);
CREATE INDEX idx_file_owner ON file_metas(owner_id);

CREATE TABLE file_versions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL,
    version INTEGER NOT NULL,
    md5 VARCHAR(32),
    storage_path VARCHAR(500),
    size INTEGER,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (file_id) REFERENCES file_metas(id)
);

CREATE TABLE conversations (
    id VARCHAR(36) PRIMARY KEY,
    user_id INTEGER NOT NULL,
    title VARCHAR(200),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE messages (
    id VARCHAR(36) PRIMARY KEY,
    conversation_id VARCHAR(36) NOT NULL,
    role VARCHAR(20) NOT NULL,
    content TEXT,
    tool_calls TEXT,
    tool_result TEXT,
    tokens INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id)
);

CREATE TABLE devices (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    type VARCHAR(30) NOT NULL,
    platform VARCHAR(20) NOT NULL,
    room VARCHAR(50),
    online BOOLEAN DEFAULT 0,
    raw_info TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE automation_rules (
    id VARCHAR(36) PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    enabled BOOLEAN DEFAULT 1,
    trigger TEXT NOT NULL,
    actions TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE mihome_credentials (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE plugins (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    version VARCHAR(20) NOT NULL,
    type VARCHAR(20) NOT NULL,
    author VARCHAR(100),
    description TEXT,
    enabled BOOLEAN DEFAULT 1,
    config TEXT,
    path VARCHAR(500),
    installed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE user_scripts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name VARCHAR(100) NOT NULL,
    event VARCHAR(100) NOT NULL,
    code TEXT NOT NULL,
    enabled BOOLEAN DEFAULT 1,
    user_id INTEGER,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### 3.2 向量库结构（chromem-go）

```
Collection: "file_chunks"
  Document fields:
    - id: "{file_id}:{chunk_index}"
    - content: 文本块内容
    - embedding: []float32
  Metadata:
    - file_id: uint
    - filename: string
    - chunk_index: int
    - mime_type: string
    - created_at: timestamp
```

---

## 4. API 接口设计

### 4.1 认证接口

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/auth/login` | 用户登录，返回 JWT |
| POST | `/api/auth/logout` | 登出 |
| GET | `/api/auth/me` | 获取当前用户信息 |
| POST | `/api/auth/change-password` | 修改密码 |

### 4.2 文件管理接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/files` | 列出文件 |
| GET | `/api/files/:id` | 获取文件详情（权限不足返回 403 及具体原因，v0.14 起） |
| POST | `/api/files/mkdir` | 创建目录 |
| POST | `/api/files/upload/init` | 初始化上传（秒传检测，返回 tus URL） |
| — | `/files/upload/*filepath` | tus 协议端点 |
| GET | `/api/files/:id/download` | 下载文件（支持 Range） |
| PUT | `/api/files/:id/rename` | 重命名 |
| POST | `/api/files/move` | 移动文件/目录（批量） |
| POST | `/api/files/copy` | **复制**文件/目录（批量，目录递归，v0.04 新增） |
| DELETE | `/api/files` | 删除文件（批量；权限不足提示具体目录与所需权限，v0.14 起） |
| GET | `/api/files/trash` | 回收站列表 |
| POST | `/api/files/restore` | 从回收站恢复（单个/批量） |
| POST | `/api/files/trash/purge` | 从回收站物理删除（不可恢复，批量，v0.10 新增） |
| POST | `/api/files/trash/clear` | 一键清空回收站（v0.10 新增） |
| POST | `/api/files/trash/restore-all` | 一键还原回收站全部条目（v0.10 新增） |
| POST | `/api/files/:id/share` | 创建共享链接 |
| GET | `/api/storage/stats` | 存储用量统计 |
| GET | `/api/system/status` | **系统状态主页数据**（所有登录用户，含磁盘/CPU/内存/连接数，主人/管理员可见在线用户，v0.03 新增） |

> 文件系统盘符自 v0.08 起支持**运行时同步**：每次列盘时自动纳入新接入磁盘、
> 隐藏已拔出磁盘（重新接入恢复原 ID），服务启动后插拔磁盘无需重启。
> 在线用户自 v0.10 起按**登录设备**展示：WebSocket 连接依据 User-Agent 判定
> 电脑/手机，`online_users` 返回每个用户的去重设备列表，前端以图标呈现。

### 4.3 AI 管家接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/ai/models` | 获取本地可用模型列表 |
| POST | `/api/ai/chat` | 非流式对话 |
| GET | `/api/ai/chat/stream` | 流式对话（WebSocket 或 SSE） |
| GET | `/api/ai/conversations` | 对话历史列表 |
| GET | `/api/ai/conversations/:id` | 对话详情 |
| DELETE | `/api/ai/conversations/:id` | 删除对话 |
| POST | `/api/ai/rag/index` | 手动触发文件索引重建 |
| GET | `/api/ai/rag/status` | 索引状态 |

### 4.4 IoT 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/iot/devices` | 设备列表 |
| GET | `/api/iot/devices/:id` | 设备详情 |
| GET | `/api/iot/devices/:id/status` | 设备实时状态 |
| POST | `/api/iot/devices/:id/execute` | 执行设备动作 |
| GET | `/api/iot/automations` | 自动化规则列表 |
| POST | `/api/iot/automations` | 创建自动化规则 |
| PUT | `/api/iot/automations/:id` | 更新规则 |
| DELETE | `/api/iot/automations/:id` | 删除规则 |
| POST | `/api/iot/automations/:id/toggle` | 启用/禁用规则 |
| GET | `/api/iot/mihome/auth-url` | 获取米家授权链接 |
| GET | `/api/iot/mihome/callback` | 米家 OAuth 回调 |
| POST | `/api/iot/mihome/sync` | 同步米家设备列表 |

### 4.5 管理接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/admin/users` | 用户列表 |
| POST | `/api/admin/users` | 创建用户 |
| PUT | `/api/admin/users/:id` | 更新用户 |
| PUT | `/api/admin/users/:id/permissions` | 配置用户目录权限（读/写，自动去重，v0.03 新增） |
| DELETE | `/api/admin/users/:id` | 删除用户 |
| GET | `/api/admin/system/status` | 系统状态（主人/管理员视角，含在线用户） |
| GET | `/api/admin/settings` | 获取系统设置（刷新频率 / 回收站位置 / 日志位置、最大大小与保留天数，v0.03 新增、v0.08 扩展） |
| PUT | `/api/admin/settings` | 更新系统设置（**仅主人可修改**，管理员/普通用户返回 403，v0.06 起；日志设置保存后立即生效，v0.08 起；回收站路径格式校验与归一化，v0.13 起） |
| GET | `/api/admin/metrics` | 指标快照（JSON，v0.01 新增） |
| GET | `/api/admin/config` | 获取配置 |
| PUT | `/api/admin/config` | 更新配置 |
| GET | `/api/admin/logs` | 查看日志 |

### 4.6 插件管理接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/plugins` | 已安装插件列表 |
| GET | `/api/plugins/:id` | 插件详情 |
| POST | `/api/plugins/:id/enable` | 启用插件 |
| POST | `/api/plugins/:id/disable` | 禁用插件 |
| POST | `/api/plugins/:id/config` | 更新插件配置 |
| DELETE | `/api/plugins/:id` | 卸载插件 |
| POST | `/api/plugins/upload` | 上传安装插件（zip 包） |
| GET | `/api/plugins/marketplace` | 插件市场列表（可选） |
| POST | `/api/plugins/:id/reload` | 重载插件 |

### 4.7 API 文档与测试策略
#### 4.7.1 自动生成文档（Swagger/OpenAPI）
集成 github.com/swaggo/swag 和 github.com/swaggo/gin-swagger ，通过注释生成 OpenAPI 3.0 文档。
示例注释：



``` go
// internal/server/router.go
// @title           Smart NAS API
// @version         1.0
// @description     智能家庭 NAS 系统 RESTful API
// @host            nas.local:8080
// @BasePath        /api
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
```

``` go
// internal/handler/file.go

// ListFiles godoc
// @Summary      列出文件列表
// @Description  获取指定目录下的文件和子目录
// @Tags         Files
// @Accept       json
// @Produce      json
// @Param        parent_id query int false "父目录 ID，0 表示根目录"
// @Param        keyword query string false "搜索关键词"
// @Success      200 {object} Response{data=[]FileMeta}
// @Failure      401 {object} Response
// @Router       /files [get]
// @Security     BearerAuth
func ListFiles(c *gin.Context) { ... }

```

访问路径：/swagger/index.html

#### 4.7.2 测试分层策略

| 测试层级   | 范围                          | 工具                                | 执行时机            |
| ------ | --------------------------- | --------------------------------- | --------------- |
| 单元测试   | 单个函数 / 方法                   | Go testing + testify              | make test（每次提交） |
| 集成测试   | 数据库、Ollama、MQTT 外部依赖        | testcontainers-go（模拟 Ollama/MQTT） | CI 流水线（PR 合并前）  |
| E2E 测试 | 完整 API 流程（登录→上传→AI 对话→设备控制） | go test + httptest                | 发布前手动触发         |
| 压力测试   | 并发上传、AI 批量请求                | vegeta / k6                       | 版本发布前基准测试       |
关键集成测试用例清单：

1. tus 断点续传（模拟网络中断后恢复）
2. 秒传去重（同一 MD5 文件重复上传）
3. AI Function Calling 工具链调用（Mock Ollama 返回 tool_calls）
4. 米家 Token 过期自动刷新
5. 插件崩溃后主进程不受影响（go-plugin 隔离验证）

### 4.8 视频在线播放接口（v0.16）

> 前置：服务器需安装 ffmpeg / ffprobe（`[play] ffmpeg_path / ffprobe_path`，默认从 PATH 查找）。
> 播放权限继承文件目录读权限；`/api/video` 由播放凭证（Ticket）认证（`<video>` 标签无法附带 JWT 头）。

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| GET | `/api/video/info?file=<id>` | JWT | 查询视频信息（分辨率 / 编码 / 是否可播 / 播放模式），供列表渲染与 2K 拦截 |
| POST | `/api/play/ticket` `{file_id}` | JWT | 创建播放凭证，返回 `{token, url, video}`；不可播（>2K 非 mp4 / 格式不支持 / 探测失败）返回 400 + 提示 |
| DELETE | `/api/play/ticket/:token` | JWT | 释放播放凭证（关闭播放器时调用，幂等） |
| GET | `/api/video?file=<id>&token=<t>` | Ticket | 输出视频流（HTTP Range → 206 + Content-Range）；凭证无效/过期/IP 不匹配 → 401 |

**播放模式决策（`internal/play`）**：

| 场景 | 模式 | 行为 |
|------|------|------|
| `.mp4` 且编码浏览器可播（h264/hevc/vp8/vp9/av1） | direct | 直接 Range 流（`http.ServeContent`，拖拽秒定位） |
| `.mkv` 等容器、编码可播 | remux | ffmpeg `-c copy` 转封装为 MP4（并发 ≤3），产物缓存 `data/cache/video/`，缓存后全量 Range |
| 编码浏览器不可播 | transcode | 实时转码 h264/aac（并发 ≤1、线程 2），流式输出，不保证 Range |
| 分辨率 > `max_online_height`（默认 1440=2K） | 拒绝 | 不转码/转封装；`.mp4` 允许直连（前端提示卡顿风险），其余返回 400/403 提示下载 |
| 格式不支持 / 探测失败 / 无 ffmpeg | 拒绝 | 返回提示，前端仅显示下载 |
| 音频（mp3/flac/wav/aac/ogg/m4a/opus/wma，v0.18） | direct | 直接 Range 流，无需 ffprobe 视频探测（无 ffmpeg 也可播），按扩展名返回 audio/* MIME |

**播放凭证（Ticket）生命周期**：创建 → 随播放链接使用（每次 Range 请求刷新活跃时间）→
30 分钟无请求自动失效（后台每分钟清扫）→ 关闭播放器显式释放；默认与创建时 IP 绑定（`[play] ip_bind`）。

**前端交互**：文件列表对视频文件异步探测分辨率（并发 ≤4）→ ≤2K 显示「播放」；>2K 显示
「高清」标签 + 下载；强行播放 >2K 时播放器内弹出非模态悬浮提示窗（继续播放 / 下载）。
音频文件（v0.18 起）同样显示「播放」按钮，点击文件名直接打开播放器。
播放器为右侧抽屉式 **Video.js**（v8.24，本地化于 `web/js/video-js-8.24.0/`，无需外网 CDN；
自带控制条：播放/暂停、进度拖拽、音量、全屏、网络中断重试），UI 与后台深色主题一致。
v0.19 起支持：键盘 ←/→ 快退/快进 5 秒、↑/↓ 音量、空格播放/暂停、F 全屏（抽屉打开时
全局生效）；手机水平滑动快进/快退；控制条倍速菜单（0.5~2 倍）。
前端资源目录化：`web/index.html`（结构）+ `web/css/style.css`（样式）+ `web/js/app.js`（逻辑）。

**FFmpeg 依赖打包集成（v0.19）**：`internal/play` 通过 `resolveToolPath` 自动探测
ffmpeg/ffprobe——优先使用可执行文件同目录 / 工作目录下的内置二进制（Windows 自动尝试
`.exe` 后缀），最后回退 `config.toml` 配置值（含 PATH 查找）。将 `ffmpeg.exe / ffprobe.exe`
与 `smart-nas.exe` 同目录放置即可随包分发，无需系统安装 FFmpeg；`ffmpegAvailable` 实际
校验二进制存在，缺失时正确返回 `ffmpeg_missing` 提示下载。

### 4.9 文件备份还原接口（v0.20）

> 权限：任务/历史/内容浏览对所有登录用户可见（只读）；创建/更新/删除任务、触发备份、
> 冻结、删除备份、还原为**主人/管理员**专属。操作日志独立记录 `data/logs/backup.log`。

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/api/backup/tasks` | 登录 | 任务列表 |
| POST | `/api/backup/tasks` | 主人/管理员 | 创建任务（`simple_period` 简单周期内部转 cron 存储） |
| PUT | `/api/backup/tasks/:id` | 主人/管理员 | 更新任务 |
| DELETE | `/api/backup/tasks/:id` | 主人/管理员 | 删除任务（历史保留） |
| POST | `/api/backup/tasks/:id/run` | 主人/管理员 | 手动触发备份 |
| GET | `/api/backup/tasks/:id/history` | 登录 | 备份历史（含冻结标记/状态/哈希/增量依赖） |
| GET | `/api/backup/backups/:id/contents` | 登录 | 浏览备份产物内文件列表（目录/zip 均支持） |
| POST | `/api/backup/backups/:id/restore` | 主人/管理员 | 还原（`{target_dir, conflict, files}`；conflict: ask/overwrite/skip/rename） |
| POST | `/api/backup/backups/:id/freeze` | 主人/管理员 | 冻结 / 解冻（`{frozen: bool}`） |
| DELETE | `/api/backup/backups/:id` | 主人/管理员 | 手动删除备份（物理删除产物，冻结的亦允许手动删） |
| GET | `/api/backup/usb-devices` | 登录 | 当前接入的 USB 可移动设备（串号\|卷标，绑定用） |

**模块设计（`internal/backup`，元数据 SQLite `data/backup/backup.db`，表 `backup_task` / `backup_history`）**：

- **热备份**：不强制锁文件；被占用/写入中的文件跳过并标记（状态 `partial`），不中断整个备份；
- **完整 / 增量**：首次完整；此后仅备份与父备份快照（相对路径+大小+修改时间）不同的文件；
  增量依赖父备份链，还原前校验整条链哈希（SHA-256），**链损坏拒绝还原**；
- **触发**：manual / timer（cron 或简单周期日/周/月）/ interval（小时）/ realtime（USB 绑定设备插入即触发）；
- **压缩**：可选 zip（级别 1-9）；产物目录/zip 命名 `backup_<taskID>_<时间戳>`；
- **生命周期**：数量/大小双阈值配额，完成后按最早优先清理非冻结备份；
  **冻结备份不计数、不占配额、自动清理绝不删除**（禁止行为硬约束）；
- **并发约束**：同一时间只运行一个备份任务；同一任务执行中重复触发直接跳过；
- **预检查**：源可读校验 + 备份磁盘剩余空间校验；中途失败丢弃不完整产物（`.bakpart` 临时文件）；
- **邮件通知**：任务结束后按 SMTP 配置（JSON）发送结果邮件（标准库 net/smtp）；
- **USB 监控**：Windows `GetDriveTypeW`（DRIVE_REMOVABLE）+ `GetVolumeInformationW`
  （串号/卷标）轮询；仅绑定设备触发，陌生 U 盘不误触发。

**配置**（`config.toml [backup]`，环境变量 `SMARTNAS_BACKUP__*` 可覆盖）：
`enabled / db_path / log_path / watch_interval / usb_poll_interval`。

**全局默认与备份类型（v0.20 扩展）**：
- 「管理 → 设置」新增 **备份默认存放目录** 与 **备份默认压缩级别**（zip 无损，默认 9 最高），
  新建任务自动套用（`GET /api/backup/defaults` 供表单预填），任务内可单独覆盖；
- 任务新增 `backup_type` 字段：`auto`（首次完整、后续增量，默认）/ `full`（每次完全备份），
  旧库启动时自动 `ALTER TABLE` 补列；执行时遵循任务选择。

---

## 5. 项目目录结构

```
smart-nas/
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   └── defaults.go
│   ├── server/
│   │   ├── server.go
│   │   ├── router.go
│   │   └── middleware/
│   │       ├── auth.go
│   │       ├── cors.go
│   │       └── logger.go
│   ├── auth/
│   │   ├── password.go
│   │   ├── jwt.go
│   │   └── service.go
│   ├── user/
│   │   ├── model.go
│   │   ├── repository.go
│   │   └── service.go
│   ├── storage/
│   │   ├── model.go
│   │   ├── repository.go
│   │   ├── service.go
│   │   └── version.go
│   ├── transport/
│   │   ├── manager.go
│   │   ├── download.go
│   │   └── tusd/
│   │       ├── server.go
│   │       └── hooks.go
│   ├── play/                    # 视频在线播放（v0.16）
│   │   ├── play.go              # 播放凭证 / ffprobe 分辨率缓存 / 模式决策
│   │   ├── stream.go            # Range 流 / 转封装 / 实时转码
│   │   └── play_test.go
│   ├── backup/                  # 备份还原（v0.20）
│   │   ├── model.go             # Task / History / 状态常量 / 校验
│   │   ├── repository.go        # SQLite 元数据（backup_task / backup_history）
│   │   ├── service.go           # 备份执行 / 调度 / USB 触发 / 生命周期
│   │   ├── copy.go              # 热拷贝 / 增量 / zip / 解压
│   │   ├── restore.go           # 还原（链校验 / 冲突策略）
│   │   ├── lifecycle.go         # 配额清理 / 冻结保护
│   │   ├── snapshot.go          # 快照与备份内容浏览
│   │   ├── usb.go               # USB 设备监控（Windows）
│   │   ├── simple.go            # 简单周期 → cron 转换
│   │   ├── notify.go            # SMTP 邮件通知
│   │   └── backup_test.go
│   ├── webdav/
│   │   ├── handler.go
│   │   └── filesystem.go
│   ├── plugin/
│   │   ├── manager.go
│   │   ├── goplugin/
│   │   │   ├── loader.go
│   │   │   └── sdk/
│   │   │       ├── interface.go
│   │   │       ├── plugin.go
│   │   │       └── proto/
│   │   ├── hook/
│   │   │   ├── bus.go
│   │   │   └── events.go
│   │   └── script/
│   │       └── runtime.go
│   ├── ai/
│   │   ├── ollama/
│   │   │   ├── client.go
│   │   │   └── types.go
│   │   ├── conversation/
│   │   │   ├── manager.go
│   │   │   └── model.go
│   │   ├── tools/
│   │   │   ├── registry.go
│   │   │   ├── file_tools.go
│   │   │   ├── device_tools.go
│   │   │   └── system_tools.go
│   │   ├── rag/
│   │   │   ├── indexer.go
│   │   │   ├── retriever.go
│   │   │   └── extractor.go
│   │   └── service.go
│   ├── iot/
│   │   ├── device/
│   │   │   ├── device.go
│   │   │   └── registry.go
│   │   ├── mihome/
│   │   │   ├── client.go
│   │   │   ├── auth.go
│   │   │   └── device.go
│   │   ├── mqtt/
│   │   │   ├── manager.go
│   │   │   └── device.go
│   │   ├── automation/
│   │   │   ├── engine.go
│   │   │   └── model.go
│   │   └── service.go
│   ├── ws/
│   │   ├── hub.go
│   │   └── client.go
│   ├── task/
│   │   ├── scheduler.go
│   │   └── worker.go
│   └── util/
│       ├── system.go
│       ├── net.go
│       ├── hash.go
│       └── id.go
├── pkg/
│   └── logger/
├── web/
│   ├── index.html          # 实际实现：原生单页入口（v0.17 起资源目录化）
│   ├── css/style.css       # 样式
│   ├── js/app.js           # 逻辑
│   ├── js/video-js-8.24.0/ # Video.js 本地化（离线可用）
│   ├── src/                # （设计规划：Vue 工程，可替换原生实现）
│   │   ├── views/
│   │   ├── components/
│   │   ├── stores/
│   │   ├── api/
│   │   └── router/
│   └── package.json
├── data/
│   ├── files/
│   ├── db/
│   ├── logs/
│   └── vectors/
├── config.toml
├── docker-compose.yml
├── Dockerfile
├── go.mod
├── go.sum
└── README.md
```

---

## 6. 关键流程设计

### 6.1 系统启动流程

```
main.go
  │
  ├─ 1. 加载配置 (config.NewManager)
  ├─ 2. 初始化日志 (logger.Init)
  ├─ 3. 连接数据库 (gorm.Open SQLite, AutoMigrate)
  ├─ 4. 初始化插件管理器 (plugin.Manager.LoadPlugins)
  ├─ 5. 初始化各模块:
  │     ├─ user.Service
  │     ├─ storage.Service
  │     ├─ transport.Manager (tus 可恢复上传)
  │     ├─ backup.Service（v0.20：加载备份任务 / cron / USB 监控 / backup.log）
  │     ├─ ai.Service (连接 Ollama, 检查模型)
  │     ├─ iot.Service (连接 MQTT, 加载米家凭证)
  │     └─ automation.Engine (启动 cron)
  ├─ 6. 启动 WebSocket Hub
  ├─ 7. 注册路由 (Gin, 含 WebDAV + 插件管理 API)
  ├─ 8. 触发 system.startup 事件（通知所有插件）
  ├─ 9. 启动异步任务 Worker
  └─ 10. 启动 HTTP Server (优雅退出: signal.Notify)
```

### 6.2 AI 对话完整流程

```
用户发送消息 (WebSocket)
    │
    ▼
AI Service.ReceiveMessage()
    │
    ├─ 1. 保存用户消息到 conversation
    ├─ 2. 判断是否需要 RAG:
    │     └─ 是 → retriever.Search(query) → 注入上下文
    ├─ 3. 构造 Ollama ChatRequest (含 tools 定义)
    ├─ 4. 调用 ollama.ChatStream()
    │     │
    │     ├─ 流式接收 chunk → WebSocket 推送给前端
    │     │
    │     └─ 收到 tool_calls:
    │           ├─ 解析工具名和参数
    │           ├─ tools.Registry.Execute() 并行执行
    │           ├─ 工具结果作为 role=tool 消息追加
    │           └─ 再次调用 ollama.ChatStream() 生成最终回复
    │
    └─ 5. 保存 AI 回复到 conversation
```

### 6.3 米家设备控制流程

```
用户/AI 调用 control_device(device_id, action, params)
    │
    ▼
device.Registry.Execute()
    │
    ├─ 查找设备 (platform=mihome)
    ├─ 检查设备在线状态
    ├─ mihome.Client.SetDeviceProperties() 或 Action()
    │     ├─ 检查 access_token 是否过期
    │     │   └─ 过期 → RefreshToken()
    │     ├─ 构造 MIoT 签名请求
    │     └─ HTTP POST 到 api.io.mi.com
    ├─ 更新本地设备状态缓存
    └─ WebSocket 广播设备状态变更
```

### 6.4 典型故障场景处理与降级策略

|故障场景|检测方式|系统行为|用户感知|
|---|---|---|---|
|Ollama 服务不可用|健康检查（/api/tags）连续失败 3 次|AI 功能进入降级模式，返回 "AI 服务暂时不可用，请稍后再试"；RAG 索引暂停|无法使用 AI 对话，其他功能正常|
|Ollama 推理超时|上下文超时（30s）|终止请求，记录日志；返回 "推理超时，请简化问题或检查模型负载"|对话中断，可重试|
|MQTT Broker 断线|paho.mqtt 连接丢失回调|自动启用指数退避重连（1s、2s、4s、8s...）；期间设备状态显示 "未知"|IoT 设备状态不可用，但米家云 API 设备仍可控制|
|tus 临时文件堆积|定时任务扫描 data/uploads/ 中超过 24h 未更新的文件|自动清理过期临时分块|无感知（后台清理）|
|磁盘空间不足|监控告警（使用率 > 85%）|禁止新文件上传；返回错误码 2002；清理回收站过期文件释放空间|上传失败，提示 "存储空间不足，请清理文件"|
|插件进程崩溃|go-plugin 自动检测子进程退出|框架自动重启插件；记录崩溃日志；如果连续重启失败 3 次，禁用该插件|插件功能临时不可用，主系统不受影响|
|米家 Token 过期|API 返回 401 或错误码|自动调用 RefreshToken；若刷新失败，标记设备离线|设备控制失败，需用户重新授权|
|SQLite 数据库锁冲突|GORM 返回 database is locked|自动重试（最多 3 次，间隔 100ms）；使用 WAL 模式减少锁竞争|偶发操作延迟，不影响整体|

> **降级开关配置**：已整合到主配置文件 `config.toml` 的 `[degradation]` 段，支持热更新。当检测到 CPU 高负载时，可自动禁用向量检索，仅保留基础对话。
---

## 7. 部署方案

### 7.1 Docker Compose 一键部署


```yaml
docker-compose.yml
version: "3.8"

services:
  nas:
    build: .
    container_name: smart-nas
    ports:
      "8080:8080"
    volumes:
      ./data:/app/data
      ./plugins:/app/plugins
      ./config.toml:/app/config.toml
    environment:
      TZ=Asia/Shanghai
    depends_on:
      ollama
      mosquitto
    restart: unless-stopped
    networks:
      nas-net

  ollama:
    image: ollama/ollama:latest
    container_name: ollama
    ports:
      "11434:11434"
    volumes:
      ./data/ollama:/root/.ollama
    deploy:
      resources:
        reservations:
          devices:
            driver: nvidia
              count: all
              capabilities: [gpu]
    restart: unless-stopped
    networks:
      nas-net

  mosquitto:
    image: eclipse-mosquitto:2
    container_name: mosquitto
    ports:
      "1883:1883"
    volumes:
      ./data/mosquitto/config:/mosquitto/config
      ./data/mosquitto/data:/mosquitto/data
    restart: unless-stopped
    networks:
      nas-net

networks:
  nas-net:
    driver: bridge
```


### 7.2 首次初始化步骤

1. 启动所有服务：`docker-compose up -d`
2. 拉取 Ollama 模型：
   `docker exec -it ollama ollama pull qwen2:7b`
   `docker exec -it ollama ollama pull nomic-embed-text`
   
3. 访问 NAS Web UI `http://<NAS-IP>:8080`
4. 使用默认管理员账号登录（首次启动自动创建，密码在日志中输出）
5. 进入系统设置 → 确认存储路径、AI 模型参数、插件目录
6. 进入插件管理 → 浏览/安装需要的插件
7. 进入智能家居 → 绑定米家账号 → 同步设备
8. 进入 AI 管家 → 开始对话

### 7.3 硬件最低要求

| 组件 | 最低配置 | 推荐配置 |
|------|---------|---------|
| CPU | x86_64 双核 | 四核及以上 |
| 内存 | 4GB | 8GB+（AI 模型需 8GB+） |
| 存储 | 系统盘 20GB + 数据盘 | SSD 系统盘 + HDD 数据盘 |
| 网络 | 千兆局域网 | 2.5Gbps |
| GPU（可选） | — | NVIDIA 显卡（加速 AI 推理） |


### 7.4 数据备份与灾难恢复

#### 7.4.1 备份范围与策略
| 数据类型            | 存储位置                   | 备份方式                               | 备份频率    | 保留周期        |
| --------------- | ---------------------- | ---------------------------------- | ------- | ----------- |
| 元数据库（SQLite）    | data/db/smart-nas.db   | 使用 sqlite3 .backup 或直接复制（需 WAL 模式） | 每日全量    | 30 天        |
| 文件 Blob         | data/files/            | rclone 增量同步至远程存储（S3/WebDAV/SFTP）   | 每日增量    | 永久（按远程存储策略） |
| 向量库（chromem-go） | data/vectors/          | 目录打包 + rclone 同步                   | 每周全量    | 30 天        |
| 插件与配置           | plugins/ + config.toml | 直接复制                               | 每次修改后自动 | 永久          |
| 系统日志            | data/logs/             | 可选备份                               | 不强制     | 7 天         |


#### 7.4.2 备份脚本示例（rclone + cron）
```bash
#!/bin/bash
# scripts/backup.sh
BACKUP_ROOT="/mnt/backup/nas"
DATE=$(date +%Y%m%d)

# 1. SQLite 热备（WAL 模式安全复制）
sqlite3 /app/data/db/smart-nas.db ".backup ${BACKUP_ROOT}/db/smart-nas-${DATE}.db"

# 2. 向量库打包
tar -czf ${BACKUP_ROOT}/vectors/vectors-${DATE}.tar.gz -C /app/data vectors/

# 3. 配置文件与插件清单
cp /app/config.toml ${BACKUP_ROOT}/config/config-${DATE}.toml
cp -r /app/plugins ${BACKUP_ROOT}/plugins-${DATE}

# 4. 使用 rclone 同步到远程（如 Backblaze B2）
rclone sync ${BACKUP_ROOT} backup:bucket/nas-backup/ --progress
```

#### 7.4.3 恢复流程（RTO < 1 小时，RPO < 24 小时）
**全量恢复步骤：**
1. 停止服务：docker-compose down
2. 恢复配置文件：cp /mnt/backup/config/config-latest.toml /app/config.toml
3. 恢复元数据库：cp /mnt/backup/db/smart-nas-latest.db /app/data/db/smart-nas.db
4. 恢复文件数据：rclone sync backup:bucket/nas-backup/files/ /app/data/files/
5. 恢复向量库：tar -xzf /mnt/backup/vectors/vectors-latest.tar.gz -C /app/data/
6. 启动服务：docker-compose up -d

**文件级恢复：** 用户可通过 Web UI 回收站恢复误删文件；管理员可通过 rclone 从备份中拉取特定文件。

### 7.5 版本升级与回滚策略

#### 7.5.1 数据库迁移策略

使用 GORM AutoMigrate，遵循以下原则：

- 仅新增字段：AutoMigrate 自动添加，无需人工干预。
- 字段重命名 / 删除：手动编写迁移脚本（migrations/ 目录），按版本号顺序执行。
- 数据迁移：涉及数据转换的，编写 Go 迁移函数，在主程序启动前执行。

```go

// internal/migrate/migrate.go
func RunMigrations(db *gorm.DB) error {
    // 自动迁移（安全字段）
    db.AutoMigrate(&User{}, &FileMeta{}, &Conversation{})
    
    // 版本化手动迁移
    var version int
    db.Raw("PRAGMA user_version").Scan(&version)
    if version < 1 {
        // 执行 v1 迁移：添加 storage_quota 默认值
        db.Exec("ALTER TABLE users ADD COLUMN storage_quota INTEGER DEFAULT 0")
        db.Exec("PRAGMA user_version = 1")
    }
    if version < 2 {
        // 执行 v2 迁移：为 file_metas 添加 md5 索引
        db.Exec("CREATE INDEX IF NOT EXISTS idx_file_md5 ON file_metas(md5)")
        db.Exec("PRAGMA user_version = 2")
    }
    return nil
}
```

#### 7.5.2 插件 API 版本兼容

主程序与插件通过 HandshakeConfig.ProtocolVersion 协商（见 2.9.4 节）。

主程序升级时，若 ProtocolVersion 不变，则旧插件无需重新编译；若变更，主程序启动时拒绝加载低版本插件，并在 UI 提示 "插件需要更新"。

插件 SDK 本身支持语义化版本（APIVersion 字段），主程序可据此选择加载策略。

#### 7.5.3 升级与回滚操作步骤

升级步骤（Docker Compose）：
```bash
# 1. 备份数据（参见 7.4）
./scripts/backup.sh

# 2. 拉取新镜像
docker-compose pull

# 3. 重新创建容器（保留数据卷）
docker-compose up -d --force-recreate --no-deps nas

# 4. 查看启动日志，确认迁移成功
docker-compose logs -f nas | grep "migration"

# 5. 验证核心功能（登录、文件列表、AI 对话）
curl -f http://localhost:8080/api/auth/me -H "Authorization: Bearer ..."
```

回滚步骤：
```bash
# 1. 停止当前容器
docker-compose stop nas

# 2. 回退镜像标签（假设前一版本为 v1.2.0）
docker tag your-registry/smart-nas:v1.2.0 your-registry/smart-nas:latest

# 3. 重新启动
docker-compose up -d nas

# 4. 若数据库迁移导致不兼容，执行数据恢复（见 7.4.3）
```
### 7.6 本地开发环境快速搭建

#### 7.6.1 使用 Air 实现热重载

安装 air：
```bash
go install github.com/cosmtrek/air@latest
```

配置文件 `.air.toml` 监听 internal/ 和 cmd/ 目录变化，自动重启。

#### 7.6.2 Mock 外部依赖（便于脱离 Docker 开发）

Mock Ollama：提供 --mock-ai 启动参数，返回预定义响应，无需安装真实模型。
```go
// cmd/server/main.go
if *mockAI {
    aiService = mock.NewMockAIService()
}
```

Mock MQTT：使用内存事件总线模拟设备状态变更，无需启动 Mosquitto。

#### 7.6.3 调试工具

- pprof：启用 net/http/pprof，分析 CPU 和内存占用。
- delve：使用 dlv debug 进行断点调试。
- 前端 DevTools：Vue Devtools + 浏览器 Network 面板查看 tus 分块请求。

#### 7.6.4 一键启动开发环境（Makefile）

```makefile
.PHONY: dev
dev:
	docker-compose -f docker-compose.dev.yml up -d ollama mosquitto
	air -c .air.toml

.PHONY: test
test:
	go test -v -race -cover ./...

.PHONY: build
build:
	cd web && npm run build
	go build -o bin/smart-nas cmd/server/main.go
```

--- 
## 8. 安全设计

| 安全维度 | 方案 |
|---------|------|
| 密码存储 | Argon2id + 随机盐 |
| 传输加密 | HTTPS (TLS 1.3) + WSS |
| 身份认证 | JWT (HS256) + Refresh Token |
| 权限控制 | RBAC（admin/user）+ 资源级 ACL |
| API 安全 | 限流 + 参数校验 + SQL 注入防护（GORM） |
| 文件安全 | 上传类型白名单 + 分块校验 + 病毒扫描（可选插件） |
| 插件安全 | 权限沙箱 + 调用超时 + 进程隔离 + 签名校验 |
| IoT 凭证 | AES-256 加密存储米家 Token |
| 日志审计 | 所有管理操作记录审计日志 |
| 数据备份 | 定时快照 + 异地备份（rclone） |

---
## 9. 可观测性与监控告警
### 9.1 指标采集（Prometheus 集成）
使用 github.com/prometheus/client_golang 暴露 /metrics 端点，采集以下四类黄金指标：

接入方式：
```go
// internal/server/metrics.go
import "github.com/prometheus/client_golang/prometheus"

var (
    // HTTP 请求指标
    httpRequestsTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "http_requests_total", Help: "Total HTTP requests"},
        []string{"method", "endpoint", "status"},
    )
    httpRequestDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "HTTP request latency"},
        []string{"method", "endpoint"},
    )
    // AI 推理指标
    aiInferenceDuration = prometheus.NewHistogram(
        prometheus.HistogramOpts{Name: "ai_inference_duration_seconds", Help: "AI inference latency", Buckets: []float64{1, 2, 5, 10, 30}},
    )
    // 设备状态指标
    deviceOnlineStatus = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{Name: "device_online_status", Help: "Device online (1) / offline (0)"},
        []string{"platform", "device_id"},
    )
    // 存储指标
    storageUsageBytes = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{Name: "storage_usage_bytes", Help: "Storage usage per user"},
        []string{"user_id"},
    )
    // 视频播放指标（v0.16）
    playRemuxActive = prometheus.NewGauge(
        prometheus.GaugeOpts{Name: "nas_play_remux_active", Help: "Active video remux tasks"},
    )
    playTranscodeActive = prometheus.NewGauge(
        prometheus.GaugeOpts{Name: "nas_play_transcode_active", Help: "Active video transcode tasks"},
    )
    playTickets = prometheus.NewGauge(
        prometheus.GaugeOpts{Name: "nas_play_tickets", Help: "Active playback tickets"},
    )
)
```


### 9.2 告警规则（示例）
| 告警名称 | 表达式 | 严重级别 | 通知方式 |
| ---- | ---- | ---- | ---- |
| 存储空间不足 | storage_usage_bytes / storage_total_bytes > 0.85 | Warning | 系统通知 + 邮件 |
| 存储空间严重不足 | storage_usage_bytes / storage_total_bytes > 0.95 | Critical | 系统通知 + 邮件 + 手机推送 |
| Ollama 服务失联 | up{job="ollama"} == 0 | Critical | 系统通知（降级 AI 功能） |
| MQTT 断线 | mqtt_connected == 0 | Warning | 系统通知（IoT 功能降级） |
| API 错误率过高 | rate(http_requests_total{status=~"5.."}[5m]) > 0.05 | Warning | 管理员邮件 |
| AI 推理超时率过高 | ai_inference_duration_seconds > 30 占比 > 10% | Warning | 建议更换量化模型或加装 GPU |
| 插件崩溃重启 | plugin_restart_total > 3（15分钟内） | Warning | 日志审计 |

### 9.3 日志规范与分布式追踪
### 日志级别定义
- DEBUG：开发调试信息（如 SQL 语句、HTTP 请求体）
- INFO：关键业务流程（用户登录、文件上传完成、设备控制成功）
- WARN：非预期但可恢复的情况（Token 刷新失败后重试、插件调用超时降级）
- ERROR：需要关注的错误（数据库连接失败、米家 API 返回错误）
- FATAL：导致进程退出的严重错误（端口冲突、配置加载失败）

### 请求追踪（Trace ID）
在网关层中间件生成或提取 X-Request-ID，贯穿整个调用链：
```go
// internal/server/middleware/trace.go
func TraceMiddleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        traceID := c.GetHeader("X-Request-ID")
        if traceID == "" {
            traceID = uuid.New().String()
        }
        c.Set("trace_id", traceID)
        c.Header("X-Request-ID", traceID)
        // 注入到 context 中，供所有下层调用使用
        ctx := context.WithValue(c.Request.Context(), "trace_id", traceID)
        c.Request = c.Request.WithContext(ctx)
        c.Next()
    }
}
```
所有日志条目必须包含 trace_id 字段，便于问题定位。
## 附录 A：Go 模块依赖（go.mod 关键依赖）

```
module github.com/yourname/smart-nas

go 1.22

require (
    github.com/gin-gonic/gin v1.10.0
    github.com/coder/websocket v1.8.12
    gorm.io/gorm v1.25.10
    gorm.io/driver/sqlite v1.5.6
    github.com/spf13/viper v1.19.0
    github.com/golang-jwt/jwt/v5 v5.2.1
    golang.org/x/crypto v0.25.0
    go.uber.org/zap v1.27.0
    gopkg.in/natefinch/lumberjack.v2 v2.2.1
    github.com/robfig/cron/v3 v3.0.1
    github.com/eclipse/paho.mqtt.golang v1.5.0
    github.com/philippgille/chromem-go v0.7.0
    github.com/tus/tusd/v2 v2.0.0
    github.com/hashicorp/go-plugin v1.6.0
    google.golang.org/grpc v1.65.0
    google.golang.org/protobuf v1.34.2
    github.com/dop251/goja v0.0.0-2024061112313c-dcbbb2c88c3a
    github.com/studio-b12/gowebdav v0.9.0
    github.com/google/uuid v1.6.0
    github.com/shirou/gopsutil/v3 v3.24.5
)
```

## 附录 B：AI 模型推荐

| 模型 | 大小 | 用途 | 内存需求 |
|------|------|------|---------|
| `qwen2:7b-instruct` | 4.7GB (Q4) | 通用对话、工具调用 | 8GB |
| `qwen2:14b-instruct` | 9GB (Q4) | 复杂推理、长文档理解 | 16GB |
| `llama3.1:8b-instruct` | 4.9GB (Q4) | 英文为主的对话 | 8GB |
| `nomic-embed-text` | 274MB | 文本向量化（RAG） | 1GB |
| `mxbai-embed-large` | 670MB | 高质量中文向量化 | 2GB |

---

## 附录 C：版本变更记录

### v0.20（2026-09-01）文件备份还原模块
- **任务化备份（新模块 `internal/backup`）**：以任务形式组织备份（多源 + 排除规则），
  元数据存 SQLite（`backup_task` / `backup_history` 两表，索引 task_id / is_frozen / start_time）。
- **热备份**：不强制锁文件；被占用/写入中的文件跳过并标记（状态 `partial` 部分成功），不中断整个备份。
- **完整 / 增量备份**：首次完整，此后基于父备份快照（相对路径+大小+修改时间）增量；
  增量依赖父备份链（无完整备份禁止增量）；还原前校验整条链 SHA-256 完整性，**链损坏拒绝还原**。
- **触发方式**：手动 / 定时（cron 或简单周期日/周/月，内部转 cron 存储）/ 间隔（小时）/
  USB 实时（绑定设备串号|卷标插入即触发，**陌生 U 盘不误触发**）。
- **压缩**：可选 zip（级别 1-9），产物 `backup_<taskID>_<时间戳>`（纳秒后缀防同秒覆盖）。
- **生命周期**：数量/大小双阈值配额、最早优先自动清理；**冻结备份**不计数、不占配额、
  自动清理绝不删除（仅允许手动删除）——与需求文档禁止行为一致。
- **还原**：原位置或指定位置；覆盖策略 询问/覆盖/跳过/重命名旧文件（`*.old-时间戳`）；
  支持浏览备份内文件列表与部分还原；还原后做文件校验。
- **预检查与容错**：源可读 + 备份磁盘剩余空间预检查；同一时间只运行一个备份任务、
  同任务重复触发跳过；中途失败丢弃不完整产物并记录独立 `backup.log`。
- **邮件通知**：任务结束后按 SMTP 配置（JSON）发送结果邮件（标准库 net/smtp，失败仅日志）。
- **前端**：新增「备份」页签（任务列表/创建编辑弹窗/历史/内容浏览/还原/冻结/删除），
  权限与整体一致（写操作仅主人/管理员）。
- **配置**：新增 `[backup]` 段（enabled/db_path/log_path/watch_interval/usb_poll_interval）。
- **测试**：新增 `internal/backup` 单元与集成测试 20 例（排除规则/增量拷贝/zip 往返/
  简单周期转 cron/USB 匹配/SQLite 往返/端到端 完整→增量→链校验→冻结保护→清理/
  还原冲突策略/热备份跳过/预检查/执行中跳过/SMTP 解析）、`internal/server` 备份接口
  链路测试 5 例（未认证 401/普通用户 403/创建→触发→历史→内容→还原→冻结→删除全链路/
  链损坏 400/USB 设备列表）；`go build ./...` / `go vet ./...` / `go test ./...` 全部通过。

### v0.19（2026-08-24）FFmpeg 打包集成 + 播放器控制增强
- **go-astiav 替代 FFmpeg 可行性分析（结论：不可行）**：go-astiav 是 FFmpeg C 库的 cgo 绑定，运行时仍需 FFmpeg 动态库（libavformat-*.dll 等），并未消除对 FFmpeg 的依赖；构建期需 FFmpeg 开发库 + cgo，破坏本项目纯 Go + vendor 离线构建；且本环境无外网无法拉取依赖。详见《问题汇总》v0.19。
- **内置 FFmpeg 二进制随包分发 + 自动探测**：新增 `resolveToolPath`——优先使用可执行文件同目录 / 工作目录下的 ffmpeg/ffprobe（Windows 自动尝试 `.exe`），最后回退 `config.toml` 配置值（含 PATH）。用户将 `ffmpeg.exe / ffprobe.exe` 与 `smart-nas.exe` 同目录放置即可，无需系统安装 FFmpeg。
- **ffmpeg 可用性实际校验**：`ffmpegAvailable` 改为 `toolExists` 实际校验二进制存在（绝对/相对路径查文件、裸名称经 `exec.LookPath`），缺失时正确返回 `ffmpeg_missing` 提示下载。
- **播放器控制增强**：①键盘 ←/→ 快退/快进 5 秒、↑/↓ 音量、空格播放/暂停、F 全屏（抽屉打开时全局生效，输入框不拦截）；②手机水平滑动快进/快退（按滑动距离占视频宽度比例）；③控制条新增倍速菜单（0.5~2 倍，`playbackRateMenuButton`）。
- **文件详情显示真实路径**：新增 `FileMeta.PublicDetail`（保留文件路径，仅用于已通过读权限校验的详情接口），前端 `showDetail` 从 `GET /api/files/:id` 获取真实路径展示。
- **测试**：新增 `TestResolveToolPath` / `TestToolExists` / `TestPublicDetail`；`go build` / `go vet` / `go test ./...` / `node --check web/js/app.js` 全部通过。

### v0.18（2026-08-23）视频播放修复
- **修复 MP4 无法播放**：点击文件名走 `previewFile` 时视频文件落入“不支持预览”分支，未接入播放器；现视频文件点击文件名直接交给右侧抽屉播放器在线播放。
- **预览弹窗优化**：无法内联预览的文件弹窗标题由“预览 - 文件名”改为固定的“详细信息”，并移除底部重复的“关闭”按钮（头部已有 ✕）。
- **详细信息弹窗按钮**：「详情」按钮弹出的“详细信息”窗口底部移除“关闭”按钮，改为「下载」按钮；视频文件额外提供「播放」按钮（点击关闭弹窗并打开抽屉播放器）。
- **音频在线播放（新增）**：音频文件（mp3/flac/wav/aac/ogg/m4a/opus/wma）同样支持在线播放——文件列表与详情弹窗显示「播放」按钮，点击文件名直接打开抽屉播放器；后端对音频直接 Range 流（无需 ffprobe 视频探测，无 ffmpeg 也可播），按扩展名返回正确 MIME 类型（audio/mpeg 等）。
- **确认视频分辨率后端代码已存在**：后端 `internal/play` 已使用 ffprobe 检测视频分辨率（`runProbe` 调用 `ffprobe -select_streams v:0 -show_entries stream=width,height,codec_name,duration -of json`，结果按 路径+大小+修改时间 LRU 缓存）。
- **审查修复**：①不可播原因提示按后端 `reason` 正确映射（`probe_failed` / `ffmpeg_missing` 不再误提示“视频文件已移动或删除”），前端新增 `videoRefuseText` 与后端 `reasonText` 一致；②>2K 视频一律隐藏「播放」按钮（仅保留「高清」标签 + 下载），点击文件名仍可触发播放器内 2K 拦截悬浮提示窗。
- **测试**：`go build` / `go vet` / `go test ./...` / `node --check web/js/app.js` 全部通过。

### v0.17（2026-08-23）Video.js 播放器 + 前端资源目录化
- **播放器升级为 Video.js**：抽屉播放器改用 Video.js 8.24.0（本地化于 `web/js/video-js-8.24.0/`，离线可用），自带播放/暂停、进度拖拽、时间、音量、全屏控制条；`videojs()` 初始化 + `player.src()` 加载，错误时隐藏 Video.js 默认错误显示并启用自定义「重试」错误层。
- **前端资源目录化**：`web/app.js` → `web/js/app.js`，`web/style.css` → `web/css/style.css`；index.html 引入 `js/video-js-8.24.0/video-js.min.css` + `video.min.js`；后端 `staticFallback` 按真实路径服务 `/css/*`、`/js/*`，无需改动。
- **验证**：清除全部旧自定义控制条残留；`go build` / `go test ./internal/...` / `go vet` / `node --check` 全部通过。

### v0.16（2026-08-23）视频在线播放
- **视频在线播放**：文件列表对视频文件异步探测分辨率（ffprobe）后差异化展示——≤2K 显示「播放」；>2K 显示「高清」标签 + 下载（不转码/转封装）；分辨率异常按"暂不支持在线播放"引导下载。
- **播放模式**：mp4 直接 HTTP Range 流（206 + Content-Range）；mkv 等容器 ffmpeg `-c copy` 转封装（并发 ≤3，产物缓存 `data/cache/video/`）；浏览器不可播编码实时转码（并发 ≤1、线程 2）；>2K 仅 mp4 允许直连（前端提示卡顿风险），其余拒绝。
- **播放凭证（Ticket）**：`POST /api/play/ticket` 创建 → `/api/video?file=&token=` 使用 → `DELETE` 释放；30 分钟无 Range 请求自动失效；默认 IP 绑定防盗链。
- **抽屉播放器**：右侧抽屉式播放器（v0.16 为原生 HTML5 + 自定义控制条，v0.17 升级为 Video.js），2K 拦截非模态悬浮提示窗（继续播放/下载）；UI 与后台主题一致。
- **配置与指标**：新增 `[play]` 配置段（环境变量 `SMARTNAS_PLAY__*` 可覆盖）与 `nas_play_remux_active / nas_play_transcode_active / nas_play_tickets` 指标。
- **测试**：`internal/play`（播放模式决策 14 例 + 2K 边界）、`internal/server`（播放路由认证链路 / 无 ffprobe 拒绝凭证）；`go test ./...` 全部通过。

### v0.06（2026-08-22）
- **修复文件管理空白**：前端 `refresh()` 引用了不存在的 `disk-tip` 元素导致 TypeError 中断请求，
  已补回该元素并做判空防御，文件页恢复正常显示磁盘列表。
- **设置权限收紧**：`PUT /api/admin/settings` 仅主人可修改（后端 403 校验 + 前端只读置灰）；
  管理员/普通用户打开设置页为只读查看。
- **目录选择图标**：回收站位置选择按钮与目录选择器改用项目风格 SVG 文件夹图标，保留悬浮提示。
- **测试补齐**：新增 settings / user / storage / server 四个包的单元与集成测试，`go test ./...` 全部通过。

### v0.04（2026-08-22）
- **角色体系**：引入 master/admin/user 三角色。默认用户（`[auth] admin_username`）为主人（master），
  密码/角色仅可通过配置文件修改；主人可管理所有用户，管理员只能管理普通用户；
  认证中间件采用数据库实时角色，角色变更立即生效（无需重新登录）。
- **文件系统（仿 Nas-Cab）**：多选 + 批量复制/移动/删除；目标目录选择器；列排序；图片/文本预览；
  磁盘根节点仅可进入不可操作（前后端双重防护）。
- **复制接口**：新增 `POST /api/files/copy`（支持文件与递归目录复制，带权限校验）。
- **系统设置**：新增 `settings` 模块与 `GET/PUT /api/admin/settings`，可在界面调整
  CPU/内存刷新频率、磁盘刷新频率、回收站位置。
- **系统状态主页**：合并 CPU/内存/磁盘/在线连接于首页；点击连接数查看在线用户及角色。
- **权限去重**：`normalizePermissions` 剔除已被父目录完整权限覆盖的子目录条目。

### v0.03 → v0.02（2026-08-21）
- 磁盘化存储（`storage.disks`，留空自动发现 Windows 盘符）、真实文件系统同步；
- 配额机制替换为目录权限（读/写）；
- 统一弹窗 UI；主人账号提示移至 README；回收站位置可选。

