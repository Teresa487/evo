//go:build !windows

package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/lock"
)

// staleSocketDialTimeout bounds probe used 到 detect whether Unix
// socket 文件 在 disk 是 backed 通过 live listener.
const staleSocketDialTimeout = 200 * time.Millisecond

// socketLockTimeout bounds wait 为 another process 那 是 binding
// same Unix socket 路径. 它 holds 锁 为 one probe 和 one
// bind, 所以 wait 是 normally fraction 的 second.
const socketLockTimeout = 5 * time.Second

// dialSocket probes socket 文件 在 address 为 live listener. 一个
// package variable 所以 tests can stall caller between 它的 probe 和
// removal 那 follows.
var dialSocket = func(network, address string) (net.Conn, error) {
	return net.DialTimeout(network, address, staleSocketDialTimeout)
}

// listen binds net.Listener 在 given network 和 address.
//
// For unix sockets it self-heals 从 stale socket files: 如果 路径
// already exists 在 disk, it first probes 用 short net.DialTimeout.
// 一个 successful dial means live 服务端 owns socket, 所以 we proceed
// 到 net.Listen (which surfaces usual "address already 在 use"
// 错误). 一个 failed dial 那 isStaleSocketErr classifies 作为 stale
// triggers os.Remove 的 路径 (ignoring fs.ErrNotExist) before
// the bind.
//
// probe, removal 和 bind 运行 under exclusive 锁 在
// address+".锁", 所以 servers starting 在 once 在 same 路径 take
// turns. Otherwise one could remove the socket another has just bound,
// leaving 那 服务端 listening 在 deleted 文件 no 客户端 can reach.
// 锁 文件 是 never removed, because 锁 是 keyed 通过 inode
// rather than 路径.
//
// returned removedStale bool reports whether stale socket 文件
// was removed prior 到 binding 所以 callers can log it. operation
// 是 idempotent: removing absent 文件 是 no-op, 和 live
// socket 是 never removed.
//
// Closing returned unix listener removes socket 文件 只 while
// 路径 still refers 到 socket it bound; see [unixListener].
func listen(network, address string) (net.Listener, bool, error) {
	var removedStale bool
	if network == "unix" && address != "" {
		ctx, cancel := context.WithTimeout(context.Background(), socketLockTimeout)
		defer cancel()
		release, err := lock.File(ctx, address+".lock")
		if err != nil {
			return nil, false, fmt.Errorf("lock socket path: %w", err)
		}
		defer release()

		if _, err := os.Stat(address); err == nil {
			conn, dialErr := dialSocket(network, address)
			if dialErr == nil {
				// 一个 live 服务端 owns socket. Fall through 到
				// net.Listen 所以 caller sees standard
				// "address already 在 use" 错误.
				conn.Close()
			} else if isStaleSocketErr(dialErr) {
				rmErr := os.Remove(address)
				switch {
				case rmErr == nil:
					removedStale = true
				case errors.Is(rmErr, fs.ErrNotExist):
					// Another process removed it between our
					// stat 和 remove; treat 作为 no-op.
				default:
					return nil, false, rmErr
				}
			}
		}
	}
	//nolint:noctx
	ln, err := net.Listen(network, address)
	if err != nil {
		return nil, removedStale, err
	}
	// socket 是 created 用 process umask, which may let other
	// users connect 当 it lives 在 shared 目录 such 作为 /tmp.
	// Restrict it 到 它的 owner. Linux abstract sockets ( leading '@')
	// have no 文件 到 chmod.
	if network == "unix" && !strings.HasPrefix(address, "@") {
		if err := os.Chmod(address, 0o600); err != nil {
			ln.Close()
			return nil, removedStale, err
		}
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
		l := &unixListener{Listener: ul, path: address}
		if fi, err := os.Stat(address); err == nil {
			l.bound = fi
		}
		return l, removedStale, nil
	}
	return ln, removedStale, nil
}

// unixListener removes 它的 socket 文件 在 Close 只 while 路径
// still refers 到 socket it bound. Go's own unlink 在 close removes
// whatever 是 在 路径 通过 then, which can 是 replacement 服务端's
// socket: 客户端 那 gives up waiting 为 这 服务端 到 exit removes
// 路径 和 starts new 服务端 在 it.
type unixListener struct {
	net.Listener
	path       string
	bound      os.FileInfo
	unlinkOnce sync.Once
}

// Close removes socket 文件 如果 it 是 still one 这 listener
// bound, then closes the listener. The check runs once, while the socket
// 是 still open, 所以 它的 inode cannot have been reused 通过 another 文件.
func (l *unixListener) Close() error {
	l.unlinkOnce.Do(func() {
		if l.bound == nil {
			return
		}
		if fi, err := os.Stat(l.path); err == nil && os.SameFile(fi, l.bound) {
			_ = os.Remove(l.path)
		}
	})
	return l.Listener.Close()
}
