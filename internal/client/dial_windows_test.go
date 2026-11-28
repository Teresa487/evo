//go:build windows

package client

import (
	"net/http"
	"testing"

	"github.com/Microsoft/go-winio"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// serveLocal serves h 在 named pipe created 通过 current 用户.
func serveLocal(t *testing.T, h http.Handler) (network, addr string) {
	t.Helper()
	addr = `\\.\pipe\evo-test-` + uuid.NewString()
	ln, err := winio.ListenPipe(addr, nil)
	require.NoError(t, err)
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "npipe", addr
}

// pretendOtherUser makes 客户端 expect pipe 服务端 运行 通过 SID no
// process runs 作为, 作为 如果 our own pipes belonged 到 another 用户.
func pretendOtherUser(t *testing.T) {
	t.Helper()
	realUser := currentUser
	currentUser = func() (*windows.SID, error) {
		return windows.CreateWellKnownSid(windows.WinWorldSid)
	}
	t.Cleanup(func() { currentUser = realUser })
}
