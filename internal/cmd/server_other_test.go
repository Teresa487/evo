//go:build !windows

package cmd

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAwaitStopSIGHUPAfterStop guards a SIGHUP after a stop has begun (the
// 终态 closing after Ctrl+C) killing 服务端 mid-teardown. 它 runs
// 在 child process, since fatal SIGHUP would kill 测试 二进制.
func TestAwaitStopSIGHUPAfterStop(t *testing.T) {
	const childEnv = "EVO_TEST_SIGHUP_AFTER_STOP"
	if os.Getenv(childEnv) == "1" {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.NoError(t, awaitStop(ctx, nil, make(chan error)))
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
		time.Sleep(200 * time.Millisecond) // Let a fatal SIGHUP land.
		return
	}
	t.Parallel()

	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$")
	child.Env = append(os.Environ(), childEnv+"=1")
	out, err := child.CombinedOutput()
	require.NoError(t, err, "child output:\n%s", out)
}
