package server

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestSessionScopedEventID verifies 那 exactly four interactive
// 提示词 事件 types 是 treated 作为 会话-scoped, 和 那 their
// 会话 IDs 是 extracted 为 delivery filter.
func TestSessionScopedEventID(t *testing.T) {
	t.Parallel()

	scoped := []struct {
		name string
		ev   any
		id   string
	}{
		{"permission request", pubsub.Event[permission.PermissionRequest]{Payload: permission.PermissionRequest{SessionID: "s1"}}, "s1"},
		{"permission notification", pubsub.Event[permission.PermissionNotification]{Payload: permission.PermissionNotification{SessionID: "s2"}}, "s2"},
		{"question request", pubsub.Event[question.Request]{Payload: question.Request{SessionID: "s3"}}, "s3"},
		{"question notification", pubsub.Event[question.Notification]{Payload: question.Notification{SessionID: "s4"}}, "s4"},
		{"tool progress", pubsub.Event[tools.ToolProgress]{Payload: tools.ToolProgress{SessionID: "s5", ToolCallID: "c1"}}, "s5"},
	}
	for _, tc := range scoped {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, ok := sessionScopedEventID(tc.ev)
			require.True(t, ok)
			require.Equal(t, tc.id, id)
		})
	}

	unscoped := []struct {
		name string
		ev   any
	}{
		{"session event", pubsub.Event[session.Session]{Payload: session.Session{ID: "s1"}}},
		{"config changed", pubsub.Event[struct{}]{}},
		{"host theme", pubsub.Event[proto.HostThemeState]{Payload: proto.HostThemeState{WorkspaceID: "w1", Theme: &proto.HostTheme{Name: "t"}}}},
		{"string", "not an event"},
		{"empty permission request", pubsub.Event[permission.PermissionRequest]{}},
		{"empty permission notification", pubsub.Event[permission.PermissionNotification]{}},
		{"empty question request", pubsub.Event[question.Request]{}},
		{"empty question notification", pubsub.Event[question.Notification]{}},
		{"empty tool progress", pubsub.Event[tools.ToolProgress]{}},
	}
	for _, tc := range unscoped {
		t.Run("unscoped/"+tc.name, func(t *testing.T) {
			t.Parallel()
			_, ok := sessionScopedEventID(tc.ev)
			require.False(t, ok)
		})
	}
}

// deliverFixture builds controller 用 synthetic 工作区 和
// two attached clients, one viewing 会话 S1 和 one viewing S2.
func deliverFixture(t *testing.T) (*controllerV1, *backend.Workspace, string, string) {
	t.Helper()
	c := newTestController()
	ws := installSyntheticWorkspace(t, c)

	cidA := uuid.New().String()
	cidB := uuid.New().String()
	require.NoError(t, c.backend.AttachClient(ws.ID, cidA))
	require.NoError(t, c.backend.AttachClient(ws.ID, cidB))
	t.Cleanup(func() {
		c.backend.DetachClient(ws.ID, cidA)
		c.backend.DetachClient(ws.ID, cidB)
	})
	require.NoError(t, c.backend.SetCurrentSession(t.Context(), ws.ID, cidA, "S1"))
	require.NoError(t, c.backend.SetCurrentSession(t.Context(), ws.ID, cidB, "S2"))
	return c, ws, cidA, cidB
}

// TestDeliverToClient covers per-客户端 SSE delivery filter:
// 会话-scoped 提示词 events 只 reach clients viewing owning
// 会话; everything else passes through untouched.
func TestDeliverToClient(t *testing.T) {
	t.Parallel()

	permReq := pubsub.Event[permission.PermissionRequest]{Payload: permission.PermissionRequest{SessionID: "S1"}}
	permNotif := pubsub.Event[permission.PermissionNotification]{Payload: permission.PermissionNotification{SessionID: "S1", Granted: true}}
	qReq := pubsub.Event[question.Request]{Payload: question.Request{SessionID: "S1"}}
	qNotif := pubsub.Event[question.Notification]{Payload: question.Notification{SessionID: "S1"}}
	progressEv := pubsub.Event[tools.ToolProgress]{Payload: tools.ToolProgress{SessionID: "S1", ToolCallID: "c1", Output: "out"}}
	sessionEv := pubsub.Event[session.Session]{Payload: session.Session{ID: "S1"}}

	t.Run("viewer receives, non-viewer does not", func(t *testing.T) {
		t.Parallel()
		c, ws, cidA, cidB := deliverFixture(t)
		for _, ev := range []any{permReq, permNotif, qReq, qNotif, progressEv} {
			require.True(t, c.deliverToClient(t.Context(), ws.ID, cidA, ev), "%T must reach the viewer", ev)
			require.False(t, c.deliverToClient(t.Context(), ws.ID, cidB, ev), "%T must not reach the non-viewer", ev)
		}
	})

	t.Run("two clients viewing the same session both receive", func(t *testing.T) {
		t.Parallel()
		c, ws, cidA, cidB := deliverFixture(t)
		require.NoError(t, c.backend.SetCurrentSession(t.Context(), ws.ID, cidB, "S1"))
		require.True(t, c.deliverToClient(t.Context(), ws.ID, cidA, permReq))
		require.True(t, c.deliverToClient(t.Context(), ws.ID, cidB, permReq))
	})

	t.Run("client viewing no session receives nothing scoped", func(t *testing.T) {
		t.Parallel()
		c, ws, cidA, _ := deliverFixture(t)
		require.NoError(t, c.backend.SetCurrentSession(t.Context(), ws.ID, cidA, ""))
		for _, ev := range []any{permReq, permNotif, qReq, qNotif} {
			require.False(t, c.deliverToClient(t.Context(), ws.ID, cidA, ev), "%T must not reach a session-less client", ev)
		}
	})

	t.Run("client that never reported a session fails open", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		ws := installSyntheticWorkspace(t, c)
		cid := uuid.New().String()
		require.NoError(t, c.backend.AttachClient(ws.ID, cid))
		t.Cleanup(func() { c.backend.DetachClient(ws.ID, cid) })
		// Intentionally no SetCurrentSession: clients 那 predate
		// 会话 reporting must still receive their prompts.
		for _, ev := range []any{permReq, permNotif, qReq, qNotif} {
			require.True(t, c.deliverToClient(t.Context(), ws.ID, cid, ev), "%T must reach a never-reporting client", ev)
		}
	})

	t.Run("unscoped events always pass", func(t *testing.T) {
		t.Parallel()
		c, ws, cidA, cidB := deliverFixture(t)
		require.NoError(t, c.backend.SetCurrentSession(t.Context(), ws.ID, cidA, ""))
		require.True(t, c.deliverToClient(t.Context(), ws.ID, cidA, sessionEv))
		require.True(t, c.deliverToClient(t.Context(), ws.ID, cidB, sessionEv))
	})

	t.Run("prompt events with an empty session ID broadcast", func(t *testing.T) {
		t.Parallel()
		c, ws, cidA, cidB := deliverFixture(t)
		emptyReq := pubsub.Event[permission.PermissionRequest]{}
		require.True(t, c.deliverToClient(t.Context(), ws.ID, cidA, emptyReq))
		require.True(t, c.deliverToClient(t.Context(), ws.ID, cidB, emptyReq))
	})
}

// TestDeliverToClient_SubSessionResolvesToParent verifies 那
// 提示词 raised 通过 子代理 会话 ( task 会话 用 parent)
// 是 delivered 到 clients viewing top-level parent 会话, 和
// 那 unresolvable 会话 fails open rather than stranding
// 提示词.
func TestDeliverToClient_SubSessionResolvesToParent(t *testing.T) {
	t.Parallel()
	c, ws, cidA, cidB := deliverFixture(t)

	// Equip 工作区 用 real 会话 service 所以 parent
	// chain 是 resolvable.
	a := app.NewForTest(t.Context())
	t.Cleanup(a.ShutdownForTest)
	dataDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	a.Sessions = session.NewService(db.New(conn), conn)
	ws.App = a

	parent, err := a.Sessions.Create(t.Context(), "parent")
	require.NoError(t, err)
	child, err := a.Sessions.CreateTaskSession(t.Context(), "child-1", parent.ID, "child")
	require.NoError(t, err)

	require.NoError(t, c.backend.SetCurrentSession(t.Context(), ws.ID, cidA, parent.ID))

	childReq := pubsub.Event[permission.PermissionRequest]{Payload: permission.PermissionRequest{SessionID: child.ID}}
	require.True(t, c.deliverToClient(t.Context(), ws.ID, cidA, childReq),
		"sub-session prompt must reach the parent session's viewer")
	require.False(t, c.deliverToClient(t.Context(), ws.ID, cidB, childReq),
		"sub-session prompt must not reach another session's viewer")

	// 一个 会话 那 cannot 是 resolved (不 在 DB) fails open:
	// better 到 broadcast than 到 strand 提示词 nobody can answer.
	ghostReq := pubsub.Event[permission.PermissionRequest]{Payload: permission.PermissionRequest{SessionID: "ghost"}}
	require.True(t, c.deliverToClient(t.Context(), ws.ID, cidB, ghostReq))
}
