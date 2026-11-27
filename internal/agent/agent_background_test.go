package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackgroundLabel(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "agent-123456", backgroundLabel(AgentToolName, "toolcall123456"))
	assert.Equal(t, "search-abc123", backgroundLabel(tools.AgenticFetchToolName, "toolu_abc123"))
}

// 一个 non-interactive coordinator keeps blocking, 所以 agent's output comes
// straight back 和 nothing 是 left 在 registry.
func TestRunSubAgentMaybeBackground_Headless(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	coord.interactive = false
	coord.dispatched = csync.NewMap[string, *dispatchedAgent]()
	coord.backgroundAfter = time.Millisecond

	parent, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	agent := newMockAgent(providerID, 4096, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		return agentResultWithText("blocking answer"), nil
	})

	resp, err := coord.runSubAgentMaybeBackground(t.Context(), AgentToolName, accessRead, subAgentParams{
		Agent:          agent,
		SessionID:      parent.ID,
		AgentMessageID: "msg-1",
		ToolCallID:     "call-1",
		Prompt:         "do something",
		SessionTitle:   "Test",
	})
	require.NoError(t, err)
	assert.Equal(t, "blocking answer", resp.Content)
	assert.Equal(t, 0, coord.dispatched.Len())
}

// 一个 fast interactive 调用 finishes before threshold, 所以 it still returns
// 它的 output directly 和 leaves registry empty.
func TestRunSubAgentMaybeBackground_Foreground(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	coord.interactive = true
	coord.dispatched = csync.NewMap[string, *dispatchedAgent]()
	coord.backgroundAfter = 5 * time.Second

	parent, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	agent := newMockAgent(providerID, 4096, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		return agentResultWithText("fast answer"), nil
	})

	resp, err := coord.runSubAgentMaybeBackground(t.Context(), AgentToolName, accessRead, subAgentParams{
		Agent:          agent,
		SessionID:      parent.ID,
		AgentMessageID: "msg-1",
		ToolCallID:     "call-1",
		Prompt:         "do something",
		SessionTitle:   "Test",
	})
	require.NoError(t, err)
	assert.Equal(t, "fast answer", resp.Content)
	assert.False(t, resp.IsError)
	assert.Equal(t, 0, coord.dispatched.Len(), "a fast call must not linger in the registry")
}

// 一个 slow interactive 调用 crosses threshold, 所以 it hands off: 工具
// returns background notice, 子代理 是 registered under label, 和
// once it settles 它的 outcome 是 reported into parent 会话.
func TestRunSubAgentMaybeBackground_Background(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	coord.interactive = true
	coord.dispatched = csync.NewMap[string, *dispatchedAgent]()
	coord.backgroundAfter = 20 * time.Millisecond

	parent, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	// 子代理 runs until 它的 detached 上下文 是 cancelled, which 是
	// how it outruns threshold 和, later, how stop unwinds it.
	agent := newMockAgent(providerID, 4096, func(ctx context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	resp, err := coord.runSubAgentMaybeBackground(t.Context(), AgentToolName, accessRead, subAgentParams{
		Agent:          agent,
		SessionID:      parent.ID,
		AgentMessageID: "msg-1",
		ToolCallID:     "toolcall999999",
		Prompt:         "long task",
		SessionTitle:   "Test",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "background")

	var meta AgentBackgroundMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	assert.True(t, meta.Backgrounded)
	label := meta.Label
	assert.True(t, strings.HasSuffix(label, "-agent-999999"), label)
	assert.Equal(t, "long task", meta.Task)

	entry, ok := coord.dispatched.Get(dispatchKey(parent.ID, label))
	require.True(t, ok, "backgrounded subagent must be in the registry")
	assert.True(t, entry.running())

	// Stopping it settles 运行, which records 报告 在 parent.
	require.True(t, entry.stop("test stop"))
	require.True(t, entry.awaitSettled(2*time.Second))

	require.Eventually(t, func() bool {
		msgs, err := env.messages.List(t.Context(), parent.ID)
		if err != nil {
			return false
		}
		for _, m := range msgs {
			for _, r := range m.SubAgentReports() {
				if r.Label == label && r.Failed {
					return true
				}
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "a stopped background subagent must report back")
}

// dispatchHistory promotes backgrounded blocking 调用 到 first-class
// 子代理, 和 later 报告 closes it, just 作为 为 派发.
func TestDispatchHistoryBackgroundedBlockingCall(t *testing.T) {
	t.Parallel()

	bgMeta, err := json.Marshal(AgentBackgroundMetadata{
		Label:        "search-1",
		Backgrounded: true,
		Access:       accessRead,
		Task:         "search x",
	})
	require.NoError(t, err)

	fetchInput, err := json.Marshal(tools.AgenticFetchParams{Prompt: "search x"})
	require.NoError(t, err)

	msgs := []message.Message{
		{ID: "m1", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "c1", Name: tools.AgenticFetchToolName, Input: string(fetchInput), Finished: true},
			// 一个 second blocking 调用 那 finished 在 foreground has
			// no background metadata, 所以 it must 不 become 子代理.
			message.ToolCall{ID: "c2", Name: AgentToolName, Input: `{"prompt":"quick"}`, Finished: true},
		}},
		{ID: "t1", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "c1", Name: tools.AgenticFetchToolName, Content: "moved to background", Metadata: string(bgMeta)},
			message.ToolResult{ToolCallID: "c2", Name: AgentToolName, Content: "quick answer"},
		}},
	}

	records := dispatchHistory(msgs)
	require.Equal(t, []string{"search-1"}, records.order)

	rec := records.byLabel["search-1"]
	require.NotNil(t, rec)
	assert.Equal(t, tools.AgenticFetchToolName, rec.Tool)
	assert.Equal(t, "m1", rec.MessageID)
	assert.Equal(t, "c1", rec.CallID)
	assert.Equal(t, "search x", rec.Task)
	assert.True(t, rec.Open)

	// 一个 报告 under 那 label closes it.
	msgs = append(msgs, message.Message{ID: "u1", Role: message.User, Parts: []message.ContentPart{
		message.SubAgentReport{Label: "search-1", Output: "found"},
	}})
	rec = dispatchHistory(msgs).byLabel["search-1"]
	require.NotNil(t, rec)
	assert.False(t, rec.Open)
	require.NotNil(t, rec.LastReport)
	assert.Equal(t, "found", rec.LastReport.Output)
}
