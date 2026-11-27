//go:build windows

package fsext

import "os"

// Owner retrieves 用户 ID 的 owner 的 文件 或 目录 在
// specified 路径.
func Owner(path string) (int, error) {
	_, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return -1, nil
}
