package notification

import (
	"log/slog"

	tea "charm.land/bubbletea/v2"
)

// BellBackend sends notifications 通过 triggering 终态 bell. 这 是
// most basic 通知 mechanism 和 works 在 virtually all terminals, 但
// provides no visual 消息 — just audible 或 visual alert depending 在
// 终态 configuration.
type BellBackend struct{}

// NewBellBackend creates new bell 通知 backend.
func NewBellBackend() *BellBackend {
	return &BellBackend{}
}

// Send returns [tea.Cmd] 那 triggers 终态 bell character (\x07).
// 终态 will emit audible beep 或 visual flash based 在 用户
// configuration. No 消息 文本 是 displayed.
func (b *BellBackend) Send(n Notification) tea.Cmd {
	slog.Debug("Sending bell notification", "title", n.Title, "message", n.Message)

	return tea.Raw("\x07")
}
