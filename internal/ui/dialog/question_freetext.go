package dialog

import (
	"image"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// FreeText 是 open-ended 文本 input component 为 questions
// 那 need narrative answer rather than selection.
type FreeText struct {
	Styles  *styles.Styles
	Request question.Question
	focused bool

	editor       textarea.Model
	scrollOffset int  // lines scrolled past the top of the textarea viewport
	wheelActive  bool // wheel-scroll mode: skip cursor-follow until next key press
	keyEnter     key.Binding
	keyNewline   key.Binding
	keyClose     key.Binding

	lastResponse question.Answer
	lastWidth    int
}

// freeTextMinEditorHeight 和 freeTextMaxEditorHeight bound
// answer textarea. 它 starts 在 minimum 和, 当 form has
// taller sibling tabs, grows 在 draw time 到 fill shared form
// height, capped 在 maximum.
const (
	freeTextMinEditorHeight = 3
	freeTextMaxEditorHeight = 6
)

// NewFreeText creates new free-文本 question component.
func NewFreeText(sty *styles.Styles, req question.Question) *FreeText {
	ta := newQuestionTextarea(sty, "Type your answer...", 1000)
	ta.DynamicHeight = false
	ta.MinHeight = freeTextMinEditorHeight
	ta.MaxHeight = freeTextMaxEditorHeight
	ta.SetHeight(freeTextMinEditorHeight)

	return &FreeText{
		Styles:     sty,
		Request:    req,
		editor:     ta,
		keyEnter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit")),
		keyNewline: key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"), key.WithHelp("shift+enter", "newline")),
		keyClose:   CloseKey,
	}
}

// HandleKey processes key press. Returns true 当 用户 has
// submitted 或 dismissed question.
func (d *FreeText) HandleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	d.wheelActive = false
	switch {
	case key.Matches(msg, d.keyClose):
		d.answer(question.Answer{QuestionID: d.Request.ID})
		return true, nil
	case key.Matches(msg, d.keyEnter):
		val := strings.TrimSpace(d.editor.Value())
		if val != "" {
			d.answer(question.Answer{
				QuestionID: d.Request.ID,
				FillInText: val,
			})
			return true, nil
		}
		return false, nil
	case key.Matches(msg, d.keyNewline):
		d.editor.InsertRune('\n')
		return false, nil
	default:
		var cmd tea.Cmd
		d.editor, cmd = d.editor.Update(msg)
		return false, cmd
	}
}

func (d *FreeText) answer(resp question.Answer) {
	d.lastResponse = resp
}

// 响应 returns current answer, including any unsaved
// editor content 所以 那 tabbing away preserves typed 文本.
func (d *FreeText) Response() question.Answer {
	if val := strings.TrimSpace(d.editor.Value()); val != "" {
		return question.Answer{QuestionID: d.Request.ID, FillInText: val}
	}
	return d.lastResponse
}

// GetRequest returns underlying question 请求.
func (d *FreeText) GetRequest() question.Question { return d.Request }

// ShortHelp returns key bindings 为 status bar.
func (d *FreeText) ShortHelp() []key.Binding {
	return []key.Binding{d.keyEnter, d.keyNewline, d.keyClose}
}

// Height returns visual height 在 默认 max width.
func (d *FreeText) Height(width int) int {
	w := width
	if w <= 0 {
		w = d.lastWidth
	}
	if w <= 0 {
		w = choiceListMaxWidth
	}
	iconPrompt := questionIconPrompt(d.Styles, d.focused)
	h := sectionHeight(d.Request.Text, w-lipgloss.Width(iconPrompt)) // question
	h++                                                              // blank
	if d.Request.Description != "" {
		r := common.MarkdownRenderer(d.Styles, w)
		mu := common.LockMarkdownRenderer(r)
		mu.Lock()
		out, err := r.Render(d.Request.Description)
		mu.Unlock()
		if err == nil {
			out = strings.TrimSuffix(out, "\n")
			h += strings.Count(out, "\n") + 1
		} else {
			h += sectionHeight(d.Request.Description, w)
		}
		h++ // blank
	}
	h += freeTextMinEditorHeight // textarea (minimum; grows to fill at draw time)
	h++                          // trailing blank for bottom padding
	return h
}

// Draw renders free-文本 question directly 到 screen.
// Returns cursor position, 或 nil.
//
// question header, description, 和 textarea share single
// scroll 缓冲 所以 标题 scrolls away 作为 answer grows,
// matching choice 和 confirm components. 这 keeps
// textarea 从 being permanently squeezed 通过 fixed header rows.
func (d *FreeText) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	d.lastWidth = area.Dx()
	viewport := area.Dy()

	barActive := d.Styles.Editor.QuestionCursorBar.Render("┃ ")
	const barInactive = "  "
	bar := barInactive
	if d.focused {
		bar = barActive
	}
	prefixWidth := lipgloss.Width(bar)
	iconPrompt := questionIconPrompt(d.Styles, d.focused)
	iconWidth := lipgloss.Width(iconPrompt)

	// ftLine 是 single 缓冲 行. cursorX >= 0 marks 行
	// carrying textarea cursor 和 它的 column (incl. prefix).
	type ftLine struct {
		text    string
		cursorX int
	}

	// build 渲染 full content (请求头, description, textarea)
	// into flat line 缓冲 在 given content width. Returns
	// 缓冲 和 行 index 的 cursor (-1 如果 none).
	build := func(contentWidth int) ([]ftLine, int) {
		var lines []ftLine
		cursorRow := -1

		header := iconPrompt + d.Styles.Editor.QuestionUnselected.Render(
			ansi.Wrap(d.Request.Text, contentWidth-iconWidth, ""),
		)
		for _, l := range strings.Split(header, "\n") {
			lines = append(lines, ftLine{text: l, cursorX: -1})
		}
		lines = append(lines, ftLine{cursorX: -1}) // blank

		if d.Request.Description != "" {
			r := common.MarkdownRenderer(d.Styles, contentWidth)
			mu := common.LockMarkdownRenderer(r)
			mu.Lock()
			desc, err := r.Render(d.Request.Description)
			mu.Unlock()
			if err != nil {
				desc = d.Request.Description
			}
			desc = strings.TrimSuffix(desc, "\n")
			for _, l := range strings.Split(desc, "\n") {
				lines = append(lines, ftLine{text: l, cursorX: -1})
			}
			lines = append(lines, ftLine{cursorX: -1}) // blank
		}

		// Grow textarea 到 fill form height, bounded 通过
		// min 和 max editor heights.
		headerLines := len(lines)
		fill := viewport - headerLines - 1 // -1 for trailing padding
		available := min(freeTextMaxEditorHeight, max(freeTextMinEditorHeight, fill))
		d.editor.SetHeight(available)
		d.editor.SetWidth(contentWidth - 2 - prefixWidth)
		tc := d.editor.Cursor()
		for j, ln := range strings.Split(d.editor.View(), "\n") {
			text := bar + ln
			cursorX := -1
			if tc != nil && tc.Y == j {
				cursorRow = len(lines)
				cursorX = tc.X + prefixWidth
			}
			lines = append(lines, ftLine{text: text, cursorX: cursorX})
		}
		lines = append(lines, ftLine{cursorX: -1}) // trailing bottom padding, matches Height()
		return lines, cursorRow
	}

	// Build 在 full width; reserve scrollbar column 和 rebuild
	// 只 如果 content overflows viewport.
	contentWidth := area.Dx()
	lines, cursorRow := build(contentWidth)
	overflow := viewport > 0 && len(lines) > viewport
	if overflow {
		contentWidth--
		lines, cursorRow = build(contentWidth)
	}

	// Clamp scroll, then keep cursor 行 visible unless
	// 用户 是 wheel-scrolling.
	maxScroll := max(0, len(lines)-viewport)
	d.scrollOffset = min(max(0, d.scrollOffset), maxScroll)
	if !d.wheelActive && cursorRow >= 0 {
		if cursorRow < d.scrollOffset {
			d.scrollOffset = cursorRow
		} else if cursorRow >= d.scrollOffset+viewport {
			d.scrollOffset = cursorRow - viewport + 1
		}
		d.scrollOffset = min(max(0, d.scrollOffset), maxScroll)
	}

	// Blit visible 窗口 和 place cursor. cursor 是
	// returned relative 到 area's top-left, matching
	// InlineEditor contract.
	var cur *tea.Cursor
	baseCursor := d.editor.Cursor()
	for screenRow := range viewport {
		idx := d.scrollOffset + screenRow
		if idx >= len(lines) {
			break
		}
		ln := lines[idx]
		y := area.Min.Y + screenRow
		drawStyledText(scr, image.Rect(area.Min.X, y, area.Min.X+contentWidth, y+1), ln.text)
		if ln.cursorX >= 0 && ln.cursorX < contentWidth && baseCursor != nil {
			c := *baseCursor
			c.X = ln.cursorX
			c.Y = screenRow
			cur = &c
		}
	}

	// Scrollbar.
	if overflow {
		sb := common.Scrollbar(d.Styles, viewport, len(lines), viewport, d.scrollOffset)
		if sb != "" {
			x := area.Max.X - 1
			uv.NewStyledString(sb).Draw(scr, image.Rect(x, area.Min.Y, x+1, area.Min.Y+viewport))
		}
	}

	return cur
}

// HeightChanged reports whether the textarea height changed.
func (d *FreeText) HeightChanged() bool { return false }

// SetFocused updates focus state.
func (d *FreeText) SetFocused(focused bool) {
	d.focused = focused
	if focused {
		d.editor.Focus()
	} else {
		d.editor.Blur()
	}
}

// SetHover 是 no-op 为 free 文本 questions.
func (d *FreeText) SetHover(x, y int) {}

// HandleMouseClick 是 no-op 为 free 文本 questions.
func (d *FreeText) HandleMouseClick(x, y int) (bool, bool) { return false, false }

// HandleWheel scrolls the textarea viewport.
func (d *FreeText) HandleWheel(deltaX, deltaY float64) {
	if deltaY != 0 {
		d.scrollOffset += int(deltaY)
		d.wheelActive = true
	}
}

// HandlePaste forwards paste events 到 editor textarea.
func (d *FreeText) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	var cmd tea.Cmd
	d.editor, cmd = d.editor.Update(msg)
	return cmd
}
