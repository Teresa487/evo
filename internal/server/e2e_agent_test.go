package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// scriptedCoordinator 是 agent.Coordinator stub 那 mimics
// externally-observable contract 的 real 运行 over SSE pipeline
// without booting real 模型, 数据库, 或 scheduler. 它 publishes
// 用户消息 当 运行 begins 和 助手消息 (用
// appropriate FinishReason) 当 运行 ends, exactly way real
// sessionAgent.运行 surfaces turn 到 SSE subscribers.
//
// 一个 运行 blocks until either 它的 per-会话 上下文 是 canceled (via
// 取消, mirroring explicit 取消 endpoint) 或 测试 releases
// it. On 取消 it emits FinishReasonCanceled 助手消息 和
// returns 上下文.Canceled (which backend.runAgent swallows, 所以 no
// AgentEvent 错误 是 published). On normal release it emits
// FinishReasonEndTurn 助手消息 和 returns nil.
//
// The internal scheduler signal points the PLAN's e2e cases reference
// (e.g. "before registration 在 activeRequests", "between
// activeRequests.Set 和 助手 创建") 是 不 exposed 通过
// codebase, 所以 这 stub reproduces documented black-box outcome 通过
// controlling 运行 timing directly through blockEntered / release.
type scriptedCoordinator struct {
	app *app.App

	// blockEntered, 当 non-nil, 是 signaled (once) right after 运行
	// 是 entered 和 before 用户消息 是 emitted, letting 测试
	// interleave 取消 用 dispatched goroutine.
	blockEntered chan struct{}

	mu sync.Mutex
	// cancels holds 取消 func 为 every 在-flight 运行, keyed 通过
	// monotonic id 所以 concurrent runs 为 same 会话 each get their
	// own 条目 ( map keyed 只 通过 sessionID would let second 运行
	// overwrite first's 取消 func 和 leak it).
	cancels map[int64]sessionCancel
	// pendingCancels counts cancels 那 arrived 为 会话 while 运行
	// was 在 flight; 运行 为 那 会话 consumes one 在 条目 和
	// cancels itself, modeling 取消-在-条目 路径 follow-up takes.
	pendingCancels map[string]int
	nextRunID      int64
	// entered carries monotonic 运行 id assigned 到 each 运行 作为 it 是
	// entered, 所以 测试 can correlate later 助手消息 back 到
	// specific 运行 (运行 1 vs accepted follow-up).
	entered   chan int64
	runStarts atomic.Int32

	release chan struct{}
}

type sessionCancel struct {
	sessionID string
	cancel    context.CancelFunc
}

func newScriptedCoordinator(a *app.App) *scriptedCoordinator {
	return &scriptedCoordinator{
		app:            a,
		cancels:        make(map[int64]sessionCancel),
		pendingCancels: make(map[string]int),
		entered:        make(chan int64, 8),
		release:        make(chan struct{}),
	}
}

func (c *scriptedCoordinator) emitUser(sessionID, id string) {
	c.app.SendEvent(pubsub.Event[message.Message]{
		Type: pubsub.CreatedEvent,
		Payload: message.Message{
			ID:        id,
			SessionID: sessionID,
			Role:      message.User,
			Parts:     []message.ContentPart{message.TextContent{Text: "hi"}},
		},
	})
}

func (c *scriptedCoordinator) emitAssistant(sessionID, id string, reason message.FinishReason) {
	c.app.SendEvent(pubsub.Event[message.Message]{
		Type: pubsub.CreatedEvent,
		Payload: message.Message{
			ID:        id,
			SessionID: sessionID,
			Role:      message.Assistant,
			Parts:     []message.ContentPart{message.Finish{Reason: reason}},
		},
	})
}

func (c *scriptedCoordinator) Run(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	c.runStarts.Add(1)
	runCtx, cancel := context.WithCancel(ctx)

	c.mu.Lock()
	id := c.nextRunID
	c.nextRunID++
	c.cancels[id] = sessionCancel{sessionID: sessionID, cancel: cancel}
	// 取消-在-条目: 如果 取消 为 这 会话 arrived while 这
	// 运行 was still being dispatched (no 运行 yet 在 flight 到 receive
	// it), consume pending 取消 now 所以 运行 takes canceled
	// 路径 instead 的 streaming output.
	if c.pendingCancels[sessionID] > 0 {
		c.pendingCancels[sessionID]--
		cancel()
	}
	c.mu.Unlock()

	select {
	case c.entered <- id:
	default:
	}

	if c.blockEntered != nil {
		select {
		case <-c.blockEntered:
		case <-runCtx.Done():
		}
	}

	defer func() {
		c.mu.Lock()
		delete(c.cancels, id)
		c.mu.Unlock()
		cancel()
	}()

	// Qualify emitted 消息 ids 用 运行 id 所以 测试 can
	// attribute 助手消息 到 exact 运行 那 produced it
	// (运行 1 vs accepted follow-up sharing same 会话).
	userID := fmt.Sprintf("u-%s-%d", sessionID, id)
	asstID := fmt.Sprintf("a-%s-%d", sessionID, id)

	c.emitUser(sessionID, userID)

	// Cancellation takes priority: 如果 运行 was already canceled it
	// must take canceled 路径 even 当 release 是 closed, 所以
	// canceled 运行 never races into normal FinishReasonEndTurn.
	select {
	case <-runCtx.Done():
		c.emitAssistant(sessionID, asstID, message.FinishReasonCanceled)
		return nil, context.Canceled
	default:
	}

	select {
	case <-c.release:
		c.emitAssistant(sessionID, asstID, message.FinishReasonEndTurn)
		return nil, nil
	case <-runCtx.Done():
		c.emitAssistant(sessionID, asstID, message.FinishReasonCanceled)
		return nil, context.Canceled
	}
}

func (c *scriptedCoordinator) RunAccepted(ctx context.Context, accept *agent.AcceptedRun, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	return c.Run(ctx, sessionID, prompt, attachments...)
}

func (c *scriptedCoordinator) BeginAccepted(string) *agent.AcceptedRun { return nil }

func (c *scriptedCoordinator) Cancel(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 取消 every 在-flight 运行 为 这 会话. Concurrent runs 为
	// same 会话 ( 活跃 运行 plus accepted follow-up still
	// dispatching) each hold their own 条目, 所以 all 的 them 是 torn
	// down 通过 single per-会话 取消.
	var canceled int
	for _, sc := range c.cancels {
		if sc.sessionID == sessionID {
			sc.cancel()
			canceled++
		}
	}
	// If 在 least one 运行 was 在 flight, arm pending 取消 所以
	// follow-up 那 has been accepted 但 不 yet entered 运行 takes
	// 取消-在-条目 路径. With no 运行 在 flight 这 是 no-op,
	// mirroring production guarantee 那 空闲 取消 does 不 arm
	// pending 取消 against next 提示词.
	if canceled > 0 {
		c.pendingCancels[sessionID]++
	}
}

func (c *scriptedCoordinator) CancelAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sc := range c.cancels {
		sc.cancel()
	}
}

func (c *scriptedCoordinator) IsBusy() bool                                  { return false }
func (c *scriptedCoordinator) IsSessionBusy(string) bool                     { return false }
func (c *scriptedCoordinator) QueuedPrompts(string) int                      { return 0 }
func (c *scriptedCoordinator) QueuedPromptsList(string) []string             { return nil }
func (c *scriptedCoordinator) ClearQueue(string)                             {}
func (c *scriptedCoordinator) AddNote(string, string)                        {}
func (c *scriptedCoordinator) Summarize(context.Context, string) error       { return nil }
func (c *scriptedCoordinator) Model() agent.Model                            { return agent.Model{} }
func (c *scriptedCoordinator) UpdateModels(context.Context) error            { return nil }
func (c *scriptedCoordinator) SetMainAgent(string) error                     { return nil }
func (c *scriptedCoordinator) MainAgent() string                             { return "" }
func (c *scriptedCoordinator) GenerateTitle(context.Context, string, string) {}
func (c *scriptedCoordinator) SetHostContext(proto.HostContext)              {}
func (c *scriptedCoordinator) HostContext() proto.HostContext                { return proto.HostContext{} }

// agentE2EHarness extends SSE harness 用 scripted coordinator
// wired into 工作区's embedded app.App, 所以 POST /agent drives
// real backend.SendMessage 派发 whose emitted 用户/助手
// messages fan out over the same SSE pipeline production uses.
type agentE2EHarness struct {
	*e2eHarness
	coord *scriptedCoordinator
}

func newAgentE2EHarness(t *testing.T) *agentE2EHarness {
	t.Helper()

	h := &e2eHarness{}

	appCtx, cancel := context.WithCancel(context.Background())
	a := app.NewForTest(appCtx)
	coord := newScriptedCoordinator(a)
	a.AgentCoordinator = coord
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
	backend.SetWorkspaceShutdownFnForTest(ws, func() {})
	backend.InsertWorkspaceForTest(h.backend, ws)

	h.workspace = ws
	h.app = a
	return &agentE2EHarness{e2eHarness: h, coord: coord}
}

// postAgentHTTP drives POST /v1/workspaces/{id}/agent over the harness's
// httptest 服务端 和 returns status code.
func (h *agentE2EHarness) postAgentHTTP(t *testing.T, ctx context.Context, sessionID string) int {
	t.Helper()
	body, err := json.Marshal(proto.AgentMessage{SessionID: sessionID, Prompt: "hi"})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.httpSrv.URL+"/v1/workspaces/"+h.workspace.ID+"/agent", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// cancelAgentHTTP drives POST /v1/workspaces/{id}/agent/sessions/{sid}/取消.
func (h *agentE2EHarness) cancelAgentHTTP(t *testing.T, ctx context.Context, sessionID string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.httpSrv.URL+"/v1/workspaces/"+h.workspace.ID+"/agent/sessions/"+sessionID+"/cancel", nil)
	require.NoError(t, err)
	resp, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// waitForRunEntered blocks until dispatched 运行 为 any 会话 has
// been entered 通过 scripted coordinator, 或 fails 测试. 它
// returns monotonic 运行 id assigned 到 那 运行 所以 caller can
// correlate it 用 later 助手消息; callers 那 don't need
// id can ignore return 值.
func (h *agentE2EHarness) waitForRunEntered(t *testing.T) int64 {
	t.Helper()
	select {
	case id := <-h.coord.entered:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("dispatched run was never entered")
		return 0
	}
}

// finishReason extracts 助手消息's FinishReason, 如果 any.
func finishReason(m proto.Message) (proto.FinishReason, bool) {
	for _, p := range m.Parts {
		if f, ok := p.(proto.Finish); ok {
			return f.Reason, true
		}
	}
	return "", false
}

// TestE2E_CancelByOtherClientDoesNotErrorPrompter covers PLAN Tests ->
// New end-到-end coverage item 1: second 客户端 canceling 运行 does
// 不 surface 服务端 错误 到 prompter; 运行 ends 用
// FinishReasonCanceled 助手消息 和 no AgentEvent carries
// non-nil 错误.
func TestE2E_CancelByOtherClientDoesNotErrorPrompter(t *testing.T) {
	t.Parallel()
	h := newAgentE2EHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	cidA := uuid.New().String()
	cidB := uuid.New().String()
	evcA, cancelA := h.subscribeSSE(t, ctx, h.workspace.ID, cidA)
	t.Cleanup(cancelA)
	evcB, cancelB := h.subscribeSSE(t, ctx, h.workspace.ID, cidB)
	t.Cleanup(cancelB)
	h.waitForAttached(t, 2)

	const sid = "s-cancel-other"

	// 一个 posts long-running 提示词; 处理器 must return 202
	// immediately ( 运行 blocks 在 coordinator).
	require.Equal(t, http.StatusAccepted, h.postAgentHTTP(t, ctx, sid))
	h.waitForRunEntered(t)

	// B cancels.
	require.Equal(t, http.StatusOK, h.cancelAgentHTTP(t, ctx, sid))

	// 一个's SSE 流 receives FinishReasonCanceled 助手
	// 消息.
	pickCtx, pickCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pickCancel()
	got, ok := drainUntil(pickCtx, evcA, func(e pubsub.Event[proto.Message]) bool {
		r, has := finishReason(e.Payload)
		return e.Payload.Role == proto.Assistant && has && r == proto.FinishReasonCanceled
	})
	require.True(t, ok, "client A must observe a FinishReasonCanceled assistant message")
	require.Equal(t, sid, got.Payload.SessionID)

	// No AgentEvent 错误 reaches 一个 (取消 是 不 服务端 错误).
	errCtx, errCancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer errCancel()
	_, gotErrA := drainUntil(errCtx, evcA, func(e pubsub.Event[proto.AgentEvent]) bool {
		return e.Payload.Type == proto.AgentEventTypeError && e.Payload.Error != nil
	})
	require.False(t, gotErrA, "cancel must not surface an AgentEvent error to the prompter")

	// And no AgentEvent 错误 reaches canceling 客户端 B either;
	// PLAN requires 那 *no* 客户端 observes non-nil 错误.
	errCtxB, errCancelB := context.WithTimeout(ctx, 250*time.Millisecond)
	defer errCancelB()
	_, gotErrB := drainUntil(errCtxB, evcB, func(e pubsub.Event[proto.AgentEvent]) bool {
		return e.Payload.Type == proto.AgentEventTypeError && e.Payload.Error != nil
	})
	require.False(t, gotErrB, "cancel must not surface an AgentEvent error to any client")
}

// TestE2E_CancelImmediatelyAfter202IsNotLost covers PLAN item 1a: a
// 取消 那 races freshly-dispatched 运行 (before it would emit any
// output) 是 不 lost. 运行 takes 取消-在-条目 路径 和 emits
// 用户消息 followed 通过 FinishReasonCanceled 助手消息
// rather than streaming 模型 output.
func TestE2E_CancelImmediatelyAfter202IsNotLost(t *testing.T) {
	t.Parallel()
	h := newAgentE2EHarness(t)
	// Gate 运行 在 signal 测试 controls 所以 取消 can 是
	// observed while dispatched goroutine 是 parked 在 条目.
	h.coord.blockEntered = make(chan struct{})

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	cid := uuid.New().String()
	evc, cancelSSE := h.subscribeSSE(t, ctx, h.workspace.ID, cid)
	t.Cleanup(cancelSSE)
	h.waitForAttached(t, 1)

	const sid = "s-race-cancel"
	require.Equal(t, http.StatusAccepted, h.postAgentHTTP(t, ctx, sid))
	h.waitForRunEntered(t)

	// 取消 while 运行 是 still blocked 在 条目, then release it.
	require.Equal(t, http.StatusOK, h.cancelAgentHTTP(t, ctx, sid))
	close(h.coord.blockEntered)

	pickCtx, pickCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pickCancel()

	gotUser, okUser := drainUntil(pickCtx, evc, func(e pubsub.Event[proto.Message]) bool {
		return e.Payload.Role == proto.User && e.Payload.SessionID == sid
	})
	require.True(t, okUser, "the canceled turn must still record a user message")
	require.Equal(t, sid, gotUser.Payload.SessionID)

	gotAsst, okAsst := drainUntil(pickCtx, evc, func(e pubsub.Event[proto.Message]) bool {
		r, has := finishReason(e.Payload)
		return e.Payload.Role == proto.Assistant && has && r == proto.FinishReasonCanceled
	})
	require.True(t, okAsst, "the canceled turn must end with a FinishReasonCanceled assistant message")
	require.Equal(t, sid, gotAsst.Payload.SessionID)
}

// TestE2E_IdleCancelDoesNotPoisonNextPrompt covers PLAN item 1b: an
// 空闲 取消 (no 活跃 运行) must 不 poison next 提示词. With
// scripted coordinator 取消 records pending 条目 只 如果 运行
// 是 在 flight; 空闲 取消 records one, 但 documented
// guarantee 是 那 *next* 提示词's outcome 是 observable. Here we
// assert regression-relevant external behavior: after 空闲
// 取消, subsequent normal 提示词 是 able 到 运行 和 emit output.
//
// NOTE: 这 是 simplified version. real "空闲 Escape must 不
// poison" guarantee lives inside sessionAgent.取消's acceptedRuns
// gating, which 是 covered 通过 agent unit tests; e2e stub cannot
// distinguish "truly 空闲" 从 "accepted 但 不 yet running" without
// internal acceptedRuns signal. See 测试 摘要.
func TestE2E_IdleCancelDoesNotPoisonNextPrompt(t *testing.T) {
	t.Parallel()
	h := newAgentE2EHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	cid := uuid.New().String()
	evc, cancelSSE := h.subscribeSSE(t, ctx, h.workspace.ID, cid)
	t.Cleanup(cancelSSE)
	h.waitForAttached(t, 1)

	const sid = "s-idle-cancel"

	// 空闲 取消: no 运行 在 flight. scripted coordinator drops it
	// (no pending 取消 recorded 为 会话 那 has no 运行), which
	// models production guarantee 那 空闲 Escape does 不 arm
	// 取消 against next 提示词.
	require.Equal(t, http.StatusOK, h.cancelAgentHTTP(t, ctx, sid))

	// Now normal 提示词; release it 所以 it finishes successfully.
	require.Equal(t, http.StatusAccepted, h.postAgentHTTP(t, ctx, sid))
	h.waitForRunEntered(t)
	close(h.coord.release)

	pickCtx, pickCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pickCancel()
	got, ok := drainUntil(pickCtx, evc, func(e pubsub.Event[proto.Message]) bool {
		r, has := finishReason(e.Payload)
		return e.Payload.Role == proto.Assistant && has && r == proto.FinishReasonEndTurn
	})
	require.True(t, ok, "the next prompt after an idle cancel must run to FinishReasonEndTurn")
	require.Equal(t, sid, got.Payload.SessionID)

	// And it must 不 是 marked canceled.
	canCtx, canCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer canCancel()
	_, gotCanceled := drainUntil(canCtx, evc, func(e pubsub.Event[proto.Message]) bool {
		r, has := finishReason(e.Payload)
		return e.Payload.Role == proto.Assistant && has && r == proto.FinishReasonCanceled
	})
	require.False(t, gotCanceled, "an idle cancel must not produce a FinishReasonCanceled marker on the next prompt")
}

// TestE2E_CancelBetweenActiveSetAndAssistantCreate covers PLAN item 1d:
// 取消 那 arrives after 运行 has begun 但 before it would
// 创建 助手消息 must still produce 用户消息 和
// FinishReasonCanceled 助手消息, never silent return.
// blockEntered gate parks 运行 after 条目 (modeling 窗口
// between activeRequests.Set 和 助手 creation).
func TestE2E_CancelBetweenActiveSetAndAssistantCreate(t *testing.T) {
	t.Parallel()
	h := newAgentE2EHarness(t)
	h.coord.blockEntered = make(chan struct{})

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	cid := uuid.New().String()
	evc, cancelSSE := h.subscribeSSE(t, ctx, h.workspace.ID, cid)
	t.Cleanup(cancelSSE)
	h.waitForAttached(t, 1)

	const sid = "s-mid-window"
	require.Equal(t, http.StatusAccepted, h.postAgentHTTP(t, ctx, sid))
	h.waitForRunEntered(t)

	// 取消 while parked 在 条目; then release 所以 运行 proceeds
	// into 它的 取消 branch (runCtx already canceled).
	require.Equal(t, http.StatusOK, h.cancelAgentHTTP(t, ctx, sid))
	close(h.coord.blockEntered)

	pickCtx, pickCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pickCancel()

	_, okUser := drainUntil(pickCtx, evc, func(e pubsub.Event[proto.Message]) bool {
		return e.Payload.Role == proto.User && e.Payload.SessionID == sid
	})
	require.True(t, okUser, "a user message must be recorded for the canceled turn")

	gotAsst, okAsst := drainUntil(pickCtx, evc, func(e pubsub.Event[proto.Message]) bool {
		r, has := finishReason(e.Payload)
		return e.Payload.Role == proto.Assistant && has && r == proto.FinishReasonCanceled
	})
	require.True(t, okAsst, "the run must not return silently; it must emit a FinishReasonCanceled assistant message")
	require.Equal(t, sid, gotAsst.Payload.SessionID)

	// No AgentEvent 错误 是 published: 取消 在
	// activeRequests.Set -> 助手-创建 窗口 是 不 服务端
	// 错误.
	errCtx, errCancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer errCancel()
	_, gotErr := drainUntil(errCtx, evc, func(e pubsub.Event[proto.AgentEvent]) bool {
		return e.Payload.Type == proto.AgentEventTypeError && e.Payload.Error != nil
	})
	require.False(t, gotErr, "no AgentEvent error must be published for the canceled turn")
}

// TestE2E_PromptRequestContextDoesNotOwnRun covers PLAN item 2: the
// prompting 客户端's HTTP 请求 上下文 does 不 own 运行. 一个 POST
// whose 上下文 是 retired 作为 soon 作为 it returns still gets 它的 202,
// 和 运行 keeps going (observed via SSE finishing normally after
// release).
func TestE2E_PromptRequestContextDoesNotOwnRun(t *testing.T) {
	t.Parallel()
	h := newAgentE2EHarness(t)
	streamCtx, streamCancel := context.WithCancel(t.Context())
	t.Cleanup(streamCancel)

	cid := uuid.New().String()
	evc, cancelSSE := h.subscribeSSE(t, streamCtx, h.workspace.ID, cid)
	t.Cleanup(cancelSSE)
	h.waitForAttached(t, 1)

	const sid = "s-short-req"

	// prompting 客户端's 请求 上下文 是 retired moment
	// POST returns. 处理器 must still have answered 202
	// (fire-和-forget) 和 运行 must outlive 那 上下文. Cancelling
	// explicitly beats racing wall-clock 期限: 期限 short
	// enough 到 是 interesting 是 also short enough 为 loaded runner
	// 到 miss, 和 POST 那 never completes proves nothing either way.
	reqCtx, reqCancel := context.WithCancel(t.Context())
	defer reqCancel()
	require.Equal(t, http.StatusAccepted, h.postAgentHTTP(t, reqCtx, sid))
	h.waitForRunEntered(t)

	// Retire 请求 上下文, then release 运行.
	reqCancel()
	<-reqCtx.Done()
	close(h.coord.release)

	pickCtx, pickCancel := context.WithTimeout(streamCtx, 3*time.Second)
	defer pickCancel()
	got, ok := drainUntil(pickCtx, evc, func(e pubsub.Event[proto.Message]) bool {
		r, has := finishReason(e.Payload)
		return e.Payload.Role == proto.Assistant && has && r == proto.FinishReasonEndTurn
	})
	require.True(t, ok, "the run must finish normally even after the prompting request context expired")
	require.Equal(t, sid, got.Payload.SessionID)
}

// TestE2E_AgentRunSurvivesAcrossWorkspaceClaims covers PLAN item 3: a
// 运行 started 通过 客户端 一个 survives 一个 detaching 作为 long 作为 another
// 客户端 (B) keeps 工作区 alive; B observes 运行 finish via
// SSE.
func TestE2E_AgentRunSurvivesAcrossWorkspaceClaims(t *testing.T) {
	t.Parallel()
	h := newAgentE2EHarness(t)

	ctxA, cancelA := context.WithCancel(t.Context())
	ctxB, cancelB := context.WithCancel(t.Context())
	t.Cleanup(cancelB)

	cidA := uuid.New().String()
	cidB := uuid.New().String()
	_, killA := h.subscribeSSE(t, ctxA, h.workspace.ID, cidA)
	t.Cleanup(killA)
	evcB, killB := h.subscribeSSE(t, ctxB, h.workspace.ID, cidB)
	t.Cleanup(killB)
	h.waitForAttached(t, 2)

	const sid = "s-survive"
	// 一个 是 poster; 运行 must outlive 一个 detaching 作为 long 作为 B
	// keeps 工作区 alive.
	require.Equal(t, http.StatusAccepted, h.postAgentHTTP(t, ctxA, sid))
	h.waitForRunEntered(t)

	// 一个 detaches; B 是 still attached 所以 工作区 stays alive.
	cancelA()
	killA()
	require.Eventually(t, func() bool {
		return backend.WorkspaceLiveStreamCountForTest(h.workspace) == 1
	}, 3*time.Second, 10*time.Millisecond,
		"A detaching must leave B as the sole attached client")
	require.False(t, h.shutdownHit.Load(), "workspace must stay alive while B is attached")

	// Release 运行; B must still observe it finish.
	close(h.coord.release)
	pickCtx, pickCancel := context.WithTimeout(ctxB, 3*time.Second)
	defer pickCancel()
	got, ok := drainUntil(pickCtx, evcB, func(e pubsub.Event[proto.Message]) bool {
		r, has := finishReason(e.Payload)
		return e.Payload.Role == proto.Assistant && has && r == proto.FinishReasonEndTurn
	})
	require.True(t, ok, "B must observe the run finish after A detaches")
	require.Equal(t, sid, got.Payload.SessionID)
}

// TestE2E_CancelOfActiveRunAlsoCancelsAcceptedFollowUp covers PLAN item
// 1c 在 externally-observable level: while 会话 sid has 活跃
// 运行, second 提示词 为 sid 是 accepted; 取消 为 sid must 取消
// 活跃 运行 和 must 不 let follow-up 流 normal
// FinishReasonEndTurn.
//
// 序号 follows PLAN exactly: 提示词 1 becomes 活跃
// 运行, 提示词 2 为 same sid 是 accepted, then 取消 为 sid
// fires, 和 只 afterwards 是 any signals released. scripted
// coordinator models externally-observable contract 的
// busy-队列 branch 和 pendingCancels (which depend 在 internal
// scheduler signals codebase does 不 expose): per-会话 取消
// tears down every 在-flight 运行 为 sid 和 arms 取消-在-条目 为
// follow-up still dispatching. invariant asserted 是 one 那
// matters: after 取消, 活跃 运行 ends canceled 和
// follow-up never streams a normal FinishReasonEndTurn.
func TestE2E_CancelOfActiveRunAlsoCancelsAcceptedFollowUp(t *testing.T) {
	t.Parallel()
	h := newAgentE2EHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	cid := uuid.New().String()
	evc, cancelSSE := h.subscribeSSE(t, ctx, h.workspace.ID, cid)
	t.Cleanup(cancelSSE)
	h.waitForAttached(t, 1)

	const sid = "s-followup"

	// () 提示词 1 为 sid becomes 活跃 运行. Capture 它的 运行 id 所以
	// canceled 助手消息 below can 是 attributed 到 运行 1
	// unambiguously.
	require.Equal(t, http.StatusAccepted, h.postAgentHTTP(t, ctx, sid))
	run1 := h.waitForRunEntered(t)

	// (b) 提示词 2 为 *same* sid 是 accepted while 活跃 运行
	// 是 still 在 flight; it 是 follow-up PLAN describes
	// (acceptedRuns > 0, either still dispatching 或 about 到 enter
	// busy-队列 branch).
	require.Equal(t, http.StatusAccepted, h.postAgentHTTP(t, ctx, sid))
	run2 := h.waitForRunEntered(t)
	require.NotEqual(t, run1, run2, "the follow-up must be a distinct run from the active one")

	// (c) B cancels sid. 这 tears down every 在-flight 运行 为
	// 会话 和 arms pending 取消 为 any follow-up 那 has 不
	// yet entered 运行.
	require.Equal(t, http.StatusOK, h.cancelAgentHTTP(t, ctx, sid))

	// (d) Open coordinator gate 所以 any 运行 那 是 NOT canceled would
	// 是 free 到 proceed straight into normal FinishReasonEndTurn
	// branch. scripted 运行 checks runCtx.Done() before release
	// select, 所以 canceled 运行 still takes canceled 路径 even 用
	// release closed; 只 non-canceled 运行 reaches FinishReasonEndTurn.
	// Releasing here 是 therefore what makes assertions below
	// meaningful: 如果 取消 had failed 到 tear down 运行 1 或 arm
	// 取消-在-条目 为 follow-up, freed gate would let 那 运行
	// 流 normal FinishReasonEndTurn 和 测试 would fail.
	close(h.coord.release)

	pickCtx, pickCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pickCancel()

	// (e) 运行 1 ( 活跃 运行) must end 用 FinishReasonCanceled.
	// 助手消息 id 是 qualified 用 运行 id, 所以 matching 在
	// run1's id proves cancellation 是 attributed 到 FIRST 运行
	// 和 不 到 follow-up.
	//
	// single drain below 是 also negative assertion 为 运行 2:
	// match closure inspects every 助手 事件 为 sid 作为 it
	// scans, 和 如果 it ever observes follow-up (运行 2) streaming
	// normal FinishReasonEndTurn it records 那 violation immediately.
	// 这 是 what makes 运行-2 check sound: previous two-phase
	// approach could let 这 very drain consume 和 discard 运行-2
	// EndTurn while still hunting 为 运行 1's canceled 消息, leaving
	// later no-EndTurn check unable 到 prove 运行 2 stayed canceled.
	// Folding negative check into same scan means 运行-2 EndTurn
	// can never slip past unobserved, whether it arrives before 或 after
	// 运行 1's canceled 消息.
	run1AsstID := fmt.Sprintf("a-%s-%d", sid, run1)
	run2AsstID := fmt.Sprintf("a-%s-%d", sid, run2)
	var followUpEndTurn bool
	got, ok := drainUntil(pickCtx, evc, func(e pubsub.Event[proto.Message]) bool {
		if e.Payload.SessionID != sid || e.Payload.Role != proto.Assistant {
			return false
		}
		r, has := finishReason(e.Payload)
		if !has {
			return false
		}
		// Any normal 模型 output 为 sid after 取消 是
		// violation. follow-up (运行 2) must never reach
		// FinishReasonEndTurn branch; flag it moment it 是 seen 所以
		// assertion below fails even 如果 这 事件 arrives while we
		// 是 still waiting 为 运行 1's canceled 消息.
		if r == proto.FinishReasonEndTurn {
			if e.Payload.ID == run2AsstID || e.Payload.ID != run1AsstID {
				followUpEndTurn = true
			}
			// Stop draining; EndTurn observation 是 decisive 和
			// require.False below 将 surface failure.
			return true
		}
		return e.Payload.ID == run1AsstID && r == proto.FinishReasonCanceled
	})
	require.False(t, followUpEndTurn, "the accepted follow-up must not stream a normal FinishReasonEndTurn after the cancel")
	require.True(t, ok, "the first (active) run must end with FinishReasonCanceled")
	require.Equal(t, run1AsstID, got.Payload.ID, "the canceled message must belong to the first (active) run")
	gotReason, gotHas := finishReason(got.Payload)
	require.True(t, gotHas)
	require.Equal(t, proto.FinishReasonCanceled, gotReason, "the matched run-1 message must be canceled, not a normal end turn")
	require.Equal(t, sid, got.Payload.SessionID)

	// Confirm no normal FinishReasonEndTurn 为 sid 是 still 在 flight.
	// By 这 point scan above has already ruled out 运行-2 EndTurn
	// arriving before 运行 1's canceled 消息; 这 guards against one
	// arriving afterward.
	endCtx, endCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer endCancel()
	_, gotEnd := drainUntil(endCtx, evc, func(e pubsub.Event[proto.Message]) bool {
		r, has := finishReason(e.Payload)
		return e.Payload.SessionID == sid && e.Payload.Role == proto.Assistant && has && r == proto.FinishReasonEndTurn
	})
	require.False(t, gotEnd, "the accepted follow-up must not stream model output after the cancel")
}

func (*scriptedCoordinator) ListSettledMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}
