package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/gitutil"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pinentry"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/sshaskpass"
)

// AppWorkspace implements 工作区 interface 通过 delegating
// directly 到 在-process [app.App] instance. 这 是 默认
// mode 当 客户端/服务端 architecture 是 不 enabled.
type AppWorkspace struct {
	app   *app.App
	store *config.ConfigStore
}

// NewAppWorkspace creates a new AppWorkspace wrapping the given app
// 和 配置 存储.
func NewAppWorkspace(a *app.App, store *config.ConfigStore) *AppWorkspace {
	return &AppWorkspace{
		app:   a,
		store: store,
	}
}

// -- Sessions --

func (w *AppWorkspace) CreateSession(ctx context.Context, title string) (session.Session, error) {
	return w.app.CreateSession(ctx, title)
}

func (w *AppWorkspace) GetSession(ctx context.Context, sessionID string) (session.Session, error) {
	return w.app.Sessions.Get(ctx, sessionID)
}

func (w *AppWorkspace) ListSessions(ctx context.Context) ([]session.Session, error) {
	return w.app.Sessions.List(ctx)
}

func (w *AppWorkspace) SaveSession(ctx context.Context, sess session.Session) (session.Session, error) {
	return w.app.Sessions.Save(ctx, sess)
}

func (w *AppWorkspace) DeleteSession(ctx context.Context, sessionID string) error {
	return w.app.Sessions.Delete(ctx, sessionID)
}

func (w *AppWorkspace) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return w.app.Sessions.CreateAgentToolSessionID(messageID, toolCallID)
}

func (w *AppWorkspace) ParseAgentToolSessionID(sessionID string) (string, string, bool) {
	return w.app.Sessions.ParseAgentToolSessionID(sessionID)
}

// SetCurrentSession reports 活跃 会话 到 herdr 所以 pane
// can persist resumable reference. Multi-客户端 presence tracking
// 是 irrelevant 在 single-客户端 local mode, 但 herdr still needs
// 到 know which 会话 是 live 到 support agent resume.
func (w *AppWorkspace) SetCurrentSession(ctx context.Context, sessionID string) error {
	w.app.ReportCurrentSession(sessionID)
	return nil
}

// RoutesChannelEvents reports false: 在-process mode has no 服务端 到
// route 渠道 events, 所以 frontend injects them itself.
func (w *AppWorkspace) RoutesChannelEvents() bool { return false }

// -- Messages --

func (w *AppWorkspace) ListMessages(ctx context.Context, sessionID string) ([]message.Message, error) {
	if w.app.AgentCoordinator != nil {
		return w.app.AgentCoordinator.ListSettledMessages(ctx, sessionID)
	}
	if err := w.app.Messages.FlushAll(ctx); err != nil {
		return nil, err
	}
	return w.app.Messages.List(ctx, sessionID)
}

func (w *AppWorkspace) ListUserMessages(ctx context.Context, sessionID string) ([]message.Message, error) {
	return w.app.Messages.ListUserMessages(ctx, sessionID)
}

func (w *AppWorkspace) ListAllUserMessages(ctx context.Context) ([]message.Message, error) {
	return w.app.Messages.ListAllUserMessages(ctx)
}

// -- Agent --

func (w *AppWorkspace) AgentRun(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) error {
	if w.app.Agent() == nil {
		return errors.New("agent coordinator not initialized")
	}
	_, err := w.app.Agent().Run(ctx, sessionID, prompt, attachments...)
	return err
}

func (w *AppWorkspace) AgentRunChannel(ctx context.Context, channel, sessionID, prompt string, attachments ...message.Attachment) error {
	if w.app.Agent() == nil {
		return errors.New("agent coordinator not initialized")
	}
	_, err := w.app.Agent().Run(agent.WithChannel(ctx, channel), sessionID, prompt, attachments...)
	return err
}

func (w *AppWorkspace) AgentRunShellCommand(ctx context.Context, sessionID, command string, termWidth int, onProgress func(string), isFirstMessage bool) (proto.ShellCommandResponse, error) {
	var persist shell.PersistFunc
	if sessionID != "" {
		persist = func(cmd, output string, exitCode int) error {
			return shell.PersistOutput(ctx, w.app.Messages, sessionID, cmd, output, exitCode, w.store.Config().Options.DataDirectory)
		}
	}

	opts := shell.RunOptions{
		Command:   command,
		Cwd:       w.store.WorkingDir(),
		TermWidth: termWidth,
		Coreutils: w.store.Config().Options.GetCoreutils(),
	}

	var result shell.CaptureResult
	var err error

	if onProgress != nil {
		result, err = shell.RunAndCaptureStream(ctx, opts, onProgress)
	} else {
		result, err = shell.RunAndPersist(ctx, opts, persist)
	}

	if err != nil && onProgress == nil {
		return proto.ShellCommandResponse{}, err
	}

	// Persist 如果 we used streaming 路径 (persist wasn't called 通过 RunAndPersist).
	if onProgress != nil && persist != nil {
		if persistErr := persist(command, result.Output, result.ExitCode); persistErr != nil {
			slog.Error("Failed to persist shell command output", "error", persistErr, "command", command)
		}
	}

	// Generate 标题 从 shell command 如果 it was first 消息.
	if isFirstMessage && w.app.Agent() != nil {
		titleCtx := context.WithoutCancel(ctx)
		w.app.Agent().GenerateTitle(titleCtx, sessionID, "$ "+command)
	}

	return proto.ShellCommandResponse{
		Output:   result.Output,
		ExitCode: result.ExitCode,
	}, nil
}

func (w *AppWorkspace) AgentCancel(sessionID string) {
	if w.app.Agent() != nil {
		w.app.Agent().Cancel(sessionID)
	}
}

func (w *AppWorkspace) AgentIsBusy() bool {
	if w.app.Agent() == nil {
		return false
	}
	return w.app.Agent().IsBusy()
}

func (w *AppWorkspace) AgentIsSessionBusy(sessionID string) bool {
	if w.app.Agent() == nil {
		return false
	}
	return w.app.Agent().IsSessionBusy(sessionID)
}

func (w *AppWorkspace) AgentModel() AgentModel {
	if w.app.Agent() == nil {
		return AgentModel{}
	}
	m := w.app.Agent().Model()
	return AgentModel{
		Agent:      w.app.Agent().MainAgent(),
		CatwalkCfg: m.CatwalkCfg,
		ModelCfg:   m.ModelCfg,
	}
}

func (w *AppWorkspace) AgentIsReady() bool {
	return w.app.Agent() != nil
}

func (w *AppWorkspace) AgentReadyErr() error {
	if w.app.Agent() == nil {
		return ErrAgentNotInitialized
	}
	return nil
}

func (w *AppWorkspace) AgentQueuedPrompts(sessionID string) int {
	if w.app.Agent() == nil {
		return 0
	}
	return w.app.Agent().QueuedPrompts(sessionID)
}

func (w *AppWorkspace) AgentQueuedPromptsList(sessionID string) []string {
	if w.app.Agent() == nil {
		return nil
	}
	return w.app.Agent().QueuedPromptsList(sessionID)
}

func (w *AppWorkspace) AgentClearQueue(sessionID string) {
	if w.app.Agent() != nil {
		w.app.Agent().ClearQueue(sessionID)
	}
}

func (w *AppWorkspace) AgentSetMain(agentID string) error {
	if w.app.Agent() == nil {
		return errors.New("agent coordinator not initialized")
	}
	return w.app.Agent().SetMainAgent(agentID)
}

func (w *AppWorkspace) AgentSummarize(ctx context.Context, sessionID string) error {
	if w.app.Agent() == nil {
		return errors.New("agent coordinator not initialized")
	}
	return w.app.Agent().Summarize(ctx, sessionID)
}

func (w *AppWorkspace) UpdateAgentModel(ctx context.Context) error {
	return w.app.UpdateAgentModel(ctx)
}

func (w *AppWorkspace) InitCoderAgent(ctx context.Context) error {
	return w.app.InitCoderAgent(ctx)
}

func (w *AppWorkspace) InitCoderAgentNonInteractive(ctx context.Context) error {
	return w.app.InitCoderAgentNonInteractive(ctx)
}

func (w *AppWorkspace) GetDefaultSmallModel(providerID string) config.SelectedModel {
	return w.app.GetDefaultSmallModel(providerID)
}

// -- Permissions --

func (w *AppWorkspace) PermissionGrant(perm permission.PermissionRequest) bool {
	return w.app.Permissions.Grant(perm)
}

func (w *AppWorkspace) PermissionGrantPersistent(perm permission.PermissionRequest) bool {
	return w.app.Permissions.GrantPersistent(perm)
}

func (w *AppWorkspace) PermissionDeny(perm permission.PermissionRequest) bool {
	return w.app.Permissions.Deny(perm)
}

func (w *AppWorkspace) PermissionMode() permission.PermissionMode {
	return w.app.Permissions.PermissionMode()
}

func (w *AppWorkspace) PermissionSetMode(mode permission.PermissionMode) {
	w.app.Permissions.SetPermissionMode(mode)
}

// -- Host 上下文 --

func (w *AppWorkspace) HostContext(_ context.Context) (proto.HostContext, error) {
	if w.app.AgentCoordinator == nil {
		return proto.HostContext{}, nil
	}
	return w.app.AgentCoordinator.HostContext(), nil
}

// -- Questions --

func (w *AppWorkspace) QuestionAnswer(batchID string, responses []question.Answer) bool {
	return w.app.Questions.Answer(batchID, responses)
}

func (w *AppWorkspace) QuestionCancel(batchID string) bool {
	return w.app.Questions.Cancel(batchID)
}

// -- Pinentry --

func (w *AppWorkspace) PinentryRespond(id, secret string) bool {
	return pinentry.DefaultPrompts().Respond(id, secret)
}

func (w *AppWorkspace) PinentryCancel(id string) bool {
	return pinentry.DefaultPrompts().Cancel(id)
}

// -- SSH prompts --

func (w *AppWorkspace) SSHRespond(id, secret string) bool {
	return sshaskpass.DefaultPrompts().Respond(id, secret)
}

func (w *AppWorkspace) SSHCancel(id string) bool {
	return sshaskpass.DefaultPrompts().Cancel(id)
}

// -- FileTracker --

func (w *AppWorkspace) FileTrackerRecordRead(ctx context.Context, sessionID, path string) {
	w.app.FileTracker.RecordRead(ctx, sessionID, path)
}

func (w *AppWorkspace) FileTrackerLastReadTime(ctx context.Context, sessionID, path string) time.Time {
	return w.app.FileTracker.LastReadTime(ctx, sessionID, path)
}

func (w *AppWorkspace) FileTrackerListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return w.app.FileTracker.ListReadFiles(ctx, sessionID)
}

// -- History --

func (w *AppWorkspace) ListSessionHistory(ctx context.Context, sessionID string) ([]history.File, error) {
	return w.app.History.ListBySession(ctx, sessionID)
}

// -- LSP --

func (w *AppWorkspace) LSPStart(ctx context.Context, path string) {
	w.app.LSPManager.Start(ctx, path)
}

func (w *AppWorkspace) LSPStopAll(ctx context.Context) {
	w.app.LSPManager.StopAll(ctx)
}

func (w *AppWorkspace) LSPGetStates() map[string]LSPClientInfo {
	states := app.GetLSPStates()
	result := make(map[string]LSPClientInfo, len(states))
	for k, v := range states {
		result[k] = LSPClientInfo{
			Name:            v.Name,
			State:           v.State,
			Error:           v.Error,
			DiagnosticCount: v.DiagnosticCount,
			ConnectedAt:     v.ConnectedAt,
		}
	}
	return result
}

func (w *AppWorkspace) LSPGetDiagnosticCounts(name string) lsp.DiagnosticCounts {
	state, ok := app.GetLSPState(name)
	if !ok || state.Client == nil {
		return lsp.DiagnosticCounts{}
	}
	return state.Client.GetDiagnosticCounts()
}

// -- 配置 (读取-只) --

func (w *AppWorkspace) Config() *config.Config {
	return w.store.Config()
}

func (w *AppWorkspace) WorkingDir() string {
	return w.store.WorkingDir()
}

func (w *AppWorkspace) GitBranch(context.Context) (string, error) {
	return gitutil.CurrentBranch(w.store.WorkingDir()), nil
}

func (w *AppWorkspace) Resolver() config.VariableResolver {
	return w.store.Resolver()
}

// -- 配置 mutations --

func (w *AppWorkspace) UpdatePreferredModel(scope config.Scope, modelType config.SelectedModelType, model config.SelectedModel) error {
	return w.store.UpdatePreferredModel(scope, modelType, model)
}

func (w *AppWorkspace) SetCompactMode(scope config.Scope, enabled bool) error {
	return w.store.SetCompactMode(scope, enabled)
}

func (w *AppWorkspace) SetProviderAPIKey(scope config.Scope, providerID string, apiKey any) error {
	if err := w.store.SetProviderAPIKey(scope, providerID, apiKey); err != nil {
		return err
	}
	w.store.SignalAuthComplete(providerID)
	return nil
}

func (w *AppWorkspace) SetConfigField(scope config.Scope, key string, value any) error {
	return w.store.SetConfigField(scope, key, value)
}

func (w *AppWorkspace) SetConfigFields(scope config.Scope, fields map[string]any) error {
	return w.store.SetConfigFields(scope, fields)
}

func (w *AppWorkspace) RemoveConfigField(scope config.Scope, key string) error {
	return w.store.RemoveConfigField(scope, key)
}

func (w *AppWorkspace) ImportCopilot() (*oauth.Token, bool) {
	return w.store.ImportCopilot()
}

func (w *AppWorkspace) RefreshOAuthToken(ctx context.Context, scope config.Scope, providerID string) error {
	return w.store.RefreshOAuthToken(ctx, scope, providerID)
}

// -- Project lifecycle --

func (w *AppWorkspace) ProjectNeedsInitialization() (bool, error) {
	return config.ProjectNeedsInitialization(w.store)
}

func (w *AppWorkspace) MarkProjectInitialized() error {
	return config.MarkProjectInitialized(w.store)
}

func (w *AppWorkspace) InitializePrompt() (string, error) {
	return agent.InitializePrompt(w.store)
}

func (w *AppWorkspace) ListSkills(_ context.Context) ([]skills.CatalogEntry, error) {
	mgr := w.app.Skills
	return skills.Catalog(mgr.ActiveSkills(), mgr.ResolvedPaths(), mgr.WorkingDir()), nil
}

func (w *AppWorkspace) ReadSkill(_ context.Context, skillID string) ([]byte, skills.SkillReadResult, error) {
	mgr := w.app.Skills
	return skills.ReadContent(mgr.ActiveSkills(), mgr.ResolvedPaths(), mgr.WorkingDir(), skillID)
}

// -- MCP operations --

func (w *AppWorkspace) MCPGetStates() map[string]mcptools.ClientInfo {
	return mcptools.GetStates()
}

func (w *AppWorkspace) MCPRefreshPrompts(ctx context.Context, name string) {
	mcptools.RefreshPrompts(ctx, name)
}

func (w *AppWorkspace) MCPRefreshResources(ctx context.Context, name string) {
	mcptools.RefreshResources(ctx, name)
}

func (w *AppWorkspace) RefreshMCPTools(ctx context.Context, name string) {
	mcptools.RefreshTools(ctx, w.store, name)
}

func (w *AppWorkspace) ReadMCPResource(ctx context.Context, name, uri string) ([]MCPResourceContents, error) {
	contents, err := mcptools.ReadResource(ctx, w.store, name, uri)
	if err != nil {
		return nil, err
	}
	result := make([]MCPResourceContents, len(contents))
	for i, c := range contents {
		result[i] = MCPResourceContents{
			URI:      c.URI,
			MIMEType: c.MIMEType,
			Text:     c.Text,
			Blob:     c.Blob,
		}
	}
	return result, nil
}

func (w *AppWorkspace) ListMCPPrompts(context.Context) ([]commands.MCPPrompt, error) {
	return commands.LoadMCPPrompts()
}

func (w *AppWorkspace) GetMCPPrompt(clientID, promptID string, args map[string]string) (string, error) {
	return commands.GetMCPPrompt(w.store, clientID, promptID, args)
}

func (w *AppWorkspace) EnableDockerMCP(ctx context.Context) error {
	mcpConfig, err := w.store.PrepareDockerMCPConfig()
	if err != nil {
		return err
	}

	if err := mcptools.InitializeSingle(ctx, config.DockerMCPName, w.store); err != nil {
		disableErr := mcptools.DisableSingle(w.store, config.DockerMCPName)
		w.store.RemoveDockerMCPInMemory()
		return fmt.Errorf("failed to start docker MCP: %w", errors.Join(err, disableErr))
	}

	if err := w.store.PersistDockerMCPConfig(mcpConfig); err != nil {
		disableErr := mcptools.DisableSingle(w.store, config.DockerMCPName)
		w.store.RemoveDockerMCPInMemory()
		return fmt.Errorf("docker MCP started but failed to persist configuration: %w", errors.Join(err, disableErr))
	}

	return nil
}

func (w *AppWorkspace) DisableDockerMCP() error {
	if err := mcptools.DisableSingle(w.store, config.DockerMCPName); err != nil {
		return fmt.Errorf("failed to disable docker MCP: %w", err)
	}
	return w.store.DisableDockerMCP()
}

// MCPServersDisabled returns MCP servers disabled 为 这
// repository. override set 是 shared 通过 every 会话, including
// 子代理 sessions.
func (w *AppWorkspace) MCPServersDisabled(ctx context.Context) ([]string, error) {
	return w.app.Sessions.MCPDisabledServers(ctx)
}

// MCPServersEnabled returns MCP servers 用 repository-scoped
// enabled override: 配置-disabled servers 用户 enabled here. Startup
// force-starts them 所以 toggle survives restarts.
func (w *AppWorkspace) MCPServersEnabled(ctx context.Context) ([]string, error) {
	return w.app.Sessions.MCPServersEnabled(ctx)
}

// MCPSetServerDisabled adds 或 removes repository-scoped MCP override
// 和 applies it 到 running 客户端. 配置 files 是 never touched.
func (w *AppWorkspace) MCPSetServerDisabled(ctx context.Context, name string, disabled bool) error {
	if err := w.app.Sessions.SetMCPServerDisabled(ctx, name, disabled); err != nil {
		return err
	}
	return mcptools.SetLocalDisabled(ctx, w.store, name, disabled)
}

// MCPSetServerConfigDisabled toggles MCP 服务端's disabled flag 在
// global 配置 和 applies change 到 running 客户端 unless
// repository-scoped override takes precedence.
func (w *AppWorkspace) MCPSetServerConfigDisabled(ctx context.Context, name string, disabled bool) error {
	override, err := session.HasMCPOverride(ctx, w.app.Sessions, name)
	if err != nil {
		return err
	}
	return mcptools.SetConfigDisabled(ctx, w.store, config.ScopeGlobal, name, disabled, override)
}

// MCPStartServer starts named MCP 服务端 even 当 它的 配置 条目 是
// disabled. repository-scoped enabled override recorded 通过
// MCPSetServerDisabled makes start survive restarts. 配置 files 是
// never touched.
func (w *AppWorkspace) MCPStartServer(ctx context.Context, name string) error {
	return mcptools.InitializeSingleForced(ctx, name, w.store)
}

// SkillsDisabled returns skills disabled 为 这 repository.
func (w *AppWorkspace) SkillsDisabled(ctx context.Context) ([]string, error) {
	return w.app.Sessions.SkillsDisabled(ctx)
}

// SkillsEnabled returns skills 用 repository-scoped enabled
// override (配置-disabled skills re-enabled here).
func (w *AppWorkspace) SkillsEnabled(ctx context.Context) ([]string, error) {
	return w.app.Sessions.SkillsEnabled(ctx)
}

// SkillSetDisabled adds 或 removes repository-scoped skill override.
func (w *AppWorkspace) SkillSetDisabled(ctx context.Context, name string, disabled bool) error {
	return w.app.Sessions.SetSkillDisabled(ctx, name, disabled, w.store.SkillConfigDisabled(name))
}

// SkillSetConfigDisabled toggles skill's disabled flag 在 global
// 配置's options.disabled_skills list.
func (w *AppWorkspace) SkillSetConfigDisabled(ctx context.Context, name string, disabled bool) error {
	return w.store.SetSkillDisabledConfig(config.ScopeGlobal, name, disabled)
}

func (w *AppWorkspace) MCPAuthenticate(ctx context.Context, name string) error {
	return mcptools.AuthenticateMCP(ctx, w.store, name)
}

func (w *AppWorkspace) MCPPendingAuth() []mcptools.PendingAuthServer {
	return mcptools.PendingAuthMCPs(w.store)
}

func (w *AppWorkspace) MCPAuthURL(name string) string {
	return mcptools.MCPAuthURL(name)
}

// -- Lifecycle --

func (w *AppWorkspace) Subscribe(program *tea.Program) {
	w.app.Subscribe(program)
}

func (w *AppWorkspace) Shutdown() {
	w.app.Shutdown()
}

// App returns the underlying app.App instance.
func (w *AppWorkspace) App() *app.App {
	return w.app
}

// 存储 returns underlying 配置 存储.
func (w *AppWorkspace) Store() *config.ConfigStore {
	return w.store
}

// Compile-time check 那 AppWorkspace implements 工作区.
var _ Workspace = (*AppWorkspace)(nil)
