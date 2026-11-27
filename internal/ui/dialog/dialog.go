// dialog.go —— 对话框的通用框架：打开/关闭/叠加与尺寸计算。

package dialog

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
)

// Dialog sizing constants.
const (
	// defaultDialogMaxWidth 是 maximum width 为 standard dialogs.
	defaultDialogMaxWidth = 70
	// defaultDialogHeight 是 默认 height 为 standard dialogs.
	defaultDialogHeight = 20
	// titleContentHeight 是 height 的 标题 content line.
	titleContentHeight = 1
	// inputContentHeight 是 height 的 input content line.
	inputContentHeight = 1
)

// CloseKey 是 默认 key binding 到 close dialogs.
var CloseKey = key.NewBinding(
	key.WithKeys("esc", "alt+esc"),
	key.WithHelp("esc", "exit"),
)

// Action represents action taken 在 dialog after handling 消息.
type Action any

// Dialog 是 component 那 can 是 displayed 在 top 的 UI.
type Dialog interface {
	// ID returns unique identifier 的 dialog.
	ID() string
	// HandleMsg processes 消息 和 returns action. An [Action] can 是
	// anything 和 caller 是 responsible 为 handling it appropriately.
	HandleMsg(msg tea.Msg) Action
	// Draw draws the dialog onto the provided screen within the specified area
	// 和 returns desired cursor position 在 screen.
	Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor
}

// LoadingDialog 是 dialog 那 can show loading state.
type LoadingDialog interface {
	StartLoading() tea.Cmd
	StopLoading()
}

// HoverDialog 是 dialog 那 reacts 到 mouse hover. While one 是 open,
// 终态 must 报告 all mouse motion events, 不 just clicks.
type HoverDialog interface {
	HandlesHover() bool
}

// Grace period constants 为 dialogs 那 open asynchronously 和 may
// receive 在-flight keystrokes 从 previously focused component.
const (
	// graceQuietPeriod 是 how long input must 是 quiet before dialog
	// arms. Each absorbed keystroke resets 这 timer.
	graceQuietPeriod = 425 * time.Millisecond
	// graceMaxDelay 是 absolute ceiling: dialog always arms after
	// 这 duration regardless 的 input activity. Prevents auto-repeat
	// 从 keeping dialog disarmed indefinitely.
	graceMaxDelay = 1500 * time.Millisecond
)

// Overlay manages multiple dialogs 作为 overlay.
type Overlay struct {
	dialogs []Dialog

	// Grace period state 为 front dialog. Only 活跃 当
	// dialog was opened via OpenDialogWithGrace.
	graceOpenedAt    time.Time
	graceLastInputAt time.Time

	// Track recently closed dialog IDs 所以 那 reopening same
	// dialog type can skip grace period. 这 prevents rapid
	// successive dialogs (e.g. multiple 权限 prompts) 从
	// each eating a keystroke.
	lastClosedID string
	lastClosedAt time.Time
}

// NewOverlay creates a new [Overlay] instance.
func NewOverlay(dialogs ...Dialog) *Overlay {
	return &Overlay{
		dialogs: dialogs,
	}
}

// HasDialogs checks 如果 there 是 any 活跃 dialogs.
func (d *Overlay) HasDialogs() bool {
	return len(d.dialogs) > 0
}

// HandlesHover reports whether any open dialog reacts 到 mouse hover, 在
// which case 终态 should 报告 all mouse motion events.
func (d *Overlay) HandlesHover() bool {
	for _, dialog := range d.dialogs {
		if hd, ok := dialog.(HoverDialog); ok && hd.HandlesHover() {
			return true
		}
	}
	return false
}

// ContainsDialog checks 如果 dialog 用 specified ID exists.
func (d *Overlay) ContainsDialog(dialogID string) bool {
	for _, dialog := range d.dialogs {
		if dialog.ID() == dialogID {
			return true
		}
	}
	return false
}

// OpenDialog opens new dialog 到 stack.
func (d *Overlay) OpenDialog(dialog Dialog) {
	d.dialogs = append(d.dialogs, dialog)
	d.graceOpenedAt = time.Time{}
	d.graceLastInputAt = time.Time{}
}

// OpenDialogWithGrace opens dialog 用 input grace period. All
// keystrokes 是 absorbed until either input has been quiet 为
// graceQuietPeriod 或 graceMaxDelay has elapsed since opening, whichever
// comes first. Use 这 为 dialogs 那 open asynchronously (e.g.
// 权限 prompts) where 在-flight keystrokes 从 previously
// focused component could act 在 dialog before 用户 sees it.
//
// If the same dialog ID was just closed (within reopenGraceWindow),
// grace period 是 skipped — 用户 是 already focused 在 这
// dialog type 和 rapid successive prompts should 不 eat keystrokes.
func (d *Overlay) OpenDialogWithGrace(dialog Dialog) {
	now := time.Now()
	d.dialogs = append(d.dialogs, dialog)

	// Skip grace 当 reopening same dialog type immediately.
	if dialog.ID() == d.lastClosedID && now.Sub(d.lastClosedAt) < reopenGraceWindow {
		d.graceOpenedAt = time.Time{}
		d.graceLastInputAt = time.Time{}
		return
	}

	d.graceOpenedAt = now
	d.graceLastInputAt = now
}

// inGracePeriod reports whether front dialog 是 still within 它的
// input grace period. Returns false 如果 no grace period 是 活跃.
func (d *Overlay) inGracePeriod() bool {
	if d.graceOpenedAt.IsZero() {
		return false
	}
	if time.Since(d.graceOpenedAt) >= graceMaxDelay {
		return false
	}
	if time.Since(d.graceLastInputAt) >= graceQuietPeriod {
		return false
	}
	return true
}

// CloseDialog closes dialog 用 specified ID 从 stack.
func (d *Overlay) CloseDialog(dialogID string) {
	for i, dialog := range d.dialogs {
		if dialog.ID() == dialogID {
			d.removeDialog(i)
			return
		}
	}
}

// CloseFrontDialog closes front dialog 在 stack.
func (d *Overlay) CloseFrontDialog() {
	if len(d.dialogs) == 0 {
		return
	}
	d.removeDialog(len(d.dialogs) - 1)
}

// reopenGraceWindow 是 how long after closing dialog we consider
// reopen 的 same dialog ID 到 是 "immediate" 和 skip grace.
const reopenGraceWindow = 500 * time.Millisecond

func (d *Overlay) removeDialog(idx int) {
	if idx == len(d.dialogs)-1 {
		d.lastClosedID = d.dialogs[idx].ID()
		d.lastClosedAt = time.Now()
	}
	d.dialogs = append(d.dialogs[:idx], d.dialogs[idx+1:]...)
	// Clear grace state 当 front dialog changes.
	if idx == len(d.dialogs) {
		d.graceOpenedAt = time.Time{}
		d.graceLastInputAt = time.Time{}
	}
}

// Dialog returns dialog 用 specified ID, 或 nil 如果 不 found.
func (d *Overlay) Dialog(dialogID string) Dialog {
	for _, dialog := range d.dialogs {
		if dialog.ID() == dialogID {
			return dialog
		}
	}
	return nil
}

// DialogLast returns front dialog, 或 nil 如果 there 是 no dialogs.
func (d *Overlay) DialogLast() Dialog {
	if len(d.dialogs) == 0 {
		return nil
	}
	return d.dialogs[len(d.dialogs)-1]
}

// BringToFront brings dialog 用 specified ID 到 front.
func (d *Overlay) BringToFront(dialogID string) {
	for i, dialog := range d.dialogs {
		if dialog.ID() == dialogID {
			// Move dialog 到 end 的 slice
			d.dialogs = append(d.dialogs[:i], d.dialogs[i+1:]...)
			d.dialogs = append(d.dialogs, dialog)
			return
		}
	}
}

// 更新 handles dialog updates.
func (d *Overlay) Update(msg tea.Msg) tea.Msg {
	if len(d.dialogs) == 0 {
		return nil
	}

	// Absorb keystrokes during grace period 为 async dialogs.
	if _, ok := msg.(tea.KeyPressMsg); ok && d.inGracePeriod() {
		d.graceLastInputAt = time.Now()
		return nil
	}

	idx := len(d.dialogs) - 1 // active dialog is the last one
	dialog := d.dialogs[idx]
	if dialog == nil {
		return nil
	}

	return dialog.HandleMsg(msg)
}

// StartLoading starts loading state 为 front dialog 如果 it
// implements [LoadingDialog].
func (d *Overlay) StartLoading() tea.Cmd {
	dialog := d.DialogLast()
	if ld, ok := dialog.(LoadingDialog); ok {
		return ld.StartLoading()
	}
	return nil
}

// StopLoading stops loading state 为 front dialog 如果 it
// implements [LoadingDialog].
func (d *Overlay) StopLoading() {
	dialog := d.DialogLast()
	if ld, ok := dialog.(LoadingDialog); ok {
		ld.StopLoading()
	}
}

// DrawCenterCursor draws given string view centered 在 screen area 和
// adjusts cursor position accordingly. Content larger than area 是
// clamped 到 fit.
func DrawCenterCursor(scr uv.Screen, area uv.Rectangle, view string, cur *tea.Cursor) {
	width, height := lipgloss.Size(view)
	// Clamp 到 available area 所以 oversized dialogs don't draw outside bounds.
	width = min(width, area.Dx())
	height = min(height, area.Dy())
	center := common.CenterRect(area, width, height)
	if cur != nil {
		cur.X += center.Min.X
		cur.Y += center.Min.Y
	}
	uv.NewStyledString(view).Draw(scr, center)
}

// DrawCenter draws given string view centered 在 screen area.
func DrawCenter(scr uv.Screen, area uv.Rectangle, view string) {
	DrawCenterCursor(scr, area, view, nil)
}

// DrawOnboarding draws given string view centered 在 screen area.
func DrawOnboarding(scr uv.Screen, area uv.Rectangle, view string) {
	DrawOnboardingCursor(scr, area, view, nil)
}

// DrawOnboardingCursor draws given string view positioned 在 bottom
// left area 的 screen. Content larger than area 是 clamped 到 fit.
func DrawOnboardingCursor(scr uv.Screen, area uv.Rectangle, view string, cur *tea.Cursor) {
	width, height := lipgloss.Size(view)
	// Clamp 到 available area 所以 oversized dialogs don't draw outside bounds.
	width = min(width, area.Dx())
	height = min(height, area.Dy())
	bottomLeft := common.BottomLeftRect(area, width, height)
	if cur != nil {
		cur.X += bottomLeft.Min.X
		cur.Y += bottomLeft.Min.Y
	}
	uv.NewStyledString(view).Draw(scr, bottomLeft)
}

// Draw renders overlay 和 它的 dialogs.
func (d *Overlay) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	var cur *tea.Cursor
	for _, dialog := range d.dialogs {
		cur = dialog.Draw(scr, area)
	}
	return cur
}
