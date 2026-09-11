# 智能家庭 NAS（smart-nas）Go 新手学习指南

> **写给谁**：刚加入团队、只学过 Go 基础语法（变量、循环、函数、struct、interface）的新同事。
> **目标**：在**不打开源码**的情况下，仅凭本文档就能理解这个项目的整体架构、核心业务流程、用到的 Go 特性与第三方库，并能在本地把项目跑起来、会写测试、会排查问题。
> **配套资料**：`smart-nas/README.md`（运行指南）、`md/基于 Ollama + Go 的智能家庭 NAS 系统开发文档v0.1.md`（设计文档）、`md/问题汇总.md`（版本迭代记录）。

---

## 目录

1. [项目是什么](#1-项目是什么)
2. [整体架构与模块划分](#2-整体架构与模块划分)
3. [核心业务流程代码导览（从 main 入口开始）](#3-核心业务流程代码导览从-main-入口开始)
4. [项目中用到的 Go 特性与库（结合代码示例）](#4-项目中用到的-go-特性与库结合代码示例)
5. [本地开发环境搭建、运行、测试](#5-本地开发环境搭建运行测试)
6. [常见问题与调试技巧](#6-常见问题与调试技巧)
7. [学习路线建议](#7-学习路线建议)

---

## 1. 项目是什么

**一句话**：这是一个跑在 Windows 家庭电脑/NAS 上的"私有云盘 + 智能管家"服务，用 Go 编写，通过浏览器访问。

**核心功能**（对应前端顶部导航的四个页签）：

| 页签 | 功能 | 对应后端模块 |
|------|------|-------------|
| 系统 | 主页：CPU/内存/磁盘使用率、在线用户（含登录设备） | `internal/util`（系统状态）、`internal/ws`（在线用户） |
| 文件 | 按真实磁盘（C:、D:…）浏览文件、上传/下载/重命名/移动/复制/删除、搜索、分享、视频在线播放 | `internal/storage`、`internal/transport`、`internal/play` |
| 回收站 | 删除的文件集中存放，可单个/批量还原、物理删除、一键清空 | `internal/storage` |
| 管理 | 用户管理（角色/目录权限）、系统设置（刷新频率/回收站位置/日志） | `internal/user`、`internal/settings` |

**技术栈速览**（详见第 4 章）：

- **Web 框架**：Gin（`github.com/gin-gonic/gin`）
- **认证**：JWT（HS256，手写实现）+ Argon2id 密码哈希
- **元数据存储**：SQLite（`modernc.org/sqlite`，纯 Go 免 cgo）
- **配置/用户/设置持久化**：TOML（`github.com/pelletier/go-toml/v2`）
- **实时通信**：WebSocket（`golang.org/x/net/websocket`）
- **文件系统挂载**：WebDAV（`golang.org/x/net/webdav`）
- **上传协议**：tus 可恢复上传（手写实现）
- **定时任务**：`github.com/robfig/cron/v3`
- **日志**：标准库 `log/slog`（结构化 JSON 日志 + 按大小滚动）
- **视频播放**：ffprobe 探测分辨率 + ffmpeg 转封装/转码（外部二进制）
- **AI / IoT / 插件**：AI 管家（v0.21 实装：Ollama 生命周期 + 硬件自适应 + Eino 框架 + RAG + HomeAssistant 工具）、IoT（MQTT/米家）、插件系统

> 注意：设计文档里写的是 GORM、viper、zap、tusd 等库，但**实际代码**因为离线环境限制，用等价实现替代了（如手写 JWT、手写 tus、用 slog 替代 zap）。**以实际代码为准**，文档中凡是"说明：文档选用 XXX，但当前离线环境不可用"的注释，都是在讲这个替代关系。

---

## 2. 整体架构与模块划分

### 2.1 目录结构总览

```
smart-nas/
├── cmd/server/main.go        # 程序入口（main 函数）
├── internal/                 # 内部业务代码（不对外暴露）
│   ├── api/types/            # 统一响应格式 + WebSocket 消息类型
│   ├── auth/                 # 认证：JWT 签发/校验、登录
│   ├── security/             # 底层安全原语：Argon2id 密码哈希
│   ├── user/                 # 用户管理：角色、目录权限
│   ├── storage/              # 文件存储：元数据(SQLite) + 真实文件操作 + 回收站
│   ├── transport/            # 传输：上传/下载任务、进度推送
│   │   └── tusd/             # tus 可恢复上传协议实现
│   ├── play/                 # 视频在线播放：凭证/分辨率/转封装/转码
│   ├── webdav/               # WebDAV 挂载（系统级文件访问）
│   ├── ws/                   # WebSocket 连接管理（Hub）
│   ├── task/                 # 定时任务（cron）+ 异步 Worker
│   ├── settings/             # 界面可调的系统设置（TOML 持久化）
│   ├── config/               # 配置管理（TOML + 环境变量 + 热重载）
│   ├── store/                # 通用 TOML 键值存储（用户/上传任务用）
│   ├── server/               # HTTP 路由、中间件、API 处理器
│   ├── util/                 # 工具：系统状态/哈希/ID/路径/受保护文件
│   ├── ai/                   # AI 管家（v0.21）：Ollama 生命周期/硬件自适应/Eino/RAG/HA 工具
│   ├── iot/                  # IoT 接口（MQTT/米家/自动化）
│   └── plugin/               # 插件系统（骨架）
├── pkg/logger/               # 日志（slog 封装 + 滚动清理）
├── web/                      # 前端（原生 HTML/JS，无需构建）
│   ├── index.html
│   ├── css/style.css
│   └── js/app.js + js/video-js-8.24.0/
├── vendor/                   # 离线依赖（Go 1.14+ 自动使用）
├── config.toml               # 主配置
├── go.mod / go.sum           # 依赖清单
└── data/                     # 运行时数据（自动生成）
    ├── db/                   # users.toml / settings.toml
    ├── files/                # 文件 blob + metadata.db(SQLite) + trash/
    ├── tus/                  # 上传临时分块
    ├── logs/                 # 日志
    └── vectors/              # RAG 向量缓存
```

### 2.2 分层架构

设计文档把系统分成六层，实际代码可以简化理解为**四层**：

```
┌─────────────────────────────────────────────────────────────┐
│ 表现层：web/（index.html + app.js + style.css）              │
│   浏览器通过 HTTP/WebSocket 调用后端 API                      │
├─────────────────────────────────────────────────────────────┤
│ 网关层：internal/server（Gin 路由 + 中间件）                  │
│   TraceID → AccessLog → CORS → Auth(JWT) → RequireAdmin      │
├─────────────────────────────────────────────────────────────┤
│ 业务逻辑层：internal/{user,storage,transport,play,settings}  │
│   每个模块 = Service（业务逻辑）+ Repository（数据访问）        │
├─────────────────────────────────────────────────────────────┤
│ 数据访问层：                                                  │
│   SQLite（文件元数据）│ TOML 文件（用户/设置/上传任务）│ 真实文件系统 │
└─────────────────────────────────────────────────────────────┘
```

**关键设计思想：依赖注入（Dependency Injection）**

整个项目大量使用"**构造函数接收依赖**"的模式。比如 `main.go` 里：

```go
// 先创建底层服务
userRepo, _ := user.NewRepository(metaDir)          // 数据访问层
userSvc := user.NewService(userRepo, argon2Params(cfg)) // 业务层依赖数据层
authSvc := auth.NewService(userSvc, cfg.Auth)       // 认证依赖用户服务
```

每个服务只通过**接口/函数回调**依赖别人，而不是直接 new 一个全局对象。这样：
- 模块之间解耦，方便替换实现（比如把 TOML 换成数据库）；
- 方便测试（可以注入假的依赖）。

### 2.3 各模块职责速查

| 模块 | 职责 | 关键类型 |
|------|------|---------|
| `internal/api/types` | 统一响应 `{code, message, data, timestamp}` 和 WS 消息 | `Response`、`WSMessage` |
| `internal/auth` | 登录、签发/校验 JWT、修改密码 | `Service`、`JWT`、`Claims` |
| `internal/security` | Argon2id 密码哈希（独立包避免循环依赖） | `Argon2Params` |
| `internal/user` | 用户 CRUD、角色（master/admin/user）、目录权限判定 | `Service`、`User`、`Permission` |
| `internal/storage` | 文件元数据（SQLite）、真实文件操作、回收站、共享、秒传去重 | `Service`、`Repository`、`FileMeta` |
| `internal/transport` | 上传/下载任务、进度跟踪、WS 推送 | `Manager`、`FileTransferTask` |
| `internal/transport/tusd` | tus 协议（POST/HEAD/PATCH/DELETE） | `Handler`、`Upload` |
| `internal/play` | 视频播放凭证、ffprobe 分辨率、转封装/转码、Range 流 | `Service`、`Ticket`、`VideoInfo` |
| `internal/webdav` | WebDAV 挂载 + Basic Auth + 权限包装 | `Handler`、`permDir` |
| `internal/ws` | WebSocket 连接管理、广播、在线用户聚合 | `Hub`、`Client` |
| `internal/task` | cron 定时任务 + 固定并发 Worker | `Scheduler`、`Worker` |
| `internal/settings` | 界面可调设置（TOML 持久化） | `Service`、`Settings` |
| `internal/config` | 配置加载、环境变量覆盖、热重载 | `Manager`、`Config` |
| `internal/store` | 通用 TOML 键值存储（泛型） | `Store[K, V]` |
| `internal/server` | 路由注册、中间件、所有 HTTP 处理器 | `Server`、`Deps` |
| `internal/ai` | AI 管家（v0.21）：对话/工具循环/RAG 注入、部署模式、Ollama 客户端与进程生命周期、硬件调优预设、Eino 适配、HA 工具 | `Service`、`ollama.Lifecycle`、`hardware.Preset`、`eino.ChatModel` |
| `internal/iot` | IoT 设备注册表、MQTT、自动化引擎、米家客户端 | `Service` |
| `internal/util` | 系统状态采集、哈希、ID、路径校验、受保护文件规则 | 各种函数 |
| `pkg/logger` | 结构化日志 + 滚动清理 | `rollingFile` |

---

## 3. 核心业务流程代码导览（从 main 入口开始）

### 3.1 程序入口：`cmd/server/main.go`

`main` 函数是理解整个项目的钥匙。它按顺序做了 15 件事，**每件事都是"创建某个服务并交给下一个"**：

```go
func main() {
    // ① 解析命令行参数：-config 指定配置文件，-data 指定数据目录
    flag.StringVar(&configPath, "config", "config.toml", "配置文件路径")
    flag.StringVar(&dataDir, "data", "./data", "数据目录")
    flag.Parse()

    // ② 加载配置（config.NewManager）：读取 config.toml，支持环境变量覆盖
    cfgMgr, err := config.NewManager(configPath)
    cfg := cfgMgr.GetConfig()

    // ③ 初始化系统设置（settings.NewService）：设置页可改的参数（日志位置等）
    settingsSvc, _ := settings.NewService(metaDir)
    st := settingsSvc.Get()

    // ④ 初始化日志（logger.Init）：设置页的日志参数优先于 config.toml
    logger.Init(logCfg.Level, logCfg.Path, logCfg.MaxSize, ...)

    // ⑤ 创建根上下文（context.WithCancel），用于优雅退出
    ctx, cancel := context.WithCancel(context.Background())

    // ⑥ 初始化存储：SQLite 元数据库 + 文件存储服务
    storageRepo, _ := storage.NewRepository(dbPath, metaDir)
    storageSvc, _ := storage.NewService(storageRepo, fileRoot, cfg.Storage, ...)

    // ⑦ 初始化用户与认证
    userRepo, _ := user.NewRepository(metaDir)
    userSvc := user.NewService(userRepo, ...)
    userSvc.EnsureMaster(cfg.Auth.AdminUsername, cfg.Auth.AdminPassword) // 确保主人账号存在
    authSvc := auth.NewService(userSvc, cfg.Auth)

    // ⑧ 注入权限校验回调：存储服务在读写前询问"这个用户对这个路径有没有权限"
    storageSvc.SetPermFn(func(uid uint, path string, write bool) bool {
        return userSvc.CanAccess(uid, path, write)
    })

    // ⑨ 创建视频播放服务、WebSocket Hub、传输管理器
    playSvc := play.NewService(cfg.Play, storageSvc)
    hub := ws.NewHub(authSvc)
    tm := transport.NewManager(storageSvc, fileRoot, hub, nil)

    // ⑩ 插件 / AI / IoT（扩展接口，可选）
    // ⑪ tus 上传处理器
    // ⑫ WebDAV 处理器
    // ⑬ 定时任务调度器 + 异步 Worker
    // ⑭ 把所有依赖打包成 server.Deps，创建 HTTP 服务
    srv := server.New(server.Deps{ Cfg: cfgMgr, Auth: authSvc, Users: userSvc, ... })

    // ⑮ 优雅退出：监听 Ctrl+C 信号，收到后取消 context、关闭资源
    sig := make(chan os.Signal, 1)
    signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
    go func() { serverErr <- srv.Run(ctx, fmt.Sprintf(":%d", cfg.Server.Port)) }()
    select {
    case err := <-serverErr: ...
    case <-sig: logger.Info("收到退出信号，开始优雅关闭")
    }
    cancel()          // 通知所有依赖 context 的地方停止
    worker.Stop()     // 等待异步任务结束
    storageRepo.Close() // 元数据库安全落盘
    ...
}
```

**新手要点**：
- `flag` 包解析命令行参数，`os.Exit(1)` 在初始化失败时退出；
- `must(err)` 是一个小工具函数：出错就打印日志并退出；
- `context.WithCancel` 创建了一个"可取消的上下文"，后面所有需要停止的组件都监听它；
- 依赖注入的链条：`config → settings → logger → storage → user → auth → server`，顺序不能乱。

### 3.2 一次"登录"请求的完整旅程

这是理解"请求如何被处理"的最佳例子。前端点击登录 → 浏览器发 `POST /api/auth/login`：

**第 1 步：路由注册**（`internal/server/app.go` 的 `setupRoutes`）

```go
api := engine.Group("/api")
api.POST("/auth/login", s.login)   // 登录接口不需要认证
```

**第 2 步：处理器**（`internal/server/handlers_auth.go`）

```go
func (s *Server) login(c *gin.Context) {
    var req loginRequest
    if !bindJSON(c, &req) { return }   // 解析 JSON 请求体，失败返回 400
    res, err := s.deps.Auth.Login(req.Username, req.Password)
    if err != nil {
        c.JSON(http.StatusUnauthorized, types.Fail(types.CodeUnauthorized, "用户名或密码错误"))
        return
    }
    c.JSON(http.StatusOK, types.OK(res))  // 成功返回 {code:0, data:{token:...}}
}
```

**第 3 步：认证服务**（`internal/auth/service.go`）

```go
func (s *Service) Login(username, password string) (*LoginResult, error) {
    u, err := s.users.GetByUsername(username)   // 查用户
    if err != nil { return nil, errors.New("用户名或密码错误") }
    if !VerifyPassword(password, u.PasswordSalt, u.PasswordHash, s.argon) {
        return nil, errors.New("用户名或密码错误")   // 校验 Argon2id 密码
    }
    token, _ := s.jwt.Sign(Claims{UserID: u.ID, Username: u.Username, Role: u.Role})
    return &LoginResult{Token: token, ...}, nil
}
```

**第 4 步：JWT 签发**（`internal/auth/jwt.go`，手写 HS256）

```go
func (j *JWT) Sign(claims Claims) (string, error) {
    payload, _ := json.Marshal(claims)
    header := []byte(`{"alg":"HS256","typ":"JWT"}`)
    signingInput := b64e(header) + "." + b64e(payload)   // header.payload
    mac := hmac.New(sha256.New, j.secret)
    mac.Write([]byte(signingInput))
    sig := b64e(mac.Sum(nil))
    return signingInput + "." + sig, nil                 // header.payload.signature
}
```

**第 5 步：用户数据访问**（`internal/user/repository.go`，TOML 持久化）

```go
func (r *Repository) GetByUsername(username string) (*User, error) {
    for _, u := range r.store.All() {   // 遍历内存中的用户表
        if u.Username == username { return u, nil }
    }
    return nil, errors.New("用户不存在")
}
```

**完整链路**：`浏览器 → Gin 路由 → login handler → auth.Service.Login → user.Repository → security.VerifyPassword → JWT.Sign → 返回 token`。

### 3.3 一次"列出文件"请求的完整旅程

前端进入"文件"页 → `GET /api/files?parent_id=0`（parent_id=0 表示列磁盘列表）：

**第 1 步：路由 + 认证中间件**（`app.go`）

```go
authed := api.Group("")
authed.Use(Auth(authSvc))          // 所有 /api 下的接口都要先过 JWT 认证
s.registerFileRoutes(authed)       // 注册文件路由
```

`Auth` 中间件（`middleware.go`）做的事：从 `Authorization: Bearer xxx` 头取出 token → `authSvc.ValidateToken` 校验 → 把用户信息塞进 gin 的 Context（`c.Set("userID", u.ID)`）→ `c.Next()` 放行。

**第 2 步：处理器**（`handlers_files.go`）

```go
func (s *Server) listFiles(c *gin.Context) {
    files, err := s.deps.Storage.ListFiles(currentUID(c), parentID, c.Query("keyword"))
    ...
    out := make([]*storage.FileMeta, 0, len(files))
    for i := range files {
        out = append(out, files[i].Public())   // 脱敏：隐藏真实路径
    }
    c.JSON(http.StatusOK, types.OK(out))
}
```

**第 3 步：存储服务**（`internal/storage/service.go`）

```go
func (s *Service) ListFiles(userID uint, parentID uint, keyword string) ([]FileMeta, error) {
    if keyword != "" { return s.search(userID, keyword) }  // 搜索
    if parentID == 0 { return s.listDisks(userID) }        // 列磁盘
    return s.listDir(userID, parentID)                     // 列目录
}
```

`listDir` 的核心逻辑（**这是理解"文件系统如何与元数据同步"的关键**）：

```go
func (s *Service) listDir(userID uint, parentID uint) ([]FileMeta, error) {
    parent, _ := s.repo.Get(parentID)          // 从 SQLite 查父目录元数据
    // 权限校验：目录可读，或用户在其中拥有任意权限范围
    if !s.canRead(userID, parent.StoragePath) && !s.permScope(userID, parent.StoragePath) {
        return nil, fmt.Errorf("无权访问「%s」：未授予该目录的读权限", parent.Name)
    }
    entries, _ := os.ReadDir(parent.StoragePath)   // 读真实磁盘目录
    for _, e := range entries {
        name := e.Name()
        full := filepath.Join(parent.StoragePath, name)
        if IsProtectedEntry(full, name) || s.isDBArtifact(full) { continue } // 屏蔽系统文件
        info, _ := e.Info()
        if f := s.syncEntry(userID, parentID, parent.StoragePath, name, info); f != nil {
            // 受限用户只看到自己可读的条目
            if s.canRead(userID, f.StoragePath) || (f.IsDir && s.permScope(userID, f.StoragePath)) {
                out = append(out, *f)
            }
        }
    }
    return out, nil
}
```

**关键概念：元数据与真实文件系统是"两套数据"**。
- **真实文件系统**：文件实际存在磁盘上（如 `S:\照片\a.jpg`）；
- **元数据**（SQLite `file_metas` 表）：记录每个文件的 ID、名称、父目录、大小、路径等。
- 浏览目录时，`syncEntry` 会把真实目录项"同步"进元数据表（不存在则创建，大小变了则更新）。这就是为什么第一次浏览大目录会慢一点（要写入元数据），之后变快。

**第 4 步：权限判定**（`internal/user/service.go` 的 `CanAccess`）

```go
func (s *Service) CanAccess(userID uint, path string, write bool) bool {
    u, _ := s.repo.GetByID(userID)
    if u.Role == RoleMaster { return true }        // 主人全量放行
    if len(u.Permissions) == 0 { return true }     // 未配置权限默认全量
    for _, p := range u.Permissions {
        if pathMatches(path, p.Path) {             // 路径匹配（含子目录）
            if write { return p.Write }
            return p.Read || p.Write               // 写权限隐含读权限
        }
    }
    return false
}
```

**完整链路**：`浏览器 → Auth 中间件(校验JWT) → listFiles handler → storage.Service.ListFiles → repository(查元数据) + os.ReadDir(读磁盘) → user.Service.CanAccess(权限) → 返回脱敏列表`。

### 3.4 一次"上传文件"的完整旅程（tus 协议）

上传用的是 **tus 可恢复上传协议**，支持断点续传。流程分两步：

**第 1 步：初始化上传**（`POST /api/files/upload/init`）

```go
// handlers_files.go
func (s *Server) uploadInit(c *gin.Context) {
    var req transport.InitUploadRequest   // {filename, size, md5, parent_id}
    bindJSON(c, &req)
    req.UserID = currentUID(c)
    res, err := s.deps.Transport.InitUpload(&req)
    c.JSON(http.StatusOK, types.OK(res))  // 返回 upload_url
}
```

`transport.Manager.InitUpload` 会先做**秒传去重**：如果传了 MD5，且库里已有同 MD5 同大小的文件，直接返回"已存在"，不用真的上传：

```go
if req.MD5 != "" {
    if fileID, ok := m.storage.CheckDedup(req.MD5, req.Size); ok {
        return &InitUploadResponse{Dedup: true, FileID: fileID}, nil  // 秒传命中
    }
}
taskID := util.NewUUIDCompact()   // 否则生成任务 ID，返回 tus 上传地址
```

**第 2 步：tus 分块上传**（`/files/upload/<task_id>`，`internal/transport/tusd/server.go`）

tus 协议用 HTTP 方法表达语义：
- `POST`：创建上传会话（带 `Upload-Length` 头声明总大小）；
- `HEAD`：查询当前进度（返回 `Upload-Offset`）；
- `PATCH`：上传一块数据（body 是文件内容，`Upload-Offset` 头声明从哪开始）；
- `DELETE`：终止上传。

```go
func (h *Handler) Handle(c *gin.Context) {
    id := strings.TrimPrefix(c.Param("filepath"), "/")
    switch c.Request.Method {
    case http.MethodPost:   h.create(c, id)
    case http.MethodHead:   h.head(c, id)
    case http.MethodPatch:  h.patch(c, id)
    case http.MethodDelete: h.delete(c, id)
    ...
    }
}
```

上传过程中，`transport.Manager.UpdateProgress` 会通过 WebSocket 把进度推给前端（见 3.6）。

### 3.5 视频在线播放流程（`internal/play`）

这是项目里最有意思的模块，涉及外部进程调用和并发控制。流程：

1. **前端探测**：文件列表对视频文件调用 `GET /api/video/info?file=<id>`，后端用 **ffprobe** 探测分辨率/编码，返回播放模式；
2. **播放模式决策**（`classifyVideo`，纯函数便于测试）：

```go
func classifyVideo(ext string, height int, maxHeight int, codec string, ffmpegOK bool) (mode, playable, tooLarge, reason) {
    if !videoExts[ext] { return ModeRefuse, false, false, "unsupported" }  // 不支持格式
    if height > maxHeight {   // 超过 2K
        if ext == ".mp4" { return ModeDirect, true, true, "" }  // mp4 允许直连（可能卡顿）
        return ModeRefuse, false, true, "too_large"             // 其余拒绝
    }
    if !ffmpegOK { return ModeRefuse, false, false, "ffmpeg_missing" }
    if directPlayCodecs[codec] {   // 浏览器可播编码
        if ext == ".mp4" { return ModeDirect, true, false, "" }  // 直接 Range 流
        return ModeRemux, true, false, ""                        // 转封装
    }
    return ModeTranscode, true, false, ""                        // 实时转码
}
```

3. **创建播放凭证**（`POST /api/play/ticket`）：因为 `<video>` 标签无法附带 JWT 头，所以用"凭证 token"来认证播放请求；
4. **播放**（`GET /api/video?file=&token=`）：`http.ServeContent` 原生支持 HTTP Range（206 响应），实现拖拽秒定位；
5. **转封装**（`ensureRemux`）：`.mkv` 等容器用 ffmpeg `-c copy` 转成 MP4 缓存到 `data/cache/video/`，用**信号量**（`remuxSem chan struct{}`）限制并发数，用 `remuxTasks` map 让并发重复请求共享同一个任务。

**新手看点**：`exec.Command` 调用外部 ffmpeg/ffprobe、`chan struct{}` 做并发信号量、`sync.Mutex` 保护共享 map、后台 `sweepLoop` 定时清理过期凭证。

### 3.6 WebSocket 实时推送（`internal/ws`）

WebSocket 用于：上传进度推送、在线用户统计、心跳保活。

**连接建立**：`GET /ws?token=<JWT>` → `Hub.Handle` 校验 token → 升级为 WebSocket。

**每个连接两个 goroutine**（这是经典的 WebSocket 并发模型）：

```go
func (h *Hub) serveConn(conn *websocket.Conn, userID uint, username, role, device string) {
    client := &Client{..., send: make(chan []byte, 256)}  // 发送队列（带缓冲的 channel）
    h.register(client)
    defer h.unregister(client)
    go h.writePump(client)   // goroutine 1：串行写（从 send 队列取消息发出去）
    h.readPump(client)       // goroutine 2：读（接收客户端消息）
}
```

- **writePump**：`select` 监听 `client.send` 队列和心跳 ticker，串行写入连接。因为 `x/net/websocket` 不支持并发写，所以**所有写操作都走这一个 goroutine**；
- **readPump**：循环接收消息，处理 `ping`（回 `pong`）等。

**广播**（`Broadcast` / `BroadcastToUser`）：遍历所有连接（或某用户的连接），把消息塞进各自的 `send` 队列。用 `select ... default` 实现**非阻塞写入**（队列满了就丢弃，避免阻塞业务）。

**在线用户聚合**（`OnlineUsers`）：按用户 ID 聚合，统计每个用户的连接数和登录设备（pc/mobile，由 User-Agent 判定）。

### 3.7 定时任务与异步 Worker（`internal/task`）

- **Scheduler**：包装 `robfig/cron/v3`，`AddFunc("0 4 * * *", "trash-purge", fn)` 注册每天凌晨 4 点清理回收站的任务；
- **Worker**：固定并发数的任务队列。`NewWorker(4).Start()` 启动 4 个 goroutine，从 `queue chan Job` 取任务执行：

```go
func (w *Worker) run() {
    defer w.wg.Done()
    for {
        select {
        case <-w.ctx.Done(): return          // 收到取消信号就退出
        case job := <-w.queue:               // 取到任务就执行
            if err := job.Fn(w.ctx); err != nil { logger.Error(...) }
        }
    }
}
```

### 3.8 AI 模块代码导览（v0.21：管家 / 知识库 / Ollama 生命周期）

AI 模块是理解「**接口分层 + 外部进程管理 + 框架集成**」的绝佳样本，涉及 6 个子包：

```
internal/ai/
├── service.go        # Service 总装配：对话入口、工具循环、RAG 注入（这是大脑）
├── deploy.go         # 部署模式：single/dual 主备切换与降级（M6）
├── ollama/
│   ├── client.go     # Ollama REST 客户端（chat/embeddings/tags/ps）
│   ├── admin.go      # 管理扩展：预热/卸载/拉取/删除模型
│   └── lifecycle.go  # 进程生命周期：检测→拉起→就绪→预热→卸载→停止（M1）
├── hardware/         # 硬件检测与模型调优预设（M2）
├── eino/             # 字节 Eino 框架适配：ChatModel + 工具桥接（M3）
├── ha/               # HomeAssistant 客户端 + AI 工具（M7）
├── rag/              # 知识库：索引器/检索器/向量存储
├── tools/            # 工具注册表与文件/系统工具
└── conversation/     # 会话管理（内存 + 截断）
```

**① 启动主线（`cmd/server/main.go` 第 7 步）**——按依赖顺序装配：

```go
// 1) 硬件检测 → 生成调优预设（config.toml [ai.tune] 显式值优先）
info := hardware.Detect(ctx)                            // nvidia-smi 查 GPU/内存/CPU
preset := info.Resolve(cfg.AI.Tune, cfg.AI.DefaultModel) // GPU≥8GB→deepseek-r1:7b，否则 qwen3.5:9b

// 2) Ollama 生命周期：没运行就拉起，就绪后预热加载模型
lifecycle = ollama.NewLifecycle(ollamaClient, runCfg, dataDir)
lifecycle.EnsureRunning(ctx, preset.Model, preset.KeepAlive)

// 3) AI 服务（Eino 组件在 NewService 内部构建）
aiSvc, _ := ai.NewService(ai.Options{Config: cfg.AI, Preset: preset, ...})
aiSvc.StartDeployWatch(ctx)   // dual+auxiliary：远端探测 + 自动切换巡检
```

**② 进程生命周期管理（`ollama/lifecycle.go`）**——学习「如何安全管理外部进程」：

```go
// EnsureRunning 三种情况：
// a) Ping 通 → 凭 data/ollama.pid 判断：pid 存活 = 上一任实例，接管管理；否则 = 用户自启，不碰
// b) Ping 不通 + managed=true → exec 后台拉起 ollama serve（注入 OLLAMA_HOST 等环境变量）
// c) 拉起后轮询 Ping 直到就绪（超时 start_timeout），再空对话预热触发模型加载
//
// Shutdown（优雅关闭）：先 UnloadModel（keep_alive=0 释放显存）再停进程
// 关键安全约束：managed=false 时 Shutdown 直接 return——绝不杀用户自启的 Ollama
```

**③ Eino 框架适配（`eino/chatmodel.go`）**——学习「如何把自研客户端接到标准框架」：

```go
// ChatModel 实现 Eino 的 ToolCallingChatModel 接口：
//   Generate：拼 ChatRequest → ollamaClient.Chat → 响应转 schema.Message
//   Stream：ollamaClient.ChatStream 的 chunk 通道 → schema.Pipe 桥接为 Eino 流
//   WithTools：绑定工具定义（不可变副本），模型据此返回 tool_calls
// 工具执行交给 Eino ToolsNode：现有 tools.Registry 的工具经 AdaptTools 转为 Eino BaseTool
```

**④ 对话主循环（`ai/service.go`）**——工具调用与 RAG 注入：

```go
// Chat(ctx, userID, convID, content)：
//   1. 取/建会话，追加用户消息
//   2. RAG 检索（本机向量库或 dual 辅助机走主服务 HTTP）→ 拼进 system 提示词
//   3. boundChatModel()：按部署状态选模型（remoteActive 时用远端 client+模型）
//   4. 循环：模型生成 → 若返回 tool_calls → ToolsNode 并行执行 → 结果回传 → 继续生成
//   5. 追加助手回复并返回
```

**⑤ 主备双机（`ai/deploy.go`）**——学习「状态机 + 巡检降级」：

```go
// deployState 记录 mode/role/remoteActive；StartDeployWatch 仅 auxiliary 生效：
//   启动立即探测一次 → 之后每 check_interval 秒探测：
//   可达 → remoteActive=true（切换远端模型，卸载本机模型释放资源）
//   不可达 → remoteActive=false（降级回本机高级模型，记录告警）
// 辅助机 RAG：NewRemoteRetriever 登录主服务拿 JWT → POST /api/ai/rag/search（不建独立向量库）
```

**⑥ HA 工具（`ai/ha/`）**——学习「HTTP 客户端 + 工具注册 + 优雅降级」：

```go
// client.go：CheckConnection(GET /api/ + token) / ListStates(domain 过滤) / CallService
// tools.go：ha_list_devices / ha_get_state / ha_call_service 三个工具
// 降级：未启用 / 自检失败 → 不注册工具 → AI 对话自然提示"暂不支持设备控制"
// 验证：ha_test.go 用 httptest Mock HA 服务器跑通整条工具链（无需真实设备）
```

**HTTP API 一览**（`internal/server/handlers_ai.go`，全部走 JWT；管理接口再过 RequireAdmin）：

| 接口 | 说明 |
|------|------|
| `POST /api/ai/chat`、`POST /api/ai/chat/stream`（SSE） | 对话 / 流式对话 |
| `GET/POST/DELETE /api/ai/conversations` | 会话管理 |
| `POST /api/ai/rag/search`、`GET /api/ai/rag/status` | 知识库检索 / 状态（dual 辅助机经此查主服务） |
| `GET /api/ai/status` | 进程 / 已加载模型 / 硬件预设 / 部署模式总览 |
| `POST /api/ai/v1/chat/completions` | OpenAI 兼容代理（局域网第三方应用调用入口） |
| `GET/POST/DELETE /api/ai/models*`、`GET/PUT /api/ai/settings` | 模型管理（仅主人/管理员） |

**动手实验建议**：①`go run ./cmd/server` 后看日志中「硬件检测完成 / 硬件调优预设生效」两行，核对预设来源是 auto 还是 config；②注释 `[ai.ollama].managed=true` 再启动，观察「用户自启实例不做进程管理」日志；③`curl -H $AUTH http://localhost:8080/api/ai/status` 对照预设与部署状态；④改 `[ai.tune].model` 热重载，验证配置优先于自动检测。

---

## 4. 项目中用到的 Go 特性与库（结合代码示例）

> 这一章是给"只学过基础语法"的你补课。每个特性都给出**项目里的真实用法**。

### 4.1 context（上下文）

**是什么**：`context.Context` 是 Go 里传递"取消信号、超时、请求级数据"的标准方式。它像一根贯穿请求的"控制线"，父级取消时子级跟着取消。

**项目里的用法**：

1. **优雅退出**（`main.go`）：`ctx, cancel := context.WithCancel(context.Background())`，收到 Ctrl+C 后调用 `cancel()`，所有监听 `ctx.Done()` 的组件停止。

2. **HTTP 服务优雅关闭**（`app.go`）：

```go
func (s *Server) Run(ctx context.Context, addr string) error {
    ...
    select {
    case err := <-errCh: return err
    case <-ctx.Done():   // 主程序 cancel() 时触发
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        return s.httpSrv.Shutdown(shutdownCtx)  // 最多等 10 秒让请求处理完
    }
}
```

3. **请求取消**（`play/stream.go`）：转码时监听 `c.Request.Context().Done()`，客户端断开就停止转码，避免浪费 CPU。

**新手要点**：记住三句话——`context.Background()` 是根；`WithCancel/WithTimeout` 派生子上下文；`<-ctx.Done()` 表示"该停了"。

### 4.2 goroutine（协程）

**是什么**：`go 函数()` 就能开一个轻量级"线程"（协程）。Go 的并发模型是"不要通过共享内存来通信，而要通过通信来共享内存"。

**项目里的用法**：

```go
// main.go：HTTP 服务在后台 goroutine 里跑，主 goroutine 等信号
go func() { serverErr <- srv.Run(ctx, fmt.Sprintf(":%d", cfg.Server.Port)) }()

// ws/hub.go：每个 WebSocket 连接两个 goroutine（读 + 写）
go h.writePump(client)
h.readPump(client)

// task/worker.go：启动 N 个 worker goroutine 消费队列
for i := 0; i < w.size; i++ { w.wg.Add(1); go w.run() }

// app.go：指标采集每 15 秒跑一次
go func() {
    for range time.Tick(15 * time.Second) { ... }
}()
```

### 4.3 channel（通道）

**是什么**：channel 是 goroutine 之间通信的管道。`make(chan T, 容量)` 创建，`ch <- v` 发送，`v := <-ch` 接收。带缓冲的 channel 可以当"队列/信号量"用。

**项目里的用法**：

1. **发送队列**（`ws/hub.go`）：`send: make(chan []byte, 256)`，写 goroutine 从队列取消息，其他 goroutine 往队列塞消息。

2. **并发信号量**（`play/play.go`）：限制同时转封装的任务数：

```go
remuxSem: make(chan struct{}, cfg.RemuxConcurrency)  // 容量=最大并发数

func (s *Service) runRemux(src, dst string) error {
    s.remuxSem <- struct{}{}        // 占一个坑（满了会阻塞等待）
    defer func() { <-s.remuxSem }() // 释放坑
    ... // 执行 ffmpeg
}
```

3. **任务队列**（`task/worker.go`）：`queue: make(chan Job, 256)`，`Submit` 往队列塞任务，worker 从队列取任务。

4. **等待任务完成**（`play/play.go` 的 `remuxTask`）：`done chan struct{}`，转封装完成后 `close(t.done)`，等待方 `<-t.done` 解除阻塞。

5. **优雅退出**（`main.go`）：`sig := make(chan os.Signal, 1)`，`signal.Notify(sig, ...)` 把系统信号转成 channel 消息。

**新手要点**：`select` 语句可以同时监听多个 channel（谁有数据处理谁），配合 `default` 实现非阻塞。项目里大量使用这个模式。

### 4.4 sync 包（并发安全）

**是什么**：多个 goroutine 同时读写同一个 map/变量会出问题，需要用锁保护。

**项目里的用法**：

1. **`sync.RWMutex`**（读写锁）：读多写少时用。`ws/hub.go` 保护 `clients` map：

```go
type Hub struct {
    mu      sync.RWMutex
    clients map[*Client]struct{}
    ...
}
func (h *Hub) Count() int {
    h.mu.RLock(); defer h.mu.RUnlock()   // 读锁（可并发）
    return len(h.clients)
}
func (h *Hub) register(c *Client) {
    h.mu.Lock(); defer h.mu.Unlock()     // 写锁（独占）
    h.clients[c] = struct{}{}
}
```

2. **`sync.WaitGroup`**：等待一组 goroutine 结束。`task/worker.go` 的 `Stop()`：

```go
func (w *Worker) Stop() {
    w.cancel()      // 先发取消信号
    w.wg.Wait()     // 等所有 worker goroutine 退出
}
```

3. **`sync.Mutex`**：`play/play.go` 保护 `tickets`、`probeCache` 等 map。

**新手要点**：凡是"多个 goroutine 会同时访问的 map"，都要加锁。这是 Go 并发编程最常见的坑（`fatal error: concurrent map writes`）。

### 4.5 interface（接口）与依赖注入

**是什么**：interface 定义"行为契约"，不关心具体实现。项目里用它做**依赖注入**和**解耦**。

**项目里的用法**：

1. **回调函数注入**（`storage/service.go`）：存储服务不直接依赖用户服务，而是通过注入的函数回调：

```go
type PermFn func(userID uint, path string, write bool) bool

func (s *Service) SetPermFn(fn PermFn) { s.permFn = fn }  // main.go 注入

func (s *Service) canRead(userID uint, path string) bool {
    if s.permFn == nil { return true }   // 没注入就默认放行
    return s.permFn(userID, path, false)
}
```

2. **接口抽象**（`transport/manager.go`）：传输管理器只依赖"能广播消息"的接口，具体是 WebSocket Hub 还是别的，它不管：

```go
type Publisher interface {
    Broadcast(msg types.WSMessage)
    BroadcastToUser(userID uint, msg types.WSMessage)
}
// ws.Hub 实现了这个接口，所以可以传进去
tm := transport.NewManager(storageSvc, fileRoot, hub, nil)
```

3. **空接口 `any`**（Go 1.18 起 `interface{}` 的别名）：`types.OK(data interface{})` 表示"可以是任何类型"，配合 JSON 序列化使用。

### 4.6 泛型（Go 1.18+）

**是什么**：泛型让一个函数/类型可以处理多种类型，避免重复代码。

**项目里的用法**：`internal/store/store.go` 的通用键值存储：

```go
type Store[K comparable, V any] struct {
    path string
    mu   sync.RWMutex
    data map[K]V
}

// 用户仓库：key 是 string，value 是 *User
store.NewStore[string, *User](filepath.Join(dataDir, "users.toml"))
// 上传任务仓库：key 是 string，value 是 *Upload
store.NewStore[string, *Upload](filepath.Join(cfg.StoreDir, "uploads.toml"))
```

`comparable` 表示"可比较的类型"（能当 map 的 key），`any` 表示任意类型。

**进阶用法：用泛型构造器消除重复分支**（v0.26 `internal/config/config.go`）。
配置项设置原本是 100+ 个 `case` 分支，每个分支都重复「解析字符串 → 赋值 → 标记成功」。
现在用泛型函数**生成 setter 闭包**，把「解析」与「赋值」解耦：

```go
// parseSetter[T]：由「赋值函数」生成「解析字符串 + 赋值」的 setter
func parseSetter[T any](f func(*Config, T)) func(*Config, string) error {
    return func(c *Config, v string) error {
        var t T
        if _, err := fmt.Sscan(v, &t); err != nil {   // 按 T 推导解析目标类型
            return fmt.Errorf("无效的配置值 %q", v)
        }
        f(c, t)
        return nil
    }
}

// 登记时只写一行；类型参数由 lambda 签名自动推导（int / int64 / bool / float64 / uint32 …）
var configSetters = map[string]func(*Config, string) error{
    "server.port":        parseSetter(func(c *Config, v int) { c.Server.Port = v }),
    "tus.chunk_size":     parseSetter(func(c *Config, v int64) { c.Tus.ChunkSize = v }),
    "storage.trash_path": strSetter(func(c *Config, v string) { c.Storage.TrashPath = v }),
}
```

要点：泛型参数由传入的 lambda 签名**自动推导**（调用处无需写 `parseSetter[int](...)`）；
把「路径 → 行为」登记进 map，运行时查表执行——**新增配置项只需登记一行，调度逻辑不动**，
这也是「用数据（映射表）代替控制流（长 switch）」的典型手法。

### 4.7 第三方库逐个讲

#### Gin（Web 框架）

**作用**：路由、中间件、请求/响应处理。项目里所有 HTTP 接口都基于它。

```go
// 创建引擎 + 挂中间件（app.go）
s.engine = gin.New()
s.engine.Use(gin.Recovery(), TraceID(), AccessLog(), CORS())

// 分组路由：/api 下的接口
api := engine.Group("/api")
api.POST("/auth/login", s.login)

// 需要认证的分组：先过 Auth 中间件
authed := api.Group("")
authed.Use(Auth(authSvc))
authed.GET("/auth/me", s.me)
```

**新手要点**：`c *gin.Context` 是请求上下文，`c.JSON(code, obj)` 返回 JSON，`c.Query("x")` 取查询参数，`c.Param("id")` 取路径参数，`c.Get/Set` 在中间件和处理器之间传数据。

#### go-toml/v2（TOML 解析）

**作用**：读写 `config.toml`、`users.toml`、`settings.toml` 等配置文件。

```go
// config.go：把 TOML 文件内容解析进 Config 结构体
data, _ := os.ReadFile(configPath)
toml.Unmarshal(data, cfg)

// 结构体字段用 toml 标签对应配置项
type ServerConfig struct {
    Port int    `toml:"port"`
    Mode string `toml:"mode"`
}
```

**新手要点**：TOML 是"人类可读的配置文件格式"，`[server]` 表示一个表（对应结构体），`port = 8080` 是字段。结构体标签 `toml:"xxx"` 告诉解析器字段对应哪个配置项。

#### robfig/cron/v3（定时任务）

**作用**：cron 表达式调度。`0 4 * * *` 表示每天 4 点。

```go
// main.go
scheduler := task.NewScheduler(nil)
scheduler.Start()
scheduler.AddFunc("0 4 * * *", "trash-purge", func() {
    // 每天凌晨 4 点清理回收站中超过 trash_days 天的文件
})
```

#### golang.org/x/crypto/argon2（密码哈希）

**作用**：安全的密码哈希算法（比 MD5/SHA 安全得多，专门用于存密码）。

```go
// security/password.go
hash := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
```

**新手要点**：密码**绝不能**明文存储。Argon2id 是 OWASP 推荐的密码哈希算法，特点是"慢"（故意慢，让暴力破解成本高）。

#### golang.org/x/net/webdav + websocket

- `webdav`：实现 WebDAV 协议（Windows 资源管理器可以直接映射网络驱动器访问 NAS 文件）；
- `websocket`：实现 WebSocket 实时通信。

#### modernc.org/sqlite（SQLite 驱动）

**作用**：文件元数据存储。`modernc.org/sqlite` 是**纯 Go 实现**的 SQLite 驱动（不需要 CGO，离线可编译）。

```go
// repository_sqlite.go
db, _ := sql.Open("sqlite", dsn)   // 标准 database/sql 接口
db.SetMaxOpenConns(1)              // 单连接避免锁冲突
db.Exec(schema)                    // 建表
db.QueryRow(`SELECT ... FROM file_metas WHERE id=?`, id)
```

**新手要点**：`database/sql` 是 Go 标准库的数据库接口，`sql.Open` 打开连接，`db.Query/QueryRow/Exec` 执行 SQL。项目里时间字段用 Unix 纳秒整数存储（`nanos()`/`timeOf()` 转换），因为 `database/sql` 不会自动把 int64 转成 `time.Time`。

#### uuid（Go 1.27 标准库，ID 生成）

```go
// util/id.go —— v0.25 起改用 Go 1.27 内置的 uuid 包（RFC 9562），不再依赖 github.com/google/uuid
import "uuid" // 标准库，无需 go get

func NewUUID() string { return uuid.NewV4().String() }           // 标准 v4 UUID（含短横线）
func NewUUIDCompact() string {                                   // 32 字符无短横线（上传任务/会话 ID）
    return strings.ReplaceAll(uuid.NewV4().String(), "-", "")
}
```

**新手要点**：`uuid.NewV4()` 用加密安全随机数生成，碰撞概率可忽略；需要按时间排序的 ID（数据库索引友好）可用 `uuid.NewV7()`。

#### log/slog（标准库结构化日志）

**作用**：输出 JSON 格式的结构化日志，方便机器解析。

```go
// pkg/logger/logger.go
global = slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))
// 使用（项目里到处是）：
logger.Info("HTTP 服务启动", "addr", addr)
logger.Warn("磁盘初始化失败", "disk", d, "error", err)
logger.Error("初始化失败", "error", err)
```

**新手要点**：日志用"键值对"方式传参（`"addr", addr`），比 `fmt.Sprintf` 拼接更规范，也方便日志系统检索。

---

## 5. 本地开发环境搭建、运行、测试

### 5.1 环境准备

1. **安装 Go**：https://go.dev/dl/ ，推荐 1.27+（`go.mod` 声明 `go 1.27.1`，v0.25 起使用 Go 1.27 标准库 `uuid` 包与泛型工具）。安装后验证：

```powershell
go version
```

2. **依赖说明**：项目自带 `vendor/` 目录（离线依赖）。Go 检测到 vendor 目录会自动使用 `-mod=vendor`，**构建全程无需联网**。不需要执行 `go mod download`。

3. **（可选）ffmpeg/ffprobe**：视频在线播放需要。把 `ffmpeg.exe`、`ffprobe.exe` 放到 `smart-nas.exe` 同目录即可（v0.19 起支持随包分发）。不装也不影响其他功能。

### 5.2 构建

```powershell
cd smart-nas

# 编译全部包（验证代码能编译）
go build ./...

# 生成可执行文件
go build -o smart-nas.exe ./cmd/server
```

**新手要点**：
- `./...` 表示"当前目录及所有子目录的所有包"；
- 编译报错时，错误信息格式是 `文件:行号: 错误描述`，从第一个错误开始修。

### 5.3 运行

```powershell
# 方式一：可执行文件
.\smart-nas.exe -config config.toml -data ./data

# 方式二：go run（开发时常用，改代码后自动重跑）
go run ./cmd/server -config config.toml -data ./data
```

**启动成功的标志**：
- 日志出现 `HTTP 服务启动 addr=:8080`；
- 浏览器打开 `http://localhost:8080/`；
- 默认账号：`admin` / `admin123`（主人角色，密码只能改 `config.toml` 的 `[auth] admin_password`）。

**停止**：`Ctrl + C` 触发优雅退出（保存元数据、关闭日志）。

### 5.4 测试

项目有完善的单元测试和集成测试。运行全部测试：

```powershell
go test ./...
```

只看某个包的测试：

```powershell
go test ./internal/storage/...
go test ./internal/play/...
```

跑单个测试函数（带详细输出）：

```powershell
go test ./internal/play/ -run TestClassifyVideo -v
```

**新手要点**：
- 测试文件命名：`xxx_test.go`（与被测文件同目录）；
- 测试函数：`func TestXxx(t *testing.T)`；
- `-v` 显示每个测试的详细结果，`-run` 指定要跑的测试名（支持正则）。

**项目里值得读的测试**（学习怎么写测试）：
- `internal/play/play_test.go`：纯函数 `classifyVideo` 的 14 个用例（表格驱动测试）；
- `internal/storage/service_test.go`：存储服务集成测试；
- `internal/server/handlers_files_trash_test.go`：HTTP 接口链路测试（用 gin 的测试模式）。

### 5.5 常用开发命令速查

```powershell
go build ./...          # 编译检查
go vet ./...            # 静态检查（发现可疑代码）
go test ./...           # 跑全部测试
go run ./cmd/server ... # 直接运行
node --check web/js/app.js   # 检查前端 JS 语法（改了前端后跑）
```

---

## 6. 常见问题与调试技巧

### 6.1 常见问题（FAQ）

| 现象 | 原因 | 解决 |
|------|------|------|
| 构建报 `no required module provides package` | 没走 vendor 模式 | 确认 `vendor/` 存在，`go build ./...` 重试 |
| 启动日志中文乱码 | PowerShell 控制台编码是 GBK | `chcp 65001` 后重启终端 |
| 端口 8080 被占用 | 其他程序占用 | 改 `config.toml` 的 `server.port`，或 `$env:SMARTNAS_SERVER_PORT=9090` |
| 登录返回 401 | Token 过期/未登录 | 重新登录获取新 token |
| 忘记默认密码 | - | 主人密码由 `config.toml` 的 `[auth] admin_password` 决定，改后重启 |
| 视频无法在线播放 | 没装 ffmpeg/ffprobe | 把 `ffmpeg.exe`/`ffprobe.exe` 放到 exe 同目录 |
| 文件列表空白 | 前端 JS 报错 | 浏览器 F12 看 Console，`node --check web/js/app.js` 查语法 |
| 磁盘显示不全 | 元数据未同步 | v0.08 起每次列盘自动同步，重启服务即可 |

### 6.2 调试技巧

**1. 看日志（最重要）**

日志在 `data/logs/smart-nas.log`（JSON 格式）。每条请求都有 `trace_id`，可以按它串联一次请求的完整链路：

```json
{"time":"...","level":"INFO","msg":"HTTP 访问","method":"GET","path":"/api/files","status":200,"latency":"12ms","ip":"127.0.0.1","trace_id":"a1b2c3..."}
```

排查问题第一步：`Get-Content data/logs/smart-nas.log -Tail 50` 看最近日志。

**2. 用 curl 直接调 API**

不打开浏览器也能验证接口。先登录拿 token：

```powershell
$login = curl.exe -s -X POST http://localhost:8080/api/auth/login `
  -H "Content-Type: application/json" `
  -d '{"username":"admin","password":"admin123"}'
# 从返回里复制 token
$TOKEN = "<token>"
$AUTH = "Authorization: Bearer $TOKEN"

# 列文件
curl.exe -s "http://localhost:8080/api/files?parent_id=0" -H $AUTH
# 健康检查
curl.exe -s http://localhost:8080/healthz
```

**3. 断点调试（VS Code）**

- 安装 Go 扩展；
- 在 `main.go` 或目标函数打断点；
- 用 VS Code 的"运行和调试"启动（launch.json 配置 `program: ./cmd/server`，args 传 `-config config.toml -data ./data`）。

**4. 常见报错解读**

| 报错 | 含义 | 排查方向 |
|------|------|---------|
| `concurrent map writes` | 多个 goroutine 同时写 map 没加锁 | 检查是否在 goroutine 里直接改共享 map，应加锁或用 channel |
| `database is locked` | SQLite 并发写冲突 | 项目已用单连接 + WAL 缓解；检查是否有长事务 |
| `fatal error: all goroutines are asleep - deadlock!` | 死锁 | 检查 channel 是否没人消费/没人关闭 |
| `listen tcp :8080: bind: Only one usage...` | 端口被占用 | 换端口或杀占用进程 |
| `panic: runtime error: invalid memory address` | 空指针 | 检查是否对 nil 调用了方法 |

**5. 前端调试**

- 浏览器 F12 → Console 看 JS 报错；
- Network 面板看 API 请求和响应（能直接看到后端返回的 `{code, message}`）；
- 改前端后跑 `node --check web/js/app.js` 查语法。

### 6.3 项目特有的"坑"（前人踩过的）

1. **时间字段**：go-toml 无法把 `*time.Time` 编码为 TOML 日期时间（会退化成字符串且读不回来）。所以项目里时间字段用**值类型**（零值表示未设置），SQLite 里用**纳秒整数**存储。**新增时间字段时务必遵守这个约定**。

2. **受保护文件**：`$RECYCLE.BIN`、`pagefile.sys`、`System Volume Information` 等系统文件必须屏蔽（`util.IsProtectedName/IsProtectedPath`）。**新增任何文件操作时都要调用这个共用方法**，否则会暴露系统文件。

3. **磁盘根目录**：盘符根（如 `S:\`）不可删除/重命名/移动/复制（前后端双重防护）。**新增文件操作时记得判断 `f.IsDisk()`**。

4. **权限校验**：所有文件操作都要过 `storage.Service` 的 `canRead/canWrite`（内部调用 `user.Service.CanAccess`）。**不要绕过权限直接操作文件**。

5. **WebDAV 方法集**：gin 的 `Any()` 不包含 PROPFIND/MKCOL 等 WebDAV 扩展方法，必须显式注册（`webdav/handler.go` 的 `davMethods`）。

6. **`go run` 的 Ctrl+C 陷阱**（v0.26）：`go run` **不会把 Ctrl+C 转发给子进程**
   （[golang/go#40467](https://github.com/golang/go/issues/40467)），Ctrl+C 只结束 `go run`
   本身，它编译出的程序会变成**孤儿进程继续占用端口**——表现为「终端按了 Ctrl+C，服务却还在跑」。
   项目为此加了父进程看门狗（`cmd/server/parent_watch.go` + `parent_watch_windows.go` /
   `parent_watch_unix.go`）：以「可执行文件是否位于 `go-build*` 临时目录」**精确判定** go run
   场景，父进程退出即触发同一优雅关闭流程（直接运行编译产物时不启用，避免误退出）。
   自行编写跨平台子进程管理时可参考：Windows 用 `OpenProcess(SYNCHRONIZE)` +
   `WaitForSingleObject` 等待进程句柄（事件驱动、零 CPU 占用），Unix 用 `kill(pid, 0)` 轮询兜底。

---

## 7. 学习路线建议

按这个顺序读代码，由浅入深：

1. **先跑起来**：按第 5 章把项目跑起来，浏览器里点一遍所有功能；
2. **读 main.go**：理解依赖注入的装配顺序（第 3.1 节）；
3. **跟一个请求**：用 curl 或浏览器 F12 跟一次"登录"和"列文件"，对照第 3.2/3.3 节；
4. **读一个模块**：从 `internal/user` 开始（最简单），再看 `internal/storage`（最核心）；
5. **读并发代码**：`internal/ws`（channel + goroutine）、`internal/play`（信号量 + 外部进程）、`internal/task`（worker 池）；
6. **写一个测试**：模仿 `internal/play/play_test.go` 的表格驱动测试，给某个纯函数写测试；
7. **改一个小功能**：比如给文件列表加一列，从前端 `app.js` → 后端 `handlers_files.go` → `storage/service.go` 全链路走一遍。

**推荐先掌握的核心概念**（按重要性排序）：
1. Gin 的请求处理模型（Context、中间件、路由）；
2. 依赖注入（构造函数传依赖、回调注入）；
3. goroutine + channel + select（并发模型）；
4. sync 锁（并发安全）；
5. 分层架构（handler → service → repository）。

---

*本文档基于 smart-nas 项目 v0.20 代码编写。代码如有更新，以实际代码为准。*
