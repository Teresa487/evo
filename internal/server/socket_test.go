//go:build !windows

package server

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeTimeoutErr 是 minimal net.错误 implementation whose 超时()
// returns true. 它 是 used 到 verify 那 IsStaleSocketErr never
// classifies 超时 作为 stale.
type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "fake timeout" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }

func TestIsStaleSocketErr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "ECONNREFUSED", err: syscall.ECONNREFUSED, want: true},
		{
			name: "wrapped ECONNREFUSED",
			err:  fmt.Errorf("dial: %w", syscall.ECONNREFUSED),
			want: true,
		},
		{name: "fs.ErrNotExist", err: fs.ErrNotExist, want: true},
		{
			name: "wrapped fs.ErrNotExist",
			err:  fmt.Errorf("stat: %w", fs.ErrNotExist),
			want: true,
		},
		{name: "timeout net.Error", err: fakeTimeoutErr{}, want: false},
		{name: "generic error", err: errors.New("boom"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsStaleSocketErr(tc.err))
		})
	}
}

// TestDefaultHost_UIDWithoutUserEnv guards against users 那 用户.Current
// cannot resolve (no $用户 或 $HOME) sharing one socket.
func TestDefaultHost_UIDWithoutUserEnv(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("HOME", "")
	require.Equal(t, fmt.Sprintf("evo-%d.sock", os.Getuid()), filepath.Base(DefaultHost()))
}

func TestDefaultHost_XDGRuntimeDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	host := DefaultHost()

	require.True(t, strings.HasPrefix(host, "unix://"),
		"DefaultHost should return a unix:// URL, got %q", host)
	path := strings.TrimPrefix(host, "unix://")

	// composed 路径 may exceed maxUnixSocketPathLen 和 fall back
	// 到 /tmp; 只 assert containment 当 it did 不. Recompose
	// 路径 under dir (rather than checking returned 路径 length,
	// which 是 short again after /tmp fallback) 到 decide whether
	// fallback happened. socket 是 named evo-<uid>.sock.
	composed := filepath.Join(dir, filepath.Base(path))
	if len(composed) <= maxUnixSocketPathLen {
		require.True(t, strings.HasPrefix(path, dir),
			"socket path %q should live under %q", path, dir)
	}
	require.True(t, strings.HasSuffix(path, ".sock"),
		"socket path %q should end in .sock", path)
	require.Contains(t, filepath.Base(path), "evo",
		"socket filename should contain 'evo'")
}

func TestDefaultHost_FallbackTemp(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	host := DefaultHost()

	require.True(t, strings.HasPrefix(host, "unix://"),
		"DefaultHost should return a unix:// URL, got %q", host)
	path := strings.TrimPrefix(host, "unix://")
	require.NotEmpty(t, path, "fallback socket path must be non-empty")
	require.True(t, strings.HasSuffix(path, ".sock"),
		"socket path %q should end in .sock", path)
	require.Contains(t, filepath.Base(path), "evo",
		"socket filename should contain 'evo'")
}

// staleSocketPath creates deterministic stale unix socket 文件 在
// disk: socket node exists 但 no goroutine 是 accepting 在 it.
// 它 does 所以 通过 binding listener, disabling unlink-在-close, then
// closing listener. 路径 是 returned 所以 caller can probe
// it. 一个 leftover 文件 是 best-effort removed via t.清理.
func staleSocketPath(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path) //nolint:noctx
	require.NoError(t, err)
	ul, ok := ln.(*net.UnixListener)
	require.True(t, ok, "expected *net.UnixListener, got %T", ln)
	ul.SetUnlinkOnClose(false)
	require.NoError(t, ul.Close())

	// Verify it 是 actually stale: dialing should fail.
	conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond) //nolint:noctx
	if dialErr == nil {
		conn.Close()
		t.Fatalf("expected stale socket at %q to refuse connections", path)
	}
	require.True(t, IsStaleSocketErr(dialErr),
		"expected stale-socket dial error, got %v", dialErr)

	t.Cleanup(func() {
		_ = os.Remove(path)
	})
}

func TestListen_RemovesStaleSocket(t *testing.T) {
	// t.TempDir() yields 路径 那 may already 是 near macOS
	// sun_path limit; use short filename 到 stay well under it.
	dir := t.TempDir()
	path := filepath.Join(dir, "s.sock")

	staleSocketPath(t, path)

	// Confirm stale node 是 present before we 调用 listen.
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "stale socket file should exist on disk")

	ln, removedStale, err := listen("unix", path)
	require.NoError(t, err)
	require.NotNil(t, ln)
	require.True(t, removedStale, "listen should report removedStale=true")
	t.Cleanup(func() {
		_ = ln.Close()
	})
}

func TestListen_LiveSocketNotRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.sock")

	ln1, err := net.Listen("unix", path) //nolint:noctx
	require.NoError(t, err)

	// Drain accepts 所以 listener stays alive 和 responsive without
	// blocking 测试 在 stray 连接.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln1.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() {
		_ = ln1.Close()
		wg.Wait()
	})

	ln2, removedStale, err := listen("unix", path)
	if ln2 != nil {
		_ = ln2.Close()
	}
	require.Error(t, err, "listen on a live socket must fail")
	require.False(t, removedStale,
		"a live socket must never be removed (got removedStale=true)")

	// live socket 文件 must still 是 在 disk 和 dialable.
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "live socket file should still exist")
	conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond) //nolint:noctx
	require.NoError(t, dialErr, "live socket should still accept dials")
	_ = conn.Close()
}

// TestListen_RacingStaleRecovery guards against listen 调用 那 found
// stale socket removing live one concurrent 调用 bound meanwhile.
func TestListen_RacingStaleRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	staleSocketPath(t, path)

	probed := make(chan struct{})
	secondDone := make(chan struct{})
	var calls atomic.Int32
	origDial := dialSocket
	dialSocket = func(network, address string) (net.Conn, error) {
		conn, err := origDial(network, address)
		if calls.Add(1) == 1 {
			// Stall first caller between 它的 probe 和 它的 removal.
			close(probed)
			select {
			case <-secondDone:
			case <-time.After(time.Second):
			}
		}
		return conn, err
	}
	t.Cleanup(func() { dialSocket = origDial })

	firstErr := make(chan error, 1)
	go func() {
		ln, _, err := listen("unix", path)
		if err == nil {
			t.Cleanup(func() { _ = ln.Close() })
		}
		firstErr <- err
	}()
	<-probed
	ln2, _, err2 := listen("unix", path)
	close(secondDone)
	if err2 == nil {
		t.Cleanup(func() { _ = ln2.Close() })
	}
	require.NoError(t, <-firstErr)
	require.ErrorIs(t, err2, syscall.EADDRINUSE)
}

// TestListen_RestrictsSocketToOwner guards against socket other users 可以
// connect 到. umask 是 process-wide, 所以 这 测试 是 不 parallel.
func TestListen_RestrictsSocketToOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	oldUmask := syscall.Umask(0)
	ln, _, err := listen("unix", path)
	syscall.Umask(oldUmask)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestListen_CloseKeepsReplacement guards against exiting 服务端's
// listener unlinking socket 的 replacement bound 在 它的 路径.
func TestListen_CloseKeepsReplacement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "s.sock")
	old, _, err := listen("unix", path)
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	repl, _, err := listen("unix", path)
	require.NoError(t, err)

	require.NoError(t, old.Close())
	_, err = os.Stat(path)
	require.NoError(t, err, "closing the old listener removed the replacement's socket")
	require.NoError(t, repl.Close())
	_, err = os.Stat(path)
	require.ErrorIs(t, err, fs.ErrNotExist, "a listener must still remove its own socket")
}
