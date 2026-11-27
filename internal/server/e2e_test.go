package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// e2eHarness wires 服务端, 它的 Backend (用 custom shutdownFn we
// can observe), httptest.NewServer, 和 synthetic 工作区 whose
// embedded App has live 事件 总线. 它 是 minimum scaffolding
// multi-客户端 end-到-end scenarios 在 PLAN item 6 need.
type e2eHarness struct {
	httpSrv     *httptest.Server
	srv         *Server
	backend     *backend.Backend
	workspace   *backend.Workspace
	app         *app.App
	shutdownHit atomic.Bool

	// sseWG tracks every SSE reader goroutine spawned 通过
	// [e2eHarness.subscribeSSE]. harness's 清理 钩子 waits 在
	// it after httptest 服务端 has been closed 所以 那 测试
	// cannot leave behind background readers (和 therefore unclosed
	// 响应 bodies) after returning.
	sseWG sync.WaitGroup
}

// installServer attaches fresh 服务端 (用 custom shutdown
// callback 那 flips [e2eHarness.shutdownHit]) wrapped 在
// [httptest.服务端] onto h. 它 registers 清理 hooks 为
// httptest 服务端 和 SSE reader WaitGroup 在 order required
// 通过 LIFO contract documented 在 [newE2EHarness].
//
// Callers 那 want fully synthetic 工作区 use [newE2EHarness];
// callers 那 want 到 drive real CreateWorkspace HTTP 路径 use
// [newRealCreateHarness] 和 then [e2eHarness.postWorkspace].
func (h *e2eHarness) installServer(t *testing.T) {
	t.Helper()
	srv := &Server{}
	srv.backend = backend.New(context.Background(), nil, func() {
		h.shutdownHit.Store(true)
	})
	srv.installHandler()

	hs := httptest.NewServer(srv.Handler())
	// Order matters: t.清理 是 LIFO 和 测试's own per-
	// 流 cancels (cancelA/cancelB) 运行 first. After those, we
	// want hs.Close 到 fire first (所以 any 处理器 still parked 在
	// 它的 `select` returns), THEN sseWG.Wait 所以 every reader
	// goroutine exits 和 closes 它的 响应 body. Any caller-
	// owned cleanups registered *before* installServer (e.g. App
	// teardown 为 synthetic harness) therefore 运行 LAST,
	// after the readers have drained.
	t.Cleanup(h.sseWG.Wait)
	t.Cleanup(hs.Close)

	h.httpSrv = hs
	h.srv = srv
	h.backend = srv.backend
}

// newE2EHarness builds 在-process 服务端 + synthetic 工作区
// whose embedded App 是 real [app.App] constructed via
// [app.NewForTest], 所以 它的 事件 总线 delivers everything SSE
// pipeline expects. Used 通过 scenarios 那 do 不 need 到
// exercise 路径-dedupe behavior 的 [backend.CreateWorkspace].
//
// 清理 tears down App's 总线 只 after sseWG.Wait 和
// hs.Close have 运行, 所以 SSE readers cannot observe dead 总线.
func newE2EHarness(t *testing.T) *e2eHarness {
	t.Helper()

	h := &e2eHarness{}

	// Register App teardown FIRST 所以 LIFO order puts it AFTER
	// cleanups 那 installServer registers below (hs.Close +
	// sseWG.Wait).
	appCtx, cancel := context.WithCancel(context.Background())
	a := app.NewForTest(appCtx)
	t.Cleanup(func() {
		cancel()
		a.ShutdownForTest()
	})

	h.installServer(t)

	ws := &backend.Workspace{
		ID:   uuid.New().String(),
		Path: t.TempDir(),
		App:  a,
	}
	// Synthetic workspaces have an incomplete App; bypass the
	// 默认 teardown 所以 "last 工作区 removed" 路径 can 运行
	// without panicking inside [app.App.Shutdown].
	backend.SetWorkspaceShutdownFnForTest(ws, func() {})
	backend.InsertWorkspaceForTest(h.backend, ws)

	h.workspace = ws
	h.app = a
	return h
}

// newRealCreateHarness builds 在-process 服务端 WITHOUT any
// pre-inserted 工作区, intended 为 tests 那 drive real
// [backend.CreateWorkspace] HTTP 路径 (路径-dedupe scenario). 它
// isolates HOME/XDG_* via [t.Setenv] 所以 [配置.Init] doesn't 读取
// host machine's 配置, which means callers MUST NOT 标记
// 测试 作为 parallel.
func newRealCreateHarness(t *testing.T) *e2eHarness {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	h := &e2eHarness{}
	h.installServer(t)
	return h
}

// postWorkspace drives real POST /v1/workspaces 处理器 和
// returns resolved 工作区 proto. 这 是 how scenario 1
// exercises 路径-dedupe behavior 从 PLAN item 1: two calls
// 用 same 路径 和 distinct ClientIDs must return same
// 工作区 ID.
func (h *e2eHarness) postWorkspace(t *testing.T, args proto.Workspace) proto.Workspace {
	t.Helper()
	body, err := json.Marshal(args)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		h.httpSrv.URL+"/v1/workspaces", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /v1/workspaces must succeed")
	var out proto.Workspace
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotEmpty(t, out.ID, "server must return a workspace id")
	return out
}

// subscribeSSE opens SSE 流 against 测试 服务端 为
// given 工作区 和 客户端 ID. 它 returns 渠道 的 decoded
// envelopes plus 取消 function 那 closes 流.
// returned 渠道 是 closed 当 流 ends.
func (h *e2eHarness) subscribeSSE(t *testing.T, ctx context.Context, workspaceID, clientID string) (<-chan any, context.CancelFunc) {
	t.Helper()
	streamCtx, cancel := context.WithCancel(ctx)

	q := url.Values{"client_id": []string{clientID}}
	reqURL := h.httpSrv.URL + "/v1/workspaces/" + workspaceID + "/events?" + q.Encode()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, reqURL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "SSE subscribe should return 200")

	out := make(chan any, 64)
	h.sseWG.Go(func() {
		defer resp.Body.Close()
		defer close(out)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			data, ok := bytes.CutPrefix(line, []byte("data:"))
			if !ok {
				continue
			}
			data = bytes.TrimSpace(data)
			var p pubsub.Payload
			if err := json.Unmarshal(data, &p); err != nil {
				continue
			}
			ev, decoded := decodeSSEEnvelope(p)
			if !decoded {
				continue
			}
			select {
			case out <- ev:
			case <-streamCtx.Done():
				return
			}
		}
	})
	return out, cancel
}

// decodeSSEEnvelope decodes the discriminated SSE envelope into the
// concrete pubsub.事件[proto.X] payload e2e tests care about.
// Unknown payload types 是 skipped 所以 tests can match 在 type
// assertions without worrying about envelope noise.
func decodeSSEEnvelope(p pubsub.Payload) (any, bool) {
	switch p.Type {
	case pubsub.PayloadTypePermissionRequest:
		var e pubsub.Event[proto.PermissionRequest]
		if err := json.Unmarshal(p.Payload, &e); err != nil {
			return nil, false
		}
		return e, true
	case pubsub.PayloadTypePermissionNotification:
		var e pubsub.Event[proto.PermissionNotification]
		if err := json.Unmarshal(p.Payload, &e); err != nil {
			return nil, false
		}
		return e, true
	case pubsub.PayloadTypeQuestionNotification:
		var e pubsub.Event[proto.QuestionNotification]
		if err := json.Unmarshal(p.Payload, &e); err != nil {
			return nil, false
		}
		return e, true
	case pubsub.PayloadTypeClientSession:
		var e pubsub.Event[proto.ClientSession]
		if err := json.Unmarshal(p.Payload, &e); err != nil {
			return nil, false
		}
		return e, true
	case pubsub.PayloadTypeMessage:
		var e pubsub.Event[proto.Message]
		if err := json.Unmarshal(p.Payload, &e); err != nil {
			return nil, false
		}
		return e, true
	case pubsub.PayloadTypeAgentEvent:
		var e pubsub.Event[proto.AgentEvent]
		if err := json.Unmarshal(p.Payload, &e); err != nil {
			return nil, false
		}
		return e, true
	case pubsub.PayloadTypeRunComplete:
		var e pubsub.Event[proto.RunComplete]
		if err := json.Unmarshal(p.Payload, &e); err != nil {
			return nil, false
		}
		return e, true
	case pubsub.PayloadTypeHostTheme:
		var e pubsub.Event[proto.HostThemeState]
		if err := json.Unmarshal(p.Payload, &e); err != nil {
			return nil, false
		}
		return e, true
	}
	return nil, false
}

// setCurrentSession reports 会话 given 客户端 是 viewing
// via HTTP surface, mirroring TUI's 会话-switch 报告.
func (h *e2eHarness) setCurrentSession(t *testing.T, ctx context.Context, workspaceID, clientID, sessionID string) {
	t.Helper()
	body, err := json.Marshal(proto.CurrentSession{SessionID: sessionID})
	require.NoError(t, err)
	reqURL := h.httpSrv.URL + "/v1/workspaces/" + workspaceID + "/current-session?client_id=" + clientID
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := h.httpSrv.Client().Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// grantPermission posts 权限 grant via HTTP surface 和
// returns 服务端's "resolved" verdict. Mirrors 客户端-side
// GrantPermission flow without importing internal/客户端 (which
// would 创建 import cycle 从 这 在-package 测试).
func (h *e2eHarness) grantPermission(t *testing.T, ctx context.Context, workspaceID string, req proto.PermissionGrant) bool {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.httpSrv.URL+"/v1/workspaces/"+workspaceID+"/permissions/grant",
		bytes.NewReader(body))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := h.httpSrv.Client().Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out proto.PermissionGrantResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Resolved
}

// waitForAttached spins until 工作区's clients map reports 在
// least n entries 用 streams > 0. Catches race where 测试
// publishes events before 服务端-side AttachClient has completed.
func (h *e2eHarness) waitForAttached(t *testing.T, n int) {
	t.Helper()
	h.waitForAttachedOn(t, h.workspace, n)
}

// waitForAttachedOn 是 工作区-explicit form 的 waitForAttached.
// Tests 那 drive 工作区 whose 指针 是 不 存储 在
// harness (e.g. real CreateWorkspace 路径) pass 工作区 在.
func (h *e2eHarness) waitForAttachedOn(t *testing.T, ws *backend.Workspace, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if backend.WorkspaceLiveStreamCountForTest(ws) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d attached streams, have %d", n,
		backend.WorkspaceLiveStreamCountForTest(ws))
}

// drainUntil reads 从 evc until it sees 事件 的 type T 那
// satisfies match, 或 ctx expires. Returns matching 事件 和
// ok=true, 或 zero 值 和 ok=false 在 超时.
func drainUntil[T any](ctx context.Context, evc <-chan any, match func(T) bool) (T, bool) {
	var zero T
	for {
		select {
		case <-ctx.Done():
			return zero, false
		case ev, ok := <-evc:
			if !ok {
				return zero, false
			}
			typed, isT := ev.(T)
			if !isT {
				continue
			}
			if match == nil || match(typed) {
				return typed, true
			}
		}
	}
}

// TestE2E_TwoClientsReceiveSameMessage covers PLAN item 6 scenario 1:
// two clients POST /v1/workspaces 用 same 路径 和 observe
// 那 服务端 returns single 工作区 (路径-dedupe 从 PLAN
// item 1) 和 那 事件 published 在 那 工作区 fans out 到
// both SSE streams.
//
// Cannot 运行 在 parallel: it isolates HOME/XDG_* via t.Setenv 所以
// 配置.Init does 不 读取 host machine's real 配置.
func TestE2E_TwoClientsReceiveSameMessage(t *testing.T) {
	h := newRealCreateHarness(t)
	// Shorten lifecycle windows 所以 工作区's 客户端 holds
	// release quickly during 测试 清理 once both SSE streams have
	// been detached: 创建 grace covers 窗口 before
	// streams attach, detach grace 窗口 after they drop.
	// pooled DB 连接 是 只 released once last hold
	// expires, 和 Windows cannot remove temp data 目录
	// while 那 连接 是 still open.
	h.backend.SetCreateGrace(200 * time.Millisecond)
	h.backend.SetDetachGrace(200 * time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	cidA := uuid.New().String()
	cidB := uuid.New().String()

	// Shared 工作区 路径. Two POSTs 用 这 路径 must
	// deduplicate 在 backend's pathIndex 和 return same
	// 工作区 id.
	wsPath := t.TempDir()
	dataDir := t.TempDir()
	args := proto.Workspace{Path: wsPath, DataDir: dataDir}

	argsA := args
	argsA.ClientID = cidA
	wsRespA := h.postWorkspace(t, argsA)

	argsB := args
	argsB.ClientID = cidB
	wsRespB := h.postWorkspace(t, argsB)

	require.Equal(t, wsRespA.ID, wsRespB.ID,
		"POST /v1/workspaces with the same Path must return the same workspace id")

	// Look up resulting 工作区 在 backend 所以 测试
	// can 发布 events through 它的 real [app.App] 事件 总线.
	ws, err := h.backend.GetWorkspace(wsRespA.ID)
	require.NoError(t, err)
	// Override shutdown callback 所以 测试 清理 doesn't 运行
	// full app.Shutdown 路径 (which would tear down LSP/MCP
	// resources 测试 doesn't need 到 exercise), 但 still
	// release pooled DB 连接 所以 Windows can clean up
	// temp data 目录.
	wsDataDir := ws.Cfg.Config().Options.DataDirectory
	released := make(chan struct{})
	backend.SetWorkspaceShutdownFnForTest(ws, func() {
		_ = db.Release(wsDataDir)
		close(released)
	})
	// Registered before 流 cancels 所以 LIFO 清理 runs it
	// after both streams have dropped. 工作区 是 只 torn
	// down once 它的 detach grace expires, 和 data 目录
	// cannot 是 removed 在 Windows until 那 release happens.
	t.Cleanup(func() {
		select {
		case <-released:
		case <-time.After(10 * time.Second):
			t.Error("workspace was not released after both clients detached")
		}
	})

	evcA, cancelA := h.subscribeSSE(t, ctx, ws.ID, cidA)
	t.Cleanup(cancelA)
	evcB, cancelB := h.subscribeSSE(t, ctx, ws.ID, cidB)
	t.Cleanup(cancelB)

	h.waitForAttachedOn(t, ws, 2)

	const sessionID = "s-e2e-1"
	msg := message.Message{
		ID:        "m-1",
		SessionID: sessionID,
		Role:      message.Assistant,
		Parts:     []message.ContentPart{message.TextContent{Text: "hello multi-client"}},
	}
	ws.SendEvent(pubsub.Event[message.Message]{
		Type:    pubsub.CreatedEvent,
		Payload: msg,
	})

	pickCtx, pickCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pickCancel()
	gotA, okA := drainUntil(pickCtx, evcA, func(e pubsub.Event[proto.Message]) bool {
		return e.Payload.ID == "m-1"
	})
	require.True(t, okA, "client A must receive the MessageEvent")
	require.Equal(t, sessionID, gotA.Payload.SessionID)

	gotB, okB := drainUntil(pickCtx, evcB, func(e pubsub.Event[proto.Message]) bool {
		return e.Payload.ID == "m-1"
	})
	require.True(t, okB, "client B must receive the same MessageEvent")
	require.Equal(t, sessionID, gotB.Payload.SessionID)
}

// TestE2E_PermissionFlowCrossClient covers PLAN item 6 scenario 2:
// 工具-driven 权限 请求 是 granted 通过 客户端 一个; 客户端 B
// observes PermissionNotification; redundant grant 从 B
// returns "already resolved" indicator (resolved=false 从
// bool plumbing landed 在 item 3).
func TestE2E_PermissionFlowCrossClient(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	cidA := uuid.New().String()
	cidB := uuid.New().String()

	evcA, cancelA := h.subscribeSSE(t, ctx, h.workspace.ID, cidA)
	t.Cleanup(cancelA)
	evcB, cancelB := h.subscribeSSE(t, ctx, h.workspace.ID, cidB)
	t.Cleanup(cancelB)

	h.waitForAttached(t, 2)

	// Drive 权限 请求 从 goroutine simulating
	// 工具 路径. 请求 blocks until resolved; capture outcome.
	const sessionID = "s-perm"

	// 权限 prompts 是 会话-scoped: 只 clients viewing
	// 会话 observe 请求. Both clients view it here, which
	// 是 what makes cross-客户端 flow below possible 在 all.
	h.setCurrentSession(t, ctx, h.workspace.ID, cidA, sessionID)
	h.setCurrentSession(t, ctx, h.workspace.ID, cidB, sessionID)

	const toolCallID = "tc-1"
	type result struct {
		granted bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		granted, err := h.app.Permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolCallID:  toolCallID,
			ToolName:    "view",
			Description: "read a file",
			Action:      "read",
			Path:        h.workspace.Path,
		})
		done <- result{granted: granted, err: err}
	}()

	// Wait 为 PermissionRequest 到 arrive 在 客户端 一个's SSE
	// 流. We need 它的 ID 到 drive grant.
	// 10s instead 的 3s used elsewhere because 这 上下文
	// bounds three sequential waits, 不 one.
	pickCtx, pickCancel := context.WithTimeout(ctx, 10*time.Second)
	defer pickCancel()
	reqEv, ok := drainUntil(pickCtx, evcA, func(e pubsub.Event[proto.PermissionRequest]) bool {
		return e.Payload.ToolCallID == toolCallID
	})
	require.True(t, ok, "client A must receive the PermissionRequest")

	// 客户端 一个 grants — first grant must 报告 resolved=true.
	resolvedA := h.grantPermission(t, ctx, h.workspace.ID, proto.PermissionGrant{
		Permission: reqEv.Payload,
		Action:     proto.PermissionAllow,
	})
	require.True(t, resolvedA, "client A's grant must resolve the pending request")

	// blocked 请求 调用 must now return granted=true.
	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.True(t, r.granted)
	case <-pickCtx.Done():
		t.Fatal("permission Request did not return after grant")
	}

	// 客户端 B must receive PermissionNotification 用
	// Granted=true 为 same ToolCallID. initial neither-
	// granted-nor-denied 通知 published 在 start 的
	// 请求 also lands 在 B's 流 — match 在 granted one.
	notif, ok := drainUntil(pickCtx, evcB, func(e pubsub.Event[proto.PermissionNotification]) bool {
		return e.Payload.ToolCallID == toolCallID && e.Payload.Granted
	})
	require.True(t, ok, "client B must receive a granting PermissionNotification")
	require.True(t, notif.Payload.Granted)
	require.False(t, notif.Payload.Denied)

	// 一个 follow-up grant 从 客户端 B must 报告 resolved=false
	// ( 请求 was already resolved 通过 一个).
	resolvedB := h.grantPermission(t, ctx, h.workspace.ID, proto.PermissionGrant{
		Permission: reqEv.Payload,
		Action:     proto.PermissionAllow,
	})
	require.False(t, resolvedB, "client B's follow-up grant must report already resolved")
}

// TestE2E_PermissionScopedToViewingClients verifies 那 权限
// prompts 是 routed 只 到 clients viewing owning 会话:
// 请求 raised while 客户端 views another 会话 never reaches
// 那 客户端's 流, 和 switching into owning 会话
// re-delivers still-pending 请求. Viewers 的 same 会话
// both observe resolution 通知.
func TestE2E_PermissionScopedToViewingClients(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	cidA := uuid.New().String()
	cidB := uuid.New().String()

	evcA, cancelA := h.subscribeSSE(t, ctx, h.workspace.ID, cidA)
	t.Cleanup(cancelA)
	evcB, cancelB := h.subscribeSSE(t, ctx, h.workspace.ID, cidB)
	t.Cleanup(cancelB)

	h.waitForAttached(t, 2)

	const sessionA = "s-a"
	const sessionB = "s-b"
	h.setCurrentSession(t, ctx, h.workspace.ID, cidA, sessionA)
	h.setCurrentSession(t, ctx, h.workspace.ID, cidB, sessionB)

	const toolCallID = "tc-scoped"
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.app.Permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionA,
			ToolCallID:  toolCallID,
			ToolName:    "view",
			Description: "read a file",
			Action:      "read",
			Path:        h.workspace.Path,
		})
	}()
	// Unblock 请求 在 failure paths before waiting 在 done.
	t.Cleanup(func() { cancel(); <-done })

	// 客户端 viewing owning 会话 receives 请求.
	reqCtx, reqCancel := context.WithTimeout(ctx, 10*time.Second)
	defer reqCancel()
	reqEv, ok := drainUntil(reqCtx, evcA, func(e pubsub.Event[proto.PermissionRequest]) bool {
		return e.Payload.ToolCallID == toolCallID
	})
	require.True(t, ok, "client viewing the owning session must receive the request")

	// 客户端 viewing another 会话 must 不. 一个 500ms absence
	// 窗口 是 generous given 在-process transport.
	absCtx, absCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer absCancel()
	_, leaked := drainUntil(absCtx, evcB, func(e pubsub.Event[proto.PermissionRequest]) bool {
		return e.Payload.ToolCallID == toolCallID
	})
	require.False(t, leaked, "client viewing another session must not receive the request")

	// Switching 客户端 B into owning 会话 re-publishes
	// still-pending 请求.
	h.setCurrentSession(t, ctx, h.workspace.ID, cidB, sessionA)
	repub, ok := drainUntil(reqCtx, evcB, func(e pubsub.Event[proto.PermissionRequest]) bool {
		return e.Payload.ToolCallID == toolCallID
	})
	require.True(t, ok, "switching into the owning session must re-deliver the pending request")
	require.Equal(t, reqEv.Payload.ID, repub.Payload.ID, "re-delivery must carry the same pending request")

	// Both viewers observe resolution 通知.
	resolved := h.grantPermission(t, ctx, h.workspace.ID, proto.PermissionGrant{
		Permission: reqEv.Payload,
		Action:     proto.PermissionAllow,
	})
	require.True(t, resolved)
	<-done
	for i, evc := range []<-chan any{evcA, evcB} {
		_, ok := drainUntil(reqCtx, evc, func(e pubsub.Event[proto.PermissionNotification]) bool {
			return e.Payload.ToolCallID == toolCallID && e.Payload.Granted
		})
		require.True(t, ok, "viewer %d must receive the granting notification", i)
	}
}

// TestE2E_UnansweredPromptDoesNotBlockOtherSessions guards against an
// unanswered 提示词 在 one 会话 holding back another 会话's 提示词.
func TestE2E_UnansweredPromptDoesNotBlockOtherSessions(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	for i, session := range []string{"s-a", "s-b"} {
		cid := uuid.New().String()
		evc, cancelSSE := h.subscribeSSE(t, ctx, h.workspace.ID, cid)
		t.Cleanup(cancelSSE)
		h.waitForAttached(t, i+1)
		h.setCurrentSession(t, ctx, h.workspace.ID, cid, session)
		wg.Go(func() {
			_, _ = h.app.Permissions.Request(ctx, permission.CreatePermissionRequest{
				SessionID:  session,
				ToolCallID: "tc-" + session,
				ToolName:   "bash",
				Action:     "execute",
				Path:       h.workspace.Path,
			})
		})
		// 会话 一个's 提示词 是 left unanswered.
		_, ok := drainUntil(ctx, evc, func(e pubsub.Event[proto.PermissionRequest]) bool {
			return e.Payload.ToolCallID == "tc-"+session
		})
		require.True(t, ok, "session %s's prompt never reached its client", session)
	}
}

// TestE2E_AutoApproveSession guards endpoint `crush 运行` uses 到 grant
// 它的 own 会话's 权限 requests without 提示词.
func TestE2E_AutoApproveSession(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.httpSrv.URL+"/v1/workspaces/"+h.workspace.ID+"/sessions/s-run/auto-approve", nil)
	require.NoError(t, err)
	resp, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	granted, err := h.app.Permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:  "s-run",
		ToolCallID: "tc-run",
		ToolName:   "write",
		Action:     "write",
		Path:       h.workspace.Path,
	})
	require.NoError(t, err)
	require.True(t, granted)
}

// TestE2E_KillingClientASSEDoesNotBreakClientB covers PLAN item 6
// scenario 3: terminating 客户端 一个's SSE 流 does 不 affect
// 客户端 B's 流; 客户端 B continues 到 receive events.
func TestE2E_KillingClientASSEDoesNotBreakClientB(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	ctxB, cancelB := context.WithCancel(t.Context())
	t.Cleanup(cancelB)
	ctxA, cancelA := context.WithCancel(t.Context())

	cidA := uuid.New().String()
	cidB := uuid.New().String()

	_, killA := h.subscribeSSE(t, ctxA, h.workspace.ID, cidA)
	t.Cleanup(killA)
	evcB, killB := h.subscribeSSE(t, ctxB, h.workspace.ID, cidB)
	t.Cleanup(killB)

	h.waitForAttached(t, 2)

	// Kill 一个's 流. 服务端's deferred DetachClient should
	// drop 一个's claim, leaving B 作为 sole attached 客户端.
	cancelA()
	killA()

	require.Eventually(t, func() bool {
		return backend.WorkspaceLiveStreamCountForTest(h.workspace) == 1
	}, 3*time.Second, 10*time.Millisecond,
		"expected client A's stream to drop the attached count to 1")

	// 工作区 must still exist (B 是 holding it open) 和
	// shutdown callback must 不 have fired yet.
	_, err := h.backend.GetWorkspace(h.workspace.ID)
	require.NoError(t, err, "workspace must still exist while B is attached")
	require.False(t, h.shutdownHit.Load(),
		"shutdown callback must not fire while B is still attached")

	// 发布 fresh 事件; B must still receive it.
	const sessionID = "s-after-a-died"
	msg := message.Message{
		ID:        "m-after",
		SessionID: sessionID,
		Role:      message.Assistant,
		Parts:     []message.ContentPart{message.TextContent{Text: "still alive"}},
	}
	h.app.SendEvent(pubsub.Event[message.Message]{
		Type:    pubsub.CreatedEvent,
		Payload: msg,
	})

	pickCtx, pickCancel := context.WithTimeout(ctxB, 3*time.Second)
	defer pickCancel()
	got, ok := drainUntil(pickCtx, evcB, func(e pubsub.Event[proto.Message]) bool {
		return e.Payload.ID == "m-after"
	})
	require.True(t, ok, "client B must still receive events after A's stream is killed")
	require.Equal(t, sessionID, got.Payload.SessionID)
}

// TestE2E_ShutdownCallbackFiresWhenLastClientLeaves covers PLAN
// item 6 scenario 4: once both clients disconnect, the backend
// runs 它的 "last 工作区 removed -> 服务端 shutdown" 路径 — now
// after 空闲-linger 窗口 rather than immediately.
func TestE2E_ShutdownCallbackFiresWhenLastClientLeaves(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	// Shorten both lifecycle windows 所以 测试 doesn't wait out
	// production values. Both stay non-zero, 所以 teardown still travels
	// real 路径: detach grace, then 空闲 linger.
	h.backend.SetDetachGrace(100 * time.Millisecond)
	h.backend.SetIdleShutdownDelay(200 * time.Millisecond)

	ctxA, cancelA := context.WithCancel(t.Context())
	ctxB, cancelB := context.WithCancel(t.Context())
	t.Cleanup(cancelA)
	t.Cleanup(cancelB)

	cidA := uuid.New().String()
	cidB := uuid.New().String()
	_, killA := h.subscribeSSE(t, ctxA, h.workspace.ID, cidA)
	t.Cleanup(killA)
	_, killB := h.subscribeSSE(t, ctxB, h.workspace.ID, cidB)
	t.Cleanup(killB)

	h.waitForAttached(t, 2)
	require.False(t, h.shutdownHit.Load(), "shutdown must not fire while clients are attached")

	cancelA()
	killA()
	require.Eventually(t, func() bool {
		return backend.WorkspaceLiveStreamCountForTest(h.workspace) == 1
	}, 3*time.Second, 10*time.Millisecond)
	require.False(t, h.shutdownHit.Load(),
		"shutdown must not fire after only one client disconnects")

	cancelB()
	killB()
	require.Eventually(t, h.shutdownHit.Load,
		3*time.Second, 10*time.Millisecond,
		"shutdown callback must fire once the last client disconnects "+
			"(after the detach grace and the idle linger)")

	// 工作区 must 是 gone 从 index.
	_, err := h.backend.GetWorkspace(h.workspace.ID)
	require.ErrorIs(t, err, backend.ErrWorkspaceNotFound)

	// Subsequent GETs against now-defunct 工作区 return
	// 404, confirming the http surface still reflects the teardown.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		h.httpSrv.URL+"/v1/workspaces/"+h.workspace.ID, nil)
	require.NoError(t, err)
	r, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()
	require.Equal(t, http.StatusNotFound, r.StatusCode)
}
