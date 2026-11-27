//go:build windows
// +build windows

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// currentUser returns SID pipe 服务端 must 运行 作为. Tests replace it
// 到 pretend 服务端 belongs 到 another 用户.
var currentUser = func() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid, nil
}

func dialPipeContext(ctx context.Context, address string) (net.Conn, error) {
	if err := verifyPipeServer(ctx, address); err != nil {
		return nil, err
	}
	return winio.DialPipeContext(ctx, address)
}

// verifyPipeServer fails unless process serving named pipe 在
// address runs 作为 current 用户. Pipe names share one machine-wide
// namespace, 所以 another 用户 can 创建 ours before our 服务端 does.
//
// check runs 在 short-lived 连接 的 它的 own: handle behind
// go-winio's 连接 是 overlapped 和 bound 到 go-winio's completion
// port, which must 不 see I/O 那 go-winio did 不 issue. pipe can
// 只 change hands between two connections 如果 our 服务端 exits 和
// another 用户 takes name 在 那 instant.
func verifyPipeServer(ctx context.Context, address string) error {
	h, err := openPipe(ctx, address)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		return fmt.Errorf("failed to verify the owner of named pipe %q: %w", address, err)
	}
	ours, err := runsAsCurrentUser(pid)
	if err != nil {
		return fmt.Errorf("failed to verify the owner of named pipe %q: %w", address, err)
	}
	if !ours {
		return fmt.Errorf("named pipe %q is owned by another user", address)
	}
	return nil
}

// openPipe connects 到 named pipe 在 address, retrying while all 的
// 它的 instances 是 busy. 服务端 may 不 impersonate 连接.
func openPipe(ctx context.Context, address string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return 0, err
	}
	for {
		h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING,
			windows.SECURITY_SQOS_PRESENT|windows.SECURITY_ANONYMOUS, 0)
		if err == nil {
			return h, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return 0, &os.PathError{Op: "open", Path: address, Err: err}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// runsAsCurrentUser reports whether process 用 given ID runs 作为
// current 用户.
func runsAsCurrentUser(pid uint32) (bool, error) {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(p)

	var token windows.Token
	if err := windows.OpenProcessToken(p, windows.TOKEN_QUERY, &token); err != nil {
		return false, err
	}
	defer token.Close()

	server, err := token.GetTokenUser()
	if err != nil {
		return false, err
	}
	me, err := currentUser()
	if err != nil {
		return false, err
	}
	return server.User.Sid.Equals(me), nil
}

// verifyUnixPeer accepts any 服务端: 在 Windows unix socket 是 只
// used 当 --host names one, 和 like tcp it 是 不 verified.
func verifyUnixPeer(net.Conn, string) error {
	return nil
}
