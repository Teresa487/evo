package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// newTestUserItem builds UserMessageItem carrying 文本.
func newTestUserItem(t *testing.T, text string) *UserMessageItem {
	t.Helper()
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:    "user-1",
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: text}},
	}
	item := NewUserMessageItem(&sty, msg, nil)
	userItem, ok := item.(*UserMessageItem)
	require.True(t, ok, "NewUserMessageItem must return *UserMessageItem")
	return userItem
}

// renderedLines returns rendered 消息 作为 trimmed, non-empty-aware
// lines 所以 assertions can talk about visual line structure without
// depending 在 ANSI styling 或 trailing pad.
func renderedLines(t *testing.T, text string, width int) []string {
	t.Helper()
	out := newTestUserItem(t, text).RawRender(width)
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(ansi.Strip(l))
	}
	return lines
}

// TestUserMessagePreservesSingleLineBreaks 是 regression 测试 为
// charmbracelet/crush#3502: 用户 submitting
//
//	a
//	b
//
//	c
//
// saw "" 和 "b" collapsed onto one line ("ab"/" b") 在 chat
// display, because 用户 input was rendered through standard
// Markdown renderer where lone newline 是 soft break. history
// sent 到 模型 was always correct; 只 display was wrong.
func TestUserMessagePreservesSingleLineBreaks(t *testing.T) {
	t.Parallel()

	lines := renderedLines(t, "a\nb\n\nc", 80)

	var got []string
	for _, l := range lines {
		if l != "" {
			got = append(got, l)
		}
	}
	require.Equal(t, []string{"a", "b", "c"}, got,
		"each typed line must render on its own visual line")

	// blank line 用户 typed between "b" 和 "c" must survive 作为
	// paragraph break, 所以 这 是 不 merely "everything got hard-wrapped".
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "b\n\nc",
		"the blank line between paragraphs must be preserved")
}

// TestUserMessageSoftWrapsLongLines guards the other direction: a
// single long line 那 exceeds render width must still soft wrap.
// Preserving *authored* newlines must 不 disable wrapping.
func TestUserMessageSoftWrapsLongLines(t *testing.T) {
	t.Parallel()

	long := "this is a single very long line of user input that comfortably exceeds the render width and therefore has to be soft wrapped by the renderer"
	lines := renderedLines(t, long, 40)

	var nonEmpty int
	for _, l := range lines {
		if l != "" {
			nonEmpty++
		}
		require.LessOrEqual(t, len(l), 40,
			"no rendered line may exceed the render width")
	}
	require.Greater(t, nonEmpty, 1,
		"a line longer than the width must wrap onto multiple lines")
}

// TestUserMessageMarkdownConstructsUnaffected pins blast radius 的
// #3502 fix: preserving newlines must 不 disturb block-level
// Markdown 那 users legitimately paste into 提示词.
func TestUserMessageMarkdownConstructsUnaffected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "bullet list keeps one item per line",
			input: "- one\n- two\n- three",
			want:  []string{"one", "two", "three"},
		},
		{
			name:  "numbered list keeps one item per line",
			input: "1. first\n2. second",
			want:  []string{"first", "second"},
		},
		{
			name:  "fenced code block keeps its lines",
			input: "```go\nfunc main() {\n\tx := 1\n}\n```",
			want:  []string{"func main() {", "x := 1", "}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			joined := strings.Join(renderedLines(t, tt.input, 80), "\n")
			for _, w := range tt.want {
				require.Contains(t, joined, w)
			}
		})
	}
}
