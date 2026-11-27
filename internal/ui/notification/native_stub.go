//go:build solaris || illumos

package notification

// NativeSupported reports whether native OS notifications 是 available 在
// 这 platform. 它 是 false 在 illumos/solaris: beeep pulls 在
// github.com/godbus/dbus/v5, whose 连接 code 是 excluded 在 solaris
// build tag (which illumos also satisfies) 和 has no replacement, 所以 it fails
// 到 compile. OSC 和 bell backends still work, 所以 callers hide native
// option 和 fall back 到 终态-based backend.
const NativeSupported = false

// defaultNotifyFunc 是 no-op fallback 所以 NewNativeBackend still compiles 如果
// something forces native backend 在 unsupported platform. Selection
// logic avoids native backend here, 所以 这 是 只 safety net.
var defaultNotifyFunc = func(title, message string, icon any) error { return nil }
