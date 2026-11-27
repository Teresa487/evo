package attachments

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newTestRenderer() *Renderer {
	sty := styles.CharmtonePantera()
	return NewRenderer(sty.Attachments)
}

func TestRender_IncludesRemoveButton(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "test.txt"},
	}
	out := r.Render(atts, false, true, 80)
	require.Contains(t, out, styles.RemoveIcon)
}

func TestRender_DeletingModeNoRemoveButton(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "test.txt"},
	}
	out := r.Render(atts, true, true, 80)
	require.NotContains(t, out, styles.RemoveIcon)
}

func TestRender_ShowRemoveFalseOmitsRemoveButton(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "no-change.png"},
	}
	out := r.Render(atts, false, false, 80)
	require.NotContains(t, out, styles.RemoveIcon,
		"posted-message attachments must not show a remove button")
	require.Empty(t, r.bounds,
		"no remove bounds should be recorded when the button is hidden")
	require.Equal(t, -1, r.HitTestRemove(atts, 0))
}

func TestRender_ShowRemoveFalseKeepsGapBetweenChips(t *testing.T) {
	t.Parallel()

	// Regression 为 #134 + #135 interaction: #134 moved trailing
	// margin onto remove button, 和 #135 hides 那 button 在 posted
	// messages. Together, posted messages 用 multiple attachments lost
	// margin 那 separated adjacent chips, 所以 their backgrounds touched.
	// filename must carry margin 当 remove button 是 hidden.
	//
	// White-box width check: visible width 的 two chips without any
	// separator 是 icon+filename per chip. With fix each posted chip adds
	// 1-column trailing margin, 所以 rendered 行 是 exactly two columns
	// wider. Stripping ANSI can't detect 这 ( margin space 和
	// background-colored padding space 是 both just spaces), 所以 we measure
	// width instead.
	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "alpha.txt"},
		{FileName: "beta.txt"},
	}
	bare := lipgloss.Width(r.styles.Text.String()+r.styles.Normal.Render("alpha.txt")) +
		lipgloss.Width(r.styles.Text.String()+r.styles.Normal.Render("beta.txt"))

	got := lipgloss.Width(r.Render(atts, false, false, 200))
	require.Equal(t, bare+2, got,
		"each posted chip must carry a 1-col trailing margin so adjacent chip backgrounds don't touch")
}

func TestRender_DeletingModeKeepsChipLayout(t *testing.T) {
	t.Parallel()

	// Regression 为 review feedback 在 #3338: entering 删除-mode used
	// 到 replace leading icon 用 numeral 和 drop remove
	// button, shifting every chip. numeral 必须 instead take over
	// remove button's slot, leaving left side 的 chip 作为-是.
	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "main.go"},
		{FileName: "models.go"},
	}
	idle := r.Render(atts, false, true, 200)
	deleting := r.Render(atts, true, true, 200)

	require.Equal(t, lipgloss.Width(idle), lipgloss.Width(deleting),
		"entering delete-mode must not shift the chips")
	require.Contains(t, deleting, styles.TextIcon,
		"delete-mode must keep the chip's icon")
	require.Contains(t, deleting, "0")
	require.Contains(t, deleting, "1")
}

func TestRender_RemoveButtonHasRightPadding(t *testing.T) {
	t.Parallel()

	// Regression 为 review feedback 在 #3338: ✕ must 不 sit 刷盘
	// against right edge 的 它的 colored box. cell 到 right 的
	// glyph has 到 是 padding — 片段 的 button's background — rather than
	// transparent margin, 所以 glyph has breathing room 在 它的 right.
	//
	// 一个 plain-width 或 ANSI-stripped check can't catch 这: margin space
	// 和 background-colored padding space 是 both one blank column. So we
	// inspect per-cell background 和 assert button's background
	// extends one cell past the ✕.
	r := newTestRenderer()
	atts := []message.Attachment{{FileName: "main.go"}}
	out := r.Render(atts, false, true, 200)

	cells := parseCells(out)
	xi := -1
	for i, c := range cells {
		if c.r == styles.RemoveIcon {
			xi = i
			break
		}
	}
	require.GreaterOrEqual(t, xi, 0, "rendered output must contain the ✕ glyph")
	require.NotEmpty(t, cells[xi].bg, "the ✕ cell must have the button's background")
	require.Less(t, xi+1, len(cells),
		"the ✕ must be followed by a trailing padding cell, not be the box's last cell")
	require.Equal(t, cells[xi].bg, cells[xi+1].bg,
		"the cell to the right of ✕ must share the button's background (padding), not be a transparent margin")
}

func TestRender_RemoveButtonKeepsGapBetweenChips(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "first.txt"},
		{FileName: "second.txt"},
	}
	cells := parseCells(r.Render(atts, false, true, 200))

	xi := -1
	for i, c := range cells {
		if c.r == styles.RemoveIcon {
			xi = i
			break
		}
	}
	require.GreaterOrEqual(t, xi, 0)
	require.Less(t, xi+2, len(cells))
	require.Empty(t, cells[xi+2].bg, "adjacent attachment chips must have a transparent one-cell gap")
}

// cell 是 one rendered 终态 cell: 它的 rune 和 truecolor background
// 在 effect ("r;g;b", 或 "" 为 none).
type cell struct {
	r  string
	bg string
}

// parseCells walks lipgloss-rendered string 和 returns 它的 visible cells
// 用 background color 活跃 在 each. 它 understands SGR sequences
// lipgloss emits (truecolor 48;2;r;g;b backgrounds, 38;2;r;g;b foregrounds,
// 和 resets); other escapes 是 ignored.
func parseCells(s string) []cell {
	var cells []cell
	bg := ""
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				bg = applyBG(s[i+2:j], bg)
				i = j + 1
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		cells = append(cells, cell{r: s[i : i+size], bg: bg})
		i += size
	}
	return cells
}

// applyBG updates current 后台 given one SGR parameter string.
func applyBG(params, cur string) string {
	if params == "" || params == "0" {
		return ""
	}
	toks := strings.Split(params, ";")
	for k := 0; k < len(toks); k++ {
		switch toks[k] {
		case "0":
			cur = ""
		case "38": // foreground — skip its arguments
			if k+1 < len(toks) && toks[k+1] == "2" {
				k += 4
			} else if k+1 < len(toks) && toks[k+1] == "5" {
				k += 2
			}
		case "48": // background
			if k+4 < len(toks) && toks[k+1] == "2" {
				cur = toks[k+2] + ";" + toks[k+3] + ";" + toks[k+4]
				k += 4
			} else if k+2 < len(toks) && toks[k+1] == "5" {
				cur = toks[k+2]
				k += 2
			}
		}
	}
	return cur
}

func TestRender_MultipleChipsEachHaveRemoveButton(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "a.txt"},
		{FileName: "b.txt"},
		{FileName: "c.txt"},
	}
	out := r.Render(atts, false, true, 120)
	// Count occurrences 的 remove glyph.
	count := 0
	for _, c := range out {
		if string(c) == styles.RemoveIcon {
			count++
		}
	}
	require.Equal(t, 3, count, "each chip should have a remove button")
}

func TestHitTestRemove_ClickOnFirstChipRemove(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "first.txt"},
		{FileName: "second.txt"},
	}
	_ = r.Render(atts, false, true, 120)

	// remove button 的 first chip should 是 hit-testable.
	// Click 在 various X positions 到 verify we hit right chip.
	idx := r.HitTestRemove(atts, 0)
	// At x=0 we're 在 icon, 不 remove button.
	require.Equal(t, -1, idx)
}

func TestHitTestRemove_ReturnsCorrectIndex(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "first.txt"},
		{FileName: "second.txt"},
	}
	_ = r.Render(atts, false, true, 120)

	// Each chip bounds 是 存储 after render. Verify there 是 two.
	require.Len(t, r.bounds, 2)

	// Click 在 first chip's remove button.
	b0 := r.bounds[0]
	idx := r.HitTestRemove(atts, b0.startX)
	require.Equal(t, 0, idx)

	// Click 在 second chip's remove button.
	b1 := r.bounds[1]
	idx = r.HitTestRemove(atts, b1.startX)
	require.Equal(t, 1, idx)
}

func TestHitTestRemove_TrailingMarginNotClickable(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "first.txt"},
		{FileName: "second.txt"},
	}
	_ = r.Render(atts, false, true, 120)

	// cell just past button's hit region belongs 到 next chip, 不
	// 到 这 button — click there must 不 remove 这 附件.
	b0 := r.bounds[0]
	require.Equal(t, -1, r.HitTestRemove(atts, b0.removeEnd))
}

func TestHitTestRemove_OutsideAnyRemoveReturnsMinusOne(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{
		{FileName: "test.txt"},
	}
	_ = r.Render(atts, false, true, 80)

	// Click far past the remove button.
	idx := r.HitTestRemove(atts, 999)
	require.Equal(t, -1, idx)
}

func TestHandleClick_RemovesAttachment(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	km := Keymap{}
	m := New(r, km)
	m.list = []message.Attachment{
		{FileName: "first.txt"},
		{FileName: "second.txt"},
	}

	// Render 所以 bounds 是 populated.
	_ = m.Render(120)

	// Click the first chip's remove button.
	b0 := r.bounds[0]
	handled := m.HandleClick(b0.startX)
	require.True(t, handled)
	require.Len(t, m.list, 1)
	require.Equal(t, "second.txt", m.list[0].FileName)
}

func TestHandleClick_ClickOutsideRemoveDoesNothing(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	km := Keymap{}
	m := New(r, km)
	m.list = []message.Attachment{
		{FileName: "test.txt"},
	}

	_ = m.Render(80)

	// Click 在 x=0 ( icon area, 不 remove button).
	handled := m.HandleClick(0)
	require.False(t, handled)
	require.Len(t, m.list, 1)
}

func TestHandleClick_DeletingModeIgnored(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	km := Keymap{}
	m := New(r, km)
	m.list = []message.Attachment{
		{FileName: "test.txt"},
	}
	m.deleting = true

	_ = m.Render(80)

	// bounds 是 empty 在 deleting mode since remove buttons aren't rendered.
	require.Empty(t, r.bounds)
	// Click anywhere — should 是 ignored.
	handled := m.HandleClick(10)
	require.False(t, handled)
}

func TestHandleClick_EmptyListIgnored(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	km := Keymap{}
	m := New(r, km)

	handled := m.HandleClick(5)
	require.False(t, handled)
}

// overflowFixture 是 set 的 attachments wide enough 到 overflow any
// reasonable editor, 所以 tests can exercise hint 路径.
func overflowFixture() []message.Attachment {
	atts := make([]message.Attachment, 8)
	for i := range atts {
		atts[i] = message.Attachment{FileName: fmt.Sprintf("file-%d.txt", i)}
	}
	return atts
}

// chipWidth 是 rendered width 的 lone editor chip 为 given
// filename. Below 这 行 cannot fit even one chip, 所以 it 是
// natural floor 为 any " 行 fits" assertion.
func chipWidth(r *Renderer, name string) int {
	return lipgloss.Width(r.Render([]message.Attachment{{FileName: name}}, false, true, 1000))
}

// hintCount returns number "N more…" hint claims, 或 0 当
// 行 carries no hint. trailing trim matters: hint padded out 到
// fixed width would otherwise 读取 作为 no hint 在 all.
func hintCount(out string) int {
	plain := strings.TrimRight(ansi.Strip(out), " ")
	if !strings.HasSuffix(plain, "more…") {
		return 0
	}
	fields := strings.Fields(plain)
	n, err := strconv.Atoi(fields[len(fields)-2])
	if err != nil {
		return 0
	}
	return n
}

func TestRender_SingleVisibleChipHasNoMoreHint(t *testing.T) {
	t.Parallel()

	// Regression: 行 used 到 budget every chip 在 maximum filename
	// width, 所以 在 narrow editor single short-named 附件 was
	// counted 作为 overflowing 它的 own 行. chip rendered fine 和 then
	// "1 more…" was tacked 在 beside it, promising 文件 那 was already
	// 在 screen. Widths 是 now measured 作为 chips 是 laid out.
	r := newTestRenderer()
	atts := []message.Attachment{{FileName: "paste_1.png"}}
	for width := range 121 {
		require.Zero(t, hintCount(r.Render(atts, false, true, width)),
			"a lone attachment must never claim there are others (width %d)", width)
	}
}

func TestRender_HintOnlyAppearsWhenSomethingIsHidden(t *testing.T) {
	t.Parallel()

	// stronger form 的 regression above: 为 any number 的
	// attachments 在 any width, hint may appear 只 当 chips were
	// actually left out, 和 it must name exactly how many. 一个 single-
	// 附件 测试 cannot catch hint 那 是 off 通过 one 为 n >= 2.
	r := newTestRenderer()
	all := overflowFixture()
	for n := 1; n <= len(all); n++ {
		for width := chipWidth(r, "file-0.txt"); width < 161; width++ {
			out := r.Render(all[:n], false, true, width)
			drawn := strings.Count(out, styles.TextIcon)
			require.Positive(t, drawn, "some chip must always be drawn (n=%d width=%d)", n, width)
			if got := hintCount(out); got != 0 {
				require.Equal(t, n-drawn, got,
					"hint must name exactly the chips left out (n=%d width=%d drew=%d)", n, width, drawn)
			} else {
				require.True(t, drawn == n || drawn == 1,
					"a row may go without the hint only when everything fit, or when "+
						"a single chip crowded the hint out (n=%d width=%d drew=%d)", n, width, drawn)
			}
		}
	}
}

func TestRender_DrawnChipsAreTheLeadingRun(t *testing.T) {
	t.Parallel()

	// Overflow drops chips off the end, never the middle. Click handling
	// indexes straight into 附件 slice 用 chip's position,
	// 所以 gap would remove wrong 文件.
	r := newTestRenderer()
	atts := overflowFixture()
	for width := chipWidth(r, "file-0.txt"); width < 161; width++ {
		out := ansi.Strip(r.Render(atts, false, true, width))
		drawn := strings.Count(out, styles.TextIcon)
		for i := range drawn {
			name := fmt.Sprintf("file-%d.txt", i)
			if drawn == 1 && width >= minFilename {
				require.True(t, showsName(out, name),
					"the only drawn chip must be chip %d (width %d)", i, width)
				continue
			}
			require.Contains(t, out, name,
				"chip %d must be present when %d chips were drawn (width %d)", i, drawn, width)
		}
		for i := drawn; i < len(atts); i++ {
			require.NotContains(t, out, fmt.Sprintf("file-%d.txt", i),
				"chip %d must be absent when only %d chips were drawn (width %d)", i, drawn, width)
		}
	}
}

// showsName reports whether stripped 行 shows name, either whole 或
// trimmed down 到 leading 运行 followed 通过 ellipsis.
func showsName(out, name string) bool {
	if strings.Contains(out, name) {
		return true
	}
	for n := len(name) - 1; n > 0; n-- {
		if strings.Contains(out, name[:n]+"…") {
			return true
		}
	}
	return false
}

func TestRender_MoreHintUsesTheThemeStyle(t *testing.T) {
	t.Parallel()

	// hint used 到 是 rendered 用 bare lipgloss.Style, 所以 it came
	// out 在 终态's 默认 foreground next 到 themed chips.
	// expectation 是 built 从 theme rather than 从
	// renderer, 所以 这 cannot pass 通过 agreeing 用 itself.
	r := newTestRenderer()
	atts := overflowFixture()
	out := r.Render(atts, false, true, 60)

	n := hintCount(out)
	require.Positive(t, n, "width 60 must overflow for this to mean anything")
	want := styles.CharmtonePantera().Attachments.More.Render(fmt.Sprintf("%d more…", n))
	require.Contains(t, want, "\x1b[", "the theme's hint style must actually set a color")
	require.Contains(t, out, want, "the row must render the hint through the theme style")
}

func TestRender_RowNeverSpills(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := overflowFixture()
	for width := range 161 {
		require.LessOrEqual(t, lipgloss.Width(r.Render(atts, false, true, width)), width,
			"the attachment row must not spill past the editor (width %d)", width)
	}
}

func TestRender_OverflowHoldsInEveryMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                 string
		deleting, showRemove bool
	}{
		{"editor", false, true},
		{"delete mode", true, true},
		{"posted message", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newTestRenderer()
			atts := overflowFixture()
			out := r.Render(atts, tc.deleting, tc.showRemove, 60)
			drawn := strings.Count(out, styles.TextIcon)
			require.Less(t, drawn, len(atts), "width 60 must overflow for this to mean anything")
			require.Equal(t, len(atts)-drawn, hintCount(out))
			require.LessOrEqual(t, lipgloss.Width(out), 60)
		})
	}
}

func TestHitTestRemove_OverflowBoundsOnlyCoverDrawnChips(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := overflowFixture()
	out := r.Render(atts, false, true, 60)
	drawn := strings.Count(out, styles.TextIcon)
	require.Less(t, drawn, len(atts), "width 60 must overflow for this to mean anything")
	require.Len(t, r.bounds, drawn, "only drawn chips may carry a clickable remove button")

	for x := range lipgloss.Width(out) {
		require.Less(t, r.HitTestRemove(atts, x), drawn,
			"column %d hit-tests to a chip that was never drawn", x)
	}
}

func TestRender_LoneChipTrimsItsNameToKeepTheHint(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := overflowFixture()
	full := chipWidth(r, "file-0.txt")

	hint := lipgloss.Width(styles.CharmtonePantera().Attachments.More.
		Render(fmt.Sprintf("%d more…", len(atts)-1)))
	width := full + hint - 1

	out := r.Render(atts, false, true, width)
	require.Equal(t, 1, strings.Count(out, styles.TextIcon),
		"only one chip should fit at width %d", width)
	require.Equal(t, len(atts)-1, hintCount(out),
		"the hint must survive and name every file left off (width %d)", width)
	require.NotContains(t, ansi.Strip(out), "file-0.txt",
		"the name must have been trimmed to make that room")
	require.LessOrEqual(t, lipgloss.Width(out), width,
		"the trimmed row must still fit the width it was given")
}

func TestRender_NonPositiveWidthRendersNothing(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{{FileName: "a.txt"}, {FileName: "b.txt"}}
	for _, width := range []int{-5, 0} {
		require.Empty(t, r.Render(atts, false, true, width))
		require.Empty(t, r.bounds)
	}
}

func TestRender_NoAttachmentsRendersNothing(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	require.Empty(t, r.Render(nil, false, true, 80))
	require.Empty(t, r.bounds)
}
