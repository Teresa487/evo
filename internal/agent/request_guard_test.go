package agent

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"charm.land/fantasy"
	"charm.land/x/vcr"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// fakeLanguageModel 是 [fantasy.LanguageModel] stub 那 records
// 上下文 它的 methods were called 用 和 can 是 configured 用 custom
// 流 body.
type fakeLanguageModel struct {
	generateCtx context.Context
	streamCtx   context.Context
	stream      func(yield func(fantasy.StreamPart) bool)
}

func (f *fakeLanguageModel) Generate(ctx context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	f.generateCtx = ctx
	return &fantasy.Response{}, nil
}

func (f *fakeLanguageModel) Stream(ctx context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	f.streamCtx = ctx
	if f.stream == nil {
		return func(yield func(fantasy.StreamPart) bool) {
			yield(fantasy.StreamPart{})
		}, nil
	}
	return f.stream, nil
}

func (f *fakeLanguageModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return &fantasy.ObjectResponse{}, nil
}

func (f *fakeLanguageModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}

func (f *fakeLanguageModel) Provider() string { return "fake" }
func (f *fakeLanguageModel) Model() string    { return "fake-model" }

// 一个 disabled 超时 drops every budget 但 still guards 模型, since
// cancellation must stop 流 either way.
func TestNewRequestGuardModel_Disabled(t *testing.T) {
	t.Parallel()

	require.Nil(t, newRequestGuardModel(nil, time.Second))
	for _, timeout := range []time.Duration{0, -time.Second} {
		inner := &fakeLanguageModel{}
		_, err := newRequestGuardModel(inner, timeout).Generate(t.Context(), fantasy.Call{})
		require.NoError(t, err)
		_, ok := inner.generateCtx.Deadline()
		require.False(t, ok, "a disabled timeout must not impose a deadline")
	}
}

// newTestRequestGuardModel 构造一个首片段额度等于空闲窗口的模型，
// 让期限相关的测试在毫秒级触发，而不必等满 [defaultFirstPartTimeout]。
func newTestRequestGuardModel(m fantasy.LanguageModel, timeout time.Duration) requestGuardModel {
	return requestGuardModel{LanguageModel: m, timeout: timeout, firstPart: timeout}
}

func TestNewRequestGuardModel_FirstPartBudget(t *testing.T) {
	t.Parallel()

	// 一个 tight 空闲 窗口 still grants full first-片段 floor: prefill
	// before first token must 不 是 mistaken 为 stalled 提供方.
	short := newRequestGuardModel(&fakeLanguageModel{}, time.Second).(requestGuardModel)
	require.Equal(t, time.Second, short.timeout)
	require.Equal(t, defaultFirstPartTimeout, short.firstPart)

	// 一个 budget larger than floor 是 used 作为 configured.
	long := newRequestGuardModel(&fakeLanguageModel{}, 10*time.Minute).(requestGuardModel)
	require.Equal(t, 10*time.Minute, long.firstPart)
}

// 大 上下文 prefill 会 取 分钟，基础 budget 会把健康的请求当成卡死。
func TestScaleRequestTimeout(t *testing.T) {
	t.Parallel()

	const perTokenBytes = 4 // approxTokenCount 的换算比例
	// estimateMessageTokens 把角色名也算进去（"user" 约 1 个 token）。
	const roleTokens = 1
	// prompt 造一个总估算恰为 tokens 的提示词。
	prompt := func(tokens int) fantasy.Prompt {
		return fantasy.Prompt{fantasy.NewUserMessage(strings.Repeat("x", (tokens-roleTokens)*perTokenBytes))}
	}

	base := time.Minute
	require.Equal(t, base, scaleRequestTimeout(base, prompt(1_000)))
	require.Equal(t, base, scaleRequestTimeout(base, prompt(largeContextPromptTokens)))
	require.Equal(t, largeContextTimeout, scaleRequestTimeout(base, prompt(largeContextPromptTokens+1)))
	require.Equal(t, largeContextTimeout, scaleRequestTimeout(base, prompt(hugeContextPromptTokens)))
	require.Equal(t, hugeContextTimeout, scaleRequestTimeout(base, prompt(hugeContextPromptTokens+1)))

	// 关闭超时，或显式配置了更大的值，都不该被这里缩小。
	require.Zero(t, scaleRequestTimeout(0, prompt(hugeContextPromptTokens+1)))
	bigger := 15 * time.Minute
	require.Equal(t, bigger, scaleRequestTimeout(bigger, prompt(hugeContextPromptTokens+1)))
}

// 放大后的 budget 也要传给首次片段的等待窗口。
func TestRequestGuardModel_BudgetsScaleWithPrompt(t *testing.T) {
	t.Parallel()

	m := newRequestGuardModel(&fakeLanguageModel{}, time.Minute).(requestGuardModel)
	large := fantasy.Prompt{fantasy.NewUserMessage(strings.Repeat("x", hugeContextPromptTokens*4))}

	timeout, firstPart := m.budgets(large)
	require.Equal(t, hugeContextTimeout, timeout)
	require.Equal(t, hugeContextTimeout, firstPart)

	timeout, firstPart = m.budgets(fantasy.Prompt{fantasy.NewUserMessage("hi")})
	require.Equal(t, time.Minute, timeout)
	require.Equal(t, defaultFirstPartTimeout, firstPart)
}

// 超时要能被 fantasy 的重试逻辑识别，同时保留自己的类型给 UI 用。
func TestAsRetryableKeepsTimeoutType(t *testing.T) {
	t.Parallel()

	timeoutErr := &requestTimeoutError{timeout: time.Second, kind: timeoutIdle}
	wrapped := asRetryable(timeoutErr)
	require.NotSame(t, timeoutErr, wrapped)

	var got *requestTimeoutError
	require.ErrorAs(t, wrapped, &got)
	require.Equal(t, timeoutErr.timeout, got.timeout)

	var retryable fantasy.RetryableError
	require.ErrorAs(t, wrapped, &retryable)
	require.True(t, retryable.IsRetryable())

	// 非超时错误原样返回。
	other := errors.New("boom")
	require.Same(t, other, asRetryable(other))
}

func TestRequestGuardModel_GenerateDeadline(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	m := newRequestGuardModel(inner, 5*time.Minute)

	_, err := m.Generate(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	_, ok := inner.generateCtx.Deadline()
	require.True(t, ok, "Generate should run under a deadline")
}

func TestRequestGuardModel_StreamDeadlineOutlivesCall(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	m := newRequestGuardModel(inner, 5*time.Minute)

	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	require.NoError(t, inner.streamCtx.Err(), "the idle timer must not fire while the stream is being consumed")

	for range stream {
	}

	require.ErrorIs(t, inner.streamCtx.Err(), context.Canceled, "the stream context should be released after the stream ends")
}

func TestRequestGuardModel_StreamAbortsWhenIdle(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// 一个 流 那 outlives 空闲 窗口: it waits 为 上下文 到
	// 是 done 和 reports what it observed.
	streamObserved := make(chan error, 1)
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		<-inner.streamCtx.Done()
		streamObserved <- inner.streamCtx.Err()
	}
	m := newTestRequestGuardModel(inner, 10*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range stream {
		}
	}()

	select {
	case err := <-streamObserved:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("stream was not aborted by the idle timeout")
	}
	<-done
}

func TestRequestGuardModel_ActiveStreamSurvives(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// 一个持续发送数据的流：总时长超过超时额度，但每段间隔都短于空闲窗口，
	// 所以它必须正常跑完。
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		for range 6 {
			time.Sleep(40 * time.Millisecond)
			if !yield(fantasy.StreamPart{}) {
				return
			}
		}
	}
	m := newRequestGuardModel(inner, 150*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	parts := 0
	for part := range stream {
		require.NoError(t, part.Error)
		parts++
	}
	require.Equal(t, 6, parts)
}

// blockingModel blocks until 上下文 是 done 和 then returns
// 上下文 错误, way hung 提供方 请求 would.
type blockingModel struct {
	fakeLanguageModel
}

func (b *blockingModel) Generate(ctx context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRequestGuardModel_GenerateReportsTimeout(t *testing.T) {
	t.Parallel()

	m := newRequestGuardModel(&blockingModel{}, 10*time.Millisecond)

	_, err := m.Generate(t.Context(), fantasy.Call{})
	require.Error(t, err)

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, err, &timeoutErr)
	require.Equal(t, 10*time.Millisecond, timeoutErr.timeout)
	// 重试 logic treats 上下文 错误 作为 deliberate abort, 所以
	// 超时 must 不 match one: losing network 是 worth retrying.
	require.NotErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, context.Canceled)
	require.Contains(t, err.Error(), "timed out after 10ms")
}

func TestRequestGuardModel_StreamReportsTimeout(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// 一个 提供方 流 那 fails 用 上下文 错误 once 期限
	// fires, mirroring how SDKs surface mid-流 aborts.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		<-inner.streamCtx.Done()
		yield(fantasy.StreamPart{Error: inner.streamCtx.Err()})
	}
	m := newTestRequestGuardModel(inner, 10*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var got error
	for part := range stream {
		if part.Error != nil {
			got = part.Error
		}
	}

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, got, &timeoutErr)
	require.Equal(t, timeoutFirstPart, timeoutErr.kind, "nothing arrived, so this is a no-data timeout")
	require.NotErrorIs(t, got, context.DeadlineExceeded)
	require.NotErrorIs(t, got, context.Canceled)
}

func TestRequestGuardModel_ParentCancelPassesThrough(t *testing.T) {
	t.Parallel()

	m := newRequestGuardModel(&blockingModel{}, 5*time.Minute)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := m.Generate(ctx, fantasy.Call{})
	require.ErrorIs(t, err, context.Canceled)

	var timeoutErr *requestTimeoutError
	require.NotErrorAs(t, err, &timeoutErr, "user cancellation must not be reported as a timeout")
}

func TestRequestTimeoutErrorMessages(t *testing.T) {
	t.Parallel()

	err := &requestTimeoutError{timeout: time.Second}
	require.Equal(t, "LLM request timed out after 1s", err.Error())
	require.Contains(t, err.userMessage(), "1s")
	require.Contains(t, err.userMessage(), "request-timeout")

	err.cause = errors.New("connection reset")
	require.Equal(t, "LLM request timed out after 1s: connection reset", err.Error())

	first := &requestTimeoutError{timeout: 5 * time.Minute, kind: timeoutFirstPart}
	require.Equal(t, "LLM stream sent no first part within 5m0s", first.Error())

	idle := &requestTimeoutError{timeout: 2 * time.Second, kind: timeoutIdle}
	require.Equal(t, "LLM stream received no data for 2s", idle.Error())
	require.Contains(t, idle.userMessage(), "stopped sending data for 2s")
	require.Contains(t, idle.userMessage(), "request-timeout")

	stalled := &requestTimeoutError{timeout: 20 * time.Second, kind: timeoutStalled}
	require.Equal(t, "LLM stream sent only keepalives for 20s", stalled.Error())
	require.Contains(t, stalled.userMessage(), "model sent nothing for 20s")
	require.Contains(t, stalled.userMessage(), "request-timeout")
}

// timeoutOnlyModel streams single 错误 片段 shaped exactly like one
// requestGuardModel produces 当 它的 期限 fires.
type timeoutOnlyModel struct {
	fakeLanguageModel
	attempts atomic.Int32
}

func (m *timeoutOnlyModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	m.attempts.Add(1)
	timeoutErr := &requestTimeoutError{timeout: time.Second, kind: timeoutIdle}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: timeoutErr})
	}, nil
}

// 重试次数耗尽后，一次超时会让本轮以「请求超时」结束；
// 这里的 bubble 会把退避时钟快进掉。
func TestRequestTimeoutRunFinishMessage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows for now")
	}
	synctest.Test(t, testRequestTimeoutRunFinishMessage)
}

func testRequestTimeoutRunFinishMessage(t *testing.T) {
	env := testEnv(t)
	model := &timeoutOnlyModel{}
	agent, err := coderAgent(vcr.NewRecorder(t), env, model, fastModel{})
	require.NoError(t, err)

	session, err := env.sessions.Create(t.Context(), "timeout session")
	require.NoError(t, err)

	_, err = agent.Run(t.Context(), SessionAgentCall{
		Prompt:          "Hello",
		SessionID:       session.ID,
		MaxOutputTokens: 10000,
	})
	require.Error(t, err)

	msgs, err := env.messages.List(t.Context(), session.ID)
	require.NoError(t, err)

	var finish *message.Finish
	for _, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}
		if part := msg.FinishPart(); part != nil {
			finish = part
		}
	}
	require.NotNil(t, finish, "the assistant message should carry a finish part")
	require.Equal(t, message.FinishReasonError, finish.Reason)
	require.Equal(t, "Request timed out", finish.Message)
	require.Contains(t, finish.Details, "stopped sending data for 1s")
	require.Contains(t, finish.Details, "request-timeout")
	require.EqualValues(t, mainConversationRetries+1, model.attempts.Load(), "a timeout must be retried, up to the conversation budget")
}

func TestRequestGuardModel_FirstPartWindowOutlivesIdle(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// Silence longer than inter-片段 窗口 但 within budget
	// granted 到 first 片段: 模型 still prefilling large 上下文,
	// 或 thinking before 它的 first token, 是 不 hung 提供方.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		time.Sleep(80 * time.Millisecond)
		yield(fantasy.StreamPart{Delta: "hello"})
	}
	m := requestGuardModel{
		LanguageModel: inner,
		timeout:       20 * time.Millisecond,
		firstPart:     200 * time.Millisecond,
	}
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	parts := 0
	for part := range stream {
		require.NoError(t, part.Error)
		parts++
	}
	require.Equal(t, 1, parts, "the first part must not be cut off by the idle window")
}

func TestRequestGuardModel_AbortsOnInterPartGap(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// One 片段, then silence 为 rest 的 流: abort must 是
	// charged 到 inter-片段 窗口, 不 到 first-片段 budget.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		if !yield(fantasy.StreamPart{Delta: "first"}) {
			return
		}
		<-inner.streamCtx.Done()
		yield(fantasy.StreamPart{Error: inner.streamCtx.Err()})
	}
	m := requestGuardModel{
		LanguageModel: inner,
		timeout:       10 * time.Millisecond,
		firstPart:     time.Hour,
	}
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var got error
	for part := range stream {
		if part.Error != nil {
			got = part.Error
		}
	}

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, got, &timeoutErr)
	require.Equal(t, timeoutIdle, timeoutErr.kind, "data already arrived, so this is not a first-part timeout")
	require.Equal(t, 10*time.Millisecond, timeoutErr.timeout)
	require.Contains(t, got.Error(), "received no data for 10ms")
	require.Contains(t, timeoutErr.userMessage(), "stopped sending data for 10ms")
}

func TestRequestGuardModel_SlowConsumerDoesNotAbort(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// 一个 提供方 那 has everything ready 到 hand over. 一个 consumer 那
	// takes longer per 片段 than 空闲 窗口 used 到 读取 作为 提供方
	// silence 和 killed healthy streams.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		for i := range 5 {
			if !yield(fantasy.StreamPart{Delta: fmt.Sprint(i)}) {
				return
			}
		}
	}
	m := newTestRequestGuardModel(inner, 100*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var got []string
	for part := range stream {
		time.Sleep(200 * time.Millisecond)
		require.NoError(t, part.Error, "work on this side must not be reported as a timeout")
		got = append(got, part.Delta)
	}

	require.Len(t, got, 5)
	for i, delta := range got {
		require.Equal(t, fmt.Sprint(i), delta, "parts must arrive in order")
	}
}

// slowTool 会阻塞自身的 delay，模拟等待长任务的工具调用，
// 并记录拿到的上下文是否带期限。
type slowTool struct {
	delay       time.Duration
	ran         bool
	hadDeadline bool
}

func (*slowTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        "sleeper",
		Description: "Sleeps past the request timeout",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (*slowTool) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (*slowTool) SetProviderOptions(fantasy.ProviderOptions) {}

func (t *slowTool) Run(ctx context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	t.ran = true
	_, t.hadDeadline = ctx.Deadline()
	select {
	case <-time.After(t.delay):
		return fantasy.NewTextResponse("slept"), nil
	case <-ctx.Done():
		return fantasy.ToolResponse{}, ctx.Err()
	}
}

// oneToolCallModel streams single 工具调用 在 first agent step 和
// ends turn afterwards. Side calls (标题 generation) carry no tools 和
// must 不 consume scripted 工具调用.
type oneToolCallModel struct {
	fakeLanguageModel
	toolSent bool
}

func (m *oneToolCallModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	first := len(call.Tools) > 0 && !m.toolSent
	if first {
		m.toolSent = true
	}
	return func(yield func(fantasy.StreamPart) bool) {
		if first {
			if !yield(fantasy.StreamPart{
				Type:          fantasy.StreamPartTypeToolCall,
				ID:            "call-1",
				ToolCallName:  "sleeper",
				ToolCallInput: "{}",
			}) {
				return
			}
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls})
			return
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

// TestRequestGuardModel_DoesNotBoundToolCalls pins invariant 那
// request_timeout covers LLM requests 只: 工具 那 waits far past
// 超时 为 它的 job must finish normally, 和 它的 上下文 must carry no
// 期限.
func TestRequestGuardModel_DoesNotBoundToolCalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows for now")
	}

	env := testEnv(t)

	inner := &oneToolCallModel{}
	slow := &slowTool{delay: 500 * time.Millisecond}
	agent := testSessionAgent(env, newRequestGuardModel(inner, 100*time.Millisecond), inner, "", slow)

	session, err := env.sessions.Create(t.Context(), "tool timeout session")
	require.NoError(t, err)

	_, err = agent.Run(t.Context(), SessionAgentCall{
		Prompt:          "Hello",
		SessionID:       session.ID,
		MaxOutputTokens: 1000,
	})
	require.NoError(t, err)
	require.True(t, slow.ran, "the tool call should have run")
	require.False(t, slow.hadDeadline, "a tool call must not inherit the request timeout deadline")
}

// 空闲超时通常意味着连接断了，而不是模型还在思考（provider 的 keepalive
// 会重置空闲计时），所以无论它包裹的是什么错误都值得再试一次。
func TestRequestTimeoutIsRetryable(t *testing.T) {
	t.Parallel()

	err := error(&requestTimeoutError{
		timeout: time.Minute,
		kind:    timeoutIdle,
		cause:   &fantasy.ProviderError{StatusCode: 400},
	})

	var retryable fantasy.RetryableError
	require.ErrorAs(t, err, &retryable)
	require.True(t, retryable.IsRetryable())

	// These 是 what 重试 logic reads 作为 " caller gave up".
	require.NotErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, context.DeadlineExceeded)
}

// drainStream consumes 流 和 returns 片段 types 和 last 错误.
func drainStream(t *testing.T, ctx context.Context, model fantasy.LanguageModel) ([]fantasy.StreamPartType, error) {
	t.Helper()
	stream, err := model.Stream(ctx, fantasy.Call{})
	require.NoError(t, err)
	var types []fantasy.StreamPartType
	var lastErr error
	for part := range stream {
		if part.Error != nil {
			lastErr = part.Error
		}
		types = append(types, part.Type)
	}
	return types, lastErr
}

// 一个 keepalive-只 stretch longer than 空闲 budget 是 模型 thinking,
// 不 dead 连接.
func TestRequestTimeoutSurvivesKeepaliveOnlyStretch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const idle = time.Minute
		inner := &fakeLanguageModel{stream: func(yield func(fantasy.StreamPart) bool) {
			for range 4 {
				time.Sleep(idle * 3 / 4)
				if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeKeepalive}) {
					return
				}
			}
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "done"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})
		}}

		types, err := drainStream(t, t.Context(), newTestRequestGuardModel(inner, idle))
		require.NoError(t, err, "keepalives should have kept the stream alive")
		require.Contains(t, types, fantasy.StreamPartTypeTextDelta)
	})
}

// first-片段 budget still fires 在 流 那 是 genuinely silent.
func TestRequestTimeoutStillFiresOnRealSilence(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const idle = time.Minute
		inner := &fakeLanguageModel{stream: func(yield func(fantasy.StreamPart) bool) {
			time.Sleep(idle * 4)
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "too late"})
		}}

		types, err := drainStream(t, t.Context(), newTestRequestGuardModel(inner, idle))
		var timeoutErr *requestTimeoutError
		require.ErrorAs(t, err, &timeoutErr)
		require.Equal(t, timeoutFirstPart, timeoutErr.kind)
		require.NotContains(t, types, fantasy.StreamPartTypeTextDelta, "a part after the timeout must not reach the consumer")
	})
}

// Keepalives alone must 不 hold turn open forever: laptop-sleep case,
// where 连接 survives 但 nothing 是 being generated behind it.
func TestRequestTimeoutFiresOnKeepaliveOnlyStream(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const idle = time.Minute
		inner := &fakeLanguageModel{stream: func(yield func(fantasy.StreamPart) bool) {
			for {
				time.Sleep(idle / 2)
				if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeKeepalive}) {
					return
				}
			}
		}}

		start := time.Now()
		_, err := drainStream(t, t.Context(), newTestRequestGuardModel(inner, idle))
		var timeoutErr *requestTimeoutError
		require.ErrorAs(t, err, &timeoutErr)
		require.Equal(t, timeoutStalled, timeoutErr.kind)
		require.Less(t, time.Since(start), idle*keepaliveGraceFactor+idle, "the stall budget, not the idle one, must end it")
	})
}

// Content resets stall budget, 所以 活跃 流 outlives it.
func TestRequestTimeoutStallBudgetResetsOnContent(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const idle = time.Minute
		inner := &fakeLanguageModel{stream: func(yield func(fantasy.StreamPart) bool) {
			for range keepaliveGraceFactor * 2 {
				time.Sleep(idle / 2)
				if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "."}) {
					return
				}
			}
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})
		}}

		_, err := drainStream(t, t.Context(), newTestRequestGuardModel(inner, idle))
		require.NoError(t, err, "an actively streaming response must never be cut off")
	})
}

// 一个 cancelled 运行 must stop consuming 流 whose parts 是 already
// buffered locally, where cancelling 上下文 reaches nothing, 和 must
// say why it stopped: quiet stop reads 作为 finished 流.
func TestStreamStopsDrainingBufferedPartsOnCancel(t *testing.T) {
	t.Parallel()

	for _, timeout := range []time.Duration{0, time.Minute} {
		t.Run(timeout.String(), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			delivered := 0
			inner := &fakeLanguageModel{stream: func(yield func(fantasy.StreamPart) bool) {
				for range 5000 {
					if delivered++; delivered == 10 {
						cancel()
					}
					if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "x"}) {
						return
					}
				}
			}}

			types, err := drainStream(t, ctx, newRequestGuardModel(inner, timeout))
			require.Len(t, types, 10, "iteration must stop once the run is cancelled")
			require.Equal(t, fantasy.StreamPartTypeError, types[len(types)-1])
			require.ErrorIs(t, err, context.Canceled)
		})
	}
}

// 一个 提供方 那 notices 取消 和 returns without 错误 片段
// still leaves consumer 用 reason.
func TestStreamReportsQuietStopOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inner := &fakeLanguageModel{stream: func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputStart, ID: "c1"})
		cancel()
	}}

	types, err := drainStream(t, ctx, newRequestGuardModel(inner, 0))
	require.Equal(t, []fantasy.StreamPartType{fantasy.StreamPartTypeToolInputStart, fantasy.StreamPartTypeError}, types)
	require.ErrorIs(t, err, context.Canceled)
}

// cancelAwareTool answers cancelled 上下文 way MCP tools do: 文本
// 错误 naming cancellation, 用 no Go 错误 到 match 在.
type cancelAwareTool struct {
	slowTool
	started chan struct{}
}

func (t *cancelAwareTool) Run(ctx context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	close(t.started)
	<-ctx.Done()
	return fantasy.NewTextErrorResponse("context canceled"), nil
}

// 一个 工具 cut off 通过 cancelled turn 是 recorded 作为 interrupted, 所以 模型
// reads it 作为 retryable rather than 作为 工具 failing.
func TestCancelledTurnRecordsToolAsInterrupted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows for now")
	}

	env := testEnv(t)
	inner := &oneToolCallModel{}
	tool := &cancelAwareTool{started: make(chan struct{})}
	agent := testSessionAgent(env, newRequestGuardModel(inner, 0), inner, "", tool)

	session, err := env.sessions.Create(t.Context(), "cancelled tool session")
	require.NoError(t, err)

	go func() {
		<-tool.started
		agent.Cancel(session.ID)
	}()
	_, err = agent.Run(t.Context(), SessionAgentCall{Prompt: "Hello", SessionID: session.ID, MaxOutputTokens: 1000})
	require.ErrorIs(t, err, context.Canceled)

	msgs, err := env.messages.List(t.Context(), session.ID)
	require.NoError(t, err)
	var results []message.ToolResult
	var calls []message.ToolCall
	for _, msg := range msgs {
		results = append(results, msg.ToolResults()...)
		calls = append(calls, msg.ToolCalls()...)
	}
	require.Len(t, results, 1)
	require.True(t, results[0].IsError)
	require.Equal(t, interruptedToolCallResult, results[0].Content)
	// 调用记录和回复夹住整轮运行，所以重新加载的会话能看到与实时一致的时长。
	require.Len(t, calls, 1)
	require.Positive(t, calls[0].StartedAtMs, "the call must record when it started")
	require.GreaterOrEqual(t, results[0].FinishedAtMs, calls[0].StartedAtMs)
}
