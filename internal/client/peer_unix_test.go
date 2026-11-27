//go:build !windows

package client

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// serveLocal serves h 在 unix socket owned 通过 current 用户.
func serveLocal(t *testing.T, h http.Handler) (network, addr string) {
	t.Helper()
	// Keep it short: macOS caps socket paths 在 104 bytes.
	path := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", path) //nolint:noctx
	require.NoError(t, err)
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "unix", path
}

// pretendOtherUser makes 客户端 expect 服务端 运行 通过 uid other
// than ours, 作为 如果 our own sockets belonged 到 another 用户.
func pretendOtherUser(t *testing.T) {
	t.Helper()
	realEUID := geteuid
	geteuid = func() int { return realEUID() + 1 }
	t.Cleanup(func() { geteuid = realEUID })
}
