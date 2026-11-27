package styles

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/stretchr/testify/require"
)

// TestCodespanPaddingIsOneCellWide guards flicker regression 从
// emoji-presentation variation selector: 当 sentinel ended 在 U+FE0F,
// ansi.StringWidth measured it 作为 two cells wide while terminals rendered it
// 作为 one, 和 那 mismatch made frame differ repaint line 在 every
// frame. sentinel 必须 measure exactly one cell under every width
// authority 在 play (glamour wraps 用 x/ansi, cells 是 存储 用
// uniseg).
func TestCodespanPaddingIsOneCellWide(t *testing.T) {
	t.Parallel()

	require.Equal(t, 1, ansi.StringWidth(CodespanPadding),
		"codespan padding must budget one cell, or word wrap disagrees with the terminal")
	require.Equal(t, 1, uniseg.StringWidth(CodespanPadding),
		"codespan padding must occupy one screen cell")

	state := -1
	cluster, _, w, _ := uniseg.FirstGraphemeClusterInString(CodespanPadding, state)
	require.Equal(t, CodespanPadding, cluster, "padding must be a single grapheme cluster")
	require.Equal(t, 1, w)

	require.NotEqual(t, "\u00a0", CodespanPadding,
		"sentinel must stay distinguishable from a real no-break space")
}
