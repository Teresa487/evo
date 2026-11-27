package dialog

import (
	"cmp"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// dialogInputTextWidth returns 文本-area width 为 dialog input 所以
// 那 input frame, 它的 提示词 (e.g. "> "), 文本, 和 trailing
// cursor cell all fit within contentWidth. 提示词 是 rendered outside
// 文本 area, 所以 it must 是 subtracted 或 long values wrap past
// dialog border.
func dialogInputTextWidth(t *styles.Styles, input textinput.Model, contentWidth int) int {
	const cursorPadding = 1
	return max(0, contentWidth-
		t.Dialog.InputPrompt.GetHorizontalFrameSize()-
		lipgloss.Width(input.Prompt)-
		cursorPadding)
}

// sizer 是 satisfied 通过 any list type 那 can 报告 它的 total content
// height 和 受理 viewport size. Both *list.List 和 *list.FilterableList
// (和 wrappers embedding them) implement 这.
type sizer interface {
	TotalHeight() int
	SetSize(width, height int)
}

// sizeDialogList computes list dimensions within dialog 和 calls
// l.SetSize. 它 accounts 为 标题, input, help, 和 view frame sizes
// 所以 callers don't have 到 repeat arithmetic. scrollbar column 是
// reserved 只 当 content overflows viewport.
//
// Returns listHeight, listTotalHeight, 和 listWidth 为 callers 那 need
// them (e.g. 到 pass 到 joinScrollbar 或 applyInfoColumnVisibility).
//
// Parameters:
// - t: styles 为 frame/border measurements.
// - l: list 到 size.
//   - innerWidth: dialog content width (total minus View horizontal frame).
//   - dialogHeight: total dialog content height (already clamped).
func sizeDialogList(t *styles.Styles, l sizer, innerWidth, dialogHeight int) (listHeight, listTotalHeight, listWidth int) {
	heightOffset := t.Dialog.Title.GetVerticalFrameSize() + titleContentHeight +
		t.Dialog.InputPrompt.GetVerticalFrameSize() + inputContentHeight +
		t.Dialog.HelpView.GetVerticalFrameSize() +
		t.Dialog.View.GetVerticalFrameSize()

	listHeight = max(0, dialogHeight-heightOffset)
	listTotalHeight = l.TotalHeight()

	// Reserve one column 为 scrollbar 只 当 it will actually
	// show, 所以 list otherwise spans full content width.
	scrollbarWidth := 0
	if listTotalHeight > listHeight {
		scrollbarWidth = 1
	}
	listWidth = max(0, innerWidth-scrollbarWidth)
	l.SetSize(listWidth, listHeight)
	return listHeight, listTotalHeight, listWidth
}

// joinScrollbar appends vertical scrollbar 到 right 的 view 当
// content overflows 它的 viewport, 和 returns view unchanged otherwise.
// contentSize 是 total content height, viewportSize visible height,
// 和 offset current scroll position.
func joinScrollbar(t *styles.Styles, view string, height, contentSize, viewportSize, offset int) string {
	if sb := common.Scrollbar(t, height, contentSize, viewportSize, offset); sb != "" {
		return lipgloss.JoinHorizontal(lipgloss.Top, view, sb)
	}
	return view
}

// Maximum share 的 list 行 width secondary info column may take
// before it 是 hidden entirely, 所以 it never crowds out item name.
// Command shortcuts 是 small 和 non-essential, 所以 they yield sooner
// than larger, more useful 会话 timestamps.
const (
	sessionInfoMaxPercent = 35
	commandInfoMaxPercent = 25
)

// infoColumnItem 是 list item 用 secondary info column ( 会话
// timestamp, command shortcut) 那 can 是 hidden 当 space 是 tight.
type infoColumnItem interface {
	// InfoText returns raw info string, 或 "" 当 there 是 none.
	InfoText() string
	// SetHideInfo toggles whether info column 是 rendered.
	SetHideInfo(bool)
}

// applyInfoColumnVisibility hides the secondary info column across every
// item uniformly 当 它的 widest 条目 would take more than maxPercent 的
// rowWidth, 所以 item names keep their room. 它 returns once rowWidth grows
// enough 为 widest 条目 到 fit within budget again.
func applyInfoColumnVisibility(items []list.Item, rowWidth, maxPercent int) {
	widest := 0
	for _, it := range items {
		if ic, ok := it.(infoColumnItem); ok {
			if info := ic.InfoText(); info != "" {
				widest = max(widest, lipgloss.Width(" "+info+" "))
			}
		}
	}
	hide := rowWidth > 0 && widest*100 > rowWidth*maxPercent
	for _, it := range items {
		if ic, ok := it.(infoColumnItem); ok {
			ic.SetHideInfo(hide)
		}
	}
}

// renderDialogHelp renders keybind hints 作为 single padded footer line 在
// contentWidth (the dialog's inner width: total minus the View border). The
// hints 是 packed greedily 和 truncated 用 ellipsis 所以 line never
// wraps 或 overflows border, 和 never ends 在 dangling separator.
func renderDialogHelp(t *styles.Styles, h *help.Model, km help.KeyMap, contentWidth int) string {
	textWidth := max(0, contentWidth-t.Dialog.HelpView.GetHorizontalFrameSize())
	return t.Dialog.HelpView.Render(shortHelpLine(h, km.ShortHelp(), textWidth))
}

// shortHelpLine builds single-line short help view truncated 到 width.
// 它 reimplements bubbles help packing 到 avoid component bug where
// items 是 kept even 当 they overflow (当 ellipsis itself does 不
// fit), 和 到 guarantee line ends cleanly rather than 在 separator.
func shortHelpLine(h *help.Model, bindings []key.Binding, width int) string {
	if width <= 0 {
		return ""
	}
	sep := h.Styles.ShortSeparator.Inline(true).Render(h.ShortSeparator)
	ellipsis := h.Styles.Ellipsis.Inline(true).Render(cmp.Or(h.Ellipsis, "…"))

	var b strings.Builder
	total := 0
	for _, kb := range bindings {
		if !kb.Enabled() {
			continue
		}
		seg := ""
		if total > 0 {
			seg = sep
		}
		seg += h.Styles.ShortKey.Inline(true).Render(kb.Help().Key) + " " +
			h.Styles.ShortDesc.Inline(true).Render(kb.Help().Desc)
		w := lipgloss.Width(seg)
		if total+w > width {
			// next item doesn't fit; add ellipsis 如果 there's room.
			// separator belongs 到 这 dropped item, 所以 what we've
			// written already ends 在 real hint, 不 dangling dot. 一个
			// leading space joins ellipsis 到 prior hints, 但 只
			// 当 there 是 prior hints.
			tail := ellipsis
			if total > 0 {
				tail = " " + ellipsis
			}
			if total+lipgloss.Width(tail) <= width {
				b.WriteString(tail)
			}
			break
		}
		total += w
		b.WriteString(seg)
	}
	return b.String()
}

// InputCursor adjusts cursor position 为 input 字段 within dialog.
func InputCursor(t *styles.Styles, cur *tea.Cursor) *tea.Cursor {
	if cur != nil {
		titleStyle := t.Dialog.Title
		dialogStyle := t.Dialog.View
		inputStyle := t.Dialog.InputPrompt
		// Adjust cursor position 到 account 为 dialog layout
		cur.X += inputStyle.GetBorderLeftSize() +
			inputStyle.GetMarginLeft() +
			inputStyle.GetPaddingLeft() +
			dialogStyle.GetBorderLeftSize() +
			dialogStyle.GetPaddingLeft() +
			dialogStyle.GetMarginLeft()
		cur.Y += titleStyle.GetVerticalFrameSize() +
			inputStyle.GetBorderTopSize() +
			inputStyle.GetMarginTop() +
			inputStyle.GetPaddingTop() +
			inputStyle.GetBorderBottomSize() +
			inputStyle.GetMarginBottom() +
			inputStyle.GetPaddingBottom() +
			dialogStyle.GetPaddingTop() +
			dialogStyle.GetMarginTop() +
			dialogStyle.GetBorderTopSize()
	}
	return cur
}

// adjustOnboardingInputCursor removes dialog view frame offset 从
// input cursor. Onboarding dialogs render without Dialog.View frame, while
// InputCursor includes 那 frame offset 为 regular dialogs.
func adjustOnboardingInputCursor(t *styles.Styles, cur *tea.Cursor) *tea.Cursor {
	if cur == nil {
		return nil
	}

	dialogStyle := t.Dialog.View
	cur.X -= dialogStyle.GetBorderLeftSize() +
		dialogStyle.GetPaddingLeft() +
		dialogStyle.GetMarginLeft()
	cur.Y -= dialogStyle.GetBorderTopSize() +
		dialogStyle.GetPaddingTop() +
		dialogStyle.GetMarginTop()
	return cur
}

// renameCursorOffset adjusts cursor returned 通过 inline list item's
// 文本 input 所以 it lands 在 correct screen position inside dialog
// 那 是 showing rename 提示词. math accounts 为 dialog
// border/padding/margin, input 提示词 frame, 和 height 的
// confirmation 消息 rendered above list. visibleStart 和
// visibleEnd 是 list's VisibleItemIndices; selectedIndex 是
// list's Selected index. cursor Y 是 advanced one 行 per item
// between visibleStart 和 selectedIndex.
func renameCursorOffset(
	t *styles.Styles,
	cur *tea.Cursor,
	messageHeight int,
	visibleStart, visibleEnd, selectedIndex int,
) *tea.Cursor {
	if cur == nil {
		return nil
	}
	titleStyle := t.Dialog.Sessions.RenamingingTitle
	dialogStyle := t.Dialog.Sessions.RenamingView
	inputStyle := t.Dialog.InputPrompt

	cur.X += inputStyle.GetBorderLeftSize() +
		inputStyle.GetMarginLeft() +
		inputStyle.GetPaddingLeft() +
		dialogStyle.GetBorderLeftSize() +
		dialogStyle.GetPaddingLeft() +
		dialogStyle.GetMarginLeft()
	cur.Y += titleStyle.GetVerticalFrameSize() +
		inputStyle.GetBorderTopSize() +
		inputStyle.GetMarginTop() +
		inputStyle.GetPaddingTop() +
		inputStyle.GetBorderBottomSize() +
		inputStyle.GetMarginBottom() +
		inputStyle.GetPaddingBottom() +
		dialogStyle.GetPaddingTop() +
		dialogStyle.GetBorderTopSize() +
		messageHeight - 1

	for i := visibleStart; i <= visibleEnd && i != selectedIndex && selectedIndex > -1; i++ {
		cur.Y++
	}
	return cur
}

// RenderContext 是 dialog rendering 上下文 那 can 是 used 到 render
// common dialog layouts.
type RenderContext struct {
	// Styles 是 styles 到 use 为 rendering.
	Styles *styles.Styles
	// TitleStyle 是 style 的 dialog 标题 通过 默认 it uses Styles.Dialog.标题
	TitleStyle lipgloss.Style
	// ViewStyle 是 style 的 dialog 标题 通过 默认 it uses Styles.Dialog.View
	ViewStyle lipgloss.Style
	// TitleGradientFromColor 是 color 标题 gradient starts 通过 默认
	// 它的 Styles.Dialog.TitleGradFromColor
	TitleGradientFromColor color.Color
	// TitleGradientToColor 是 color 标题 gradient ends 通过 默认 它的
	// Styles.Dialog.TitleGradToColor
	TitleGradientToColor color.Color
	// Width 是 total width 的 dialog including any margins, borders,
	// 和 paddings.
	Width int
	// Gap 是 gap between content parts. Zero means no gap.
	Gap int
	// 标题 是 标题 的 dialog. 这 will 是 styled using 默认
	// dialog 标题 style 和 prepended 到 content parts slice.
	Title string
	// TitleInfo 是 additional information 到 display next 到 标题. 这
	// 片段 是 displayed 作为 是, any styling must 是 applied before setting 这
	// 字段.
	TitleInfo string
	// Parts 是 rendered parts 的 dialog.
	Parts []string
	// Help 是 fully rendered help footer line. Produce it 用
	// renderDialogHelp 所以 it 是 sized 和 padded consistently; it 是
	// appended 作为-是 without further styling.
	Help string
	// IsOnboarding indicates whether 到 render dialog 作为 片段 的
	// onboarding flow. 这 means 那 content will 是 rendered 在
	// bottom left 的 screen.
	IsOnboarding bool
}

// NewRenderContext creates new RenderContext 用 provided styles 和 width.
func NewRenderContext(t *styles.Styles, width int) *RenderContext {
	return &RenderContext{
		Styles:                 t,
		TitleStyle:             t.Dialog.Title,
		ViewStyle:              t.Dialog.View,
		TitleGradientFromColor: t.Dialog.TitleGradFromColor,
		TitleGradientToColor:   t.Dialog.TitleGradToColor,
		Width:                  width,
		Parts:                  []string{},
	}
}

// AddPart adds rendered 片段 到 dialog.
func (rc *RenderContext) AddPart(part string) {
	if len(part) > 0 {
		rc.Parts = append(rc.Parts, part)
	}
}

// Render renders dialog using provided 上下文.
func (rc *RenderContext) Render() string {
	titleStyle := rc.TitleStyle
	dialogStyle := rc.ViewStyle.Width(rc.Width)

	var parts []string

	if len(rc.Title) > 0 {
		contentWidth := rc.Width - dialogStyle.GetHorizontalFrameSize() -
			titleStyle.GetHorizontalFrameSize()
		titleInfo := rc.TitleInfo
		titleInfoWidth := lipgloss.Width(titleInfo)
		// Drop 标题 info entirely 当 it can't sit beside 标题
		// 文本 用 在 least one-cell gap. 标题 info 是 often styled
		// (e.g. radio toggles 用 backgrounds 和 padding); truncating it
		// mid-segment leaves broken colored fragments, 所以 hide it instead.
		if titleInfoWidth > 0 && lipgloss.Width(rc.Title)+1+titleInfoWidth > contentWidth {
			titleInfo = ""
			titleInfoWidth = 0
		}
		title := common.DialogTitle(rc.Styles, rc.Title,
			max(0, contentWidth-titleInfoWidth), rc.TitleGradientFromColor, rc.TitleGradientToColor)
		if len(titleInfo) > 0 {
			title += titleInfo
		}
		parts = append(parts, titleStyle.Render(title))
		if rc.Gap > 0 {
			parts = append(parts, make([]string, rc.Gap)...)
		}
	}

	if rc.Gap <= 0 {
		parts = append(parts, rc.Parts...)
	} else {
		for i, p := range rc.Parts {
			if len(p) > 0 {
				parts = append(parts, p)
			}
			if i < len(rc.Parts)-1 {
				parts = append(parts, make([]string, rc.Gap)...)
			}
		}
	}

	if len(rc.Help) > 0 {
		if rc.Gap > 0 {
			parts = append(parts, make([]string, rc.Gap)...)
		}
		parts = append(parts, rc.Help)
	}

	content := strings.Join(parts, "\n")
	if rc.IsOnboarding {
		return content
	}
	return dialogStyle.Render(content)
}
