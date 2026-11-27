package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// renderCancelledBash renders finished bash 调用 用 given 结果 作为
// plain 文本.
func renderCancelledBash(t *testing.T, result message.ToolResult, canceled bool) string {
	t.Helper()
	sty := styles.CharmtonePantera()
	result.ToolCallID = "c1"
	item := NewToolMessageItem(&sty, "m1",
		message.ToolCall{ID: "c1", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
		&result, canceled, "/tmp",
	)
	return ansi.Strip(item.Render(120))
}

// An 错误 结果 在 cancelled turn 是 interruption, 不 工具 failure;
// "上下文 canceled" 是 what sessions saved before agent relabelled it hold.
func TestCancelledTurnRendersToolErrorAsCancelled(t *testing.T) {
	t.Parallel()

	for _, content := range []string{"tool call was interrupted and did not produce a result", "context canceled"} {
		out := renderCancelledBash(t, message.ToolResult{Content: content, IsError: true}, true)
		require.Contains(t, out, "Canceled")
		require.NotContains(t, out, content)
	}
}

// 一个 工具 那 finished before turn was cancelled keeps 它的 output.
func TestCancelledTurnKeepsSuccessfulToolOutput(t *testing.T) {
	t.Parallel()

	out := renderCancelledBash(t, message.ToolResult{Content: "main.go"}, true)
	require.Contains(t, out, "main.go")
	require.NotContains(t, out, "Canceled")
}

// Without cancellation, genuine 工具 错误 still reads 作为 错误.
func TestToolErrorStillRendersAsError(t *testing.T) {
	t.Parallel()

	require.Contains(t, renderCancelledBash(t, message.ToolResult{Content: "no such file", IsError: true}, false), "no such file")
}
