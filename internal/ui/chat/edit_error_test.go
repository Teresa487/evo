package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// An edit can fail before it ever produces diff — refusing 到 touch 文件
// 那 was never 读取, 为 one — 和 failure then has nothing 到 show
// underneath it. 错误 used 到 是 followed 通过 blank line 和 empty
// diff, which 读取 作为 gap 在 transcript 用 nothing 在 it.
func TestEditErrorWithoutDiffHasNoTrailingBlank(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	// Metadata 那 parses 但 carries no old/new content, which 是 what
	// pre-edit 拒绝 leaves behind.
	const noDiffMeta = `{"hook":{"hooks":[{"name":"wakatime","event":"PreToolUse","duration_ms":12}]}}`

	tests := []struct {
		name string
		item func() ToolMessageItem
	}{
		{
			name: "edit",
			item: func() ToolMessageItem {
				return NewEditToolMessageItem(&sty,
					message.ToolCall{ID: "1", Name: "edit", Input: `{"file_path":"/tmp/x.h"}`, Finished: true},
					&message.ToolResult{ToolCallID: "1", Content: "you must read the file before editing it", IsError: true, Metadata: noDiffMeta},
					false)
			},
		},
		{
			name: "multiedit",
			item: func() ToolMessageItem {
				return NewMultiEditToolMessageItem(&sty,
					message.ToolCall{ID: "1", Name: "multiedit", Input: `{"file_path":"/tmp/x.h","edits":[]}`, Finished: true},
					&message.ToolResult{ToolCallID: "1", Content: "you must read the file before editing it", IsError: true, Metadata: noDiffMeta},
					false)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lines := strings.Split(tt.item().Render(100), "\n")
			require.NotEmpty(t, lines)

			last := lines[len(lines)-1]
			require.NotEmpty(t, strings.TrimSpace(stripANSI(last)),
				"the item ends on a blank line, which shows up as a gap below the "+
					"error with nothing in it; full render: %q", strings.Join(lines, "\n"))
		})
	}
}
