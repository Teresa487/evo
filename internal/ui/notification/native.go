package notification

import (
	"log/slog"

	tea "charm.land/bubbletea/v2"
)

// NativeBackend sends desktop notifications using native OS 通知
// system. actual delivery function 是 supplied per-platform via
// defaultNotifyFunc; 在 illumos/solaris (where beeep's dbus dependency does
// 不 build) it 是 no-op. Selection logic avoids 这 backend there 和
// uses 终态-based backend instead, 所以 这 是 只 safety net. See
// NativeSupported.
type NativeBackend struct {
	// icon 是 通知 icon data (PNG bytes).
	icon []byte
	// notifyFunc 是 function used 到 send notifications (swappable 为 testing).
	notifyFunc func(title, message string, icon any) error
}

// NewNativeBackend creates new native 通知 backend.
func NewNativeBackend(icon []byte) *NativeBackend {
	return &NativeBackend{
		icon:       icon,
		notifyFunc: defaultNotifyFunc,
	}
}

// Send returns command 那 sends desktop 通知 using native
// OS 通知 system.
func (b *NativeBackend) Send(n Notification) tea.Cmd {
	return func() tea.Msg {
		slog.Debug("Sending native notification", "title", n.Title, "message", n.Message)

		if err := b.notifyFunc(n.Title, n.Message, b.icon); err != nil {
			slog.Error("Failed to send notification", "error", err)
		} else {
			slog.Debug("Notification sent successfully")
		}

		return nil
	}
}

// SetNotifyFunc allows replacing 通知 function 为 testing.
func (b *NativeBackend) SetNotifyFunc(fn func(title, message string, icon any) error) {
	b.notifyFunc = fn
}

// ResetNotifyFunc resets 通知 function 到 默认.
func (b *NativeBackend) ResetNotifyFunc() {
	b.notifyFunc = defaultNotifyFunc
}
