package message

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// slowUpdateQuerier wraps [db.Querier] 和 forces UpdateMessage 到
// hang 在 release 渠道. Used 到 simulate 在-flight SQL 写入.
type slowUpdateQuerier struct {
	db.Querier
	release   chan struct{}
	started   chan struct{}
	startOnce sync.Once
}

func (s *slowUpdateQuerier) UpdateMessage(ctx context.Context, arg db.UpdateMessageParams) error {
	s.startOnce.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Querier.UpdateMessage(ctx, arg)
}

// newTestService spins up fresh 在-memory 消息.Service backed 通过
// temporary 在-disk SQLite 数据库. Returns service plus 会话
// ID 到 attach messages 到.
func newTestService(t *testing.T, opts ...ServiceOption) (Service, string) {
	t.Helper()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	sess, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)

	svc := NewService(q, opts...)
	return svc, sess.ID
}

// eventCollector consumes 总线 events into slice 在 goroutine
// 和 exposes thread-safe Snapshot / Reset helpers 为 assertions.
type eventCollector struct {
	mu     sync.Mutex
	events []pubsub.Event[Message]
}

func collect(ctx context.Context, sub <-chan pubsub.Event[Message]) *eventCollector {
	c := &eventCollector{}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-sub:
				if !ok {
					return
				}
				c.mu.Lock()
				c.events = append(c.events, ev)
				c.mu.Unlock()
			}
		}
	}()
	return c
}

func (c *eventCollector) snapshot() []pubsub.Event[Message] {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]pubsub.Event[Message], len(c.events))
	copy(out, c.events)
	return out
}

// dropCreated waits 为 CreatedEvent 那 创建 publishes, then clears
// it, 所以 it cannot land after reset 和 是 mistaken 为 later 事件.
func (c *eventCollector) dropCreated(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(c.snapshot()) >= 1
	}, time.Second, time.Millisecond)
	require.Equal(t, pubsub.CreatedEvent, c.snapshot()[0].Type)
	c.reset()
}

func (c *eventCollector) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = nil
}

func TestUpdate_DebouncesTextDeltas(t *testing.T) {
	t.Parallel()

	// Long-enough debounce 那 we can verify nothing flushes prematurely.
	svc, sessionID := newTestService(t, WithDebounce(50*time.Millisecond))

	subCtx, cancelSub := context.WithCancel(t.Context())
	defer cancelSub()
	sub := svc.Subscribe(subCtx)
	collector := collect(subCtx, sub)

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role: Assistant,
	})
	require.NoError(t, err)
	collector.dropCreated(t)

	// Push 5 deltas inside single debounce 窗口.
	for i := 0; i < 5; i++ {
		msg.AppendContent("a")
		require.NoError(t, svc.Update(t.Context(), msg))
	}

	// Before debounce 过期 no UpdatedEvent 应当 have landed.
	time.Sleep(10 * time.Millisecond)
	require.Empty(t, collector.snapshot(), "no events should land before debounce window expires")

	// Wait 为 debounce timer 到 fire.
	require.Eventually(t, func() bool {
		return len(collector.snapshot()) >= 1
	}, time.Second, 5*time.Millisecond)
	events := collector.snapshot()
	require.Len(t, events, 1, "5 deltas should coalesce into 1 UpdatedEvent")
	require.Equal(t, pubsub.UpdatedEvent, events[0].Type)
	require.Equal(t, "aaaaa", events[0].Payload.Content().Text)

	// Final state must 是 persisted.
	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, "aaaaa", got.Content().Text)
}

func TestUpdate_TerminalUpdatesFlushSynchronously(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))

	subCtx, cancelSub := context.WithCancel(t.Context())
	defer cancelSub()
	sub := svc.Subscribe(subCtx)
	collector := collect(subCtx, sub)

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	collector.dropCreated(t)

	// AddFinish makes 消息 终态; 更新 must 刷盘
	// synchronously even 用 1-hour debounce.
	msg.AppendContent("done")
	msg.AddFinish(FinishReasonEndTurn, "", "")
	require.NoError(t, svc.Update(t.Context(), msg))

	require.Eventually(t, func() bool {
		return len(collector.snapshot()) >= 1
	}, time.Second, 5*time.Millisecond,
		"terminal update must publish without waiting for debounce")
	events := collector.snapshot()
	require.Len(t, events, 1)
	require.True(t, events[0].Payload.IsFinished())

	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.True(t, got.IsFinished())
}

func TestUpdate_ToolCallStructuralChangeFlushes(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)

	// Adding new 工具调用 是 structural change → sync 刷盘.
	msg.AddToolCall(ToolCall{ID: "tc1", Name: "view", Finished: false})
	require.NoError(t, svc.Update(t.Context(), msg))

	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Len(t, got.ToolCalls(), 1)
	require.Equal(t, "tc1", got.ToolCalls()[0].ID)

	// Marking 工具调用 finished 是 also structural change.
	msg.AddToolCall(ToolCall{ID: "tc1", Name: "view", Input: "{}", Finished: true})
	require.NoError(t, svc.Update(t.Context(), msg))

	got, err = svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.True(t, got.ToolCalls()[0].Finished)
}

func TestUpdate_ReasoningEndFlushes(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)

	// Reasoning deltas alone debounce.
	msg.AppendReasoningContent("hmm")
	require.NoError(t, svc.Update(t.Context(), msg))
	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Empty(t, got.ReasoningContent().Thinking, "reasoning delta should still be in the debounce buffer")

	// FinishThinking sets FinishedAt → 终态 刷盘.
	msg.FinishThinking()
	require.NoError(t, svc.Update(t.Context(), msg))

	got, err = svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, "hmm", got.ReasoningContent().Thinking)
	require.NotZero(t, got.ReasoningContent().FinishedAt)
}

func TestFlush_DrainsPendingDebouncedUpdates(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	msg.AppendContent("buffered")
	require.NoError(t, svc.Update(t.Context(), msg))

	// Without 刷盘 SQL 行 是 unchanged 从 创建.
	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Empty(t, got.Content().Text)

	require.NoError(t, svc.Flush(t.Context(), msg.ID))

	got, err = svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, "buffered", got.Content().Text)

	// Subsequent 刷盘 是 no-op.
	require.NoError(t, svc.Flush(t.Context(), msg.ID))
}

func TestFlushAll_DrainsAllPending(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))

	const n = 5
	msgs := make([]Message, n)
	for i := range msgs {
		m, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		m.AppendContent("hi")
		require.NoError(t, svc.Update(t.Context(), m))
		msgs[i] = m
	}

	require.NoError(t, svc.FlushAll(t.Context()))

	for _, m := range msgs {
		got, err := svc.Get(t.Context(), m.ID)
		require.NoError(t, err)
		require.Equal(t, "hi", got.Content().Text, "FlushAll should drain every pending message")
	}
}

func TestUpdate_OrderingMatchesNonCoalesced(t *testing.T) {
	t.Parallel()

	// Compare the final state after coalesced vs zero-debounce updates.
	// 一个 序号 的 interleaved 文本/reasoning/工具-调用 updates must
	// converge 到 same final DB 行 either way.
	build := func(svc Service, sessionID string) Message {
		msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		msg.AppendReasoningContent("thinking 1 ")
		require.NoError(t, svc.Update(t.Context(), msg))
		msg.AppendReasoningContent("thinking 2")
		require.NoError(t, svc.Update(t.Context(), msg))
		msg.FinishThinking()
		require.NoError(t, svc.Update(t.Context(), msg))
		msg.AppendContent("hello ")
		require.NoError(t, svc.Update(t.Context(), msg))
		msg.AppendContent("world")
		require.NoError(t, svc.Update(t.Context(), msg))
		msg.AddToolCall(ToolCall{ID: "tc", Name: "x", Finished: false})
		require.NoError(t, svc.Update(t.Context(), msg))
		msg.AddToolCall(ToolCall{ID: "tc", Name: "x", Input: "{}", Finished: true})
		require.NoError(t, svc.Update(t.Context(), msg))
		msg.AddFinish(FinishReasonEndTurn, "", "")
		require.NoError(t, svc.Update(t.Context(), msg))
		return msg
	}

	coalesced, sid1 := newTestService(t, WithDebounce(20*time.Millisecond))
	a := build(coalesced, sid1)
	require.NoError(t, coalesced.FlushAll(t.Context()))
	gotA, err := coalesced.Get(t.Context(), a.ID)
	require.NoError(t, err)

	immediate, sid2 := newTestService(t, WithDebounce(0))
	b := build(immediate, sid2)
	gotB, err := immediate.Get(t.Context(), b.ID)
	require.NoError(t, err)

	require.Equal(t, gotA.Content().Text, gotB.Content().Text)
	require.Equal(t, gotA.ReasoningContent().Thinking, gotB.ReasoningContent().Thinking)
	require.Equal(t, len(gotA.ToolCalls()), len(gotB.ToolCalls()))
	require.Equal(t, gotA.IsFinished(), gotB.IsFinished())
}

func TestDelete_DropsPendingState(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))
	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	msg.AppendContent("dropped")
	require.NoError(t, svc.Update(t.Context(), msg))

	require.NoError(t, svc.Delete(t.Context(), msg.ID))

	// FlushAll after 删除 must 不 写入 到 deleted 行.
	require.NoError(t, svc.FlushAll(t.Context()))

	_, err = svc.Get(t.Context(), msg.ID)
	require.Error(t, err, "deleted message must remain deleted")
}

func TestBroker_PublishLossyDropCounter(t *testing.T) {
	t.Parallel()

	// Tiny 渠道 缓冲 所以 we can saturate 从 single sender.
	b := pubsub.NewBrokerWithOptions[int](1)
	defer b.Shutdown()

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sub := b.Subscribe(subCtx)
	require.NotNil(t, sub)

	// Don't 读取 从 sub. Saturate 缓冲.
	for range 100 {
		b.Publish(pubsub.UpdatedEvent, 1)
	}

	require.GreaterOrEqual(t, b.DropCount(), uint64(1),
		"lossy Publish must increment the drop counter under contention")
}

func TestBroker_PublishMustDeliverHonorsTimeout(t *testing.T) {
	t.Parallel()

	b := pubsub.NewBrokerWithOptions[int](1)
	b.SetMustDeliverTimeout(20 * time.Millisecond)
	defer b.Shutdown()

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sub := b.Subscribe(subCtx)
	require.NotNil(t, sub)

	// Saturate: 订阅者's pump holds one 事件 和 它的 队列
	// (capacity 1) next. Keep publishing across several ticks 所以
	// the pump has parked before we stop.
	require.Eventually(t, func() bool {
		b.Publish(pubsub.UpdatedEvent, 1)
		return b.DropCount() >= 3
	}, time.Second, 5*time.Millisecond)

	// PublishMustDeliver should block up 到 20ms then drop.
	start := time.Now()
	b.PublishMustDeliver(t.Context(), pubsub.UpdatedEvent, 2)
	elapsed := time.Since(start)

	require.GreaterOrEqual(t, elapsed, 20*time.Millisecond,
		"PublishMustDeliver should block at least the timeout under contention")
	require.Less(t, elapsed, 200*time.Millisecond,
		"PublishMustDeliver must not block indefinitely")
	require.GreaterOrEqual(t, b.MustDeliverDropCount(), uint64(1),
		"timeout must increment the must-deliver drop counter")
}

func TestBroker_PublishMustDeliverWithReader(t *testing.T) {
	t.Parallel()

	b := pubsub.NewBrokerWithOptions[int](1)
	b.SetMustDeliverTimeout(50 * time.Millisecond)
	defer b.Shutdown()

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sub := b.Subscribe(subCtx)

	var received atomic.Uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-subCtx.Done():
				return
			case _, ok := <-sub:
				if !ok {
					return
				}
				received.Add(1)
			}
		}
	}()

	for i := range 10 {
		b.PublishMustDeliver(t.Context(), pubsub.UpdatedEvent, i)
	}

	// All 10 should land within must-投递 超时 窗口.
	require.Eventually(t, func() bool { return received.Load() == 10 },
		time.Second, 5*time.Millisecond,
		"all must-deliver events should reach an active subscriber")
	require.Zero(t, b.MustDeliverDropCount(),
		"no drops expected when subscriber drains promptly")
}

func TestUpdate_TerminalEventUsesMustDeliver(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sub := svc.Subscribe(subCtx)

	var seenFinish atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-subCtx.Done():
				return
			case ev, ok := <-sub:
				if !ok {
					return
				}
				if ev.Type == pubsub.UpdatedEvent && ev.Payload.IsFinished() {
					seenFinish.Store(true)
				}
			}
		}
	}()

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	msg.AppendContent("final")
	msg.AddFinish(FinishReasonEndTurn, "", "")
	require.NoError(t, svc.Update(t.Context(), msg))

	require.Eventually(t, func() bool { return seenFinish.Load() },
		time.Second, 10*time.Millisecond,
		"terminal update must reach subscribers via the must-deliver path")
}

func TestUpdate_ZeroDebounceFlushesEveryUpdate(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(0))

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		msg.AppendContent("x")
		require.NoError(t, svc.Update(t.Context(), msg))
		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Len(t, got.Content().Text, i+1, "every update must land synchronously when debounce is 0")
	}
}

// TestFlush_WaitsForInFlightWrite reproduces failure where 刷盘
// 或 FlushAll could return before concurrent 在-flight SQL 写入
// completed. We block UpdateMessage 在 release 渠道, fire
// debounce timer, then 调用 刷盘 和 assert it does 不 return until
// 在-flight 写入 actually lands.
func TestFlush_WaitsForInFlightWrite(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	sess, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)

	slow := &slowUpdateQuerier{
		Querier: q,
		release: make(chan struct{}),
		started: make(chan struct{}),
	}
	// Short debounce 所以 timer fires quickly.
	svc := NewService(slow, WithDebounce(10*time.Millisecond))

	msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	msg.AppendContent("payload")
	require.NoError(t, svc.Update(t.Context(), msg))

	// Wait 为 timer-fired 刷盘 到 enter UpdateMessage.
	select {
	case <-slow.started:
	case <-time.After(time.Second):
		t.Fatal("timer-fired flush never reached UpdateMessage")
	}

	// At 这 point 缓冲 是 dirty=false 但 flushing=true. 一个
	// naive 刷盘 would early-return 在 !dirty. Spawn 刷盘 在
	// goroutine 和 assert it has 不 returned while 写入 是
	// still blocked.
	flushDone := make(chan error, 1)
	go func() { flushDone <- svc.Flush(t.Context(), msg.ID) }()

	select {
	case err := <-flushDone:
		t.Fatalf("Flush returned %v while in-flight write was still blocked", err)
	case <-time.After(50 * time.Millisecond):
		// Expected: 刷盘 是 correctly waiting.
	}

	// Release slow 写入; 刷盘 must now return cleanly.
	close(slow.release)
	select {
	case err := <-flushDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Flush did not return after in-flight write completed")
	}

	// SQL 行 should now reflect buffered payload.
	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, "payload", got.Content().Text)
}

// TestFlushAll_WaitsForInFlightWrite asserts FlushAll picks up IDs
// whose 缓冲 是 currently flushing (dirty=false) 所以 shutdown 和
// 会话-switch callers don't return while SQL 写入 是 mid-flight.
func TestFlushAll_WaitsForInFlightWrite(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	sess, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)

	slow := &slowUpdateQuerier{
		Querier: q,
		release: make(chan struct{}),
		started: make(chan struct{}),
	}
	svc := NewService(slow, WithDebounce(10*time.Millisecond))

	msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	msg.AppendContent("payload")
	require.NoError(t, svc.Update(t.Context(), msg))

	select {
	case <-slow.started:
	case <-time.After(time.Second):
		t.Fatal("timer-fired flush never reached UpdateMessage")
	}

	flushDone := make(chan error, 1)
	go func() { flushDone <- svc.FlushAll(t.Context()) }()

	select {
	case err := <-flushDone:
		t.Fatalf("FlushAll returned %v while in-flight write was still blocked", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(slow.release)
	select {
	case err := <-flushDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("FlushAll did not return after in-flight write completed")
	}

	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, "payload", got.Content().Text)
}

// TestUpdate_StructuralFlushUsesMustDeliver covers the second review
// finding: structural 终态 events (工具-调用 add, 工具-调用
// finish, reasoning end) must 发布 via must-投递 路径 even
// 当 消息 itself 是 不 yet IsFinished.
//
// We detect which 路径 was taken 通过 saturating 订阅者's 渠道
// 缓冲 用 no reader. With short must-投递 超时,
// must-投递 路径 increments [pubsub.总线.MustDeliverDropCount]
// after 超时 expires; lossy 路径 increments
// [pubsub.总线.DropCount] immediately. two counters 是
// disjoint, 所以 they precisely identify which 调用 site published
// 事件.
func TestUpdate_StructuralFlushUsesMustDeliver(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mut  func(*Message)
	}{
		{
			name: "tool call add",
			mut: func(m *Message) {
				m.AddToolCall(ToolCall{ID: "tc1", Name: "view"})
			},
		},
		{
			name: "tool call finish",
			mut: func(m *Message) {
				m.AddToolCall(ToolCall{ID: "tc1", Name: "view", Input: "{}", Finished: true})
			},
		},
		{
			name: "reasoning end",
			mut: func(m *Message) {
				m.AppendReasoningContent("hmm")
				m.FinishThinking()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			conn, err := db.Connect(t.Context(), t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })

			q := db.New(conn)
			sessions := session.NewService(q, conn)
			sess, err := sessions.Create(t.Context(), "test")
			require.NoError(t, err)

			// Replace 默认 总线 用 tiny 缓冲 + short
			// must-投递 超时 所以 we can fully saturate 从
			// single sender 和 observe drops without long waits.
			svc := NewService(q, WithDebounce(time.Hour))
			impl := svc.(*service)
			impl.Shutdown()
			impl.Broker = pubsub.NewBrokerWithOptions[Message](1)
			impl.SetMustDeliverTimeout(40 * time.Millisecond)

			subCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sub := svc.Subscribe(subCtx)

			msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
			require.NoError(t, err)

			// Saturate 订阅者, which we never 读取: 它的 pump
			// holds one 事件 和 它的 队列 (capacity 1) next.
			// Keep publishing fillers across several ticks 所以 pump
			// has parked before we stop.
			_ = sub // intentionally not drained.
			require.Eventually(t, func() bool {
				impl.Publish(pubsub.CreatedEvent, Message{})
				return impl.DropCount() >= 3
			}, time.Second, 5*time.Millisecond)
			drops := impl.DropCount()

			// Drive structural change. With debounce=1h, 更新
			// flushes synchronously 和 routes through whichever
			// 发布 路径 service chose 为 structural events.
			tc.mut(&msg)
			require.NoError(t, svc.Update(t.Context(), msg))

			// Must-投递 超时 (40ms) should have expired 用
			// no drain. If structural events 是 routed through
			// must-投递: MustDeliverDropCount > 0, DropCount
			// unchanged. If routed through lossy 发布:
			// DropCount > 0, MustDeliverDropCount == 0.
			require.Eventually(t, func() bool {
				return impl.MustDeliverDropCount() >= 1
			}, time.Second, 5*time.Millisecond,
				"structural terminal event should publish via must-deliver, not lossy Publish")
			require.Equal(t, drops, impl.DropCount(),
				"structural terminal event must not be silently dropped via lossy Publish")
		})
	}
}

// TestPendingStateDoesNotRetainPayload guards fix 为
// unbounded heap growth reported 在 #3162: pending entries live 为
// life 的 service, 所以 once 消息 是 fully flushed they must
// hold bookkeeping 只, never 消息 parts.
func TestPendingStateDoesNotRetainPayload(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Millisecond))
	s := svc.(*service)

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role:  Assistant,
		Parts: []ContentPart{},
	})
	require.NoError(t, err)

	msg.AppendReasoningContent(strings.Repeat("x", 64*1024))
	msg.AddToolCall(ToolCall{ID: "tc-1", Name: "bash", Finished: true})
	msg.AddFinish(FinishReasonEndTurn, "", "")
	require.NoError(t, svc.Update(t.Context(), msg))
	require.NoError(t, svc.Flush(t.Context(), msg.ID))

	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[msg.ID]
	require.NotNil(t, p)
	require.False(t, p.dirty)
	require.Empty(t, p.latest.Parts, "flushed pending state still pins the message parts")
	require.True(t, p.hasFlushed)
	require.Equal(t, []bool{true}, p.baseline.toolCallsFinished)
}

// TestShouldFlushNow_AgainstCompactBaseline pins 终态 detection 到
// projection 存储 在 flushBaseline rather than 到 full 消息 snapshot.
//
// The existing structural tests all assert the positive direction, which a
// broken projection would still satisfy: empty baseline yields 工具-调用
// length mismatch, 所以 it flushes 为 wrong reason. load-bearing case
// 是 negative one — after 刷盘, delta 那 changes nothing structural
// must stay buffered. That 只 holds 如果 baseline carries prior
// finished flags 和 reasoning finish time accurately.
func TestShouldFlushNow_AgainstCompactBaseline(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (Service, Message) {
		t.Helper()
		svc, sessionID := newTestService(t, WithDebounce(time.Hour))
		msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)

		// Land baseline: one unfinished 工具调用, reasoning still open.
		msg.AppendReasoningContent("thinking")
		msg.AddToolCall(ToolCall{ID: "tc1", Name: "view", Finished: false})
		require.NoError(t, svc.Update(t.Context(), msg))
		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Len(t, got.ToolCalls(), 1)
		require.False(t, got.ToolCalls()[0].Finished)
		return svc, msg
	}

	t.Run("non-structural delta stays buffered", func(t *testing.T) {
		t.Parallel()
		svc, msg := setup(t)

		// Same 工具调用, same finished flag, reasoning still open. 一个
		// baseline 那 lost projection would see length 或 flag
		// mismatch here 和 刷盘.
		msg.AppendReasoningContent(" some more")
		require.NoError(t, svc.Update(t.Context(), msg))

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Equal(t, "thinking", got.ReasoningContent().Thinking,
			"a non-structural delta must not trigger a sync flush against the compact baseline")
	})

	t.Run("finished flip flushes", func(t *testing.T) {
		t.Parallel()
		svc, msg := setup(t)

		msg.AddToolCall(ToolCall{ID: "tc1", Name: "view", Input: "{}", Finished: true})
		require.NoError(t, svc.Update(t.Context(), msg))

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.True(t, got.ToolCalls()[0].Finished)
	})

	t.Run("new tool call flushes", func(t *testing.T) {
		t.Parallel()
		svc, msg := setup(t)

		msg.AddToolCall(ToolCall{ID: "tc2", Name: "bash", Finished: false})
		require.NoError(t, svc.Update(t.Context(), msg))

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Len(t, got.ToolCalls(), 2)
	})

	t.Run("reasoning end flushes", func(t *testing.T) {
		t.Parallel()
		svc, msg := setup(t)

		msg.FinishThinking()
		require.NoError(t, svc.Update(t.Context(), msg))

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.NotZero(t, got.ReasoningContent().FinishedAt)
	})
}

// drainEvents reads until no 事件 arrives 为 short while.
func drainEvents(sub <-chan pubsub.Event[Message]) []pubsub.Event[Message] {
	var got []pubsub.Event[Message]
	for {
		select {
		case ev := <-sub:
			got = append(got, ev)
		case <-time.After(100 * time.Millisecond):
			return got
		}
	}
}

// TestStalledSubscriberHoldsOnlyNewestUpdate guards against a slow
// 订阅者 retaining one full snapshot per streamed 更新.
func TestStalledSubscriberHoldsOnlyNewestUpdate(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(0))
	sub := svc.Subscribe(t.Context())

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	chunk := strings.Repeat("x", 1024)
	const n = 100
	for range n {
		msg.AppendContent(chunk)
		require.NoError(t, svc.Update(t.Context(), msg))
	}

	got := drainEvents(sub)
	require.Len(t, got, 2)
	require.Equal(t, pubsub.CreatedEvent, got[0].Type)
	require.Equal(t, pubsub.UpdatedEvent, got[1].Type)
	require.Len(t, got[1].Payload.Content().Text, n*len(chunk))

	// 终态 updates 是 must-投递 和 still arrive.
	msg.AddFinish(FinishReasonEndTurn, "", "")
	require.NoError(t, svc.Update(t.Context(), msg))
	got = drainEvents(sub)
	require.Len(t, got, 1)
	require.True(t, got[0].MustDeliver)
	require.True(t, got[0].Payload.IsFinished())
	require.Zero(t, svc.(*service).DropCount())
}
