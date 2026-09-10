// main 智能家庭 NAS 服务入口。
//
// 负责加载配置、初始化各模块（存储 / 认证 / 传输 / WebSocket / AI /
// IoT / 插件 / 定时任务），装配 HTTP 路由并优雅启停。
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"smart-nas/internal/ai"
	"smart-nas/internal/ai/conversation"
	"smart-nas/internal/ai/ha"
	"smart-nas/internal/ai/hardware"
	"smart-nas/internal/ai/ollama"
	"smart-nas/internal/ai/rag"
	"smart-nas/internal/ai/tools"
	"smart-nas/internal/auth"
	"smart-nas/internal/backup"
	"smart-nas/internal/config"
	"smart-nas/internal/iot"
	"smart-nas/internal/play"
	"smart-nas/internal/plugin"
	"smart-nas/internal/security"
	"smart-nas/internal/server"
	"smart-nas/internal/settings"
	"smart-nas/internal/storage"
	"smart-nas/internal/task"
	"smart-nas/internal/transport"
	"smart-nas/internal/transport/tusd"
	"smart-nas/internal/user"
	"smart-nas/internal/webdav"
	"smart-nas/internal/ws"
	"smart-nas/pkg/logger"
)

func main() {
	var configPath string
	var dataDir string
	flag.StringVar(&configPath, "config", "config.toml", "配置文件路径")
	flag.StringVar(&dataDir, "data", "./data", "数据目录")
	flag.Parse()

	// 1. 加载配置
	cfgMgr, err := config.NewManager(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}
	cfg := cfgMgr.GetConfig()

	// 2. 数据目录与系统设置（设置中包含日志位置/大小/保留天数，先于日志初始化加载）
	dataDir, _ = filepath.Abs(dataDir)
	metaDir := filepath.Join(dataDir, "db")
	fileRoot := cfg.Storage.Root
	if !filepath.IsAbs(fileRoot) {
		fileRoot = filepath.Join(fileRoot)
	}
	fileRoot, _ = filepath.Abs(fileRoot)

	settingsSvc, err := settings.NewService(metaDir)
	must(err)
	st := settingsSvc.Get()

	// 3. 初始化日志：设置页（settings.toml）中的日志参数优先于 config.toml；
	//    设置中的路径无效时逐级回退（config 默认路径 → 仅控制台），避免因
	//    一条无效的日志设置导致服务无法启动。
	logCfg := cfg.Log
	if st.LogPath != "" {
		logCfg.Path = st.LogPath
	}
	if st.LogMaxSize > 0 {
		logCfg.MaxSize = st.LogMaxSize
	}
	if st.LogMaxAge > 0 {
		logCfg.MaxAge = st.LogMaxAge
	}
	if logCfg.MaxSize <= 0 {
		logCfg.MaxSize = 100
	}
	if logCfg.MaxAge <= 0 {
		logCfg.MaxAge = 30
	}
	if err := logger.Init(logCfg.Level, logCfg.Path, logCfg.MaxSize, logCfg.MaxBackups, logCfg.MaxAge); err != nil {
		fmt.Fprintf(os.Stderr, "警告：按系统设置初始化日志失败（%v），回退 config.toml 默认路径\n", err)
		def := cfg.Log
		if def.MaxSize <= 0 {
			def.MaxSize = 100
		}
		if def.MaxAge <= 0 {
			def.MaxAge = 30
		}
		if err2 := logger.Init(def.Level, def.Path, def.MaxSize, def.MaxBackups, def.MaxAge); err2 != nil {
			fmt.Fprintf(os.Stderr, "警告：默认日志路径初始化失败（%v），仅输出到控制台\n", err2)
			_ = logger.Init(def.Level, "", def.MaxSize, def.MaxBackups, def.MaxAge)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 4. 初始化存储
	// 元数据库（SQLite）：默认 root/metadata.db（./data/files/metadata.db），
	// 可在 config.toml [storage] metadata_db 指定目录或完整 .db 路径
	dbPath := resolveDBPath(cfg.Storage.MetadataDB, fileRoot)
	storageRepo, err := storage.NewRepository(dbPath, metaDir)
	must(err)
	storageSvc, err := storage.NewService(storageRepo, fileRoot, cfg.Storage, argon2Params(cfg))
	must(err)
	// 默认回收站：运行目录下 data/trash（设置页/config 自定义位置优先）
	storageSvc.SetDefaultTrashPath(filepath.Join(dataDir, "trash"))

	// 5. 用户与认证
	userRepo, err := user.NewRepository(metaDir)
	must(err)
	userSvc := user.NewService(userRepo, argon2Params(cfg))
	must(userSvc.EnsureMaster(cfg.Auth.AdminUsername, cfg.Auth.AdminPassword))
	authSvc := auth.NewService(userSvc, cfg.Auth)

	// 5.1 注入目录权限校验回调（存储服务在读写前校验用户对该路径的权限）
	storageSvc.SetPermFn(func(uid uint, path string, write bool) bool {
		return userSvc.CanAccess(uid, path, write)
	})
	// 注入权限范围回调：受限用户可见"包含其被授权子路径"的磁盘/目录，便于逐级导航
	storageSvc.SetPermScopeFn(func(uid uint, dir string) bool {
		return userSvc.HasScopeUnder(uid, dir)
	})

	// 5.2 应用回收站位置（设置页优先，其次 config.toml）
	if st.TrashPath != "" {
		storageSvc.SetTrashPath(st.TrashPath)
	} else if cfg.Storage.TrashPath != "" {
		storageSvc.SetTrashPath(cfg.Storage.TrashPath)
	}

	// 5.3 视频在线播放服务（ffprobe 分辨率检测 / 转封装 / 播放凭证）
	playSvc := play.NewService(cfg.Play, storageSvc)

	// 5.4 备份还原服务（v0.20：任务/热备份/增量/生命周期/USB 绑定触发/邮件通知）
	var backupSvc *backup.Service
	if cfg.Backup.Enabled {
		dbPath := cfg.Backup.DBPath
		if dbPath == "" {
			dbPath = filepath.Join(dataDir, "backup", "backup.db")
		}
		logPath := cfg.Backup.LogPath
		if logPath == "" {
			logPath = filepath.Join(dataDir, "logs", "backup.log")
		}
		backupSvc, err = backup.NewService(dbPath, logPath)
		must(err)
		// 预检查预留：源与目标均为绝对路径（任务创建时校验）
		must(backupSvc.Start())
		logger.Info("备份还原服务已启动", "db", dbPath, "log", logPath)
	}

	// 5. 实时通道与传输
	hub := ws.NewHub(authSvc)
	tm := transport.NewManager(storageSvc, fileRoot, hub, nil)

	// 6. 插件系统
	pluginMgr := plugin.NewManager(cfg.Plugin, filepath.Join(dataDir, "plugins"))
	if cfg.Plugin.Enabled {
		_ = pluginMgr.LoadPlugins()
	}
	hookEmitter := pluginMgr

	// 7. AI 管家 + 知识库（v0.21）：硬件自适应 → Ollama 生命周期 → Eino 服务
	var aiSvc *ai.Service
	var lifecycle *ollama.Lifecycle
	var hwInfo *hardware.Info
	var haClient *ha.Client // HomeAssistant 客户端（配置完整即创建，v0.23 设备管理 API 使用）
	if cfg.AI.OllamaHost != "" {
		// v0.23 部署模式 auto 判定：配置了服务端地址且可达 → 辅机；否则 → 服务端
		cfg = resolveDeployMode(cfg, cfg.AI.Deploy.RemoteHost != "")
		ollamaClient := ollama.NewClient(cfg.AI.OllamaHost, 90*time.Second)

		// M2 硬件检测与调优预设（config.toml [ai.tune] 显式配置优先）
		hwCtx, hwCancel := context.WithTimeout(ctx, 15*time.Second)
		info := hardware.Detect(hwCtx)
		hwCancel()
		hwInfo = &info
		preset := recomputePreset(info, cfg.AI)

		// M1 Ollama 生命周期：检测 → 拉起 → 就绪 → 预热
		lifecycle = ollama.NewLifecycle(ollamaClient, ollama.OllamaRunConfig{
			Managed:      cfg.AI.Ollama.Managed,
			Binary:       cfg.AI.Ollama.Binary,
			BindHost:     cfg.AI.Ollama.BindHost,
			StartTimeout: cfg.AI.Ollama.StartTimeout,
			AutoWarmup:   cfg.AI.Ollama.AutoWarmup,
			NumParallel:  preset.NumParallel,
		}, dataDir)
		if _, err := lifecycle.EnsureRunning(ctx, preset.Model, preset.KeepAlive); err != nil {
			logger.Warn("Ollama 生命周期管理异常（AI 功能可能不可用）", "error", err)
		}

		convs := conversation.NewManager(40)
		toolsReg := tools.NewRegistry(storageSvc)
		toolsReg.Register(tools.DefaultTools(storageSvc)...)
		toolsReg.Register(tools.SystemTools()...)
		toolsReg.Register(tools.DeviceTools(toolsReg)...) // DeviceProvider 由 IoT 注入

		// M7 HomeAssistant 工具（未配置 / 自检失败时降级为不注册；客户端始终返回供设备管理 API）
		haClient = registerHATools(ctx, toolsReg, cfg.AI.HomeAssistant)

		// RAG：single/primary 用本机索引；dual+auxiliary 走主服务（不重复建向量库）
		var retriever ai.RagRetriever
		var indexer *rag.Indexer
		if cfg.AI.Deploy.Mode == "dual" && cfg.AI.Deploy.Role == "auxiliary" && cfg.AI.Deploy.RemoteAPI != "" {
			topK := cfg.AI.RAG.TopK
			if topK <= 0 {
				topK = 5
			}
			retriever = ai.NewRemoteRetriever(cfg.AI.Deploy.RemoteAPI,
				cfg.AI.Deploy.RemoteUsername, cfg.AI.Deploy.RemotePassword, topK)
			logger.Info("双机模式辅助机：RAG 统一走主服务", "remote_api", cfg.AI.Deploy.RemoteAPI)
		} else if cfg.AI.RAG.Enabled {
			vs, verr := rag.NewInMemoryStore(filepath.Join(dataDir, "vectors", "store.json"))
			if verr != nil {
				logger.Warn("向量存储初始化失败，RAG 不可用", "error", verr)
			} else {
				retriever = rag.NewRetriever(vs, ollamaClient, cfg.AI.RAG, cfg.AI.EmbeddingModel)
				indexer = rag.NewIndexer(vs, ollamaClient, storageSvc, cfg.AI.RAG, cfg.AI.EmbeddingModel)
			}
		}

		svc, aerr := ai.NewService(ai.Options{
			Config:    cfg.AI,
			Preset:    preset,
			Client:    ollamaClient,
			Convs:     convs,
			Registry:  toolsReg,
			Retriever: retriever,
			Indexer:   indexer,
		})
		if aerr != nil {
			logger.Warn("AI 服务初始化失败", "error", aerr)
		} else {
			aiSvc = svc
			// M6 双机模式辅助机：远端探测 + 自动切换 / 降级巡检
			aiSvc.StartDeployWatch(ctx)
		}
		_ = haClient // 已通过工具注册接入
	}

	// 8. IoT（注册表 + 自动化）；设备能力注入 AI 工具
	iotSvc := iot.NewService(cfg.IoT, hub)
	if aiSvc != nil {
		// DeviceProvider 注入需在 AI 服务创建前？工具经 Registry 动态查表执行，
		// 注入顺序无影响；此处保持装配期注入
	}

	// 9. tus 上传
	var tusHandler *tusd.Handler
	tusPrefix := cfg.Tus.PathPrefix
	if cfg.Tus.Enabled {
		tusHandler, err = tusd.NewHandler(cfg.Tus, storageSvc, tm, hub, hookEmitter, tusAuthenticate(authSvc))
		must(err)
	} else {
		tusHandler = nil
	}

	// 10. WebDAV（挂载到磁盘根目录；未配置磁盘时使用 root）
	var davHandler *webdav.Handler
	var davPrefix string
	if cfg.Storage.WebDAVEnabled {
		davRoot := fileRoot
		if len(cfg.Storage.Disks) > 0 {
			davRoot = cfg.Storage.Disks[0]
			if !filepath.IsAbs(davRoot) {
				davRoot, _ = filepath.Abs(davRoot)
			}
		}
		davHandler = webdav.NewHandler(authSvc, davRoot)
		davPrefix = cfg.Storage.WebDAVPrefix
		if davPrefix == "" {
			davPrefix = "/dav"
		}
	}

	// 11. 定时任务
	scheduler := task.NewScheduler(nil)
	scheduler.Start()
	worker := task.NewWorker(4).Start()
	registerJobs(scheduler, storageSvc, cfg)

	// 12. IoT 启动（MQTT 连接等）
	_ = iotSvc.Start()

	// 13. 配置热重载
	cfgMgr.WatchConfig()

	// 14. 装配 HTTP 服务
	restartCh := make(chan struct{}, 1) // 网页重启请求（v0.23，缓冲 1 保证 handler 非阻塞）
	deps := server.Deps{
		Cfg:         cfgMgr,
		Auth:        authSvc,
		Users:       userSvc,
		Storage:     storageSvc,
		Settings:    settingsSvc,
		Transport:   tm,
		Tus:         tusHandler,
		Hub:         hub,
		AI:          aiSvc,
		Lifecycle:   lifecycle, // Ollama 进程生命周期管理（v0.21）
		Hardware:    hwInfo,    // 硬件检测结果（v0.21）
		HAClient:    haClient,  // HomeAssistant 客户端（v0.23 设备管理 API）
		RestartCh:   restartCh, // 进程级重启通道（v0.23）
		IoT:         iotSvc,
		Plugins:     pluginMgr,
		Scheduler:   scheduler,
		Worker:      worker,
		WebDAV:      davHandler,
		WebDAVPrefix: davPrefix,
		TusPrefix:    tusPrefix,
		Play:         playSvc,
		Backup:       backupSvc,
		DataDir:      dataDir,
	}
	srv := server.New(deps)

	// 14.1 配置热更新回调（v0.23）：AI 模型 / 调优 / 部署参数变更即时生效
	//（此前 ReloadConfig 无装配点为死代码）；mode/role 等重启项由运行态解析保证
	if aiSvc != nil {
		cfgMgr.OnChange(func(c *config.Config) {
			rc := resolveDeployMode(c, c.AI.Deploy.RemoteHost != "")
			aiSvc.ReloadConfig(rc.AI, recomputePreset(*hwInfo, rc.AI))
		})
	}

	// 15. 优雅退出（含网页触发的进程级重启 v0.23）
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Run(ctx, fmt.Sprintf(":%d", cfg.Server.Port))
	}()

	restartRequested := false
	select {
	case err := <-serverErr:
		if err != nil {
			logger.Error("HTTP 服务异常退出", "error", err)
		}
	case <-sig:
		logger.Info("收到退出信号，开始优雅关闭")
	case <-restartCh:
		restartRequested = true
		logger.Info("收到网页重启请求，开始优雅重启")
	}
	cancel()
	worker.Stop()
	if backupSvc != nil {
		backupSvc.Close()
	}
	_ = storageRepo.Close() // 元数据库安全落盘
	scheduler.Stop()
	iotSvc.Stop()
	playSvc.Close()
	hub.Shutdown()
	pluginMgr.ScriptRuntime()
	// v0.21 M1：优雅关闭 Ollama——先卸载模型释放显存 / 内存，
	// 再停止本服务拉起的实例（用户自启的 Ollama 不受影响）
	if lifecycle != nil {
		model := ""
		if aiSvc != nil {
			model = aiSvc.Preset().Model
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 60*time.Second)
		lifecycle.Shutdown(shutdownCtx, model)
		shutdownCancel()
	}
	_ = logger.Close()
	// v0.23 网页触发的进程级重启：所有资源（端口 / 日志句柄 / SQLite / Ollama）
	// 已优雅关闭，此时重新拉起自身进程，父进程退出——新进程接管端口，无竞争窗口
	if restartRequested {
		if err := relaunchSelf(); err != nil {
			fmt.Fprintf(os.Stderr, "服务重启失败: %v\n", err)
			os.Exit(1)
		}
	}
}

// resolveDeployMode 解析部署模式 auto（v0.23）：配置了服务端地址（remote_host）
// 且可达 → 辅机（dual/auxiliary）；未配置或不可达 → 服务端（dual/primary）。
// 仅启动时判定一次，运行中不因断连切换身份（辅助机远端探测由 StartDeployWatch 负责）。
// 返回浅拷贝（仅覆盖 AI.Deploy 运行态），持久化配置保留 auto 原值；非 auto 原样返回。
func resolveDeployMode(cfg *config.Config, hasAddr bool) *config.Config {
	d := cfg.AI.Deploy
	if d.Mode != "auto" {
		return cfg
	}
	out := *cfg
	out.AI.Deploy.Mode = "dual"
	if !hasAddr {
		out.AI.Deploy.Role = "primary"
		logger.Info("部署模式 auto：未配置服务端地址，以服务端模式启动")
		return &out
	}
	pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ollama.NewClient(d.RemoteHost, 90*time.Second).Ping(pctx) == nil {
		out.AI.Deploy.Role = "auxiliary"
		logger.Info("部署模式 auto：服务端可达，以辅机模式启动", "remote_host", d.RemoteHost)
	} else {
		out.AI.Deploy.Role = "primary"
		logger.Warn("部署模式 auto：服务端不可达，以服务端模式启动", "remote_host", d.RemoteHost)
	}
	return &out
}

// recomputePreset 计算硬件调优预设（config.toml [ai.tune] 显式配置优先）；
// 抽取自启动装配逻辑，供启动与配置热更新回调复用
func recomputePreset(info hardware.Info, aiCfg config.AIConfig) hardware.Preset {
	preset := info.Resolve(aiCfg.Tune, aiCfg.DefaultModel)
	// 生效模型：config.default_model 仍可作为显式覆盖（保持旧行为：配置了就生效）
	if aiCfg.Tune.Model == "" && aiCfg.DefaultModel != "" && aiCfg.DefaultModel != "qwen2:7b" {
		preset.Model = aiCfg.DefaultModel
	}
	return preset
}

// resolveDBPath 解析元数据库位置：空 → root/metadata.db；以分隔符结尾或
// 无 .db 扩展名视为目录，追加默认文件名
func resolveDBPath(configured, root string) string {
	p := strings.TrimSpace(configured)
	if p == "" {
		return filepath.Join(root, "metadata.db")
	}
	if !strings.HasSuffix(p, ".db") {
		return filepath.Join(p, "metadata.db")
	}
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return p
}

// must 出错即退出
func must(err error) {
	if err != nil {
		logger.Error("初始化失败", "error", err)
		os.Exit(1)
	}
}

// argon2Params 由 config 派生 Argon2 参数
func argon2Params(cfg *config.Config) security.Argon2Params {
	return security.Argon2Params{
		Time:    cfg.Auth.Argon2Time,
		Memory:  cfg.Auth.Argon2Memory,
		Threads: cfg.Auth.Argon2Threads,
		KeyLen:  cfg.Auth.Argon2KeyLen,
		SaltLen: cfg.Auth.Argon2SaltLen,
	}
}

// tusAuthenticate 从 tus 请求的 Authorization 头校验 JWT（可选认证）
func tusAuthenticate(authSvc *auth.Service) func(r *http.Request) (uint, error) {
	return func(r *http.Request) (uint, error) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			return 0, fmt.Errorf("缺少认证信息")
		}
		u, _, err := authSvc.ValidateToken(token)
		if err != nil {
			return 0, err
		}
		return u.ID, nil
	}
}

// registerJobs 注册内置定时任务：回收站清理（每日 04:00）
func registerJobs(s *task.Scheduler, storageSvc *storage.Service, cfg *config.Config) {
	_ = s.AddFunc("0 4 * * *", "trash-purge", func() {
		trashDays := cfg.Storage.TrashDays
		if trashDays <= 0 {
			trashDays = 30
		}
		cutoff := time.Now().AddDate(0, 0, -trashDays)
		// 清理所有用户回收站
		_, _ = storageSvc.PurgeTrash(0, cutoff)
		logger.Info("回收站定时清理完成")
	})
}

// registerHATools 注册 HomeAssistant AI 工具（v0.21 M7）。
// v0.23：配置完整（地址 + 令牌）时始终创建并返回客户端（供设备管理 API 与状态查询）；
// 未启用 / 自检失败时仅跳过 AI 工具注册（对话中表现为「暂不支持设备控制」）。
func registerHATools(ctx context.Context, reg *tools.Registry, cfg config.HAConfig) *ha.Client {
	if cfg.BaseURL == "" || cfg.Token == "" {
		logger.Info("HomeAssistant 未配置完整（地址 / 令牌缺失），智能家居不可用")
		return nil
	}
	client := ha.NewClient(cfg.BaseURL, cfg.Token)
	if !cfg.Enabled {
		logger.Info("HomeAssistant 未启用（enabled=false），跳过智能家居工具注册")
		return client
	}
	if err := client.CheckConnection(ctx); err != nil {
		logger.Warn("HomeAssistant 连通性自检失败，智能家居工具不可用（对话中将给出降级提示）",
			"base_url", cfg.BaseURL, "error", err)
		return client
	}
	for _, t := range ha.Tools(client) {
		// ha.Tool 与 tools.Tool 方法集一致，逐个注册完成接口转换
		reg.Register(t)
	}
	logger.Info("HomeAssistant 已接入，智能家居 AI 工具就绪", "base_url", cfg.BaseURL)
	return client
}