package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func dispatchItem(t *testing.T) *AgentToolMessageItem {
	t.Helper()
	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "msg-1", message.ToolCall{
		ID:       "call-1",
		Name:     agent.AgentDispatchToolName,
		Input:    `{"prompt":"look into it","label":"audit"}`,
		Finished: true,
	}, &message.ToolResult{
		ToolCallID: "call-1",
		Content:    "Started subagent \"audit\".",
	}, false, "/tmp")

	dispatch, ok := item.(*AgentToolMessageItem)
	require.True(t, ok)
	require.Equal(t, "audit", dispatch.DispatchLabel())
	return dispatch
}

func running(t *testing.T, dispatch *AgentToolMessageItem) bool {
	t.Helper()
	status, ok := dispatch.DispatchStatus()
	require.True(t, ok)
	return status.Running
}

// 一个 派发 调用 是 answered moment 它的 子代理 starts, 所以
// presence 的 结果 says nothing about whether work 是 done. Judging
// it 通过 结果 alone leaves finished 子代理 looking like it 是
// still running after 会话 是 reopened.
func TestDispatchRunsUntilItReports(t *testing.T) {
	t.Parallel()

	dispatch := dispatchItem(t)
	require.True(t, running(t, dispatch), "a launched subagent has not answered yet")
	require.True(t, dispatch.Spinning())

	dispatch.MarkDispatchReported()
	require.False(t, running(t, dispatch), "the report settles it")
	require.False(t, dispatch.Spinning())
}

// plain agent 工具 是 不 派发, 所以 it carries no 派发 status.
func TestSyncAgentToolHasNoDispatchStatus(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "msg-1", message.ToolCall{
		ID: "call-1", Name: agent.AgentToolName, Input: `{"prompt":"go"}`, Finished: true,
	}, &message.ToolResult{ToolCallID: "call-1", Content: "done"}, false, "/tmp")

	_, ok := item.(*AgentToolMessageItem).DispatchStatus()
	require.False(t, ok)
}

func TestDispatchCancelledIsSettled(t *testing.T) {
	t.Parallel()

	dispatch := dispatchItem(t)
	dispatch.SetStatus(ToolStatusCanceled)
	require.False(t, running(t, dispatch), "a cancelled dispatch is settled")
}

// 一个 refused 派发 never started 子代理, 所以 it has no status 到 show.
func TestRefusedDispatchHasNoStatus(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "msg-1", message.ToolCall{
		ID:       "call-1",
		Name:     agent.AgentDispatchToolName,
		Input:    `{"prompt":"look","label":"audit"}`,
		Finished: true,
	}, &message.ToolResult{ToolCallID: "call-1", Content: "unknown model", IsError: true}, false, "/tmp")
	_, ok := item.(*AgentToolMessageItem).DispatchStatus()
	require.False(t, ok)
}

// 一个 blocking agent 调用 那 moved 到 background 是 子代理 从
// then 在: it keeps spinning until 它的 报告, 和 sidebar lists it.
func TestBackgroundedAgentCallIsADispatch(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "msg-1", message.ToolCall{
		ID: "call-1", Name: agent.AgentToolName, Input: `{"prompt":"long"}`, Finished: true,
	}, nil, false, "/tmp").(*AgentToolMessageItem)
	_, ok := item.DispatchStatus()
	require.False(t, ok, "a blocking call is not a dispatch")

	item.SetResult(&message.ToolResult{
		ToolCallID: "call-1",
		Content:    "moved to the background",
		Metadata:   `{"label":"sofia-agent-123456","backgrounded":true}`,
	})
	require.Equal(t, "sofia-agent-123456", item.DispatchLabel())
	require.True(t, running(t, item))
	require.True(t, item.Spinning())

	item.MarkDispatchReported()
	require.False(t, running(t, item))
}
