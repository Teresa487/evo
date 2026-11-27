//go:build windows

package cmd

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isConnRefused reports whether err 是 refused tcp 连接. Windows
// reports WSAECONNREFUSED, which syscall.ECONNREFUSED never matches there.
func isConnRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED)
}
