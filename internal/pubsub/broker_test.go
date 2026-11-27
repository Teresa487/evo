package pubsub

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type snap struct {
	ID   string
	V    int
	Data []byte
}

func snapKey(ev Event[snap]) string {
	if ev.Type != UpdatedEvent {
		return ""
	}
	return ev.Payload.ID
}

func newTestCoalescingBroker(t *testing.T) *Broker[snap] {
	t.Helper()
	b := NewCoalescingBroker(snapKey)
	t.Cleanup(b.Shutdown)
	return b
}

// subscribeStalled subscribes without reading, then parks pump 在
// blocker 事件 所以 every later 发布 stays 在 队列.
func subscribeStalled(t *testing.T, b *Broker[snap]) (*subscription[snap], <-chan Event[snap]) {
	t.Helper()
	ch := b.Subscribe(t.Context())
	b.mu.RLock()
	var sub *subscription[snap]
	for s := range b.subs {
		if s.out == ch {
			sub = s
		}
	}
	b.mu.RUnlock()
	require.NotNil(t, sub)

	b.Publish(CreatedEvent, snap{ID: "blocker"})
	require.Eventually(t, func() bool { return len(sub.queue) == 0 }, time.Second, time.Millisecond)
	return sub, ch
}

// drain reads until no 事件 arrives 为 short while.
func drain(t *testing.T, ch <-chan Event[snap]) []Event[snap] {
	t.Helper()
	var got []Event[snap]
	for {
		select {
		case ev := <-ch:
			got = append(got, ev)
		case <-time.After(50 * time.Millisecond):
			return got
		}
	}
}

func describe(evs []Event[snap]) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, fmt.Sprintf("%s:%s:%d:%t", ev.Type, ev.Payload.ID, ev.Payload.V, ev.MustDeliver))
	}
	return out
}

func TestCoalescingBroker_StalledSubscriberKeepsOnlyNewestUpdate(t *testing.T) {
	t.Parallel()

	b := newTestCoalescingBroker(t)
	sub, ch := subscribeStalled(t, b)

	const n = 2000
	for i := 1; i <= n; i++ {
		b.Publish(UpdatedEvent, snap{ID: "a", V: i, Data: make([]byte, 64<<10)})
	}

	// 2000 snapshots 的 64 KiB would 是 125 MiB; 只 one 是 pending.
	require.Len(t, sub.queue, 1)
	sub.mu.Lock()
	require.Len(t, sub.open, 1)
	require.Equal(t, n, sub.open["a"].event.Payload.V)
	sub.mu.Unlock()
	require.Zero(t, b.DropCount())

	require.Equal(t, []string{
		"created:blocker:0:false",
		fmt.Sprintf("updated:a:%d:false", n),
	}, describe(drain(t, ch)))

	sub.mu.Lock()
	require.Empty(t, sub.open, "delivered slots must not stay pinned")
	sub.mu.Unlock()
}

func TestCoalescingBroker_Ordering(t *testing.T) {
	t.Parallel()

	b := newTestCoalescingBroker(t)
	_, ch := subscribeStalled(t, b)

	b.Publish(CreatedEvent, snap{ID: "a"})
	b.Publish(UpdatedEvent, snap{ID: "a", V: 1})
	b.Publish(CreatedEvent, snap{ID: "b"})
	b.Publish(UpdatedEvent, snap{ID: "a", V: 2})
	b.Publish(UpdatedEvent, snap{ID: "b", V: 1})
	b.Publish(UpdatedEvent, snap{ID: "a", V: 3})
	b.Publish(DeletedEvent, snap{ID: "b"})

	// Newer snapshots take slot 的 pending one; Created stays
	// first 和 nothing 为 b follows 它的 Deleted.
	require.Equal(t, []string{
		"created:blocker:0:false",
		"created:a:0:false",
		"updated:a:3:false",
		"created:b:0:false",
		"updated:b:1:false",
		"deleted:b:0:false",
	}, describe(drain(t, ch)))
}

func TestCoalescingBroker_MustDeliverNeverCoalesced(t *testing.T) {
	t.Parallel()

	b := newTestCoalescingBroker(t)
	_, ch := subscribeStalled(t, b)

	b.Publish(UpdatedEvent, snap{ID: "a", V: 1})
	b.PublishMustDeliver(t.Context(), UpdatedEvent, snap{ID: "a", V: 2})
	b.Publish(UpdatedEvent, snap{ID: "a", V: 3})
	b.PublishMustDeliver(t.Context(), UpdatedEvent, snap{ID: "a", V: 4})
	b.PublishMustDeliver(t.Context(), UpdatedEvent, snap{ID: "a", V: 5})
	b.Publish(UpdatedEvent, snap{ID: "a", V: 6})
	b.Publish(UpdatedEvent, snap{ID: "a", V: 7})

	// Older plain updates 是 superseded 通过 must-投递 events after
	// them; must-投递 events all arrive, 在 order, 和 是 flagged.
	require.Equal(t, []string{
		"created:blocker:0:false",
		"updated:a:2:true",
		"updated:a:4:true",
		"updated:a:5:true",
		"updated:a:7:false",
	}, describe(drain(t, ch)))
	require.Zero(t, b.DropCount())
	require.Zero(t, b.MustDeliverDropCount())
}

func TestCoalescingBroker_MustDeliverTimeoutAndTakeover(t *testing.T) {
	t.Parallel()

	b := NewCoalescingBroker(snapKey)
	b.channelBufferSize = 2
	b.SetMustDeliverTimeout(10 * time.Millisecond)
	t.Cleanup(b.Shutdown)
	_, ch := subscribeStalled(t, b)

	b.Publish(UpdatedEvent, snap{ID: "a", V: 1})
	b.Publish(CreatedEvent, snap{ID: "x"})

	// 队列 是 full. Taking over pending 更新 needs no space.
	b.PublishMustDeliver(t.Context(), UpdatedEvent, snap{ID: "a", V: 2})
	require.Zero(t, b.MustDeliverDropCount())

	// With nothing 到 take over, must-投递 事件 times out.
	b.PublishMustDeliver(t.Context(), UpdatedEvent, snap{ID: "b", V: 1})
	require.Equal(t, uint64(1), b.MustDeliverDropCount())

	// takeover removed pending item 从 coalescing, 所以 plain
	// 更新 为 has no slot 到 merge into 和 是 dropped.
	b.Publish(UpdatedEvent, snap{ID: "a", V: 3})
	require.Equal(t, uint64(1), b.DropCount())

	require.Equal(t, []string{
		"created:blocker:0:false",
		"updated:a:2:true",
		"created:x:0:false",
	}, describe(drain(t, ch)))
}

func TestCoalescingBroker_PlainUpdateAfterQueuedMustDeliver(t *testing.T) {
	t.Parallel()

	b := newTestCoalescingBroker(t)
	sub, ch := subscribeStalled(t, b)

	// No pending 更新, 所以 v2 是 queued 在 它的 own 和 never replaceable.
	b.PublishMustDeliver(t.Context(), UpdatedEvent, snap{ID: "a", V: 2})
	sub.mu.Lock()
	require.Empty(t, sub.open)
	sub.mu.Unlock()

	// 一个 newer plain 更新 must 不 是 lost behind it.
	b.Publish(UpdatedEvent, snap{ID: "a", V: 3})
	require.Equal(t, []string{
		"created:blocker:0:false",
		"updated:a:2:true",
		"updated:a:3:false",
	}, describe(drain(t, ch)))
}

func TestCoalescingBroker_MustDeliverTakesOverPendingInPlace(t *testing.T) {
	t.Parallel()

	b := newTestCoalescingBroker(t)
	sub, ch := subscribeStalled(t, b)

	b.Publish(UpdatedEvent, snap{ID: "a", V: 1})
	b.Publish(CreatedEvent, snap{ID: "x"})
	b.PublishMustDeliver(t.Context(), UpdatedEvent, snap{ID: "a", V: 2})

	// v2 reuses v1's 队列 position instead 的 adding item.
	require.Len(t, sub.queue, 2)
	sub.mu.Lock()
	require.Empty(t, sub.open)
	sub.mu.Unlock()

	require.Equal(t, []string{
		"created:blocker:0:false",
		"updated:a:2:true",
		"created:x:0:false",
	}, describe(drain(t, ch)))
}

func TestBroker_ShutdownWithStalledReaderStopsPump(t *testing.T) {
	t.Parallel()

	for _, coalesce := range []bool{false, true} {
		t.Run(fmt.Sprintf("coalesce=%t", coalesce), func(t *testing.T) {
			t.Parallel()

			b := NewBroker[snap]()
			if coalesce {
				b = NewCoalescingBroker(snapKey)
			}
			// 上下文 outlives 测试 所以 只 Shutdown can stop
			// the pump.
			ch := b.Subscribe(context.Background())
			for i := range 10 {
				b.Publish(UpdatedEvent, snap{ID: "a", V: i})
				b.Publish(CreatedEvent, snap{ID: "x", V: i})
			}

			b.Shutdown()

			// pump closes out 当 it exits. 一个 live pump would still
			// 是 blocked handing over 它的 first 事件, 所以 first
			// receive tells the two apart without feeding the pump.
			time.Sleep(5 * shutdownDrainTimeout)
			select {
			case ev, ok := <-ch:
				require.False(t, ok, "pump still running, handed over %v", ev)
			case <-time.After(time.Second):
				t.Fatal("pump neither exited nor handed over an event")
			}
		})
	}
}

func TestBroker_ShutdownDeliversPendingToActiveReader(t *testing.T) {
	t.Parallel()

	b := NewBroker[snap]()
	ch := b.Subscribe(context.Background())
	const n = 100
	for i := range n {
		b.Publish(UpdatedEvent, snap{ID: "a", V: i})
	}
	b.Shutdown()

	var got int
	for range ch {
		got++
	}
	require.Equal(t, n, got)
}

func TestBroker_PlainDeliversEveryEventInOrder(t *testing.T) {
	t.Parallel()

	b := NewBroker[snap]()
	t.Cleanup(b.Shutdown)
	sub, ch := subscribeStalled(t, b)

	const n = 1000
	var want []string
	for i := 1; i <= n; i++ {
		b.Publish(UpdatedEvent, snap{ID: "a", V: i})
		want = append(want, fmt.Sprintf("updated:a:%d:false", i))
		if i%100 == 0 {
			b.PublishMustDeliver(t.Context(), UpdatedEvent, snap{ID: "a", V: i})
			want = append(want, fmt.Sprintf("updated:a:%d:true", i))
		}
	}
	require.Len(t, sub.queue, n+n/100)
	sub.mu.Lock()
	require.Empty(t, sub.open)
	sub.mu.Unlock()
	require.Zero(t, b.DropCount())

	require.Equal(t, append([]string{"created:blocker:0:false"}, want...), describe(drain(t, ch)))
}

func TestCoalescingBroker_UnrelatedEventsUnaffected(t *testing.T) {
	t.Parallel()

	b := newTestCoalescingBroker(t)
	_, ch := subscribeStalled(t, b)

	var want []string
	for i := range 50 {
		id := fmt.Sprintf("m%d", i)
		b.Publish(UpdatedEvent, snap{ID: id, V: 1})
		want = append(want, fmt.Sprintf("updated:%s:1:false", id))
	}
	for range 3 {
		b.Publish(CreatedEvent, snap{ID: "a"})
		b.Publish(DeletedEvent, snap{ID: "a"})
		want = append(want, "created:a:0:false", "deleted:a:0:false")
	}
	b.Publish("custom", snap{ID: "a", V: 9})
	b.Publish("custom", snap{ID: "a", V: 10})
	want = append(want, "custom:a:9:false", "custom:a:10:false")

	require.Equal(t, append([]string{"created:blocker:0:false"}, want...), describe(drain(t, ch)))
}

func TestCoalescingBroker_ConcurrentSlowReaderSeesMonotonicSnapshots(t *testing.T) {
	t.Parallel()

	b := newTestCoalescingBroker(t)
	ch := b.Subscribe(t.Context())

	const ids, versions = 8, 500
	done := make(chan struct{})
	var got []Event[snap]
	go func() {
		defer close(done)
		for ev := range ch {
			got = append(got, ev)
			if ev.Type == "end" {
				return
			}
			if rand.IntN(10) == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	}()

	var wg sync.WaitGroup
	for i := range ids {
		id := fmt.Sprintf("m%d", i)
		wg.Go(func() {
			b.Publish(CreatedEvent, snap{ID: id})
			for v := 1; v <= versions; v++ {
				if v%100 == 0 {
					b.PublishMustDeliver(context.Background(), UpdatedEvent, snap{ID: id, V: v})
				} else {
					b.Publish(UpdatedEvent, snap{ID: id, V: v})
				}
			}
			b.Publish(DeletedEvent, snap{ID: id})
		})
	}
	wg.Wait()
	b.PublishMustDeliver(t.Context(), "end", snap{})
	<-done

	require.Zero(t, b.DropCount())
	require.Zero(t, b.MustDeliverDropCount())
	last := map[string]int{}
	created := map[string]bool{}
	deleted := map[string]bool{}
	for _, ev := range got {
		id := ev.Payload.ID
		switch ev.Type {
		case CreatedEvent:
			created[id] = true
		case UpdatedEvent:
			require.True(t, created[id], "update before create for %s", id)
			require.False(t, deleted[id], "update after delete for %s", id)
			require.Greater(t, ev.Payload.V, last[id], "older snapshot after newer for %s", id)
			last[id] = ev.Payload.V
		case DeletedEvent:
			deleted[id] = true
		}
	}
	for i := range ids {
		id := fmt.Sprintf("m%d", i)
		require.Equal(t, versions, last[id])
		require.True(t, deleted[id])
	}
}

// After Shutdown slow reader gets one drain 窗口 在 total, 不 one per
// remaining 事件.
func TestBroker_ShutdownDrainIsOneDeadline(t *testing.T) {
	t.Parallel()
	b := NewBroker[int]()
	ch := b.Subscribe(t.Context())
	for i := range 50 {
		b.Publish(UpdatedEvent, i)
	}
	time.Sleep(10 * time.Millisecond)
	b.Shutdown()

	start := time.Now()
	for range ch {
		time.Sleep(20 * time.Millisecond)
	}
	require.Less(t, time.Since(start), 5*shutdownDrainTimeout)
}

// TestBroker_ConcurrentShutdown verifies 那 Shutdown 是 idempotent 当
// several goroutines 调用 it 在 once, 作为 workspaces sharing
// process-global MCP 总线 do 当 they shut down 在 parallel. Closing
// done 渠道 twice would panic 和 crash 服务端.
func TestBroker_ConcurrentShutdown(t *testing.T) {
	t.Parallel()

	const (
		iterations = 2000
		callers    = 4
	)
	for n := range iterations {
		b := NewBroker[int]()
		start := make(chan struct{})
		recovered := make([]any, callers)
		var wg sync.WaitGroup
		for i := range callers {
			wg.Go(func() {
				defer func() { recovered[i] = recover() }()
				<-start
				b.Shutdown()
			})
		}
		close(start)
		wg.Wait()

		for _, r := range recovered {
			require.Nil(t, r, "Shutdown panicked on iteration %d", n)
		}
	}
}

// TestSubscribeWithOverflow guards 那 订阅者 那 falls behind 是
// told 所以 (它的 overflow 渠道 closes) instead 的 silently losing events.
func TestSubscribeWithOverflow(t *testing.T) {
	t.Parallel()

	for name, publish := range map[string]func(*Broker[int], int){
		"Publish": func(b *Broker[int], v int) { b.Publish(CreatedEvent, v) },
		"PublishMustDeliver": func(b *Broker[int], v int) {
			b.PublishMustDeliver(context.Background(), CreatedEvent, v)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := NewBrokerWithOptions[int](1)
			b.SetMustDeliverTimeout(time.Millisecond)
			t.Cleanup(b.Shutdown)

			slow, slowOverflow := b.SubscribeWithOverflow(t.Context())
			fast, fastOverflow := b.SubscribeWithOverflow(t.Context())

			publish(b, 1)
			<-fast
			require.False(t, isClosed(slowOverflow))

			publish(b, 2)
			<-fast
			publish(b, 3)
			<-fast
			require.True(t, isClosed(slowOverflow))
			require.False(t, isClosed(fastOverflow))
			require.Equal(t, 1, (<-slow).Payload)
		})
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
