package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// gatedStreamModel streams single 文本 片段 followed 通过 clean finish,
// 但 blocks very first 流 调用 until 它的 gate 是 released. That
// lets 测试 hold 运行 "活跃" (past PrepareStep, inside 流) just
// long enough 到 enqueue follow-up 提示词 behind busy 会话.
// Subsequent 流 calls (e.g. recursive 运行 draining 队列)
// proceed immediately.
type gatedStreamModel struct {
	text    string
	gate    chan struct{}
	entered chan struct{}
	calls   atomic.Int64
}

func (m *gatedStreamModel) Provider() string { return "fake" }
func (m *gatedStreamModel) Model() string    { return "fake-model" }

func (m *gatedStreamModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: m.text}},
		FinishReason: fantasy.FinishReasonStop,
	}, nil
}

func (m *gatedStreamModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	if m.calls.Add(1) == 1 {
		close(m.entered)
		select {
		case <-m.gate:
		case <-ctx.Done():
		}
	}
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

func (m *gatedStreamModel) GenerateObject(ctx context.Context, call fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *gatedStreamModel) StreamObject(ctx context.Context, call fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

// TestRun_QueuedRunIDPromptRunsRecursivelyAndPublishesRunComplete 是
// end-到-end proof 的 fix 2: 提示词 carrying RunID 那 是 queued
// behind busy 会话 must NOT 是 silently folded into 活跃 turn.
// 它 runs 作为 它的 own turn via recursive 运行 路径 和 publishes 它的
// own 终态 RunComplete, 所以 `crush 运行` caller blocking 在 那
// RunID does 不 hang. 活跃 turn keeps 它的 own RunComplete too.
func TestRun_QueuedRunIDPromptRunsRecursivelyAndPublishesRunComplete(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	broker := pubsub.NewBroker[notify.RunComplete]()
	t.Cleanup(broker.Shutdown)

	large := &gatedStreamModel{
		text:    "done",
		gate:    make(chan struct{}),
		entered: make(chan struct{}),
	}
	small := &finishStreamModel{text: "title"}

	sa := NewSessionAgent(SessionAgentOptions{
		Cfg: testConfigStore(),
		Models: fixedModels(
			Model{Model: large, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}},
			Model{Model: small, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}},
		),
		Sessions:    env.sessions,
		Messages:    env.messages,
		RunComplete: broker,
	}).(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	subCtx, subCancel := context.WithCancel(t.Context())
	defer subCancel()
	ch := broker.Subscribe(subCtx)

	// Start main turn; it blocks inside 流 once 活跃.
	mainDone := make(chan error, 1)
	go func() {
		_, runErr := sa.Run(t.Context(), SessionAgentCall{
			SessionID: sess.ID,
			RunID:     "run-main",
			Prompt:    "main",
		})
		mainDone <- runErr
	}()

	// Wait until main turn 是 活跃 (inside 流).
	select {
	case <-large.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("main run never entered Stream")
	}
	require.True(t, sa.HasActiveTurn(sess.ID), "main run must be active before enqueueing the follow-up")

	// Enqueue RunID-bearing follow-up behind busy 会话.
	res, err := sa.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		RunID:     "run-follow",
		Prompt:    "follow",
	})
	require.NoError(t, err)
	require.Nil(t, res, "a busy-session follow-up must enqueue and return (nil, nil)")
	require.Equal(t, 1, sa.QueuedPrompts(sess.ID), "the follow-up must be queued, not folded")

	// Release main turn 所以 it completes 和 hands off 到 队列.
	close(large.gate)
	require.NoError(t, <-mainDone)

	// Both turns must 发布 their own 终态 RunComplete.
	got := map[string]notify.RunComplete{}
	deadline := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case ev := <-ch:
			got[ev.Payload.RunID] = ev.Payload
		case <-deadline:
			t.Fatalf("timed out waiting for both RunCompletes; got %v", got)
		}
	}

	main, ok := got["run-main"]
	require.True(t, ok, "the active turn must publish its own RunComplete")
	require.Empty(t, main.Error)
	require.False(t, main.Cancelled)

	follow, ok := got["run-follow"]
	require.True(t, ok,
		"the queued RunID prompt must publish its own RunComplete instead of being folded silently")
	require.Empty(t, follow.Error)
	require.False(t, follow.Cancelled)
	require.Equal(t, "done", follow.Text, "the queued prompt ran as its own turn")

	// Two distinct 助手 turns prove follow-up was 不 folded.
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var assistants, follows int
	for _, m := range msgs {
		switch m.Role {
		case message.Assistant:
			assistants++
		case message.User:
			if m.Content().String() == "follow" {
				follows++
			}
		}
	}
	require.Equal(t, 2, assistants, "the active turn and the recursive turn each produce one assistant message")
	require.Equal(t, 1, follows, "the follow-up prompt is its own user turn")
}
