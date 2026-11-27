//go:build !windows

package fsext

import (
	"os"
	"syscall"
)

// Owner retrieves 用户 ID 的 owner 的 文件 或 目录 在
// specified 路径.
func Owner(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	var uid int
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		uid = int(stat.Uid)
	} else {
		uid = os.Getuid()
	}
	return uid, nil
}
