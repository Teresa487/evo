package chat

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// 一个 wrapped parameter keeps colour 的 token it was broken 在
// middle 的.
//
// There 是 two ways 到 get 这 wrong 和 they look different 在 screen.
// Wrapping first 和 styling after emits parameter colour 在 head 的
// every line, 所以 tail 的 string literal comes out drab. Styling first
// 和 wrapping after emits nothing there, 所以 it comes out 在 终态
// 默认 instead. Both 是 checked here: head 的 continuation line
// has 到 carry colour 那 was open 在 break.
func TestToolParamListKeepsHighlightingAcrossWraps(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	// Long enough 到 break 在 middle 的 first quoted 路径.
	cmd := `for p in ("providers/openrouter/language_model_hooks.go","openai"),("x/y.go","z")`
	highlighted, err := common.SyntaxHighlightLexerName(&sty, cmd, "bash", nil)
	require.NoError(t, err)

	const width = 60
	out := toolParamList(&sty, []string{highlighted}, width, &ToolRenderOpts{ExpandedContent: true})

	lines := strings.Split(out, "\n")
	require.Greater(t, len(lines), 1, "the command must actually wrap for this to test anything")

	for i, line := range lines[1:] {
		require.NotEmpty(t, line, "continuation line %d is empty", i+1)
		require.True(t, strings.HasPrefix(line, "\x1b["),
			"continuation line %d starts with no colour at all, so it renders in the "+
				"terminal default instead of the highlight that was open at the break: %q",
			i+1, line)

		// Past parameter style, line has 到 re-open colour 那
		// was running 当 wrap fell, rather than starting fresh token.
		rest := strings.TrimPrefix(line, sgrPrefix(t, sty.Tool.ParamMain.Render("x")))
		require.True(t, strings.HasPrefix(rest, "\x1b["),
			"continuation line %d carries only the parameter colour, so the rest of the "+
				"token it was broken inside comes out drab: %q", i+1, line)
	}
}

// sgrPrefix returns leading escape 序号 的 rendered string.
func sgrPrefix(t *testing.T, rendered string) string {
	t.Helper()
	if !strings.HasPrefix(rendered, "\x1b[") {
		return ""
	}
	end := strings.Index(rendered, "m")
	require.Greater(t, end, 0, "expected a terminated escape sequence")
	return rendered[:end+1]
}

// Truncation 是 other branch through same function, 和 truncated
// parameter still has 到 carry parameter style.
func TestToolParamListStylesTruncatedParams(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	long := strings.Repeat("abcdefghij", 20)

	out := toolParamList(&sty, []string{long}, 30, nil)
	require.Contains(t, out, "…", "an over-long parameter must be truncated")
	require.NotEqual(t, long, out, "the parameter must be styled, not passed through bare")
}

// 一个 wrapped header has 到 stay inside space it was given.
//
// Word wrapping keeps space it broke 在, 所以 word ending exactly 在
// edge pushes 它的 line one column past width. 一个 header 那 overruns
// pane 通过 column 是 wrapped again 通过 终态, which splits it across
// two rows 和 leaves stray line 在 transcript.
func TestToolHeaderStaysWithinWidth(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	opts := &ToolRenderOpts{ExpandedContent: true}
	commands := []string{
		"go test ./internal/ui/chat -run TestToolParamList -v && echo done",
		"git commit -m aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj",
	}

	for _, cmd := range commands {
		for width := 30; width <= 100; width++ {
			out := toolHeader(&sty, ToolStatusSuccess, "Bash", width, opts, cmd)
			for i, line := range strings.Split(out, "\n") {
				require.LessOrEqual(t, lipgloss.Width(line), width,
					"line %d of %q at width %d overruns the pane: %q", i, cmd, width, line)
			}
		}
	}
}
