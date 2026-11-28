# evo 目录结构

evo 是基于 Crush 的二次开发：保留上游的 agent 编排、会话与工具框架，去掉不适合
本仓库的部分（内置/远程 provider 目录、上游 CI），并优化原版用起来不顺手的地方
（超时过短、失败不重试、配置只认内置目录等）。仓库注释统一为中文，本地不做编译，
构建交给 GitHub Actions 的 `evo-build` 工作流。

## 顶层

- `main.go` — 程序入口
- `AGENT.md` — 本文件
- `evo.json` — 项目配置（LSP 等）
- `go.mod` / `go.sum` — Go 模块依赖
- `schema.json` — 配置 schema（由 `evo schema` 生成，改配置结构后需同步）
- `Taskfile.yaml` — 构建/开发任务
- `flake.nix` / `flake.lock` — Nix 环境
- `scripts/` — 辅助脚本
- `internal/` — 全部源码

## internal/agent — Agent 核心

- `agent.go` — 核心编排：`Run`、`preparePrompt`、`createUserMessage`、流式响应、摘要、排队、取消
- `coordinator.go` — 多 agent / 子任务协调
- `channel.go` / `channelreply.go` — 渠道消息与下游回复路由
- `host_context.go` — 宿主应用上下文（当前文件、光标）注入
- `turn_hooks.go` — TurnStart / TurnEnd / StopFailure / Interrupt 钩子
- `hooked_tool.go` — 工具调用的 PreToolUse / PostToolUse 钩子包装
- `loop_detection.go` — 本轮工具调用上限（50 次）与重复调用检测
- `refusal_retry.go` — provider 安全拒绝后的重试
- `request_guard.go` — 请求超时保护：额度按提示词规模放大，超时包装成可重试错误
- `retry_budget.go` — 重试次数上限（主对话 10 次，后台任务 3 次）
- `stall.go` — 识别模型「偷懒收尾」并把它推回继续（最多 2 次，复用自动摘要的续跑通路）
- `context_replay_test.go` — 上下文重放的语义测试
- `prompts.go` / `templates/` — 内置 prompt 与模板（coder、summary、title、task、plan）
- `tools/` — 工具实现与工具描述 `.md`（go:embed）
- `notify/` — RunComplete 等通知事件
- `hyper/` — Hyper 计费/额度相关
- `agenttest/` — agent 测试辅助

## internal/message — 消息模型

- `message.go` — 消息服务：创建、更新、列出；`ListFromSummary` 从摘要点读历史
- `content.go` — 内容片段（文本、二进制附件、工具调用/结果）；`ToAIMessage` 把历史消息转成模型输入
- `attachment.go` — 附件类型判断
- `hidden.go` — 隐藏用户消息标记（仅进模型历史，不显示）

## internal/session — Session

- `session.go` — session 模型与服务：创建、查找、保存、重命名、todo、SummaryMessageID 指针

## internal/backend — 后端状态

- `backend.go` — workspace 生命周期、per-client 状态（`currentSessionID`、SSE 流、holdTimer）、会话广播、空闲关闭

## internal/server — HTTP/SSE 服务端

- `server.go` — 服务端与 listener
- `endpoints.go` — 路由定义
- `proto.go` — 请求校验（`client_id` 必须合法 UUID）
- `events.go` — SSE 事件流与过滤
- `multiclient_test.go` / `client_session_test.go` — 多客户端校验与会话广播测试

## internal/client — RPC 客户端 SDK

- `client.go` — 客户端连接、workspace 创建/销毁、事件订阅
- `config.go` / `proto.go` — 配置与协议

## internal/workspace — Workspace 抽象

- `workspace.go` — `Workspace` 接口（session、message、agent、config、event 的统一入口）
- `app_workspace.go` — 本地模式：直接包装 `app.App`
- `client_workspace.go` — 远程模式：通过 `internal/client` 调服务端
- `multiclient_integration_test.go` — 多客户端共享/隔离验证

## internal/app — 应用装配

- `app.go` — 把 agent、session、message、config、tools、LSP、hooks 装配起来
- `provider.go` — provider / 模型解析
- `hooks.go` — 应用层 hook
- `run_output.go` — 运行输出处理

## internal/db — 存储

- `connect.go` — SQLite 连接；每个 dataDir 一个 `crush.db`
- `sql/` — SQL 查询定义
- `querier.go` / `*.sql.go` — sqlc 生成的查询代码

## internal/config — 配置

- 配置结构、加载、校验、默认值、重载钩子
- `provider.go` — provider 目录入口；本仓库只认用户手写的 provider，`LoadProviders` 恒返回空列表
- `timeouts` 相关默认值在 `config.go`（`DefaultRequestTimeout` 为基础额度，实际按上下文放大）

## internal/hooks — Hook 系统

- `events.go` — 事件名（PreToolUse、PostToolUse、TurnStart、TurnEnd、PreCompact 等）
- `hooks.go` — hook 执行与结果聚合（allow/deny/halt/context/input rewrite/output rewrite）
- `builtin/` — 内置 hook（命令日志、webhook 审计）

## internal/proto — 协议类型

- 客户端/服务端之间传输的 workspace、event、permission、question 等结构

## internal/pubsub / internal/event — 事件系统

- `pubsub` — 通用发布订阅
- `event` — 事件埋点/统计

## internal/commands — 命令

- TUI/CLI 命令定义与分发

## internal/ui — 界面

- TUI 的 model、view、dialog、chat、styles、list

## internal/skills — 内置技能

- `builtin/` — crush-config、crush-hooks、jq 等内置 SKILL.md

## 其他支撑目录

- `agent/` 之外：`lsp/` 语言服务、`oauth/` 认证、`permission/` 权限、`question/` 问答交互、`shell/` shell 集成、`shellconfig/` 用户 shell 配置注入、`diff/` / `diffdetect/` 差异处理、`filetracker/` 文件追踪、`gitutil/` Git 工具、`csync/` 并发安全容器、`lock/` 文件锁、`log/` 日志、`format/` 格式化、`stringext/` 字符串工具、`home/` 路径发现、`env/` 环境变量、`dns/` DNS、`discover/` 服务发现、`update/` 更新、`version/` 版本、`clipboard/` 剪贴板、`pinentry/` / `sshaskpass/` 凭据输入辅助、`login/` / `logout/` 登录登出、`apigen/` API 生成辅助、`ansiext/` ANSI 扩展、`dirid/` 目录 ID、`fsext/` / `filepathext/` 文件工具
