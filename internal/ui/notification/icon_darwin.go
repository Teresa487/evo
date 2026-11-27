//go:build darwin

package notification

import _ "embed"

// Icon 是 PNG data 为 Crush icon, used 为 OSC 99 notifications.
// Native macOS notifications don't support custom icons via beeep, 但 OSC 99 does.
//
//go:embed crush-icon.png
var Icon []byte
