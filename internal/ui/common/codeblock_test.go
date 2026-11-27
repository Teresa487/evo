package common

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"charm.land/glamour/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

const goCodeBlock = "```go\nfunc main() {}\n```\n"

// keywordStyles returns 默认 theme 用 given code keyword color.
func keywordStyles(t *testing.T, color string) styles.Styles {
	t.Helper()
	s, err := styles.HostThemeStyles("", nil, map[string]string{"keyword": color})
	require.NoError(t, err)
	return s
}

// sgr returns truecolor foreground 序号 为 "#rrggbb".
func sgr(color string) string {
	var r, g, b int
	_, _ = fmt.Sscanf(color, "#%02x%02x%02x", &r, &g, &b)
	return fmt.Sprintf("38;2;%d;%d;%dm", r, g, b)
}

// renderCode renders Go code block 用 fresh renderer 为 s.
func renderCode(t *testing.T, s *styles.Styles) string {
	t.Helper()
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(codeBlockConfig(s.Markdown)),
		glamour.WithWordWrap(80),
		glamour.WithChromaFormatter(formatterName),
	)
	require.NoError(t, err)
	out, err := r.Render(goCodeBlock)
	require.NoError(t, err)
	return out
}

// TestCodeBlockColorsFollowTheStyles checks 那 every renderer highlights
// fenced code 用 它的 own theme's colors. Glamour alone keeps colors
// 的 first theme it ever rendered.
func TestCodeBlockColorsFollowTheStyles(t *testing.T) {
	t.Parallel()

	red, green := keywordStyles(t, "#ff0001"), keywordStyles(t, "#00ff02")
	require.Contains(t, renderCode(t, &red), sgr("#ff0001"))
	out := renderCode(t, &green)
	require.Contains(t, out, sgr("#00ff02"))
	require.NotContains(t, out, sgr("#ff0001"))
	// first 配置 keeps 它的 colors too.
	require.Contains(t, renderCode(t, &red), sgr("#ff0001"))
}

// TestMarkdownRendererFollowsThemeSwitch mirrors 在-place theme switch:
// shared *Styles 是 overwritten 和 caches 是 invalidated.
func TestMarkdownRendererFollowsThemeSwitch(t *testing.T) {
	// Not parallel: renderer caches 是 process-wide.
	s := keywordStyles(t, "#ff0003")
	InvalidateStyleCaches()
	t.Cleanup(InvalidateStyleCaches)

	render := func() string {
		r := MarkdownRenderer(&s, 60)
		mu := LockMarkdownRenderer(r)
		mu.Lock()
		defer mu.Unlock()
		out, err := r.Render(goCodeBlock)
		require.NoError(t, err)
		return out
	}
	require.Contains(t, render(), sgr("#ff0003"))

	s = keywordStyles(t, "#00ff04")
	InvalidateStyleCaches()
	out := render()
	require.Contains(t, out, sgr("#00ff04"))
	require.NotContains(t, out, sgr("#ff0003"))
}

// TestCodeBlockSlotsAreReused checks 那 more distinct themes than there
// 是 placeholders still render 用 right colors.
func TestCodeBlockSlotsAreReused(t *testing.T) {
	// Not parallel: it cycles through every placeholder, which would hand
	// concurrent 测试's placeholder 到 another theme mid-render.
	for i := range codeBlockSlots + 3 {
		color := fmt.Sprintf("#%02x00%02x", i+1, 0xaa)
		s := keywordStyles(t, color)
		require.Contains(t, renderCode(t, &s), sgr(color), "theme %d", i)
	}
}

// TestCodeBlockRendersConcurrently renders code blocks 为 different themes
// 从 several goroutines while new themes keep being registered. 运行 用
// -race: changing chroma's global registry 在 runtime would 是 reported.
func TestCodeBlockRendersConcurrently(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	// Fewer distinct themes than placeholders, 所以 none 是 reused while
	// goroutine still renders 用 it.
	for g := range 4 {
		wg.Go(func() {
			for i := range 4 {
				color := fmt.Sprintf("#%02x%02x01", g+1, i+1)
				s := keywordStyles(t, color)
				out := renderCode(t, &s)
				if !strings.Contains(out, sgr(color)) {
					t.Errorf("theme %d/%d rendered without its keyword color", g, i)
				}
			}
		})
	}
	wg.Wait()
}

// TestHostSyntaxReachesChromaStyle checks 那 host theme's syntax colors
// land 在 memoized style 那 文件 views 和 diffs highlight 用.
func TestHostSyntaxReachesChromaStyle(t *testing.T) {
	t.Parallel()

	s := keywordStyles(t, "#123456")
	require.Equal(t, "#123456", ChromaStyle(&s, nil).Get(chroma.Keyword).Colour.String())
	out, err := SyntaxHighlight(&s, "func main() {}", "main.go", nil)
	require.NoError(t, err)
	require.Contains(t, out, "18;52;86")
}
