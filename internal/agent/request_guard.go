// request_guard.go —— 请求超时保护：按上下文规模放大的超时额度、面向用户的提示，
// 以及把超时包装成可重试错误交给上层重试。

package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"charm.land/fantasy"
)

// timeoutKind 表示哪一份请求额度被耗尽了。
type timeoutKind int

const (
	// timeoutDeadline 是非流式请求超出了整体额度。
	timeoutDeadline timeoutKind = iota
	// timeoutFirstPart 是流式请求在首个片段之前一直没有数据。
	timeoutFirstPart
	// timeoutIdle 是流式请求开始应答之后又归于沉默。
	timeoutIdle
	// timeoutStalled 是流式请求整段时间只收到 keepalive。
	timeoutStalled
)

// timeoutMessages 按超时类型给出日志与界面文案，%s 处填入额度。
var timeoutMessages = [...]struct{ log, user string }{
	timeoutDeadline:  {"LLM request timed out after %s", "The model did not respond within %s."},
	timeoutFirstPart: {"LLM stream sent no first part within %s", "The model did not send any data within %s."},
	timeoutIdle:      {"LLM stream received no data for %s", "The model stopped sending data for %s."},
	timeoutStalled:   {"LLM stream sent only keepalives for %s", "The connection stayed open but the model sent nothing for %s."},
}

// requestTimeoutError 表示额度已耗尽；它不会包裹上下文错误。
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

// IsRetryable 实现 [fantasy.RetryableError]：沉默通常意味着连接断了。
func (e *requestTimeoutError) IsRetryable() bool { return true }

// userMessage 面向界面说明超时，并提示如何调整限制。
func (e *requestTimeoutError) userMessage() string {
	const hint = "Increase the limit with \"option request-timeout SECONDS\" or set it to 0 to disable the timeout."
	return fmt.Sprintf(timeoutMessages[e.kind].user, e.timeout) + " " + hint
}

// timeoutRetryDelay 是超时请求两次尝试之间的固定间隔（秒）。
// fantasy 自带的退避是指数的（5s、10s、20s……），连续重试十次会让用户
// 干等几十分钟；而超时通常意味着连接断了（keepalive 会重置空闲计时），
// 所以立刻重连既安全又够用。把它作为 Retry-After 返回，让 fantasy 采用
// 这个值而不是它自己的退避表。
const timeoutRetryDelay = "3"

// asRetryable 把超时包装成 fantasy 眼中的可重试错误：一个标记为瞬时的
// ProviderError，其 cause 仍是 requestTimeoutError。外层用
// errors.As 依旧能取到超时类型，UI 文案和 finish 片段保持不变。
func asRetryable(err error) error {
	timeoutErr, ok := errors.AsType[*requestTimeoutError](err)
	if !ok {
		return err
	}
	return &fantasy.ProviderError{
		Message:         timeoutErr.Error(),
		Cause:           timeoutErr,
		TransientError:  true,
		ResponseHeaders: map[string]string{"retry-after": timeoutRetryDelay},
	}
}

// defaultFirstPartTimeout 是首个片段的下限等待，覆盖 prefill 与思考阶段。
const defaultFirstPartTimeout = 5 * time.Minute

// keepaliveGraceFactor 把空闲窗口放大成“只收到 keepalive”时的容忍额度。
const keepaliveGraceFactor = 10

// 按提示词估算规模分档（单位 token）。大上下文和推理模型在首个 token 之前
// 就可能思考数分钟，只按基础额度会误杀健康请求。这些档位只会抬高额度，
// 绝不会下调。
const (
	largeContextPromptTokens = 50_000
	hugeContextPromptTokens  = 100_000
	largeContextTimeout      = 4 * time.Minute
	hugeContextTimeout       = 5 * time.Minute
)

// requestGuardModel 在每次运行被取消时结束请求，或在设置了超时额度时，
// 在额度耗尽时结束请求。
type requestGuardModel struct {
	fantasy.LanguageModel
	// timeout 是两次流片段之间的基础空闲窗口；0 表示禁用。
	timeout time.Duration
	// firstPart 是首个片段到达前的基础等待窗口。
	firstPart time.Duration
}

// newRequestGuardModel 给 m 套上超时保护，timeout 为正数时生效。
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

// budgets 给出本次调用的期限和首个片段等待窗口；提示词很大时按规模放大，
// 避免把 prefill 误判成 provider 卡死。
func (m requestGuardModel) budgets(prompt fantasy.Prompt) (timeout, firstPart time.Duration) {
	timeout = scaleRequestTimeout(m.timeout, prompt)
	return timeout, max(m.firstPart, timeout)
}

// scaleRequestTimeout 按估算的提示词规模放大基础额度。0 或负数（已禁用）
// 原样返回；显式配置的 request_timeout 只会被放大，绝不会被缩小。
func scaleRequestTimeout(base time.Duration, prompt fantasy.Prompt) time.Duration {
	if base <= 0 {
		return base
	}
	switch tokens := estimateMessageTokens(prompt); {
	case tokens > hugeContextPromptTokens:
		return max(base, hugeContextTimeout)
	case tokens > largeContextPromptTokens:
		return max(base, largeContextTimeout)
	default:
		return base
	}
}

// wrapTimedOut 在 ctx 因超时被取消时，返回带上具体原因的错误副本；
// 如果 err 只是被取消的回声，就不再重复包裹；其它错误原样返回。
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

// isContextErr 判断错误是否为调用方主动中止，重试逻辑据此放弃重试。
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Generate 没有进度信号，所以整个调用只给一个期限。
func (m requestGuardModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	timeout, _ := m.budgets(call.Prompt)
	if timeout == 0 {
		return m.LanguageModel.Generate(ctx, call)
	}
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, &requestTimeoutError{timeout: timeout, kind: timeoutDeadline})
	defer cancel()
	resp, err := m.LanguageModel.Generate(ctx, call)
	return resp, asRetryable(wrapTimedOut(ctx, err))
}

// Stream 在 ctx 结束后不再向消费者交付后续片段（已缓冲的片段会越过取消），
// 并给被中断的流补一个错误片段说明原因。
func (m requestGuardModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	timeout, firstPart := m.budgets(call.Prompt)
	ctx, cancel := context.WithCancelCause(ctx)
	watchdog := newStreamWatchdog(cancel, timeout, firstPart)
	inner, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		watchdog.stop()
		err = asRetryable(wrapTimedOut(ctx, err))
		cancel(nil)
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		defer cancel(nil)
		defer watchdog.stop()
		ended := false // 消费者已经看到错误或结束片段，或者提前中断
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
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: asRetryable(wrapTimedOut(ctx, cause))})
		}
	}, nil
}

// streamWatchdog 负责取消那些迟迟不开始、中途沉默，或只发 keepalive 的流，
// 计时的只是等待 provider 的时间；nil watchdog 不做任何监视。
type streamWatchdog struct {
	idle, stall           time.Duration
	idleTimer, stallTimer *time.Timer
	arrived               atomic.Bool
	stallHeld             bool // 上一个片段是正文内容，暂停 stall 额度
}

// newStreamWatchdog 启动两份计时，空闲窗口未设置时返回 nil。
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

// received 暂停空闲计时；收到正文内容时同时暂停 stall 计时。
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

// waiting 在消费者处理完一个片段后重新启动计时。
func (w *streamWatchdog) waiting() {
	if w == nil {
		return
	}
	w.idleTimer.Reset(w.idle)
	if w.stallHeld {
		w.stallTimer.Reset(w.stall)
	}
}

// stop 释放两个计时器。
func (w *streamWatchdog) stop() {
	if w == nil {
		return
	}
	w.idleTimer.Stop()
	w.stallTimer.Stop()
}
