package agent

import (
	"sync"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// killedTurn seeds shape process killed mid-工具-调用 leaves behind:
// 助手消息 holding 工具调用, no 工具结果 anywhere, 和 no
// finish 片段. finished says whether 调用's arguments had finished
// arriving before the process died.
func killedTurn(t *testing.T, env fakeEnv, finished bool) (session.Session, message.Message) {
	t.Helper()
	ctx := t.Context()

	sess, err := env.sessions.Create(ctx, "killed")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "run the thing"}},
	})
	require.NoError(t, err)

	input := `{"command":"sleep 30"}`
	if !finished {
		input = ""
	}
	assistant, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{
				ID:       "call_killed",
				Name:     "bash",
				Input:    input,
				Finished: finished,
			},
		},
	})
	require.NoError(t, err)
	return sess, assistant
}

func TestRepairInterruptedToolCalls_WritesTheRepair(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	sess, assistant := killedTurn(t, env, false)

	listSettled(t, env, sess.ID)

	stored, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	var result *message.ToolResult
	for _, msg := range stored {
		for _, tr := range msg.ToolResults() {
			if tr.ToolCallID == "call_killed" {
				tr := tr
				result = &tr
			}
		}
	}
	require.NotNil(t, result, "the orphaned call must be given a stored result")
	require.True(t, result.IsError)
	require.Equal(t, interruptedToolCallResult, result.Content)

	reloaded, err := env.messages.Get(ctx, assistant.ID)
	require.NoError(t, err)
	calls := reloaded.ToolCalls()
	require.Len(t, calls, 1)
	require.True(t, calls[0].Finished, "a call that never got its arguments is settled")
	require.Empty(t, calls[0].Input, "arguments that never arrived are not invented")
	require.True(t, reloaded.IsFinished(), "the turn is marked finished")
}

// finish 片段 records 当 turn actually stopped. Stamping it 用
// now would tell 响应-time statistics 那 turn killed months ago
// took months 到 answer.
func TestRepairInterruptedToolCalls_BackdatesTheFinish(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	sess, assistant := killedTurn(t, env, true)

	before, err := env.messages.Get(ctx, assistant.ID)
	require.NoError(t, err)

	listSettled(t, env, sess.ID)

	reloaded, err := env.messages.Get(ctx, assistant.ID)
	require.NoError(t, err)
	finish := reloaded.FinishPart()
	require.NotNil(t, finish)
	require.Equal(t, before.UpdatedAt, finish.Time, "the finish is stamped with when the turn stopped")
}

// 一个 turn can end cleanly 和 still lose 工具结果, 所以 present finish
// 片段 must 不 是 taken 作为 proof 那 nothing 是 dangling.
func TestRepairInterruptedToolCalls_RepairsFinishedTurn(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	sess, err := env.sessions.Create(ctx, "finished-but-orphaned")
	require.NoError(t, err)

	assistant, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_a", Name: "bash", Input: "{}", Finished: true},
			message.Finish{Reason: message.FinishReasonEndTurn, Time: 1000},
		},
	})
	require.NoError(t, err)

	listSettled(t, env, sess.ID)

	stored, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	var found bool
	for _, msg := range stored {
		for _, tr := range msg.ToolResults() {
			found = found || tr.ToolCallID == "call_a"
		}
	}
	require.True(t, found, "an orphan on a finished turn is still repaired")

	reloaded, err := env.messages.Get(ctx, assistant.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1000), reloaded.FinishPart().Time, "an existing finish is left alone")
}

func TestRepairInterruptedToolCalls_LeavesSettledCallsAlone(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	sess, err := env.sessions.Create(ctx, "healthy")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_ok", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
			message.Finish{Reason: message.FinishReasonEndTurn, Time: 1000},
		},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call_ok", Name: "bash", Content: "ok"}},
	})
	require.NoError(t, err)

	before, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	listSettled(t, env, sess.ID)
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, len(before), "a healthy session gains no rows")

	after, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, after, len(before))
}

// Repairing twice must 不 stack up results, since every turn re-reads
// 会话.
func TestRepairInterruptedToolCalls_IsIdempotent(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	sess, _ := killedTurn(t, env, true)

	listSettled(t, env, sess.ID)
	first, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	listSettled(t, env, sess.ID)
	second, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	require.Len(t, second, len(first), "a second pass adds nothing")
}

// Parallel 工具 calls die together, 和 each needs 它的 own 结果 或
// 提供方 rejects whole 消息.
func TestRepairInterruptedToolCalls_HandlesParallelCalls(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	sess, err := env.sessions.Create(ctx, "parallel")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_1", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
			message.ToolCall{ID: "call_2", Name: "view", Input: "", Finished: false},
			message.ToolCall{ID: "call_3", Name: "grep", Input: `{"pattern":"x"}`, Finished: true},
		},
	})
	require.NoError(t, err)
	// Only middle 调用 got answer before process died.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call_2", Name: "view", Content: "file"}},
	})
	require.NoError(t, err)

	listSettled(t, env, sess.ID)

	stored, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	results := map[string]message.ToolResult{}
	for _, msg := range stored {
		for _, tr := range msg.ToolResults() {
			results[tr.ToolCallID] = tr
		}
	}
	require.Len(t, results, 3, "every call ends up with exactly one result")
	require.Equal(t, "file", results["call_2"].Content, "the real result is left alone")
	require.True(t, results["call_1"].IsError)
	require.True(t, results["call_3"].IsError)
}

// 一个 reader 的 transcript settles orphans too, 不 just next 运行.
// Without 这 会话 opened after crash shows 调用 作为 still 在
// flight, 和 first 消息 的 next 运行 rewrites transcript
// under 用户.
func TestListSettledMessages_SettlesBeforeAnyRun(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	sess, assistant := killedTurn(t, env, false)

	listSettled(t, env, sess.ID)

	stored, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	var result *message.ToolResult
	for _, msg := range stored {
		for _, tr := range msg.ToolResults() {
			if tr.ToolCallID == "call_killed" {
				tr := tr
				result = &tr
			}
		}
	}
	require.NotNil(t, result, "opening the session must settle the orphan")

	reloaded, err := env.messages.Get(ctx, assistant.ID)
	require.NoError(t, err)
	require.Empty(t, reloaded.ToolCalls()[0].Input)
	require.True(t, reloaded.IsFinished())
}

// Arguments can stop arriving halfway through. What 模型 managed 到
// 写入 stays 在 transcript; 只 what goes 到 提供方 是 repaired.
func TestListSettledMessages_KeepsHalfWrittenArguments(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	const partial = `{"command":"sleep 3`

	sess, err := env.sessions.Create(ctx, "half-written")
	require.NoError(t, err)
	assistant, err := env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.ToolCall{ID: "call_partial", Name: "bash", Input: partial}},
	})
	require.NoError(t, err)

	listSettled(t, env, sess.ID)

	reloaded, err := env.messages.Get(ctx, assistant.ID)
	require.NoError(t, err)
	call := reloaded.ToolCalls()[0]
	require.True(t, call.Finished, "the call is settled")
	require.Equal(t, partial, call.Input, "the transcript keeps what the model wrote")

	for _, part := range reloaded.ToAIMessage()[0].Content {
		if tc, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part); ok {
			require.Equal(t, "{}", tc.Input, "the send path is what makes it valid JSON")
		}
	}
}

func TestListSettledMessages_HealthySessionIsUntouched(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	sess, err := env.sessions.Create(ctx, "healthy")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_ok", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
			message.Finish{Reason: message.FinishReasonEndTurn, Time: 1000},
		},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call_ok", Name: "bash", Content: "ok"}},
	})
	require.NoError(t, err)

	before, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	listSettled(t, env, sess.ID)
	after, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, after, len(before), "settling a healthy session writes nothing")
}

// listSettled reads 会话 through coordinator, which settles orphans
// first.
func listSettled(t *testing.T, env fakeEnv, sessionID string) []message.Message {
	t.Helper()
	msgs, err := settlingCoordinator(env).ListSettledMessages(t.Context(), sessionID)
	require.NoError(t, err)
	return msgs
}

// returned transcript 是 存储 one, 所以 callers need no second 读取.
func TestListSettledMessages_ReturnsWhatWasStored(t *testing.T) {
	env := testEnv(t)
	ctx := t.Context()

	sess, _ := killedTurn(t, env, true)

	got := listSettled(t, env, sess.ID)
	stored, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, got, len(stored))
	for i := range stored {
		require.Equal(t, stored[i].ID, got[i].ID)
		require.Equal(t, stored[i].Parts, got[i].Parts)
	}
}

func settlingCoordinator(env fakeEnv) *coordinator {
	main := NewSessionAgent(SessionAgentOptions{Models: noModels, Sessions: env.sessions, Messages: env.messages})
	return &coordinator{mainAgent: main, agents: map[string]SessionAgent{config.AgentCoder: main}, messages: env.messages}
}

// 一个 live 运行's unanswered 调用 是 不 orphan yet, 所以 it 是 left alone,
// whether main agent 是 running it 或 子代理 在 它的 own instance.
func TestListSettledMessages_SkipsBusySessions(t *testing.T) {
	for name, markBusy := range map[string]func(t *testing.T, c *coordinator, sessionID string){
		"main agent": func(_ *testing.T, c *coordinator, sessionID string) {
			c.mainAgent.(*sessionAgent).activeRequests.Set(sessionID, &activeCancel{cancel: func() {}})
		},
		"subagent": func(t *testing.T, c *coordinator, sessionID string) {
			t.Cleanup(c.trackSubSession(sessionID))
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := testEnv(t)
			sess, _ := killedTurn(t, env, true)
			c := settlingCoordinator(env)
			markBusy(t, c, sess.ID)

			msgs, err := c.ListSettledMessages(t.Context(), sess.ID)
			require.NoError(t, err)
			for _, m := range msgs {
				require.Empty(t, m.ToolResults(), "a live run's unanswered call must be left alone")
			}
		})
	}
}

// Switching coder 到 plan must 不 make 运行 still going 在 coder look 空闲,
// 或 reader would settle 它的 在-flight calls.
func TestCoordinatorIsSessionBusy_SeesEveryMainAgent(t *testing.T) {
	env := testEnv(t)
	coder := NewSessionAgent(SessionAgentOptions{Models: noModels, Sessions: env.sessions, Messages: env.messages}).(*sessionAgent)
	plan := NewSessionAgent(SessionAgentOptions{Models: noModels, Sessions: env.sessions, Messages: env.messages})
	c := &coordinator{mainAgent: plan, agents: map[string]SessionAgent{"coder": coder, "plan": plan}}

	coder.activeRequests.Set("sess", &activeCancel{cancel: func() {}})
	require.True(t, c.IsSessionBusy("sess"))
	require.False(t, c.IsSessionBusy("other"))
}

// Several clients can reload one 会话 在 once, say after reconnect.
// Each must 不 settle same orphan, 或 调用 ends up 用 two results.
func TestListSettledMessages_ConcurrentReadersSettleOnce(t *testing.T) {
	env := testEnv(t)
	sess, _ := killedTurn(t, env, true)
	c := settlingCoordinator(env)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := c.ListSettledMessages(t.Context(), sess.ID)
			require.NoError(t, err)
		})
	}
	wg.Wait()

	stored, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var results int
	for _, m := range stored {
		results += len(m.ToolResults())
	}
	require.Equal(t, 1, results, "the orphan must be settled exactly once")
}
