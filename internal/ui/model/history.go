package model

import (
	"context"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/message"
)

// promptHistoryLoadedMsg 是 sent 当 提示词 history 是 loaded.
type promptHistoryLoadedMsg struct {
	messages []string
}

// loadPromptHistory loads 用户 messages 为 history navigation. Both queries
// stop 在 200 most recent entries; recall 只 steps back one 在 time.
func (m *UI) loadPromptHistory() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		var messages []message.Message
		var err error

		if m.session != nil {
			messages, err = m.com.Workspace.ListUserMessages(ctx, m.session.ID)
		} else {
			messages, err = m.com.Workspace.ListAllUserMessages(ctx)
		}
		if err != nil {
			slog.Error("Failed to load prompt history", "error", err)
			return promptHistoryLoadedMsg{messages: nil}
		}

		texts := make([]string, 0, len(messages))
		for _, msg := range messages {
			if text := msg.Content().Text; text != "" {
				texts = append(texts, text)
			}
			for _, sc := range msg.ShellCommands() {
				texts = append(texts, "!"+sc.Command)
			}
		}
		return promptHistoryLoadedMsg{messages: texts}
	}
}

// handleHistoryUp handles up arrow 为 history navigation.
func (m *UI) handleHistoryUp(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Navigate 到 older history 条目 从 cursor position (0,0).
	if m.textarea.Length() == 0 || m.isAtEditorStart() {
		if m.historyPrev() {
			// we send 这 所以 那 textarea moves view 到 correct position
			// without 这 cursor will show up 在 wrong place.
			return m.updateTextareaWithPrevHeight(nil, prevHeight)
		}
	}

	// First move cursor 到 start before entering history.
	if m.textarea.Line() == 0 {
		m.textarea.CursorStart()
		return nil
	}

	// Let textarea handle normal cursor movement.
	return m.updateTextarea(msg)
}

// handleHistoryDown handles down arrow 为 history navigation.
func (m *UI) handleHistoryDown(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Navigate 到 newer history 条目 从 end 的 文本.
	if m.isAtEditorEnd() {
		if m.historyNext() {
			// we send 这 所以 那 textarea moves view 到 correct position
			// without 这 cursor will show up 在 wrong place.
			return m.updateTextareaWithPrevHeight(nil, prevHeight)
		}
	}

	// First move cursor 到 end before navigating history.
	if m.textarea.Line() == max(m.textarea.LineCount()-1, 0) {
		m.textarea.MoveToEnd()
		return m.updateTextarea(nil)
	}

	// Let textarea handle normal cursor movement.
	return m.updateTextarea(msg)
}

// handleHistoryEscape handles escape 为 exiting history navigation.
func (m *UI) handleHistoryEscape(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Return 到 current draft 当 browsing history.
	if m.promptHistory.index >= 0 {
		m.promptHistory.index = -1
		m.textarea.Reset()
		m.textarea.InsertString(m.promptHistory.draft)
		m.syncBangModeFromTextarea()
		return m.updateTextareaWithPrevHeight(nil, prevHeight)
	}

	// Let textarea handle escape normally.
	return m.updateTextarea(msg)
}

// updateHistoryDraft updates history state 当 文本 是 modified.
func (m *UI) updateHistoryDraft(oldValue string) {
	if m.textarea.Value() != oldValue {
		m.promptHistory.draft = m.textarea.Value()
		m.promptHistory.index = -1
	}
}

// syncBangModeFromTextarea engages 或 disengages bang mode based 在
// whether current textarea 值 starts 用 "!". "!" prefix
// 是 stripped 当 entering bang mode 和 re-added 当 leaving it 所以
// visible 文本 always reflects correct state.
func (m *UI) syncBangModeFromTextarea() {
	val := m.textarea.Value()
	hasBang := strings.HasPrefix(val, "!")
	if hasBang {
		if !m.bangMode {
			m.bangMode = true
			m.bangWasEmpty = false
		}
		m.textarea.SetValue(strings.TrimPrefix(val, "!"))
		m.textarea.MoveToBegin()
	} else if m.bangMode {
		m.bangMode = false
		m.bangWasEmpty = false
	}
	m.setEditorPrompt(m.permModeCached())
}

// historyPrev changes 文本 area content 到 previous 消息 在 history
// it returns false 如果 it could 不 find previous 消息.
func (m *UI) historyPrev() bool {
	if len(m.promptHistory.messages) == 0 {
		return false
	}
	if m.promptHistory.index == -1 {
		m.promptHistory.draft = m.textarea.Value()
	}
	nextIndex := m.promptHistory.index + 1
	if nextIndex >= len(m.promptHistory.messages) {
		return false
	}
	m.promptHistory.index = nextIndex
	m.textarea.Reset()
	m.textarea.InsertString(m.promptHistory.messages[nextIndex])
	m.textarea.MoveToBegin()
	m.syncBangModeFromTextarea()
	return true
}

// historyNext changes 文本 area content 到 next 消息 在 history
// it returns false 如果 it could 不 find next 消息.
func (m *UI) historyNext() bool {
	if m.promptHistory.index < 0 {
		return false
	}
	nextIndex := m.promptHistory.index - 1
	if nextIndex < 0 {
		m.promptHistory.index = -1
		m.textarea.Reset()
		m.textarea.InsertString(m.promptHistory.draft)
		m.syncBangModeFromTextarea()
		return true
	}
	m.promptHistory.index = nextIndex
	m.textarea.Reset()
	m.textarea.InsertString(m.promptHistory.messages[nextIndex])
	m.syncBangModeFromTextarea()
	return true
}

// historyReset resets history, 但 does 不 clear 消息
// it just sets current draft 到 empty 和 position 在 history.
func (m *UI) historyReset() {
	m.promptHistory.index = -1
	m.promptHistory.draft = ""
}

// isAtEditorStart returns true 如果 we 是 在 0 line 和 0 col 在 textarea.
func (m *UI) isAtEditorStart() bool {
	return m.textarea.Line() == 0 && m.textarea.LineInfo().ColumnOffset == 0
}

// isAtEditorEnd returns true 如果 we 是 在 last line 和 last column 在 textarea.
func (m *UI) isAtEditorEnd() bool {
	lineCount := m.textarea.LineCount()
	if lineCount == 0 {
		return true
	}
	if m.textarea.Line() != lineCount-1 {
		return false
	}
	info := m.textarea.LineInfo()
	return info.CharOffset >= info.CharWidth-1 || info.CharWidth == 0
}
