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
│   ├── ai/                # AI（Ollama 客户端 / 对话 / 工具 / RAG，扩展接口）
│   ├── iot/               # IoT（设备注册表 / MQTT / 自动化，扩展接口）
│   ├── plugin/            # 插件系统（Hook / goplugin / script）
│   ├── server/            # HTTP 路由、中间件、metrics
│   ├── store/             # JSON 文件持久化存储
│   ├── config/            # 配置管理（TOML + 环境变量 + 热重载）
│   └── util/              # 系统状态 / 哈希 / ID / 网络工具
├── pkg/logger/            # slog 结构化日志（文件滚动）
├── web/                   # 前端 Web 界面（index.html + css/style.css + js/app.js + js/video-js-8.24.0/）
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
│   └── file_metas.toml.migrated  # 旧版文件元数据（已迁移至 SQLite 后的备份）
├── files/                 # 文件 blob（按 MD5 分散存储）
│   ├── metadata.db        # 文件元数据 SQLite 库（v0.11 起，含 -wal/-shm 伴随文件）
│   └── trash/             # 默认回收站（删除的文件集中存放于此）
├── tus/                   # 上传临时分块（uploads.toml 任务记录）
├── backup/                # 备份元数据 SQLite（backup.db，backup_task / backup_history）
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

[ai]
ollama_host = "http://localhost:11434"   # 未安装 Ollama 时 AI 功能自动降级

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

`Ctrl + C` 会触发优雅退出（保存元数据、关闭日志、停止调度器）。

---

## 6. 功能验证

以下示例均在 PowerShell 中执行，服务已监听 `:8080`。

### 6.1 前端 Web 界面（入口）

项目内置轻量单页前端（`web/index.html`，原生 HTML/JS，无需构建；
样式与脚本分离：`web/css/style.css`、`web/js/app.js`，第三方库 Video.js 本地化于
`web/js/video-js-8.24.0/`，离线可用）。启动服务后浏览器直接打开：

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
物理删除、一键还原、一键清空，默认位置 `./data/files/trash`）、**文件备份还原**
（任务化管理：完整/增量、定时/间隔/USB 实时/手动、配额与冻结、还原与邮件通知，见 §6.13）、
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
> 存在 `web/` 目录时，未命中 API 的路径会渲染 `index.html`（单页入口），
> 若后续替换为 Vue/React 等 SPA，把构建产物放到 `web/` 即可，无需改后端。

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
curl.exe -s -X PUT http://localhost:8080/api/admin/settings `
  -H $AUTH -H "Content-Type: application/json" `
  -d '{"cpu_refresh_seconds":5,"disk_refresh_seconds":60,"trash_path":"D:/nas-trash","log_path":"D:/logs/nas.log","log_max_size":100,"log_max_age":30}'
# 日志设置保存后立即生效（滚动归档 + 过期清理，无需重启）
# 注：非主人账号调用 PUT /api/admin/settings 返回 403（只有主人可以修改系统设置）

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
> **Video.js** 播放器（本地引入 `web/js/video-js-8.24.0/`，无需外网 CDN；自带控制条：
> 播放/暂停、进度拖拽、音量、全屏、网络中断重试），UI 与后台深色主题一致。
> v0.19 起支持：键盘 ←/→ 快退/快进 5 秒、↑/↓ 音量、空格播放/暂停、F 全屏（抽屉打开时
> 全局生效）；手机水平滑动快进/快退；控制条倍速菜单（0.5~2 倍）。
> 前端资源已拆分：`web/index.html`（结构）+ `web/css/style.css`（样式）+ `web/js/app.js`（逻辑）。
> 点击视频文件名同样会打开播放器（v0.18 起，不再弹出“预览”弹窗）；无法内联预览的
> 文件点击文件名显示“详细信息”。「详情」按钮弹出的“详细信息”窗口对视频文件提供
> 「播放」按钮、对文件提供「下载」按钮（不再放重复的“关闭”按钮），并显示文件真实路径
> （v0.19 起，详情接口已做读权限校验）。

### 6.13 文件备份还原（v0.20）

> 前端入口：顶部导航「备份」页签。任务/历史对**所有登录用户**可见（只读），
> 创建/编辑/删除任务、触发备份、冻结、删除备份、还原为**主人/管理员**专属。
> 操作日志独立记录于 `data/logs/backup.log`。

**任务化管理**（`internal/backup`，元数据存 SQLite `data/backup/backup.db`）：

- **多源备份**：一个任务可选多个源目录/文件（目录选择器或手动输入，chip 列表多选管理），
  排除规则支持精确名、目录名、通配符（`*.tmp`，同样多选管理）；
- **热备份**：程序运行中即可执行，不强制锁文件；被占用/写入中的文件自动跳过并标记
  （历史状态记为 `partial` 部分成功，不中断整个备份）；
- **备份类型**：创建任务时可选 **增量**（首次完整，后续仅备份变化，默认）或
  **完全**（每次全量备份）；增量依赖父备份链（无任何可用备份时禁止增量），
  还原前**校验整条链哈希完整性，链损坏拒绝还原**；
- **触发方式**：手动 / 定时（cron 表达式或简单周期 日/周/月，内部转 cron 存储）/
  间隔（小时，内部转 cron）/ USB 实时（绑定的移动设备插入即触发）；
- **压缩**：可选 zip（**无损**），级别 1-9；**全局默认级别 9（最高）**，可在
  「管理 → 设置」调整，任务内可单独覆盖；
- **全局默认**（「管理 → 设置」）：备份默认存放目录、备份默认压缩级别——
  新建任务时自动套用，任务内可单独修改；
- **生命周期**：数量 / 大小双阈值配额（大小配额数值与单位 MB/GB/TB 分离选择），
  备份完成后按最早优先自动清理；
  **冻结备份**不计数、不占配额、自动清理绝不删除（仅允许手动删除）；
- **还原**：支持还原到原位置或指定位置；覆盖策略可选（询问 / 覆盖 / 跳过 /
  重命名旧文件为 `*.old-时间戳`）；还原后做文件校验；
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
```

> 行为约束（与需求一致）：同一时间只运行一个备份任务，同一任务执行中重复触发直接跳过；
> 备份前预检查源可读性与备份磁盘剩余空间，中途失败丢弃不完整产物并记录错误日志；
> 自动清理逻辑**严禁删除冻结备份**；无完整备份（链）时禁止创建/还原增量备份。

---

## 7. 可选组件（可选启用）

### 7.1 Ollama（本地大模型，AI 对话 / RAG）

1. 安装：[ollama.com](https://ollama.com/) 下载 Windows 版并运行；
2. 拉取模型（约需几分钟，首次较大）：

```powershell
ollama pull qwen2:7b          # 对话模型（config 默认）
ollama pull nomic-embed-text  # 向量模型（RAG 用）
```

3. `config.toml` 中 `[ai] ollama_host` 默认 `http://localhost:11434` 即可；
4. 重启服务，`GET /api/ai/models` 接口即可用（AI 路由为扩展接口，按需接入前端）。

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

---

## 9. 安全建议（上线前）

1. **更改主人密码**：编辑 `config.toml` 的 `[auth] admin_password` 并重启（主人密码仅能如此修改）；
2. **更换 `jwt_secret`**：`config.toml` 中 `[auth] jwt_secret` 改为随机长字符串；
3. 如需公网访问，请置于反向代理（Nginx/Caddy）之后并启用 HTTPS；
4. 定期备份 `data/` 目录（含元数据 JSON 与文件存储的真实磁盘目录）。