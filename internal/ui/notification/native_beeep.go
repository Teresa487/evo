//go:build !solaris && !illumos

package notification

import "github.com/gen2brain/beeep"

// NativeSupported reports whether native OS notifications 是 available 在
// 这 platform. 它 是 false 在 illumos/solaris, where beeep's dbus
// dependency does 不 build. Callers use it 到 hide native option 和
// pick 终态-based backend instead.
const NativeSupported = true

// defaultNotifyFunc delivers notifications through beeep 在 platforms where
// 它的 dependencies build.
var defaultNotifyFunc = beeep.Notify

func init() {
	beeep.AppName = "Crush"
}
