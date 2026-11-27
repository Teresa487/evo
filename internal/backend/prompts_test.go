package backend

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// newPromptsTestBackend builds backend 用 工作区 backed 通过
// minimal 在-process app (live 事件 总线, real 权限 和
// question services, no agent machinery).
func newPromptsTestBackend(t *testing.T) (*Backend, *Workspace, *app.App) {
	t.Helper()
	ctx := context.Background()
	a := app.NewForTest(ctx)
	t.Cleanup(a.ShutdownForTest)
	b := New(ctx, nil, nil)
	ws := &Workspace{ID: uuid.New().String(), Path: t.TempDir(), App: a}
	InsertWorkspaceForTest(b, ws)
	return b, ws, a
}

// drainQuiet discards events 为 given 窗口. 它 absorbs
// original 提示词 发布 那 fans 在 asynchronously after 请求
// becomes pending: fan-在 goroutines subscribe 到 service
// brokers 在 background, 所以 under load original 发布 may
// arrive late 或 是 dropped entirely. Either way, after quiet
// 窗口 只 re-delivery triggered 通过 SetCurrentSession can
// arrive. Re-deliveries 发布 straight into 工作区 总线,
// which tests subscribe 到 synchronously, 所以 they race nothing.
func drainQuiet(events <-chan pubsub.Event[tea.Msg], window time.Duration) {
	timer := time.NewTimer(window)
	defer timer.Stop()
	for {
		select {
		case <-events:
		case <-timer.C:
			return
		}
	}
}

func TestClientCurrentSession(t *testing.T) {
	t.Parallel()
	b, ws, _ := newPromptsTestBackend(t)

	current, reported := b.ClientCurrentSession("no-such-workspace", "cid")
	require.Empty(t, current, "unknown workspace")
	require.False(t, reported, "unknown workspace")

	cid := uuid.New().String()
	current, reported = b.ClientCurrentSession(ws.ID, cid)
	require.Empty(t, current, "unknown client")
	require.False(t, reported, "unknown client")

	require.NoError(t, b.AttachClient(ws.ID, cid))
	current, reported = b.ClientCurrentSession(ws.ID, cid)
	require.Empty(t, current, "no session reported yet")
	require.False(t, reported, "no session reported yet")

	require.NoError(t, b.SetCurrentSession(t.Context(), ws.ID, cid, "S1"))
	current, reported = b.ClientCurrentSession(ws.ID, cid)
	require.Equal(t, "S1", current)
	require.True(t, reported)

	require.NoError(t, b.SetCurrentSession(t.Context(), ws.ID, cid, ""))
	current, reported = b.ClientCurrentSession(ws.ID, cid)
	require.Empty(t, current, "cleared session")
	require.True(t, reported, "clearing the selection is still a report")
}

func TestRootSessionID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New(ctx, nil, nil)

	t.Run("workspace without app treats sessions as top-level", func(t *testing.T) {
		t.Parallel()
		ws := &Workspace{ID: uuid.New().String(), Path: t.TempDir()}
		InsertWorkspaceForTest(b, ws)

		root, err := b.RootSessionID(ctx, ws.ID, "anything")
		require.NoError(t, err)
		require.Equal(t, "anything", root)
	})

	t.Run("unknown workspace returns input and error", func(t *testing.T) {
		t.Parallel()
		root, err := b.RootSessionID(ctx, "no-such-workspace", "S1")
		require.Error(t, err)
		require.Equal(t, "S1", root)
	})

	t.Run("resolves the parent chain", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		conn, err := db.Connect(ctx, dataDir)
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		sessions := session.NewService(db.New(conn), conn)

		ws := &Workspace{ID: uuid.New().String(), Path: t.TempDir(), App: &app.App{Sessions: sessions}}
		InsertWorkspaceForTest(b, ws)

		top, err := sessions.Create(ctx, "top")
		require.NoError(t, err)
		child, err := sessions.CreateTaskSession(ctx, "child-1", top.ID, "child")
		require.NoError(t, err)
		grandchild, err := sessions.CreateTaskSession(ctx, "grand-1", child.ID, "grandchild")
		require.NoError(t, err)

		root, err := b.RootSessionID(ctx, ws.ID, top.ID)
		require.NoError(t, err)
		require.Equal(t, top.ID, root, "top-level session resolves to itself")

		root, err = b.RootSessionID(ctx, ws.ID, child.ID)
		require.NoError(t, err)
		require.Equal(t, top.ID, root, "child resolves to parent")

		root, err = b.RootSessionID(ctx, ws.ID, grandchild.ID)
		require.NoError(t, err)
		require.Equal(t, top.ID, root, "grandchild resolves to the top-level session")

		root, err = b.RootSessionID(ctx, ws.ID, "ghost")
		require.Error(t, err, "unknown session must surface the lookup error")
		require.Equal(t, "ghost", root, "input is returned alongside the error")

		_, err = b.RootSessionID(ctx, ws.ID, "ghost")
		require.Error(t, err, "failed lookups must not be cached")
	})

	t.Run("memoizes successful resolutions", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		conn, err := db.Connect(ctx, dataDir)
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		sessions := session.NewService(db.New(conn), conn)

		ws := &Workspace{ID: uuid.New().String(), Path: t.TempDir(), App: &app.App{Sessions: sessions}}
		InsertWorkspaceForTest(b, ws)

		top, err := sessions.Create(ctx, "top")
		require.NoError(t, err)
		child, err := sessions.CreateTaskSession(ctx, "child-1", top.ID, "child")
		require.NoError(t, err)

		root, err := b.RootSessionID(ctx, ws.ID, child.ID)
		require.NoError(t, err)
		require.Equal(t, top.ID, root)

		// With child 行 gone, second resolution would fail 如果
		// it hit DB; memoized answer 是 served instead.
		require.NoError(t, sessions.Delete(ctx, child.ID))
		root, err = b.RootSessionID(ctx, ws.ID, child.ID)
		require.NoError(t, err)
		require.Equal(t, top.ID, root)
	})
}

// TestSetCurrentSession_RepublishesPendingPermission verifies 那
// 客户端 switching into 会话 用 pending 权限 请求
// re-receives 请求 在 工作区 事件 流, while
// switching 到 unrelated 会话 delivers nothing.
func TestSetCurrentSession_RepublishesPendingPermission(t *testing.T) {
	t.Parallel()
	b, ws, a := newPromptsTestBackend(t)
	ctx := context.Background()

	events, err := b.SubscribeEvents(ctx, ws.ID)
	require.NoError(t, err)

	reqCtx, cancelReq := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = a.Permissions.Request(reqCtx, permission.CreatePermissionRequest{
			SessionID:  "s-1",
			ToolCallID: "tc-1",
			ToolName:   "bash",
			Action:     "execute",
			Path:       ws.Path,
		})
	}()
	defer func() { cancelReq(); <-done }()

	var active permission.PermissionRequest
	require.Eventually(t, func() bool {
		pending := a.Permissions.ActiveRequests()
		if len(pending) == 0 {
			return false
		}
		active = pending[0]
		return true
	}, 10*time.Second, 5*time.Millisecond, "request must be pending")
	require.Equal(t, "tc-1", active.ToolCallID)

	// Absorb original 发布, then attach 客户端.
	drainQuiet(events, 300*time.Millisecond)

	cid := uuid.New().String()
	require.NoError(t, b.AttachClient(ws.ID, cid))

	// Switching 到 unrelated 会话 must 不 re-发布.
	require.NoError(t, b.SetCurrentSession(ctx, ws.ID, cid, "s-2"))
	select {
	case ev := <-events:
		t.Fatalf("unexpected event for unrelated session: %v", ev)
	case <-time.After(200 * time.Millisecond):
	}

	// Switching 到 owning 会话 must re-发布 请求.
	require.NoError(t, b.SetCurrentSession(ctx, ws.ID, cid, "s-1"))
	select {
	case ev := <-events:
		reqEv, ok := ev.Payload.(pubsub.Event[permission.PermissionRequest])
		require.True(t, ok, "expected permission request event, got %T", ev.Payload)
		require.Equal(t, "s-1", reqEv.Payload.SessionID)
		require.Equal(t, "tc-1", reqEv.Payload.ToolCallID)
		require.Equal(t, active.ID, reqEv.Payload.ID, "same pending request")
	case <-time.After(10 * time.Second):
		t.Fatal("expected re-published permission request")
	}
}

// TestSetCurrentSession_RepublishesPendingQuestion verifies the same
// re-delivery 为 pending question batch.
func TestSetCurrentSession_RepublishesPendingQuestion(t *testing.T) {
	t.Parallel()
	b, ws, a := newPromptsTestBackend(t)
	ctx := context.Background()

	events, err := b.SubscribeEvents(ctx, ws.ID)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = a.Questions.Ask(ctx, question.Request{
			SessionID: "s-1",
			Questions: []question.Question{{
				Type:        question.TypeFreeText,
				Text:        "Proceed?",
				Description: "Tell me what you think",
			}},
		})
	}()
	defer func() { a.Questions.Cancel(""); <-done }()

	require.Eventually(t, func() bool {
		return len(a.Questions.Pending()) > 0
	}, 10*time.Second, 5*time.Millisecond, "question must be pending")

	// Absorb original 发布, then attach 客户端.
	drainQuiet(events, 300*time.Millisecond)

	cid := uuid.New().String()
	require.NoError(t, b.AttachClient(ws.ID, cid))
	require.NoError(t, b.SetCurrentSession(ctx, ws.ID, cid, "s-1"))

	select {
	case ev := <-events:
		reqEv, ok := ev.Payload.(pubsub.Event[question.Request])
		require.True(t, ok, "expected question request event, got %T", ev.Payload)
		require.Equal(t, "s-1", reqEv.Payload.SessionID)
	case <-time.After(10 * time.Second):
		t.Fatal("expected re-published question request")
	}
}

// TestCreateWorkspace_PermissionPromptsWaitPerTopLevelSession guards
// against one unanswered 权限 提示词 blocking every other 会话's;
// sub-agent's 提示词 must still wait 为 它的 parent 会话's.
func TestCreateWorkspace_PermissionPromptsWaitPerTopLevelSession(t *testing.T) {
	xdgIsolated(t)
	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })
	ws, _, err := b.CreateWorkspace(protoWS(t.TempDir(), t.TempDir(), uuid.New().String()))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	top, err := ws.Sessions.Create(ctx, "top")
	require.NoError(t, err)
	child, err := ws.Sessions.CreateTaskSession(ctx, "child-1", top.ID, "child")
	require.NoError(t, err)
	other, err := ws.Sessions.Create(ctx, "other")
	require.NoError(t, err)

	requests := ws.Permissions.Subscribe(ctx)
	request := func(sessionID string) {
		wg.Go(func() {
			_, _ = ws.Permissions.Request(ctx, permission.CreatePermissionRequest{
				SessionID: sessionID, ToolCallID: "tc-" + sessionID,
				ToolName: "bash", Action: "execute", Path: ws.Path,
			})
		})
	}
	next := func(wait time.Duration) (permission.PermissionRequest, bool) {
		select {
		case ev := <-requests:
			return ev.Payload, true
		case <-time.After(wait):
			return permission.PermissionRequest{}, false
		}
	}

	request(top.ID)
	reqTop, ok := next(10 * time.Second)
	require.True(t, ok)
	require.Equal(t, top.ID, reqTop.SessionID)
	request(child.ID)
	request(other.ID)
	reqOther, ok := next(10 * time.Second)
	require.True(t, ok, "another session's prompt waited for an unanswered one")
	require.Equal(t, other.ID, reqOther.SessionID, "a sub-agent's prompt must wait for its parent's")
	_, ok = next(200 * time.Millisecond)
	require.False(t, ok, "a sub-agent's prompt must wait for its parent's")

	require.True(t, ws.Permissions.Grant(reqTop))
	reqChild, ok := next(10 * time.Second)
	require.True(t, ok)
	require.Equal(t, child.ID, reqChild.SessionID)
}
