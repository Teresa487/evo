package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// runCoordinator 是 configurable agent.Coordinator stub 为
// 取消/drop tests. 运行 blocks until either ctx 是 canceled (所以 it
// can observe explicit 取消 paths) 或 release fires (所以 测试
// can let "still running" turn finish 在 它的 own). most recent
// ctx 和 错误 returned 到 caller 是 recorded 为
// assertions.
type runCoordinator struct {
	release  chan struct{}
	returnFn func(ctx context.Context) error

	mu         sync.Mutex
	gotCtx     context.Context
	ranCount   atomic.Int32
	entered    chan struct{} // closed exactly once when Run is first entered.
	enteredOne sync.Once

	setMainAgentErr  error
	lastMainAgentSet atomic.Value
	busy             bool
}

func newRunCoordinator(returnFn func(ctx context.Context) error) *runCoordinator {
	return &runCoordinator{
		release:  make(chan struct{}),
		returnFn: returnFn,
		entered:  make(chan struct{}),
	}
}

func (s *runCoordinator) Run(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	s.mu.Lock()
	s.gotCtx = ctx
	s.mu.Unlock()
	s.ranCount.Add(1)
	s.enteredOne.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
		// Only fires 如果 运行 是 actually cancellable.
	}
	return nil, s.returnFn(ctx)
}

func (s *runCoordinator) RunAccepted(ctx context.Context, accept *agent.AcceptedRun, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	return s.Run(ctx, sessionID, prompt, attachments...)
}

func (s *runCoordinator) BeginAccepted(sessionID string) *agent.AcceptedRun {
	return nil
}
func (s *runCoordinator) Cancel(string) {}
func (s *runCoordinator) CancelAll()    {}
func (s *runCoordinator) IsBusy() bool  { return s.busy }
func (s *runCoordinator) IsSessionBusy(string) bool {
	return false
}
func (s *runCoordinator) QueuedPrompts(string) int          { return 0 }
func (s *runCoordinator) QueuedPromptsList(string) []string { return nil }
func (s *runCoordinator) ClearQueue(string)                 {}
func (s *runCoordinator) AddNote(string, string)            {}
func (s *runCoordinator) Summarize(context.Context, string) error {
	return nil
}
func (s *runCoordinator) Model() agent.Model                            { return agent.Model{} }
func (s *runCoordinator) UpdateModels(context.Context) error            { return nil }
func (s *runCoordinator) GenerateTitle(context.Context, string, string) {}
func (s *runCoordinator) SetHostContext(proto.HostContext)              {}
func (s *runCoordinator) HostContext() proto.HostContext                { return proto.HostContext{} }

func (s *runCoordinator) SetMainAgent(agentName string) error {
	s.lastMainAgentSet.Store(agentName)
	return s.setMainAgentErr
}

func (s *runCoordinator) MainAgent() string {
	name, _ := s.lastMainAgentSet.Load().(string)
	return name
}

func (s *runCoordinator) capturedCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gotCtx
}

// buildAgentWorkspace returns controller wired 到 backend whose
// single 工作区 exposes given coordinator. 工作区
// shutdown 钩子 是 overridden 到 avoid driving real [app.App]
// through teardown 当 测试 exits.
func buildAgentWorkspace(t *testing.T, coord agent.Coordinator) (*controllerV1, string) {
	t.Helper()
	b := backend.New(context.Background(), nil, nil)
	a := &app.App{AgentCoordinator: coord}

	ws := &backend.Workspace{
		ID:   uuid.New().String(),
		Path: t.TempDir(),
		App:  a,
	}
	backend.InsertWorkspaceForTest(b, ws)
	backend.SetWorkspaceShutdownFnForTest(ws, func() {})

	s := &Server{backend: b}
	return &controllerV1{backend: b, server: s}, ws.ID
}

func postAgent(t *testing.T, c *controllerV1, ctx context.Context, wsID, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(proto.AgentMessage{SessionID: sessionID, Prompt: "hi"})
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/workspaces/"+wsID+"/agent", bytes.NewReader(body))
	req.SetPathValue("id", wsID)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handlePostWorkspaceAgent(rec, req)
	return rec
}

// TestPostAgent_ReturnsOKOnContextCanceled verifies 那 当 another
// 客户端 cancels 会话 mid-turn, prompting 客户端's POST 是
// unaffected: SendMessage 是 fire-和-forget, 所以 处理器 returns
// 200 immediately without waiting 为 turn. 一个 运行 那 later
// returns 上下文.Canceled never surfaces 作为 500 到 prompter;
// the FinishReasonCanceled marker reaches SSE subscribers via the
// 助手消息 instead.
func TestPostAgent_ReturnsOKOnContextCanceled(t *testing.T) {
	t.Parallel()

	coord := newRunCoordinator(func(context.Context) error {
		return context.Canceled
	})
	c, wsID := buildAgentWorkspace(t, coord)

	// 处理器 returns immediately, before dispatched 运行 是
	// released, because 运行 no longer owns HTTP 响应.
	rec := postAgent(t, c, t.Context(), wsID, "S1")
	require.Equal(t, http.StatusAccepted, rec.Code, "fire-and-forget SendMessage must return 202 without waiting for the run")

	// 运行 是 dispatched 在 goroutine; let it return
	// 上下文.Canceled. Nothing 从 那 路径 reaches (already
	// returned) 处理器.
	select {
	case <-coord.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatched run was never entered")
	}
	close(coord.release)

	// Wait 为 dispatched 运行 到 fully return. Backend.runAgent
	// swallows 上下文.Canceled, 所以 it must 不 发布
	// notify.TypeAgentError. Publishing would dereference the synthetic
	// 工作区's nil 通知 总线 和 crash 这 goroutine,
	// which 是 explicit guard 那 取消 produces no top-level
	// 错误 事件.
	require.Eventually(t, func() bool {
		return coord.ranCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

// TestHandleError_ContextCanceledFallsThroughTo500 documents the step 8
// 清理: old 上下文.Canceled special case 在 handleError was
// removed because runtime cancellation 的 agent 运行 can no longer
// reach handleError. agent-提示词 处理器 returns 202 before 运行
// starts (fire-和-forget SendMessage) 和 Backend.runAgent swallows
// 上下文.Canceled. Any 上下文.Canceled 那 still reaches handleError
// 是 therefore unexpected synchronous 错误 和 falls through 到
// 默认 500 like any other.
func TestHandleError_ContextCanceledFallsThroughTo500(t *testing.T) {
	t.Parallel()

	c := &controllerV1{server: &Server{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

	c.handleError(rec, req, context.Canceled)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestPostAgent_DetachesRequestContext verifies 那 dispatched 运行
// 是 bound 到 工作区 上下文, 不 prompting 客户端's HTTP
// 请求 上下文. Canceling 请求 上下文 must neither 取消
// 运行 nor 是 observed 通过 coordinator.
func TestPostAgent_DetachesRequestContext(t *testing.T) {
	t.Parallel()

	coord := newRunCoordinator(func(context.Context) error {
		return nil
	})
	c, wsID := buildAgentWorkspace(t, coord)

	reqCtx, cancelReq := context.WithCancel(context.Background())

	// 处理器 returns immediately; 运行 keeps executing 在 它的
	// own goroutine bound 到 工作区 上下文.
	rec := postAgent(t, c, reqCtx, wsID, "S1")
	require.Equal(t, http.StatusAccepted, rec.Code)

	select {
	case <-coord.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatched run was never entered")
	}

	// Drop prompting 客户端. 这 must 不 reach 运行.
	cancelReq()

	got := coord.capturedCtx()
	require.NotNil(t, got)
	// Compare 通过 identity (指针), 不 reflect.DeepEqual: deep
	// comparison would traverse 上下文 internals 那 runtime
	// mutates concurrently.
	require.False(t, got == reqCtx, "run ctx must not be the request ctx")
	require.NoError(t, got.Err(), "run ctx must not inherit cancellation from the dropped request")

	// Release 运行 所以 it returns cleanly.
	close(coord.release)
	require.Eventually(t, func() bool {
		return coord.ranCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func (*runCoordinator) ListSettledMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}
