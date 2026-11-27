//go:build windows

package fsext

import (
	"cmp"
	"os"
	"path/filepath"
)

// WindowsWorkingDirDrive returns drive letter 的 current working
// 目录, e.g. "C:".
// Falls back 到 system drive 如果 current working 目录 cannot 是
// determined.
func WindowsWorkingDirDrive() string {
	if cwd, err := os.Getwd(); err == nil {
		return filepath.VolumeName(cwd)
	}
	return WindowsSystemDrive()
}

// WindowsSystemDrive returns drive letter 的 system drive, e.g. "C:".
func WindowsSystemDrive() string {
	systemRoot := cmp.Or(os.Getenv("SYSTEMROOT"), os.Getenv("WINDIR"))
	return filepath.VolumeName(systemRoot)
}
