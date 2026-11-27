package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"charm.land/fantasy"
)

// timeoutKind 是 which 请求 budget ran out.
type timeoutKind int

const (
	// timeoutDeadline 是 non-streaming 请求 那 ran past 它的 budget.
	timeoutDeadline timeoutKind = iota
	// timeoutFirstPart 是 流 那 sent nothing before 它的 first 片段.
	timeoutFirstPart
	// timeoutIdle 是 流 那 went silent after it started answering.
	timeoutIdle
	// timeoutStalled 是 流 那 sent 只 keepalives 为 它的 budget.
	timeoutStalled
)

// timeoutMessages holds each kind's log 和 UI wording, given budget.
var timeoutMessages = [...]struct{ log, user string }{
	timeoutDeadline:  {"LLM request timed out after %s", "The model did not respond within %s."},
	timeoutFirstPart: {"LLM stream sent no first part within %s", "The model did not send any data within %s."},
	timeoutIdle:      {"LLM stream received no data for %s", "The model stopped sending data for %s."},
	timeoutStalled:   {"LLM stream sent only keepalives for %s", "The connection stayed open but the model sent nothing for %s."},
}

// requestTimeoutError reports exhausted budget; it never wraps 上下文 错误.
type requestTimeoutError struct {
	timeout time.Duration
	kind    timeoutKind
	cause   error
}

func (e *requestTimeoutError) Error() string {
	msg := fmt.Sprintf(timeoutMessages[e.kind].log, e.timeout)
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", msg, e.cause)
	}
	return msg
}

func (e *requestTimeoutError) Unwrap() error { return e.cause }

// IsRetryable implements [fantasy.RetryableError]: silence usually means the network dropped.
func (e *requestTimeoutError) IsRetryable() bool { return true }

// userMessage explains 超时 在 UI 和 how 到 change limit.
func (e *requestTimeoutError) userMessage() string {
	const hint = "Increase the limit with \"option request-timeout SECONDS\" or set it to 0 to disable the timeout."
	return fmt.Sprintf(timeoutMessages[e.kind].user, e.timeout) + " " + hint
}

// defaultFirstPartTimeout floors first-片段 wait, which covers prefill 和 thinking.
const defaultFirstPartTimeout = 5 * time.Minute

// keepaliveGraceFactor scales 超时 into keepalive-只 (stall) budget.
const keepaliveGraceFactor = 10

// requestGuardModel ends each 请求 当 它的 运行 是 cancelled 或, 如果 set,
// 当 request_timeout budget runs out.
type requestGuardModel struct {
	fantasy.LanguageModel
	// 超时 是 inactivity 窗口 between 流 parts; zero disables it.
	timeout time.Duration
	// firstPart 是 窗口 granted until first 片段 arrives.
	firstPart time.Duration
}

// newRequestGuardModel guards m, bounding it 通过 超时 当 positive.
func newRequestGuardModel(m fantasy.LanguageModel, timeout time.Duration) fantasy.LanguageModel {
	if m == nil {
		return m
	}
	return requestGuardModel{
		LanguageModel: m,
		timeout:       max(timeout, 0),
		firstPart:     max(timeout, defaultFirstPartTimeout),
	}
}

// wrapTimedOut returns copy 的 超时 那 cancelled ctx, caused 通过
// err unless err 是 just cancellation echoed back; other errors pass.
func wrapTimedOut(ctx context.Context, err error) error {
	timeoutErr, ok := context.Cause(ctx).(*requestTimeoutError)
	if err == nil || !ok {
		return err
	}
	wrapped := *timeoutErr
	if !isContextErr(err) && !errors.Is(err, timeoutErr) {
		wrapped.cause = err
	}
	return &wrapped
}

// isContextErr reports 错误 那 重试 logic reads 作为 deliberate abort.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Generate has no progress signal, 所以 whole 调用 gets one 期限.
func (m requestGuardModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	if m.timeout == 0 {
		return m.LanguageModel.Generate(ctx, call)
	}
	ctx, cancel := context.WithTimeoutCause(ctx, m.timeout, &requestTimeoutError{timeout: m.timeout, kind: timeoutDeadline})
	defer cancel()
	resp, err := m.LanguageModel.Generate(ctx, call)
	return resp, wrapTimedOut(ctx, err)
}

// 流 stops 在 first 片段 after ctx 是 done, since buffered parts
// outlive 取消, 和 ends any cut-short 流 用 错误 片段.
func (m requestGuardModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	watchdog := newStreamWatchdog(cancel, m.timeout, m.firstPart)
	inner, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		watchdog.stop()
		err = wrapTimedOut(ctx, err)
		cancel(nil)
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		defer cancel(nil)
		defer watchdog.stop()
		ended := false // the consumer saw an error or finish, or broke off
		inner(func(part fantasy.StreamPart) bool {
			if context.Cause(ctx) != nil {
				return false
			}
			watchdog.received(part)
			keepGoing := yield(part)
			if !keepGoing || part.Type == fantasy.StreamPartTypeError || part.Type == fantasy.StreamPartTypeFinish {
				ended = true
			}
			if keepGoing {
				watchdog.waiting()
			}
			return keepGoing
		})
		if cause := context.Cause(ctx); cause != nil && !ended {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: wrapTimedOut(ctx, cause)})
		}
	}, nil
}

// streamWatchdog cancels 流 那 never starts, goes silent, 或 只
// keeps alive, timing 只 提供方 waits; nil watchdog watches nothing.
type streamWatchdog struct {
	idle, stall           time.Duration
	idleTimer, stallTimer *time.Timer
	arrived               atomic.Bool
	stallHeld             bool // the last part was content, which pauses the stall budget
}

// newStreamWatchdog starts both budgets, 或 returns nil 当 空闲 是 unset.
func newStreamWatchdog(cancel context.CancelCauseFunc, idle, firstPart time.Duration) *streamWatchdog {
	if idle <= 0 {
		return nil
	}
	w := &streamWatchdog{idle: idle, stall: max(idle*keepaliveGraceFactor, firstPart)}
	w.idleTimer = time.AfterFunc(firstPart, func() {
		if w.arrived.Load() {
			cancel(&requestTimeoutError{timeout: w.idle, kind: timeoutIdle})
			return
		}
		cancel(&requestTimeoutError{timeout: firstPart, kind: timeoutFirstPart})
	})
	w.stallTimer = time.AfterFunc(w.stall, func() {
		cancel(&requestTimeoutError{timeout: w.stall, kind: timeoutStalled})
	})
	return w
}

// received pauses 空闲 clock, 和 stall clock too 为 content.
func (w *streamWatchdog) received(part fantasy.StreamPart) {
	if w == nil {
		return
	}
	w.arrived.Store(true)
	w.idleTimer.Stop()
	w.stallHeld = part.Type != fantasy.StreamPartTypeKeepalive
	if w.stallHeld {
		w.stallTimer.Stop()
	}
}

// waiting re-arms clocks once consumer 是 done 用 片段.
func (w *streamWatchdog) waiting() {
	if w == nil {
		return
	}
	w.idleTimer.Reset(w.idle)
	if w.stallHeld {
		w.stallTimer.Reset(w.stall)
	}
}

// stop releases both timers.
func (w *streamWatchdog) stop() {
	if w == nil {
		return
	}
	w.idleTimer.Stop()
	w.stallTimer.Stop()
}
