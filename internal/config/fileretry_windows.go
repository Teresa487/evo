//go:build windows

package config

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isTransientFileError reports whether err 是 Windows 文件 failure
// 那 clears once another handle ( concurrent reader 或 writer,
// antivirus, 或 search indexer) briefly open 在 文件 closes.
func isTransientFileError(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
