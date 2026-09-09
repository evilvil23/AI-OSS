# 实施提示词：AI 管家 + AI 知识库 + Ollama 管理 + 智能家居控制（优化版）

> 本提示词基于 
>
> `smart-nas`
>
>  项目（
>
> `S:\Code\Go\project\AI-OSS`
>
> ）现状优化，可直接整段复制给 AI 助手执行。
> 优化要点：补充项目上下文防止重复造轮子、里程碑化任务分解、修正模型 / 硬件表述歧义、补充验收标准与边界。



***

## 角色

你是一名资深的 Go 后端工程师，精通 Ollama 本地推理、字节 Eino 框架、RAG 检索增强生成、跨平台进程生命周期管理。你的任务是在现有 smart-nas 项目上实装「AI 管家 + AI 知识库 + Ollama 全生命周期管理 + 智能家居控制」，并同步更新项目文档。全程使用中文沟通。

## 项目现状（动工前必须完整阅读，禁止凭印象实现）

项目根目录：`S:\Code\Go\project\AI-OSS`



* 主程序：`smart-nas/`（Go 1.25、Gin、modernc SQLite、TOML 配置、vendor 离线依赖）

* 已有模块（在其上扩展，**不得重复造轮子**）：


  * `internal/ai/ollama/client.go` —— Ollama REST 客户端（`/api/chat` 流式与非流式、`/api/embeddings`、`/api/tags`、Ping）

  * `internal/ai/service.go` —— AI 服务（工具调用循环、RAG 上下文注入、会话管理、系统提示词）

  * `internal/ai/rag/` —— 索引器 / 检索器 / 向量存储（chromem 嵌入式）

  * `internal/ai/tools/` —— 工具注册表与文件 / 系统工具

  * `internal/ai/conversation/` —— 会话管理

  * `internal/iot/` —— 设备注册表、MQTT 客户端、自动化引擎、米家 OpenAPI 客户端

* 配置：`smart-nas/config.toml` 已含 `[ai]`、`[ai.rag]`、`[ai.rag.qdrant]`、`[iot.mihome]`、`[iot.mqtt]` 等配置段

* 文档（本任务需更新的 4 个文件）：


  * `README.md`（Windows 本地运行指南）

  * `md/需求文档.md`（当前为 v0.01 备份功能需求）

  * `md/基于 Ollama + Go 的智能家庭 NAS 系统开发文档v0.1.md`（含 §2.5 AI 管家、§2.6 智能家居、附录 B 模型推荐、附录 C 版本记录）

  * `md/smart-nas-Go新手学习指南.md`

## 硬件与部署目标

部署模式**可选开启**（config.toml 配置 `mode = "single" | "dual"`，默认 `single`），三种形态互不依赖、切换无需改代码：

1. **单机模式 · 大主机独立**（Windows，RTX 4070 **12GB 显存** + 32GB 内存）：默认模型 `deepseek-r1-distill-qwen-7b:q4_K_M`（Q4\_K\_M 约 4.2–5.5GB 显存，可全量加载到 GPU），按 M2 硬件自适应与调优预设运行

2. **单机模式 · N100 小主机独立**（无独显，CPU 推理）：默认模型 `qwen3.5-7b-instruct:q4_K_M`（若 Ollama 官方库不存在该精确 tag，选择最接近的 7B 级 Q4\_K\_M 量化模型，记录实际使用的 tag 及原因），低配模型、低资源占用常驻

3. **主备双机模式**（可选开启）：N100 小主机作为常驻主服务（primary，低配模型 + RAG 默认开启），大主机作为辅助机（auxiliary）随时接入；辅助机连接时自动切换为远端模型并调用主服务侧 RAG，断线自动降级回本机高级模型

## 硬性约束



1. 技术栈：Go 1.25 + Eino（`github.com/cloudwego/eino`）+ Ollama（本地推理），沿用现有 Gin 分层架构

2. 保持现有代码风格与分层：`internal/xxx` 包、`pkg/logger` 日志、TOML 配置支持热更新

3. 模型选择与调优参数必须可配置（config.toml 优先），禁止写死；自动检测结果仅作默认值

4. 进程管理需跨平台（当前 Windows / 未来 N100 Linux）；Windows 下用 `os/exec` 后台进程方式并记录 PID，Linux 下可退化为 systemd 或直接 exec

5. 保留现有对外 API 与前端行为不变，Eino 改造是内部实现替换，前端无感

6. 安全：局域网暴露的 AI 接口必须走既有 JWT 鉴权；Ollama 绑定地址与防火墙放行需在文档中说明

## 任务分解（按里程碑推进，每步可编译、可验证）

### M1 Ollama 生命周期管理



* 服务端启动时：检测 Ollama 是否已在运行（Ping `/api/tags`）；未运行则后台拉起 `ollama serve`（记录 PID），等待就绪（最多 N 秒）后按硬件适配规则加载默认模型（空对话预热触发加载）

* 服务端优雅关闭时：先以 `keep_alive=0` 调用 `/api/generate` 卸载模型释放显存 / 内存，再停止 Ollama 进程 ——**仅停止本服务拉起的实例，绝不杀掉用户自启的 Ollama**

* 提供状态查询 API：Ollama 进程状态、已加载模型（等价 `ollama ps`）、显存 / 内存占用

### M2 硬件自适应与模型调优



* 启动时检测：NVIDIA GPU 是否存在及显存大小、总内存、CPU 型号

* 判定规则（可配置，config.toml 可整体覆盖）：


  * 有 NVIDIA GPU 且显存 ≥ 8GB → `deepseek-r1-distill-qwen-7b:q4_K_M`，预设 `num_ctx=8192`、`keep_alive` 常驻、`num_parallel=1~2`（按 12GB 显存核算，GPU 全量加载）

  * 无 GPU 或显存 < 8GB（如 N100）→ `qwen3.5-7b-instruct:q4_K_M`，预设 `num_ctx=4096`、`keep_alive=5m`、`num_parallel=1`，避免内存挤占

* 调优参数保存为「硬件配置预设」，记录实际生效值到日志，便于核验

* 硬件自适应在本机直连（单机模式 / 双机模式主服务）时生效；双机模式下辅助机优先使用远端模型，本机预设仅作为远端不可达时的降级配置

### M3 Eino 集成



* 引入 `github.com/cloudwego/eino`，将 `internal/ai/ollama` 的直连调用重构为 Eino ChatModel 组件（可用 eino-ext 的 OpenAI 组件指向 Ollama 的 OpenAI 兼容端点，或自定义 Ollama 组件），工具调用走 Eino Tool 协议

* 保持现有 AI 服务 API（Chat / StreamChat / 工具循环 / RAG 注入）行为不变

### M4 服务端 Ollama 管理



* 管理 API：模型列表（含量化信息）、拉取模型（`ollama pull`，进度可查）、删除模型、切换默认模型、查看 / 调整推理参数（num\_ctx、temperature、keep\_alive 等）

* Web 管理页（如可行）：模型管理与参数调整入口

### M5 局域网调用



* 支持配置 Ollama 绑定 `0.0.0.0:11434`（`OLLAMA_HOST`），对外提供 OpenAI 兼容端点（Ollama 原生 `/v1` 或自建代理），供局域网其他设备 / 应用调用

* 说明防火墙放行端口与鉴权方式

### M6 部署模式与主备双机（可选开启）

* 配置项：`mode`（`single` / `dual`，默认 `single`）、本机角色（`primary` / `auxiliary`，dual 模式生效）、远端 Ollama 地址、自动切换开关；三种模式切换只改配置，重启或热重载生效，不涉及代码改动

* 单机模式：本机独立运行，按 M2 硬件自适应选择本机模型，RAG 使用本机索引——大主机即高级模型、N100 即低配模型，互不影响

* 双机模式（可选开启）：

  * 主服务（primary，通常为 N100 常驻）：按本机硬件选低配模型，RAG 默认开启，作为知识库与模型的服务端

  * 辅助机（auxiliary，通常为大主机）：启动时探测远端 Ollama → 可达则自动切换为远端模型（卸载本地模型释放资源）→ 自动调用主服务侧 RAG；远端不可达则降级回本机模型（大主机即高级模型）并记录告警

  * 辅助机不重复建立独立向量库，RAG 统一走主服务

### M7 HomeAssistant 接入



* 实现 HomeAssistant REST + WebSocket API 客户端：entity 列表、状态查询、调用服务（开关 / 亮度 / 温度等）

* 注册为 AI 工具（查询设备状态、控制设备），并入现有工具注册表

* 当前无 HA 设备：交付连接配置项 + 连通性自检（`/api/` 与 token 校验）+ 无设备时的明确降级提示；接入逻辑用单元测试 + Mock 响应验证

### M8 文档更新（完成后统一执行，内容须与实现一致）



* `README.md`：新增 AI 管家 / 知识库 / Ollama 管理 / 双机模式的功能说明、模型部署步骤、局域网调用示例

* `md/需求文档.md`：追加 v0.21「AI 管家与知识库」需求（生命周期、硬件适配、模型管理、局域网调用、部署模式 single/dual 与主备双机、HA 接入）

* `md/基于 Ollama + Go 的智能家庭 NAS 系统开发文档v0.1.md`：扩展 §2.5 / §2.6、更新附录 B 模型推荐与硬件要求、版本记录追加 v0.21

* `md/smart-nas-Go新手学习指南.md`：新增 AI 模块代码导览一节

## 验收标准



1. 启动 NAS 服务后无需手动操作，Ollama 自动运行且默认模型可对话（`curl /api/chat` 实测）

2. 优雅关闭后模型已卸载、Ollama 进程退出（仅限本服务拉起的实例）

3. 4070 12G 机器自动选择 `deepseek-r1-distill-qwen-7b:q4_K_M`；可通过配置模拟无 GPU 环境验证 N100 分支

4. `go build ./...` 与 `go vet ./...` 通过；AI 模块相关单元测试通过

5. 局域网内另一台设备能通过 API（含 OpenAI 兼容端点）调用对话

6. 三种部署模式均可独立运行：单机大主机用高级模型、单机 N100 用低配模型、双机模式辅助机自动切换远端模型并调用主服务侧 RAG；远端不可达时优雅降级回本机模型

7. 四个文档全部更新，内容与代码实现一致

8. HA 模块：未配置 / 无设备时给出降级提示；配置 Mock 后工具调用链可跑通

## 边界（不做的事）



* 不引入新的向量库服务（沿用现有 chromem）

* 不做 HA 高级自动化编排（沿用现有 `iot/automation` 引擎）

* 不做多用户 AI 配额、计费与请求限流

* 不修改现有备份、播放、WebDAV 等非 AI 模块（除非实现必需）