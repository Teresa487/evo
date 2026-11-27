package backend

import (
	"context"

	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
)

// CreateSession creates new 会话 在 given 工作区.
func (b *Backend) CreateSession(ctx context.Context, workspaceID, title string) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.CreateSession(ctx, title)
}

// GetSession retrieves 会话 通过 工作区 和 会话 ID.
func (b *Backend) GetSession(ctx context.Context, workspaceID, sessionID string) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.Sessions.Get(ctx, sessionID)
}

// ListSessions returns all sessions 在 given 工作区.
func (b *Backend) ListSessions(ctx context.Context, workspaceID string) ([]session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Sessions.List(ctx)
}

// GetAgentSession returns 会话 metadata 用 agent's busy
// status.
func (b *Backend) GetAgentSession(ctx context.Context, workspaceID, sessionID string) (proto.AgentSession, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return proto.AgentSession{}, err
	}

	se, err := ws.Sessions.Get(ctx, sessionID)
	if err != nil {
		return proto.AgentSession{}, err
	}

	var isSessionBusy bool
	if coord := ws.Coordinator(); coord != nil {
		isSessionBusy = coord.IsSessionBusy(sessionID)
	}

	return proto.AgentSession{
		Session: proto.Session{
			ID:    se.ID,
			Title: se.Title,
		},
		IsBusy: isSessionBusy,
	}, nil
}

// ListSessionMessages returns all messages 为 会话.
func (b *Backend) ListSessionMessages(ctx context.Context, workspaceID, sessionID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	// Every remote 客户端 reads through here, 所以 这 是 where interrupted
	// calls get settled 为 them.
	if coord := ws.Coordinator(); coord != nil {
		return coord.ListSettledMessages(ctx, sessionID)
	}
	// Drain debounced updates 所以 clients observe latest 在-memory state.
	if err := ws.Messages.FlushAll(ctx); err != nil {
		return nil, err
	}
	return ws.Messages.List(ctx, sessionID)
}

// ListSessionHistory returns history items 为 会话.
func (b *Backend) ListSessionHistory(ctx context.Context, workspaceID, sessionID string) ([]history.File, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.History.ListBySession(ctx, sessionID)
}

// SaveSession updates 会话 在 given 工作区.
func (b *Backend) SaveSession(ctx context.Context, workspaceID string, sess session.Session) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.Sessions.Save(ctx, sess)
}

// MCPServersDisabled returns MCP servers disabled 为 given
// 工作区's repository.
func (b *Backend) MCPServersDisabled(ctx context.Context, workspaceID string) ([]string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Sessions.MCPDisabledServers(ctx)
}

// MCPServersEnabled returns MCP servers 用 repository-scoped
// enabled override (配置-disabled servers 用户 enabled here).
func (b *Backend) MCPServersEnabled(ctx context.Context, workspaceID string) ([]string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Sessions.MCPServersEnabled(ctx)
}

// SetMCPServerDisabled toggles repository-scoped MCP override 为
// given 工作区 和 applies it 到 running 客户端.
func (b *Backend) SetMCPServerDisabled(ctx context.Context, workspaceID, name string, disabled bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if err := ws.Sessions.SetMCPServerDisabled(ctx, name, disabled); err != nil {
		return err
	}
	return mcptools.SetLocalDisabled(ctx, ws.Cfg, name, disabled)
}

// SetMCPServerConfigDisabled toggles MCP 服务端's disabled flag 在
// global 配置 和 applies change 到 running 客户端.
func (b *Backend) SetMCPServerConfigDisabled(ctx context.Context, workspaceID, name string, disabled bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	override, err := session.HasMCPOverride(ctx, ws.Sessions, name)
	if err != nil {
		return err
	}
	if err := mcptools.SetConfigDisabled(ctx, ws.Cfg, config.ScopeGlobal, name, disabled, override); err != nil {
		return err
	}
	announceConfigChanged(ws)
	return nil
}

// SkillsDisabled returns skills disabled 为 given 工作区's
// repository.
func (b *Backend) SkillsDisabled(ctx context.Context, workspaceID string) ([]string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Sessions.SkillsDisabled(ctx)
}

// SetSkillDisabled toggles repository-scoped skill override 为
// given 工作区.
func (b *Backend) SetSkillDisabled(ctx context.Context, workspaceID, name string, disabled bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	return ws.Sessions.SetSkillDisabled(ctx, name, disabled, ws.Cfg.SkillConfigDisabled(name))
}

// SkillsEnabled returns skills 用 repository-scoped enabled
// override 为 given 工作区's repository.
func (b *Backend) SkillsEnabled(ctx context.Context, workspaceID string) ([]string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Sessions.SkillsEnabled(ctx)
}

// SetSkillConfigDisabled toggles skill's disabled flag 在 global
// 配置's options.disabled_skills list.
func (b *Backend) SetSkillConfigDisabled(ctx context.Context, workspaceID, name string, disabled bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if err := ws.Cfg.SetSkillDisabledConfig(config.ScopeGlobal, name, disabled); err != nil {
		return err
	}
	announceConfigChanged(ws)
	return nil
}

// StartMCPServer starts named MCP 服务端 为 given 工作区 even
// 当 它的 配置 条目 是 disabled. Runtime-只.
func (b *Backend) StartMCPServer(ctx context.Context, workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	return mcptools.InitializeSingleForced(ctx, name, ws.Cfg)
}

// DeleteSession deletes 会话 从 given 工作区.
func (b *Backend) DeleteSession(ctx context.Context, workspaceID, sessionID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	return ws.Sessions.Delete(ctx, sessionID)
}

// ListUserMessages returns 用户-角色 messages 为 会话.
func (b *Backend) ListUserMessages(ctx context.Context, workspaceID, sessionID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Messages.ListUserMessages(ctx, sessionID)
}

// ListAllUserMessages returns all 用户-角色 messages across sessions.
func (b *Backend) ListAllUserMessages(ctx context.Context, workspaceID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Messages.ListAllUserMessages(ctx)
}
