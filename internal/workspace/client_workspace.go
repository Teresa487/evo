// client_workspace.go —— Workspace 的远程实现：所有操作通过 RPC 发给服务端。
//
// 从产物反推：
//
//	远端 TUI 的 AgentRun → ClientWorkspace.AgentRun → internal/client →
//	  HTTP /v1/... → internal/server → backend → agent.Run
//
// 它缓存 proto.Workspace，订阅 SSE 事件，并在配置变更后刷新缓存，
// 是多客户端架构里「客户端这一侧」的适配层。
package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/herdr"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pinentry"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/server"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/sshaskpass"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/pkg/browser"
)

// ClientWorkspace implements 工作区 interface 通过 delegating all
// operations 到 remote 服务端 via 客户端 SDK. 它 caches
// proto.工作区 returned 在 creation time 和 refreshes it after
// 配置-mutating operations.
type ClientWorkspace struct {
	client *client.Client

	mu     sync.RWMutex
	ws     proto.Workspace
	skills *skills.Manager
	// lastSession 是 most recent 会话 ID reported via
	// SetCurrentSession, "" 为 landing screen. subscription loop
	// reports it every time 事件 流 attaches: 服务端 只
	// accepts 报告 从 attached 客户端, 和 它的 presence 条目 (或
	// whole 工作区) may have been re-created 在 meantime.
	lastSession string
	// presenceMu serializes 会话 reports, 所以 报告 subscription
	// loop 读取 before newer SetCurrentSession can never reach
	// 服务端 after it.
	presenceMu sync.Mutex
	// mainAgent 是 agent 这 客户端 last made main agent (empty
	// until it switches). Recovery restores it: re-created 工作区
	// runs coder agent, 所以 plan mode would otherwise get 写入 tools
	// while the UI still says plan.
	mainAgent string

	// subCtx bounds lifetime 的 事件 subscription (和 它的
	// reconnect loop). Shutdown cancels it 所以 Subscribe stops
	// reconnecting instead 的 racing teardown.
	subCtx    context.Context
	subCancel context.CancelFunc
	// subStarted reports whether subscription loop ever ran, 和
	// subDone 是 closed 当 it returns. Shutdown uses them 到 let
	// 在-flight 工作区 recovery finish before it says goodbye 到
	// 服务端, 所以 工作区 it releases 是 one recovery just
	// minted.
	subStarted atomic.Bool
	subDone    chan struct{}

	// herdrClient reports agent state 到 herdr 当 running inside
	// herdr-managed pane. Nil 当 不 在 herdr environment.
	herdrClient *herdr.Client

	// respawnServer brings 服务端 back 在 客户端's address after
	// previous one died. Nil leaves the loop retrying the address.
	respawnServer func(context.Context) error
}

// ClientWorkspaceOption configures ClientWorkspace 在 construction time.
type ClientWorkspaceOption func(*ClientWorkspace)

// WithServerRespawn sets 钩子 subscription loop calls 当
// 服务端 是 gone, 那 是, nothing listens 在 它的 address any more.
// 钩子 should start 服务端 there; loop then finds 那 new 服务端
// does 不 know 这 工作区 和 re-registers it. 钩子 runs 在
// subscription goroutine 用 subscription 上下文, which Shutdown
// cancels, 和 loop stops calling it once 那 happens. Its failures
// count toward loop's escalation 到 stuck 连接.
func WithServerRespawn(respawn func(ctx context.Context) error) ClientWorkspaceOption {
	return func(w *ClientWorkspace) {
		w.respawnServer = respawn
	}
}

// SSE reconnect backoff bounds 为 工作区 事件 流. Declared
// 作为 vars (不 consts) 所以 tests can shrink delays.
var (
	sseReconnectInitialBackoff = 250 * time.Millisecond
	sseReconnectMaxBackoff     = 10 * time.Second
)

// NewClientWorkspace creates new ClientWorkspace 那 proxies all
// operations through given 客户端 SDK. ws parameter 是
// proto.工作区 snapshot returned 通过 服务端 在 creation time.
// snapshot's Skills 字段 seeds process-local skills.Manager 所以
// TUI sees discovery state before first SSE 事件 arrives.
// manager 是 constructed 用 WithGlobalMirror because 客户端
// process represents exactly one 工作区 和 TUI reads
// skills.GetLatestStates directly 在 construction time.
func NewClientWorkspace(c *client.Client, ws proto.Workspace, opts ...ClientWorkspaceOption) *ClientWorkspace {
	if ws.Config != nil {
		ws.Config.SetupAgents()
		ws.Config.NormalizeOptions()
	}
	states := protoToSkillStates(ws.Skills)
	mgr := skills.NewManager(nil, nil, states, skills.WithGlobalMirror())
	subCtx, subCancel := context.WithCancel(context.Background())
	w := &ClientWorkspace{
		client:      c,
		ws:          ws,
		skills:      mgr,
		subCtx:      subCtx,
		subCancel:   subCancel,
		subDone:     make(chan struct{}),
		herdrClient: herdr.Init(),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// refreshWorkspace re-fetches 工作区 从 服务端, updating
// cached snapshot. Called after 配置-mutating operations.
func (w *ClientWorkspace) refreshWorkspace() {
	updated, err := w.client.GetWorkspace(context.Background(), w.workspaceID())
	if err != nil {
		slog.Error("Failed to refresh workspace", "error", err)
		return
	}
	if updated.Config != nil {
		updated.Config.SetupAgents()
		updated.Config.NormalizeOptions()
	}
	w.mu.Lock()
	w.ws = *updated
	w.mu.Unlock()
}

// cached returns snapshot 的 cached 工作区.
func (w *ClientWorkspace) cached() proto.Workspace {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.ws
}

// workspaceID returns cached 工作区 ID.
func (w *ClientWorkspace) workspaceID() string {
	return w.cached().ID
}

// -- Sessions --

func (w *ClientWorkspace) CreateSession(ctx context.Context, title string) (session.Session, error) {
	sess, err := w.client.CreateSession(ctx, w.workspaceID(), title)
	if err != nil {
		return session.Session{}, err
	}
	return protoToSession(*sess), nil
}

func (w *ClientWorkspace) GetSession(ctx context.Context, sessionID string) (session.Session, error) {
	sess, err := w.client.GetSession(ctx, w.workspaceID(), sessionID)
	if err != nil {
		return session.Session{}, err
	}
	return protoToSession(*sess), nil
}

func (w *ClientWorkspace) ListSessions(ctx context.Context) ([]session.Session, error) {
	protoSessions, err := w.client.ListSessions(ctx, w.workspaceID())
	if err != nil {
		return nil, err
	}
	sessions := make([]session.Session, len(protoSessions))
	for i, s := range protoSessions {
		sessions[i] = protoToSession(s)
	}
	return sessions, nil
}

func (w *ClientWorkspace) SaveSession(ctx context.Context, sess session.Session) (session.Session, error) {
	saved, err := w.client.SaveSession(ctx, w.workspaceID(), sessionToProto(sess))
	if err != nil {
		return session.Session{}, err
	}
	return protoToSession(*saved), nil
}

func (w *ClientWorkspace) DeleteSession(ctx context.Context, sessionID string) error {
	return w.client.DeleteSession(ctx, w.workspaceID(), sessionID)
}

func (w *ClientWorkspace) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return fmt.Sprintf("%s$$%s", messageID, toolCallID)
}

func (w *ClientWorkspace) ParseAgentToolSessionID(sessionID string) (string, string, bool) {
	parts := strings.Split(sessionID, "$$")
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// SetCurrentSession reports 会话 这 客户端 是 currently
// viewing 到 服务端. Empty sessionID clears 条目. 报告
// scopes 客户端's 事件 流: 权限 和 question prompts
// 是 只 delivered 到 clients viewing 会话 那 raised them.
// Errors 是 propagated 到 caller; TUI logs 和 ignores them
// since presence record 是 best-effort, 不 correctness-critical
// state.
func (w *ClientWorkspace) SetCurrentSession(ctx context.Context, sessionID string) error {
	w.herdrClient.SetSessionID(sessionID)
	w.presenceMu.Lock()
	defer w.presenceMu.Unlock()
	w.mu.Lock()
	w.lastSession = sessionID
	w.mu.Unlock()
	return w.sendCurrentSession(ctx, sessionID)
}

// presenceReportTimeout bounds 会话 报告, 所以 wedged 服务端 cannot
// hold up reports 那 follow it.
const presenceReportTimeout = 5 * time.Second

// sendCurrentSession sends 会话 报告. Callers hold presenceMu.
func (w *ClientWorkspace) sendCurrentSession(ctx context.Context, sessionID string) error {
	ctx, cancel := context.WithTimeout(ctx, presenceReportTimeout)
	defer cancel()
	return w.client.SetCurrentSession(ctx, w.workspaceID(), sessionID)
}

// reportCurrentSession tells 服务端 which 会话 这 客户端 views
// once 它的 事件 流 has attached. 服务端 sends 客户端 那 never
// reported every 会话's 权限 和 question prompts, 和 rejects
// 报告 made before 流 attached, 所以 without 这 窗口 在
// landing screen, 或 one whose first 报告 lost 那 race, would pop up
// prompts 为 sessions it does 不 show.
func (w *ClientWorkspace) reportCurrentSession() {
	w.presenceMu.Lock()
	defer w.presenceMu.Unlock()
	w.mu.RLock()
	sid := w.lastSession
	w.mu.RUnlock()
	if err := w.sendCurrentSession(w.subCtx, sid); err != nil && w.subCtx.Err() == nil {
		slog.Warn("Failed to report the current session", "error", err)
	}
}

// RoutesChannelEvents reports true: 服务端 backend injects each
// 渠道 事件 exactly once (see backend.startChannelRouter), 所以 这
// 客户端 must 不 inject 在 EventChannelMessage — 用 several clients
// attached, per-客户端 injection would 运行 same 事件 multiple
// times, 和 it would never 运行 用 zero clients attached.
func (w *ClientWorkspace) RoutesChannelEvents() bool { return true }

// -- Messages --

func (w *ClientWorkspace) ListMessages(ctx context.Context, sessionID string) ([]message.Message, error) {
	msgs, err := w.client.ListMessages(ctx, w.workspaceID(), sessionID)
	if err != nil {
		return nil, err
	}
	return protoToMessages(msgs), nil
}

func (w *ClientWorkspace) ListUserMessages(ctx context.Context, sessionID string) ([]message.Message, error) {
	msgs, err := w.client.ListUserMessages(ctx, w.workspaceID(), sessionID)
	if err != nil {
		return nil, err
	}
	return protoToMessages(msgs), nil
}

func (w *ClientWorkspace) ListAllUserMessages(ctx context.Context) ([]message.Message, error) {
	msgs, err := w.client.ListAllUserMessages(ctx, w.workspaceID())
	if err != nil {
		return nil, err
	}
	return protoToMessages(msgs), nil
}

// -- Agent --

func (w *ClientWorkspace) AgentRun(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) error {
	// interactive TUI matches notify.RunComplete 通过 会话, 不
	// RunID, 所以 passing empty RunID 是 correct here: it skips
	// correlator stamping 路径 without functional consequences.
	return w.client.SendMessage(ctx, w.workspaceID(), sessionID, "", prompt, attachments...)
}

// AgentRunChannel 是 unreachable 在 客户端/服务端 mode: TUI skips
// local injection 当 工作区 routes 渠道 events itself, 所以
// 服务端's injection 路径 owns every 渠道-originated turn.
// Returning 错误 rather than sending 渠道 over wire
// keeps 渠道 binding 的 运行 服务端-side 只 — 客户端 must
// 不 是 able 到 标记 它的 own turn 作为 渠道-originated.
func (w *ClientWorkspace) AgentRunChannel(ctx context.Context, channel, sessionID, prompt string, attachments ...message.Attachment) error {
	return errors.New("channel-originated runs are injected server-side in client/server mode")
}

func (w *ClientWorkspace) AgentRunShellCommand(ctx context.Context, sessionID, command string, termWidth int, _ func(string), isFirstMessage bool) (proto.ShellCommandResponse, error) {
	return w.client.RunShellCommand(ctx, w.workspaceID(), sessionID, command, termWidth, isFirstMessage)
}

func (w *ClientWorkspace) AgentCancel(sessionID string) {
	_ = w.client.CancelAgentSession(context.Background(), w.workspaceID(), sessionID)
}

func (w *ClientWorkspace) AgentIsBusy() bool {
	info, err := w.client.GetAgentInfo(context.Background(), w.workspaceID())
	if err != nil {
		return false
	}
	return info.IsBusy
}

func (w *ClientWorkspace) AgentIsSessionBusy(sessionID string) bool {
	info, err := w.client.GetAgentSessionInfo(context.Background(), w.workspaceID(), sessionID)
	if err != nil {
		return false
	}
	return info.IsBusy
}

func (w *ClientWorkspace) AgentModel() AgentModel {
	info, err := w.client.GetAgentInfo(context.Background(), w.workspaceID())
	if err != nil {
		return AgentModel{}
	}
	return AgentModel{
		Agent:      info.MainAgent,
		CatwalkCfg: info.Model,
		ModelCfg:   info.ModelCfg,
	}
}

func (w *ClientWorkspace) AgentIsReady() bool {
	return w.AgentReadyErr() == nil
}

func (w *ClientWorkspace) AgentReadyErr() error {
	info, err := w.client.GetAgentInfo(context.Background(), w.workspaceID())
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			// 服务端 answered, it just does 不 know 这 工作区
			// any more. subscription loop 是 already re-registering;
			// saying "lost 连接" here would 是 plainly wrong.
			return ErrWorkspaceGone
		}
		// 工作区/服务端 could 不 是 reached. 这 是 distinct
		// 从 initialized-但-不-ready agent: 服务端 may have
		// torn 工作区 down 或 restarted underneath us.
		return fmt.Errorf("%w: %v", ErrServerUnreachable, err)
	}
	if !info.IsReady {
		return ErrAgentNotInitialized
	}
	return nil
}

func (w *ClientWorkspace) AgentQueuedPrompts(sessionID string) int {
	count, err := w.client.GetAgentSessionQueuedPrompts(context.Background(), w.workspaceID(), sessionID)
	if err != nil {
		return 0
	}
	return count
}

func (w *ClientWorkspace) AgentQueuedPromptsList(sessionID string) []string {
	prompts, err := w.client.GetAgentSessionQueuedPromptsList(context.Background(), w.workspaceID(), sessionID)
	if err != nil {
		return nil
	}
	return prompts
}

func (w *ClientWorkspace) AgentClearQueue(sessionID string) {
	_ = w.client.ClearAgentSessionQueuedPrompts(context.Background(), w.workspaceID(), sessionID)
}

func (w *ClientWorkspace) AgentSetMain(agentID string) error {
	if err := w.client.SetMainAgent(context.Background(), w.workspaceID(), agentID); err != nil {
		return err
	}
	w.mu.Lock()
	w.mainAgent = agentID
	w.mu.Unlock()
	return nil
}

func (w *ClientWorkspace) AgentSummarize(ctx context.Context, sessionID string) error {
	return w.client.AgentSummarizeSession(ctx, w.workspaceID(), sessionID)
}

func (w *ClientWorkspace) UpdateAgentModel(ctx context.Context) error {
	return w.client.UpdateAgent(ctx, w.workspaceID())
}

func (w *ClientWorkspace) InitCoderAgent(ctx context.Context) error {
	return w.client.InitiateAgentProcessing(ctx, w.workspaceID(), true)
}

func (w *ClientWorkspace) InitCoderAgentNonInteractive(ctx context.Context) error {
	return w.client.InitiateAgentProcessing(ctx, w.workspaceID(), false)
}

func (w *ClientWorkspace) GetDefaultSmallModel(providerID string) config.SelectedModel {
	model, err := w.client.GetDefaultSmallModel(context.Background(), w.workspaceID(), providerID)
	if err != nil {
		return config.SelectedModel{}
	}
	return *model
}

// -- Permissions --

// PermissionGrant grants 权限 请求 once. Like other
// answers, it leaves params out 的 请求 it sends back:
// 服务端 resolves 请求 通过 它的 ID 和 never reads them, 和 older
// servers reject MCP 工具's params, which 是 JSON string, 所以
// sending them would lose the answer.
func (w *ClientWorkspace) PermissionGrant(perm permission.PermissionRequest) bool {
	resolved, _ := w.client.GrantPermission(context.Background(), w.workspaceID(), proto.PermissionGrant{
		Permission: proto.PermissionRequest{
			ID:          perm.ID,
			SessionID:   perm.SessionID,
			ToolCallID:  perm.ToolCallID,
			ToolName:    perm.ToolName,
			Description: perm.Description,
			Action:      perm.Action,
			Path:        perm.Path,
		},
		Action: proto.PermissionAllow,
	})
	return resolved
}

func (w *ClientWorkspace) PermissionGrantPersistent(perm permission.PermissionRequest) bool {
	resolved, _ := w.client.GrantPermission(context.Background(), w.workspaceID(), proto.PermissionGrant{
		Permission: proto.PermissionRequest{
			ID:          perm.ID,
			SessionID:   perm.SessionID,
			ToolCallID:  perm.ToolCallID,
			ToolName:    perm.ToolName,
			Description: perm.Description,
			Action:      perm.Action,
			Path:        perm.Path,
		},
		Action: proto.PermissionAllowForSession,
	})
	return resolved
}

func (w *ClientWorkspace) PermissionDeny(perm permission.PermissionRequest) bool {
	resolved, _ := w.client.GrantPermission(context.Background(), w.workspaceID(), proto.PermissionGrant{
		Permission: proto.PermissionRequest{
			ID:          perm.ID,
			SessionID:   perm.SessionID,
			ToolCallID:  perm.ToolCallID,
			ToolName:    perm.ToolName,
			Description: perm.Description,
			Action:      perm.Action,
			Path:        perm.Path,
		},
		Action: proto.PermissionDeny,
	})
	return resolved
}

func (w *ClientWorkspace) PermissionMode() permission.PermissionMode {
	return proto.ProtoModeToPermission(w.cached().PermissionMode)
}

func (w *ClientWorkspace) PermissionSetMode(mode permission.PermissionMode) {
	_ = w.client.SetPermissionMode(context.Background(), w.workspaceID(), proto.PermissionModeToProto(mode))
}

// -- Host 上下文 --

func (w *ClientWorkspace) HostContext(ctx context.Context) (proto.HostContext, error) {
	return w.client.GetHostContext(ctx, w.workspaceID())
}

// -- Questions --

// QuestionAnswer submits answers 为 question via 客户端 SDK.
func (w *ClientWorkspace) QuestionAnswer(batchID string, responses []question.Answer) bool {
	protoResp := proto.QuestionAnswer{
		BatchRequestID: batchID,
		Responses:      make([]proto.QuestionResponse, len(responses)),
	}
	for i, r := range responses {
		protoResp.Responses[i] = proto.QuestionResponse{
			QuestionID:  r.QuestionID,
			SelectedIDs: r.SelectedIDs,
			FillInText:  r.FillInText,
			Yes:         r.Yes,
			Notes:       r.Notes,
		}
	}
	resolved, err := w.client.AnswerQuestionBatch(context.Background(), w.workspaceID(), protoResp)
	if err != nil {
		slog.Error("Failed to answer question", "error", err)
		return false
	}
	return resolved
}

// QuestionCancel cancels pending question via 客户端 SDK.
func (w *ClientWorkspace) QuestionCancel(batchID string) bool {
	cancelled, err := w.client.CancelQuestionBatch(context.Background(), w.workspaceID(), batchID)
	if err != nil {
		slog.Error("Failed to cancel question", "error", err)
		return false
	}
	return cancelled
}

// -- Pinentry --

// PinentryRespond submits secret 为 pending integrated pinentry
// 提示词 via 客户端 SDK.
func (w *ClientWorkspace) PinentryRespond(id, secret string) bool {
	resolved, err := w.client.AnswerPinentry(context.Background(), w.workspaceID(), proto.PinentryAnswer{
		RequestID: id,
		Secret:    secret,
	})
	if err != nil {
		slog.Error("Failed to answer pinentry prompt", "error", err)
		return false
	}
	return resolved
}

// PinentryCancel dismisses pending integrated pinentry 提示词 via
// 客户端 SDK.
func (w *ClientWorkspace) PinentryCancel(id string) bool {
	cancelled, err := w.client.CancelPinentry(context.Background(), w.workspaceID(), proto.PinentryCancel{
		RequestID: id,
	})
	if err != nil {
		slog.Error("Failed to cancel pinentry prompt", "error", err)
		return false
	}
	return cancelled
}

// -- SSH prompts --

// SSHRespond submits secret 为 pending integrated SSH 提示词 via
// 客户端 SDK. Confirmations answer "yes".
func (w *ClientWorkspace) SSHRespond(id, secret string) bool {
	resolved, err := w.client.AnswerSSH(context.Background(), w.workspaceID(), proto.SSHAnswer{
		RequestID: id,
		Secret:    secret,
	})
	if err != nil {
		slog.Error("Failed to answer ssh prompt", "error", err)
		return false
	}
	return resolved
}

// SSHCancel dismisses pending integrated SSH 提示词 via 客户端
// SDK.
func (w *ClientWorkspace) SSHCancel(id string) bool {
	cancelled, err := w.client.CancelSSH(context.Background(), w.workspaceID(), proto.SSHCancel{
		RequestID: id,
	})
	if err != nil {
		slog.Error("Failed to cancel ssh prompt", "error", err)
		return false
	}
	return cancelled
}

// -- FileTracker --

func (w *ClientWorkspace) FileTrackerRecordRead(ctx context.Context, sessionID, path string) {
	_ = w.client.FileTrackerRecordRead(ctx, w.workspaceID(), sessionID, path)
}

func (w *ClientWorkspace) FileTrackerLastReadTime(ctx context.Context, sessionID, path string) time.Time {
	t, err := w.client.FileTrackerLastReadTime(ctx, w.workspaceID(), sessionID, path)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (w *ClientWorkspace) FileTrackerListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return w.client.FileTrackerListReadFiles(ctx, w.workspaceID(), sessionID)
}

// -- History --

func (w *ClientWorkspace) ListSessionHistory(ctx context.Context, sessionID string) ([]history.File, error) {
	files, err := w.client.ListSessionHistoryFiles(ctx, w.workspaceID(), sessionID)
	if err != nil {
		return nil, err
	}
	return protoToFiles(files), nil
}

// -- LSP --

func (w *ClientWorkspace) LSPStart(ctx context.Context, path string) {
	_ = w.client.LSPStart(ctx, w.workspaceID(), path)
}

func (w *ClientWorkspace) LSPStopAll(ctx context.Context) {
	_ = w.client.LSPStopAll(ctx, w.workspaceID())
}

func (w *ClientWorkspace) LSPGetStates() map[string]LSPClientInfo {
	states, err := w.client.GetLSPs(context.Background(), w.workspaceID())
	if err != nil {
		return nil
	}
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

func (w *ClientWorkspace) LSPGetDiagnosticCounts(name string) lsp.DiagnosticCounts {
	diags, err := w.client.GetLSPDiagnostics(context.Background(), w.workspaceID(), name)
	if err != nil {
		return lsp.DiagnosticCounts{}
	}
	var counts lsp.DiagnosticCounts
	for _, fileDiags := range diags {
		for _, d := range fileDiags {
			switch d.Severity {
			case protocol.SeverityError:
				counts.Error++
			case protocol.SeverityWarning:
				counts.Warning++
			case protocol.SeverityInformation:
				counts.Information++
			case protocol.SeverityHint:
				counts.Hint++
			}
		}
	}
	return counts
}

// -- 配置 (读取-只) --

func (w *ClientWorkspace) Config() *config.Config {
	return w.cached().Config
}

func (w *ClientWorkspace) WorkingDir() string {
	return w.cached().Path
}

// GitBranch asks 服务端 为 branch checked out 在 工作区's
// working 目录. Callers keep 这 off render 路径; TUI polls it
// 在 ticker 和 renders 从 它的 own state.
func (w *ClientWorkspace) GitBranch(ctx context.Context) (string, error) {
	return w.client.GitBranch(ctx, w.workspaceID())
}

func (w *ClientWorkspace) Resolver() config.VariableResolver {
	return config.IdentityResolver()
}

// -- 配置 mutations --

func (w *ClientWorkspace) UpdatePreferredModel(scope config.Scope, modelType config.SelectedModelType, model config.SelectedModel) error {
	err := w.client.UpdatePreferredModel(context.Background(), w.workspaceID(), scope, modelType, model)
	if err == nil {
		w.refreshWorkspace()
	}
	return err
}

func (w *ClientWorkspace) SetCompactMode(scope config.Scope, enabled bool) error {
	err := w.client.SetCompactMode(context.Background(), w.workspaceID(), scope, enabled)
	if err == nil {
		w.refreshWorkspace()
	}
	return err
}

func (w *ClientWorkspace) SetProviderAPIKey(scope config.Scope, providerID string, apiKey any) error {
	err := w.client.SetProviderAPIKey(context.Background(), w.workspaceID(), scope, providerID, apiKey)
	if err == nil {
		w.refreshWorkspace()
	}
	return err
}

func (w *ClientWorkspace) SetConfigField(scope config.Scope, key string, value any) error {
	err := w.client.SetConfigField(context.Background(), w.workspaceID(), scope, key, value)
	if err == nil {
		w.refreshWorkspace()
	}
	return err
}

func (w *ClientWorkspace) SetConfigFields(scope config.Scope, fields map[string]any) error {
	for key, value := range fields {
		if err := w.SetConfigField(scope, key, value); err != nil {
			return err
		}
	}
	return nil
}

func (w *ClientWorkspace) RemoveConfigField(scope config.Scope, key string) error {
	err := w.client.RemoveConfigField(context.Background(), w.workspaceID(), scope, key)
	if err == nil {
		w.refreshWorkspace()
	}
	return err
}

func (w *ClientWorkspace) ImportCopilot() (*oauth.Token, bool) {
	token, ok, err := w.client.ImportCopilot(context.Background(), w.workspaceID())
	if err != nil {
		return nil, false
	}
	if ok {
		w.refreshWorkspace()
	}
	return token, ok
}

func (w *ClientWorkspace) RefreshOAuthToken(ctx context.Context, scope config.Scope, providerID string) error {
	err := w.client.RefreshOAuthToken(ctx, w.workspaceID(), scope, providerID)
	if err == nil {
		w.refreshWorkspace()
	}
	return err
}

// -- Project lifecycle --

func (w *ClientWorkspace) ProjectNeedsInitialization() (bool, error) {
	return w.client.ProjectNeedsInitialization(context.Background(), w.workspaceID())
}

func (w *ClientWorkspace) MarkProjectInitialized() error {
	return w.client.MarkProjectInitialized(context.Background(), w.workspaceID())
}

func (w *ClientWorkspace) InitializePrompt() (string, error) {
	return w.client.GetInitializePrompt(context.Background(), w.workspaceID())
}

func (w *ClientWorkspace) ListSkills(ctx context.Context) ([]skills.CatalogEntry, error) {
	entries, err := w.client.ListSkills(ctx, w.workspaceID())
	if err != nil {
		return nil, err
	}
	result := make([]skills.CatalogEntry, len(entries))
	for i, entry := range entries {
		result[i] = skills.CatalogEntry{
			ID:            entry.ID,
			Name:          entry.Name,
			Description:   entry.Description,
			Label:         entry.Label,
			Source:        skills.SourceType(entry.Source),
			UserInvocable: entry.UserInvocable,
		}
	}
	return result, nil
}

func (w *ClientWorkspace) ReadSkill(ctx context.Context, skillID string) ([]byte, skills.SkillReadResult, error) {
	resp, err := w.client.ReadSkill(ctx, w.workspaceID(), skillID)
	if err != nil {
		return nil, skills.SkillReadResult{}, err
	}
	return resp.Content, skills.SkillReadResult{
		Name:        resp.Result.Name,
		Description: resp.Result.Description,
		Source:      skills.SourceType(resp.Result.Source),
		Builtin:     resp.Result.Builtin,
	}, nil
}

// -- MCP operations --

// mcpStatesTimeout bounds single MCP state probe. 客户端 SDK sets no
// 请求 超时 的 它的 own, 和 UI serializes refreshes behind
// 在-flight flag: without 期限 hung 请求 would wedge 那 flag
// forever, queueing every later refresh behind it 和 silently freezing
// MCP sidebar. 一个 var, 不 const, 所以 tests can shrink it.
var mcpStatesTimeout = 10 * time.Second

func (w *ClientWorkspace) MCPGetStates() map[string]mcp.ClientInfo {
	ctx, cancel := context.WithTimeout(context.Background(), mcpStatesTimeout)
	defer cancel()
	states, err := w.client.MCPGetStates(ctx, w.workspaceID())
	if err != nil {
		slog.Warn("Failed to fetch MCP states", "error", err)
		return nil
	}
	result := make(map[string]mcp.ClientInfo, len(states))
	for k, v := range states {
		result[k] = mcp.ClientInfo{
			Name:  v.Name,
			State: mcp.State(v.State),
			Error: v.Error,
			Counts: mcp.Counts{
				Tools:     v.ToolCount,
				Prompts:   v.PromptCount,
				Resources: v.ResourceCount,
			},
			ConnectedAt:    v.ConnectedAt,
			Channel:        v.Channel,
			ChannelOptIn:   v.ChannelOptIn,
			ChannelCapable: v.ChannelCapable,
		}
	}
	return result
}

func (w *ClientWorkspace) MCPRefreshPrompts(ctx context.Context, name string) {
	_ = w.client.MCPRefreshPrompts(ctx, w.workspaceID(), name)
}

func (w *ClientWorkspace) MCPRefreshResources(ctx context.Context, name string) {
	_ = w.client.MCPRefreshResources(ctx, w.workspaceID(), name)
}

func (w *ClientWorkspace) RefreshMCPTools(ctx context.Context, name string) {
	_ = w.client.RefreshMCPTools(ctx, w.workspaceID(), name)
}

func (w *ClientWorkspace) ReadMCPResource(ctx context.Context, name, uri string) ([]MCPResourceContents, error) {
	contents, err := w.client.ReadMCPResource(ctx, w.workspaceID(), name, uri)
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

func (w *ClientWorkspace) ListMCPPrompts(ctx context.Context) ([]commands.MCPPrompt, error) {
	prompts, err := w.client.ListMCPPrompts(ctx, w.workspaceID())
	if err != nil {
		return nil, err
	}
	result := make([]commands.MCPPrompt, len(prompts))
	for i, prompt := range prompts {
		arguments := make([]commands.Argument, len(prompt.Arguments))
		for j, argument := range prompt.Arguments {
			arguments[j] = commands.Argument{
				ID:          argument.ID,
				Title:       argument.Title,
				Description: argument.Description,
				Required:    argument.Required,
			}
		}
		result[i] = commands.MCPPrompt{
			ID:          prompt.ID,
			Title:       prompt.Title,
			Description: prompt.Description,
			PromptID:    prompt.PromptID,
			ClientID:    prompt.ClientID,
			Arguments:   arguments,
		}
	}
	return result, nil
}

func (w *ClientWorkspace) GetMCPPrompt(clientID, promptID string, args map[string]string) (string, error) {
	return w.client.GetMCPPrompt(context.Background(), w.workspaceID(), clientID, promptID, args)
}

func (w *ClientWorkspace) EnableDockerMCP(ctx context.Context) error {
	return w.client.EnableDockerMCP(ctx, w.workspaceID())
}

func (w *ClientWorkspace) DisableDockerMCP() error {
	return w.client.DisableDockerMCP(context.Background(), w.workspaceID())
}

func (w *ClientWorkspace) MCPServersDisabled(ctx context.Context) ([]string, error) {
	return w.client.MCPServersDisabled(ctx, w.workspaceID())
}

func (w *ClientWorkspace) MCPServersEnabled(ctx context.Context) ([]string, error) {
	return w.client.MCPServersEnabled(ctx, w.workspaceID())
}

func (w *ClientWorkspace) MCPSetServerDisabled(ctx context.Context, name string, disabled bool) error {
	return w.client.SetMCPServerDisabled(ctx, w.workspaceID(), name, disabled)
}

func (w *ClientWorkspace) MCPSetServerConfigDisabled(ctx context.Context, name string, disabled bool) error {
	err := w.client.SetMCPServerConfigDisabled(ctx, w.workspaceID(), name, disabled)
	if err == nil {
		w.refreshWorkspace()
	}
	return err
}

func (w *ClientWorkspace) SkillsDisabled(ctx context.Context) ([]string, error) {
	return w.client.SkillsDisabled(ctx, w.workspaceID())
}

func (w *ClientWorkspace) SkillSetDisabled(ctx context.Context, name string, disabled bool) error {
	return w.client.SetSkillDisabled(ctx, w.workspaceID(), name, disabled)
}

func (w *ClientWorkspace) SkillsEnabled(ctx context.Context) ([]string, error) {
	return w.client.SkillsEnabled(ctx, w.workspaceID())
}

func (w *ClientWorkspace) SkillSetConfigDisabled(ctx context.Context, name string, disabled bool) error {
	err := w.client.SetSkillConfigDisabled(ctx, w.workspaceID(), name, disabled)
	if err == nil {
		w.refreshWorkspace()
	}
	return err
}

func (w *ClientWorkspace) MCPStartServer(ctx context.Context, name string) error {
	return w.client.StartMCPServer(ctx, w.workspaceID(), name)
}

func (w *ClientWorkspace) MCPAuthenticate(ctx context.Context, name string) error {
	// 服务端 suppresses 它的 own browser open 为 这 flow; 客户端
	// polls auth URL 和 opens it locally 所以 用户 authorizes 在
	// their own machine. OAuth callback listener runs 在 服务端
	// (localhost ports shared 当 服务端 和 客户端 是 co-located).
	authErr := make(chan error, 1)
	go func() {
		authErr <- w.client.MCPAuthenticate(ctx, w.workspaceID(), name)
	}()

	// Poll 为 authorization URL 所以 we can open it 在 local
	// browser 作为 soon 作为 flow generates one.
	var opened bool
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-authErr:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if opened {
				continue
			}
			if u := w.MCPAuthURL(name); u != "" {
				if err := browser.OpenURL(u); err != nil {
					slog.Warn("Failed to open MCP OAuth URL in browser", "error", err)
				}
				opened = true
			}
		}
	}
}

func (w *ClientWorkspace) MCPPendingAuth() []mcp.PendingAuthServer {
	pending, err := w.client.MCPPendingAuth(context.Background(), w.workspaceID())
	if err != nil {
		slog.Warn("Failed to fetch MCP pending auth", "error", err)
		return nil
	}
	result := make([]mcp.PendingAuthServer, len(pending))
	for i, p := range pending {
		result[i] = mcp.PendingAuthServer{Name: p.Name, URL: p.URL}
	}
	return result
}

func (w *ClientWorkspace) MCPAuthURL(name string) string {
	// 服务端's 在-progress authorization URL 是 exposed through
	// pending-auth list while flow runs; 服务端 在 StateNeedsAuth
	// paired 用 活跃 flow reports 它的 URL here. Poll 服务端
	// 为 在-flight URL.
	u, err := w.client.MCPAuthURL(context.Background(), w.workspaceID(), name)
	if err != nil {
		return ""
	}
	return u
}

// -- Lifecycle --

func (w *ClientWorkspace) Subscribe(program *tea.Program) {
	defer log.RecoverPanic("ClientWorkspace.Subscribe", func() {
		slog.Info("TUI subscription panic: attempting graceful shutdown")
		program.Quit()
	})

	w.runSubscription(program.Send)
}

// maxRecoveryEscalate 是 number 的 consecutive failed recovery
// attempts (re-registering 工作区 或 restarting 服务端) after
// which loop tells UI 连接 looks unrecoverable. 它 keeps
// retrying regardless: hard stop would strand 用户 whose 服务端 comes
// back minute later, 和 Shutdown can always 取消 it.
const maxRecoveryEscalate = 20

// recoveryCreateTimeout bounds single re-registration attempt. 它 是
// generous because 工作区 startup 是 slow (配置, 数据库, LSP, MCP);
// it exists 只 所以 unresponsive 服务端 cannot pin subscription
// goroutine indefinitely, 和 loop simply retries 当 it trips.
// 一个 var, 不 const, 所以 tests can shrink it.
var recoveryCreateTimeout = 30 * time.Second

// runSubscription subscribes 到 工作区 事件 流 和 forwards
// translated events 到 send, reconnecting 用 capped exponential
// backoff whenever 流 drops. 它 returns 只 当
// subscription 上下文 是 cancelled (via Shutdown). Split out 从
// Subscribe 所以 it can 是 tested without real *tea.Program.
//
// Some failures need more than 重试. 一个 404 means 服务端 no longer
// knows 这 工作区, 所以 resubscribing 用 same ID can never
// succeed 和 loop re-registers instead. When nothing listens 在
// 服务端's address 在 all, 服务端 died 和 no 重试 can reach it, 所以
// loop has respawn 钩子 start new one, which then answers 用
// 那 404. Every 流 那 attaches, first one included, reports
// 会话 客户端 views. And any 流 那 closes loses whatever
// was published while 客户端 was away, 所以 every re-established 流
// — even one 那 reconnects 在 first try — asks UI 到 resync.
func (w *ClientWorkspace) runSubscription(send func(tea.Msg)) {
	w.subStarted.Store(true)
	defer close(w.subDone)

	backoff := sseReconnectInitialBackoff
	degraded := false
	recoveryFailures := 0
	// versionWarning 是 服务端 version warning last shown, 所以
	// 服务端 是 不 reported again 在 every reconnect.
	versionWarning := ""
	markDegraded := func(err error, stuck bool) {
		if degraded && !stuck {
			return
		}
		degraded = true
		send(ConnectionEvent{State: ConnectionDegraded, Err: err, Stuck: stuck})
	}
	recoveryFailed := func(err error) {
		recoveryFailures++
		if recoveryFailures == maxRecoveryEscalate {
			markDegraded(err, true)
		}
	}

	for {
		if w.subCtx.Err() != nil {
			return
		}

		evc, err := w.client.SubscribeEvents(w.subCtx, w.workspaceID())
		if err != nil {
			if w.subCtx.Err() != nil {
				return
			}
			markDegraded(err, false)
			if !errors.Is(err, client.ErrNotFound) {
				slog.Error("Failed to subscribe to workspace events; retrying",
					"error", err, "retry_in", backoff)
				if w.respawnServer != nil && server.IsStaleSocketErr(err) {
					// 服务端 died: bring up one 重试 will reach.
					if err := w.respawnServer(w.subCtx); err != nil && w.subCtx.Err() == nil {
						slog.Error("Failed to restart the evo server", "error", err)
						recoveryFailed(fmt.Errorf("%w: %v", ErrServerUnreachable, err))
					}
				}
			} else if w.recoverWorkspace() == nil {
				// Re-registered: resubscribe immediately under the fresh
				// 工作区 ID.
				backoff = sseReconnectInitialBackoff
				continue
			} else if w.subCtx.Err() == nil {
				recoveryFailed(ErrWorkspaceGone)
			}
			if !w.sleepOrDone(backoff) {
				return
			}
			backoff = min(backoff*2, sseReconnectMaxBackoff)
			continue
		}

		w.reportCurrentSession()
		if degraded {
			degraded = false
			recoveryFailures = 0
			w.afterReconnect(send)
		}
		// Recovery may have re-created 工作区 在 another 服务端,
		// 所以 check version 在 every attach. 它 goes after
		// reconnect notice 所以 那 notice does 不 replace it.
		if msg := ServerVersionWarning(w.cached().Version); msg != versionWarning {
			versionWarning = msg
			if msg != "" {
				send(util.InfoMsg{Type: util.InfoTypeWarn, Msg: msg, TTL: 10 * time.Second})
			}
		}
		backoff = sseReconnectInitialBackoff
		w.consumeEvents(evc, send)

		// 事件 渠道 closed: 服务端 restarted, 流 was
		// interrupted, 或 工作区 briefly went away. Reconnect
		// after short delay instead 的 leaving TUI permanently
		// orphaned, which 是 what surfaced 作为 stuck "coder agent 是
		// 离线".
		if w.subCtx.Err() != nil {
			return
		}
		markDegraded(ErrStreamClosed, false)
		slog.Warn("Workspace event stream closed; reconnecting", "retry_in", backoff)
		if !w.sleepOrDone(backoff) {
			return
		}
		backoff = min(backoff*2, sseReconnectMaxBackoff)
	}
}

// recoverWorkspace re-registers 工作区 after 服务端 reported it
// gone: it re-creates it 从 cached snapshot ( 服务端's own view 的
// 路径, data dir, flags 和 env), adopts new ID, 和 re-initializes
// coder agent 当 配置 是 ready, mirroring startup handshake,
// before switching it back 到 main agent 这 客户端 chose.
// 服务端's 路径 dedupe means 这 either rejoins live sibling 工作区 或
// mints fresh one. 它 must 只 是 called 从 subscription goroutine,
// 只 writer 的 cached ID.
//
// 创建 deliberately runs detached 从 subscription 上下文:
// 服务端 does 不 abandon 创建 当 requesting 连接 goes away,
// 所以 cancelling would hide outcome while 工作区 got registered
// anyway. Riding it out means ID 是 known 通过 time Shutdown looks,
// 和 retirement covers lost 响应 regardless. Its own 超时 keeps
// wedged 服务端 从 pinning subscription goroutine forever; 客户端
// SDK sets no 请求 超时 的 它的 own.
func (w *ClientWorkspace) recoverWorkspace() error {
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(w.subCtx), recoveryCreateTimeout,
	)
	defer cancel()
	created, err := w.client.CreateWorkspace(ctx, w.recreateArgs())
	if err != nil {
		slog.Error("Failed to re-register workspace; retrying", "error", err)
		return err
	}
	if created.Config != nil {
		created.Config.SetupAgents()
		created.Config.NormalizeOptions()
	}
	w.mu.Lock()
	oldID := w.ws.ID
	w.ws = *created
	w.mu.Unlock()
	slog.Info("Re-registered workspace after server-side loss",
		"old_id", oldID, "new_id", created.ID)

	if created.Config != nil && created.Config.IsConfigured() {
		if err := w.InitCoderAgent(w.subCtx); err != nil {
			// Matches startup handshake: agent init failure 是
			// logged, 不 fatal, since 用户 can still pick 模型.
			slog.Error("Failed to initialize coder agent after workspace recovery", "error", err)
		} else {
			w.restoreMainAgent()
		}
	}
	return nil
}

// restoreMainAgent switches freshly initialized agent back 到 main
// agent 这 客户端 chose. new coordinator runs coder agent, 所以
// 客户端 在 plan mode would otherwise get 写入 tools while 它的 UI still
// says plan mode; 客户端 在 coder agent needs nothing.
func (w *ClientWorkspace) restoreMainAgent() {
	w.mu.RLock()
	agentID := w.mainAgent
	w.mu.RUnlock()
	if agentID == "" || agentID == config.AgentCoder {
		return
	}
	if err := w.client.SetMainAgent(w.subCtx, w.workspaceID(), agentID); err != nil {
		slog.Error("Failed to restore main agent after workspace recovery", "agent", agentID, "error", err)
	}
}

// recreateArgs derives CreateWorkspace 请求 used 为 recovery 从
// cached snapshot. ID 是 dropped 所以 服务端 can dedupe 通过
// 路径 或 mint fresh 工作区, 和 Version carries 这 客户端's
// version, matching the startup handshake.
func (w *ClientWorkspace) recreateArgs() proto.Workspace {
	ws := w.cached()
	return proto.Workspace{
		Path:           ws.Path,
		DataDir:        ws.DataDir,
		Debug:          ws.Debug,
		PermissionMode: ws.PermissionMode,
		Channels:       ws.Channels,
		Prompts:        ws.Prompts,
		Env:            os.Environ(),
		Version:        version.Version,
	}
}

// ServerVersionWarning returns warning 到 show 用户 当
// 服务端, which reports 它的 version 在 every 工作区 it returns, runs
// different Crush version than 这 客户端. 它 returns "" 当
// versions match 或 服务端 是 too old 到 报告 它的 version.
func ServerVersionWarning(serverVersion string) string {
	if serverVersion == "" || serverVersion == version.Version {
		return ""
	}
	return fmt.Sprintf(
		"Server version %q differs from client version %q. Consider restarting the server.",
		serverVersion, version.Version,
	)
}

// afterReconnect runs once degraded subscription 是 re-established. 它
// re-reads host theme 和 tells UI 到 resync state published while
// detached.
func (w *ClientWorkspace) afterReconnect(send func(tea.Msg)) {
	w.resyncHostTheme(send)
	send(ConnectionEvent{State: ConnectionRecovered})
}

// hostThemeResyncTimeout bounds host theme 读取 after reconnect, 所以
// wedged 服务端 cannot pin subscription goroutine.
const hostThemeResyncTimeout = 5 * time.Second

// resyncHostTheme hands UI host theme 服务端 holds after
// reconnect. attach 重放 只 covers theme 那 是 set, 所以 clear
// published while 客户端 was away, 或 工作区 re-created without
// one, would otherwise leave TUI 在 host's colors. 读取 runs
// after new 流 attached, 所以 any later change still arrives 作为
// 事件, 和 UI ignores theme equal 到 last one it received.
func (w *ClientWorkspace) resyncHostTheme(send func(tea.Msg)) {
	ctx, cancel := context.WithTimeout(w.subCtx, hostThemeResyncTimeout)
	defer cancel()
	st, err := w.client.GetHostTheme(ctx, w.workspaceID())
	if err != nil {
		// Servers 那 predate host themes have no such endpoint.
		slog.Debug("Failed to read host theme after reconnect", "error", err)
		return
	}
	send(HostThemeEvent{Theme: st.Theme})
}

// sleepOrDone waits 为 d 或 until subscription 上下文 是
// cancelled. 它 reports false 当 上下文 was cancelled, signalling
// caller 到 stop reconnecting.
func (w *ClientWorkspace) sleepOrDone(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-w.subCtx.Done():
		return false
	}
}

// consumeEvents drives 工作区 事件 loop. 它 是 split out 从
// Subscribe 所以 tests can drive it without real *tea.Program.
// ConfigChanged events trigger 工作区 refresh; all other events
// 是 translated into domain types 和 forwarded 到 send.
func (w *ClientWorkspace) consumeEvents(evc <-chan any, send func(tea.Msg)) {
	for ev := range evc {
		// Forward events 到 herdr 如果 running inside herdr pane.
		if hev := herdr.Translate(ev); hev != nil {
			w.herdrClient.HandleEvent(hev)
		}

		if _, ok := ev.(pubsub.Event[proto.ConfigChanged]); ok {
			w.refreshWorkspace()
			continue
		}
		translated := w.translateEvent(ev)
		if translated != nil && send != nil {
			send(translated)
		}
	}
}

// shutdownDrainTimeout bounds how long Shutdown waits 为 subscription
// loop 到 stop. Exceeding it 是 不 correctness problem — retiring
// 客户端 releases whatever late recovery registers — it 只 makes
// goodbye less tidy.
const shutdownDrainTimeout = 5 * time.Second

// shutdownRetireTimeout bounds Shutdown's goodbye 到 服务端, legacy
// fallback included. 客户端 SDK sets no 请求 超时 的 它的 own, 和
// 通过 time Shutdown runs TUI 是 gone 和 root signal 处理器
// swallows Ctrl+C, 所以 服务端 那 accepts 请求 和 never answers
// (stopped 在 debugger, suspended 用 Ctrl+Z) would hang exit
// forever. Giving up 是 safe: 服务端 releases 客户端 那 left without
// goodbye once 它的 detach 或 创建 grace runs out. 一个 var, 不 const, 所以
// tests 可以 shrink it.
var shutdownRetireTimeout = 5 * time.Second

func (w *ClientWorkspace) Shutdown() {
	// Stop reconnect/recovery loop first, then wait 为 it: cancelling
	// alone does 不 unwind 工作区 recovery 那 是 already 在
	// flight, 和 we want 到 release 工作区 那 recovery ended up
	// 用 rather than one it 是 about 到 replace.
	if w.subCancel != nil {
		w.subCancel()
	}
	w.awaitSubscription()
	w.herdrClient.Close()

	// Retiring 客户端 releases every claim it holds, 在 every 工作区,
	// 和 blocks any further 创建 从 这 客户端 ID. That 是 what makes
	// teardown exact even 当 recovery 创建's 响应 was lost:
	// 创建 either landed before 这 调用, 和 它的 claim 是 released here,
	// 或 it arrives afterwards 和 registers nothing.
	ctx, cancel := context.WithTimeout(context.Background(), shutdownRetireTimeout)
	defer cancel()
	err := w.client.RetireClient(ctx)
	if err == nil {
		return
	}
	if !errors.Is(err, client.ErrUnsupported) {
		slog.Warn("Failed to retire client on the server", "error", err)
		return
	}
	// 服务端 predates 客户端 retirement, 所以 fall back 到 releasing
	// 工作区 we know about. Nothing better 是 possible against
	// older 服务端.
	_ = w.client.DeleteWorkspace(ctx, w.workspaceID())
}

// awaitSubscription waits 为 subscription loop 到 return. 它 returns
// immediately 当 loop never started, which 是 case 为
// workspaces shut down before Subscribe runs.
func (w *ClientWorkspace) awaitSubscription() {
	if !w.subStarted.Load() || w.subDone == nil {
		return
	}
	t := time.NewTimer(shutdownDrainTimeout)
	defer t.Stop()
	select {
	case <-w.subDone:
	case <-t.C:
		slog.Warn("Timed out waiting for the workspace subscription to stop")
	}
}

// translateEvent converts proto-typed SSE events into the domain types
// 那 TUI's 更新() method expects. Skills events also 更新
// process-local skills.Manager 所以 callers reading
// skills.GetLatestStates see fresh data.
func (w *ClientWorkspace) translateEvent(ev any) tea.Msg {
	switch e := ev.(type) {
	case pubsub.Event[proto.ToolProgress]:
		return pubsub.Event[tools.ToolProgress]{
			Type:    e.Type,
			Payload: tools.ToolProgress(e.Payload),
		}
	case pubsub.Event[proto.LSPEvent]:
		return pubsub.Event[LSPEvent]{
			Type: e.Type,
			Payload: LSPEvent{
				Type:            LSPEventType(e.Payload.Type),
				Name:            e.Payload.Name,
				State:           e.Payload.State,
				Error:           e.Payload.Error,
				DiagnosticCount: e.Payload.DiagnosticCount,
			},
		}
	case pubsub.Event[proto.MCPEvent]:
		return pubsub.Event[mcp.Event]{
			Type: e.Type,
			Payload: mcp.Event{
				Type:  protoToMCPEventType(e.Payload.Type),
				Name:  e.Payload.Name,
				State: mcp.State(e.Payload.State),
				Error: e.Payload.Error,
				Counts: mcp.Counts{
					Tools:     e.Payload.ToolCount,
					Prompts:   e.Payload.PromptCount,
					Resources: e.Payload.ResourceCount,
				},
				ChannelMessage: e.Payload.ChannelMessage,
			},
		}
	case pubsub.Event[proto.PermissionRequest]:
		return pubsub.Event[permission.PermissionRequest]{
			Type: e.Type,
			Payload: permission.PermissionRequest{
				ID:          e.Payload.ID,
				SessionID:   e.Payload.SessionID,
				ToolCallID:  e.Payload.ToolCallID,
				ToolName:    e.Payload.ToolName,
				Description: e.Payload.Description,
				Action:      e.Payload.Action,
				Path:        e.Payload.Path,
				Params:      e.Payload.Params,
				Danger:      e.Payload.Danger,
			},
		}
	case pubsub.Event[proto.PermissionNotification]:
		return pubsub.Event[permission.PermissionNotification]{
			Type: e.Type,
			Payload: permission.PermissionNotification{
				SessionID:  e.Payload.SessionID,
				ToolCallID: e.Payload.ToolCallID,
				Granted:    e.Payload.Granted,
				Denied:     e.Payload.Denied,
				Cancelled:  e.Payload.Cancelled,
			},
		}
	case pubsub.Event[proto.QuestionRequest]:
		return pubsub.Event[question.Request]{
			Type: e.Type,
			Payload: question.Request{
				ID:                 e.Payload.ID,
				SessionID:          e.Payload.SessionID,
				ToolCallID:         e.Payload.ToolCallID,
				Questions:          protoQuestionsToDomain(e.Payload.Questions),
				ConfirmTitle:       e.Payload.ConfirmTitle,
				ConfirmDescription: e.Payload.ConfirmDescription,
			},
		}
	case pubsub.Event[proto.QuestionNotification]:
		return pubsub.Event[question.Notification]{
			Type: e.Type,
			Payload: question.Notification{
				BatchID: e.Payload.BatchID,
			},
		}
	case pubsub.Event[proto.PinentryPromptRequest]:
		return pubsub.Event[pinentry.PromptRequest]{
			Type: e.Type,
			Payload: pinentry.PromptRequest{
				ID:     e.Payload.ID,
				Prompt: e.Payload.Prompt,
				Key: pinentry.Key{
					CardNumber:  e.Payload.Key.CardNumber,
					KeyID:       e.Payload.Key.KeyID,
					Fingerprint: e.Payload.Key.Fingerprint,
					Holder:      e.Payload.Key.Holder,
				},
				Kind:       pinentry.Kind(e.Payload.Kind),
				RetryCount: e.Payload.RetryCount,
				Error:      e.Payload.Error,
			},
		}
	case pubsub.Event[proto.PinentryNotification]:
		return pubsub.Event[pinentry.Notification]{
			Type: e.Type,
			Payload: pinentry.Notification{
				RequestID: e.Payload.RequestID,
			},
		}
	case pubsub.Event[proto.SSHPromptRequest]:
		return pubsub.Event[sshaskpass.PromptRequest]{
			Type: e.Type,
			Payload: sshaskpass.PromptRequest{
				ID:      e.Payload.ID,
				Prompt:  e.Payload.Prompt,
				KeyInfo: e.Payload.KeyInfo,
				Kind:    sshaskpass.Kind(e.Payload.Kind),
				Title:   e.Payload.Title,
				Message: e.Payload.Message,
			},
		}
	case pubsub.Event[proto.SSHNotification]:
		return pubsub.Event[sshaskpass.Notification]{
			Type: e.Type,
			Payload: sshaskpass.Notification{
				RequestID: e.Payload.RequestID,
			},
		}
	case pubsub.Event[proto.Message]:
		return pubsub.Event[message.Message]{
			Type:    e.Type,
			Payload: protoToMessage(e.Payload),
		}
	case pubsub.Event[proto.Session]:
		return pubsub.Event[session.Session]{
			Type:    e.Type,
			Payload: protoToSession(e.Payload),
		}
	case pubsub.Event[proto.File]:
		return pubsub.Event[history.File]{
			Type:    e.Type,
			Payload: protoToFile(e.Payload),
		}
	case pubsub.Event[proto.AgentEvent]:
		n := notify.Notification{
			SessionID:    e.Payload.SessionID,
			SessionTitle: e.Payload.SessionTitle,
			RunID:        e.Payload.RunID,
			Type:         notify.Type(e.Payload.Type),
			ProviderID:   e.Payload.ProviderID,
			AWSSOCommand: e.Payload.AWSSOCommand,
			AWSSOURL:     e.Payload.AWSSOURL,
		}
		if e.Payload.Error != nil {
			n.Message = e.Payload.Error.Error()
		}
		return pubsub.Event[notify.Notification]{
			Type:    e.Type,
			Payload: n,
		}
	case pubsub.Event[proto.RunComplete]:
		// Translate the wire-level proto.RunComplete back into the
		// agent's domain notify.RunComplete. TUI ends 它的 pending
		// turn 和 clears 它的 busy probe 在 it.
		return pubsub.Event[notify.RunComplete]{
			Type: e.Type,
			Payload: notify.RunComplete{
				SessionID: e.Payload.SessionID,
				RunID:     e.Payload.RunID,
				MessageID: e.Payload.MessageID,
				Text:      e.Payload.Text,
				Error:     e.Payload.Error,
				Cancelled: e.Payload.Cancelled,
			},
		}
	case pubsub.Event[proto.SkillsEvent]:
		states := protoToSkillStates(e.Payload.States)
		if w.skills != nil {
			w.skills.SetLatestStates(states)
		}
		return pubsub.Event[skills.Event]{
			Type:    e.Type,
			Payload: skills.Event{States: states},
		}
	case pubsub.Event[proto.UpdateAvailable]:
		return app.UpdateAvailableMsg{
			CurrentVersion: e.Payload.CurrentVersion,
			LatestVersion:  e.Payload.LatestVersion,
			IsDevelopment:  e.Payload.IsDevelopment,
		}
	case pubsub.Event[proto.HostThemeState]:
		theme := e.Payload.Theme
		if e.Type == pubsub.DeletedEvent {
			theme = nil
		}
		return HostThemeEvent{Theme: theme}
	case pubsub.Event[proto.PermissionModeEvent]:
		// Keep cached 工作区 在 sync 所以 PermissionMode() never goes
		// stale 当 another 客户端 或 服务端 changes mode.
		w.mu.Lock()
		w.ws.PermissionMode = e.Payload.Mode
		w.mu.Unlock()
		return nil
	case pubsub.Event[proto.ClientSession]:
		// Which 会话 each 客户端 views 是 为 host applications;
		// TUI does 不 show it.
		return nil
	default:
		slog.Warn("Unknown event type in translateEvent", "type", fmt.Sprintf("%T", ev))
		return nil
	}
}

func protoToMCPEventType(t proto.MCPEventType) mcp.EventType {
	switch t {
	case proto.MCPEventStateChanged:
		return mcp.EventStateChanged
	case proto.MCPEventToolsListChanged:
		return mcp.EventToolsListChanged
	case proto.MCPEventPromptsListChanged:
		return mcp.EventPromptsListChanged
	case proto.MCPEventResourcesListChanged:
		return mcp.EventResourcesListChanged
	case proto.MCPEventChannelMessage:
		return mcp.EventChannelMessage
	default:
		return mcp.EventStateChanged
	}
}

// protoToSession converts wire-level proto.会话 into domain
// 会话.会话. Fields 那 exist 只 在 wire (computed-在-读取
// signals like IsBusy, 和 any future presence counters) 是
// intentionally dropped here: 会话.会话 models persisted state,
// 不 transient runtime signals. UI features 那 need those signals
// should either extend 会话.会话 或 读取 them 从 proto
// payload directly before 这 conversion runs.
func protoToSession(s proto.Session) session.Session {
	return session.Session{
		ID:               s.ID,
		ParentSessionID:  s.ParentSessionID,
		Title:            s.Title,
		SummaryMessageID: s.SummaryMessageID,
		MessageCount:     s.MessageCount,
		PromptTokens:     s.PromptTokens,
		CompletionTokens: s.CompletionTokens,
		EstimatedUsage:   s.EstimatedUsage,
		Cost:             s.Cost,
		Todos:            protoToTodos(s.Todos),
		Channel:          s.Channel,
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        s.UpdatedAt,
	}
}

func protoToTodos(todos []proto.Todo) []session.Todo {
	if len(todos) == 0 {
		return nil
	}
	out := make([]session.Todo, len(todos))
	for i, t := range todos {
		out[i] = session.Todo{
			Content:    t.Content,
			Status:     session.TodoStatus(t.Status),
			ActiveForm: t.ActiveForm,
		}
	}
	return out
}

func protoToFile(f proto.File) history.File {
	return history.File{
		ID:        f.ID,
		SessionID: f.SessionID,
		Path:      f.Path,
		Content:   f.Content,
		Version:   f.Version,
		CreatedAt: f.CreatedAt,
		UpdatedAt: f.UpdatedAt,
	}
}

func protoToMessage(m proto.Message) message.Message {
	msg := message.Message{
		ID:                      m.ID,
		SessionID:               m.SessionID,
		Role:                    message.MessageRole(m.Role),
		Model:                   m.Model,
		ModelName:               m.ModelName,
		Provider:                m.Provider,
		PrismModelID:            m.PrismModelID,
		PrismModelName:          m.PrismModelName,
		PrismHypercreditSavings: m.PrismHypercreditSavings,
		PrismDollarSavings:      m.PrismDollarSavings,
		CreatedAt:               m.CreatedAt,
		UpdatedAt:               m.UpdatedAt,
		IsSummaryMessage:        m.IsSummaryMessage,
	}

	for _, p := range m.Parts {
		switch v := p.(type) {
		case proto.TextContent:
			msg.Parts = append(msg.Parts, message.TextContent{Text: v.Text, Hidden: v.Hidden})
		case proto.ReasoningContent:
			msg.Parts = append(msg.Parts, message.ReasoningContent{
				Thinking:   v.Thinking,
				Signature:  v.Signature,
				StartedAt:  v.StartedAt,
				FinishedAt: v.FinishedAt,
			})
		case proto.ToolCall:
			msg.Parts = append(msg.Parts, message.ToolCall{
				ID:          v.ID,
				Name:        v.Name,
				Input:       v.Input,
				Finished:    v.Finished,
				StartedAtMs: v.StartedAtMs,
			})
		case proto.ToolResult:
			msg.Parts = append(msg.Parts, message.ToolResult{
				ToolCallID:   v.ToolCallID,
				Name:         v.Name,
				Content:      v.Content,
				Data:         v.Data,
				MIMEType:     v.MIMEType,
				Metadata:     v.Metadata,
				IsError:      v.IsError,
				FinishedAtMs: v.FinishedAtMs,
			})
		case proto.Finish:
			msg.Parts = append(msg.Parts, message.Finish{
				Reason:  message.FinishReason(v.Reason),
				Time:    v.Time,
				Message: v.Message,
				Details: v.Details,
			})
		case proto.ImageURLContent:
			msg.Parts = append(msg.Parts, message.ImageURLContent{URL: v.URL, Detail: v.Detail})
		case proto.BinaryContent:
			msg.Parts = append(msg.Parts, message.BinaryContent{Path: v.Path, MIMEType: v.MIMEType, Data: v.Data})
		case proto.ShellCommand:
			msg.Parts = append(msg.Parts, message.ShellCommand{
				Command:  v.Command,
				Output:   v.Output,
				ExitCode: v.ExitCode,
			})
		case proto.SubAgentReport:
			msg.Parts = append(msg.Parts, message.SubAgentReport{
				Label:  v.Label,
				Output: v.Output,
				Failed: v.Failed,
				Model:  v.Model,
			})
		}
	}

	return msg
}

func protoToMessages(msgs []proto.Message) []message.Message {
	out := make([]message.Message, len(msgs))
	for i, m := range msgs {
		out[i] = protoToMessage(m)
	}
	return out
}

func protoToFiles(files []proto.File) []history.File {
	out := make([]history.File, len(files))
	for i, f := range files {
		out[i] = protoToFile(f)
	}
	return out
}

func sessionToProto(s session.Session) proto.Session {
	return proto.Session{
		ID:               s.ID,
		ParentSessionID:  s.ParentSessionID,
		Title:            s.Title,
		SummaryMessageID: s.SummaryMessageID,
		MessageCount:     s.MessageCount,
		PromptTokens:     s.PromptTokens,
		CompletionTokens: s.CompletionTokens,
		EstimatedUsage:   s.EstimatedUsage,
		Cost:             s.Cost,
		Todos:            todosToProto(s.Todos),
		Channel:          s.Channel,
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        s.UpdatedAt,
	}
}

// protoToSkillStates reconstructs internal skill state slices 从
// their wire representation. Non-empty 错误 strings 是 turned into
// synthetic 错误 values; TUI never type-asserts 在 Err.
func protoToSkillStates(in []proto.SkillState) []*skills.SkillState {
	if len(in) == 0 {
		return nil
	}
	out := make([]*skills.SkillState, len(in))
	for i, s := range in {
		state := &skills.SkillState{
			Name:  s.Name,
			Path:  s.Path,
			State: skills.DiscoveryState(s.State),
		}
		if s.Error != "" {
			state.Err = errors.New(s.Error)
		}
		out[i] = state
	}
	return out
}

func todosToProto(todos []session.Todo) []proto.Todo {
	if len(todos) == 0 {
		return nil
	}
	out := make([]proto.Todo, len(todos))
	for i, t := range todos {
		out[i] = proto.Todo{
			Content:    t.Content,
			Status:     string(t.Status),
			ActiveForm: t.ActiveForm,
		}
	}
	return out
}

func protoQuestionsToDomain(qs []proto.QuestionItem) []question.Question {
	if len(qs) == 0 {
		return nil
	}
	out := make([]question.Question, len(qs))
	for i, q := range qs {
		choices := make([]question.Choice, len(q.Choices))
		for j, c := range q.Choices {
			choices[j] = question.Choice{
				ID:          c.ID,
				Label:       c.Label,
				Description: c.Description,
			}
		}
		out[i] = question.Question{
			ID:          q.ID,
			Type:        question.Type(q.Type),
			Label:       q.Label,
			Text:        q.Question,
			Description: q.Description,
			Choices:     choices,
		}
	}
	return out
}
