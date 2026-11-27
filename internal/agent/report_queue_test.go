package agent

import (
	"errors"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// 一个 报告 是 saved before parent 是 woken, 所以 turn 那 fails 到
// start, 或 是 cancelled, cannot lose it.
func TestReportIsSavedEvenWhenWakingFails(t *testing.T) {
	t.Parallel()
	c, env := newDispatchTestCoordinator(t)
	c.readyWg.Go(func() error { return errors.New("not ready") })
	parent, err := env.sessions.Create(t.Context(), "parent")
	require.NoError(t, err)
	entry := newDispatchedAgent("betty-review", accessRead, "child", parent.ID, newMockAgent("p", 4096, nil))

	c.reportDispatchResult(entry, fantasy.NewTextResponse("all good"), nil)

	msgs, err := env.messages.List(t.Context(), parent.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	reports := msgs[0].SubAgentReports()
	require.Len(t, reports, 1)
	require.Equal(t, "all good", reports[0].Output)
}

// 一个 报告 调用 writes 报告 当 it 是 delivered.
func TestReportCallWritesTheReport(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "parent")
	require.NoError(t, err)
	report := message.SubAgentReport{Label: "akiko-bugs", Output: "all good"}
	got, err := a.createUserMessage(t.Context(), SessionAgentCall{SessionID: sess.ID, Report: &report}, "")
	require.NoError(t, err)
	require.Equal(t, []message.SubAgentReport{report}, got.SubAgentReports())
}

// 一个 报告 为 busy 会话 waits 在 队列, out 的 transcript,
// until turn reads it, 和 取消 那 drops it still records it.
func TestQueuedReportReachesTheTranscriptOnlyWhenDeliveredOrDropped(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "parent")
	require.NoError(t, err)
	a.activeRequests.Set(sess.ID, &activeCancel{cancel: func() {}})

	report := message.SubAgentReport{Label: "akiko-bugs", Output: "all good"}
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: report.PromptText(), Report: &report})
	require.NoError(t, err)
	require.Equal(t, 1, a.QueuedPrompts(sess.ID))
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Empty(t, msgs, "a queued report is not in the transcript yet")

	a.ClearQueue(sess.ID)
	msgs, err = env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, []message.SubAgentReport{report}, msgs[0].SubAgentReports())
}

// 用户's own queued prompts go ahead 的 queued reports, 在 order, 所以
// short line 是 不 buried 在 end 的 long 报告.
func TestUserPromptsQueueAheadOfReports(t *testing.T) {
	t.Parallel()
	report := func(label string) SessionAgentCall {
		return SessionAgentCall{Prompt: label, Report: &message.SubAgentReport{Label: label}}
	}
	var queue []SessionAgentCall
	for _, call := range []SessionAgentCall{
		{Prompt: "first"},
		report("r1"),
		report("r2"),
		{Prompt: "pw hunter2"},
		report("r3"),
		{Prompt: "and another"},
	} {
		queue = insertQueued(queue, call)
	}
	var got []string
	for _, call := range queue {
		got = append(got, call.Prompt)
	}
	require.Equal(t, []string{"first", "pw hunter2", "and another", "r1", "r2", "r3"}, got)
}

// 一个 队列-只 调用 never starts turn: 在 空闲 会话 it 是 refused,
// 和 在 busy one it 是 queued.
func TestQueueOnlyCallNeverStartsATurn(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "child")
	require.NoError(t, err)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hi", QueueOnly: true})
	require.ErrorIs(t, err, ErrSessionIdle)
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Empty(t, msgs, "an idle session gets no turn")

	a.activeRequests.Set(sess.ID, &activeCancel{cancel: func() {}})
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hi", QueueOnly: true})
	require.NoError(t, err)
	require.Equal(t, 1, a.QueuedPrompts(sess.ID))
}

// 一个 队列-只 调用 那 was accepted into 队列 但 missed every step
// still runs 作为 它的 own turn 当 队列 drains, instead 的 being refused
// 作为 空闲.
func TestQueuedCallDropsQueueOnly(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		Models:   noModels,
		Sessions: env.sessions,
		Messages: env.messages,
	}).(*sessionAgent)
	a.enqueueCall(SessionAgentCall{SessionID: "s", Prompt: "more", QueueOnly: true})
	queued, ok := a.messageQueue.Get("s")
	require.True(t, ok)
	require.Len(t, queued, 1)
	require.False(t, queued[0].QueueOnly)
}
