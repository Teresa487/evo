package dialog

import (
	"image"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
)

// newQuestionTextarea creates configured textarea 为 question
// input. All question textareas share the same base configuration;
// 只 placeholder 和 char limit vary.
func newQuestionTextarea(sty *styles.Styles, placeholder string, charLimit int) textarea.Model {
	ta := textarea.New()
	ta.SetStyles(questionTextareaStyles(sty))
	ta.Placeholder = placeholder
	ta.ShowLineNumbers = false
	ta.CharLimit = charLimit
	ta.MaxWidth = choiceListMaxWidth
	ta.SetVirtualCursor(false)
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 3
	ta.SetHeight(1)
	ta.SetPromptFunc(0, func(textarea.PromptInfo) string { return "" })
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithDisabled())
	ta.Blur()
	return ta
}

// questionTextareaStyles returns styles 的 question textarea.
func questionTextareaStyles(sty *styles.Styles) textarea.Styles {
	taStyles := sty.Editor.Textarea
	taStyles.Cursor.Color = sty.Editor.QuestionCursorBar.GetForeground()
	return taStyles
}

// questionEditor owns fill-在 textarea, note editor, 和 备注
// map shared across all question component types. Components embed
// 这 struct 和 调用 它的 methods instead 的 reimplementing editor
// logic.
type questionEditor struct {
	Styles *styles.Styles

	fillIn        textarea.Model
	noteEditor    textarea.Model
	activeNoteKey string // non-empty when a note editor is open
	notes         map[string]string

	keyNote key.Binding
	navUp   key.Binding
	navDown key.Binding
}

// newQuestionEditor creates questionEditor 用 configured
// fill-在 和 note textareas.
func newQuestionEditor(sty *styles.Styles) questionEditor {
	return questionEditor{
		Styles:     sty,
		fillIn:     newQuestionTextarea(sty, "Something else?", 500),
		noteEditor: newQuestionTextarea(sty, "Add a note...", 300),
		notes:      make(map[string]string),
		keyNote:    key.NewBinding(key.WithKeys("alt+n"), key.WithHelp("alt+n", "note")),
		navUp:      key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		navDown:    key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
	}
}

// openNote opens note editor 为 given key, pre-populating
// it 用 any existing note 文本.
func (e *questionEditor) openNote(noteKey string) tea.Cmd {
	e.activeNoteKey = noteKey
	if existing, ok := e.notes[noteKey]; ok {
		e.noteEditor.SetValue(existing)
	} else {
		e.noteEditor.Reset()
	}
	return e.noteEditor.Focus()
}

// closeNote saves current note 文本 和 closes editor.
func (e *questionEditor) closeNote(noteKey string) {
	e.activeNoteKey = ""
	val := strings.TrimSpace(e.noteEditor.Value())
	if val != "" {
		e.notes[noteKey] = val
	} else {
		delete(e.notes, noteKey)
	}
	e.noteEditor.Blur()
}

// handleNoteKey processes keys 当 note editor 是 focused.
// Returns (cmd, handled). When handled 是 true caller should
// 不 process key further. onClose 是 called 为 close
// key 所以 caller can control what happens after closing.
func (e *questionEditor) handleNoteKey(msg tea.KeyPressMsg, closeKey key.Binding, onClose func()) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, closeKey):
		onClose()
		return nil, true
	case key.Matches(msg, e.navUp), key.Matches(msg, e.navDown):
		onClose()
		return nil, false
	default:
		if key.Matches(msg, key.NewBinding(key.WithKeys("enter"))) {
			onClose()
			return nil, true
		}
		var cmd tea.Cmd
		e.noteEditor, cmd = e.noteEditor.Update(msg)
		return cmd, true
	}
}

// handlePaste forwards paste 消息 到 currently focused
// textarea (note editor 或 fill-在). Returns nil 如果 no textarea
// 是 focused.
func (e *questionEditor) handlePaste(msg tea.PasteMsg) tea.Cmd {
	if e.activeNoteKey != "" && e.noteEditor.Focused() {
		var cmd tea.Cmd
		e.noteEditor, cmd = e.noteEditor.Update(msg)
		return cmd
	}
	if e.fillIn.Focused() {
		var cmd tea.Cmd
		e.fillIn, cmd = e.fillIn.Update(msg)
		return cmd
	}
	return nil
}

// drawFillIn appends fill-在 rows 到 lines. When focused, renders
// live textarea; otherwise shows saved 文本 或 placeholder.
// styleFilled controls whether non-empty fill-在 文本 gets
// selected (pink) style. Pass true 为 single-choice where
// fill-在 IS answer; false 为 multi-choice where it's supplementary.
func (e *questionEditor) drawFillIn(lines *[]contentLine, innerWidth int, bar, fillPrefix string, isActive bool, styleFilled bool) {
	bodyStyle := e.Styles.Editor.QuestionBody
	prefixWidth := lipgloss.Width(fillPrefix)

	if isActive && e.fillIn.Focused() {
		e.fillIn.SetWidth(innerWidth - 2 - prefixWidth)
		indent := strings.Repeat(" ", prefixWidth)
		for j, tl := range strings.Split(e.fillIn.View(), "\n") {
			// 提示词 leads 只 first 行, 但 bar runs
			// whole height: typed answer 那 spans lines 是 one item,
			// 和 gutter 那 stops partway reads 作为 several.
			text := bar + fillPrefix + tl
			if j > 0 {
				text = bar + indent + tl
			}
			*lines = append(*lines, contentLine{text: text, fillInRow: j == 0, cursorItem: true, choiceIdx: -1})
		}
		return
	}

	val := strings.TrimSpace(e.fillIn.Value())
	if val != "" {
		rendered := e.Styles.Editor.QuestionUnselected.Render(val)
		if styleFilled {
			rendered = e.Styles.Editor.QuestionSelected.Render(val)
		}
		*lines = append(*lines, contentLine{text: bar + fillPrefix + rendered, cursorItem: isActive, choiceIdx: -1})
		return
	}
	*lines = append(*lines, contentLine{text: bar + fillPrefix + bodyStyle.Render("Something else?"), cursorItem: isActive, choiceIdx: -1})
}

// drawNote appends note rows 到 lines 为 given key. When
// note editor 是 活跃, renders live textarea; otherwise shows
// saved note 文本 或 nothing.
func (e *questionEditor) drawNote(lines *[]contentLine, innerWidth int, bar, noteKey string, isActive bool) {
	noteStyle := e.Styles.Editor.QuestionNote
	isEditing := e.activeNoteKey == noteKey && e.noteEditor.Focused()
	const notePrefix = "> "

	if isEditing && e.noteEditor.Focused() {
		prefixWidth := lipgloss.Width(notePrefix)
		e.noteEditor.SetWidth(innerWidth - 2 - prefixWidth)
		indent := strings.Repeat(" ", prefixWidth)
		for j, tl := range strings.Split(e.noteEditor.View(), "\n") {
			// As 用 fill-在: 提示词 在 first 行, bar 在 all
			// 的 them.
			text := bar + notePrefix + tl
			if j > 0 {
				text = bar + indent + tl
			}
			*lines = append(*lines, contentLine{text: text, noteRow: j == 0, cursorItem: true, choiceIdx: -1})
		}
		return
	}

	if saved, ok := e.notes[noteKey]; ok && saved != "" {
		dimmed := noteStyle.Render(saved)
		for _, ln := range strings.Split(dimmed, "\n") {
			*lines = append(*lines, contentLine{text: bar + notePrefix + ln, cursorItem: isActive, choiceIdx: -1})
		}
	}
}

// fillInCursor returns hardware cursor position 为 fill-在
// textarea 当 it's focused. areaMinX 是 left edge 的
// content area; prefixWidth 是 visual width 的 "> " 提示词.
func (e *questionEditor) fillInCursor(screenRow, areaMinX, prefixWidth int) *tea.Cursor {
	if !e.fillIn.Focused() {
		return nil
	}
	tc := e.fillIn.Cursor()
	if tc == nil {
		return nil
	}
	tc.X += areaMinX + 1 + prefixWidth
	tc.Y += screenRow
	return tc
}

// noteCursor returns hardware cursor position 为 note
// editor 当 it's focused.
func (e *questionEditor) noteCursor(screenRow, areaMinX, prefixWidth int) *tea.Cursor {
	if !e.noteEditor.Focused() {
		return nil
	}
	tc := e.noteEditor.Cursor()
	if tc == nil {
		return nil
	}
	tc.X += areaMinX + 1 + prefixWidth
	tc.Y += screenRow
	return tc
}

// drawStandaloneNote draws note editor 或 saved note directly
// onto screen (不 via line list). Used 通过 YesNo which doesn't
// use line-list 模型. Returns cursor 或 nil.
func (e *questionEditor) drawStandaloneNote(scr uv.Screen, area uv.Rectangle, y int, noteKey string) (*tea.Cursor, int) {
	const notePrefix = "> "

	if e.activeNoteKey != "" && e.noteEditor.Focused() {
		y++
		prefixWidth := lipgloss.Width(notePrefix)
		e.noteEditor.SetWidth(area.Dx() - 2 - prefixWidth)
		noteView := e.noteEditor.View()
		var cur *tea.Cursor
		for j, ln := range strings.Split(noteView, "\n") {
			text := notePrefix + ln
			if j > 0 {
				text = strings.Repeat(" ", prefixWidth) + ln
			}
			lines := drawStyledText(scr, image.Rect(area.Min.X, y, area.Max.X, y+1), text)
			if j == 0 {
				if tc := e.noteEditor.Cursor(); tc != nil {
					tc.X += prefixWidth
					tc.Y += y - area.Min.Y
					cur = tc
				}
			}
			y += lines
		}
		return cur, y
	}

	if saved, ok := e.notes[noteKey]; ok && saved != "" {
		y++
		noteStyle := e.Styles.Editor.QuestionNote
		drawStyledText(scr, image.Rect(area.Min.X, y, area.Max.X, y+1), notePrefix+noteStyle.Render(saved))
		y++
	}

	return nil, y
}
