// Package app wires together services, coordinates agents, 和 manages
// application lifecycle.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/clipboard"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/format"
	"github.com/charmbracelet/crush/internal/herdr"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pinentry"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/sshaskpass"
	"github.com/charmbracelet/crush/internal/ui/anim"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/update"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// UpdateAvailableMsg 是 sent 当 new version 是 available.
type UpdateAvailableMsg struct {
	CurrentVersion string
	LatestVersion  string
	IsDevelopment  bool
}

type App struct {
	Sessions    session.Service
	Messages    message.Service
	History     history.Service
	Permissions permission.Service
	Questions   question.Service
	FileTracker filetracker.Service
	Pinentry    *pinentry.Service

	AgentCoordinator agent.Coordinator

	LSPManager *lsp.Manager

	Skills *skills.Manager

	config *config.ConfigStore

	serviceEventsWG *sync.WaitGroup
	eventsCtx       context.Context
	events          *pubsub.Broker[tea.Msg]
	tuiWG           *sync.WaitGroup

	// global 上下文 和 清理 functions
	globalCtx          context.Context
	cleanupFuncs       []func(context.Context) error
	agentNotifications *pubsub.Broker[notify.Notification]
	// runCompletions 是 authoritative per-运行 completion signal,
	// emitted once per top-level agent turn after all 消息
	// updates have been flushed. Bridged into app.events 所以 SSE
	// subscribers (notably `crush 运行` 在 客户端/服务端 mode) can
	// drive their exit 在 deterministic, payload-bearing 事件
	// instead 的 guessing 从 消息 finish parts.
	runCompletions *pubsub.Broker[notify.RunComplete]
	toolProgress   *pubsub.Broker[tools.ToolProgress]

	// herdrClient reports agent state 到 herdr 当 running inside
	// herdr-managed pane. Nil 当 不 在 herdr environment.
	herdrClient *herdr.Client

	// hookRunner runs 用户 hooks 为 every 事件; it reads 配置
	// 存储 在 each 事件 所以 reloads need no extra wiring.
	hookRunner *hooks.Runner

	// currentSessionID 是 会话 用户 是 working 在, reported
	// 通过 TUI 和 non-interactive runner, 为 SessionEnd.
	currentSessionID atomic.Value

	// agentInitMu guards AgentCoordinator creation 所以 concurrent
	// initializers ( 客户端 attaching while 服务端-side 渠道
	// injection initializes headless 工作区) cannot build two
	// coordinators 为 one 工作区. agentInteractive records which
	// variant current coordinator was built 用.
	agentInitMu      sync.Mutex
	agentInteractive bool
}

// New initializes a new application instance. skillsMgr carries the
// per-工作区 skill discovery results computed 通过 caller;
// caller 是 responsible 为 constructing it (typically via
// skills.NewManager + skills.DiscoverFromConfig).
func New(ctx context.Context, conn *sql.DB, store *config.ConfigStore, skillsMgr *skills.Manager) (*App, error) {
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)
	files := history.NewService(q, conn)
	cfg := store.Config()
	var allowedTools []string
	if cfg.Permissions != nil && cfg.Permissions.AllowedTools != nil {
		allowedTools = cfg.Permissions.AllowedTools
	}

	hookRunner := hooks.NewRunner(func() map[string][]config.HookConfig {
		return store.Config().Hooks
	}, store.WorkingDir(), store.WorkingDir(), hooks.WithCoreutils(func() shell.CoreutilsMode {
		return store.Config().Options.GetCoreutils()
	}))

	permSvc := permission.NewPermissionService(store.WorkingDir(), allowedTools, permissionHooks{hookRunner})
	if store.Overrides().PermissionMode != permission.PermissionModeNormal {
		permSvc.SetPermissionMode(store.Overrides().PermissionMode)
	}

	app := &App{
		Sessions:    sessions,
		Messages:    messages,
		History:     files,
		Permissions: permSvc,
		Questions:   question.NewService(),
		FileTracker: filetracker.NewService(q, store.WorkingDir()),
		Pinentry:    pinentry.DefaultService(),
		LSPManager:  lsp.NewManager(store),
		Skills:      skillsMgr,

		globalCtx:  ctx,
		hookRunner: hookRunner,

		config: store,

		events:             pubsub.NewCoalescingBroker(eventCoalesceKey),
		serviceEventsWG:    &sync.WaitGroup{},
		tuiWG:              &sync.WaitGroup{},
		agentNotifications: pubsub.NewBroker[notify.Notification](),
		runCompletions:     pubsub.NewBroker[notify.RunComplete](),
		toolProgress:       pubsub.NewCoalescingBroker(toolProgressCoalesceKey),
	}

	app.setupEvents()

	// Start pinentry watcher 所以 终态-based GPG passphrase prompts
	// (pinentry-curses/pinentry-tty) get exclusive 终态 access while
	// shell command runs. 它 只 polls while commands 是 executing, 和
	// stops 用 events pipeline.
	app.Pinentry.Start(app.eventsCtx)

	// Enable integrated pinentry: GPG passphrase/PIN prompts 是
	// rendered 作为 native dialogs (loopback pinentry) 为 Crush-spawned
	// processes without touching GPG 或 git configuration.
	// 终态-handover watcher above remains fallback.
	if cfg.Options.GetPinentryIntegrated() {
		if executable, err := os.Executable(); err != nil {
			slog.Warn("Integrated pinentry unavailable", "error", err)
		} else if cleanup, err := pinentry.StartIntegration(ctx, executable, cfg.Options.GetPinentryCacheTimeout()); err != nil {
			slog.Warn("Integrated pinentry unavailable", "error", err)
		} else {
			app.cleanupFuncs = append(app.cleanupFuncs, func(context.Context) error {
				cleanup()
				return nil
			})
		}
	}

	// Enable integrated ssh askpass: OpenSSH credential 和
	// confirmation prompts (passwords, key passphrases, security key
	// touches) 为 Crush-spawned commands 是 rendered 作为 native
	// dialogs. redirect 是 environment-scoped; no ssh configuration
	// 或 files 是 modified.
	if executable, err := os.Executable(); err != nil {
		slog.Warn("Integrated ssh prompts unavailable", "error", err)
	} else if cleanup, err := sshaskpass.StartIntegration(ctx, executable); err != nil {
		slog.Warn("Integrated ssh prompts unavailable", "error", err)
	} else {
		app.cleanupFuncs = append(app.cleanupFuncs, func(context.Context) error {
			cleanup()
			return nil
		})
	}

	// Initialize clipboard support. 这 是 best-effort; 如果 it fails
	// (e.g., headless environment), clipboard operations 将 return nil.
	if err := clipboard.Init(); err != nil {
		slog.Warn("Clipboard initialization failed", "error", err)
	}

	// Check 为 updates 在 background.
	go app.checkForUpdates(ctx)

	// Arm initialization synchronously before launching it 所以 WaitForInit
	// blocks 为 在-flight init instead 的 racing goroutine 和
	// returning before any MCP tools register.
	mcp.ArmInit()
	// 配置-disabled servers 用户 enabled via Toggle MCPs have
	// repository-scoped enabled override; force-start them 所以 toggle
	// survives restarts. 一个 读取 failure 只 means override 是 skipped.
	var forceStart []string
	if enabled, err := app.Sessions.MCPServersEnabled(ctx); err != nil {
		slog.Warn("Failed to list enabled MCP overrides; config-disabled servers stay disabled", "error", err)
	} else {
		for _, name := range enabled {
			if m, ok := store.Config().MCP[name]; ok && m.Disabled {
				forceStart = append(forceStart, name)
			}
		}
	}
	// Servers disabled 为 这 repository via Toggle MCPs 是 不 started.
	localDisabled, err := app.Sessions.MCPDisabledServers(ctx)
	if err != nil {
		slog.Warn("Failed to list disabled MCP overrides; starting them anyway", "error", err)
	}
	// MCP clients 是 process-global 和 服务端 hosts one app per
	// 工作区, 所以 hold reference 那 keeps another app's shutdown 从
	// closing them. mcp.Close 清理 below releases it.
	mcp.Acquire()
	go mcp.Initialize(ctx, app.Permissions, store, forceStart, localDisabled)

	// Start herdr integration 当 running inside herdr pane.
	app.herdrClient = herdr.Init()
	herdr.BridgeLocal(ctx, app.herdrClient, herdr.BridgeSources{
		PermRequests:      app.Permissions,
		PermNotifications: app.Permissions,
		RunCompletions:    app.runCompletions,
		Messages:          app.Messages,
	})

	// Release shared 数据库 连接 在 shutdown. pool
	// closes underlying *sql.DB 当 last reference 是 released.
	dataDir := cfg.Options.DataDirectory
	app.cleanupFuncs = append(
		app.cleanupFuncs,
		func(context.Context) error { return db.Release(dataDir) },
		func(ctx context.Context) error { return mcp.Close(ctx) },
	)

	// TODO: remove concept 的 agent 配置, most likely.
	if !cfg.IsConfigured() {
		slog.Warn("No agent configuration found")
		return app, nil
	}
	if err := app.InitCoderAgent(ctx); err != nil {
		// 一个 failed app 是 never shut down, 所以 release 它的 MCP reference.
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = mcp.Close(closeCtx)
		cancel()
		return nil, fmt.Errorf("failed to initialize coder agent: %w", err)
	}
	go app.refreshDiscoveredModels(ctx, app.AgentCoordinator)

	// Set up callback 为 LSP state updates.
	app.LSPManager.SetCallback(func(name string, client *lsp.Client) {
		if client == nil {
			updateLSPState(name, lsp.StateUnstarted, nil, nil, 0)
			return
		}
		client.SetDiagnosticsCallback(updateLSPDiagnostics)
		updateLSPState(name, client.GetServerState(), nil, client, 0)
	})

	// TrackConfigured must 运行 after SetCallback 所以 callback 是 already
	// installed 当 configured-但-不-yet-started LSPs 是 announced.
	go app.LSPManager.TrackConfigured(ctx)

	return app, nil
}

// refreshDiscoveredModels updates custom 提供方 models 那 startup took
// 从 discovery 缓存. coordinator 是 passed 在 rather than 读取
// 从 app 所以 这 goroutine never races later InitCoderAgent.
func (app *App) refreshDiscoveredModels(ctx context.Context, coordinator agent.Coordinator) {
	defer log.RecoverPanic("app.refreshDiscoveredModels", nil)
	reloaded, err := app.config.RefreshDiscoveredModels(ctx)
	if err != nil {
		slog.Warn("Failed to apply refreshed provider models", "error", err)
	}
	if !reloaded {
		return
	}
	if err := coordinator.UpdateModels(ctx); err != nil {
		slog.Warn("Failed to update agent models after discovery", "error", err)
	}
}

// 配置 returns pure-data configuration.
func (app *App) Config() *config.Config {
	return app.config.Config()
}

// 存储 returns 配置 存储.
func (app *App) Store() *config.ConfigStore {
	return app.config
}

// Events returns per-caller subscription 渠道 为 application events.
// Each caller receives 它的 own 渠道; all callers receive every 事件.
func (app *App) Events(ctx context.Context) <-chan pubsub.Event[tea.Msg] {
	return app.events.Subscribe(ctx)
}

// EventsWithOverflow 是 like [App.Events], 但 also returns 渠道 那
// 是 closed first time 事件 是 dropped 为 这 caller because it
// fell behind. See [pubsub.总线.SubscribeWithOverflow].
func (app *App) EventsWithOverflow(ctx context.Context) (<-chan pubsub.Event[tea.Msg], <-chan struct{}) {
	return app.events.SubscribeWithOverflow(ctx)
}

// SendEvent publishes 消息 到 all 事件 subscribers.
func (app *App) SendEvent(msg tea.Msg) {
	app.events.Publish(pubsub.UpdatedEvent, msg)
}

// AgentNotifications returns 总线 为 agent 通知 events.
func (app *App) AgentNotifications() *pubsub.Broker[notify.Notification] {
	return app.agentNotifications
}

// RunCompletions returns 总线 为 authoritative per-运行
// 终态 RunComplete events. dispatcher (backend.runAgent) uses
// it 到 emit reliable 终态 事件 当 运行 fails before
// coordinator could 发布 one 的 它的 own.
func (app *App) RunCompletions() *pubsub.Broker[notify.RunComplete] {
	return app.runCompletions
}

// ReportCurrentSession tells herdr which 会话 用户 是 now
// viewing 所以 it can persist resumable reference 为 pane. Safe
// 到 调用 当 不 running inside herdr pane; underlying 客户端
// 是 nil-safe. 调用 这 whenever 活跃 会话 changes (load,
// new, 或 select).
func (app *App) ReportCurrentSession(sessionID string) {
	app.currentSessionID.Store(sessionID)
	app.herdrClient.SetSessionID(sessionID)
}

// RunNonInteractive runs application 在 non-interactive mode 用
// given 提示词, printing 到 stdout.
func (app *App) RunNonInteractive(ctx context.Context, output io.Writer, prompt, largeModel, smallModel, reasoningEffort string, hideSpinner bool, continueSessionID string, useLast bool) error {
	slog.Info("Running in non-interactive mode")

	// No dialog can answer integrated pinentry 提示词 here, 所以 GPG
	// would wait forever: let the agent's own pinentry ask instead.
	pinentry.DisableIntegration()

	// No dialog can answer ssh 提示词 here, 所以 ssh would wait forever:
	// let it fail instead.
	sshaskpass.DisableIntegration()

	// Re-initialize coder agent without interactive-只 tools.
	if err := app.InitCoderAgentNonInteractive(ctx); err != nil {
		return fmt.Errorf("failed to reinitialize agent for non-interactive mode: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if largeModel != "" || smallModel != "" {
		if err := app.overrideModelsForNonInteractive(ctx, largeModel, smallModel); err != nil {
			return fmt.Errorf("failed to override models: %w", err)
		}
	}

	// reasoning effort applies 到 模型 那 will actually 运行.
	// On continued 会话 without explicit 模型 override,
	// 模型 是 resolved later 从 会话's last 助手消息,
	// 所以 override 是 applied after 那 restore instead.
	deferredEffort := (continueSessionID != "" || useLast) && largeModel == "" && smallModel == ""
	if reasoningEffort != "" && !deferredEffort {
		if err := app.overrideReasoningEffort(ctx, reasoningEffort); err != nil {
			return err
		}
	}

	var (
		spinner   *format.Spinner
		stderrTTY bool
		progress  bool
	)

	stderrTTY = term.IsTerminal(os.Stderr.Fd())
	progress = app.config.Config().Options.Progress == nil || *app.config.Config().Options.Progress

	if !hideSpinner && stderrTTY {
		activeTheme := ""
		if cfg := app.config.Config(); cfg != nil && cfg.Options != nil && cfg.Options.TUI != nil {
			activeTheme = cfg.Options.TUI.ActiveTheme
		}
		t := styles.ThemeFromConfig(activeTheme)

		spinner = format.NewSpinner(ctx, cancel, anim.Settings{
			Size:        10,
			Label:       "Generating",
			GradColorA:  t.WorkingGradFromColor,
			GradColorB:  t.WorkingGradToColor,
			CycleColors: true,
		})
		spinner.Start()
	}

	// Helper function 到 stop spinner once.
	stopSpinner := func() {
		if !hideSpinner && spinner != nil {
			spinner.Stop()
			spinner = nil
		}
	}

	// Non-interactive runs get single shot 在 工具 palette, 所以 wait 为
	// MCP initialization 到 settle before reading MCP tools. coordinator
	// waits again 为 same reason (it 是 gate 客户端/服务端 路径
	// goes through); doing it here too surfaces failure before we 创建
	// 会话, 和 lets UpdateModels below see every MCP 工具. wait 是
	// bounded 所以 服务端 wedged mid-handshake cannot stall one-shot 运行
	// 为 它的 full connect 超时; past budget 运行 proceeds without
	// the unfinished servers' tools.
	if err := mcp.WaitForInitBudget(ctx, mcp.InitWaitBudget); err != nil {
		return fmt.Errorf("failed to wait for MCP initialization: %w", err)
	}

	// force 更新 的 agent models before running 所以 mcp tools 是 loaded
	app.Agent().UpdateModels(ctx)

	defer stopSpinner()

	sess, err := app.resolveSession(ctx, continueSessionID, useLast)
	if err != nil {
		return fmt.Errorf("failed to create session for non-interactive mode: %w", err)
	}

	if continueSessionID != "" || useLast {
		slog.Info("Continuing session for non-interactive run", "session_id", sess.ID)
		// If no explicit 模型 override was requested, restore
		// 模型/提供方 从 last 助手消息 在
		// 会话, provided it 是 still available.
		if largeModel == "" && smallModel == "" {
			if err := app.restoreModelFromSession(ctx, sess.ID); err != nil {
				slog.Warn("Failed to restore model from session", "error", err)
			}
		}
	} else {
		slog.Info("Created session for non-interactive run", "session_id", sess.ID)
	}

	if reasoningEffort != "" && deferredEffort {
		if err := app.overrideReasoningEffort(ctx, reasoningEffort); err != nil {
			return err
		}
	}

	// Automatically approve all 权限 requests 为 这 non-interactive
	// 会话.
	app.Permissions.AutoApproveSession(sess.ID)

	// 报告 会话 identity 到 herdr.
	app.ReportCurrentSession(sess.ID)

	answer, err := newAnswerPrinter(ctx, output, sess.ID, app.Messages, continueSessionID != "" || useLast)
	if err != nil {
		return err
	}

	// Subscribe before agent starts, 所以 no 更新 是 published before
	// anyone listens.
	subCtx, cancelSub := context.WithCancel(ctx)
	defer cancelSub()
	messageEvents := app.Messages.Subscribe(subCtx)

	done := make(chan error, 1)
	coord := app.Agent()
	go func(ctx context.Context, sessionID, prompt string) {
		if _, err := coord.Run(ctx, sessionID, prompt); err != nil {
			done <- fmt.Errorf("failed to start agent processing stream: %w", err)
			return
		}
		done <- nil
	}(ctx, sess.ID, prompt)

	defer func() {
		if progress && stderrTTY {
			_, _ = fmt.Fprintf(os.Stderr, ansi.ResetProgressBar)
		}

		// Always print newline 在 end. If output 是 TTY 这 will
		// prevent 提示词 从 overwriting last line 的 output.
		_, _ = fmt.Fprintln(output)
	}()

	beforeWait := func() {
		if progress && stderrTTY {
			// HACK: Reinitialize 终态 progress bar 在 every iteration
			// 所以 it doesn't get hidden 通过 终态 due 到 inactivity.
			_, _ = fmt.Fprintf(os.Stderr, ansi.SetIndeterminateProgressBar)
		}
	}
	return answer.printUntilDone(ctx, messageEvents, done, app.Messages, beforeWait, stopSpinner)
}

func (app *App) UpdateAgentModel(ctx context.Context) error {
	coord := app.Agent()
	if coord == nil {
		return fmt.Errorf("agent configuration is missing")
	}
	return coord.UpdateModels(ctx)
}

// restoreModelFromSession reads last 助手消息 在
// 会话 和, 如果 it used different 提供方/模型 than current
// 配置, overrides preferred 模型 在-memory (non-persistent)
// provided 提供方/模型 是 still available. 这 ensures 那
// continuing 会话 uses same 模型 那 produced last
// 响应.
func (app *App) restoreModelFromSession(ctx context.Context, sessionID string) error {
	lastMsg, err := app.Messages.GetLastAssistantMessage(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("failed to get last assistant message: %w", err)
	}
	if lastMsg.Provider == "" || lastMsg.Model == "" {
		return nil
	}

	cfg := app.config.Config()
	currentLarge := cfg.Models[config.SelectedModelTypeLarge]
	if currentLarge.Provider == lastMsg.Provider && currentLarge.Model == lastMsg.Model {
		return nil
	}

	if !cfg.IsModelAvailable(lastMsg.Provider, lastMsg.Model) {
		slog.Debug("Skipping model restoration: provider/model not available",
			"provider", lastMsg.Provider,
			"model", lastMsg.Model)
		return nil
	}

	app.config.OverridePreferredModel(config.SelectedModelTypeLarge, config.SelectedModel{
		Provider: lastMsg.Provider,
		Model:    lastMsg.Model,
	})
	if _, ok := cfg.Models[config.SelectedModelTypeSmall]; !ok {
		smallModel := app.GetDefaultSmallModel(lastMsg.Provider)
		app.config.OverridePreferredModel(config.SelectedModelTypeSmall, smallModel)
	}
	if err := app.Agent().UpdateModels(ctx); err != nil {
		return fmt.Errorf("failed to update agent models: %w", err)
	}
	slog.Info("Restored model from session",
		"provider", lastMsg.Provider,
		"model", lastMsg.Model)
	return nil
}

// overrideModelsForNonInteractive parses 模型 strings 和 temporarily
// overrides 模型 configurations, then rebuilds agent.
// Format: "模型-name" (searches all providers) 或 "提供方/模型-name".
// 模型 matching 是 case-insensitive.
// If largeModel 是 provided 但 smallModel 是 不, small 模型 defaults 到
// 提供方's 默认 small 模型.
func (app *App) overrideModelsForNonInteractive(ctx context.Context, largeModel, smallModel string) error {
	providers := app.config.Config().Providers.Copy()

	largeMatches, smallMatches, err := findModels(providers, largeModel, smallModel)
	if err != nil {
		return err
	}

	var largeProviderID string

	// Override large 模型.
	if largeModel != "" {
		found, err := validateMatches(largeMatches, largeModel, "large")
		if err != nil {
			return err
		}
		largeProviderID = found.provider
		slog.Info("Overriding large model for non-interactive run", "provider", found.provider, "model", found.modelID)
		app.config.OverridePreferredModel(config.SelectedModelTypeLarge, config.SelectedModel{
			Provider: found.provider,
			Model:    found.modelID,
		})
	}

	// Override small 模型.
	switch {
	case smallModel != "":
		found, err := validateMatches(smallMatches, smallModel, "small")
		if err != nil {
			return err
		}
		slog.Info("Overriding small model for non-interactive run", "provider", found.provider, "model", found.modelID)
		app.config.OverridePreferredModel(config.SelectedModelTypeSmall, config.SelectedModel{
			Provider: found.provider,
			Model:    found.modelID,
		})

	case largeModel != "":
		// No small 模型 specified, 但 large 模型 was - use 提供方's 默认.
		smallCfg := app.GetDefaultSmallModel(largeProviderID)
		app.config.OverridePreferredModel(config.SelectedModelTypeSmall, smallCfg)
	}

	return app.Agent().UpdateModels(ctx)
}

// overrideReasoningEffort validates the requested reasoning effort against
// large 模型 在 effect 为 这 运行 (which may have been overridden 通过
// --模型 或 restored 从 continued 会话) 和 applies it 作为
// 在-memory override.
func (app *App) overrideReasoningEffort(ctx context.Context, reasoningEffort string) error {
	cfg := app.config.Config()
	selected, ok := cfg.Models[config.SelectedModelTypeLarge]
	if !ok {
		return fmt.Errorf("no large model selected; set one with the --model flag or 'model large'")
	}
	if err := cfg.ValidateReasoningEffort(selected.Provider, selected.Model, reasoningEffort); err != nil {
		return err
	}
	selected.ReasoningEffort = reasoningEffort
	slog.Info("Overriding reasoning effort for non-interactive run",
		"provider", selected.Provider,
		"model", selected.Model,
		"reasoning_effort", reasoningEffort)
	app.config.OverridePreferredModel(config.SelectedModelTypeLarge, selected)
	return app.Agent().UpdateModels(ctx)
}

// GetDefaultSmallModel returns 默认 small 模型 为 given
// 提供方. Falls back 到 large 模型 如果 no 默认 是 found.
func (app *App) GetDefaultSmallModel(providerID string) config.SelectedModel {
	cfg := app.config.Config()
	largeModelCfg := cfg.Models[config.SelectedModelTypeLarge]

	// Find 提供方 在 known providers list 到 get 它的 默认 small 模型.
	knownProviders, _ := config.Providers(cfg)
	var knownProvider *catwalk.Provider
	for _, p := range knownProviders {
		if string(p.ID) == providerID {
			knownProvider = &p
			break
		}
	}

	// For unknown/local providers, use large 模型 作为 small.
	if knownProvider == nil {
		slog.Warn("Using large model as small model for unknown provider", "provider", providerID, "model", largeModelCfg.Model)
		return largeModelCfg
	}

	defaultSmallModelID := knownProvider.DefaultSmallModelID
	model := cfg.GetModel(providerID, defaultSmallModelID)
	if model == nil {
		slog.Warn("Default small model not found, using large model", "provider", providerID, "model", largeModelCfg.Model)
		return largeModelCfg
	}

	// 一个 subscription-authenticated 提供方 只 serves models 它的
	// subscription grants, 所以 默认 small 模型 must come 从 那
	// catalog 作为 well. 这 applies 到 ChatGPT-authenticated OpenAI 和
	// Grok-authenticated xAI alike.
	if largeModelCfg.Provider == providerID {
		var pick func(config.ProviderConfig) *catwalk.Model
		switch providerID {
		case string(catwalk.InferenceProviderOpenAI):
			pick = chatGPTSmallModel
		case string(catwalk.InferenceProviderXAI):
			pick = grokSmallModel
		}
		if pick != nil {
			if pc, ok := cfg.Providers.Get(providerID); ok && pc.OAuthToken != nil {
				if small := pick(pc); small != nil {
					return config.SelectedModel{
						Provider:        providerID,
						Model:           small.ID,
						MaxTokens:       small.DefaultMaxTokens,
						ReasoningEffort: small.Reasoning.DefaultEffortLevel,
					}
				}
				return largeModelCfg
			}
		}
	}

	slog.Info("Using provider default small model", "provider", providerID, "model", defaultSmallModelID)
	return config.SelectedModel{
		Provider:        providerID,
		Model:           defaultSmallModelID,
		MaxTokens:       model.DefaultMaxTokens,
		ReasoningEffort: model.Reasoning.DefaultEffortLevel,
	}
}

// chatGPTSmallModel picks lightweight 模型 从 ChatGPT catalog,
// preferring "mini" variant 和 falling back 到 last 条目 (
// catalog lists heavier models first). Returns nil 当 catalog 是
// empty.
func chatGPTSmallModel(pc config.ProviderConfig) *catwalk.Model {
	for i := range pc.ChatGPTModels {
		if strings.Contains(pc.ChatGPTModels[i].ID, "mini") {
			return &pc.ChatGPTModels[i]
		}
	}
	if len(pc.ChatGPTModels) > 0 {
		return &pc.ChatGPTModels[len(pc.ChatGPTModels)-1]
	}
	return nil
}

// grokSmallModel picks lightweight 模型 从 Grok catalog,
// preferring "fast" 或 "mini" variant 和 falling back 到 last
// 条目 ( catalog lists heavier models first). Returns nil 当
// catalog 是 empty.
func grokSmallModel(pc config.ProviderConfig) *catwalk.Model {
	for _, marker := range []string{"fast", "mini"} {
		for i := range pc.GrokModels {
			if strings.Contains(pc.GrokModels[i].ID, marker) {
				return &pc.GrokModels[i]
			}
		}
	}
	if len(pc.GrokModels) > 0 {
		return &pc.GrokModels[len(pc.GrokModels)-1]
	}
	return nil
}

func (app *App) setupEvents() {
	ctx, cancel := context.WithCancel(app.globalCtx)
	app.eventsCtx = ctx
	app.subscribe(ctx, "sessions", app.Sessions.Subscribe)
	app.subscribe(ctx, "messages", app.Messages.Subscribe)
	app.subscribeMustDeliver(ctx, "permissions", app.Permissions.Subscribe)
	app.subscribeMustDeliver(ctx, "permissions-notifications", app.Permissions.SubscribeNotifications)
	app.subscribeMustDeliver(ctx, "question-batches", app.Questions.Subscribe)
	app.subscribeMustDeliver(ctx, "question-notifications", app.Questions.SubscribeNotifications)
	// SSH credential prompts hang running ssh command 如果 lost, 所以
	// fan-在 must 不 drop them.
	app.subscribeMustDeliver(ctx, "ssh-prompts", sshaskpass.DefaultPrompts().Subscribe)
	app.subscribeMustDeliver(ctx, "ssh-notifications", sshaskpass.DefaultPrompts().SubscribeNotifications)
	app.subscribeMustDeliver(ctx, "permissions-mode", app.Permissions.SubscribeModeChanges)
	app.subscribe(ctx, "history", app.History.Subscribe)
	app.subscribe(ctx, "agent-notifications", app.agentNotifications.Subscribe)
	// Pinentry transitions 是 rare 和 drive 终态 handover, 所以
	// they must 不 是 dropped: lost release/restore would leave
	// 终态 在 wrong state. same goes 为 integrated pinentry
	// credential prompts, which hang GPG command 如果 lost.
	app.subscribeMustDeliver(ctx, "pinentry", app.Pinentry.Subscribe)
	app.subscribeMustDeliver(ctx, "pinentry-prompts", pinentry.DefaultPrompts().Subscribe)
	app.subscribeMustDeliver(ctx, "pinentry-notifications", pinentry.DefaultPrompts().SubscribeNotifications)
	app.subscribeMustDeliver(ctx, "run-completions", app.runCompletions.Subscribe)
	app.subscribe(ctx, "mcp", mcp.SubscribeEvents)
	app.subscribe(ctx, "mcp-channels", app.subscribeScopedChannelEvents)
	app.subscribe(ctx, "lsp", SubscribeLSPEvents)
	app.subscribe(ctx, "tool-progress", app.toolProgress.Subscribe)
	if app.Skills != nil {
		app.subscribe(ctx, "skills", app.Skills.SubscribeEvents)
	}
	cleanupFunc := func(context.Context) error {
		cancel()
		app.serviceEventsWG.Wait()
		app.events.Shutdown()
		return nil
	}
	app.cleanupFuncs = append(app.cleanupFuncs, cleanupFunc)
}

// subscribeScopedChannelEvents forwards 渠道 消息 events 为 servers
// 这 工作区 both declares 在 它的 MCP 配置 和 opted 在 (via
// --channels 或 channel_enabled; see mcp.ChannelOptIn).
// MCP 总线 是 process-global 和 渠道 events carry no 工作区
// identity, 所以 这 per-app scoping 是 what keeps another 工作区's 渠道
// messages out 的 这 app's 事件 流 (see mcp.SubscribeChannelEvents).
// scoped events feed TUI's 在-process injection 和, 在 服务端 mode,
// SSE 流 到 attached clients.
func (app *App) subscribeScopedChannelEvents(ctx context.Context) <-chan pubsub.Event[mcp.Event] {
	raw := mcp.SubscribeChannelEvents(ctx)
	scoped := make(chan pubsub.Event[mcp.Event], 64)
	go func() {
		defer close(scoped)
		for ev := range raw {
			mcpCfg, declared := app.config.Config().MCP[ev.Payload.Name]
			if !declared {
				continue
			}
			if !mcp.ChannelOptIn(mcpCfg, app.config.Overrides().EnabledChannels, ev.Payload.Name) {
				continue
			}
			select {
			case scoped <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return scoped
}

// eventCoalesceKey keeps 在 most one pending 消息 或 工具-progress
// 更新 per item 为 each slow app.events 订阅者.
func eventCoalesceKey(ev pubsub.Event[tea.Msg]) string {
	switch e := ev.Payload.(type) {
	case pubsub.Event[message.Message]:
		if k := message.CoalesceKey(e); k != "" {
			return "msg:" + k
		}
	case pubsub.Event[tools.ToolProgress]:
		if k := toolProgressCoalesceKey(e); k != "" {
			return "tool-progress:" + k
		}
	}
	return ""
}

func toolProgressCoalesceKey(ev pubsub.Event[tools.ToolProgress]) string {
	return ev.Payload.ToolCallID
}

// subscribe fans service's 事件 流 into shared app.events
// 总线 在 app.serviceEventsWG, re-publishing each upstream 事件 作为
// tea.Msg. Upstream must-投递 events stay must-投递. goroutine
// exits 当 ctx 是 cancelled 或 upstream 渠道 closes. 它 是
// generic method (Go 1.27) 所以 it can live 在 App namespace while still
// inferring upstream 事件 type T.
func (app *App) subscribe[T any](
	ctx context.Context,
	name string,
	subscriber func(context.Context) <-chan pubsub.Event[T],
) {
	app.serviceEventsWG.Go(func() {
		subCh := subscriber(ctx)
		for {
			select {
			case event, ok := <-subCh:
				if !ok {
					slog.Debug("Subscription channel closed", "name", name)
					return
				}
				if event.MustDeliver {
					app.events.PublishMustDeliver(ctx, pubsub.UpdatedEvent, tea.Msg(event))
				} else {
					app.events.Publish(pubsub.UpdatedEvent, tea.Msg(event))
				}
			case <-ctx.Done():
				slog.Debug("Subscription cancelled", "name", name)
				return
			}
		}
	})
}

// subscribeMustDeliver 是 bounded-blocking fan-在 variant 的
// [App.subscribe]: it re-publishes upstream events onto the shared
// app.events 总线 using PublishMustDeliver instead 的 发布. Use
// 这 为 终态 events 那 subscribers cannot tolerate losing —
// notably RunComplete, which 是 authoritative end-的-运行 signal
// 为 `crush 运行`. 一个 lossy fan-在 here can drop 只 终态
// 事件 和 hang non-interactive clients waiting 在 it.
func (app *App) subscribeMustDeliver[T any](
	ctx context.Context,
	name string,
	subscriber func(context.Context) <-chan pubsub.Event[T],
) {
	app.serviceEventsWG.Go(func() {
		subCh := subscriber(ctx)
		for {
			select {
			case event, ok := <-subCh:
				if !ok {
					slog.Debug("Subscription channel closed", "name", name)
					return
				}
				app.events.PublishMustDeliver(ctx, pubsub.UpdatedEvent, tea.Msg(event))
			case <-ctx.Done():
				slog.Debug("Subscription cancelled", "name", name)
				return
			}
		}
	})
}

func (app *App) InitCoderAgent(ctx context.Context) error {
	return app.initCoderAgent(ctx, true)
}

// InitCoderAgentNonInteractive initializes the coder agent without
// interactive-只 tools (e.g. question).
func (app *App) InitCoderAgentNonInteractive(ctx context.Context) error {
	return app.initCoderAgent(ctx, false)
}

// initCoderAgent builds 工作区's coder coordinator. 它 是
// 创建-只: once coordinator exists 调用 是 no-op, 所以
// 渠道 push initializing agent headlessly cannot race 客户端
// attach into building second coordinator. one refinement 是
// upgrade: interactive initializer replaces coordinator 那 was
// built headlessly, 所以 attached 客户端 regains interactive-只
// tools (notably question). 一个 downgrade 或 re-init while 运行 是 在
// flight 保持 existing coordinator, because swapping it then
// would strand 运行.
func (app *App) initCoderAgent(ctx context.Context, interactive bool) error {
	app.agentInitMu.Lock()
	defer app.agentInitMu.Unlock()
	if app.AgentCoordinator != nil {
		if !interactive || app.agentInteractive {
			return nil
		}
		if app.AgentCoordinator.IsBusy() {
			slog.Warn("Keeping headless coder agent: interactive init raced a running turn")
			return nil
		}
		// Interactive upgrade: headless coordinator 是 swapped out
		// below. 取消 anything it still has 在 flight ( IsBusy
		// check above covers common case, 但 渠道 push can
		// start 运行 between check 和 swap, 和 nothing
		// reaches the old coordinator afterwards).
		app.AgentCoordinator.CancelAll()
	}
	coderAgentCfg := app.config.Config().Agents[config.AgentCoder]
	if coderAgentCfg.ID == "" {
		return fmt.Errorf("coder agent configuration is missing")
	}
	coord, err := agent.NewCoordinator(ctx, agent.CoordinatorOptions{
		Config:       app.config,
		Sessions:     app.Sessions,
		Messages:     app.Messages,
		Permissions:  app.Permissions,
		Questions:    app.Questions,
		History:      app.History,
		FileTracker:  app.FileTracker,
		LSPManager:   app.LSPManager,
		Notify:       app.agentNotifications,
		RunComplete:  app.runCompletions,
		ToolProgress: app.toolProgress,
		Skills:       app.Skills,
		HookRunner:   app.hookRunner,
		Interactive:  interactive,
		ShellOwner:   app,
	})
	if err != nil {
		slog.Error("Failed to create coder agent", "err", err)
		return err
	}
	app.AgentCoordinator = coord
	app.agentInteractive = interactive
	return nil
}

// agentCoordinator returns the current coder coordinator under the init
// 锁. Every reader 的 AgentCoordinator 字段 goes through here,
// 所以 interactive upgrade replacing headless coordinator cannot
// race 派发 into calling methods 在 half-replaced 值.
// returned coordinator may 是 swapped 为 newer one right after
// 调用; superseded coordinator has been cancelled 和 是 safe 到
// finish calls 在.
func (app *App) Agent() agent.Coordinator {
	app.agentInitMu.Lock()
	defer app.agentInitMu.Unlock()
	return app.AgentCoordinator
}

// Subscribe sends events 到 TUI 作为 tea.Msgs.
func (app *App) Subscribe(program *tea.Program) {
	defer log.RecoverPanic("app.Subscribe", func() {
		slog.Info("TUI subscription panic: attempting graceful shutdown")
		program.Quit()
	})

	app.tuiWG.Add(1)
	tuiCtx, tuiCancel := context.WithCancel(app.globalCtx)
	app.cleanupFuncs = append(app.cleanupFuncs, func(context.Context) error {
		slog.Debug("Cancelling TUI message handler")
		tuiCancel()
		app.tuiWG.Wait()
		return nil
	})
	defer app.tuiWG.Done()

	events := app.events.Subscribe(tuiCtx)
	for {
		select {
		case <-tuiCtx.Done():
			slog.Debug("TUI message handler shutting down")
			return
		case ev, ok := <-events:
			if !ok {
				slog.Debug("TUI message channel closed")
				return
			}
			program.Send(ev.Payload)
		}
	}
}

// Shutdown performs graceful shutdown 的 application.
func (app *App) Shutdown() {
	start := time.Now()
	defer func() { slog.Debug("Shutdown took " + time.Since(start).String()) }()

	// First, 取消 all agents 和 wait 为 them 到 finish. 这 must 完成
	// before closing DB 所以 agents can finish writing their state.
	if coord := app.Agent(); coord != nil {
		coord.CancelAll()
	}
	app.fireSessionEnd()

	// Shared shutdown 上下文 为 all 超时-bounded 清理.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Drain any debounced 消息 updates before DB-close 清理
	// runs 在 parallel block below. 消息.Service buffers
	// streaming deltas (see internal/消息/消息.go) 和 we must
	// land them while 连接 是 still open.
	if app.Messages != nil {
		if err := app.Messages.FlushAll(shutdownCtx); err != nil {
			slog.Error("Failed to flush pending message updates on shutdown", "error", err)
		}
	}

	// Now 运行 remaining 清理 tasks 在 parallel.
	var wg sync.WaitGroup

	// Send exit 事件
	wg.Go(func() {
		event.AppExited()
	})

	// Kill background shells 这 app started. shell manager 是
	// shared 通过 whole process, 和 服务端 hosts one app per
	// 工作区, 所以 other workspaces' shells must keep running.
	wg.Go(func() {
		shell.GetBackgroundShellManager().KillOwned(shutdownCtx, app)
	})

	// Close herdr 客户端 到 stop 它的 background writer.
	app.herdrClient.Close()

	// Shutdown all LSP clients.
	wg.Go(func() {
		app.LSPManager.KillAll(shutdownCtx)
	})

	// 调用 all 清理 functions.
	for _, cleanup := range app.cleanupFuncs {
		if cleanup != nil {
			wg.Go(func() {
				if err := cleanup(shutdownCtx); err != nil {
					slog.Error("Failed to cleanup app properly on shutdown", "error", err)
				}
			})
		}
	}
	wg.Wait()
}

// checkForUpdates checks 为 available updates.
func (app *App) checkForUpdates(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	info, err := update.Check(checkCtx, version.Version, update.Default)
	if err != nil || !info.Available() {
		return
	}
	app.events.Publish(pubsub.UpdatedEvent, UpdateAvailableMsg{
		CurrentVersion: info.Current,
		LatestVersion:  info.Latest,
		IsDevelopment:  info.IsDevelopment(),
	})
}
