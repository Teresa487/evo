// Package pubsub provides lightweight 在-process 总线 为 fan-out
// 事件 delivery between services 和 UI.
//
// Delivery semantics:
//
// - [总线.发布] 是 best-effort 和 lossy under contention. If
// 订阅者's 渠道 是 full, 事件 是 dropped 为 那
// 订阅者, warning 是 logged, 和 计数器 是 incremented.
// 这 是 right choice 为 high-frequency intermediate updates
// (e.g. streaming token deltas) where 只 latest state
//     matters.
//
// - [总线.PublishMustDeliver] 是 bounded-blocking. For each
// 订阅者 it first tries non-blocking send, then falls back 到
// per-订阅者 blocking send 用 hard 超时. On 超时
// 事件 是 dropped 为 那 订阅者, 错误 是 logged, 和
// must-投递 drop 计数器 是 incremented. publisher never
// blocks indefinitely. 这 是 right choice 为 终态 events
// (finish, 工具结果, 错误, 取消) 那 must 不 是 silently
//     coalesced away.
//
// - 一个 总线 created 用 [NewCoalescingBroker] keeps 在 most one
// pending 事件 per key 为 each 订阅者; newer 事件 replaces
// it 在 place 和 takes 它的 delivery position. Events 用 same
// key arrive 在 order, 但 replacement can arrive before unrelated
// events published after 事件 it replaced.
//
// Drop counters ([总线.DropCount], [总线.MustDeliverDropCount]) 是
// exposed 所以 callers can surface saturation 在 telemetry. 一个 订阅者
// 那 cannot tolerate gaps subscribes 用
// [总线.SubscribeWithOverflow] 到 learn 当 事件 是 dropped 为 it.
package pubsub

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// bufferSize 是 per-订阅者 队列 capacity 为 any 总线
	// created via NewBroker. 发布 是 non-blocking, 所以 full 缓冲
	// drops events (用 warning log); sized 到 cover long
	// streaming 助手 turn (~one UpdatedEvent per token) even under
	// TUI render stalls.
	bufferSize = 4096

	// defaultMustDeliverTimeout 是 per-订阅者 upper bound 在 how
	// long [总线.PublishMustDeliver] will block waiting 为 缓冲
	// space before giving up 在 那 订阅者.
	defaultMustDeliverTimeout = 50 * time.Millisecond

	// shutdownDrainTimeout bounds how long 订阅 保持 delivering
	// 它的 remaining events after [总线.Shutdown].
	shutdownDrainTimeout = 100 * time.Millisecond
)

type Broker[T any] struct {
	subs                 map[*subscription[T]]struct{}
	coalesceKey          func(Event[T]) string
	mu                   sync.RWMutex
	done                 chan struct{}
	subCount             int
	channelBufferSize    int
	mustDeliverTimeout   time.Duration
	dropCount            atomic.Uint64
	mustDeliverDropCount atomic.Uint64
}

func NewBroker[T any]() *Broker[T] {
	return NewBrokerWithOptions[T](bufferSize)
}

func NewBrokerWithOptions[T any](channelBufferSize int) *Broker[T] {
	return &Broker[T]{
		subs:               make(map[*subscription[T]]struct{}),
		done:               make(chan struct{}),
		channelBufferSize:  channelBufferSize,
		mustDeliverTimeout: defaultMustDeliverTimeout,
	}
}

// NewCoalescingBroker returns 总线 whose subscribers keep 在 most one
// pending [总线.发布] 事件 per non-empty key returned 通过 key. Events
// 为 which key returns "" 是 delivered 在 order without coalescing.
func NewCoalescingBroker[T any](key func(Event[T]) string) *Broker[T] {
	b := NewBroker[T]()
	b.coalesceKey = key
	return b
}

// SetMustDeliverTimeout overrides per-订阅者 超时 used 通过
// [总线.PublishMustDeliver]. 一个 zero 或 negative 值 resets 到
// 默认. Intended primarily 为 tests.
func (b *Broker[T]) SetMustDeliverTimeout(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d <= 0 {
		b.mustDeliverTimeout = defaultMustDeliverTimeout
		return
	}
	b.mustDeliverTimeout = d
}

// Shutdown stops 总线. Each 订阅者's 渠道 closes once 它的
// pending events 是 delivered, 或 once 它的 reader stops taking them 为
// [shutdownDrainTimeout]. 它 是 safe 到 调用 more than once, even
// concurrently, because done 是 checked 和 closed under 写入 锁.
func (b *Broker[T]) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()

	select {
	case <-b.done: // Already closed
		return
	default:
		close(b.done)
	}

	for sub := range b.subs {
		delete(b.subs, sub)
		close(sub.queue)
	}

	b.subCount = 0
}

func (b *Broker[T]) Subscribe(ctx context.Context) <-chan Event[T] {
	return b.subscribe(ctx, nil)
}

// SubscribeWithOverflow 是 like [总线.Subscribe] 为 subscribers 那
// cannot tolerate gaps, such 作为 流 到 remote 客户端. second
// 渠道 是 closed first time 事件 是 dropped 为 这
// 订阅者, 通过 [总线.发布] finding 它的 队列 full 或 通过
// [总线.PublishMustDeliver] timing out. 订阅者 should then stop
// consuming 和 resynchronize 从 source 的 truth.
func (b *Broker[T]) SubscribeWithOverflow(ctx context.Context) (<-chan Event[T], <-chan struct{}) {
	overflow := make(chan struct{})
	return b.subscribe(ctx, sync.OnceFunc(func() { close(overflow) })), overflow
}

func (b *Broker[T]) subscribe(ctx context.Context, onDrop func()) <-chan Event[T] {
	b.mu.Lock()
	defer b.mu.Unlock()

	select {
	case <-b.done:
		ch := make(chan Event[T])
		close(ch)
		return ch
	default:
	}

	sub := &subscription[T]{
		// Unbuffered 所以 pending events stay 在 队列 where they can
		// still 是 coalesced.
		out:    make(chan Event[T]),
		queue:  make(chan *item[T], b.channelBufferSize),
		open:   make(map[string]*item[T]),
		onDrop: onDrop,
	}
	b.subs[sub] = struct{}{}
	b.subCount++
	go sub.pump(ctx, b.done)

	go func() {
		select {
		case <-ctx.Done():
		case <-b.done:
			return
		}

		b.mu.Lock()
		defer b.mu.Unlock()

		select {
		case <-b.done:
			return
		default:
		}

		delete(b.subs, sub)
		close(sub.queue)
		b.subCount--
	}()

	return sub.out
}

func (b *Broker[T]) GetSubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.subCount
}

// DropCount returns cumulative number 的 events dropped 通过
// [总线.发布] because 订阅者's 队列 was full.
func (b *Broker[T]) DropCount() uint64 {
	return b.dropCount.Load()
}

// MustDeliverDropCount returns cumulative number 的 events dropped
// 通过 [总线.PublishMustDeliver] after per-订阅者 超时
// expired.
func (b *Broker[T]) MustDeliverDropCount() uint64 {
	return b.mustDeliverDropCount.Load()
}

// 发布 delivers 事件 到 every 活跃 订阅者.
//
// Delivery 是 non-blocking 和 lossy: 如果 订阅者's 队列 是 full
// 事件 是 dropped 为 那 订阅者, warning 是 logged, 和
// [总线.DropCount] 是 incremented. Use [总线.PublishMustDeliver]
// 为 events 那 must 不 是 silently dropped.
func (b *Broker[T]) Publish(t EventType, payload T) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	select {
	case <-b.done:
		return
	default:
	}

	event := Event[T]{Type: t, Payload: payload}
	key := b.key(event)

	for sub := range b.subs {
		if !sub.publish(event, key) {
			// 队列 是 full, 订阅者 是 slow — skip 这 事件.
			// Lossy 通过 design; counted 和 logged 所以 saturation 是
			// observable.
			b.dropCount.Add(1)
			slog.Warn("Pubsub buffer full; dropping event", "type", t)
			sub.dropped()
		}
	}
}

// PublishMustDeliver delivers 事件 用 bounded-blocking semantics.
// For each 订阅者 it first attempts non-blocking send, then falls
// back 到 blocking send bounded 通过 per-订阅者 超时 (默认
// [defaultMustDeliverTimeout]). On 超时 事件 是 dropped 为
// 那 订阅者, [总线.MustDeliverDropCount] 是 incremented, 和
// 错误 是 logged. publisher never blocks indefinitely. On
// coalescing 总线 事件 takes over pending 事件 用 same
// key instead, which cannot time out, 和 是 never replaced itself.
//
// Use 这 为 终态 events 那 must reach subscribers (finish,
// 工具结果, 错误, 取消). Callers must still tolerate rare drops
// after 超时 — recovery 是 订阅者's responsibility (e.g.
// re-fetch 在 next 会话-visible 事件).
func (b *Broker[T]) PublishMustDeliver(ctx context.Context, t EventType, payload T) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	select {
	case <-b.done:
		return
	default:
	}

	event := Event[T]{Type: t, Payload: payload, MustDeliver: true}
	key := b.key(event)
	timeout := b.mustDeliverTimeout

	for sub := range b.subs {
		if sub.publishMustDeliver(ctx, event, key, timeout) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		b.mustDeliverDropCount.Add(1)
		slog.Error("PublishMustDeliver timed out delivering event",
			"type", t, "timeout", timeout)
		sub.dropped()
	}
}

func (b *Broker[T]) key(event Event[T]) string {
	if b.coalesceKey == nil {
		return ""
	}
	return b.coalesceKey(event)
}

// item 是 one queued 事件. While it 是 pending under non-empty key,
// later events 用 那 key overwrite it 在 place.
type item[T any] struct {
	event Event[T]
	key   string
}

// subscription 是 one 订阅者: publishers fill bounded 队列 和
// pump goroutine feeds out.
type subscription[T any] struct {
	out   chan Event[T]
	queue chan *item[T]

	mu   sync.Mutex
	open map[string]*item[T] // Pending replaceable items by key.

	// onDrop 是 called 当 事件 是 dropped 为 这 订阅者, 或
	// 是 nil.
	onDrop func()
}

// dropped reports 那 事件 was dropped 为 这 订阅者.
func (s *subscription[T]) dropped() {
	if s.onDrop != nil {
		s.onDrop()
	}
}

// 发布 queues 事件 without blocking, 或 overwrites pending item
// 用 same key. 它 reports false 如果 队列 was full.
func (s *subscription[T]) publish(event Event[T], key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it, ok := s.open[key]; ok {
		it.event = event
		return true
	}
	it := &item[T]{event: event, key: key}
	select {
	case s.queue <- it:
		if key != "" {
			s.open[key] = it
		}
		return true
	default:
		return false
	}
}

// publishMustDeliver takes over pending item 用 same key, 或
// queues new item 那 是 never replaced, waiting up 到 超时 为
// space. 它 reports false 如果 事件 was 不 delivered.
func (s *subscription[T]) publishMustDeliver(ctx context.Context, event Event[T], key string, timeout time.Duration) bool {
	it := &item[T]{event: event, key: key}
	s.mu.Lock()
	if open, ok := s.open[key]; ok {
		open.event = event
		delete(s.open, key)
		s.mu.Unlock()
		return true
	}
	// fast 路径 stays under 锁 所以 concurrent plain 更新 为
	// key cannot 队列 ahead 的 这 事件.
	select {
	case s.queue <- it:
		s.mu.Unlock()
		return true
	default:
	}
	s.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case s.queue <- it:
		return true
	case <-timer.C:
	case <-ctx.Done():
	}
	return false
}

// pump hands queued items 到 out until 队列 是 closed 或 ctx ends.
// After done closes it keeps going 只 while reader keeps up.
func (s *subscription[T]) pump(ctx context.Context, done <-chan struct{}) {
	defer close(s.out)
	var deadline <-chan time.Time
	for it := range s.queue {
		s.mu.Lock()
		event := it.event
		it.event = Event[T]{}
		if s.open[it.key] == it {
			delete(s.open, it.key)
		}
		s.mu.Unlock()

		if deadline == nil {
			select {
			case s.out <- event:
				continue
			case <-ctx.Done():
				return
			case <-done:
				timer := time.NewTimer(shutdownDrainTimeout)
				defer timer.Stop()
				deadline = timer.C
			}
		}
		select {
		case s.out <- event:
		case <-ctx.Done():
			return
		case <-deadline:
			return
		}
	}
}
