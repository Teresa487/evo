package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// 一个 turn stopped 在 它的 output token limit finishes 用 FinishReasonLength,
// 所以 nothing 在 错误 路径 settles 调用 it left unfinished.
func TestSettleTurn(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	agent := &sessionAgent{messages: env.messages}

	sess, err := env.sessions.Create(t.Context(), "t")
	require.NoError(t, err)

	assistant, err := env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{},
	})
	require.NoError(t, err)

	// One 调用 那 landed normally, one cut off mid arguments.
	assistant.AddToolCall(message.ToolCall{ID: "done", Name: "view", Input: `{"file_path":"a"}`, Finished: true})
	assistant.AddToolCall(message.ToolCall{ID: "cutoff", Name: "write", Finished: false})
	assistant.AddFinish(message.FinishReasonMaxTokens, "", "")
	require.NoError(t, env.messages.Update(t.Context(), assistant))

	require.NoError(t, agent.settleTurn(t.Context(), &assistant, answeredOnly("done"), nil))

	calls := assistant.ToolCalls()
	require.Len(t, calls, 2)
	for _, tc := range calls {
		require.True(t, tc.Finished, "%s should be finished", tc.ID)
	}

	// Whatever 模型 managed 到 写入 stays 在 transcript; send
	// 路径 是 where it becomes something 提供方 will 受理.
	for _, part := range assistant.ToAIMessage()[0].Content {
		if tc, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part); ok {
			require.True(t, json.Valid([]byte(tc.Input)), "%s should reach the provider parseable", tc.ToolCallID)
		}
	}

	// Without 结果 next 请求 carries 工具调用 用 no reply,
	// which providers reject.
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var results []message.ToolResult
	for _, m := range msgs {
		if m.Role == message.Tool {
			results = append(results, m.ToolResults()...)
		}
	}
	require.Len(t, results, 1)
	require.Equal(t, "cutoff", results[0].ToolCallID)
	require.True(t, results[0].IsError)
	require.Equal(t, truncatedToolCallResult, results[0].Content)
}

// 一个 safety classifier can stop 响应 mid 工具调用 too. Reporting 那
// 作为 output limit sends next attempt after wrong fix.
func TestSettleTurnReportsRefusal(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	agent := &sessionAgent{messages: env.messages}

	sess, err := env.sessions.Create(t.Context(), "t")
	require.NoError(t, err)

	assistant, err := env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{},
	})
	require.NoError(t, err)
	assistant.AddToolCall(message.ToolCall{ID: "cutoff", Name: "bash", Finished: false})
	assistant.AddFinish(message.FinishReasonContentFilter, "", "")
	require.NoError(t, env.messages.Update(t.Context(), assistant))

	require.NoError(t, agent.settleTurn(t.Context(), &assistant, answeredOnly("done"), nil))

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var results []message.ToolResult
	for _, m := range msgs {
		if m.Role == message.Tool {
			results = append(results, m.ToolResults()...)
		}
	}
	require.Len(t, results, 1)
	require.True(t, results[0].IsError)
	require.Equal(t, refusedToolCallResult, results[0].Content)
}

// Completed turns must 是 left alone, 和 must 不 gain 工具结果 messages.
func TestSettleTurnLeavesCompleteTurnsAlone(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	agent := &sessionAgent{messages: env.messages}

	sess, err := env.sessions.Create(t.Context(), "t")
	require.NoError(t, err)

	assistant, err := env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{},
	})
	require.NoError(t, err)
	assistant.AddToolCall(message.ToolCall{ID: "done", Name: "view", Input: `{}`, Finished: true})
	assistant.AddFinish(message.FinishReasonToolUse, "", "")
	require.NoError(t, env.messages.Update(t.Context(), assistant))

	require.NoError(t, agent.settleTurn(t.Context(), &assistant, answeredOnly("done"), nil))

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	for _, m := range msgs {
		require.NotEqual(t, message.Tool, m.Role, "no tool result should be invented")
	}
}

// answeredOnly reports reply 为 exactly given 工具调用 IDs.
func answeredOnly(ids ...string) func(string) bool {
	return func(id string) bool { return slices.Contains(ids, id) }
}

// 一个 failed 或 canceled turn settles every 调用 still waiting, finished 或
// 不, 和 says which 的 two happened.
func TestSettleTurnOnRunError(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"canceled": {context.Canceled, canceledToolCallResult},
		"failed":   {errors.New("boom"), failedToolCallResult},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env := testEnv(t)
			agent := &sessionAgent{messages: env.messages}

			sess, err := env.sessions.Create(t.Context(), "t")
			require.NoError(t, err)
			assistant, err := env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
				Role: message.Assistant,
				Parts: []message.ContentPart{
					message.ToolCall{ID: "done", Name: "view", Input: `{}`, Finished: true},
					message.ToolCall{ID: "running", Name: "bash", Input: `{}`, Finished: true},
					message.ToolCall{ID: "cutoff", Name: "write", Finished: false},
				},
			})
			require.NoError(t, err)

			require.NoError(t, agent.settleTurn(t.Context(), &assistant, answeredOnly("done"), tc.err))

			msgs, err := env.messages.List(t.Context(), sess.ID)
			require.NoError(t, err)
			replies := map[string]string{}
			for _, m := range msgs {
				for _, tr := range m.ToolResults() {
					replies[tr.ToolCallID] = tr.Content
				}
			}
			require.Equal(t, map[string]string{"running": tc.want, "cutoff": tc.want}, replies)
		})
	}
}
