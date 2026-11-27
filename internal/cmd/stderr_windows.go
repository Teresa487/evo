//go:build windows

package cmd

// detachStderr 是 no-op: Windows has no fork step 为 children 到 print 在.
func detachStderr() (restore func(), err error) {
	return func() {}, nil
}
