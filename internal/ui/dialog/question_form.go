// question_form.go —— 多字段表单型提问弹窗。

package dialog

import (
	"fmt"
	"image"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// questionResponder extends InlineEditor 用 access 到 last
// 响应. Used internally 通过 QuestionForm 到 collect answers
// 从 child components.
type questionResponder interface {
	InlineEditor
	Response() question.Answer
	SetHover(x, y int)
	HandleMouseClick(x, y int) (done bool, handled bool)
}

// QuestionForm presents multiple questions 作为 tabbed form.
// Tab/shift+tab switches between questions; each question 保持
// 它的 own internal keybindings. For multi-question batches,
// Confirm tab 是 appended automatically.
type QuestionForm struct {
	Styles       *styles.Styles
	BatchID      string
	questions    []questionResponder // includes ConfirmComponent as last item for batches
	labels       []string            // includes "Confirm" for batches
	requestIDs   []string
	answers      []*question.Answer // nil until answered; only covers real questions
	activeIdx    int
	focused      bool
	hasConfirm   bool              // whether a confirm tab exists
	showTabs     bool              // whether to render tab chrome
	numQuestions int               // real question count (excludes confirm tab)
	confirmComp  *ConfirmComponent // nil when no confirm tab

	keyPrevTab key.Binding
	keyNextTab key.Binding
	keyClose   key.Binding

	// Compositor 为 tab hit detection. Built during Draw() 从
	// tab layers positioned 在 their screen coordinates.
	compositor *lipgloss.Compositor

	// Hover position 为 highlighting interactive elements.
	hoverX, hoverY int

	// OnAnswer 是 called 当 form 是 submitted. UI sets
	// 这 到 wire up 工作区 submission.
	OnAnswer func(responses []question.Answer)

	// OnCancel 是 called 当 用户 presses escape 到 取消
	// entire question batch. UI sets 这 到 wire up
	// 工作区 cancellation.
	OnCancel func()
}

var _ CollapsibleInlineEditor = (*QuestionForm)(nil)

// NewQuestionForm creates tabbed multi-question form 从
// batch 请求. Each question 是 wrapped 在 它的 existing
// component type (YesNo, SingleChoice, MultiChoice, FreeText).
// 一个 Confirm tab 是 appended 为 multi-question batches.
func NewQuestionForm(sty *styles.Styles, batch question.Request) *QuestionForm {
	comps := make([]questionResponder, len(batch.Questions))
	labels := make([]string, len(batch.Questions))
	ids := make([]string, len(batch.Questions))
	for i, req := range batch.Questions {
		switch req.Type {
		case question.TypeYesNo:
			comps[i] = NewYesNo(sty, req)
		case question.TypeSingleChoice:
			comps[i] = NewSingleChoice(sty, req)
		case question.TypeMultiChoice:
			comps[i] = NewMultiChoice(sty, req)
		case question.TypeFreeText:
			comps[i] = NewFreeText(sty, req)
		}
		if req.Label != "" {
			labels[i] = req.Label
		} else {
			labels[i] = shortLabel(req.Text)
		}
		ids[i] = req.ID
	}

	numQuestions := len(comps)
	// Confirm tab 只 为 multi-question batches.
	hasConfirm := numQuestions > 1
	answers := make([]*question.Answer, numQuestions)

	var confirmComp *ConfirmComponent
	allLabels := labels
	if hasConfirm {
		confirmTitle := batch.ConfirmTitle
		if confirmTitle == "" {
			confirmTitle = "Confirm"
		}
		confirmComp = NewConfirmComponent(
			sty,
			confirmTitle,
			batch.ConfirmDescription,
			labels,
			batch.Questions,
			answers,
		)
		allLabels = make([]string, len(labels)+1)
		copy(allLabels, labels)
		allLabels[len(labels)] = "Confirm"
	}
	showTabs := numQuestions > 1

	f := &QuestionForm{
		Styles:       sty,
		BatchID:      batch.ID,
		questions:    comps,
		labels:       allLabels,
		requestIDs:   ids,
		answers:      answers,
		hasConfirm:   hasConfirm,
		showTabs:     showTabs,
		numQuestions: numQuestions,
		confirmComp:  confirmComp,
		keyPrevTab: key.NewBinding(
			key.WithKeys("[", "ctrl+left"),
			key.WithHelp("[", "prev tab"),
		),
		keyNextTab: key.NewBinding(
			key.WithKeys("]", "ctrl+right"),
			key.WithHelp("]", "next tab"),
		),
		keyClose: CloseKey,
	}

	// Wire confirm callbacks.
	if confirmComp != nil {
		confirmComp.OnConfirm = f.submit
		confirmComp.OnReject = func() {
			if idx := f.firstUnanswered(); idx >= 0 {
				f.switchTab(idx)
			} else if numQuestions > 0 {
				f.switchTab(numQuestions - 1)
			}
		}
	}

	if len(comps) > 0 {
		comps[0].SetFocused(true)
	}
	return f
}

// shortLabel truncates question 到 在 most three words 为 use
// 作为 tab header.
func shortLabel(q string) string {
	q = strings.ReplaceAll(q, "\n", " ")
	words := strings.Fields(q)
	if len(words) > 3 {
		words = words[:3]
	}
	return strings.Join(words, " ")
}

// isConfirmTab reports whether 活跃 tab 是 confirm tab.
func (f *QuestionForm) isConfirmTab() bool {
	return f.hasConfirm && f.activeIdx == f.numQuestions
}

// isAnswered reports whether a question has a meaningful answer.
func (f *QuestionForm) isAnswered(idx int) bool {
	if idx >= len(f.answers) || f.answers[idx] == nil {
		return false
	}
	resp := f.answers[idx]
	return len(resp.SelectedIDs) > 0 || resp.FillInText != "" || resp.Yes != nil
}

// firstUnanswered returns index 的 first unanswered
// question, 或 -1 如果 all 是 answered.
func (f *QuestionForm) firstUnanswered() int {
	for i, ans := range f.answers {
		if ans == nil {
			return i
		}
		if len(ans.SelectedIDs) == 0 && ans.FillInText == "" && ans.Yes == nil {
			return i
		}
	}
	return -1
}

// HandleKey routes keys 到 活跃 tab. Returns true 当
// entire batch 是 submitted.
func (f *QuestionForm) HandleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	// Tab navigation works 在 all tabs including confirm.
	switch {
	case key.Matches(msg, f.keyNextTab):
		f.switchTab(f.activeIdx + 1)
		return false, nil
	case key.Matches(msg, f.keyPrevTab):
		f.switchTab(f.activeIdx - 1)
		return false, nil
	}

	// Confirm tab delegates 到 ConfirmComponent.
	if f.isConfirmTab() {
		done, cmd := f.confirmComp.HandleKey(msg)
		if done {
			return true, cmd
		}
		return false, cmd
	}

	// Global keys 为 question tabs.
	if key.Matches(msg, f.keyClose) {
		f.cancel()
		return true, nil
	}

	// Route 到 活跃 question.
	if f.activeIdx < f.numQuestions {
		done, cmd := f.questions[f.activeIdx].HandleKey(msg)
		if done {
			resp := f.questions[f.activeIdx].Response()
			f.answers[f.activeIdx] = &resp
			f.syncConfirmAnswers()
			if f.activeIdx < len(f.labels)-1 {
				f.switchTab(f.activeIdx + 1)
			} else if !f.hasConfirm {
				f.submit()
				return true, cmd
			}
			return false, cmd
		}
		return false, cmd
	}
	return false, nil
}

// HandleWheel scrolls 活跃 choice list vertically, 或 delegates
// 到 活跃 question 如果 it supports wheel scrolling.
func (f *QuestionForm) HandleWheel(deltaX, deltaY float64) {
	if f.isConfirmTab() {
		if deltaY < 0 && f.confirmComp.scrollOffset > 0 {
			f.confirmComp.scrollOffset--
		} else if deltaY > 0 {
			f.confirmComp.scrollOffset++
		}
		return
	}
	if f.activeIdx >= f.numQuestions {
		return
	}
	if we, ok := f.questions[f.activeIdx].(common.WheelScrollable); ok {
		we.HandleWheel(deltaX, deltaY)
	}
}

// switchTab moves focus 到 given tab index, wrapping around.
// Snapshots current question's 响应 before leaving.
func (f *QuestionForm) switchTab(idx int) {
	totalTabs := len(f.labels)
	if totalTabs == 0 {
		return
	}
	// Snapshot 和 unfocus current.
	if !f.isConfirmTab() && f.activeIdx < f.numQuestions {
		resp := f.questions[f.activeIdx].Response()
		f.answers[f.activeIdx] = &resp
		f.questions[f.activeIdx].SetFocused(false)
	} else if f.isConfirmTab() {
		f.confirmComp.SetFocused(false)
	}
	// Wrap.
	if idx < 0 {
		idx = totalTabs - 1
	} else if idx >= totalTabs {
		idx = 0
	}
	f.activeIdx = idx
	// Focus new.
	if f.isConfirmTab() {
		f.syncConfirmAnswers()
		f.confirmComp.SetFocused(f.focused)
	} else if f.activeIdx < f.numQuestions {
		f.questions[f.activeIdx].SetFocused(f.focused)
	}
}

// syncConfirmAnswers pushes latest answers 到 confirm
// component 所以 它的 摘要 stays current.
func (f *QuestionForm) syncConfirmAnswers() {
	if f.confirmComp != nil {
		f.confirmComp.UpdateAnswers(f.answers)
	}
}

// submit collects 存储 responses 和 calls OnAnswer.
func (f *QuestionForm) submit() {
	responses := make([]question.Answer, f.numQuestions)
	for i, ans := range f.answers {
		if ans != nil {
			responses[i] = *ans
		} else {
			responses[i] = question.Answer{
				QuestionID: f.requestIDs[i],
			}
		}
	}
	if f.OnAnswer != nil {
		f.OnAnswer(responses)
	}
}

// 取消 calls OnCancel 到 signal 那 用户 dismissed
// question batch without answering.
func (f *QuestionForm) cancel() {
	if f.OnCancel != nil {
		f.OnCancel()
	}
}

// ShortHelp returns key bindings 为 status bar.
func (f *QuestionForm) ShortHelp() []key.Binding {
	if f.isConfirmTab() {
		return f.confirmComp.ShortHelp()
	}
	bindings := []key.Binding{f.keyPrevTab, f.keyNextTab}
	if f.activeIdx < f.numQuestions {
		bindings = append(bindings, f.questions[f.activeIdx].ShortHelp()...)
	}
	return bindings
}

// Height returns total height using max tab height 所以
// switching tabs doesn't cause layout jumps.
func (f *QuestionForm) Height(width int) int {
	h := 0
	if f.showTabs {
		h = 4 // bordered tab row (top + label + bottom) + blank line
	}
	maxQ := 0
	for _, q := range f.questions {
		if qh := q.Height(width); qh > maxQ {
			maxQ = qh
		}
	}
	if f.confirmComp != nil {
		if ch := f.confirmComp.Height(width); ch > maxQ {
			maxQ = ch
		}
	}
	h += maxQ
	return h
}

// CollapsedHeight returns height 的 collapsed 摘要
// line shown 当 editor area 是 不 focused.
func (f *QuestionForm) CollapsedHeight() int { return 1 }

// ShouldCollapse reports whether form should use 它的 compact 摘要
// while the chat has focus.
func (f *QuestionForm) ShouldCollapse(width, terminalHeight int) bool {
	return terminalHeight > 0 && f.Height(width) > terminalHeight*2/5
}

// CollapsedHelp returns help description 为 restoring form.
func (f *QuestionForm) CollapsedHelp() string { return "answer questions" }

// DrawCollapsed renders compact one-line 摘要 的 form
// 当 用户 has tabbed away 到 chat. For multi-question
// batches it shows 活跃 question 文本 和 answered count;
// 为 single questions it shows just question 文本.
func (f *QuestionForm) DrawCollapsed(scr uv.Screen, area uv.Rectangle) {
	icon := f.Styles.Editor.PromptQuestionIconBlurred.Render()
	iconWidth := lipgloss.Width(icon)
	textStyle := f.Styles.Messages.AssistantInfoModel
	countStyle := f.Styles.Messages.AssistantInfoProvider
	lineStyle := f.Styles.Section.Line

	var plainText string
	var confirmRendered string
	if f.numQuestions > 1 {
		answered := 0
		for i := 0; i < f.numQuestions; i++ {
			if f.isAnswered(i) {
				answered++
			}
		}
		if f.isConfirmTab() && f.confirmComp != nil {
			plainText = f.confirmComp.Title
			confirmRendered = f.Styles.Editor.QuestionUnselected.Render(f.confirmComp.Title)
		} else if f.activeIdx < len(f.questions) {
			plainText = f.getQuestionText(f.activeIdx)
		}
		count := fmt.Sprintf("(%d/%d answered)", answered, f.numQuestions)
		plainLabel := plainText + " " + count
		textWidth := iconWidth + 1 + lipgloss.Width(plainLabel)
		remaining := area.Dx() - textWidth - 1

		var rendered string
		if confirmRendered != "" {
			rendered = fmt.Sprintf("%s%s %s", icon, confirmRendered, countStyle.Render(count))
		} else {
			rendered = fmt.Sprintf("%s%s %s", icon, textStyle.Render(plainText), countStyle.Render(count))
		}
		if remaining > 0 {
			rendered = rendered + " " + lineStyle.Render(strings.Repeat(styles.SectionSeparator, remaining))
		}
		drawStyledText(scr, area, rendered)
	} else if f.numQuestions == 1 {
		plainText = f.getQuestionText(0)
		textWidth := iconWidth + 1 + lipgloss.Width(plainText)
		remaining := area.Dx() - textWidth - 1
		rendered := fmt.Sprintf("%s%s", icon, textStyle.Render(plainText))
		if remaining > 0 {
			rendered = rendered + " " + lineStyle.Render(strings.Repeat(styles.SectionSeparator, remaining))
		}
		drawStyledText(scr, area, rendered)
	}
}

// getQuestionText returns question 文本 为 given index.
func (f *QuestionForm) getQuestionText(idx int) string {
	type hasRequest interface {
		GetRequest() question.Question
	}
	if idx < len(f.questions) {
		if hr, ok := f.questions[idx].(hasRequest); ok {
			return hr.GetRequest().Text
		}
	}
	if idx < len(f.labels) {
		return f.labels[idx]
	}
	return ""
}

// Draw renders tab bar 和 活跃 tab content. When
// showTabs 是 false (single question), renders content directly
// without tab chrome.
func (f *QuestionForm) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	contentY := area.Min.Y

	if f.showTabs {
		const tabPadX = 1
		tabHeight := 3

		// Compute display labels.
		labels := make([]string, len(f.labels))
		copy(labels, f.labels)

		// Truncate 如果 tabs exceed width. Distribute available
		// space fairly: short labels keep their natural width 和
		// deficit 是 shared proportionally among longer ones,
		// 用 remainder cells distributed left-到-right 所以
		// layout resizes smoothly pixel-通过-pixel.
		tabWidths := make([]int, len(labels))
		naturalWidths := make([]int, len(labels))
		totalWidth := 0
		for i, l := range labels {
			w := ansi.StringWidth(l) + tabPadX*2 + 2
			tabWidths[i] = w
			naturalWidths[i] = w
			totalWidth += w
		}
		avail := area.Dx()
		if totalWidth > avail && len(labels) > 0 {
			const minLabelW = 1
			minTabW := minLabelW + tabPadX*2 + 2
			n := len(labels)

			// Check 如果 there's enough room 到 show all tabs 用
			// 在 least useful label. If each tab can't fit 在
			// least 5 cells 的 label, switch 到 single-tab mode
			// 用 "N 的 M" 计数器.
			usefulMinTabW := 5 + tabPadX*2 + 2
			if avail/n < usefulMinTabW {
				// Single-tab mode: show 只 活跃 tab
				// label plus 计数器.
				counter := fmt.Sprintf("%d/%d", f.activeIdx+1, n)
				activeLabel := labels[f.activeIdx]
				combined := activeLabel + " · " + counter
				maxLabel := avail - tabPadX*2 - 2
				if maxLabel < 3 {
					maxLabel = 3
				}
				if ansi.StringWidth(combined) > maxLabel {
					// Truncate label 片段 到 fit.
					counterPart := " · " + counter
					labelBudget := maxLabel - ansi.StringWidth(counterPart)
					if labelBudget < 1 {
						labelBudget = 1
					}
					combined = ansi.Truncate(activeLabel, labelBudget, "…") + counterPart
				}
				for i := range labels {
					if i == f.activeIdx {
						labels[i] = combined
					} else {
						labels[i] = ""
					}
				}
				// Recalculate widths 为 single visible tab.
				totalWidth = 0
				for i := range labels {
					if labels[i] == "" {
						tabWidths[i] = 0
					} else {
						w := ansi.StringWidth(labels[i]) + tabPadX*2 + 2
						tabWidths[i] = w
						totalWidth += w
					}
				}
			} else {
				// Normal truncation: distribute space fairly.
				capped := make([]bool, n)
				for {
					freeCount := 0
					freeTotal := 0
					for i := range n {
						if capped[i] {
							continue
						}
						freeCount++
						freeTotal += naturalWidths[i]
					}
					if freeCount == 0 {
						break
					}
					budget := avail
					for i := range n {
						if capped[i] {
							budget -= tabWidths[i]
						}
					}
					share := budget / freeCount
					changed := false
					for i := range n {
						if !capped[i] && naturalWidths[i] <= share {
							capped[i] = true
							tabWidths[i] = naturalWidths[i]
							changed = true
						}
					}
					if !changed {
						for i := range n {
							if !capped[i] {
								tabWidths[i] = max(share, minTabW)
							}
						}
						remainder := budget - share*freeCount
						for i := range n {
							if remainder <= 0 {
								break
							}
							if !capped[i] && tabWidths[i] < naturalWidths[i] {
								tabWidths[i]++
								remainder--
							}
						}
						break
					}
				}

				// Apply truncation based 在 final widths.
				for i, l := range labels {
					labelAvail := max(tabWidths[i]-tabPadX*2-2, minLabelW)
					if ansi.StringWidth(l) > labelAvail {
						labels[i] = ansi.Truncate(l, labelAvail, "…")
					}
				}
			}
		}

		// Build tab layers 为 click hit detection.
		var layers []*lipgloss.Layer
		x := area.Min.X

		// Determine hovered tab via simple bounds check.
		hoveredTab := -1
		if f.hoverY >= area.Min.Y && f.hoverY < area.Min.Y+tabHeight {
			tx := area.Min.X
			for i := range labels {
				tw := tabWidths[i]
				if f.hoverX >= tx && f.hoverX < tx+tw {
					hoveredTab = i
					break
				}
				tx += tw
			}
		}

		firstVisible := -1
		for i := range labels {
			if tabWidths[i] > 0 {
				firstVisible = i
				break
			}
		}

		for i, label := range labels {
			// Skip hidden tabs (single-tab mode).
			if tabWidths[i] == 0 {
				continue
			}
			isActive := i == f.activeIdx
			isHovered := i == hoveredTab && !isActive
			labelWidth := ansi.StringWidth(label)
			tabWidth := tabWidths[i]

			tabArea := image.Rect(x, area.Min.Y, x+tabWidth, area.Min.Y+tabHeight)

			border := f.Styles.Tab.InactiveBorder
			textStyle := f.Styles.Tab.InactiveStyle
			if !f.focused {
				border = f.Styles.Tab.InactiveBorderBlurred
			}
			if isActive {
				border = f.Styles.Tab.ActiveBorder
				textStyle = f.Styles.Tab.ActiveStyle
				if !f.focused {
					border = f.Styles.Tab.ActiveBorderBlurred
				}
			} else if i < f.numQuestions && f.isAnswered(i) {
				textStyle = f.Styles.Tab.ActiveStyle
			}
			if isHovered {
				hovered := textStyle
				hovered.Attrs |= uv.AttrBold
				textStyle = hovered
			}

			if i == firstVisible {
				if isActive {
					border.BottomLeft = uv.Side{Content: "┘", Style: border.BottomLeft.Style}
				} else {
					border.BottomLeft = uv.Side{Content: "┴", Style: border.BottomLeft.Style}
				}
			}

			border.Draw(scr, tabArea)

			innerWidth := tabWidth - 2
			xOff := (innerWidth - labelWidth) / 2
			innerArea := image.Rect(
				tabArea.Min.X+1+xOff, tabArea.Min.Y+1,
				tabArea.Max.X-1, tabArea.Max.Y-1,
			)
			uv.NewStyledString(textStyle.Styled(label)).Draw(scr, innerArea)

			// 创建 invisible hit layer 为 这 tab.
			hitStr := strings.Repeat(strings.Repeat(" ", tabWidth)+"\n", tabHeight-1) + strings.Repeat(" ", tabWidth)
			layers = append(layers, lipgloss.NewLayer(hitStr).X(x).Y(area.Min.Y).ID(fmt.Sprintf("tab_%d", i)))

			x += tabWidth
		}

		f.compositor = lipgloss.NewCompositor(layers...)

		lineY := area.Min.Y + tabHeight - 1
		lineSide := f.Styles.Tab.InactiveBorder.Bottom
		if !f.focused {
			lineSide = f.Styles.Tab.InactiveBorderBlurred.Bottom
		}
		for lx := x; lx < area.Max.X; lx++ {
			c := uv.NewCell(scr.WidthMethod(), lineSide.Content)
			if c != nil {
				c.Style = lineSide.Style
			}
			scr.SetCell(lx, lineY, c)
		}

		contentY = area.Min.Y + tabHeight + 1
	} else {
		f.compositor = nil
	}

	contentArea := image.Rect(area.Min.X, contentY, area.Max.X, area.Max.Y)

	if f.isConfirmTab() {
		return f.confirmComp.Draw(scr, contentArea)
	}
	if f.activeIdx < f.numQuestions {
		cur := f.questions[f.activeIdx].Draw(scr, contentArea)
		if cur != nil {
			cur.Y += contentY - area.Min.Y
		}
		return cur
	}
	return nil
}

// HeightChanged reports whether any component's height changed.
func (f *QuestionForm) HeightChanged() bool {
	for _, q := range f.questions {
		if q.HeightChanged() {
			return true
		}
	}
	if f.confirmComp != nil && f.confirmComp.HeightChanged() {
		return true
	}
	return false
}

// SetFocused updates focus state 为 活跃 tab.
func (f *QuestionForm) SetFocused(focused bool) {
	f.focused = focused
	if f.isConfirmTab() {
		f.confirmComp.SetFocused(focused)
	} else if f.activeIdx < f.numQuestions {
		f.questions[f.activeIdx].SetFocused(focused)
	}
}

// SetHover implements MouseClickableEditor. Stores the hover
// position 和 propagates it 到 活跃 component.
func (f *QuestionForm) SetHover(x, y int) {
	f.hoverX = x
	f.hoverY = y
	if f.isConfirmTab() && f.confirmComp != nil {
		f.confirmComp.SetHover(x, y)
	} else if f.activeIdx < len(f.questions) {
		f.questions[f.activeIdx].SetHover(x, y)
	}
}

// HandlePaste implements PasteableEditor. Forwards paste events
// 到 活跃 question component 如果 it supports pasting.
func (f *QuestionForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if f.isConfirmTab() {
		return nil
	}
	if f.activeIdx < f.numQuestions {
		if p, ok := f.questions[f.activeIdx].(PasteableEditor); ok {
			return p.HandlePaste(msg)
		}
	}
	return nil
}

// HandleMouseClick implements MouseClickableEditor. 它 checks 如果
// click landed 在 tab 和 switches 到 it, 或 delegates 到
// 活跃 component 为 content-area clicks.
func (f *QuestionForm) HandleMouseClick(x, y int) (bool, bool) {
	// Check tabs first.
	if f.showTabs && f.compositor != nil {
		hit := f.compositor.Hit(x, y)
		if !hit.Empty() {
			var idx int
			if _, err := fmt.Sscanf(hit.ID(), "tab_%d", &idx); err == nil {
				if idx >= 0 && idx < len(f.labels) && idx != f.activeIdx {
					f.switchTab(idx)
				}
				return false, true
			}
		}
	}

	// Delegate 到 活跃 component.
	if f.isConfirmTab() && f.confirmComp != nil {
		return f.confirmComp.HandleMouseClick(x, y)
	}
	if f.activeIdx < len(f.questions) {
		done, handled := f.questions[f.activeIdx].HandleMouseClick(x, y)
		if handled {
			resp := f.questions[f.activeIdx].Response()
			f.answers[f.activeIdx] = &resp
			f.syncConfirmAnswers()
			if done {
				if f.activeIdx < len(f.labels)-1 {
					f.switchTab(f.activeIdx + 1)
					return false, true
				} else if !f.hasConfirm {
					f.submit()
					return true, true
				}
			}
			return false, true
		}
	}
	return false, false
}
