package agent

import (
	"context"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// TestSessionAgentRun_QueueStripsOnComplete verifies 那 当 运行
// 调用 是 enqueued (because 会话 是 already busy),
// OnComplete 钩子 是 NOT propagated onto queued copy. 钩子
// belongs 到 caller's 重试/coalesce scope (typically
// coordinator.运行) which has already returned 通过 time 队列
// drains; carrying it forward would silently funnel 终态
// 事件 into closure nobody reads, 和 subscribers (`crush 运行`)
// would hang waiting 为 RunComplete 那 never publishes.
func TestSessionAgentRun_QueueStripsOnComplete(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Cfg:      testConfigStore(),
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)

	const sessionID = "queued-session"
	// 标记 会话 作为 busy 所以 运行 takes 队列 branch
	// without needing real 模型.
	a.activeRequests.Set(sessionID, &activeCancel{cancel: func() {}})

	var called bool
	hook := func(notify.RunComplete) { called = true }

	res, err := a.Run(t.Context(), SessionAgentCall{
		SessionID:  sessionID,
		RunID:      "run-xyz",
		Prompt:     "queued prompt",
		OnComplete: hook,
	})
	require.NoError(t, err)
	require.Nil(t, res, "queued Run must return (nil, nil)")
	require.False(t, called,
		"OnComplete must not fire on the enqueue path; the caller's scope is still live")

	queued, ok := a.messageQueue.Get(sessionID)
	require.True(t, ok)
	require.Len(t, queued, 1)
	require.Nil(t, queued[0].OnComplete,
		"queued SessionAgentCall must have OnComplete stripped so the drain falls back to the default broker publish")
	require.Equal(t, "queued prompt", queued[0].Prompt,
		"all other fields must be preserved on the queued copy")
	require.Equal(t, "run-xyz", queued[0].RunID,
		"RunID must be preserved on the queued copy so the drained turn's "+
			"RunComplete still correlates with the originating SendMessage")
}

// TestDrainQueueForStep_FiltersUnderDispatchLock verifies 那 队列
// drain evaluates per-会话 取消 标记 while holding 派发
// 互斥锁 (canceledBySeq's documented precondition). Queued calls 在 或
// below 取消 high-water 标记 是 dropped, calls queued after
// 取消 (higher seq) 是 folded, untracked enqueues (seq == 0) 是
// dropped whenever any 标记 是 present, 和 队列 是 cleared. These
// calls carry no RunID, 所以 all foldable survivors 是 returned 为
// folding (the existing follow-up behavior).
func TestDrainQueueForStep_FiltersUnderDispatchLock(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Cfg:      testConfigStore(),
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)

	const sessionID = "drain-session"
	a.messageQueue.Set(sessionID, []SessionAgentCall{
		{SessionID: sessionID, Prompt: "below", acceptSeq: 1},
		{SessionID: sessionID, Prompt: "at-mark", acceptSeq: 2},
		{SessionID: sessionID, Prompt: "after", acceptSeq: 3},
		{SessionID: sessionID, Prompt: "untracked", acceptSeq: 0},
	})
	// 取消 high-water 标记 在 seq 2: seq <= 2 和 seq == 0 是 covered.
	a.cancelMark.Set(sessionID, 2)

	fold, canceled := a.drainQueueForStep(sessionID)

	require.Len(t, fold, 1,
		"only the follow-up queued after the cancel (seq > mark) must be folded")
	require.Equal(t, "after", fold[0].Prompt)
	require.Len(t, canceled, 3, "every call the cancel covers is dropped")

	_, ok := a.messageQueue.Get(sessionID)
	require.False(t, ok, "drain must clear the session message queue when nothing is kept")
}

// TestDrainQueueForStep_NoMarkFoldsAllNonRunID verifies 那 用 no
// 取消 标记 recorded, every queued 调用 without RunID 是 folded.
func TestDrainQueueForStep_NoMarkFoldsAllNonRunID(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Cfg:      testConfigStore(),
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)

	const sessionID = "drain-nomark"
	a.messageQueue.Set(sessionID, []SessionAgentCall{
		{SessionID: sessionID, Prompt: "a", acceptSeq: 0},
		{SessionID: sessionID, Prompt: "b", acceptSeq: 5},
	})

	fold, canceledWithRunID := a.drainQueueForStep(sessionID)
	require.Len(t, fold, 2, "no cancel mark means all non-RunID queued calls are folded")
	require.Empty(t, canceledWithRunID)
}

// TestDrainQueueForStep_KeepsRunIDPromptsQueued 是 core 的 fix 2:
// queued 提示词 那 carries RunID must NOT 是 folded into 活跃
// turn. Folding it would silently absorb it into another turn 和 never
// 发布 RunComplete 为 它的 RunID, hanging `crush 运行` caller 那
// blocks 在 那 事件. Such prompts 是 left 在 队列 所以
// recursive 运行 路径 gives each 它的 own turn 和 它的 own RunComplete.
// Non-RunID prompts 是 still folded.
func TestDrainQueueForStep_KeepsRunIDPromptsQueued(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Cfg:      testConfigStore(),
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)

	const sessionID = "drain-runid"
	a.messageQueue.Set(sessionID, []SessionAgentCall{
		{SessionID: sessionID, Prompt: "fold-me", acceptSeq: 1},
		{SessionID: sessionID, RunID: "run-a", Prompt: "keep-me", acceptSeq: 2},
		{SessionID: sessionID, RunID: "run-b", Prompt: "keep-me-too", acceptSeq: 3},
	})

	fold, canceledWithRunID := a.drainQueueForStep(sessionID)

	require.Len(t, fold, 1, "only the non-RunID prompt is folded into the active turn")
	require.Equal(t, "fold-me", fold[0].Prompt)
	require.Empty(t, canceledWithRunID)

	kept, ok := a.messageQueue.Get(sessionID)
	require.True(t, ok, "RunID-bearing prompts must remain queued for the recursive run path")
	require.Len(t, kept, 2)
	require.Equal(t, "run-a", kept[0].RunID)
	require.Equal(t, "run-b", kept[1].RunID)
}

// TestDrainQueueForStep_ReportsCanceledRunIDDrops verifies 那 queued
// 提示词 carrying RunID 那 是 dropped because 取消 covers it 是
// reported 在 canceledWithRunID 所以 caller can 发布 它的 终态
// cancelled RunComplete. 一个 canceled 提示词 without RunID 是 dropped
// silently 作为 before.
func TestDrainQueueForStep_ReportsCanceledRunIDDrops(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Cfg:      testConfigStore(),
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)

	const sessionID = "drain-cancel-runid"
	a.messageQueue.Set(sessionID, []SessionAgentCall{
		{SessionID: sessionID, RunID: "run-canceled", Prompt: "canceled", acceptSeq: 1},
		{SessionID: sessionID, Prompt: "canceled-no-runid", acceptSeq: 1},
		{SessionID: sessionID, RunID: "run-survives", Prompt: "survives", acceptSeq: 5},
	})
	a.cancelMark.Set(sessionID, 2)

	fold, canceled := a.drainQueueForStep(sessionID)

	require.Empty(t, fold, "no uncanceled non-RunID prompts to fold")
	require.Len(t, canceled, 2, "both prompts the cancel covers are dropped")
	require.Equal(t, "run-canceled", canceled[0].RunID)

	kept, ok := a.messageQueue.Get(sessionID)
	require.True(t, ok)
	require.Len(t, kept, 1, "the uncanceled RunID prompt stays queued")
	require.Equal(t, "run-survives", kept[0].RunID)
}

// TestRunCompletePublisher_MustDeliverOverTakesPublish exercises the
// pubsub.Publisher interface change end-到-end: 总线 是 只
// concrete Publisher implementation 和 must satisfy both 发布 和
// PublishMustDeliver. The coordinator's final RunComplete emit relies
// 在 PublishMustDeliver 到 apply bounded-blocking semantics 所以
// momentarily-full 订阅者 缓冲 can't silently drop
// authoritative end-的-运行 事件.
func TestRunCompletePublisher_MustDeliverOverTakesPublish(t *testing.T) {
	t.Parallel()

	broker := pubsub.NewBroker[notify.RunComplete]()
	t.Cleanup(broker.Shutdown)

	// Subscribe before publishing 所以 事件 是 delivered.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch := broker.Subscribe(ctx)

	rc := notify.RunComplete{SessionID: "S", MessageID: "m", Text: "ok"}
	var pub pubsub.Publisher[notify.RunComplete] = broker
	pub.PublishMustDeliver(t.Context(), pubsub.UpdatedEvent, rc)

	select {
	case got := <-ch:
		require.Equal(t, rc, got.Payload)
	case <-time.After(time.Second):
		t.Fatal("PublishMustDeliver did not deliver event")
	}
}

// requireSingleCancelledRunComplete reads exactly one RunComplete 从 ch,
// asserts it 是 cancelled 终态 事件 为 runID, 和 verifies no
// second 事件 arrives. 这 observes published pubsub 事件 rather
// than internal bookkeeping, which 是 contract `crush 运行` caller
// blocking 在 总线 actually relies 在.
func requireSingleCancelledRunComplete(t *testing.T, ch <-chan pubsub.Event[notify.RunComplete], sessionID, runID string) {
	t.Helper()
	select {
	case ev := <-ch:
		require.Equal(t, runID, ev.Payload.RunID,
			"the published RunComplete must carry the dropped queued prompt's RunID")
		require.Equal(t, sessionID, ev.Payload.SessionID)
		require.True(t, ev.Payload.Cancelled,
			"a dropped queued prompt must publish a cancelled RunComplete")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the cancelled RunComplete")
	}
	select {
	case extra := <-ch:
		t.Fatalf("expected exactly one RunComplete, got a second: %+v", extra.Payload)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestCancel_QueuedRunIDPromptPublishesCancelledRunComplete 证明
// 终态-事件 behavior end-到-end: RunID-bearing 提示词 sitting 在
// 队列 那 是 canceled while queued (via public 取消 路径,
// which routes through clearQueueAndNotify -> settleDroppedCalls)
// must emit exactly one cancelled RunComplete 在 总线 为 它的
// RunID. 一个 queued 提示词 without RunID 是 dropped silently. 这 是
// coverage earlier drain 测试 lacked: it asserted returned
// bookkeeping slice, 不 published 事件 `crush 运行` caller awaits.
func TestCancel_QueuedRunIDPromptPublishesCancelledRunComplete(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	broker := pubsub.NewBroker[notify.RunComplete]()
	t.Cleanup(broker.Shutdown)

	a := NewSessionAgent(SessionAgentOptions{
		Cfg:         testConfigStore(),
		Models:      noModels,
		Sessions:    env.sessions,
		Messages:    env.messages,
		RunComplete: broker,
	}).(*sessionAgent)

	subCtx, subCancel := context.WithCancel(t.Context())
	defer subCancel()
	ch := broker.Subscribe(subCtx)

	const sessionID = "cancel-queued-runid"
	a.messageQueue.Set(sessionID, []SessionAgentCall{
		{SessionID: sessionID, Prompt: "no-runid", acceptSeq: 1},
		{SessionID: sessionID, RunID: "run-queued", Prompt: "queued", acceptSeq: 2},
	})

	a.Cancel(sessionID)

	requireSingleCancelledRunComplete(t, ch, sessionID, "run-queued")

	_, ok := a.messageQueue.Get(sessionID)
	require.False(t, ok, "Cancel must clear the queue")
}

// TestDrainQueueForStep_DroppedRunIDPublishesCancelledRunComplete drives
// production drain 序号 (drainQueueForStep then
// settleDroppedCalls, mirroring PrepareStep handoff) 和
// asserts dropped RunID-bearing 提示词 actually publishes exactly one
// cancelled RunComplete 在 总线. companion bookkeeping 测试
// covers returned slice; 这 one covers observable 终态
// 事件.
func TestDrainQueueForStep_DroppedRunIDPublishesCancelledRunComplete(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	broker := pubsub.NewBroker[notify.RunComplete]()
	t.Cleanup(broker.Shutdown)

	a := NewSessionAgent(SessionAgentOptions{
		Cfg:         testConfigStore(),
		Models:      noModels,
		Sessions:    env.sessions,
		Messages:    env.messages,
		RunComplete: broker,
	}).(*sessionAgent)

	subCtx, subCancel := context.WithCancel(t.Context())
	defer subCancel()
	ch := broker.Subscribe(subCtx)

	const sessionID = "drain-drop-runid"
	a.messageQueue.Set(sessionID, []SessionAgentCall{
		{SessionID: sessionID, RunID: "run-dropped", Prompt: "dropped", acceptSeq: 1},
		{SessionID: sessionID, Prompt: "dropped-no-runid", acceptSeq: 1},
	})
	a.cancelMark.Set(sessionID, 2)

	_, canceled := a.drainQueueForStep(sessionID)
	require.Len(t, canceled, 2, "both canceled prompts are dropped")
	a.settleDroppedCalls(canceled)

	requireSingleCancelledRunComplete(t, ch, sessionID, "run-dropped")
}

// TestQueuedPromptsList_SkipsHidden pins 那 hidden continuation 是
// counted 为 scheduling 但 never listed 为 队列 pill.
func TestQueuedPromptsList_SkipsHidden(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)

	const sessionID = "hidden-queue"
	a.messageQueue.Set(sessionID, []SessionAgentCall{
		{SessionID: sessionID, Prompt: "visible"},
		{SessionID: sessionID, Prompt: "continue the plan", HiddenUserMessage: true},
	})

	require.Equal(t, 2, a.QueuedPrompts(sessionID))
	require.Equal(t, []string{"visible"}, a.QueuedPromptsList(sessionID))
}
