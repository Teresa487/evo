// quit.go —— 退出确认弹窗。

package dialog

import (
	"fmt"
	"image"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/crush/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
)

// QuitID 是 identifier 为 quit dialog.
const QuitID = "quit"

// Dialog content lines.
const (
	quitQuestion    = "Are you sure you want to quit?"
	quitHintLineOne = "To quit without confirmation"
	quitHintLineTwo = "press ctrl+c twice."
)

// Quit represents confirmation dialog 为 quitting application.
type Quit struct {
	com        *common.Common
	selectedNo bool // true if "No" button is selected
	compositor *lipgloss.Compositor
	hoverX     int
	hoverY     int
	// runningSubAgents 是 how many detached subagents 是 still working.
	// They die 用 process, 所以 quitting throws their work away.
	runningSubAgents int
	keyMap           struct {
		LeftRight,
		EnterSpace,
		Yes,
		No,
		Tab,
		Close,
		Quit key.Binding
	}
}

var _ Dialog = (*Quit)(nil)

// NewQuit creates a new quit confirmation dialog.
func NewQuit(com *common.Common, runningSubAgents int) *Quit {
	q := &Quit{
		com:              com,
		selectedNo:       true,
		runningSubAgents: runningSubAgents,
	}
	q.keyMap.LeftRight = key.NewBinding(
		key.WithKeys("left", "right"),
		key.WithHelp("←/→", "switch options"),
	)
	q.keyMap.EnterSpace = key.NewBinding(
		key.WithKeys("enter", " ", "space"),
		key.WithHelp("enter/space", "confirm"),
	)
	q.keyMap.Yes = key.NewBinding(
		key.WithKeys("y", "Y", "ctrl+c"),
		key.WithHelp("y/Y/ctrl+c", "yes"),
	)
	q.keyMap.No = key.NewBinding(
		key.WithKeys("n", "N"),
		key.WithHelp("n/N", "no"),
	)
	q.keyMap.Tab = key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch options"),
	)
	q.keyMap.Close = CloseKey
	q.keyMap.Quit = key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	)
	return q
}

// ID implements [模型].
func (*Quit) ID() string {
	return QuitID
}

// HandleMsg implements [模型].
func (q *Quit) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.MouseMotionMsg:
		q.hoverX, q.hoverY = msg.X, msg.Y
	case tea.MouseClickMsg:
		return q.handleMouseClick(msg)
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, q.keyMap.Quit):
			return ActionQuit{}
		case key.Matches(msg, q.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, q.keyMap.LeftRight, q.keyMap.Tab):
			q.selectedNo = !q.selectedNo
		case key.Matches(msg, q.keyMap.EnterSpace):
			if !q.selectedNo {
				return ActionQuit{}
			}
			return ActionClose{}
		case key.Matches(msg, q.keyMap.Yes):
			return ActionQuit{}
		case key.Matches(msg, q.keyMap.No, q.keyMap.Close):
			return ActionClose{}
		}
	}

	return nil
}

// handleMouseClick selects the clicked button. Clicking the already
// selected button activates it.
func (q *Quit) handleMouseClick(msg tea.MouseClickMsg) Action {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	switch common.HitButtonIndex(q.compositor, msg.X, msg.Y) {
	case 0: // "Yep!"
		if q.selectedNo {
			q.selectedNo = false
			return nil
		}
		return ActionQuit{}
	case 1: // "Nope"
		if !q.selectedNo {
			q.selectedNo = true
			return nil
		}
		return ActionClose{}
	}
	return nil
}

// Draw implements [Dialog].
func (q *Quit) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	var (
		baseStyle    = q.com.Styles.Dialog.Quit.Content
		hintStyle    = q.com.Styles.Dialog.Quit.Hint
		warningStyle = q.com.Styles.Dialog.Quit.Warning
	)
	buttonOpts := []common.ButtonOpts{
		{Text: "Yep!", Selected: !q.selectedNo, Padding: 3},
		{Text: "Nope", Selected: q.selectedNo, Padding: 3},
	}
	if hovered := common.HitButtonIndex(q.compositor, q.hoverX, q.hoverY); hovered >= 0 {
		buttonOpts[hovered].Hovered = true
	}
	buttonViews := make([]string, len(buttonOpts))
	for i, o := range buttonOpts {
		buttonViews[i] = common.Button(q.com.Styles, o)
	}
	buttons := strings.Join(buttonViews, " ")
	buttonsWidth := lipgloss.Width(buttons)

	// Quitting cuts off any 子代理 mid-turn.
	var warning string
	if q.runningSubAgents > 0 {
		noun := "subagents are"
		if q.runningSubAgents == 1 {
			noun = "subagent is"
		}
		warning = warningStyle.Render(fmt.Sprintf(
			"%d %s still running and will be cut off.",
			q.runningSubAgents, noun,
		))
	}

	// buttonLine 是 index 的 content line reserved 为 buttons,
	// which 是 drawn 作为 layers 在 top 的 frame. warning, 当
	// present, pushes 那 行 down 通过 它的 own height plus blank line.
	buttonLine := 2
	if warning != "" {
		buttonLine += lipgloss.Height(warning) + 1
	}

	// renderContent builds dialog body around given button 行. 一个
	// blank 行 的 same width reserves space 为 buttons, which 是
	// painted 作为 layers 所以 their bounds double 作为 mouse hit regions.
	renderContent := func(buttonRow string) string {
		lines := []string{quitQuestion}
		if warning != "" {
			lines = append(lines, "", warning)
		}
		lines = append(lines,
			"",
			buttonRow,
			"",
			hintStyle.Render(quitHintLineOne),
			hintStyle.Render(quitHintLineTwo),
		)
		return baseStyle.Render(lipgloss.JoinVertical(lipgloss.Center, lines...))
	}

	content := renderContent(strings.Repeat(" ", buttonsWidth))

	frameStyle := q.com.Styles.Dialog.Quit.Frame
	maxWidth := area.Dx() - frameStyle.GetHorizontalBorderSize()
	if maxWidth < lipgloss.Width(content) {
		frameStyle = frameStyle.Padding(1, 0)
	}
	view := frameStyle.Render(content)

	width, height := lipgloss.Size(view)
	width = min(width, area.Dx())
	height = min(height, area.Dy())
	center := common.CenterRect(area, width, height)

	// Offset content 通过 every frame two styles add around it.
	contentX := center.Min.X +
		baseStyle.GetMarginLeft() + baseStyle.GetBorderLeftSize() + baseStyle.GetPaddingLeft() +
		frameStyle.GetMarginLeft() + frameStyle.GetBorderLeftSize() + frameStyle.GetPaddingLeft()
	contentY := center.Min.Y +
		baseStyle.GetMarginTop() + baseStyle.GetBorderTopSize() + baseStyle.GetPaddingTop() +
		frameStyle.GetMarginTop() + frameStyle.GetBorderTopSize() + frameStyle.GetPaddingTop()
	// buttons 行 是 centered within rendered content, exactly like
	// lipgloss.JoinVertical centers it: the inner width excludes the
	// content frame, 和 odd gap rounds left padding up.
	innerWidth := lipgloss.Width(content) - baseStyle.GetHorizontalFrameSize()
	buttonsX := contentX + (innerWidth-buttonsWidth+1)/2
	buttonsY := contentY + buttonLine

	q.compositor = nil
	buttonsRect := image.Rect(buttonsX, buttonsY, buttonsX+buttonsWidth, buttonsY+1)
	if buttonsRect.In(center) {
		DrawCenter(scr, area, view)
		q.compositor = drawButtons(scr, buttonsX, buttonsY, buttonViews)
	} else {
		// buttons don't fit 在 screen 作为 layers, 所以 render them inline
		// 作为 片段 的 content instead: 用户 always sees them.
		DrawCenter(scr, area, frameStyle.Render(renderContent(buttons)))
	}
	return nil
}

// HandlesHover implements [HoverDialog].
func (*Quit) HandlesHover() bool {
	return true
}

// drawButtons paints buttons 作为 layers 在 given position 和 returns
// compositor whose layer bounds serve 作为 mouse hit regions.
func drawButtons(scr uv.Screen, x, y int, views []string) *lipgloss.Compositor {
	layers := make([]*lipgloss.Layer, len(views))
	bx := x
	for i, v := range views {
		layers[i] = lipgloss.NewLayer(v).X(bx).Y(y).ID(fmt.Sprintf("btn_%d", i))
		bx += lipgloss.Width(v) + 1 // one cell between buttons
	}
	compositor := lipgloss.NewCompositor(layers...)
	compositor.Draw(scr, scr.Bounds())
	return compositor
}

// ShortHelp implements [help.KeyMap].
func (q *Quit) ShortHelp() []key.Binding {
	return []key.Binding{
		q.keyMap.LeftRight,
		q.keyMap.EnterSpace,
	}
}

// FullHelp implements [help.KeyMap].
func (q *Quit) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{q.keyMap.LeftRight, q.keyMap.EnterSpace, q.keyMap.Yes, q.keyMap.No},
		{q.keyMap.Tab, q.keyMap.Close},
	}
}
