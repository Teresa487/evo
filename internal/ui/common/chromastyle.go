package common

import (
	"image/color"
	"sync"

	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// Building chroma style 从 theme (chroma.MustNewStyle) parses every
// token rule 和 resolves inheritance, which 是 expensive. Syntax
// highlighting 和 diff formatting both did it 在 every 调用, 所以 chat
// full 的 code views 和 diffs rebuilt same style hundreds 的 times
// per frame 在 resize. 结果 depends 只 在 theme (和, 为
// highlighting, optional background override), 所以 we memoize it.
var (
	chromaStyleMu   sync.Mutex
	chromaStyleFor  *styles.Styles
	chromaStyleBase *chroma.Style
	chromaStyleByBg map[[3]uint8]*chroma.Style
)

// InvalidateChromaStyleCache drops memoized chroma style 所以 next
// 调用 到 ChromaStyle rebuilds it 从 current theme. 调用 it 当
// 活跃 theme 是 mutated 在 place ( *styles.Styles 指针 是 reused),
// which 指针-keyed memoization cannot otherwise detect.
func InvalidateChromaStyleCache() {
	chromaStyleMu.Lock()
	defer chromaStyleMu.Unlock()
	chromaStyleFor = nil
	chromaStyleBase = nil
	chromaStyleByBg = nil
}

// InvalidateStyleCaches drops every style-derived 缓存 那 指针
// identity 的 活跃 *styles.Styles cannot invalidate 在 它的 own 当
// theme 是 swapped 在 place. 调用 it after applying new theme.
func InvalidateStyleCaches() {
	InvalidateMarkdownRendererCache()
	InvalidateChromaStyleCache()
}

// ChromaStyle returns chroma style 为 given theme, memoized. When
// bg 是 non-nil style's background 是 overridden 用 it (also
// memoized per color). 缓存 resets whenever 活跃 theme changes.
func ChromaStyle(st *styles.Styles, bg color.Color) *chroma.Style {
	chromaStyleMu.Lock()
	defer chromaStyleMu.Unlock()

	if chromaStyleFor != st {
		chromaStyleFor = st
		chromaStyleBase = nil
		chromaStyleByBg = nil
	}
	if chromaStyleBase == nil {
		chromaStyleBase = chroma.MustNewStyle("crush", st.ChromaTheme())
	}
	if bg == nil {
		return chromaStyleBase
	}

	r, g, b, _ := bg.RGBA()
	key := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
	if s, ok := chromaStyleByBg[key]; ok {
		return s
	}

	built, err := chromaStyleBase.Builder().Transform(
		func(t chroma.StyleEntry) chroma.StyleEntry {
			t.Background = chroma.NewColour(key[0], key[1], key[2])
			return t
		},
	).Build()
	if err != nil {
		built = chromastyles.Fallback
	}
	if chromaStyleByBg == nil {
		chromaStyleByBg = map[[3]uint8]*chroma.Style{}
	}
	chromaStyleByBg[key] = built
	return built
}
