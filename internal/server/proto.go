// proto.go —— HTTP controller：解析请求、校验 client_id、调用 backend、写响应。
//
// 多客户端的关键校验在这里：缺失或非法的 client_id 会被直接拒绝。
// 路由定义在 endpoints.go，真正的业务在 backend。
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/google/uuid"
)

type controllerV1 struct {
	backend *backend.Backend
	server  *Server
}

// handleGetHealth checks 服务端 health. 一个 服务端 那 has committed 到
// shutting down answers 503: it takes no new workspaces, 所以 readiness
// probe must 不 mistake it 为 live 服务端.
func (c *controllerV1) handleGetHealth(w http.ResponseWriter, _ *http.Request) {
	if c.backend.Closing() {
		jsonError(w, http.StatusServiceUnavailable, backend.ErrServerShuttingDown.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetVersion returns 服务端 version information.
func (c *controllerV1) handleGetVersion(w http.ResponseWriter, _ *http.Request) {
	jsonEncode(w, c.backend.VersionInfo())
}

// handlePostControl sends control command 到 服务端.
func (c *controllerV1) handlePostControl(w http.ResponseWriter, r *http.Request) {
	var req proto.ServerControl
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	switch req.Command {
	case proto.ServerControlShutdown, proto.ServerControlShutdownIfIdle:
		// Both spellings 是 conditional. Only backend can rule 在
		// idleness without racing 会话 那 arrives between
		// 客户端's own check 和 它的 请求, 和 guarding plain
		// command too means clients predating the check cannot take live
		// sessions down either.
		if !c.backend.ShutdownIfIdle() {
			c.handleError(w, r, backend.ErrServerNotIdle)
			return
		}
	default:
		c.server.logError(r, "Unknown command", "command", req.Command)
		jsonError(w, http.StatusBadRequest, "unknown command")
		return
	}
}

// handleGetConfig returns global 服务端 configuration.
func (c *controllerV1) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	jsonEncode(w, c.backend.Config())
}

// handleGetWorkspaces lists all workspaces.
func (c *controllerV1) handleGetWorkspaces(w http.ResponseWriter, _ *http.Request) {
	jsonEncode(w, c.backend.ListWorkspaces())
}

// handleGetWorkspace returns single 工作区 通过 ID.
func (c *controllerV1) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, err := c.backend.GetWorkspaceProto(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, ws)
}

// handlePostWorkspaces creates new 工作区.
func (c *controllerV1) handlePostWorkspaces(w http.ResponseWriter, r *http.Request) {
	var args proto.Workspace
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	_, result, err := c.backend.CreateWorkspace(args)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, result)
}

// requireClientID reads client_id 查询 parameter 和 validates it
// 作为 UUID. On failure it writes 400 和 returns false.
func (c *controllerV1) requireClientID(w http.ResponseWriter, r *http.Request) (string, bool) {
	cid := r.URL.Query().Get("client_id")
	if cid == "" {
		c.server.logError(r, "Missing client_id query parameter")
		jsonError(w, http.StatusBadRequest, "client_id is required")
		return "", false
	}
	if _, err := uuid.Parse(cid); err != nil {
		c.server.logError(r, "Invalid client_id", "error", err)
		jsonError(w, http.StatusBadRequest, "client_id is not a valid UUID")
		return "", false
	}
	return cid, true
}

// handlePostWorkspaceCurrentSession records calling 客户端's
// current 会话 selection 为 工作区. An empty session_id
// clears 条目 (e.g. 客户端 是 在 landing screen).
func (c *controllerV1) handlePostWorkspaceCurrentSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	clientID, ok := c.requireClientID(w, r)
	if !ok {
		return
	}
	var req proto.CurrentSession
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	if err := c.backend.SetCurrentSession(r.Context(), id, clientID, req.SessionID); err != nil {
		c.handleError(w, r, err)
		return
	}
}

// handleGetWorkspaceHostContext returns what host application has 在
// view.
func (c *controllerV1) handleGetWorkspaceHostContext(w http.ResponseWriter, r *http.Request) {
	hc, err := c.backend.GetHostContext(r.PathValue("id"))
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, hc)
}

// handlePostWorkspaceHostContext records what host application has 在
// view.
func (c *controllerV1) handlePostWorkspaceHostContext(w http.ResponseWriter, r *http.Request) {
	var hc proto.HostContext
	if err := json.NewDecoder(r.Body).Decode(&hc); err != nil && !errors.Is(err, io.EOF) {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	if err := c.backend.SetHostContext(r.PathValue("id"), hc); err != nil {
		c.handleError(w, r, err)
	}
}

// handleDeleteClient retires 客户端, releasing every claim it holds.
func (c *controllerV1) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	if err := c.backend.RetireClient(r.PathValue("client_id")); err != nil {
		c.handleError(w, r, err)
		return
	}
}

// handleDeleteWorkspaces deletes 工作区.
func (c *controllerV1) handleDeleteWorkspaces(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	clientID, ok := c.requireClientID(w, r)
	if !ok {
		return
	}
	if err := c.backend.DeleteWorkspace(id, clientID); err != nil {
		c.handleError(w, r, err)
		return
	}
}

// handleGetWorkspaceConfig returns 工作区 configuration.
func (c *controllerV1) handleGetWorkspaceConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg, err := c.backend.GetWorkspaceConfig(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, cfg)
}

// handleGetWorkspaceProviders lists available providers 为 工作区.
func (c *controllerV1) handleGetWorkspaceProviders(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	providers, err := c.backend.GetWorkspaceProviders(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, providers)
}

// handleGetWorkspaceEvents streams 工作区 events 作为 服务端-Sent Events.
func (c *controllerV1) handleGetWorkspaceEvents(w http.ResponseWriter, r *http.Request) {
	flusher := http.NewResponseController(w)
	id := r.PathValue("id")
	clientID, ok := c.requireClientID(w, r)
	if !ok {
		return
	}
	// Subscribe 到 事件 总线 BEFORE attaching 客户端.
	// AttachClient bumps 流 count 那 observers use 到
	// detect live 订阅者; subscribing first guarantees 那
	// once 客户端 appears attached, any published 事件 是
	// delivered rather than dropped 在 不-yet-registered 流.
	events, overflow, err := c.backend.SubscribeEventsWithOverflow(r.Context(), id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	sessions, err := c.backend.SubscribeClientSessions(r.Context(), id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	if err := c.backend.AttachClient(id, clientID); err != nil {
		c.handleError(w, r, err)
		return
	}
	defer c.backend.DetachClient(id, clientID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// 刷盘 headers immediately 所以 clients see 200 响应
	// before any events arrive. Without 这, quiet 工作区
	// keeps 客户端's SubscribeEvents 调用 blocked 在
	// initial RoundTrip.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	c.replayHostTheme(w, r, flusher, id)
	c.replayClientSessions(w, r, flusher, id)

	for {
		select {
		case <-r.Context().Done():
			c.server.logDebug(r, "Stopping event stream")
			return
		case <-overflow:
			// 客户端 stopped keeping up ( suspended 或 wedged
			// 终态) 和 events were dropped 为 it, possibly
			// 权限 请求 或 运行's completion. End 流
			// rather than leave silent gap. 一个 客户端 用 reconnect
			// loop ( TUI) re-reports 它的 会话, which re-delivers
			// pending 权限 和 question prompts, 和 reloads what
			// it shows. Nothing re-delivers a dropped RunComplete:
			// `crush 运行` has no reconnect loop 和 treats closed
			// 流 作为 failed 运行.
			slog.Warn("Closing event stream of a client that fell behind",
				"workspace", id, "client_id", clientID)
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !c.deliverToClient(r.Context(), id, clientID, ev.Payload) {
				continue
			}
			c.writeEvent(w, r, flusher, ev.Payload)
		case sev, ok := <-sessions:
			if !ok {
				sessions = nil
				continue
			}
			c.writeEvent(w, r, flusher, sev)
		}
	}
}

// writeEvent wraps 工作区 事件 在 它的 SSE envelope 和 writes it 到
// 流. Events 用 no wire form 是 skipped.
func (c *controllerV1) writeEvent(w http.ResponseWriter, r *http.Request, flusher *http.ResponseController, ev any) {
	wrapped := wrapEvent(ev)
	if wrapped == nil {
		return
	}
	data, err := json.Marshal(wrapped)
	if err != nil {
		c.server.logError(r, "Failed to marshal event", "error", err)
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// handleGetWorkspaceLSPs lists LSP clients 为 工作区.
func (c *controllerV1) handleGetWorkspaceLSPs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	states, err := c.backend.GetLSPStates(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	result := make(map[string]proto.LSPClientInfo, len(states))
	for k, v := range states {
		result[k] = proto.LSPClientInfo{
			Name:            v.Name,
			State:           v.State,
			Error:           v.Error,
			DiagnosticCount: v.DiagnosticCount,
			ConnectedAt:     v.ConnectedAt,
		}
	}
	jsonEncode(w, result)
}

// handleGetWorkspaceLSPDiagnostics returns diagnostics 为 LSP 客户端.
func (c *controllerV1) handleGetWorkspaceLSPDiagnostics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	lspName := r.PathValue("lsp")
	diagnostics, err := c.backend.GetLSPDiagnostics(id, lspName)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, diagnostics)
}

// handleGetWorkspaceSessions lists sessions 为 工作区.
func (c *controllerV1) handleGetWorkspaceSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sessions, err := c.backend.ListSessions(r.Context(), id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	ws, _ := c.backend.GetWorkspace(id)
	result := make([]proto.Session, len(sessions))
	for i, s := range sessions {
		result[i] = sessionToProto(s)
		result[i].IsBusy = isSessionBusy(ws, s.ID)
		result[i].AttachedClients = attachedClients(ws, s.ID)
	}
	jsonEncode(w, result)
}

// handlePostWorkspaceSessions creates new 会话 在 工作区.
func (c *controllerV1) handlePostWorkspaceSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var args session.Session
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	sess, err := c.backend.CreateSession(r.Context(), id, args.Title)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	ws, _ := c.backend.GetWorkspace(id)
	out := sessionToProto(sess)
	out.IsBusy = isSessionBusy(ws, sess.ID)
	out.AttachedClients = attachedClients(ws, sess.ID)
	jsonEncode(w, out)
}

// handleGetWorkspaceSession returns single 会话.
func (c *controllerV1) handleGetWorkspaceSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	sess, err := c.backend.GetSession(r.Context(), id, sid)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	ws, _ := c.backend.GetWorkspace(id)
	out := sessionToProto(sess)
	out.IsBusy = isSessionBusy(ws, sess.ID)
	out.AttachedClients = attachedClients(ws, sess.ID)
	jsonEncode(w, out)
}

// handleGetWorkspaceSessionHistory returns history 为 会话.
func (c *controllerV1) handleGetWorkspaceSessionHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	history, err := c.backend.ListSessionHistory(r.Context(), id, sid)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	files := make([]proto.File, len(history))
	for i, f := range history {
		files[i] = fileToProto(f)
	}
	jsonEncode(w, files)
}

// handleGetWorkspaceSessionMessages returns all messages 为 会话.
func (c *controllerV1) handleGetWorkspaceSessionMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	messages, err := c.backend.ListSessionMessages(r.Context(), id, sid)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, messagesToProto(messages))
}

// handlePutWorkspaceSession updates 会话.
func (c *controllerV1) handlePutWorkspaceSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var sess proto.Session
	if err := json.NewDecoder(r.Body).Decode(&sess); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	saved, err := c.backend.SaveSession(r.Context(), id, protoToSession(sess))
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	ws, _ := c.backend.GetWorkspace(id)
	out := sessionToProto(saved)
	out.IsBusy = isSessionBusy(ws, saved.ID)
	out.AttachedClients = attachedClients(ws, saved.ID)
	jsonEncode(w, out)
}

// handleDeleteWorkspaceSession deletes 会话.
func (c *controllerV1) handleDeleteWorkspaceSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	if err := c.backend.DeleteSession(r.Context(), id, sid); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceSessionUserMessages returns 用户 messages 为 会话.
func (c *controllerV1) handleGetWorkspaceSessionUserMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	messages, err := c.backend.ListUserMessages(r.Context(), id, sid)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, messagesToProto(messages))
}

// handleGetWorkspaceAllUserMessages returns all 用户 messages across sessions.
func (c *controllerV1) handleGetWorkspaceAllUserMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	messages, err := c.backend.ListAllUserMessages(r.Context(), id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, messagesToProto(messages))
}

// handleGetWorkspaceSessionFileTrackerFiles lists files 读取 在 会话.
func (c *controllerV1) handleGetWorkspaceSessionFileTrackerFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	files, err := c.backend.FileTrackerListReadFiles(r.Context(), id, sid)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, files)
}

// handlePostWorkspaceFileTrackerRead records 文件 读取 事件.
func (c *controllerV1) handlePostWorkspaceFileTrackerRead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.FileTrackerReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if err := c.backend.FileTrackerRecordRead(r.Context(), id, req.SessionID, req.Path); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceFileTrackerLastRead returns last 读取 time 为 文件.
func (c *controllerV1) handleGetWorkspaceFileTrackerLastRead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.URL.Query().Get("session_id")
	path := r.URL.Query().Get("path")

	t, err := c.backend.FileTrackerLastReadTime(r.Context(), id, sid, path)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, t)
}

// handlePostWorkspaceLSPStart starts LSP 服务端 为 路径.
func (c *controllerV1) handlePostWorkspaceLSPStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.LSPStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if err := c.backend.LSPStart(r.Context(), id, req.Path); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceLSPStopAll stops all LSP servers.
func (c *controllerV1) handlePostWorkspaceLSPStopAll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := c.backend.LSPStopAll(r.Context(), id); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceAgent returns agent info 为 工作区.
func (c *controllerV1) handleGetWorkspaceAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, err := c.backend.GetAgentInfo(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, info)
}

// handlePostWorkspaceAgent sends 消息 到 agent.
func (c *controllerV1) handlePostWorkspaceAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var msg proto.AgentMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	// 运行's lifetime 是 detached 从 prompting 客户端's HTTP
	// 请求: SendMessage validates 和 accepts 提示词, dispatches
	// 运行 在 goroutine bound 到 工作区 上下文, 和 returns
	// immediately. 一个 dropping 它的 TCP 连接 (network blip, TUI
	// restart) 或 B canceling 会话 via explicit 取消
	// endpoint can no longer tear down turn 那 other subscribed
	// clients 是 still watching. Only explicit 取消 endpoint
	// should 是 able 到 end 运行.
	if err := c.backend.SendMessage(id, msg); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handlePostWorkspaceAgentInit initializes agent 为 工作区.
func (c *controllerV1) handlePostWorkspaceAgentInit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.AgentInitRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			c.server.logError(r, "Failed to decode agent init request", "error", err)
			jsonError(w, http.StatusBadRequest, "failed to decode request")
			return
		}
	}

	if err := c.backend.InitAgent(r.Context(), id, req.Interactive); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceAgentUpdate updates agent 为 工作区.
func (c *controllerV1) handlePostWorkspaceAgentUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := c.backend.UpdateAgent(r.Context(), id); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceAgentMain switches 工作区's 活跃 agent
// (e.g. "coder" 或 "plan").
func (c *controllerV1) handlePostWorkspaceAgentMain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.AgentSetMainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode agent set-main request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if err := c.backend.SetMainAgent(id, req.AgentID); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceAgentSession returns specific agent 会话.
func (c *controllerV1) handleGetWorkspaceAgentSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	agentSession, err := c.backend.GetAgentSession(r.Context(), id, sid)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, agentSession)
}

// handlePostWorkspaceAgentSessionCancel cancels running agent 会话.
func (c *controllerV1) handlePostWorkspaceAgentSessionCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	if err := c.backend.CancelSession(id, sid); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceAgentSessionPromptQueued returns whether queued 提示词 exists.
func (c *controllerV1) handleGetWorkspaceAgentSessionPromptQueued(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	queued, err := c.backend.QueuedPrompts(id, sid)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, queued)
}

// handlePostWorkspaceAgentSessionPromptClear clears 提示词 队列 为 会话.
func (c *controllerV1) handlePostWorkspaceAgentSessionPromptClear(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	if err := c.backend.ClearQueue(id, sid); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceAgentSessionNote keeps note 为 会话's next 提示词.
func (c *controllerV1) handlePostWorkspaceAgentSessionNote(w http.ResponseWriter, r *http.Request) {
	var note proto.SessionNote
	if err := json.NewDecoder(r.Body).Decode(&note); err != nil || note.Text == "" {
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	if err := c.backend.AddNote(r.PathValue("id"), r.PathValue("sid"), note.Text); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceAgentSessionSummarize summarizes 会话.
func (c *controllerV1) handlePostWorkspaceAgentSessionSummarize(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	if err := c.backend.SummarizeSession(r.Context(), id, sid); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceAgentSessionShell runs shell command 在 工作区.
func (c *controllerV1) handlePostWorkspaceAgentSessionShell(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")

	var req proto.ShellCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	req.SessionID = sid

	resp, err := c.backend.RunShellCommand(r.Context(), id, req)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, resp)
}

// handleGetWorkspaceAgentSessionPromptList returns list 的 queued prompts.
func (c *controllerV1) handleGetWorkspaceAgentSessionPromptList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	prompts, err := c.backend.QueuedPromptsList(id, sid)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, prompts)
}

// handleGetWorkspaceAgentDefaultSmallModel returns 默认 small 模型 为 提供方.
func (c *controllerV1) handleGetWorkspaceAgentDefaultSmallModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	providerID := r.URL.Query().Get("provider_id")
	model, err := c.backend.GetDefaultSmallModel(id, providerID)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, model)
}

// handlePostWorkspacePermissionsGrant grants 权限 请求.
func (c *controllerV1) handlePostWorkspacePermissionsGrant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.PermissionGrant
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	resolved, err := c.backend.GrantPermission(id, req)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.PermissionGrantResponse{Resolved: resolved})
}

// handlePostWorkspaceSessionAutoApprove auto-approves every 权限
// 请求 会话 raises.
func (c *controllerV1) handlePostWorkspaceSessionAutoApprove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	if err := c.backend.AutoApproveSession(id, sid); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceQuestionsAnswer submits answers 为 batch question.
func (c *controllerV1) handlePostWorkspaceQuestionsAnswer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.QuestionAnswer
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	resolved, err := c.backend.AnswerQuestion(id, req)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.QuestionAnswerResponse{Resolved: resolved})
}

// handlePostWorkspaceQuestionsCancel cancels a pending question batch
// 为 工作区.
func (c *controllerV1) handlePostWorkspaceQuestionsCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Clients 那 predate batch IDs send no body.
	var req proto.QuestionCancel
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	cancelled, err := c.backend.CancelQuestion(id, req.BatchRequestID)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.QuestionAnswerResponse{Resolved: cancelled})
}

// handlePostWorkspacePermissionsMode sets 权限 mode 为 工作区.
func (c *controllerV1) handlePostWorkspacePermissionsMode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.PermissionSetModeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	mode := proto.ProtoModeToPermission(req.Mode)
	if err := c.backend.SetPermissionMode(id, mode); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleError maps backend errors 到 HTTP status codes 和 writes
// JSON 错误 响应.
//
// Runtime cancellation 的 agent 运行 no longer reaches here 为
// agent-提示词 路径: SendMessage 是 fire-和-forget ( 处理器 returns
// 202 before 运行 starts) 和 Backend.runAgent swallows
// 上下文.Canceled, surfacing FinishReasonCanceled marker 到 SSE
// subscribers instead. The remaining callers pass synchronous backend
// errors, 所以 上下文.Canceled gets no special case 和 would fall through
// 到 默认 500 like any other unexpected 错误.
func (c *controllerV1) handleError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, backend.ErrWorkspaceNotFound):
		status = http.StatusNotFound
	case errors.Is(err, backend.ErrLSPClientNotFound):
		status = http.StatusNotFound
	case errors.Is(err, backend.ErrAgentNotInitialized):
		status = http.StatusBadRequest
	case errors.Is(err, backend.ErrAgentBusy):
		// Switching main agent mid-运行 could strand 运行's
		// queued prompts 在 previous agent; mirror TUI's
		// own busy guard 为 API callers.
		status = http.StatusConflict
	case errors.Is(err, backend.ErrPathRequired):
		status = http.StatusBadRequest
	case errors.Is(err, backend.ErrInvalidPermissionAction):
		status = http.StatusBadRequest
	case errors.Is(err, backend.ErrUnknownCommand):
		status = http.StatusBadRequest
	case errors.Is(err, backend.ErrInvalidClientID):
		status = http.StatusBadRequest
	case errors.Is(err, backend.ErrInvalidTheme):
		// 消息 names every offending color (或 unknown base
		// theme), 所以 host can fix 它的 theme mapping 从 响应.
		status = http.StatusBadRequest
	case errors.Is(err, backend.ErrClientNotAttached):
		// 409, 不 404: 工作区 exists, caller just has no live
		// 流 yet. 一个 404 here 是 indistinguishable 从 "工作区
		// gone" 和 would trip clients 那 treat 404 作为 trigger 为
		// 工作区 recovery.
		status = http.StatusConflict
	case errors.Is(err, backend.ErrWorkspaceClosing),
		errors.Is(err, backend.ErrServerNotIdle),
		errors.Is(err, backend.ErrClientRetired),
		errors.Is(err, backend.ErrPromptOverrideMismatch):
		status = http.StatusConflict
	case errors.Is(err, backend.ErrServerShuttingDown):
		// 503, 不 409: 请求 是 不 wrong, 这 process 是 just
		// leaving. Clients 重试 against 它的 replacement.
		status = http.StatusServiceUnavailable
	}
	c.server.logError(r, err.Error())
	jsonError(w, status, err.Error())
}

func jsonEncode(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(proto.Error{Message: message})
}
