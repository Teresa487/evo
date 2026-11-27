package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	agenttools "github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestToolProgressScopesToSessionAndDirectItem verifies 那 live 工具
// progress updates ignore events 从 other sessions 和 do 不 mutate
// parent agent item 当 nested 子代理 工具 shares 它的 index 条目.
func TestToolProgressScopesToSessionAndDirectItem(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.session = &session.Session{ID: "sess-1"}
	sty := u.com.Styles

	bashItem := chat.NewToolMessageItem(sty, "m1", message.ToolCall{
		ID:       "bash-1",
		Name:     agenttools.BashToolName,
		Input:    `{"command":"echo hi"}`,
		Finished: true,
	}, nil, false, "/tmp")

	agentItem := chat.NewToolMessageItem(sty, "m1", message.ToolCall{
		ID:       "agent-1",
		Name:     agent.AgentToolName,
		Input:    `{"prompt":"do work"}`,
		Finished: true,
	}, nil, false, "/tmp")
	nestedBash := chat.NewToolMessageItem(sty, "m2", message.ToolCall{
		ID:       "nested-bash-1",
		Name:     agenttools.BashToolName,
		Input:    `{"command":"echo sub"}`,
		Finished: true,
	}, nil, false, "/tmp")
	agentItem.(chat.NestedToolContainer).SetNestedTools([]chat.ToolMessageItem{nestedBash})

	u.chat.AppendMessages(bashItem, agentItem)
	u.chat.UpdateNestedToolIDs("agent-1")

	// Different 会话 must 是 ignored.
	u.Update(pubsub.Event[agenttools.ToolProgress]{
		Type: pubsub.UpdatedEvent,
		Payload: agenttools.ToolProgress{
			SessionID:  "other-session",
			ToolCallID: "bash-1",
			Output:     "wrong-session",
		},
	})
	require.NotContains(t, ansi.Strip(bashItem.Render(100)), "wrong-session")

	// Matching 会话 updates direct bash item.
	u.Update(pubsub.Event[agenttools.ToolProgress]{
		Type: pubsub.UpdatedEvent,
		Payload: agenttools.ToolProgress{
			SessionID:  "sess-1",
			ToolCallID: "bash-1",
			Output:     "live-line",
		},
	})
	require.Contains(t, ansi.Strip(bashItem.Render(100)), "live-line")

	// Nested 工具 ID aliases 到 parent agent item 在 idInxMap; progress
	// 为 nested 调用 must 不 mutate 或 bump parent agent item.
	vBefore := agentItem.Version()
	u.Update(pubsub.Event[agenttools.ToolProgress]{
		Type: pubsub.UpdatedEvent,
		Payload: agenttools.ToolProgress{
			SessionID:  "sess-1",
			ToolCallID: "nested-bash-1",
			Output:     "subagent-output",
		},
	})
	require.Equal(t, vBefore, agentItem.Version())
}
