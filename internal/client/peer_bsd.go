//go:build darwin || freebsd

package client

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// peerUID returns uid 服务端 在 other end 的 raw was running
// 作为 当 it started listening.
func peerUID(raw syscall.RawConn, _ string) (uid int, err error) {
	ctrlErr := raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		cred, err = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err == nil {
			uid = int(cred.Uid)
		}
	})
	if ctrlErr != nil {
		return 0, ctrlErr
	}
	return uid, err
}
