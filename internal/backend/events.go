package backend

import (
	"context"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"

	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
)

// SubscribeClientSessions returns per-caller 渠道 的 会话
// changes 的 clients 的 工作区.
func (b *Backend) SubscribeClientSessions(ctx context.Context, workspaceID string) (<-chan pubsub.Event[proto.ClientSession], error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	return ws.clientSessionBroker().Subscribe(ctx), nil
}

// SubscribeEvents returns per-caller 事件 渠道 为 工作区.
// Each caller receives all events; multiple callers do 不 compete.
func (b *Backend) SubscribeEvents(ctx context.Context, workspaceID string) (<-chan pubsub.Event[tea.Msg], error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Events(ctx), nil
}

// SubscribeEventsWithOverflow 是 like [Backend.SubscribeEvents], 但 also
// returns 渠道 那 是 closed first time 事件 是 dropped 为
// 这 caller because it fell behind. 客户端 事件 streams use it 到 end
// 流 rather than leave gap.
func (b *Backend) SubscribeEventsWithOverflow(ctx context.Context, workspaceID string) (<-chan pubsub.Event[tea.Msg], <-chan struct{}, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, nil, err
	}

	events, overflow := ws.EventsWithOverflow(ctx)
	return events, overflow, nil
}

// maxSessionAncestryDepth bounds how far [Backend.RootSessionID] walks
// parent chain 当 resolving 会话 到 它的 top-level 会话.
// Subagents cannot spawn subagents today, 所以 one hop suffices 在
// practice; extra slack guards against cycles 和 future nesting.
const maxSessionAncestryDepth = 4

// RootSessionID resolves sessionID 到 它的 top-level 会话 通过 walking
// parent chain. 子代理 sessions (created 通过 agent 工具 或
// agentic fetch) carry derived IDs; their interactive prompts 是
// scoped 到 top-level 会话 用户 can actually view. 一个 会话
// 用 no parent resolves 到 itself. sessionID 是 returned
// unchanged alongside any 错误, 所以 callers can fall back 到
// unresolved ID. Successful resolutions 是 memoized 在 工作区:
// parentage 是 fixed 在 creation, 所以 cached entries never go stale.
func (b *Backend) RootSessionID(ctx context.Context, workspaceID, sessionID string) (string, error) {
	if sessionID == "" {
		return sessionID, nil
	}
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return sessionID, err
	}
	return rootSessionID(ctx, ws, sessionID)
}

// rootSessionID 是 工作区-resolved variant 的
// [Backend.RootSessionID]. Workspaces without app (synthetic 测试
// workspaces) treat every 会话 作为 top-level.
func rootSessionID(ctx context.Context, ws *Workspace, sessionID string) (string, error) {
	if sessionID == "" || ws.App == nil || ws.Sessions == nil {
		return sessionID, nil
	}
	if root, ok := ws.rootCache.Load(sessionID); ok {
		return root.(string), nil
	}
	id := sessionID
	for range maxSessionAncestryDepth {
		sess, err := ws.Sessions.Get(ctx, id)
		if err != nil {
			return sessionID, err
		}
		if sess.ParentSessionID == "" || sess.ParentSessionID == id {
			break
		}
		id = sess.ParentSessionID
	}
	ws.rootCache.Store(sessionID, id)
	return id, nil
}

// scopePermissionPrompts makes 工作区's 权限 requests wait
// 只 为 earlier requests 的 same top-level 会话, instead 的
// every earlier 请求 在 工作区. 一个 提示词 只 reaches
// clients viewing 它的 top-level 会话, 所以 one 那 nobody answers
// would otherwise hold back every other 会话's prompts, unseen 通过
// clients waiting 在 them. Sub-agent prompts still wait 为 their
// top-level 会话's, since same viewers answer them one 在
// time.
func scopePermissionPrompts(ws *Workspace) {
	if ws.App == nil || ws.Permissions == nil {
		return
	}
	ws.Permissions.SetSessionScope(func(ctx context.Context, sessionID string) string {
		root, _ := rootSessionID(ctx, ws, sessionID)
		return root
	})
}

// republishPendingPrompts re-publishes 工作区's pending
// 权限 requests 和 question batches 当 their 会话 matches
// one 客户端 just switched 到. Without 这, 提示词 raised while
// nobody viewed 会话 would remain invisible — 和 therefore
// unanswerable — until 运行 was cancelled. Delivery 是 still scoped
// 通过 per-客户端 SSE filter, 所以 只 会话's viewers (re)see
// 提示词.
func (b *Backend) republishPendingPrompts(ctx context.Context, ws *Workspace, sessionID string) {
	if ws.App == nil {
		return
	}
	// individual services 是 nil 在 workspaces 用 stubbed app
	// (tests); treat those 作为 having no pending prompts.
	if ws.Permissions != nil {
		for _, req := range ws.Permissions.ActiveRequests() {
			if b.sessionMatches(ctx, ws, req.SessionID, sessionID) {
				ws.SendEvent(pubsub.Event[permission.PermissionRequest]{
					Type:    pubsub.CreatedEvent,
					Payload: req,
				})
			}
		}
	}
	if ws.Questions != nil {
		for _, req := range ws.Questions.Pending() {
			if b.sessionMatches(ctx, ws, req.SessionID, sessionID) {
				ws.SendEvent(pubsub.Event[question.Request]{
					Type:    pubsub.CreatedEvent,
					Payload: req,
				})
			}
		}
	}
}

// sessionMatches reports whether 事件 scoped 到 eventSessionID
// belongs 到 given (top-level) 会话, either directly 或 through
// 事件 会话's ancestry. Resolution failures fail closed here:
// caller simply refrains 从 re-publishing.
func (b *Backend) sessionMatches(ctx context.Context, ws *Workspace, eventSessionID, sessionID string) bool {
	if eventSessionID == sessionID {
		return true
	}
	root, err := rootSessionID(ctx, ws, eventSessionID)
	if err != nil {
		slog.Debug("Failed to resolve root session", "session_id", eventSessionID, "error", err)
		return false
	}
	return root == sessionID
}

// GetLSPStates returns state 的 all LSP clients.
func (b *Backend) GetLSPStates(workspaceID string) (map[string]app.LSPClientInfo, error) {
	_, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return app.GetLSPStates(), nil
}

// GetLSPDiagnostics returns diagnostics 为 specific LSP 客户端 在
// 工作区.
func (b *Backend) GetLSPDiagnostics(workspaceID, lspName string) (any, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	for name, client := range ws.LSPManager.Clients().Seq2() {
		if name == lspName {
			return client.GetDiagnostics(), nil
		}
	}

	// GetLSPStates also lists servers 那 是 no longer running (failed
	// init, stopped, 或 started 通过 another 工作区), 所以 callers iterate
	// over names 这 manager does 不 hold. 报告 those 作为 having no
	// diagnostics, like local 工作区 does, instead 的 404.
	if _, ok := app.GetLSPState(lspName); ok {
		return map[protocol.DocumentURI][]protocol.Diagnostic{}, nil
	}

	return nil, ErrLSPClientNotFound
}

// GetWorkspaceConfig returns 工作区-level configuration.
func (b *Backend) GetWorkspaceConfig(workspaceID string) (*config.Config, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Cfg.Config(), nil
}

// GetWorkspaceProviders returns configured providers 为
// 工作区.
func (b *Backend) GetWorkspaceProviders(workspaceID string) (any, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	providers, _ := config.Providers(ws.Cfg.Config())
	return providers, nil
}

// LSPStart starts LSP 服务端 为 given 路径.
func (b *Backend) LSPStart(ctx context.Context, workspaceID, path string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	ws.LSPManager.Start(ctx, path)
	return nil
}

// LSPStopAll stops all LSP servers 为 工作区.
func (b *Backend) LSPStopAll(ctx context.Context, workspaceID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	ws.LSPManager.StopAll(ctx)
	return nil
}

// MCPGetStates returns current state 的 all MCP clients.
func (b *Backend) MCPGetStates(_ string) map[string]mcptools.ClientInfo {
	return mcptools.GetStates()
}

// MCPRefreshPrompts refreshes prompts 为 named MCP 客户端.
func (b *Backend) MCPRefreshPrompts(ctx context.Context, _ string, name string) {
	mcptools.RefreshPrompts(ctx, name)
}

// MCPRefreshResources refreshes resources 为 named MCP 客户端.
func (b *Backend) MCPRefreshResources(ctx context.Context, _ string, name string) {
	mcptools.RefreshResources(ctx, name)
}

// MCPPendingAuth returns MCP servers awaiting OAuth 认证,
// 为 clients 那 need 到 提示词 用户. workspaceID selects
// 工作区 whose 配置 provides 服务端 URLs.
func (b *Backend) MCPPendingAuth(workspaceID string) ([]mcptools.PendingAuthServer, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	return mcptools.PendingAuthMCPs(ws.Cfg), nil
}

// MCPAuthURL returns current OAuth authorization URL 为 named
// 服务端, 如果 flow 是 在 progress.
func (b *Backend) MCPAuthURL(name string) string {
	return mcptools.MCPAuthURL(name)
}

// MCPAuthenticate runs OAuth flow 为 named MCP 服务端 用
// local browser suppressed: authorization URL 是 exposed via
// MCPAuthURL/MCPPendingAuth 为 calling 客户端 到 open 在 用户's
// machine. 调用 blocks until flow completes, fails, 或 ctx 是
// cancelled. workspaceID selects 工作区 whose 配置 drives
// 流程.
func (b *Backend) MCPAuthenticate(ctx context.Context, workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	finish, cancel, err := mcptools.BeginAuth(ws.Cfg, name)
	if err != nil {
		return err
	}
	defer cancel()
	return finish(ctx)
}
