//go:build openbsd

package pinentry

import "errors"

// injectCtrlLRedraw 是 no-op 在 OpenBSD, which removed TIOCSTI
// ioctl; hint printed 在 终态 remains 作为 fallback.
func injectCtrlLRedraw() {}

// InjectCtrlL always fails 在 OpenBSD, which has no TIOCSTI.
func InjectCtrlL() error {
	return errors.New("TIOCSTI is not available on OpenBSD")
}
