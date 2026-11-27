package cmd

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// TestRunStream_ToolUseDoesNotTerminate 是 regression 测试 为
// original bug: 工具-调用 助手消息 has Finish 片段
// 用 reason=tool_use 和 used 到 terminate `crush 运行` early via
// the discarded `msg.IsFinished()` exit condition. With the new
// RunComplete-driven loop, tool_use finishes must keep 流
// alive 所以 post-工具 final 文本 still reaches stdout.
func TestRunStream_ToolUseDoesNotTerminate(t *testing.T) {
	t.Parallel()

	s := &runStream{sessionID: "S", out: &bytes.Buffer{}, seen: map[string]string{}}

	toolUse := proto.Message{
		ID:        "m1",
		SessionID: "S",
		Role:      proto.Assistant,
		Parts: []proto.ContentPart{
			proto.TextContent{Text: ""},
			proto.Finish{Reason: proto.FinishReasonToolUse, Time: time.Now().Unix()},
		},
	}
	done, err := s.handle(pubsub.Event[proto.Message]{Payload: toolUse}, nil)
	require.NoError(t, err)
	require.False(t, done, "tool_use finish must NOT terminate the run loop")
}

// TestRunStream_RunCompleteExits verifies happy 路径: streaming
// 助手 文本 then RunComplete terminates 用 full final
// 文本 在 stdout. Together 用 tool_use 测试 above 这
// nails down "工具 use + final 文本" 序号 那 original
// bug truncated.
func TestRunStream_RunCompleteExits(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", out: buf, seen: map[string]string{}}

	// 工具-use step.
	done, err := s.handle(pubsub.Event[proto.Message]{Payload: proto.Message{
		ID: "m1", SessionID: "S", Role: proto.Assistant,
		Parts: []proto.ContentPart{
			proto.TextContent{Text: ""},
			proto.Finish{Reason: proto.FinishReasonToolUse},
		},
	}}, nil)
	require.NoError(t, err)
	require.False(t, done)

	// Final 助手消息 流.
	done, err = s.handle(pubsub.Event[proto.Message]{Payload: proto.Message{
		ID: "m2", SessionID: "S", Role: proto.Assistant,
		Parts: []proto.ContentPart{
			proto.TextContent{Text: "VERDICT: APPROVED"},
			proto.Finish{Reason: proto.FinishReasonEndTurn},
		},
	}}, nil)
	require.NoError(t, err)
	require.False(t, done, "message finish (even end_turn) must not exit; RunComplete is the only terminal signal")

	// RunComplete.
	done, err = s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S",
		MessageID: "m2",
		Text:      "VERDICT: APPROVED",
	}}, nil)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "VERDICT: APPROVED", buf.String())
}

// TestRunStream_ReconcilesOnOutOfOrderRunComplete 是 worst-case
// ordering scenario: RunComplete reaches 客户端 BEFORE any 的
// streaming 助手消息 events 为 turn ( pubsub
// fan-在 across upstream brokers does 不 preserve cross-总线
// ordering). embedded 文本 字段 must rescue stdout 所以
// caller still sees 完成 final 文本.
func TestRunStream_ReconcilesOnOutOfOrderRunComplete(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", out: buf, seen: map[string]string{}}

	done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S",
		MessageID: "m2",
		Text:      "VERDICT: APPROVED",
	}}, nil)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "VERDICT: APPROVED", buf.String(),
		"RunComplete must reconcile stdout when message events did not arrive in time")
}

// TestRunStream_ReconcilesPartialStream covers the realistic case
// where some streaming output reached stdout before RunComplete
// arrived: reconciliation pass must append 只 unread tail,
// never duplicate the prefix.
func TestRunStream_ReconcilesPartialStream(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", out: buf, seen: map[string]string{}}

	_, err := s.handle(pubsub.Event[proto.Message]{Payload: proto.Message{
		ID: "m2", SessionID: "S", Role: proto.Assistant,
		Parts: []proto.ContentPart{proto.TextContent{Text: "VERDICT: "}},
	}}, nil)
	require.NoError(t, err)

	_, err = s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S",
		MessageID: "m2",
		Text:      "VERDICT: APPROVED",
	}}, nil)
	require.NoError(t, err)
	require.Equal(t, "VERDICT: APPROVED", buf.String())
}

// TestRunStream_IgnoresOtherSessions ensures multi-会话
// subscribers (e.g. TUI watching 工作区 events while `crush
// 运行` 是 在 flight against same 工作区) do 不 cause
// premature exit 在 RunComplete 为 different 会话.
func TestRunStream_IgnoresOtherSessions(t *testing.T) {
	t.Parallel()

	s := &runStream{sessionID: "S", out: &bytes.Buffer{}, seen: map[string]string{}}
	done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "OTHER",
		MessageID: "x",
		Text:      "noise",
	}}, nil)
	require.NoError(t, err)
	require.False(t, done)
}

// TestRunStream_ErrorRunComplete surfaces failing 运行 作为
// non-nil 错误 从 `crush 运行` 所以 shells 和 CI catch it via
// exit status.
func TestRunStream_ErrorRunComplete(t *testing.T) {
	t.Parallel()

	s := &runStream{sessionID: "S", out: &bytes.Buffer{}, seen: map[string]string{}}
	done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S",
		Error:     "model temporarily unavailable",
	}}, nil)
	require.True(t, done)
	require.Error(t, err)
	require.Contains(t, err.Error(), "model temporarily unavailable")
}

// TestRunStream_CancelledRunCompleteIsClean ensures a cancelled
// 运行 (e.g. Ctrl+C while `crush 运行` waits) exits cleanly rather
// than reporting cancellation 作为 failure.
func TestRunStream_CancelledRunCompleteIsClean(t *testing.T) {
	t.Parallel()

	s := &runStream{sessionID: "S", out: &bytes.Buffer{}, seen: map[string]string{}}
	done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S",
		Error:     "context canceled",
		Cancelled: true,
	}}, nil)
	require.True(t, done)
	require.NoError(t, err)
}

// TestRunStream_LeadingWhitespaceTrimmedOnce checks 那 initial whitespace
// 清理 strips leading blank lines 和 spaces 和 tracks raw seen content
// across live, fragmented, 和 RunComplete-只 output, without duplicating
// reconciled 文本.
func TestRunStream_LeadingWhitespaceTrimmedOnce(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", out: buf, seen: map[string]string{}}

	done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S",
		MessageID: "m2",
		Text:      "  \tactual output",
	}}, nil)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "actual output", buf.String())

	for _, text := range []string{"\nactual output", "\n\nactual output"} {
		for _, updates := range [][]string{nil, {text}, {text[:1], text[:len(text)-1]}} {
			buf.Reset()
			s = &runStream{sessionID: "S", out: buf, seen: map[string]string{}}
			for _, update := range updates {
				_, err = s.handle(pubsub.Event[proto.Message]{Payload: proto.Message{
					ID: "m2", SessionID: "S", Role: proto.Assistant,
					Parts: []proto.ContentPart{proto.TextContent{Text: update}},
				}}, nil)
				require.NoError(t, err)
				require.Equal(t, update, s.seen["m2"])
			}
			done, err = s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
				SessionID: "S", MessageID: "m2", Text: text,
			}}, nil)
			require.NoError(t, err)
			require.True(t, done)
			require.Equal(t, "actual output", buf.String())
		}
	}
}

// TestRunStream_LeadingBlankLines checks 那 leading blank lines 和
// spaces 是 dropped until something 是 printed. With RunID 只
// final 文本 在 RunComplete 是 printed. Without one reply 是 printed
// 在 pieces 作为 它的 events arrive.
func TestRunStream_LeadingBlankLines(t *testing.T) {
	t.Parallel()
	for name, texts := range map[string][]string{
		"whole":               {"\nHello"},
		"pieces":              {"\n", "\nHello"},
		"more blank lines":    {"\n", "\n\nHello"},
		"spaces":              {" ", "  Hello"},
		"blank then indented": {"\n", "\n  Hello"},
	} {
		for _, runID := range []string{"", "run-mine"} {
			t.Run(name+"/runID="+runID, func(t *testing.T) {
				t.Parallel()
				buf := &bytes.Buffer{}
				s := &runStream{sessionID: "S", runID: runID, out: buf, seen: map[string]string{}}
				for _, text := range texts {
					done, err := s.handle(replyEvent("m", text), nil)
					require.NoError(t, err)
					require.False(t, done)
				}
				done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
					SessionID: "S", RunID: runID, MessageID: "m", Text: texts[len(texts)-1],
				}}, nil)
				require.NoError(t, err)
				require.True(t, done)
				require.Equal(t, "Hello", buf.String())
			})
		}
	}
}

// TestRunStream_StopSpinnerInvokedOnFirstOutput verifies the
// spinner 是 stopped exactly 当 meaningful output starts (either
// streamed 助手消息 或 reconciliation tail). 这
// matches prior behaviour 和 prevents spinner 从
// painting over final 响应 在 TTYs.
func TestRunStream_StopSpinnerInvokedOnFirstOutput(t *testing.T) {
	t.Parallel()

	calls := 0
	stop := func() { calls++ }
	s := &runStream{sessionID: "S", out: &bytes.Buffer{}, seen: map[string]string{}}
	_, _ = s.handle(pubsub.Event[proto.Message]{Payload: proto.Message{
		ID: "m1", SessionID: "S", Role: proto.Assistant,
		Parts: []proto.ContentPart{proto.TextContent{Text: "hi"}},
	}}, stop)
	require.GreaterOrEqual(t, calls, 1, "spinner must stop once stdout has content")
}

// TestRunStream_RunIDFiltersForeignTurns covers busy-会话
// 队列 scenario: `crush 运行 --continue` attaches 到 会话
// whose currently running turn finishes first, publishing 它的
// RunComplete 在 same 会话 ID. Without per-运行 correlation
// 流 would exit 在 那 foreign 事件 和 drop our own
// queued turn's output. With RunID filtering foreign 事件 是
// ignored 和 只 matching RunComplete terminates 流.
func TestRunStream_RunIDFiltersForeignTurns(t *testing.T) {
	t.Parallel()

	const sessionID = "S"
	const myRun = "run-mine"
	const otherRun = "run-other"

	buf := &bytes.Buffer{}
	s := &runStream{
		sessionID: sessionID,
		runID:     myRun,
		out:       buf,
		seen:      map[string]string{},
	}

	// busy 会话's existing turn emits more 文本 before it finishes.
	done, err := s.handle(pubsub.Event[proto.Message]{Payload: proto.Message{
		ID:        "other-msg",
		SessionID: sessionID,
		Role:      proto.Assistant,
		Parts:     []proto.ContentPart{proto.TextContent{Text: "noise from another turn"}},
	}}, nil)
	require.NoError(t, err)
	require.False(t, done,
		"foreign message on same session must not terminate our run")
	require.Empty(t, buf.String(),
		"foreign message on same session must not write to our stdout")

	// busy 会话's existing turn finishes first.
	done, err = s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: sessionID,
		RunID:     otherRun,
		MessageID: "other-msg",
		Text:      "noise from another turn",
	}}, nil)
	require.NoError(t, err)
	require.False(t, done,
		"foreign RunComplete on same session must not terminate our run")
	require.Empty(t, buf.String(),
		"foreign RunComplete must not write to our stdout")

	// Our own queued turn eventually finishes.
	done, err = s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: sessionID,
		RunID:     myRun,
		MessageID: "my-msg",
		Text:      "OK",
	}}, nil)
	require.NoError(t, err)
	require.True(t, done, "matching RunID must terminate the stream")
	require.Equal(t, "OK", buf.String())
}

func TestRunStream_RunIDSuppressesLiveMessagesAndPrintsRunComplete(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{
		sessionID: "S",
		runID:     "run-mine",
		out:       buf,
		seen:      map[string]string{},
	}

	done, err := s.handle(pubsub.Event[proto.Message]{Payload: proto.Message{
		ID:        "my-msg",
		SessionID: "S",
		Role:      proto.Assistant,
		Parts:     []proto.ContentPart{proto.TextContent{Text: "streamed prefix"}},
	}}, nil)
	require.NoError(t, err)
	require.False(t, done)
	require.Empty(t, buf.String())

	done, err = s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S",
		RunID:     "run-mine",
		MessageID: "my-msg",
		Text:      "streamed prefix final",
	}}, nil)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "streamed prefix final", buf.String())
}

// TestRunStream_AgentErrorRunIDFiltersForeign verifies 那 async
// agent 错误 carrying non-empty RunID 是 fatal 只 当 it matches
// our 运行. 一个 foreign RunID 是 ignored regardless 的 事件's
// SessionID, because RunID 是 authoritative correlator 和 async
// errors share agent 事件 渠道: without strict RunID matching
// unrelated 工作区 failure would abort our 运行.
func TestRunStream_AgentErrorRunIDFiltersForeign(t *testing.T) {
	t.Parallel()

	// Foreign RunID 用 matching 会话 是 still foreign.
	s := &runStream{sessionID: "S", runID: "run-mine", out: &bytes.Buffer{}, seen: map[string]string{}}
	done, err := s.handle(pubsub.Event[proto.AgentEvent]{Payload: proto.AgentEvent{
		Type:      proto.AgentEventTypeError,
		SessionID: "S",
		RunID:     "run-other",
		Error:     errors.New("foreign boom"),
	}}, nil)
	require.NoError(t, err, "foreign RunID error must not abort our run")
	require.False(t, done)

	// Foreign RunID 用 different 会话 是 ignored.
	done, err = s.handle(pubsub.Event[proto.AgentEvent]{Payload: proto.AgentEvent{
		Type:      proto.AgentEventTypeError,
		SessionID: "other",
		RunID:     "run-other",
		Error:     errors.New("foreign boom"),
	}}, nil)
	require.NoError(t, err, "foreign RunID error must not abort our run")
	require.False(t, done)

	// Foreign RunID 用 missing 会话 是 ignored.
	done, err = s.handle(pubsub.Event[proto.AgentEvent]{Payload: proto.AgentEvent{
		Type:  proto.AgentEventTypeError,
		RunID: "run-other",
		Error: errors.New("foreign boom"),
	}}, nil)
	require.NoError(t, err, "foreign RunID error must not abort our run")
	require.False(t, done)

	// Matching RunID 是 fatal.
	done, err = s.handle(pubsub.Event[proto.AgentEvent]{Payload: proto.AgentEvent{
		Type:      proto.AgentEventTypeError,
		SessionID: "S",
		RunID:     "run-mine",
		Error:     errors.New("my boom"),
	}}, nil)
	require.Error(t, err, "matching RunID error must be fatal")
	require.True(t, done)
}

// TestRunStream_AgentErrorNoRunIDFiltersBySession verifies the
// compatibility fallback: 当 事件 carries no RunID, attribution
// falls back 到 SessionID. An 错误 为 another 会话 或 用
// empty 会话 是 ignored, while 错误 为 our own 会话 是 fatal
// 所以 real failure 是 never dropped.
func TestRunStream_AgentErrorNoRunIDFiltersBySession(t *testing.T) {
	t.Parallel()

	s := &runStream{sessionID: "S", out: &bytes.Buffer{}, seen: map[string]string{}}

	// Empty RunID 为 another 会话 是 ignored.
	done, err := s.handle(pubsub.Event[proto.AgentEvent]{Payload: proto.AgentEvent{
		Type:      proto.AgentEventTypeError,
		SessionID: "other",
		Error:     errors.New("foreign boom"),
	}}, nil)
	require.NoError(t, err, "error for another session must not abort our run")
	require.False(t, done)

	// Empty RunID 用 empty 会话 是 ignored.
	done, err = s.handle(pubsub.Event[proto.AgentEvent]{Payload: proto.AgentEvent{
		Type:  proto.AgentEventTypeError,
		Error: errors.New("foreign boom"),
	}}, nil)
	require.NoError(t, err, "error with no session must not abort our run")
	require.False(t, done)

	// Empty RunID 用 matching 会话 是 fatal.
	done, err = s.handle(pubsub.Event[proto.AgentEvent]{Payload: proto.AgentEvent{
		Type:      proto.AgentEventTypeError,
		SessionID: "S",
		Error:     errors.New("my boom"),
	}}, nil)
	require.Error(t, err, "error for our own session must be fatal")
	require.True(t, done)
}

// TestRunStream_NoRunIDFallsBackToSessionID preserves the older
// behaviour 为 callers (和 tests) 那 don't supply RunID:
// SessionID-只 matching still terminates 流 在
// 会话's RunComplete. 这 keeps contract backwards
// compatible 用 servers 那 don't echo RunID 和 用
// pre-existing TestRunStream_* assertions.
func TestRunStream_NoRunIDFallsBackToSessionID(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", out: buf, seen: map[string]string{}}
	done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S",
		MessageID: "m2",
		Text:      "DONE",
	}}, nil)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "DONE", buf.String())
}

// replyEvent returns 消息 事件 为 助手 reply 用
// given 文本 在 会话 "S".
func replyEvent(id, text string) pubsub.Event[proto.Message] {
	return pubsub.Event[proto.Message]{Payload: proto.Message{
		ID: id, SessionID: "S", Role: proto.Assistant,
		Parts: []proto.ContentPart{proto.TextContent{Text: text}},
	}}
}

// TestRunStream_RunIDRetryPrintsFinalText pins what `crush 运行` prints
// through 服务端 当 提供方 请求 是 retried after 片段 的
// reply has streamed. runNonInteractive always supplies RunID, 所以
// 消息 events 是 不 printed 和 stdout comes 从 RunComplete
// alone: retried 文本, even 当 it 是 shorter than what first
// attempt streamed.
func TestRunStream_RunIDRetryPrintsFinalText(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", runID: "run-mine", out: buf, seen: map[string]string{}}
	for _, ev := range []pubsub.Event[proto.Message]{
		replyEvent("m", "A first attempt that"),
		// reset 更新 的 重试 has no parts.
		{Payload: proto.Message{ID: "m", SessionID: "S", Role: proto.Assistant}},
		replyEvent("m", "Real."),
	} {
		done, err := s.handle(ev, nil)
		require.NoError(t, err)
		require.False(t, done)
	}
	require.Empty(t, buf.String())

	done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S", RunID: "run-mine", MessageID: "m", Text: "Real.",
	}}, nil)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "Real.", buf.String())
}

// TestRunStream_NoRunIDRetryReplacesText covers the fallback without a
// RunID, where 消息 events 是 printed 作为 they arrive. 一个 提供方
// 重试 resets reply after 片段 的 it has streamed. 运行 must 不
// fail, 和 new 文本 是 printed 在 full without 它的 leading spaces,
// whether it 是 shorter 或 longer than first attempt. 它 starts 在
// new line unless the first attempt was blank.
func TestRunStream_NoRunIDRetryReplacesText(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		texts []string
		want  string
	}{
		"shorter": {
			texts: []string{"A first attempt that", "Real."},
			want:  "A first attempt that\nReal.",
		},
		"longer": {
			texts: []string{"A first attempt that", "The real answer, which is longer than the first attempt."},
			want:  "A first attempt that\nThe real answer, which is longer than the first attempt.",
		},
		"indented": {
			texts: []string{"First", "  Second"},
			want:  "First\nSecond",
		},
		"blank first attempt": {
			texts: []string{"\n", "Real."},
			want:  "Real.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			buf := &bytes.Buffer{}
			s := &runStream{sessionID: "S", out: buf, seen: map[string]string{}}
			for _, text := range tc.texts {
				done, err := s.handle(replyEvent("m", text), nil)
				require.NoError(t, err)
				require.False(t, done)
			}
			final := tc.texts[len(tc.texts)-1]
			done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
				SessionID: "S", MessageID: "m", Text: final,
			}}, nil)
			require.NoError(t, err)
			require.True(t, done)
			require.Equal(t, tc.want, buf.String())
		})
	}
}

// TestRunStream_NoRunIDReconcilesAfterMissedRetry covers 重试 whose
// 消息 events never reach 流. RunComplete then carries 文本
// 那 does 不 continue what was printed, 所以 it 是 printed 在 full 在
// new line instead 的 being dropped 或 cut 在 old length.
func TestRunStream_NoRunIDReconcilesAfterMissedRetry(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", out: buf, seen: map[string]string{}}
	_, err := s.handle(replyEvent("m", "A first attempt that"), nil)
	require.NoError(t, err)
	done, err := s.handle(pubsub.Event[proto.RunComplete]{Payload: proto.RunComplete{
		SessionID: "S", MessageID: "m", Text: "Real.",
	}}, nil)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "A first attempt that\nReal.", buf.String())
}

// TestRunStream_NoRunIDGrowingAndStaleUpdates covers 文本 那 grows,
// which continues output, 和 更新 older than one already
// printed, which adds nothing. 一个 重试 那 starts 用 文本 的
// failed attempt looks same 作为 growing 文本.
func TestRunStream_NoRunIDGrowingAndStaleUpdates(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", out: buf, seen: map[string]string{}}
	for _, text := range []string{"The ans", "The answer", "The ans", "The answer is 42."} {
		done, err := s.handle(replyEvent("m", text), nil)
		require.NoError(t, err)
		require.False(t, done)
	}
	require.Equal(t, "The answer is 42.", buf.String())
}

// TestRunStream_NoRunIDSkipsSummaries covers the fallback without a
// RunID: 压缩 摘要 是 存储 作为 助手消息 在
// 会话, 但 it 是 不 片段 的 answer, 所以 it 是 不 printed.
func TestRunStream_NoRunIDSkipsSummaries(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	s := &runStream{sessionID: "S", out: buf, seen: map[string]string{}}
	summary := replyEvent("sum", "SUMMARY")
	summary.Payload.IsSummaryMessage = true
	for _, ev := range []pubsub.Event[proto.Message]{
		replyEvent("m1", "Before."), summary, replyEvent("m2", "After."),
	} {
		done, err := s.handle(ev, nil)
		require.NoError(t, err)
		require.False(t, done)
	}
	require.Equal(t, "Before.After.", buf.String())
}
