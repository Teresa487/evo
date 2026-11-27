package dialog

import (
	"fmt"
	"image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

func newTestQuit() *Quit {
	sty := styles.CharmtonePantera()
	return NewQuit(&common.Common{Styles: &sty}, 0)
}

func drawQuit(t *testing.T, q *Quit) uv.ScreenBuffer {
	t.Helper()
	scr := uv.NewScreenBuffer(100, 40)
	q.Draw(scr, image.Rect(0, 0, 100, 40))
	return scr
}

// quitButtonPoint returns point 在 center 的 given button layer.
func quitButtonPoint(t *testing.T, q *Quit, index int) (int, int) {
	t.Helper()
	require.NotNil(t, q.compositor, "expected button layers to be drawn")
	layer := q.compositor.GetLayer(fmt.Sprintf("btn_%d", index))
	require.NotNil(t, layer)
	return layer.GetX() + layer.Width()/2, layer.GetY()
}

func TestQuitMousePaintsButtonsAtHitRegions(t *testing.T) {
	t.Parallel()

	q := newTestQuit()
	scr := drawQuit(t, q)

	// layer bounds 是 where buttons 是 actually painted, 所以
	// labels must 是 visible inside them.
	for i, label := range []string{"Yep!", "Nope"} {
		layer := q.compositor.GetLayer(fmt.Sprintf("btn_%d", i))
		var got string
		for x := layer.GetX(); x < layer.GetX()+layer.Width(); x++ {
			got += scr.CellAt(x, layer.GetY()).Content
		}
		require.Contains(t, got, label)
	}
}

func TestQuitMouseClickYepSelectsBeforeQuitting(t *testing.T) {
	t.Parallel()

	q := newTestQuit()
	drawQuit(t, q)
	require.True(t, q.selectedNo, "Nope must be selected by default")

	x, y := quitButtonPoint(t, q, 0)
	click := tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft})

	// First click selects Yep! without quitting.
	require.Nil(t, q.HandleMsg(click))
	require.False(t, q.selectedNo)

	// Clicking the now-selected button quits.
	action := q.HandleMsg(click)
	require.IsType(t, ActionQuit{}, action)
}

func TestQuitMouseClickNope(t *testing.T) {
	t.Parallel()

	// Nope 是 selected 通过 默认, 所以 single click closes dialog.
	q := newTestQuit()
	drawQuit(t, q)
	x, y := quitButtonPoint(t, q, 1)
	click := tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft})
	require.IsType(t, ActionClose{}, q.HandleMsg(click))

	// When Yep! 是 selected, clicking Nope selects it first, 和 second
	// click closes the dialog.
	q = newTestQuit()
	drawQuit(t, q)
	q.selectedNo = false
	require.Nil(t, q.HandleMsg(click))
	require.True(t, q.selectedNo)
	require.IsType(t, ActionClose{}, q.HandleMsg(click))
}

func TestQuitMouseClickIgnoresNonButtonAndNonLeftClicks(t *testing.T) {
	t.Parallel()

	q := newTestQuit()
	drawQuit(t, q)

	require.Nil(t, q.HandleMsg(tea.MouseClickMsg(tea.Mouse{X: 0, Y: 0, Button: tea.MouseLeft})))
	require.True(t, q.selectedNo)

	x, y := quitButtonPoint(t, q, 0)
	require.Nil(t, q.HandleMsg(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseRight})))
	require.True(t, q.selectedNo)
}

// screenText flattens screen 缓冲 into newline-separated rows.
func screenText(scr uv.ScreenBuffer) string {
	bounds := scr.Bounds()
	rows := make([]string, bounds.Dy())
	for y := range bounds.Dy() {
		var b strings.Builder
		for x := range bounds.Dx() {
			if cell := scr.CellAt(x, y); cell != nil {
				b.WriteString(cell.Content)
			}
		}
		rows[y] = b.String()
	}
	return strings.Join(rows, "\n")
}

func TestQuitMouseButtonsRenderInlineWhenTheyDontFit(t *testing.T) {
	t.Parallel()

	q := newTestQuit()
	scr := uv.NewScreenBuffer(24, 20)
	q.Draw(scr, image.Rect(0, 0, 24, 20))

	// dialog does 不 fit, 所以 buttons fall back 到 inline
	// rendering: they stay visible, 但 是 不 clickable.
	require.Nil(t, q.compositor)
	text := screenText(scr)
	require.Contains(t, text, "Yep!")
	require.Contains(t, text, "Nope")
	require.Nil(t, q.HandleMsg(tea.MouseClickMsg(tea.Mouse{X: 1, Y: 1, Button: tea.MouseLeft})))
	require.True(t, q.selectedNo)
}

// TestQuitMouseButtonsAlignWithCenteredRow guards the layer positions: the
// buttons 是 centered 通过 lipgloss.JoinVertical, 和 hit layers must start
// exactly where 那 centering paints them. Being off 通过 one cell steals
// single space separating two buttons, 所以 they appear 到 touch.
func TestQuitMouseButtonsAlignWithCenteredRow(t *testing.T) {
	t.Parallel()

	q := newTestQuit()
	scr := drawQuit(t, q)

	layer := q.compositor.GetLayer("btn_0")
	require.NotNil(t, layer)

	// Find frame's left border 在 button 行, then content
	// origin just inside it.
	frame := q.com.Styles.Dialog.Quit.Frame
	borderX := -1
	for x := range 100 {
		if cell := scr.CellAt(x, layer.GetY()); cell != nil && cell.Content == "│" {
			borderX = x
			break
		}
	}
	require.NotEqual(t, -1, borderX, "expected a frame border on the button row")
	contentX := borderX + frame.GetBorderLeftSize() + frame.GetPaddingLeft()

	// Rebuild 行 way Draw does 到 learn where centering puts
	// it, then require layers 到 sit there.
	buttons := common.ButtonGroup(q.com.Styles, []common.ButtonOpts{
		{Text: "Yep!", Selected: true, Padding: 3},
		{Text: "Nope", Padding: 3},
	}, " ")
	joined := lipgloss.JoinVertical(
		lipgloss.Center,
		quitQuestion,
		"",
		buttons,
		"",
		q.com.Styles.Dialog.Quit.Hint.Render(quitHintLineOne),
		q.com.Styles.Dialog.Quit.Hint.Render(quitHintLineTwo),
	)
	wantX := contentX + strings.Index(strings.Split(joined, "\n")[2], buttons)

	require.Equal(t, wantX, layer.GetX())
}

func TestQuitMouseHoverHighlightsButton(t *testing.T) {
	t.Parallel()

	q := newTestQuit()
	drawQuit(t, q)

	x, y := quitButtonPoint(t, q, 0)
	require.Nil(t, q.HandleMsg(tea.MouseMotionMsg(tea.Mouse{X: x, Y: y})))
	drawQuit(t, q)

	layer := q.compositor.GetLayer("btn_0")
	hovered := common.Button(q.com.Styles, common.ButtonOpts{Text: "Yep!", Hovered: true, Padding: 3})
	require.Equal(t, hovered, layer.GetContent())

	// Moving away clears the highlight.
	require.Nil(t, q.HandleMsg(tea.MouseMotionMsg(tea.Mouse{X: 0, Y: 0})))
	drawQuit(t, q)
	layer = q.compositor.GetLayer("btn_0")
	plain := common.Button(q.com.Styles, common.ButtonOpts{Text: "Yep!", Padding: 3})
	require.Equal(t, plain, layer.GetContent())
}

func TestQuitMarksOverlayAsHoverable(t *testing.T) {
	t.Parallel()

	overlay := NewOverlay()
	require.False(t, overlay.HandlesHover(), "empty overlay must not request hover events")

	overlay.OpenDialog(newTestQuit())
	require.True(t, overlay.HandlesHover(), "quit dialog needs all-motion mouse reporting")
}

func TestQuitKeyboardUnchanged(t *testing.T) {
	t.Parallel()

	q := newTestQuit()

	// Space confirms 默认 selection: Nope closes dialog.
	require.IsType(t, ActionClose{}, q.HandleMsg(tea.KeyPressMsg{Code: ' ', Text: " "}))

	// Left/right switches the selection; enter confirms it.
	require.Nil(t, q.HandleMsg(tea.KeyPressMsg{Code: tea.KeyRight}))
	require.False(t, q.selectedNo)
	require.IsType(t, ActionQuit{}, q.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
}
