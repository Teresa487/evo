// Package 工作区 defines 工作区 interface used 通过 all
// frontends (TUI, CLI) 到 interact 用 running 工作区. Two
// implementations exist: one wrapping local app.App instance 和 one
// wrapping HTTP 客户端 SDK.
package workspace

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/v3/pkg/catwalk"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
)

// Reasons coder agent may 是 unavailable, returned 通过
// 工作区.AgentReadyErr 所以 callers can tell genuinely
// uninitialized agent apart 从 lost 服务端 连接.
var (
	// ErrAgentNotInitialized means 工作区 exists 但 它的 coder
	// agent has 不 been configured/initialized (e.g. no 模型 set).
	ErrAgentNotInitialized = errors.New("coder agent is not initialized")
	// ErrServerUnreachable means 客户端 could 不 reach 服务端
	// 到 determine agent's status (服务端 down, 或 工作区 was
	// torn down out 从 under 客户端).
	ErrServerUnreachable = errors.New("lost connection to the crush server")
	// ErrWorkspaceGone means 服务端 是 reachable 但 no longer knows
	// 这 客户端's 工作区: it was torn down, 或 服务端 was
	// replaced underneath 客户端. subscription loop re-registers
	// 工作区 在 background 当 it sees 这.
	ErrWorkspaceGone = errors.New("the server reset this workspace; reconnecting")
	// ErrStreamClosed means established 事件 流 ended.
	// Resubscribing usually succeeds immediately, 但 events published 在
	// meantime 是 lost 为 good, 所以 客户端 treats it 作为
	// degraded link 那 requires resync.
	ErrStreamClosed = errors.New("the event stream closed; reconnecting")
)

// ConnectionState describes health 的 客户端-服务端 link 作为
// reported 通过 [ClientWorkspace] subscription loop.
type ConnectionState int

const (
	// ConnectionDegraded means 事件 流 是 down (或 工作区
	// was lost 服务端-side) 和 客户端 是 retrying 或 re-registering
	// 在 background.
	ConnectionDegraded ConnectionState = iota
	// ConnectionRecovered means 事件 流 was re-established,
	// possibly against re-created 工作区.
	ConnectionRecovered
)

// ConnectionEvent 是 delivered 到 TUI 作为 tea.Msg 在 degraded 和
// recovered transitions 的 客户端-服务端 link. Local (在-process)
// workspaces never emit it.
type ConnectionEvent struct {
	State ConnectionState
	// Err 是 most recent failure, set 当 State 是
	// ConnectionDegraded.
	Err error
	// Stuck marks degraded 连接 那 has resisted repeated
	// recovery attempts. loop 保持 retrying regardless; UI
	// should escalate 从 transient notice 到 persistent 错误.
	Stuck bool
}

// HostThemeEvent 是 delivered 到 TUI 作为 tea.Msg 当 host
// application running Crush 在 pane ( editor, 为 example) sets 或
// clears 它的 theme 在 服务端. Clients also receive current theme
// 当 their 事件 流 attaches, 和 current theme 或 它的 absence
// after reconnect, 所以 same theme can arrive more than once. Local
// (在-process) workspaces never emit it.
type HostThemeEvent struct {
	// Theme 是 host's theme, 或 nil 当 host cleared it.
	Theme *proto.HostTheme
}

// LSPClientInfo holds information about LSP 客户端's state. 这 是
// frontend-facing type; implementations translate 从
// underlying app 或 proto representation.
type LSPClientInfo struct {
	Name            string
	State           lsp.ServerState
	Error           error
	DiagnosticCount int
	ConnectedAt     time.Time
}

// LSPEventType represents type 的 LSP 事件.
type LSPEventType string

const (
	LSPEventStateChanged       LSPEventType = "state_changed"
	LSPEventDiagnosticsChanged LSPEventType = "diagnostics_changed"
)

// LSPEvent represents LSP 事件 forwarded 到 TUI.
type LSPEvent struct {
	Type            LSPEventType
	Name            string
	State           lsp.ServerState
	Error           error
	DiagnosticCount int
}

// AgentModel holds 活跃 agent 和 它的 模型, 作为 exposed 到 UI.
type AgentModel struct {
	// Agent names 活跃 agent (配置.AgentCoder 或 配置.AgentPlan),
	// which every 客户端 的 服务端 工作区 shares. Empty 当 unknown,
	// 作为 用 服务端 too old 到 报告 it.
	Agent      string
	CatwalkCfg catwalk.Model
	ModelCfg   config.SelectedModel
}

// 工作区 是 main abstraction consumed 通过 TUI 和 CLI. 它
// groups every operation frontend needs 到 perform against running
// 工作区, regardless 的 whether 工作区 是 在-process 或
// remote.
type Workspace interface {
	// Sessions
	CreateSession(ctx context.Context, title string) (session.Session, error)
	GetSession(ctx context.Context, sessionID string) (session.Session, error)
	ListSessions(ctx context.Context) ([]session.Session, error)
	SaveSession(ctx context.Context, sess session.Session) (session.Session, error)
	DeleteSession(ctx context.Context, sessionID string) error
	CreateAgentToolSessionID(messageID, toolCallID string) string
	ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool)
	// SetCurrentSession reports 会话 这 客户端 是 currently
	// viewing. Empty sessionID clears 条目 (e.g. landing screen).
	// In single-客户端 local mode 这 是 no-op. In 客户端/服务端
	// mode it informs 服务端's per-客户端 presence map 所以 other
	// observers can compute attached-客户端 counts per 会话.
	SetCurrentSession(ctx context.Context, sessionID string) error

	// Messages
	ListMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	ListUserMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	ListAllUserMessages(ctx context.Context) ([]message.Message, error)

	// RoutesChannelEvents reports whether 工作区's backing
	// process routes MCP 渠道 events into sessions itself. When
	// true (客户端/服务端 mode), frontends must 不 inject 渠道
	// messages 在 EventChannelMessage — 服务端 injects each 事件
	// exactly once 和 resulting turn arrives through normal
	// 会话/消息 事件 流. When false (在-process mode),
	// frontend owns injection.
	RoutesChannelEvents() bool

	// Agent
	AgentRun(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) error
	AgentRunChannel(ctx context.Context, channel, sessionID, prompt string, attachments ...message.Attachment) error
	AgentRunShellCommand(ctx context.Context, sessionID, command string, termWidth int, onProgress func(string), isFirstMessage bool) (proto.ShellCommandResponse, error)
	AgentCancel(sessionID string)
	AgentIsBusy() bool
	AgentIsSessionBusy(sessionID string) bool
	AgentModel() AgentModel
	AgentIsReady() bool
	// AgentReadyErr reports nil 当 coder agent 是 ready 到 受理
	// work, 或 descriptive 错误 otherwise: ErrAgentNotInitialized
	// 当 agent simply isn't set up, 或 ErrServerUnreachable
	// (wrapped) 当 客户端 could 不 reach 服务端 到 find out.
	// 它 lets UI show actionable 消息 instead 的 collapsing
	// both cases into "agent 离线".
	AgentReadyErr() error
	AgentQueuedPrompts(sessionID string) int
	AgentQueuedPromptsList(sessionID string) []string
	AgentClearQueue(sessionID string)
	AgentSetMain(agentID string) error
	AgentSummarize(ctx context.Context, sessionID string) error
	UpdateAgentModel(ctx context.Context) error
	InitCoderAgent(ctx context.Context) error
	InitCoderAgentNonInteractive(ctx context.Context) error
	GetDefaultSmallModel(providerID string) config.SelectedModel

	// Permissions
	//
	// PermissionGrant, PermissionGrantPersistent, 和 PermissionDeny
	// return true 如果 调用 resolved pending 请求 和 false 如果
	// it had already been resolved 通过 another 订阅者 (或 是 no
	// longer pending). 一个 false return 是 不 错误; modal can
	// still close locally because resolution 将 arrive via
	// PermissionNotification 事件 流 regardless 的 which 客户端
	// won the race.
	PermissionGrant(perm permission.PermissionRequest) bool
	PermissionGrantPersistent(perm permission.PermissionRequest) bool
	PermissionDeny(perm permission.PermissionRequest) bool
	PermissionMode() permission.PermissionMode
	PermissionSetMode(mode permission.PermissionMode)

	// HostContext returns what host application has 在 view, such
	// 作为 它的 open files, 或 zero 上下文 当 there 是 no host.
	HostContext(ctx context.Context) (proto.HostContext, error)

	// Questions
	//
	// QuestionAnswer submits responses 到 pending question batch
	// 用 given ID.
	QuestionAnswer(batchID string, responses []question.Answer) bool

	// QuestionCancel cancels pending question batch 用
	// given ID.
	QuestionCancel(batchID string) bool

	// Pinentry
	//
	// PinentryRespond resolves pending GPG credential 提示词 用
	// secret; PinentryCancel dismisses it. Both return false 当
	// no matching 提示词 是 pending.
	PinentryRespond(id, secret string) bool
	PinentryCancel(id string) bool
	// SSH prompts
	//
	// SSHRespond resolves pending integrated SSH credential 或
	// confirmation 提示词 用 secret ("yes" 为 confirmations);
	// SSHCancel dismisses it. Both return false 当 no matching 提示词
	// 是 pending.
	SSHRespond(id, secret string) bool
	SSHCancel(id string) bool

	// FileTracker
	FileTrackerRecordRead(ctx context.Context, sessionID, path string)
	FileTrackerLastReadTime(ctx context.Context, sessionID, path string) time.Time
	FileTrackerListReadFiles(ctx context.Context, sessionID string) ([]string, error)

	// History
	ListSessionHistory(ctx context.Context, sessionID string) ([]history.File, error)

	// LSP
	LSPStart(ctx context.Context, path string)
	LSPStopAll(ctx context.Context)
	LSPGetStates() map[string]LSPClientInfo
	LSPGetDiagnosticCounts(name string) lsp.DiagnosticCounts

	// 配置 (读取-只 data)
	Config() *config.Config
	WorkingDir() string
	GitBranch(ctx context.Context) (string, error)
	Resolver() config.VariableResolver

	// 配置 mutations (proxied 到 服务端 在 客户端 mode)
	UpdatePreferredModel(scope config.Scope, modelType config.SelectedModelType, model config.SelectedModel) error
	SetCompactMode(scope config.Scope, enabled bool) error
	SetProviderAPIKey(scope config.Scope, providerID string, apiKey any) error
	SetConfigField(scope config.Scope, key string, value any) error
	SetConfigFields(scope config.Scope, fields map[string]any) error
	RemoveConfigField(scope config.Scope, key string) error
	ImportCopilot() (*oauth.Token, bool)
	RefreshOAuthToken(ctx context.Context, scope config.Scope, providerID string) error

	// Project lifecycle
	ProjectNeedsInitialization() (bool, error)
	MarkProjectInitialized() error
	InitializePrompt() (string, error)
	ListSkills(ctx context.Context) ([]skills.CatalogEntry, error)
	ReadSkill(ctx context.Context, skillID string) ([]byte, skills.SkillReadResult, error)

	// MCP operations (服务端-side 在 客户端 mode)
	MCPGetStates() map[string]mcptools.ClientInfo
	MCPRefreshPrompts(ctx context.Context, name string)
	MCPRefreshResources(ctx context.Context, name string)
	RefreshMCPTools(ctx context.Context, name string)
	ReadMCPResource(ctx context.Context, name, uri string) ([]MCPResourceContents, error)
	ListMCPPrompts(ctx context.Context) ([]commands.MCPPrompt, error)
	GetMCPPrompt(clientID, promptID string, args map[string]string) (string, error)
	EnableDockerMCP(ctx context.Context) error
	DisableDockerMCP() error
	MCPServersDisabled(ctx context.Context) ([]string, error)
	MCPSetServerDisabled(ctx context.Context, name string, disabled bool) error
	MCPSetServerConfigDisabled(ctx context.Context, name string, disabled bool) error
	MCPServersEnabled(ctx context.Context) ([]string, error)
	MCPStartServer(ctx context.Context, name string) error
	MCPAuthenticate(ctx context.Context, name string) error
	MCPPendingAuth() []mcptools.PendingAuthServer
	MCPAuthURL(name string) string

	// Skills toggles
	SkillsDisabled(ctx context.Context) ([]string, error)
	SkillSetDisabled(ctx context.Context, name string, disabled bool) error
	SkillsEnabled(ctx context.Context) ([]string, error)
	SkillSetConfigDisabled(ctx context.Context, name string, disabled bool) error

	// Events
	Subscribe(program *tea.Program)
	Shutdown()
}

// MCPResourceContents holds contents 的 MCP resource.
type MCPResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     []byte `json:"blob,omitempty"`
}
