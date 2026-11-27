package completions

import (
	"slices"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/sahilm/fuzzy"
)

// FileCompletionValue represents 文件 路径 completion 值.
type FileCompletionValue struct {
	Path string
}

// ResourceCompletionValue represents MCP resource completion 值.
type ResourceCompletionValue struct {
	MCPName  string
	URI      string
	Title    string
	MIMEType string
}

// CompletionItem represents item 在 completions list.
type CompletionItem struct {
	*list.Versioned

	text    string
	value   any
	match   fuzzy.Match
	focused bool
	cache   map[int]string

	// Styles
	normalStyle  lipgloss.Style
	focusedStyle lipgloss.Style
	matchStyle   lipgloss.Style
}

// NewCompletionItem creates a new completion item.
func NewCompletionItem(text string, value any, normalStyle, focusedStyle, matchStyle lipgloss.Style) *CompletionItem {
	return &CompletionItem{
		Versioned:    list.NewVersioned(),
		text:         text,
		value:        value,
		normalStyle:  normalStyle,
		focusedStyle: focusedStyle,
		matchStyle:   matchStyle,
	}
}

// 文本 returns display 文本 的 item.
func (c *CompletionItem) Text() string {
	return c.text
}

// 值 returns 值 的 item.
func (c *CompletionItem) Value() any {
	return c.value
}

// Filter implements [list.FilterableItem].
func (c *CompletionItem) Filter() string {
	return c.text
}

// SetMatch implements [list.MatchSettable].
func (c *CompletionItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(c.match, m) {
		return
	}
	c.cache = nil
	c.match = m
	c.Bump()
}

// sameFuzzyMatch reports whether two fuzzy.Match values 是
// observably equal. Because Match contains a slice (MatchedIndexes)
// it 是 不 directly comparable 用 ==; we compare scalar
// fields 和 then walk indexes. SetMatch uses 这 到 skip
// gratuitous version bumps 当 same match 是 reapplied.
func sameFuzzyMatch(a, b fuzzy.Match) bool {
	return a.Str == b.Str &&
		a.Index == b.Index &&
		a.Score == b.Score &&
		slices.Equal(a.MatchedIndexes, b.MatchedIndexes)
}

// SetFocused implements [list.Focusable].
func (c *CompletionItem) SetFocused(focused bool) {
	if c.focused == focused {
		return
	}
	c.cache = nil
	c.focused = focused
	c.Bump()
}

// setStyles replaces item's styles 和 drops 它的 cached renders.
func (c *CompletionItem) setStyles(normalStyle, focusedStyle, matchStyle lipgloss.Style) {
	c.normalStyle = normalStyle
	c.focusedStyle = focusedStyle
	c.matchStyle = matchStyle
	c.cache = nil
	c.Bump()
}

// Render implements [list.Item].
func (c *CompletionItem) Render(width int) string {
	return renderItem(
		c.normalStyle,
		c.focusedStyle,
		c.matchStyle,
		c.text,
		c.focused,
		width,
		c.cache,
		&c.match,
	)
}

func renderItem(
	normalStyle, focusedStyle, matchStyle lipgloss.Style,
	text string,
	focused bool,
	width int,
	cache map[int]string,
	match *fuzzy.Match,
) string {
	if cache == nil {
		cache = make(map[int]string)
	}

	cached, ok := cache[width]
	if ok {
		return cached
	}

	innerWidth := width - 2 // Account for padding
	// Truncate 如果 needed.
	if ansi.StringWidth(text) > innerWidth {
		text = ansi.Truncate(text, innerWidth, "…")
	}

	// Select base style. match highlight inherits 行 background;
	// 在 focused 行 it must also inherit focused foreground.
	// Otherwise theme whose foreground matches focused 后台
	// (e.g. monochrome green palette) renders matched 文本 invisibly.
	style := normalStyle
	matchStyle = matchStyle.Background(style.GetBackground())
	if focused {
		style = focusedStyle
		matchStyle = matchStyle.
			Background(style.GetBackground()).
			Foreground(style.GetForeground())
	}

	// Render full-width 文本 用 background.
	content := style.Padding(0, 1).Width(width).Render(text)

	// Apply match highlighting using StyleRanges.
	if len(match.MatchedIndexes) > 0 {
		var ranges []lipgloss.Range
		for _, rng := range matchedRanges(match.MatchedIndexes) {
			start, stop := bytePosToVisibleCharPos(text, rng)
			// Offset 通过 1 为 padding space.
			ranges = append(ranges, lipgloss.NewRange(start+1, stop+2, matchStyle))
		}
		content = lipgloss.StyleRanges(content, ranges...)
	}

	cache[width] = content
	return content
}

// matchedRanges converts list 的 match indexes into contiguous ranges.
func matchedRanges(in []int) [][2]int {
	if len(in) == 0 {
		return [][2]int{}
	}
	current := [2]int{in[0], in[0]}
	if len(in) == 1 {
		return [][2]int{current}
	}
	var out [][2]int
	for i := 1; i < len(in); i++ {
		if in[i] == current[1]+1 {
			current[1] = in[i]
		} else {
			out = append(out, current)
			current = [2]int{in[i], in[i]}
		}
	}
	out = append(out, current)
	return out
}

// bytePosToVisibleCharPos converts byte positions 到 visible character positions.
func bytePosToVisibleCharPos(str string, rng [2]int) (int, int) {
	bytePos, byteStart, byteStop := 0, rng[0], rng[1]
	pos, start, stop := 0, 0, 0
	gr := uniseg.NewGraphemes(str)
	for byteStart > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	start = pos
	for byteStop > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	stop = pos
	return start, stop
}

// Ensure CompletionItem implements the required interfaces.
var (
	_ list.Item           = (*CompletionItem)(nil)
	_ list.FilterableItem = (*CompletionItem)(nil)
	_ list.MatchSettable  = (*CompletionItem)(nil)
	_ list.Focusable      = (*CompletionItem)(nil)
)
