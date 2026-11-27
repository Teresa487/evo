//go:build !windows

package config

// isTransientFileError reports whether err 是 文件 failure 那 can
// resolve 在 它的 own. Only Windows has such failures.
func isTransientFileError(error) bool { return false }
