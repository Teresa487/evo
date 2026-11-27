//go:build !windows && !linux && !darwin && !freebsd

package client

import (
	"fmt"
	"os"
	"syscall"
)

// peerUID returns owner 的 socket 文件 在 路径. Peer credentials
// 是 只 looked up 在 Linux, macOS 和 FreeBSD; elsewhere owner
// stands 在 为 them, since 在 sticky 目录 such 作为 /tmp 只 那
// 用户 can replace 文件.
func peerUID(_ syscall.RawConn, path string) (int, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unsupported file info %T", fi.Sys())
	}
	return int(st.Uid), nil
}
