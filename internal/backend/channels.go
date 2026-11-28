// channels.go —— 服务端的渠道事件注入：把 MCP channel 消息投进 session。

package backend

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
)

// channelSessionStore 是 slice 的 会话.Service 渠道 router
// needs 到 pick 或 创建 target 会话. Narrowed 所以 tests can fake it
// without implementing the full service.
type channelSessionStore interface {
	Create(ctx context.Context, title string) (session.Session, error)
	Get(ctx context.Context, id string) (session.Session, error)
	List(ctx context.Context) ([]session.Session, error)
}

// startChannelRouter subscribes 到 MCP events 为 backend's lifetime
// 和 injects each 渠道 push into exactly one 会话 per opted-在
// 工作区. In 客户端/服务端 mode 服务端 owns 渠道 routing:
// frontends do 不 inject (see 工作区.ClientWorkspace.RoutesChannelEvents),
// 所以 push 是 processed once no matter how many clients 是 attached —
// including zero, which keeps " pushed 事件 是 never dropped"
// contract 为 headless `evo serve`.
//
// Events 是 drained sequentially 从 subscription, 所以 pushes keep
// their arrival order. Delivery 到 multiple workspaces within one 事件
// 是 dispatched concurrently 所以 slow agent init 在 one 工作区 does
// 不 block others.
func (b *Backend) startChannelRouter() {
	events := mcptools.SubscribeChannelEvents(b.ctx)
	go func() {
		for ev := range events {
			if ev.Payload.ChannelMessage == "" {
				continue
			}
			b.routeChannelMessage(ev.Payload)
		}
	}()
}

// routeChannelMessage delivers one 渠道 push 到 every hosted 工作区
// 那 both declares originating MCP 服务端 在 它的 配置 和 opted it
// 在 via --channels 或 channel_enabled. MCP 事件 总线 是 process-global, 所以
// 配置 check 是 what scopes push 到 工作区(s) 那 actually
// enabled 渠道. Delivery 到 each 工作区 是 dispatched 在 它的
// own goroutine 所以 slow agent init 在 one does 不 block rest.
func (b *Backend) routeChannelMessage(ev mcptools.Event) {
	var wg sync.WaitGroup
	for _, ws := range b.workspaces.Seq2() {
		mcpCfg, declared := ws.Cfg.Config().MCP[ev.Name]
		if !declared {
			continue
		}
		if !mcptools.ChannelOptIn(mcpCfg, ws.Cfg.Overrides().EnabledChannels, ev.Name) {
			continue
		}
		wg.Go(func() {
			b.injectChannelMessage(ws, ev.Name, ev.ChannelMessage)
		})
	}
	wg.Wait()
}

// injectChannelMessage runs one rendered <渠道> element 作为 agent
// turn 在 ws. coder agent 是 initialized 在 demand 所以 headless
// 服务端 (where no 客户端 has called InitCoderAgent yet) can still
// process pushes; non-interactive variant 是 used because there 是
// nobody attached 到 answer question 工具. 渠道 binding 是
// carried through dispatchRun's internal argument, 不 wire
// 消息, 所以 no 客户端 can claim it 为 它的 own turns. Failures 是
// logged 和 push 是 dropped 为 这 工作区; there 是 no
// caller 到 return 错误 到.
func (b *Backend) injectChannelMessage(ws *Workspace, serverName, content string) {
	if _, err := ws.initAgent(ws.ctx, false); err != nil {
		slog.Warn("Channel message dropped: coder agent init failed",
			"workspace", ws.ID, "server", serverName, "error", err)
		return
	}
	sessionID, err := channelTargetSession(ws.ctx, serverName, ws.viewedSessions(), ws.Sessions)
	if err != nil {
		slog.Warn("Channel message dropped: no target session",
			"workspace", ws.ID, "server", serverName, "error", err)
		return
	}
	if err := b.dispatchRun(ws, proto.AgentMessage{
		SessionID: sessionID,
		Prompt:    content,
	}, serverName); err != nil {
		slog.Warn("Channel message dropped: dispatch failed",
			"workspace", ws.ID, "server", serverName, "session", sessionID, "error", err)
	}
}

// channelTargetSession picks 会话 渠道 push should land 在,
// mirroring 在-process TUI semantics ("route into 会话 you
// have open; start one 如果 none 是 open") 作为 closely 作为 multi-客户端
// 服务端 can:
//
//   - exactly one 会话 是 being viewed 通过 attached clients: use it;
//   - several distinct sessions 是 viewed: use most recently
//     updated 的 them (ties broken 通过 smallest ID 为 determinism);
//   - none viewed (all clients 在 landing screen, 或 no clients):
//     most recent top-level 会话 already bound 到 这 渠道 wins, 所以
//     push lands 在 conversation it has been driving rather than
//     whatever unrelated 会话 local edit touched last; otherwise most
//     recently updated top-level 会话, 所以 repeated pushes coalesce
//     into one conversation, creating a 会话 只 当 工作区 has none.
//
// fallback relies 在 会话.Service.List returning 只 top-level
// sessions (no 子代理 sessions) ordered 通过 updated_at DESC, 所以
// first hit 在 list 是 most recently updated top-level 会话.
// underlying 查询 (ListSessions) enforces both: WHERE
// parent_session_id IS NULL ORDER BY updated_at DESC.
func channelTargetSession(ctx context.Context, channel string, viewed []string, sessions channelSessionStore) (string, error) {
	switch len(viewed) {
	case 0:
		existing, err := sessions.List(ctx)
		if err != nil {
			return "", err
		}
		for _, sess := range existing {
			if sess.Channel == channel {
				return sess.ID, nil
			}
		}
		if len(existing) > 0 {
			return existing[0].ID, nil
		}
		sess, err := sessions.Create(ctx, "New Session")
		if err != nil {
			return "", err
		}
		return sess.ID, nil
	case 1:
		return viewed[0], nil
	}

	best := ""
	var bestUpdated int64 = -1
	for _, id := range viewed {
		sess, err := sessions.Get(ctx, id)
		if err != nil {
			// 一个 viewed 会话 那 cannot 是 loaded (e.g. deleted
			// while still selected 客户端-side) 是 skipped rather
			// than failing the whole push.
			continue
		}
		if sess.UpdatedAt > bestUpdated || (sess.UpdatedAt == bestUpdated && sess.ID < best) {
			best = sess.ID
			bestUpdated = sess.UpdatedAt
		}
	}
	if best != "" {
		return best, nil
	}
	// Every viewed 会话 failed 到 load; fall back 到 no-viewed
	// 路径.
	return channelTargetSession(ctx, channel, nil, sessions)
}

// viewedSessions returns distinct sessions currently being viewed 通过
// clients 用 在 least one live SSE 流, sorted 为 determinism.
// Hold-只 clients 和 clients 在 landing screen do 不 contribute.
func (w *Workspace) viewedSessions() []string {
	w.clientsMu.Lock()
	set := make(map[string]struct{})
	for _, cs := range w.clients {
		if cs.streams > 0 && cs.currentSessionID != "" {
			set[cs.currentSessionID] = struct{}{}
		}
	}
	w.clientsMu.Unlock()

	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
