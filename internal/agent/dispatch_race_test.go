package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// concurrencyProbeModel records maximum number 的 流 iterators 在
// flight 在 once. Each 流 blocks 在 release, 所以 second 派发 那
// wrongly started concurrent 运行 为 same 会话 是 observable 作为
// 在-flight count above one.
type concurrencyProbeModel struct {
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	entered  chan struct{}
	release  chan struct{}
}

func (m *concurrencyProbeModel) Provider() string { return "fake" }
func (m *concurrencyProbeModel) Model() string    { return "fake-model" }

func (m *concurrencyProbeModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: "done"}},
		FinishReason: fantasy.FinishReasonStop,
	}, nil
}

func (m *concurrencyProbeModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return func(yield func(fantasy.StreamPart) bool) {
		cur := m.inFlight.Add(1)
		for {
			mx := m.maxSeen.Load()
			if cur <= mx || m.maxSeen.CompareAndSwap(mx, cur) {
				break
			}
		}
		// Signal 那 流 是 在 flight (non-blocking), then hold here
		// 所以 racing second 派发 would 是 caught 通过 maxSeen.
		select {
		case m.entered <- struct{}{}:
		default:
		}
		<-m.release
		m.inFlight.Add(-1)

		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "1", Delta: "done"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

func (m *concurrencyProbeModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *concurrencyProbeModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

// fastModel 是 non-blocking 模型 used 作为 small 模型 在 concurrency
// tests. GenerateTitle runs 在 small 模型; 如果 it shares probe 模型,
// 它的 流 调用 races into inFlight/maxSeen 和 produces spurious failures.
type fastModel struct{}

func (fastModel) Provider() string { return "fake" }
func (fastModel) Model() string    { return "fake-model" }

func (fastModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: "title"}},
		FinishReason: fantasy.FinishReasonStop,
	}, nil
}

func (fastModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "1", Delta: "title"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

func (fastModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (fastModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

// TestRun_ConcurrentInProcessDispatchStartsOneRun fires burst 的 concurrent
// 在-process 运行 calls ( 路径 渠道 events use) 在 空闲 会话. Only
// one may become 活跃 运行; rest must 队列 behind it. Before
// 派发 decision was serialized under per-会话 互斥锁, two callers
// could both pass busy check 和 start two runs 在 same 会话 — 这
// 测试 catches 那 regression (maxSeen would exceed one).
//
// fastModel 是 used 作为 small 模型 所以 GenerateTitle (which runs 在
// small 模型) does 不 race into probe's inFlight/maxSeen counters.
// 队列 count 是 不 asserted because PrepareStep drains queued prompts into
// 活跃 step before 模型's 流 是 called — 通过 time "entered"
// fires 队列 是 already empty 通过 design.
func TestRun_ConcurrentInProcessDispatchStartsOneRun(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	model := &concurrencyProbeModel{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	sa := testSessionAgent(env, model, fastModel{}, "system").(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	const n = 8
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = sa.Run(t.Context(), SessionAgentCall{
				SessionID: sess.ID,
				Prompt:    "event",
			})
		}()
	}

	// Wait until 活跃 运行's 流 是 在 flight (blocked 在 release).
	select {
	case <-model.entered:
	case <-time.After(5 * time.Second):
		close(model.release)
		wg.Wait()
		t.Fatal("no run became active")
	}

	// Every other 派发 must have either queued (和 been folded into
	// 活跃 step 通过 PrepareStep) 或 never started 它的 own 流. Either way,
	// 在 most one 流 may 是 在 flight 和 high-water 标记 must 是 one.
	require.Equal(t, int32(1), model.inFlight.Load(), "exactly one run may be active")
	require.Equal(t, int32(1), model.maxSeen.Load(), "no two runs may stream concurrently for one session")

	close(model.release)
	wg.Wait()
}
