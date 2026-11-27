package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// 一个 调用 提供方 cut off before writing any input shows 它的 real 错误,
// 不 它的 renderer's "Invalid parameters".
func TestEmptyInputCallShowsItsError(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	item := NewToolMessageItem(&sty, "msg-1", message.ToolCall{ID: "w1", Name: "write", Finished: true},
		&message.ToolResult{ToolCallID: "w1", Name: "write", Content: "the provider's safety classifier stopped this response", IsError: true}, false, "/tmp")
	out := ansi.Strip(item.RawRender(120))
	require.Contains(t, out, "Write")
	require.Contains(t, out, "safety classifier stopped")
	require.NotContains(t, out, "Invalid parameters")
}
