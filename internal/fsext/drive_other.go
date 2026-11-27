//go:build !windows

package fsext

// WindowsWorkingDirDrive returns drive letter 的 current working
// 目录, e.g. "C:".
// Falls back 到 system drive 如果 current working 目录 cannot 是
// determined.
func WindowsWorkingDirDrive() string {
	panic("cannot call fsext.WindowsWorkingDirDrive() on non-Windows OS")
}

// WindowsSystemDrive returns drive letter 的 system drive, e.g. "C:".
func WindowsSystemDrive() string {
	panic("cannot call fsext.WindowsSystemDrive() on non-Windows OS")
}
