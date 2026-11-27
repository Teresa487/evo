package dialog

import (
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

func newTestSingleChoice(t *testing.T) *SingleChoice {
	t.Helper()
	s := styles.CharmtonePantera()
	req := question.Question{
		ID:   "q1",
		Type: question.TypeSingleChoice,
		Text: "Pick one",
		Choices: []question.Choice{
			{ID: "a", Label: "Alpha"},
			{ID: "b", Label: "Beta"},
			{ID: "c", Label: "Gamma"},
		},
	}
	return NewSingleChoice(&s, req)
}

func newTestMultiChoice(t *testing.T) *MultiChoice {
	t.Helper()
	s := styles.CharmtonePantera()
	req := question.Question{
		ID:   "q1",
		Type: question.TypeMultiChoice,
		Text: "Pick some",
		Choices: []question.Choice{
			{ID: "a", Label: "Alpha"},
			{ID: "b", Label: "Beta"},
			{ID: "c", Label: "Gamma"},
		},
	}
	return NewMultiChoice(&s, req)
}

// TestMoveDownFromHoverIsSymmetric verifies 那 first arrow
// press after hovering moves one step 在 arrow's direction
// relative 到 hovered item: down lands below it, up lands above
// it. Previously moveDown landed 在 hovered item itself, making
// the first down-press a dead keystroke.
func TestMoveDownFromHoverIsSymmetric(t *testing.T) {
	t.Parallel()

	t.Run("down moves below hovered", func(t *testing.T) {
		t.Parallel()
		d := newTestSingleChoice(t)
		d.mouseActive = true
		d.hoveredChoice = 1 // hovering Beta
		d.moveDown()
		require.Equal(t, 2, d.cursorIdx, "down from hovered index 1 should land on 2")
		require.False(t, d.mouseActive, "keyboard nav must exit hover mode")
	})

	t.Run("up moves above hovered", func(t *testing.T) {
		t.Parallel()
		d := newTestSingleChoice(t)
		d.mouseActive = true
		d.hoveredChoice = 1 // hovering Beta
		d.moveUp()
		require.Equal(t, 0, d.cursorIdx, "up from hovered index 1 should land on 0")
		require.False(t, d.mouseActive, "keyboard nav must exit hover mode")
	})

	t.Run("down with no hovered choice lands on first", func(t *testing.T) {
		t.Parallel()
		d := newTestSingleChoice(t)
		d.mouseActive = true
		d.hoveredChoice = -1
		d.moveDown()
		require.Equal(t, 0, d.cursorIdx)
	})
}

// TestHoverStaysLiveWhileFillInFocused verifies 那 hover feedback
// keeps tracking mouse even while fill-在 textarea 是
// focused, 所以 用户 can still see what they're pointing 在.
func TestHoverStaysLiveWhileFillInFocused(t *testing.T) {
	t.Parallel()

	d := newTestSingleChoice(t)
	// Navigate 到 fill-在 item 和 focus it, mimicking
	// keyboard-driven 路径 into fill-在 字段.
	d.cursorIdx = len(d.Request.Choices) // fill-in index
	require.True(t, d.isFillIn())
	d.fillIn.Focus()
	// Draw once 所以 hit-测试 compositor 为 choice rows 是
	// built; setHover resolves the hovered choice through it.
	scr := uv.NewScreenBuffer(40, 20)
	d.Draw(scr, image.Rect(0, 0, 40, 20))

	d.setHover(2, 6) // somewhere over a choice row

	require.True(t, d.mouseActive, "hover should stay live while editing the fill-in")
	require.GreaterOrEqual(t, d.hoveredChoice, 0, "the hovered choice should be resolved")
}

// TestFillInArrowIgnoresHover verifies 那 arrow keys pressed while
// fill-在 是 focused move relative 到 fill-在, 不 到
// choice mouse happens 到 是 hovering over.
func TestFillInArrowIgnoresHover(t *testing.T) {
	t.Parallel()

	d := newTestSingleChoice(t)
	d.cursorIdx = len(d.Request.Choices) // on the fill-in
	d.fillIn.Focus()
	d.mouseActive = true
	d.hoveredChoice = 0 // mouse is hovering the first choice

	// Up 从 fill-在 should land 在 last real choice, 不
	// adopt the hovered index 0.
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})

	require.Equal(t, len(d.Request.Choices)-1, d.cursorIdx,
		"arrow up from the fill-in should move to the last choice")
	require.False(t, d.mouseActive)
}

// TestHoverActiveWhenFillInBlurred verifies hover still works once
// cursor 是 在 fill-在 但 textarea 是 不 focused
// (e.g. after pressing escape), 所以 leaving editor restores
// normal mouse behavior.
func TestHoverActiveWhenFillInBlurred(t *testing.T) {
	t.Parallel()

	d := newTestSingleChoice(t)
	d.cursorIdx = 0
	d.fillIn.Blur()

	d.setHover(3, 3)

	require.True(t, d.mouseActive, "hover should activate when no textarea is focused")
}

// enterKey 是 key press used 到 trigger selection 在 tests.
var (
	enterKey = tea.KeyPressMsg{Code: tea.KeyEnter}
	spaceKey = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
)

// TestSingleChoiceEnterAdoptsHover verifies 那 pressing Enter while
// hovering choice selects hovered item, 不 stale cursor.
func TestSingleChoiceEnterAdoptsHover(t *testing.T) {
	t.Parallel()

	d := newTestSingleChoice(t)
	d.cursorIdx = 0 // stale keyboard cursor on Alpha
	d.mouseActive = true
	d.hoveredChoice = 2 // hovering Gamma

	done, _ := d.HandleKey(enterKey)

	require.True(t, done, "Enter should submit the selection")
	require.Equal(t, []string{"c"}, d.Response().SelectedIDs,
		"Enter while hovering should select the hovered choice")
}

// TestMultiChoiceToggleAdoptsHover verifies 那 pressing space while
// hovering choice toggles hovered item, 不 stale cursor.
func TestMultiChoiceToggleAdoptsHover(t *testing.T) {
	t.Parallel()

	d := newTestMultiChoice(t)
	d.cursorIdx = 0 // stale keyboard cursor on Alpha
	d.mouseActive = true
	d.hoveredChoice = 1 // hovering Beta

	d.HandleKey(spaceKey)

	require.Equal(t, []string{"b"}, d.Response().SelectedIDs,
		"space while hovering should toggle the hovered choice")
	require.False(t, d.mouseActive, "toggling must exit hover mode")
}

// placeholderItem 是 choiceItemRenderer stub 为 buildLines tests
// where 只 layout (不 label content) matters.
func placeholderItem(int, question.Choice, bool, int) string { return "x" }

// fillInRowText builds choice list 和 returns first fill-在
// 行's rendered 文本.
func fillInRowText(t *testing.T, d *SingleChoice, prefix string) string {
	t.Helper()
	lines := d.buildLines(40, prefix, placeholderItem)
	require.GreaterOrEqual(t, d.fillInTop, 0)
	require.Less(t, d.fillInTop, len(lines))
	return lines[d.fillInTop].text
}

// TestFillInPrefixIsThreadedThrough verifies 提示词 string
// component supplies actually reaches fill-在 行. Previously
// buildLines ignored it 和 recomputed 它的 own, 所以 single-select's
// pink 提示词 never rendered.
func TestFillInPrefixIsThreadedThrough(t *testing.T) {
	t.Parallel()

	d := newTestSingleChoice(t)
	d.cursorIdx = len(d.Request.Choices) // on the fill-in
	got := fillInRowText(t, d, "MARKER> ")
	require.Contains(t, got, "MARKER>", "buildLines must use the supplied fill-in prompt")
}

// TestFillInBarFollowsSelection verifies fill-在 gutter bar
// tracks 活跃/hover state like choice 行, rather than
// staying lit whenever fill-在 holds 文本.
func TestFillInBarFollowsSelection(t *testing.T) {
	t.Parallel()

	const bar = "┃"
	fillInIdx := 3 // len(choices)

	t.Run("lit when selected via keyboard", func(t *testing.T) {
		t.Parallel()
		d := newTestSingleChoice(t)
		d.cursorIdx = fillInIdx
		d.mouseActive = false
		require.Contains(t, fillInRowText(t, d, "> "), bar)
	})

	t.Run("dark when it holds text but is not selected", func(t *testing.T) {
		t.Parallel()
		d := newTestSingleChoice(t)
		d.fillIn.SetValue("custom answer")
		d.cursorIdx = 0 // a choice, not the fill-in
		d.mouseActive = false
		require.NotContains(t, fillInRowText(t, d, "> "), bar,
			"the bar should not stay lit just because the fill-in has text")
	})

	t.Run("dark when selected but hovering elsewhere", func(t *testing.T) {
		t.Parallel()
		d := newTestSingleChoice(t)
		d.cursorIdx = fillInIdx
		d.mouseActive = true
		d.hoveredChoice = 0 // mouse over the first choice
		require.NotContains(t, fillInRowText(t, d, "> "), bar,
			"in hover mode the bar belongs to the hovered choice")
	})
}

// TestSuppressedScrollStillClampsBounds verifies 那 click 那 skips
// snap 到 cursor still keeps offset inside content, since
// click can shrink it ( blurred fill-在 collapses 到 one 行).
func TestSuppressedScrollStillClampsBounds(t *testing.T) {
	t.Parallel()

	d := newTestMultiChoice(t)
	d.scrollOffset = 6
	d.suppressScroll = true
	d.clampScroll(make([]contentLine, 12), 8)

	require.Equal(t, 4, d.scrollOffset)
	require.False(t, d.suppressScroll)
}
