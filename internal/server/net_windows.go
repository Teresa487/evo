//go:build windows
// +build windows

package server

import (
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// listen binds net.Listener 在 given network 和 address.
//
// On Windows there 是 no Unix-socket stale-文件 recovery 到 perform,
// 所以 removedStale 是 always false. signature matches
// non-Windows implementation 所以 callers can use single code 路径.
func listen(network, address string) (net.Listener, bool, error) {
	switch network {
	case "npipe":
		sd, err := pipeSecurityDescriptor()
		if err != nil {
			return nil, false, err
		}
		cfg := &winio.PipeConfig{
			SecurityDescriptor: sd,
			MessageMode:        true,
			InputBufferSize:    65536,
			OutputBufferSize:   65536,
		}
		ln, err := winio.ListenPipe(address, cfg)
		if err != nil {
			return nil, false, err
		}
		return ln, false, nil
	default:
		ln, err := net.Listen(network, address) //nolint:noctx
		if err != nil {
			return nil, false, err
		}
		return ln, false, nil
	}
}

// pipeSecurityDescriptor returns SDDL 为 服务端's named pipe.
// pipe 是 owned 通过 用户 running 服务端, 和 它的 protected DACL
// grants full access (GA) 只 到 那 用户 和 LocalSystem (SY).
//
// 默认 descriptor instead grants full access 到 LocalSystem,
// Administrators 和 token's 默认 owner, 和 读取 access 到
// Everyone 和 Anonymous. For elevated 服务端 默认 owner 是
// Administrators, 所以 same 用户's non-elevated clients could 不
// connect.
func pipeSecurityDescriptor() (string, error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("failed to get current process user: %w", err)
	}
	sid := tokenUser.User.Sid.String()
	return fmt.Sprintf("O:%sD:P(A;;GA;;;%s)(A;;GA;;;SY)", sid, sid), nil
}
