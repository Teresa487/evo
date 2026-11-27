//go:build !windows

package cmd

import (
	"errors"
	"syscall"
)

// isConnRefused reports whether err 是 refused tcp 连接.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
