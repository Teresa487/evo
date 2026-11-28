package backend

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// sessionRunCoordinator 是 agent.Coordinator whose runs 是 contexts
// registered per 会话. 取消 ends 会话's 运行 way real
// coordinator cancels 它的 活跃 请求, 所以 测试 can park 运行 在
// 权限 提示词 和 see whether anything cancels it.
type sessionRunCoordinator struct {
	*blockingCoordinator

	mu   sync.Mutex
	runs map[string]context.CancelFunc
}

func (c *sessionRunCoordinator) Cancel(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cancel, ok := c.runs[sessionID]; ok {
		cancel()
	}
}

// startRun parks 运行 的 sessionID 在 权限 提示词, which 是
// where 运行 waits while 它的 工具 needs 用户's answer. 它 returns
// 运行's 上下文 和 渠道 那 yields 提示词's 错误 once
// 运行 stops waiting.
func (c *sessionRunCoordinator) startRun(t *testing.T, ws *Workspace, sessionID string) (context.Context, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	c.mu.Lock()
	c.runs[sessionID] = cancel
	c.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := ws.Permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:  sessionID,
			ToolCallID: "tc-" + sessionID,
			ToolName:   "bash",
			Action:     "execute",
			Path:       ws.Path,
		})
		done <- err
	}()
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(ws.Permissions.ActiveRequests(), func(req permission.PermissionRequest) bool {
			return req.SessionID == sessionID
		})
	}, 10*time.Second, 5*time.Millisecond, "the run of %s must be waiting on its prompt", sessionID)
	return ctx, done
}

// newRunWorkspace installs 工作区 用 real 权限 service 和
// sessionRunCoordinator. Its teardown 是 stubbed out 和 cancels
// nothing, 所以 cancelled 运行 can 只 have been cancelled 通过 客户端
// leaving.
func newRunWorkspace(t *testing.T, b *Backend) (*Workspace, *sessionRunCoordinator) {
	t.Helper()
	a := app.NewForTest(context.Background())
	t.Cleanup(a.ShutdownForTest)
	coord := &sessionRunCoordinator{
		blockingCoordinator: newBlockingCoordinator(),
		runs:                make(map[string]context.CancelFunc),
	}
	a.AgentCoordinator = coord
	ws := &Workspace{ID: uuid.New().String(), Path: t.TempDir(), App: a}
	SetWorkspaceShutdownFnForTest(ws, func() {})
	InsertWorkspaceForTest(b, ws)
	return ws, coord
}

// attachViewing attaches clientID 到 ws 用 live 流 和 reports it
// 作为 viewing sessionID, 作为 TUI does once it opens 会话.
func attachViewing(t *testing.T, b *Backend, ws *Workspace, clientID, sessionID string) {
	t.Helper()
	require.NoError(t, b.AttachClient(ws.ID, clientID))
	require.NoError(t, b.SetCurrentSession(t.Context(), ws.ID, clientID, sessionID))
}

// awaitCancelled waits 为 parked 运行 到 stop waiting 在 它的 提示词 和
// checks 那 it stopped because it was cancelled.
func awaitCancelled(t *testing.T, done <-chan error, msg string) {
	t.Helper()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled, msg)
	case <-time.After(10 * time.Second):
		t.Fatal(msg)
	}
}

// TestClientDeparture_CancelsRunNobodyViews 是 regression 为 客户端
// 那 quits 或 是 killed while 它的 运行 waits 在 权限 提示词 和
// another 客户端 keeps 工作区 alive. Prompts 只 reach clients
// viewing their 会话, 所以 nobody was left 到 answer 那 one: 运行
// waited headless forever, 和 since 只 one 提示词 是 shown 在 time,
// remaining 客户端's own prompts never appeared either. However
// departing 客户端's claim ends, 运行 的 会话 it was viewing
// must 是 cancelled, 作为 quitting evo does 在 local mode.
func TestClientDeparture_CancelsRunNobodyViews(t *testing.T) {
	t.Parallel()

	departures := map[string]func(t *testing.T, b *Backend, ws *Workspace, clientID string){
		"retired": func(t *testing.T, b *Backend, _ *Workspace, clientID string) {
			require.NoError(t, b.RetireClient(clientID))
		},
		"detach grace expired": func(t *testing.T, b *Backend, ws *Workspace, clientID string) {
			b.SetDetachGrace(50 * time.Millisecond)
			b.DetachClient(ws.ID, clientID)
		},
		"detach grace disabled": func(t *testing.T, b *Backend, ws *Workspace, clientID string) {
			b.SetDetachGrace(0)
			b.DetachClient(ws.ID, clientID)
		},
		"released then detached": func(t *testing.T, b *Backend, ws *Workspace, clientID string) {
			b.SetDetachGrace(time.Hour)
			require.NoError(t, b.DeleteWorkspace(ws.ID, clientID))
			b.DetachClient(ws.ID, clientID)
		},
		"detached then released": func(t *testing.T, b *Backend, ws *Workspace, clientID string) {
			b.SetDetachGrace(time.Hour)
			b.DetachClient(ws.ID, clientID)
			require.NoError(t, b.DeleteWorkspace(ws.ID, clientID))
		},
	}
	for name, depart := range departures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, _ := newTestBackend(t)
			ws, coord := newRunWorkspace(t, b)

			quitter, stayer := newClientID(t), newClientID(t)
			attachViewing(t, b, ws, quitter, "s-quitter")
			attachViewing(t, b, ws, stayer, "s-stayer")
			_, done := coord.startRun(t, ws, "s-quitter")

			depart(t, b, ws, quitter)

			awaitCancelled(t, done,
				"the run of a session no remaining client views must be cancelled when its viewer leaves")
			_, err := b.GetWorkspace(ws.ID)
			require.NoError(t, err, "the remaining client keeps the workspace")

			// remaining 客户端's prompts 是 no longer held back.
			coord.startRun(t, ws, "s-stayer")
		})
	}
}

// TestClientDeparture_KeepsRunAnotherClientViews 是 other side: 运行
// 是 只 cancelled once nobody 是 left who could answer it. 一个 客户端
// still viewing 会话 keeps 运行 going, even while 那 客户端
// waits out 它的 detach grace, since it 是 expected back.
func TestClientDeparture_KeepsRunAnotherClientViews(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend(t)
	b.SetDetachGrace(time.Hour)
	ws, coord := newRunWorkspace(t, b)

	quitter, watcher, bystander := newClientID(t), newClientID(t), newClientID(t)
	attachViewing(t, b, ws, quitter, "s-shared")
	attachViewing(t, b, ws, watcher, "s-shared")
	require.NoError(t, b.AttachClient(ws.ID, bystander))
	run, done := coord.startRun(t, ws, "s-shared")

	// watcher's 流 drops; it may still reconnect.
	b.DetachClient(ws.ID, watcher)
	require.NoError(t, b.RetireClient(quitter))
	require.NoError(t, run.Err(), "a run another client still views must keep going")

	require.NoError(t, b.RetireClient(watcher))
	awaitCancelled(t, done, "the run must be cancelled once its last viewer leaves")
}

// TestClientDeparture_KeepsNonInteractiveRun 是 regression 为 crush
// 运行 continuing (-C 或 -s) 会话 TUI also has open. crush 运行
// reports 会话 it drives 作为 current once it knows it, 所以 当
// TUI quits 运行 still has viewer 和 keeps going. 它 是 cancelled
// 只 once crush 运行 itself leaves.
func TestClientDeparture_KeepsNonInteractiveRun(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend(t)
	ws, coord := newRunWorkspace(t, b)

	runner, tui := newClientID(t), newClientID(t)
	attachViewing(t, b, ws, tui, "s-shared")
	attachViewing(t, b, ws, runner, "s-shared")
	run, done := coord.startRun(t, ws, "s-shared")

	require.NoError(t, b.RetireClient(tui))
	require.NoError(t, run.Err(), "a run driven by a connected evo run must keep going")

	require.NoError(t, b.RetireClient(runner))
	awaitCancelled(t, done, "the run must be cancelled once evo run leaves too")
}
