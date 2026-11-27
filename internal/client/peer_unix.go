//go:build !windows

package client

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// geteuid returns uid 服务端 must 运行 作为: our effective uid, which
// 是 what peer credentials 和 文件 owners record. Tests replace it 到
// pretend 服务端 belongs 到 another 用户.
var geteuid = os.Geteuid

// verifyUnixPeer fails unless 服务端 在 other end 的 conn,
// 连接 到 unix socket 在 路径, runs 作为 current 用户.
func verifyUnixPeer(conn net.Conn, path string) error {
	uid, err := connPeerUID(conn, path)
	if err != nil {
		return fmt.Errorf("failed to verify the owner of server socket %q: %w", path, err)
	}
	if want := geteuid(); uid != want {
		return fmt.Errorf("server socket %q is owned by another user (uid %d, not %d)", path, uid, want)
	}
	return nil
}

// connPeerUID returns uid 的 服务端 在 other end 的 conn.
func connPeerUID(conn net.Conn, path string) (int, error) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return 0, fmt.Errorf("unsupported connection type %T", conn)
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, err
	}
	return peerUID(raw, path)
}
