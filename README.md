
# 智能家庭 NAS（smart-nas）Windows 本地运行指南

基于《基于 Ollama + Go 的智能家庭 NAS 系统开发文档v0.1》实现。
本文档面向 **Windows 本地环境**，从环境准备、构建、启动到功能验证，
逐步说明如何完整运行本系统。

---

## 1. 环境准备

### 1.1 安装 Go

- 下载 Go 安装包：https://go.dev/dl/ （推荐 **1.25 或更高**，`go.mod` 声明 `go 1.25`）
- 安装完成后确认：

```powershell
go version
go env GOPROXY GOMODCACHE
```

### 1.2 依赖说明（vendor 模式）

项目已包含 `vendor/` 目录，Go 1.14+ **检测到 vendor 目录时自动使用 `-mod=vendor`**，
构建全程无需联网、无需下载依赖。验证：

```powershell
go env GOFLAGS        # 无需额外设置
cd smart-nas
go build ./...        # 离线即可构建成功
```

> 若日后需要更新依赖（vendor 目录由 `go mod vendor` 重新生成），
> 请在有网络的机器上操作后再拷贝回本机。

> **Windows 系统调用依赖**：系统状态采集（CPU/内存/磁盘）、隐藏文件检测、
> 备份磁盘空间预检与 USB 设备枚举统一使用 **`github.com/ebitengine/purego`**
> （免 cgo 动态调用 kernel32，已 vendor），替代原生 `syscall.NewLazyDLL`；
> DLL 句柄经 `golang.org/x/sys/windows.LoadLibrary` 获取，函数经
> `purego.RegisterLibFunc` 绑定为类型化函数指针调用。
> 详见开发文档 §1.3「Windows 系统调用实现」。

---

## 2. 项目结构

```
smart-nas/
├── cmd/server/            # 主程序入口
├── internal/
│   ├── api/types/         # 统一响应 / WS 消息类型
│   ├── auth/              # JWT 认证（HS256）
│   ├── security/          # Argon2id 密码哈希
│   ├── user/              # 用户管理
│   ├── storage/           # 文件元数据 + blob 存储 + 版本/回收站/秒传
│   ├── transport/         # 传输任务 + tus 下载
│   │   └── tusd/          # tus 可恢复上传协议服务端
│   ├── play/              # 视频在线播放（凭证 / ffprobe 分辨率 / 转封装 / 转码 / Range 流）
│   ├── backup/            # 备份还原（v0.20：任务 / 热备份 / 增量 / 生命周期 / USB 绑定触发 / 邮件）
│   ├── webdav/            # WebDAV（x/net/webdav + Basic Auth）
│   ├── ws/                # WebSocket Hub
│   ├── task/              # 定时任务（robfig/cron）+ 异步 Worker
│   ├── ai/                # AI 管家（v0.21：Ollama 生命周期 / 硬件自适应 / Eino 框架 / RAG 知识库 / HomeAssistant 工具 / 部署模式）
│   ├── iot/               # IoT（设备注册表 / MQTT / 自动化，扩展接口）
│   ├── plugin/            # 插件系统（Hook / goplugin / script）
│   ├── server/            # HTTP 路由、中间件、metrics
│   ├── store/             # JSON 文件持久化存储
│   ├── config/            # 配置管理（TOML + 环境变量 + 热重载）
│   └── util/              # 系统状态 / 哈希 / ID / 网络工具
├── pkg/logger/            # slog 结构化日志（文件滚动）
├── web/                   # 前端 Web 界面（v0.22 重组：html/template 模板 + static 静态资源）
│   ├── templates/         # 页面模板（index.html，经 {{.WebVersion}} 注入资源缓存版本号）
│   └── static/            # 静态资源（css/style.css + js/app.js + js/ai.js + js/smarthome.js + js/video-js-8.24.0/，经 /static/* 服务）
├── vendor/                # 离线依赖（已就绪）
├── config.toml            # 主配置
└── go.mod / go.sum
```

数据文件（启动后自动生成在 `./data`）：
```
data/
├── db/                    # 配置类 TOML（小文件）
│   ├── users.toml         # 用户数据（旧 *.json 启动时自动迁移并删除）
│   ├── settings.toml      # 界面可调的系统设置
│   ├── exclude-list.txt   # 备份全局排除规则（v0.21.2，每行一条，支持 # 注释）
│   └── file_metas.toml.migrated  # 旧版文件元数据（已迁移至 SQLite 后的备份）
├── files/                 # 文件 blob（按 MD5 分散存储）
│   └── metadata.db        # 文件元数据 SQLite 库（v0.11 起，含 -wal/-shm 伴随文件）
├── trash/                 # 默认回收站（v0.24.3 起集中存放于运行目录 data/trash，不在各盘符/文件根下创建）
├── tus/                   # 上传临时分块（uploads.toml 任务记录）
├── backup/                # 备份元数据 SQLite（backup.db，backup_task / backup_history）；
│                          # v0.24.3 起同时作为备份默认存放目录（全局设置为空时，备份产物按任务名存放于此）
├── vectors/               # RAG 向量库（可选，store.json 为向量数据缓存）
├── logs/                  # 日志（smart-nas.log，按大小滚动、按天数清理）
└── plugins/               # 插件数据目录
```

> 文件元数据自 v0.11 起存于 SQLite（增量写入 + 索引，浏览大目录不再全量重写文件）；
> 旧版 `file_metas.toml / file_versions.toml` 首次启动自动导入并保留原 ID，
> 原文件改名 `*.migrated` 留作备份。库文件及 WAL 伴随文件不会出现在文件列表与
> WebDAV 挂载中。

---

## 3. 配置

编辑项目根目录的 `config.toml`，重点关注：

```toml
[server]
port = 8080                # 服务端口
mode = "release"           # debug / release

[storage]
root = "./data/files"      # 磁盘未配置时的兜底根目录
metadata_db = ""           # 元数据库位置（空 = root/metadata.db，即 ./data/files/）
webdav_enabled = true
webdav_prefix = "/dav"     # WebDAV 挂载点

[auth]
jwt_secret = "change-me-in-production"   # ⚠️ 生产环境务必修改！
admin_username = "admin"                 # 主人账号（角色 master），仅能通过配置文件管理
admin_password = "admin123"              # 主人密码，仅可通过配置文件修改

# 角色说明：
#   master（主人）——默认 admin，密码/角色仅可通过本配置文件修改，不可在界面中增删改；
#   可管理所有用户（含管理员）。主人密码在每次启动时按本配置重置。
#   admin（管理员）——可创建/管理普通用户（不能管理其他管理员与主人）。
#   user（普通用户）——目录权限由其 permissions 决定。未配置权限时默认全量访问。
# 文件系统按真实磁盘组织：storage.disks 配置可访问的盘（留空自动发现 Windows 盘符）。

[tus]
enabled = true
path_prefix = "/files/upload/"

[ai]                         # AI 管家（v0.21）
ollama_host = "http://localhost:11434"   # Ollama 服务地址
default_model = "qwen2:7b"               # 显式默认模型（配置了就生效）
embedding_model = "nomic-embed-text"     # RAG 向量模型

[ai.ollama]                  # Ollama 进程生命周期（v0.21）
managed = true               # 由本服务拉起/停止 Ollama；false = 用户自行管理
binary = ''                  # 可执行文件路径，留空自动探测 PATH 与常见安装位置
bind_host = '127.0.0.1:11434'  # 拉起时注入 OLLAMA_HOST；局域网调用改为 '0.0.0.0:11434'
start_timeout = 60           # 拉起后等待就绪的最长秒数
auto_warmup = true           # 就绪后空对话预热，触发默认模型加载

[ai.tune]                    # 硬件自适应调优预设（v0.21）：零值 = 按硬件自动选择
model = ''                   # 覆盖自动模型（GPU 显存≥8GB → deepseek-r1:7b；否则 qwen3.5:9b）
num_ctx = 0                  # 上下文窗口（自动：GPU 8192 / CPU 4096）
keep_alive = ''              # 模型驻留时长（自动：GPU -1 常驻 / CPU 5m）
num_parallel = 0             # 并行推理数

[ai.deploy]                  # 部署模式（v0.21）：auto（v0.23）| single | dual
mode = 'auto'                # v0.23：auto = 启动时自动判定（配置了 remote_host 且可达 → 辅机；否则 → 服务端）
role = 'primary'             # dual 生效：primary（常驻主服务，通常 N100）| auxiliary（辅助机，通常大主机）
remote_host = ''             # auxiliary：远端（主服务）Ollama 地址
remote_api = ''              # auxiliary：主服务 API 根地址（RAG 统一走主服务）
remote_username = ''         # auxiliary：主服务账号（自动获取 JWT 调 RAG）
remote_password = ''
auto_switch = true           # 远端可达自动切换、断线自动降级回本机模型
check_interval = 30          # 远端可达性探测间隔（秒）

[ai.homeassistant]           # HomeAssistant 智能家居接入（v0.21）
enabled = false
base_url = 'http://homeassistant.local:8123'
token = ''                   # HA 长期访问令牌（个人资料 → 安全）

[iot.mqtt]
enabled = true
broker = "tcp://localhost:1883"          # 未启动 MQTT broker 时自动降级

[log]
level = "info"
path = "./data/logs/smart-nas.log"

[play]                       # 视频在线播放（v0.16）
enabled = true
max_online_height = 1440     # 最高在线播放分辨率高度（2K=1440），超出提示下载
ffmpeg_path = 'ffmpeg'       # 留空自动从 PATH 查找
ffprobe_path = 'ffprobe'
remux_concurrency = 3        # 转封装（-c copy）最大并发
transcode_concurrency = 1    # 实时转码最大并发（大开销，防止 NAS 过载）
transcode_threads = 2        # 单个转码任务线程数
ticket_ttl_minutes = 30      # 播放凭证无 Range 请求自动失效时长
cache_dir = './data/cache/video'  # 转封装产物缓存目录
ip_bind = true               # 播放凭证与请求 IP 绑定（防盗链）

[backup]                     # 备份还原（v0.20）
enabled = true               # 备份功能总开关
db_path = './data/backup/backup.db'   # 备份元数据库（SQLite）
log_path = './data/logs/backup.log'   # 独立备份操作日志 backup.log
watch_interval = 30          # 任务表重载间隔（秒，增删改任务后自动生效）
usb_poll_interval = 3        # USB 设备轮询间隔（秒）
```

> 日志参数（位置 / 单文件最大大小 / 保留天数）可在 Web 界面「管理 → 设置」中
> 调整并即时生效，界面设置优先于 `config.toml`。

所有配置都可用**环境变量覆盖**（前缀 `SMARTNAS_`，双下划线表示层级）：

```powershell
$env:SMARTNAS_SERVER_PORT = "9090"
$env:SMARTNAS_AI__RAG__ENABLED = "false"
```

---

## 4. 构建

```powershell
cd smart-nas

# 编译全部包（验证）
go build ./...

# 生成可执行文件
go build -o smart-nas.exe ./cmd/server
```

**期望输出**：无任何错误，生成 `smart-nas.exe`。

---

## 5. 启动

### 5.1 直接运行

```powershell
# 方式一：可执行文件
.\smart-nas.exe -config config.toml -data ./data

# 方式二：go run
go run ./cmd/server -config config.toml -data ./data
```

### 5.2 启动成功的标志

- 日志出现 `HTTP 服务启动 addr=:8080`；
- 启动时自动确保配置中的默认用户为**主人（master）**并打印日志：
  - 用户名：`admin`（config.toml 的 `[auth] admin_username`）
  - 密码：`admin123`（config.toml 的 `[auth] admin_password`，每次启动按此重置）
- 未启动 MQTT/Ollama 时会有降级日志，但**不影响服务运行**。

### 5.3 关闭

`Ctrl + C` 会触发优雅退出（保存元数据、关闭日志、停止调度器；v0.21 起还会先卸载
AI 模型释放显存，再停止本服务拉起的 Ollama 进程——用户自启的 Ollama 不受影响，
详见 §6.15）。

---

## 6. 功能验证

以下示例均在 PowerShell 中执行，服务已监听 `:8080`。

### 6.1 前端 Web 界面（入口）

项目内置轻量单页前端（v0.22 起按 Go 惯例重组为**模板 + 静态资源**结构：
页面模板 `web/templates/index.html` 由 Go `html/template` 渲染并注入 `WebVersion`
资源缓存版本号（`?v=`），无需构建；样式与脚本分离于 `web/static/css/style.css`、
`web/static/js/app.js`、`web/static/js/ai.js`（AI 页签，v0.23）、
`web/static/js/smarthome.js`（智能家居页签，v0.23），第三方库 Video.js 本地化于
`web/static/js/video-js-8.24.0/`，统一经 `/static/*` 路由服务，离线可用）。启动服务后浏览器直接打开：

```
http://localhost:8080/
```

提供：登录 / 退出、**系统状态主页**（CPU/内存/磁盘/在线连接，点击连接数查看在线用户
及其**登录设备**——电脑/手机图标）、文件管理（按真实磁盘组织的文件浏览器：盘符运行时同步
（新盘自动出现/拔出隐藏）、**隐藏/系统文件统一屏蔽**（$RECYCLE.BIN、pagefile.sys 等不显示
且所有操作拒绝）、**详情按钮**（悬浮窗查看名称/类型/大小/路径/时间）、类型列按
图片/视频/音频/文档/压缩包 分类显示（其余显示尾缀）、面包屑、
多选批量 **复制/移动/删除**、目标目录选择、列排序、图片/文本预览、**视频在线播放**
（抽屉式播放器：进度拖拽 / 音量 / 全屏 / 2K 拦截提示，见 §6.12）、新建文件夹、
**tus 单块上传**、下载、重命名、分享链接、**回收站**（多选单个/批量还原与
物理删除、一键还原、一键清空，默认位置 `./data/trash`）、**文件备份还原**
（任务化管理：完整/增量、定时/间隔/USB 实时/手动、配额与冻结、还原与邮件通知，见 §6.13）、
**AI 对话页签**（v0.23：会话列表 / 流式对话 / 模型管理 / AI 设置，见 §6.14）、
**智能家居页签**（v0.23：HomeAssistant 设备网格管理与开关控制，见 §6.14）、
目录权限配置（读/写 +
自动去重，目录通过**内置目录浏览器**选择并自动校验路径格式）、系统设置（刷新频率 /
回收站位置 / **日志位置、最大大小与保留天数**，**仅主人可修改**，管理员/普通用户为只读查看）、
**手机端自适应**（长文件名多行、紧凑表格，次要列 类型/MD5/修改时间 自动隐藏）、
顶栏用户名可点击查看**个人信息弹窗**（含目录权限，非主人可改密码）、
**权限不足时提示具体原因**（如“需要该路径的读/写权限”）、WebSocket 实时上传进度。

> 目录选择使用前端内置浏览器（v0.13）：不再调用系统文件选择器——那会在手机访问时
> 于服务器桌面弹出对话框。手动输入路径统一校验（盘符开头的绝对路径，正斜杠自动
> 归一为反斜杠）。管理员只能授予自身权限范围内的目录，且读写级别不可超过自身。

> 后端 404 回退逻辑见 `internal/server/app.go` 的 `staticFallback`：
> 页面模板加载成功时，未命中 API 的路径会渲染 `web/templates/index.html`（单页入口），
> `/static/*` 由 `gin Static` 直接服务静态资源；
> 若后续替换为 Vue/React 等 SPA，把模板替换为 `web/templates/index.html`、
> 构建产物放到 `web/static/` 即可，无需改后端路由。

### 6.2 健康检查

```powershell
curl.exe -i http://localhost:8080/healthz
```

期望返回 `200` 且 `{"code":0,...,"data":{"status":"ok"}}`。

### 6.3 登录获取 Token

```powershell
$login = curl.exe -s -X POST http://localhost:8080/api/auth/login `
  -H "Content-Type: application/json" `
  -d '{"username":"admin","password":"admin123"}'
$login
```

从返回中复制 `data.token`（形如 `xxxx.yyyy.zzzz` 的 JWT），后续请求带上：

```powershell
$TOKEN = "<粘贴 token>"
$AUTH  = "Authorization: Bearer $TOKEN"
```

### 6.4 获取当前用户 / 修改密码

```powershell
curl.exe -s http://localhost:8080/api/auth/me -H $AUTH

# 修改密码（主人账号不可通过接口修改，仅能改 config.toml 的 admin_password）
curl.exe -s -X POST http://localhost:8080/api/auth/change-password `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"old_password":"旧密码","new_password":"新密码"}'
```

### 6.5 文件操作

```powershell
# 创建目录
curl.exe -s -X POST http://localhost:8080/api/files/mkdir `
  -H $AUTH -H "Content-Type: application/json" -d '{"name":"照片","parent_id":0}'

# 列出文件
curl.exe -s "http://localhost:8080/api/files?parent_id=0" -H $AUTH

# 批量移动 / 复制到目标目录
curl.exe -s -X POST http://localhost:8080/api/files/move `
  -H $AUTH -H "Content-Type: application/json" -d '{"ids":[1,2],"target_dir_id":3}'
curl.exe -s -X POST http://localhost:8080/api/files/copy `
  -H $AUTH -H "Content-Type: application/json" -d '{"ids":[1,2],"target_dir_id":3}'

# 回收站：批量物理删除 / 一键清空 / 一键还原（v0.10 新增）
curl.exe -s -X POST http://localhost:8080/api/files/trash/purge `
  -H $AUTH -H "Content-Type: application/json" -d '{"ids":[1,2]}'
curl.exe -s -X POST http://localhost:8080/api/files/trash/clear -H $AUTH
curl.exe -s -X POST http://localhost:8080/api/files/trash/restore-all -H $AUTH

# 存储统计
curl.exe -s http://localhost:8080/api/storage/stats -H $AUTH
```

### 6.6 上传（tus 协议，支持断点续传 / 秒传去重）

初始化上传（可先算 MD5 秒传检测）：

```powershell
curl.exe -s -X POST http://localhost:8080/api/files/upload/init `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"filename":"a.txt","size":11,"md5":"","parent_id":0}'
```

返回 `upload_url`（如 `/files/upload/<task_id>`）后，按 tus 协议分块上传：

```powershell
# 1) 创建上传会话
curl.exe -s -i -X POST "http://localhost:8080/files/upload/<task_id>" `
  -H "Tus-Resumable: 1.0.0" -H "Upload-Length: 11" `
  -H "Upload-Metadata: filename dC50eHQ=" -H $AUTH

# 2) 查询进度（HEAD）
curl.exe -s -i -X HEAD "http://localhost:8080/files/upload/<task_id>" `
  -H "Tus-Resumable: 1.0.0" -H $AUTH

# 3) 上传数据（PATCH，body 为文件内容）
curl.exe -s -i -X PATCH "http://localhost:8080/files/upload/<task_id>" `
  -H "Tus-Resumable: 1.0.0" -H "Upload-Offset: 0" `
  -H "Content-Type: application/offset+octet-stream" `
  --data-binary "hello world" -H $AUTH
```

### 6.7 下载（支持 Range 断点续传）

```powershell
curl.exe -s -i -O "http://localhost:8080/api/files/<file_id>/download" -H $AUTH

# 指定 Range
curl.exe -s -i -H "Range: bytes=0-4" "http://localhost:8080/api/files/<file_id>/download" -H $AUTH
```

### 6.8 WebDAV（系统级挂载）

浏览器访问 `http://localhost:8080/dav/` 输入 admin 账号密码；
或 Windows 资源管理器「映射网络驱动器」连接到：

```
http://localhost:8080/dav/
```

> 已注册完整 WebDAV 方法集（PROPFIND/MKCOL/COPY/MOVE/LOCK 等），资源管理器
> 可正常挂载浏览；隐藏/系统文件与 NAS 内部目录（回收站 trash、版本库 .versions）
> 不会通过挂载暴露。

### 6.9 WebSocket 实时通道

浏览器/工具连接：

```
ws://localhost:8080/ws?token=<JWT>
```

收到 `{"type":"ping"}` 时客户端回复 `{"type":"pong"}`；上传进度等通过
`file_progress` 类型消息推送。

### 6.10 管理接口（需主人/管理员角色）

```powershell
curl.exe -s http://localhost:8080/api/admin/users -H $AUTH
curl.exe -s http://localhost:8080/api/admin/system/status -H $AUTH
# 创建用户（permissions 为空表示全量访问；仅主人才可创建管理员角色）
curl.exe -s -X POST http://localhost:8080/api/admin/users `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"username":"tom","password":"tom123","role":"user","permissions":[]}'
# 配置目录权限（读写）与只读目录；已具备父目录完整权限的子目录条目会自动去重
curl.exe -s -X PUT http://localhost:8080/api/admin/users/2/permissions `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"permissions":[{"path":"D:/","read":true,"write":true},{"path":"E:/photo","read":true,"write":false}]}'
# 系统设置（刷新频率 / 回收站位置 / 日志位置与清理，界面"管理 → 设置"可改；仅主人可修改）
curl.exe -s http://localhost:8080/api/admin/settings -H $AUTH
# 设置项默认值（v0.24.3：回收站/日志/备份目录的系统默认绝对路径，供设置页占位显示）
curl.exe -s http://localhost:8080/api/admin/settings/defaults -H $AUTH
curl.exe -s -X PUT http://localhost:8080/api/admin/settings `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"cpu_refresh_seconds":5,"disk_refresh_seconds":60,"trash_path":"D:/nas-trash","log_path":"D:/logs/nas.log","log_max_size":100,"log_max_age":30}'
# 日志设置保存后立即生效（滚动归档 + 过期清理，无需重启）
# 注：非主人账号调用 PUT /api/admin/settings 返回 403（只有主人可以修改系统设置）

# 进程级重启服务（v0.23，仅主人/管理员；AI 设置中需重启项保存后前端会自动调用）
curl.exe -s -X POST http://localhost:8080/api/admin/restart -H $AUTH
# 重启为优雅退出 + 自动拉起新进程（Windows 使用父进程接力）；页面轮询 /healthz 恢复后提示刷新

# 注：目录选择器为前端内置浏览器（v0.13 起不再提供服务端系统对话框接口）；
# 管理员只能为普通用户授予自身权限范围内的目录（读写不超自身级别）
```

### 6.11 监控指标（Prometheus 文本格式）

```powershell
curl.exe -s http://localhost:8080/metrics
```

包含 `nas_system_cpu_usage_percent`、`nas_ws_connections`、`nas_play_remux_active`、
`nas_play_transcode_active`、`nas_play_tickets` 等指标，可直接接入 Prometheus / Grafana。

### 6.12 视频在线播放（v0.16）

> 前置：需要服务器安装 ffmpeg / ffprobe（见 §7.4）。未安装时 mp4 仍可尝试播放，
> mkv 等转封装格式会提示"请下载"。

- 支持 .mp4 直接播放（HTTP Range），.mkv 等容器由后端 **ffmpeg 转封装（-c copy）** 为
  MP4 后播放（浏览器不支持原编码时降级为实时转码，转码并发默认 1）。
  转换产物缓存在 `data/cache/video/`，首次处理后有全量 Range 能力（拖拽秒定位）。
- **音频在线播放（v0.18）**：mp3/flac/wav/aac/ogg/m4a/opus/wma 等音频文件同样支持
  在线播放——直接 Range 流，无需 ffprobe 视频探测（未安装 ffmpeg 也可播），
  按扩展名返回正确 MIME 类型。
- 分辨率上限默认 2K（`[play] max_online_height=1440`）：文件列表对 ≤2K 视频显示
  「播放」按钮；**超过 2K** 显示「高清」标签与下载按钮（不转码/转封装）；强行点击播放
  时播放器弹出悬浮提示窗（继续播放 / 下载），mp4 允许继续直连（可能卡顿），其余拒绝。
- 播放凭证（Ticket）：打开播放器时 `POST /api/play/ticket` 获取，随播放链接
  `/api/video?file=&token=` 使用；关闭播放器 `DELETE /api/play/ticket/{token}` 释放；
  30 分钟无 Range 请求自动失效；默认与请求 IP 绑定（`[play] ip_bind`）。
- 支持 5 人同时播放、单文件最大 10GB；播放权限继承目录读权限。

```powershell
# 查询视频信息（分辨率 / 是否可播 / 播放模式）
curl.exe -s "http://localhost:8080/api/video/info?file=<file_id>" -H $AUTH

# 创建播放凭证（返回 token 与播放 url）
curl.exe -s -X POST http://localhost:8080/api/play/ticket `
  -H $AUTH -H "Content-Type: application/json" -d '{"file_id":<file_id>}'

# 播放/拖拽：HTTP Range 请求返回 206 Partial Content + Content-Range（示例直连验证）
curl.exe -s -i -H "Range: bytes=0-102399" http://localhost:8080/api/video?file=<file_id>&token=<token>

# 释放播放凭证（关闭播放器）
curl.exe -s -X DELETE http://localhost:8080/api/play/ticket/<token> -H $AUTH
```

> 前端在「文件」列表对视频文件异步探测分辨率后按结果展示按钮；播放器为右侧抽屉式
> **Video.js** 播放器（本地引入 `web/static/js/video-js-8.24.0/`，无需外网 CDN；自带控制条：
> 播放/暂停、进度拖拽、音量、全屏、网络中断重试），UI 与后台深色主题一致。
> v0.19 起支持：键盘 ←/→ 快退/快进 5 秒、↑/↓ 音量、空格播放/暂停、F 全屏（抽屉打开时
> 全局生效）；手机水平滑动快进/快退；控制条倍速菜单（0.5~2 倍）。
> 前端资源已拆分（v0.22 起重组）：`web/templates/index.html`（结构模板）+
> `web/static/css/style.css`（样式）+ `web/static/js/app.js`（逻辑）。
> 点击视频文件名同样会打开播放器（v0.18 起，不再弹出“预览”弹窗）；无法内联预览的
> 文件点击文件名显示“详细信息”。「详情」按钮弹出的“详细信息”窗口对视频文件提供
> 「播放」按钮、对文件提供「下载」按钮（不再放重复的“关闭”按钮），并显示文件真实路径
> （v0.19 起，详情接口已做读权限校验）。

### 6.13 文件备份还原（v0.20，v0.21.x 增强）

> 前端入口：顶部导航「备份」页签。任务/历史对**所有登录用户**可见（只读），
> 创建/编辑/删除任务、触发备份、冻结、删除备份、还原为**主人/管理员**专属。
> 操作日志独立记录于 `data/logs/backup.log`。

**任务化管理**（`internal/backup`，元数据存 SQLite `data/backup/backup.db`）：

- **多源备份**：一个任务可选多个源目录/文件（目录选择器或手动输入，chip 列表多选管理），
  排除规则支持精确名、目录名、通配符（`*.tmp`，同样多选管理）；
  添加包含已有源的父目录时**自动移除其子目录源**（重复/子目录路径被拒绝）；
- **产物命名与内部布局**（v0.21.6）：产物命名 `<任务名称>_<年月日-时分秒>`（精确到秒，
  同秒重复备份自动追加 `_1/_2` 防覆盖；增量快照 json 同名）；产物内部目录完全按源
  路径生成、**从盘符开始**——备份 `S:\Code\AI` 则压缩包内第一级为 `S/`
  （`S/Code/AI/...`），目录模式结构一致，多源不再使用 `source_N_` 前缀；
- **热备份**：程序运行中即可执行，不强制锁文件；被占用/写入中的文件自动跳过并标记
  （历史状态记为 `partial` 部分成功，不中断整个备份）；
- **备份类型**：创建任务时可选 **增量**（首次完整，后续仅备份变化，默认）或
  **完全**（每次全量备份）；增量依赖父备份链（无任何可用备份时禁止增量），
  还原前**校验整条链哈希完整性，链损坏拒绝还原**；
  增量比对基于产物旁的 **sidecar 快照**（`<产物>.snapshot.json`，记录文件 size+mtime
  纳秒指纹），无需重遍历/解压历史产物；旧版备份无快照，升级后第一次增量做一次
  **自愈全量**生成快照，之后为真增量；
- **触发方式**：手动 / 定时（cron 表达式或简单周期 日/周/月，内部转 cron 存储）/
  间隔（小时，内部转 cron）/ USB 实时（绑定的移动设备插入即触发）；
- **压缩**：可选 zip（**无损**），级别 1-9；**全局默认级别 6**，可在
  「管理 → 设置」调整，任务内可单独覆盖；
- **全局默认**（「管理 → 设置」）：备份默认存放目录、备份默认压缩级别、
  **备份全局排除规则**——新建任务时自动套用，任务内可单独修改；
  存放目录留空时使用系统默认 `data/backup`（v0.24.3 起）；
  规则自 v0.21.2 起独立存储于数据目录 `data/db/exclude-list.txt`
  （每行一条、支持 `#` 注释）；
  排除规则已预置 Windows / Linux 系统目录、开发项目产物（node_modules、.git 等）
  与 NAS 跨平台临时文件（Thumbs.db、~$* 等），每行一条、支持通配符，
  可直接复制用作 `exclude-list.txt`；
- **启动补跑**（v0.21）：服务启动时检查定时任务（cron / 间隔小时），
  若「上次执行之后」存在已到期的计划时间（服务离线期间错过），自动补执行一次；
- **任务进度**（v0.21）：「创建备份任务」右侧的「任务进度」按钮实时展示
  各任务执行的**百分比进度条**（预扫描 → 拷贝 → 压缩 → 校验分阶段推算）与
  已处理字节/文件数/当前文件；点击「展开详情」可查看**实时执行日志**
  （最近 200 条，自动滚动），任务结束后保留 10 分钟；
- **生命周期**：数量 / 大小双阈值配额（大小配额数值与单位 MB/GB/TB 分离选择），
  备份完成后按最早优先自动清理；
  **冻结备份**不计数、不占配额、自动清理绝不删除（仅允许手动删除）；
- **还原**：支持还原到原位置或指定位置；目标目录通过弹窗内的**内置目录选择器**
  选择（v0.21.6 起，不再使用浏览器原生 prompt），同名处理下拉可选
  （询问 / 覆盖 / 跳过 / 重命名旧文件为 `*.old-时间戳`）；还原后做文件校验，
  还原内容保持源路径布局（目标目录下出现 `S/Code/AI/...`）；
- **邮件通知**：任务结束后发送结果邮件（SMTP 配置 JSON 存于任务内）；
- **USB 绑定**：仅绑定到任务的设备（串号|卷标）插入才触发备份，**陌生 U 盘不会误触发**。

```powershell
# 任务列表 / 创建任务（简单周期每日 04:30 自动转 cron "30 4 * * *" 存储）
curl.exe -s http://localhost:8080/api/backup/tasks -H $AUTH
curl.exe -s -X POST http://localhost:8080/api/backup/tasks `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"task_name":"照片每日备份","source_paths":["S:/photo"],"output_dir":"Y:/nas-backup",
       "trigger_mode":"timer","simple_period":{"unit":"day","hour":4,"minute":30},
       "enable_compress":true,"compress_level":6,"max_backup_count":7,"max_backup_size":"50GB"}'

# 手动触发 / 历史 / 备份内容浏览
curl.exe -s -X POST http://localhost:8080/api/backup/tasks/<task_id>/run -H $AUTH
curl.exe -s http://localhost:8080/api/backup/tasks/<task_id>/history -H $AUTH
curl.exe -s http://localhost:8080/api/backup/backups/<backup_id>/contents -H $AUTH

# 还原（原位置 + 覆盖策略；target_dir 留空 = 原位置；conflict: ask/overwrite/skip/rename）
curl.exe -s -X POST http://localhost:8080/api/backup/backups/<backup_id>/restore `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"target_dir":"Y:/restore-test","conflict":"overwrite"}'

# 冻结 / 解冻 / 手动删除（删除为物理删除，不可恢复）
curl.exe -s -X POST http://localhost:8080/api/backup/backups/<backup_id>/freeze `
  -H $AUTH -H "Content-Type: application/json" -d '{"frozen":true}'
curl.exe -s -X DELETE http://localhost:8080/api/backup/backups/<backup_id> -H $AUTH

# 当前接入的 USB 可移动设备（绑定用）
curl.exe -s http://localhost:8080/api/backup/usb-devices -H $AUTH

# 各任务最近一次执行的实时进度（百分比/阶段/日志，前端"任务进度"面板数据源）
curl.exe -s http://localhost:8080/api/backup/progress -H $AUTH
```

> 行为约束（与需求一致）：同一时间只运行一个备份任务，同一任务执行中重复触发直接跳过；
> 备份前预检查源可读性与备份磁盘剩余空间，中途失败丢弃不完整产物并记录错误日志；
> 自动清理逻辑**严禁删除冻结备份**；无完整备份（链）时禁止创建/还原增量备份。

### 6.14 AI 管家 / 知识库 / Ollama 管理（v0.21）

> 前置：安装 Ollama（见 §7.1）。服务启动时**自动完成一切**——检测 Ollama →
> 未运行则后台拉起（`[ai.ollama].managed=true` 默认开启）→ 等就绪 → 按硬件
> 自适应加载默认模型 → 记录生效预设日志。所有 AI 接口走 JWT 鉴权。

**硬件自适应**：启动日志可见「硬件检测完成 / 硬件调优预设生效」：

| 硬件 | 自动选择模型 | 预设 |
|------|------------|------|
| NVIDIA GPU 显存 ≥ 8GB（如 RTX 4070 12GB） | `deepseek-r1:7b`（Q4_K_M 全量进显存） | num_ctx=8192、常驻、并行 2 |
| 无 GPU / 显存 < 8GB（如 N100） | `qwen3.5:9b`（Q4_K_M CPU 推理） | num_ctx=4096、驻留 5m、并行 1 |

`config.toml [ai.tune]` 显式配置永远优先于自动检测（模拟无 GPU 环境验证 N100 分支：
直接配置 `model = "qwen3.5:9b"` 即可）。

```powershell
# 对话（非流式）
curl.exe -s -X POST http://localhost:8080/api/ai/chat `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"content":"帮我总结一下这个月的照片"}'

# 流式对话（SSE）
curl.exe -s -N -X POST http://localhost:8080/api/ai/chat/stream `
  -H $AUTH -H "Content-Type: application/json" -d '{"content":"你好"}'

# 状态总览：Ollama 进程（托管/自启）、已加载模型、硬件预设、部署模式
curl.exe -s http://localhost:8080/api/ai/status -H $AUTH

# 模型管理（仅主人/管理员）
curl.exe -s http://localhost:8080/api/ai/models -H $AUTH                        # 模型列表
curl.exe -s -X POST http://localhost:8080/api/ai/models/pull `
  -H $AUTH -H "Content-Type: application/json" -d '{"model":"qwen2:7b"}'        # 拉取
curl.exe -s http://localhost:8080/api/ai/models/pull/status -H $AUTH            # 拉取进度
curl.exe -s -X DELETE "http://localhost:8080/api/ai/models/qwen2:7b" -H $AUTH   # 删除

# 推理参数查看 / 调整（仅主人/管理员，热更新）
curl.exe -s http://localhost:8080/api/ai/settings -H $AUTH
curl.exe -s -X PUT http://localhost:8080/api/ai/settings `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"num_ctx":8192,"temperature":0.7,"keep_alive":"-1"}'

# v0.23 设置页新增字段（同一接口）：
#   default_model  主动选择启动模型（热更新）
#   deploy_mode    机器模式 auto | server | auxiliary（auto 需重启判定）
#   server_addr    服务端地址（host:port 或 URL，写入 ai.deploy.remote_host）
curl.exe -s -X PUT http://localhost:8080/api/ai/settings `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"default_model":"qwen2:7b","deploy_mode":"auto","server_addr":"192.168.1.10:11434"}'
# 响应含 need_restart 数组：列出需重启服务才能生效的字段（如 ai.deploy.mode）。
# 网页「AI → 设置」保存时若该项非空，会提示确认并自动重启服务（POST /api/admin/restart），
# 重启完成（/healthz 恢复）后提示刷新页面。

# HomeAssistant 设备管理（v0.23，需启用 [ai.homeassistant]）
curl.exe -s http://localhost:8080/api/ai/ha/status -H $AUTH            # 连接状态
curl.exe -s "http://localhost:8080/api/ai/ha/devices?domain=light" -H $AUTH  # 设备列表
curl.exe -s -X POST http://localhost:8080/api/ai/ha/service `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"domain":"light","service":"turn_on","entity_id":"light.living_room"}' # 控制设备
```

**局域网调用（OpenAI 兼容端点）**——两种方式：

1. **经 smart-nas 代理（推荐，统一 JWT 鉴权）**：

```powershell
# 局域网另一台设备上（模型缺省时自动补当前生效模型）
curl.exe -s -X POST http://<NAS主机IP>:8080/api/ai/v1/chat/completions `
  -H "Authorization: Bearer <smart-nas JWT>" -H "Content-Type: application/json" `
  -d '{"model":"","messages":[{"role":"user","content":"你好"}],"stream":false}'
```

2. **直连 Ollama**：`[ai.ollama].bind_host` 改为 `'0.0.0.0:11434'` 并重启服务后，
   局域网设备可直接访问 `http://<NAS主机IP>:11434/v1/chat/completions`
   （Ollama 原生 OpenAI 兼容端点）。**防火墙需放行**对应端口（11434 或 8080）：
   `netsh advfirewall firewall add rule name="Ollama LAN" dir=in action=allow protocol=TCP localport=11434`。
   公网暴露务必置于反向代理 + HTTPS 之后。

**部署模式（`[ai.deploy]`，v0.23 起支持 auto 自动判定）**——切换只改配置或网页设置，不改代码：

| 形态 | 配置 | 效果 |
|------|------|------|
| **自动判定（v0.23，推荐）** | `mode='auto'` + 可选 `remote_host` | 启动时探测：配置了服务端地址且**可达 → 以辅机启动**；未配置地址或**不可达 → 以服务端启动**。判定结果仅作用于本次运行（持久化保留 auto），运行中不因断连切换身份 |
| 单机·大主机 | `mode='single'` | 自动选高级模型（deepseek-r1:7b，GPU 常驻），RAG 本机索引 |
| 单机·N100 | `mode='single'` | 自动选低配模型（qwen3.5:9b，低资源常驻），RAG 本机索引 |
| 双机·主服务 | `mode='dual' role='primary'`（N100） | 低配模型 + RAG 开启，作为知识库与模型服务端 |
| 双机·辅助机 | `mode='dual' role='auxiliary'`（大主机）+ `remote_host`/`remote_api` 等 | 探测远端：可达 → 自动切换远端模型（卸载本机模型）+ RAG 走主服务（不建独立向量库）；断线 → 优雅降级回本机高级模型；周期探测自动双向切换 |

> 机器模式既可通过 `config.toml` 配置，也可在网页「AI → 设置」中调整（主人/管理员）。
> `deploy_mode` / `server_addr` / Ollama 地址 / 向量模型等**启动期装配**项保存后会返回
> `need_restart` 提示，确认后自动重启服务生效；模型选择与推理参数为**热更新**即时生效。

**前端 AI 页签（v0.23）**：左侧会话列表（新建 / 切换 / 删除），右侧流式对话窗口
（SSE 逐字输出、可停止）；工具栏支持模型切换、模型管理（拉取进度 / 删除）与
「设置」面板（启动模型 / 机器模式 / 服务端地址 / 推理参数）。

**智能家居页签（v0.23）**：HomeAssistant 设备网格——按域名（灯 / 开关 / 传感器等）
展示设备名称与实时状态，开关类设备可直接点击控制；未启用或连接失败时页面提示
配置方法（需 `[ai.homeassistant]`）。

**HomeAssistant 接入（可选）**：`[ai.homeassistant]` 填 `base_url` 与长期访问令牌
（HA → 个人资料 → 安全）并 `enabled=true`，重启后自动连通性自检并注册 3 个 AI 工具
（`ha_list_devices` / `ha_get_state` / `ha_call_service`），之后直接对话即可控制设备
（如「把客厅灯调到 50%」）。未启用 / 自检失败时降级不注册，对话中提示暂不支持设备控制。

**知识库（RAG）**：`[ai.rag] enabled=true` 时对文档建立向量索引（chromem 嵌入式，
`data/vectors/store.json`），对话自动注入检索上下文；`POST /api/ai/rag/search` 可直接语义检索。

### 6.15 优雅关闭与 Ollama 进程安全（v0.21）

`Ctrl + C` 优雅退出时：先卸载 AI 模型（`keep_alive=0` 释放显存/内存），再停止
**本服务拉起的** Ollama 进程。用户自己启动的 Ollama **永远不会被停止**；
服务重启时凭 `data/ollama.pid` 自动接管上一任服务拉起的实例。

---

## 7. 可选组件（可选启用）

### 7.1 Ollama（本地大模型，AI 对话 / RAG）

1. 安装：[ollama.com](https://ollama.com/) 下载 Windows 版并安装即可
   （**无需手动启动**：v0.21 起服务端检测到未运行会自动后台拉起 `ollama serve`，
   优雅关闭时自动停止本服务拉起的实例；`[ai.ollama].managed=false` 可改为自行管理）；
2. 拉取模型（三种方式任选，也可只做第 3 步让服务端在管理页拉取）：

```powershell
ollama pull deepseek-r1:7b    # GPU 机器（显存≥8GB）硬件自适应默认模型
ollama pull qwen3.5:9b        # 无 GPU 机器（N100）硬件自适应默认模型
ollama pull qwen2:7b          # config 默认对话模型（兼容旧配置）
ollama pull nomic-embed-text  # 向量模型（RAG 用）
```

   或经 smart-nas 管理接口拉取（进度可查，见 §6.14）：
   `POST /api/ai/models/pull`。
3. `config.toml` 中 `[ai] ollama_host` 默认 `http://localhost:11434` 即可；
   局域网调用改 `[ai.ollama].bind_host = '0.0.0.0:11434'`（见 §6.14）；
4. 重启服务：启动日志确认「硬件检测完成 → 硬件调优预设生效 → Ollama 已就绪 →
   默认模型预热完成」，即可对话（见 §6.14 验证命令）。

### 7.2 MQTT Broker（IoT 设备接入，可选）

推荐使用 Mosquitto：

```powershell
# 方式一：Docker Desktop
docker run -d -p 1883:1883 -p 9001:9001 --name mosquitto eclipse-mosquitto

# 方式二：下载安装包 https://mosquitto.org/download/
```

未启动时服务自动降级为"仅本地设备"，不影响其它功能。

### 7.3 rclone（备份，可选）

配置 `config.toml` 的 `[storage.rclone]` 后启用，定时任务按 `schedule` 执行。

### 7.4 ffmpeg / ffprobe（视频在线播放前置，推荐）

在线播放 .mkv 等格式（转封装）与浏览器不支持的编码（实时转码）依赖 ffmpeg。
**推荐方式（v0.19，随包分发、无需系统安装）**：将 `ffmpeg.exe` 与 `ffprobe.exe` 放到
`smart-nas.exe` 同目录（或运行目录）即可，服务启动时自动探测并优先使用：

```powershell
# 下载 Windows 构建版（https://ffmpeg.org/download.html 或 gyan.dev 全量版）
# 解压后把 bin 下的 ffmpeg.exe / ffprobe.exe 复制到 smart-nas.exe 所在目录
ffmpeg.exe -version   # 验证
ffprobe.exe -version  # 验证
```

也可安装到系统 PATH（`choco install ffmpeg`），或通过 `config.toml` 的
`[play] ffmpeg_path / ffprobe_path` 指定绝对路径（优先级最高）。

- 探测顺序：可执行文件同目录 → 工作目录 → `config.toml` 配置值（含 PATH 查找）；
- 非 mp4 / 无法直出的编码在缺少 ffmpeg 时，前端按"暂不支持在线播放"引导下载；
- 音频文件（mp3/flac/wav 等）在线播放不依赖 ffmpeg。

---

## 8. 常见问题（FAQ）

| 现象 | 原因 | 解决 |
|---|---|---|
| 构建报 `no required module provides package` | 未走 vendor 模式 | 确认 `vendor/` 存在且位于项目根目录，`go build ./...` 重试 |
| 启动日志中文乱码 | PowerShell 控制台编码为 GBK | `chcp 65001` 后重启终端，或用 `chcp 65001; go run ./cmd/server` |
| 端口 8080 被占用 | 其它程序占用 | 改 `config.toml` 的 `server.port`，或用 `$env:SMARTNAS_SERVER_PORT=9090` |
| 日志出现 `listen tcp :8080: ... not a socket` | 受限沙箱/容器禁止 socket | 在正常的 Windows 宿主机/可开端口的服务器上运行 |
| 日志出现 MQTT 连接失败 | 未启动 broker | 无需处理（自动降级），或参考 7.2 启动 Mosquitto |
| 登录返回 401 | Token 过期 / 未登录 | 重新 `POST /api/auth/login` 获取新 token |
| 忘记默认密码 | - | 主人密码由 `config.toml` 的 `[auth] admin_password` 决定，修改后重启生效；若误删主人导致无法登录，删除 `data/db/users.json` 后重启重建（会丢失全部用户数据，慎用） |
| 修改 `config.toml` 后热加载 | 支持 | 服务每 1 秒轮询配置文件，改动保存后自动生效（部分模块需重启） |
| 备份状态显示"部分成功" | 部分源文件被占用/正在写入 | 属热备份正常行为（不中断）；关闭占用程序后重跑即可完整备份 |
| 备份报"剩余空间不足" | 备份目标磁盘配额不足 | 清理目标磁盘或回收站；或修改任务存放目录 |
| 备份被跳过"正在执行中" | 同一任务未跑完又触发 | 等待当前执行完成（同一时间只运行一个备份任务） |
| USB 插入未触发备份 | 设备未绑定/任务非实时模式 | 任务触发方式需为「USB 实时」并绑定该设备（串号|卷标） |
| 还原报"备份链损坏" | 链上某备份产物丢失/被改动 | 属保护机制（拒绝还原损坏链）；删除损坏链后重新完整备份 |
| 升级后第一次增量备份仍是全量 | 旧版备份无 sidecar 快照（或快照键格式为旧版） | 属自愈机制：本次生成新格式快照，之后即为真增量 |
| 备份进度日志出现 Z:\TEMP\ 等临时目录路径 | 旧版本压缩模式日志显示 staging 临时路径 | v0.21.5 起已换算回源路径显示，更新程序即可 |
| AI 对话报「AI 模块未启用」 | 未配置 `[ai].ollama_host` 或 Ollama 不可用 | 确认 Ollama 已安装且 `[ai.ollama].managed=true`（默认自动拉起） |
| AI 接口返回 401 | 未登录 / Token 过期 | 所有 AI 接口均需 JWT（先 `POST /api/auth/login`） |
| 模型管理接口返回 403 | 非主人/管理员 | 模型拉取/删除/参数调整为管理员专属 |
| 关闭服务后 Ollama 仍在运行 | Ollama 为用户自行启动（非服务拉起） | 属安全设计（绝不停止用户自启实例）；`managed=false` 时同理 |
| 局域网设备无法直连 Ollama | bind_host 为 127.0.0.1 或防火墙拦截 | `[ai.ollama].bind_host = '0.0.0.0:11434'` 并放行端口（见 §6.14） |
| 智能家居工具不可用 | HA 未启用 / 地址或令牌错误 | 检查 `[ai.homeassistant]` 配置与 HA 长期访问令牌；自检失败会降级不注册 |
| AI 设置保存后提示需重启 | 修改了启动期装配项（机器模式 / 服务端地址 / Ollama 地址 / 向量模型） | 确认后自动重启服务，`/healthz` 恢复后按提示刷新页面；模型选择与推理参数无需重启 |
| 网页重启后页面无响应 | 服务正在重启 | 属正常现象：轮询 `/healthz` 恢复后页面会提示刷新，稍候即可 |
| auto 模式启动成了服务端 | 未配置服务端地址或地址不可达 | 属预期判定逻辑；确认对端 Ollama 运行且 `server_addr` 配置正确后重启 |

---

## 9. 安全建议（上线前）

1. **更改主人密码**：编辑 `config.toml` 的 `[auth] admin_password` 并重启（主人密码仅能如此修改）；
2. **更换 `jwt_secret`**：`config.toml` 中 `[auth] jwt_secret` 改为随机长字符串；
3. 如需公网访问，请置于反向代理（Nginx/Caddy）之后并启用 HTTPS；
4. 定期备份 `data/` 目录（含元数据 JSON 与文件存储的真实磁盘目录）。