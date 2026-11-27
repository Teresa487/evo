package list

import (
	"strings"
	"testing"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func TestHighlightContentWrappedLines(t *testing.T) {
	t.Parallel()

	// 一个 long line 那 will wrap 在 width 20.
	content := "This is a long line that should wrap around"
	width := 20

	// When selecting entire content, wrapped portions should 是 joined
	// 用 spaces, 不 newlines.
	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)

	// 结果 should contain 只 one trailing newline, no internal ones
	// 从 wrapping.
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	require.Len(t, lines, 1, "wrapped content should be a single logical line")
}

func TestHighlightContentRealNewlinesPreserved(t *testing.T) {
	t.Parallel()

	// Short lines 那 don't wrap should preserve real newlines.
	content := "first\nsecond"
	width := 40

	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)

	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	require.Len(t, lines, 2, "real newlines should be preserved")
	require.Contains(t, lines[0], "first")
	require.Contains(t, lines[1], "second")
}

func TestHighlightContentParagraphBreak(t *testing.T) {
	t.Parallel()

	content := "first paragraph\n\nsecond paragraph"
	width := 40

	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)

	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 3, "paragraph break should produce empty line")
}

func TestHighlightContentHardWrap(t *testing.T) {
	t.Parallel()

	// 一个 word longer than width 是 cut mid-word 通过 screen 缓冲;
	// pieces must 是 joined without inserting space.
	content := strings.Repeat("a", 79) + "b"
	width := 80

	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)

	require.Equal(t, strings.Repeat("a", 79)+"b\n", result)
}

func TestHighlightContentMarkdownList(t *testing.T) {
	t.Parallel()

	// Render markdown list through glamour 所以 output matches what
	// the chat view actually produces: a long item word-wrapped onto a
	// continuation 行, followed 通过 another item.
	md := "- If the current row's content extends past sixty percent of the buffer width emit a space (space, wrap continuation)\n- Otherwise emit a newline (real newline, short lines like headings, list items, code)"
	sty := styles.CharmtonePantera()
	width := 100
	r, err := glamour.NewTermRenderer(glamour.WithStyles(sty.Markdown), glamour.WithWordWrap(width))
	require.NoError(t, err)
	content, err := r.Render(md)
	require.NoError(t, err)

	result := HighlightContent(content, uv.Rect(0, 0, width, lipgloss.Height(content)), 0, 0, -1, -1)

	// wrapped item must join 用 它的 continuation, 和 next item
	// must start 在 它的 own line.
	require.Contains(t, result, "(space, wrap continuation)\n", "wrapped continuation must join with a space, got:\n%s", result)
	require.Contains(t, result, "continuation)\n• Otherwise", "next list item must start on its own line, got:\n%s", result)
}

// TestHighlightContentRestoresCodespanBackticks guards copy side 的
// inline-code copy fix: markdown codespans render blank sentinel padding
// instead 的 their backticks ([styles.CodespanPadding]), 和 selection copy
// must turn 那 padding back into original backticks 所以 clipboard
// matches source 文本.
func TestHighlightContentRestoresCodespanBackticks(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	r, err := glamour.NewTermRenderer(glamour.WithStyles(sty.Markdown), glamour.WithWordWrap(80))
	require.NoError(t, err)
	rendered, err := r.Render(`message "this is ` + "`code`" + `" ok`)
	require.NoError(t, err)

	require.NotContains(t, ansi.Strip(rendered), "`", "display must not show backticks")

	result := HighlightContent(rendered, uv.Rect(0, 0, 80, lipgloss.Height(rendered)), 0, 0, -1, -1)
	require.Contains(t, result, `"this is `+"`code`"+`" ok`, "copy must restore the codespan backticks, got:\n%s", result)
}

// TestHighlightContentPreservesRealNonBreakingSpaces 是 regression 测试
// 为 sentinel collision: real no-break space 在 消息 文本
// (pasted 文本, LLM output, non-English typography) 是 不 codespan padding
// 和 must survive selection copy verbatim, never turn into backtick.
func TestHighlightContentPreservesRealNonBreakingSpaces(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	r, err := glamour.NewTermRenderer(glamour.WithStyles(sty.Markdown), glamour.WithWordWrap(80))
	require.NoError(t, err)
	rendered, err := r.Render("question\u00a0: is `code` ok")
	require.NoError(t, err)

	result := HighlightContent(rendered, uv.Rect(0, 0, 80, lipgloss.Height(rendered)), 0, 0, -1, -1)
	require.Contains(t, result, "question\u00a0: is `code` ok",
		"copy must keep the real no-break space and restore only the codespan backticks, got:\n%q", result)

	// Plain (unstyled) content 用 real no-break space must also pass
	// through 原样.
	result = HighlightContent("foo\u00a0bar", uv.Rect(0, 0, 40, 1), 0, 0, -1, -1)
	require.Equal(t, "foo\u00a0bar\n", result)
}

// TestHighlightContentRestoresWrappedCodespanBackticks 是 narrow-width
// variant: 当 codespan pill wraps onto 它的 own 行, copy must still
// reassemble the pill into a single backticked span.
func TestHighlightContentRestoresWrappedCodespanBackticks(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	width := 16
	r, err := glamour.NewTermRenderer(glamour.WithStyles(sty.Markdown), glamour.WithWordWrap(width))
	require.NoError(t, err)
	rendered, err := r.Render("some text `codespan` and more words to force wrapping across rows")
	require.NoError(t, err)

	result := HighlightContent(rendered, uv.Rect(0, 0, width, lipgloss.Height(rendered)), 0, 0, -1, -1)
	require.Contains(t, result, "`codespan`", "wrapped pill must reassemble in copy, got:\n%s", result)
}
