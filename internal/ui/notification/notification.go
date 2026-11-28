// Package 通知 provides desktop 通知 support 为 UI.
//
// 这 package supports multiple 通知 backends:
//
//   - NativeBackend: Uses native OS 通知 system (macOS, Windows, Linux)
//   - OSCBackend: Uses OSC escape sequences 用 automatic protocol detection.
//     Prefers OSC 99 (modern standard 用 rich notifications) 如果
//     supported, falling back 到 OSC 777 (urxvt extension, widely
//     supported). Used 为 SSH sessions.
//   - BellBackend: Triggers 终态 bell character (\x07), causing audible
//     beep 或 visual flash. Works 在 virtually all terminals 但 provides
//     no 消息 文本.
//   - NoopBackend: 一个 no-op backend 那 silently discards notifications.
//     Used 当 notifications 是 disabled 或 no suitable backend 是
//     available.
//
// Backend selection 是 based 在 终态 capabilities, environment, 和 用户
// 配置:
//
//   - Users can explicitly set notifications 在 crush.json
//     (auto/native/osc/bell/disabled)
//   - Auto mode: SSH sessions use OSC backend (auto-detects OSC 99 vs 777)
//   - Auto mode: Local sessions use native OS notifications
//   - If focus events 是 不 supported 在 local sessions, notifications 是
//     disabled (NoopBackend)
package notification

import tea "charm.land/bubbletea/v2"

// 通知 represents desktop 通知 请求.
type Notification struct {
	Title   string
	Message string
}

// Backend defines interface 为 sending desktop notifications.
// Implementations return tea.Cmd 那 performs 通知, allowing
// each backend 到 choose between synchronous (native OS) 和 asynchronous
// (终态 escape sequences) delivery. Policy decisions (配置 checks,
// focus state) 是 handled 通过 caller.
type Backend interface {
	Send(n Notification) tea.Cmd
}
