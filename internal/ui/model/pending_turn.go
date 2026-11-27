package model

import (
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/common"
)

// pendingTurn 是 turn 这 客户端 sent 那 has 不 yet ended. 它 lives
// until 运行's RunComplete (或 failure 到 start it) arrives.
type pendingTurn struct {
	// sessionID 是 会话 running turn; empty 当 none.
	sessionID string
	// echo shows 提示词 until 存储 用户消息 replaces it.
	echo chat.MessageItem
	// after 是 last 提示词 存储 before 这 turn's; empty 如果 none.
	after string
	// spinner 是 placeholder, kept 所以 moving it keeps 它的 animation.
	spinner chat.MessageItem
	// handedOff records 那 助手消息 now carries spinner.
	handedOff bool
}

// showPendingTurn shows 提示词 和 turn spinner 为 sessionID,
// ahead 的 agent storing 消息 和 publishing it back. Reports
// whether a placeholder was added.
func (m *UI) showPendingTurn(sessionID, prompt string, attachments []message.Attachment) bool {
	m.pending = pendingTurn{sessionID: sessionID, after: m.chat.LastPromptID()}
	if prompt != "" || len(attachments) > 0 {
		m.pending.echo = chat.NewPendingUserItem(m.com.Styles, prompt, attachments)
	}
	return m.syncPendingItem()
}

// handOffPendingTurn drops placeholder 为 助手消息 那
// now carries the spinner, without ending the turn.
func (m *UI) handOffPendingTurn() {
	m.pending.handedOff = true
	m.pending.spinner = nil
	m.syncPendingItem()
}

// endPendingTurn retires pending items, clock 和 busy probe 为
// sessionID, 如果 那 是 会话 currently running turn.
func (m *UI) endPendingTurn(sessionID string) {
	common.StopTurn(sessionID)
	// 一个 运行 ending 是 busy edge: drop probe 和 any still 在 flight.
	m.invalidateBusyCaches()
	if m.probedSession == sessionID {
		m.probedSessionBusy = false
	}
	if m.pending.sessionID == sessionID {
		m.pending = pendingTurn{}
		m.syncPendingItem()
	}
}

// turnInFlight reports whether 提示词 sent 到 sessionID now would 队列
// behind a running turn rather than start one.
func (m *UI) turnInFlight(sessionID string) bool {
	return m.pending.sessionID == sessionID ||
		m.probedSession == sessionID && m.probedSessionBusy
}

// syncPendingItem makes chat match pending turn, adding 或 removing
// 提示词 echo 和 placeholder spinner 作为 needed, 和 reports whether
// it added spinner. 它 是 idempotent, 所以 callers may 运行 it 在 any 路径
// 那 could have changed either pending turn 或 items 在 view.
func (m *UI) syncPendingItem() bool {
	if m.pending.sessionID == "" || m.pending.sessionID != m.currentSessionID() {
		m.chat.RemoveMessage(chat.PendingUserID)
		m.chat.RemoveMessage(chat.PendingAssistantID)
		return false
	}
	// echo stands 在 为 提示词 until newer 存储 one shows up.
	if m.pending.echo != nil && m.chat.LastPromptID() != m.pending.after {
		m.pending.echo = nil
		m.chat.RemoveMessage(chat.PendingUserID)
	}
	if m.pending.echo != nil && m.chat.MessageItem(chat.PendingUserID) == nil {
		m.chat.AppendMessages(m.pending.echo)
	}
	if m.pending.handedOff {
		m.chat.RemoveMessage(chat.PendingAssistantID)
		return false
	}
	if m.chat.MessageItem(chat.PendingAssistantID) != nil {
		return false
	}
	if m.pending.spinner == nil {
		m.pending.spinner = chat.NewPendingAssistantItem(m.com.Styles, m.pending.sessionID)
	}
	m.chat.AppendMessages(m.pending.spinner)
	return true
}
