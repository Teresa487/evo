package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// finishStreamModel 是 minimal fantasy.LanguageModel 那 streams
// single 文本 片段 followed 通过 normal (FinishReasonStop) finish. 它
// 是 enough 到 drive sessionAgent.运行 through PrepareStep 和 clean
// completion without recorded 提供方 cassette.
type finishStreamModel struct {
	text string
}

func (m *finishStreamModel) Provider() string { return "fake" }
func (m *finishStreamModel) Model() string    { return "fake-model" }

func (m *finishStreamModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: m.text}},
		FinishReason: fantasy.FinishReasonStop,
	}, nil
}

func (m *finishStreamModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	text := m.text
	return func(yield func(fantasy.StreamPart) bool) {
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "1"}) {
			return
		}
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "1", Delta: text}) {
			return
		}
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "1"}) {
			return
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

func (m *finishStreamModel) GenerateObject(ctx context.Context, call fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *finishStreamModel) StreamObject(ctx context.Context, call fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

func newStreamTestAgent(t *testing.T) (*sessionAgent, fakeEnv) {
	t.Helper()
	env := testEnv(t)
	model := &finishStreamModel{text: "done"}
	sa := testSessionAgent(env, model, model, "system").(*sessionAgent)
	return sa, env
}

// TestCancel_ActiveAndAcceptedFiresBothBranches covers the case where a
// 会话 是 actively running (activeRequests set) AND follow-up has
// been accepted (acceptedRuns > 0). 一个 single 取消 must fire both: it
// invokes 活跃 取消 func 和 records pending 取消 为
// accepted follow-up.
func TestCancel_ActiveAndAcceptedFiresBothBranches(t *testing.T) {
	t.Parallel()
	sa, _ := newCancelTestAgent(t)

	const sid = "sid"
	var activeCanceled atomic.Bool
	sa.activeRequests.Set(sid, &activeCancel{cancel: func() { activeCanceled.Store(true) }})

	accept := sa.BeginAccepted(sid)
	defer accept.Close()

	sa.Cancel(sid)

	require.True(t, activeCanceled.Load(), "active cancel func must fire")
	require.True(t, sa.hasPendingCancel(sid), "accepted follow-up must record a pending cancel")
}

// TestRun_BusyWithPendingCancelTakesCancelOnEntry covers busy-队列
// branch consulting pendingCancels: 当 会话 是 busy AND
// 取消 has been recorded 为 accepted follow-up, 运行 must take
// 取消-在-条目 路径 (persist canceled turn) instead 的 enqueueing
// 调用 behind 活跃 运行.
func TestRun_BusyWithPendingCancelTakesCancelOnEntry(t *testing.T) {
	t.Parallel()
	sa, env := newCancelTestAgent(t)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	// Make 会话 look busy: earlier 提示词 是 活跃.
	sa.activeRequests.Set(sess.ID, &activeCancel{cancel: func() {}})

	accept := sa.BeginAccepted(sess.ID)
	// 一个 取消 arrives while 这 follow-up 是 accepted-但-不-活跃.
	sa.Cancel(sess.ID)
	require.True(t, sa.hasPendingCancel(sess.ID))

	result, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		Prompt:    "follow-up",
		Accepted:  accept,
	})
	require.NoError(t, err)
	require.Nil(t, result)

	// follow-up was canceled 在 条目, 不 enqueued.
	require.Equal(t, 0, sa.QueuedPrompts(sess.ID),
		"cancel-on-entry must not enqueue the follow-up behind the active run")
	require.False(t, sa.hasPendingCancel(sess.ID), "pending cancel must be consumed")
	require.Equal(t, 0, sa.acceptedCount(sess.ID), "accept reservation must be released")

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, message.User, msgs[0].Role)
	assert.Equal(t, message.Assistant, msgs[1].Role)
	assert.Equal(t, message.FinishReasonCanceled, msgs[1].FinishReason())
}

// TestRun_PrepareStepDrainSkipsQueuedOnPendingCancel verifies 那
// 队列 drain inside PrepareStep skips queued follow-up prompts 当
// 取消 has been recorded 为 会话: queued 提示词 must 不
// 是 folded into 活跃 turn 作为 extra 用户消息.
func TestRun_PrepareStepDrainSkipsQueuedOnPendingCancel(t *testing.T) {
	t.Parallel()
	sa, env := newStreamTestAgent(t)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	// 一个 follow-up 提示词 sits queued 为 会话.
	sa.enqueueCall(SessionAgentCall{SessionID: sess.ID, Prompt: "queued-followup"})
	// 一个 取消 was recorded 为 会话 while it sat 在 队列.
	sa.cancelMark.Set(sess.ID, 1)

	result, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		Prompt:    "main",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Only main 提示词 produced 用户消息; queued
	// follow-up was skipped, 不 folded into turn.
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var userMsgs []message.Message
	for _, m := range msgs {
		if m.Role == message.User {
			userMsgs = append(userMsgs, m)
		}
	}
	require.Len(t, userMsgs, 1, "queued follow-up must not create a user message")
	assert.Equal(t, "main", userMsgs[0].Content().String())

	// 队列 was drained 和 pending 取消 consumed.
	require.Equal(t, 0, sa.QueuedPrompts(sess.ID))
	require.False(t, sa.hasPendingCancel(sess.ID))
}

// TestRun_NormalCompletionClearsStalePendingCancel verifies 那 运行
// which completes normally clears any stale pending-取消 条目 为
// 会话, 所以 it cannot catch future 运行.
func TestRun_NormalCompletionClearsStalePendingCancel(t *testing.T) {
	t.Parallel()
	sa, env := newStreamTestAgent(t)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	// 一个 stale 取消 标记 lingers (no queued work, no accepted 运行).
	sa.cancelMark.Set(sess.ID, 1)

	result, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		Prompt:    "main",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	require.False(t, sa.hasPendingCancel(sess.ID),
		"normal completion must clear the stale pending cancel")

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, message.Assistant, msgs[1].Role)
	assert.Equal(t, message.FinishReasonEndTurn, msgs[1].FinishReason())
}

// newCancelTestAgentWithRunComplete builds a DB-backed sessionAgent wired
// 到 RunComplete 总线 所以 tests can observe 终态 事件
// RunID-bearing caller (e.g. `crush 运行`) blocks 在.
func newCancelTestAgentWithRunComplete(t *testing.T) (*sessionAgent, fakeEnv, *pubsub.Broker[notify.RunComplete]) {
	t.Helper()
	env := testEnv(t)
	broker := pubsub.NewBroker[notify.RunComplete]()
	t.Cleanup(broker.Shutdown)
	sa := NewSessionAgent(SessionAgentOptions{
		Cfg:         testConfigStore(),
		Models:      noModels,
		Sessions:    env.sessions,
		Messages:    env.messages,
		RunComplete: broker,
	}).(*sessionAgent)
	return sa, env, broker
}

// TestRun_CancelOnEntryPublishesRunComplete covers the first review
// finding: 取消-在-条目 路径 returned before streaming defer
// 那 publishes RunComplete was installed. 一个 caller 那 dispatches
// 运行 用 RunID 和 blocks 在 RunComplete (ignoring 消息 events,
// like `crush 运行`) would hang 在 immediately-canceled accepted 运行.
// 取消-在-条目 路径 must 发布 终态 RunComplete carrying
// the originating RunID.
func TestRun_CancelOnEntryPublishesRunComplete(t *testing.T) {
	t.Parallel()
	sa, env, broker := newCancelTestAgentWithRunComplete(t)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch := broker.Subscribe(ctx)

	accept := sa.BeginAccepted(sess.ID)
	// 一个 取消 arrives 在 accepted-但-不-yet-活跃 窗口.
	sa.Cancel(sess.ID)
	require.True(t, sa.hasPendingCancel(sess.ID))

	result, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		RunID:     "run-cancel-on-entry",
		Prompt:    "hello",
		Accepted:  accept,
	})
	require.NoError(t, err)
	require.Nil(t, result)

	select {
	case got := <-ch:
		assert.Equal(t, "run-cancel-on-entry", got.Payload.RunID,
			"RunComplete must echo the originating RunID")
		assert.Equal(t, sess.ID, got.Payload.SessionID)
		assert.True(t, got.Payload.Cancelled,
			"cancel-on-entry RunComplete must be marked cancelled")
	case <-time.After(2 * time.Second):
		t.Fatal("cancel-on-entry must publish RunComplete; a RunID caller would hang otherwise")
	}
}

// 一个 运行 那 fails while setting up, after it became 活跃, still ends
// 用 RunComplete; observers would otherwise think 会话 busy.
func TestRun_SetupFailurePublishesRunComplete(t *testing.T) {
	t.Parallel()
	sa, _, broker := newCancelTestAgentWithRunComplete(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch := broker.Subscribe(ctx)

	_, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: "missing",
		RunID:     "run-setup-failure",
		Prompt:    "hello",
	})
	require.Error(t, err)

	select {
	case got := <-ch:
		assert.Equal(t, "run-setup-failure", got.Payload.RunID)
		assert.NotEmpty(t, got.Payload.Error)
		assert.False(t, got.Payload.Cancelled)
	case <-time.After(2 * time.Second):
		t.Fatal("a run that failed during setup must publish RunComplete")
	}
}

// TestCancel_TwoAcceptedBothObserveCancellation covers the second review
// finding: single 取消 用 two accepted-不-yet-活跃 prompts must
// 取消 both. 取消 raises 会话's high-water 标记 到
// latest 受理 序号, 所以 every 提示词 accepted-但-不-yet-活跃 在
// 取消 time 是 covered 和 both take 取消-在-条目 路径.
func TestCancel_TwoAcceptedBothObserveCancellation(t *testing.T) {
	t.Parallel()
	sa, env := newCancelTestAgent(t)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	// Two prompts 是 accepted-但-不-yet-活跃 为 same 会话.
	accept1 := sa.BeginAccepted(sess.ID)
	accept2 := sa.BeginAccepted(sess.ID)
	require.Equal(t, 2, sa.acceptedCount(sess.ID))

	// 一个 single 取消 arrives before either becomes 活跃.
	sa.Cancel(sess.ID)
	require.Equal(t, accept2.seq, sa.pendingCancelMark(sess.ID),
		"one cancel must mark every currently-accepted prompt as canceled")
	require.GreaterOrEqual(t, sa.pendingCancelMark(sess.ID), accept1.seq,
		"the mark must cover the earlier accepted prompt too")

	// Both prompts enter 运行; each must take 取消-在-条目, 不 运行.
	r1, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		Prompt:    "first",
		Accepted:  accept1,
	})
	require.NoError(t, err)
	require.Nil(t, r1)

	r2, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		Prompt:    "second",
		Accepted:  accept2,
	})
	require.NoError(t, err)
	require.Nil(t, r2)

	require.False(t, sa.hasPendingCancel(sess.ID),
		"both reserved units must be consumed")
	require.Equal(t, 0, sa.acceptedCount(sess.ID),
		"both accept reservations must be released")

	// Each canceled-在-条目 turn writes 用户 + canceled 助手
	// 消息, 和 neither 提示词 was enqueued 到 运行 normally.
	require.Equal(t, 0, sa.QueuedPrompts(sess.ID),
		"neither accepted prompt may be enqueued to run normally")
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 4, "two canceled turns produce two user + two assistant messages")
	var canceled int
	for _, m := range msgs {
		if m.Role == message.Assistant {
			assert.Equal(t, message.FinishReasonCanceled, m.FinishReason())
			canceled++
		}
	}
	require.Equal(t, 2, canceled, "both turns must finish canceled")
}

// TestRun_IdleCancelDoesNotPoisonNextPrompt covers 空闲-取消
// no-op guarantee end-到-end: Escape 在 空闲 会话 must 不
// record pending 取消 那 leaks into next accepted 提示词, which
// must 运行 normally 到 completion.
func TestRun_IdleCancelDoesNotPoisonNextPrompt(t *testing.T) {
	t.Parallel()
	sa, env := newStreamTestAgent(t)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	// 空闲 Escape: no accepted 运行, no 活跃 请求.
	sa.Cancel(sess.ID)
	require.False(t, sa.hasPendingCancel(sess.ID),
		"idle cancel must not record a pending cancel")

	// next accepted 提示词 must 运行 normally, 不 取消 在 条目.
	accept := sa.BeginAccepted(sess.ID)
	result, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		Prompt:    "next",
		Accepted:  accept,
	})
	require.NoError(t, err)
	require.NotNil(t, result, "next prompt must run normally after an idle cancel")

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, message.User, msgs[0].Role)
	assert.Equal(t, message.Assistant, msgs[1].Role)
	assert.Equal(t, message.FinishReasonEndTurn, msgs[1].FinishReason(),
		"the prompt must finish normally, not canceled")
}

// TestCancel_AcceptedAfterCancelIsNotPoisoned 是 regression 测试 为
// reviewer's finding: counted 会话-level pending 取消 let
// 提示词 accepted after 取消 enter 运行 first 和 consume unit
// reserved 为 earlier prompts. With 序号 high-water 标记,
// single 取消 covers exactly prompts accepted-但-不-yet-活跃 在
// 取消 time (一个 和 B); 提示词 accepted afterwards (C) gets higher
// 序号 和 must 运行 normally without consuming 一个 或 B's cancellation.
// C 是 运行 first 到 prove it neither cancels nor drains 标记, then 一个
// 和 B 是 运行 和 must both 取消 在 条目.
func TestCancel_AcceptedAfterCancelIsNotPoisoned(t *testing.T) {
	t.Parallel()
	sa, env := newStreamTestAgent(t)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	// 一个 和 B 是 accepted-但-不-yet-活跃.
	acceptA := sa.BeginAccepted(sess.ID)
	acceptB := sa.BeginAccepted(sess.ID)

	// One 取消 arrives covering both 一个 和 B.
	sa.Cancel(sess.ID)
	require.True(t, sa.hasPendingCancel(sess.ID))
	require.Equal(t, acceptB.seq, sa.pendingCancelMark(sess.ID),
		"the mark must cover every prompt accepted before the cancel")

	// C 是 accepted AFTER 取消; 它的 序号 是 above 标记.
	acceptC := sa.BeginAccepted(sess.ID)
	require.Greater(t, acceptC.seq, sa.pendingCancelMark(sess.ID),
		"a prompt accepted after the cancel must not be covered by the mark")

	// 运行 C first. 它 must 运行 normally 到 completion 和 must 不
	// consume 或 clear cancellation reserved 为 一个 和 B.
	rc, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		Prompt:    "C",
		Accepted:  acceptC,
	})
	require.NoError(t, err)
	require.NotNil(t, rc, "C was accepted after the cancel and must run normally")
	require.True(t, sa.hasPendingCancel(sess.ID),
		"running C must not drain the cancellation reserved for A and B")

	// Now 一个 和 B 运行. Both were accepted before 取消 和 must
	// take 取消-在-条目 路径.
	ra, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		Prompt:    "A",
		Accepted:  acceptA,
	})
	require.NoError(t, err)
	require.Nil(t, ra, "A must cancel on entry, not run")

	rb, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		Prompt:    "B",
		Accepted:  acceptB,
	})
	require.NoError(t, err)
	require.Nil(t, rb, "B must cancel on entry, not run")

	require.False(t, sa.hasPendingCancel(sess.ID),
		"the mark clears once all covered handles are resolved")
	require.Equal(t, 0, sa.acceptedCount(sess.ID))
	require.Equal(t, 0, sa.QueuedPrompts(sess.ID),
		"neither A nor B may be enqueued to run normally")

	// C produced normal turn; 一个 和 B each produced canceled turn.
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 6, "C normal + A canceled + B canceled = 3 user + 3 assistant")

	var normal, canceled int
	for _, m := range msgs {
		if m.Role != message.Assistant {
			continue
		}
		switch m.FinishReason() {
		case message.FinishReasonEndTurn:
			normal++
		case message.FinishReasonCanceled:
			canceled++
		}
	}
	require.Equal(t, 1, normal, "only C finished normally")
	require.Equal(t, 2, canceled, "both A and B finished canceled")
}
