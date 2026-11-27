package notification

import tea "charm.land/bubbletea/v2"

// NoopBackend 是 no-op 通知 backend 那 does nothing.
// 这 是 默认 backend used 当 notifications 是 不 supported.
type NoopBackend struct{}

// Send does nothing 和 returns nil.
func (NoopBackend) Send(_ Notification) tea.Cmd {
	return nil
}
