package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func renderPlain(t *testing.T, item MessageItem) []string {
	t.Helper()
	return strings.Split(ansi.Strip(item.RawRender(100)), "\n")
}

// 一个 报告's header names 子代理 和 它的 模型, 和 它的 body starts
// 在 same column 作为 header's 文本 rather than indented past it.
func TestSubagentReportAlignsWithHeader(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	item := NewSubAgentReportItem(&sty, "bjorn-date-impl", "hyper/glm-5", "All done.", false)
	lines := renderPlain(t, item)

	require.Contains(t, lines[0], "Subagent Report → Björn date-impl (model=hyper/glm-5)")
	headerText := ansi.StringWidth(lines[0][:strings.Index(lines[0], "Subagent Report")])
	body := lines[2]
	require.Equal(t, headerText, len(body)-len(strings.TrimLeft(body, " ")), "body: %q", body)
}

// 一个 follow-up names 它的 子代理 和 shows 消息 作为 prose, 不
// 调用's JSON 或 delivery receipt.
func TestAgentSendShowsTheMessage(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "msg-1", message.ToolCall{
		ID:       "call-1",
		Name:     agent.AgentSendToolName,
		Input:    `{"label":"betty-date-impl","message":"Please fix the lint."}`,
		Finished: true,
	}, &message.ToolResult{
		ToolCallID: "call-1",
		Content:    `Delivered to "date-impl", which had finished.`,
	}, false, "/tmp")
	out := strings.Join(renderPlain(t, item), "\n")

	require.Contains(t, out, "Message Subagent → Betty date-impl")
	require.Contains(t, out, "Please fix the lint.")
	require.NotContains(t, out, `"label"`)
	require.NotContains(t, out, "Delivered")
}

// 派发 header shows 模型 子代理 actually runs 在, which
// 派发 结果 records even without override.
func TestDispatchHeaderShowsRunModel(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "msg-1", message.ToolCall{
		ID:       "call-1",
		Name:     agent.AgentDispatchToolName,
		Input:    `{"prompt":"look","label":"audit"}`,
		Finished: true,
	}, &message.ToolResult{
		ToolCallID: "call-1",
		Content:    `Started subagent "audit".`,
		Metadata:   `{"label":"sasha-audit","run_model":"hyper/glm-5"}`,
	}, false, "/tmp")
	lines := renderPlain(t, item)
	require.Contains(t, lines[0], "Create Subagent → Sasha audit (model=hyper/glm-5)")
}

// 一个 long 模型 name 在 narrow pane 是 what hid override's 模型:
// shared header drops key=值 pairs once they leave under 30 columns.
func TestSubagentHeaderKeepsLongModelOnNarrowPane(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	h := ansi.Strip(subagentHeader(&sty, ToolStatusSuccess, "Create Subagent", "smoke-override", "anthropic/claude-haiku-4-5-20251001", 72, nil))
	require.Contains(t, h, "(model=")
	require.LessOrEqual(t, ansi.StringWidth(h), 72)
}

// agent_status draws one aligned 行 per 子代理 从 它的 metadata.
func TestAgentStatusRendersRows(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "msg-1", message.ToolCall{
		ID: "call-1", Name: agent.AgentStatusToolName, Input: `{}`, Finished: true,
	}, &message.ToolResult{
		ToolCallID: "call-1",
		Content:    "- a: finished",
		Metadata:   `{"agents":[{"label":"betty-a","state":"finished","timing":"5m ago","access":"read","tool_calls":2},{"label":"sasha-long-label","state":"running","timing":"for 1m","access":"write","tool_calls":1}]}`,
	}, false, "/tmp")
	lines := renderPlain(t, item)

	require.NotContains(t, lines[0], "→", "a status of every subagent names no target")
	a, long := lines[2], lines[3]
	require.Contains(t, a, "✓ Betty a")
	require.Contains(t, a, "finished 5m ago · 2 tool calls")
	require.Contains(t, long, "running for 1m · 1 tool call · can write")
	require.Equal(t, strings.Index(a, "finished"), strings.Index(long, "running"), "states line up")
}

// 一个 stopped 子代理 was stopped 在 purpose, 所以 it reads 作为 canceled, 不 failed.
func TestAgentStateIconStoppedIsNotAnError(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	require.Equal(t, toolIcon(&sty, ToolStatusCanceled), agentStateIcon(&sty, "stopped"))
	require.Equal(t, toolIcon(&sty, ToolStatusError), agentStateIcon(&sty, "failed"))
}

// Markdown output ends 用 它的 last line 的 文本, 不 painted blank line.
func TestSubagentReportHasNoBlankTail(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	for _, out := range []string{"Done.\n\nAll good.", "Contents:\n\n```\na\nb\n```"} {
		lines := renderPlain(t, NewSubAgentReportItem(&sty, "x", "", out, false))
		require.NotEmpty(t, strings.TrimSpace(lines[len(lines)-1]), "%q", out)
	}
}

// 一个 派发 goes 通过 label it asked 为 until 它的 结果 names
// 子代理, 和 从 then 在 通过 那 name, which 它的 报告 shares.
func TestDispatchAdoptsTheNameItWasGiven(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "msg-1", message.ToolCall{
		ID:       "call-1",
		Name:     agent.AgentDispatchToolName,
		Input:    `{"prompt":"look","label":"audit"}`,
		Finished: true,
	}, nil, false, "/tmp")
	dispatch := item.(*AgentToolMessageItem)
	require.Equal(t, "audit", dispatch.DispatchLabel())

	dispatch.SetResult(&message.ToolResult{
		ToolCallID: "call-1",
		Content:    `Started subagent "xochitl-audit".`,
		Metadata:   `{"label":"xochitl-audit"}`,
	})
	require.Equal(t, "xochitl-audit", dispatch.DispatchLabel())
	require.Contains(t, renderPlain(t, dispatch)[0], "Create Subagent → Xóchitl audit")
}

// 一个 busy 子代理's item shows 只 它的 latest 工具 calls until expanded,
// 所以 it stays readable 和 cheap 到 redraw however much 子代理 does.
func TestDispatchCollapsesOlderToolCalls(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "m", message.ToolCall{
		ID: "c", Name: agent.AgentDispatchToolName, Input: `{"prompt":"look","label":"r"}`, Finished: true,
	}, &message.ToolResult{ToolCallID: "c", Metadata: `{"label":"betty-r"}`}, false, "/tmp").(*AgentToolMessageItem)
	for i := range collapsedNestedTools + 5 {
		id := fmt.Sprintf("n%02d", i)
		item.AddNestedTool(NewToolMessageItem(&sty, "m", message.ToolCall{
			ID: id, Name: "view", Input: fmt.Sprintf(`{"file_path":"/tmp/f%02d.go"}`, i), Finished: true,
		}, &message.ToolResult{ToolCallID: id}, false, "/tmp"))
	}

	out := strings.Join(renderPlain(t, item), "\n")
	require.Contains(t, out, "… 5 earlier tool calls")
	require.NotContains(t, out, "f04.go")
	require.Contains(t, out, "f05.go")
	require.Contains(t, out, fmt.Sprintf("f%02d.go", collapsedNestedTools+4))

	item.ToggleExpanded()
	out = strings.Join(renderPlain(t, item), "\n")
	require.NotContains(t, out, "earlier tool calls")
	require.Contains(t, out, "f00.go")
}
