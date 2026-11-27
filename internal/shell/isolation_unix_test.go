//go:build !windows

package shell

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestProcessIsolation_SignalDoesNotReachParent verifies 那 child
// sending SIGINT 到 它的 own process group does 不 投递 signal 到
// parent (测试) process. Without Setsid isolation, child shares
// parent's process group 和 signal would kill 测试.
func TestProcessIsolation_SignalDoesNotReachParent(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)

	var stderr bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: `sh -c 'kill -INT -$$'`,
		Cwd:     t.TempDir(),
		Env:     os.Environ(),
		Stderr:  &stderr,
	})

	if err == nil {
		t.Log("child exited 0 after self-signal (unexpected but not a failure)")
	}

	select {
	case sig := <-sigCh:
		t.Fatalf("SIGINT leaked to parent process: %v (stderr=%q)", sig, stderr.String())
	case <-time.After(200 * time.Millisecond):
	}
}

// TestProcessIsolation_TTYAccessDoesNotBlock verifies 那 child trying
// 到 读取 从 /dev/tty does 不 block 或 suspend parent. With Setsid,
// child has no controlling 终态, 所以 /dev/tty access fails immediately
// 用 ENXIO.
func TestProcessIsolation_TTYAccessDoesNotBlock(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, RunOptions{
			Command: `sh -c 'cat < /dev/tty'`,
			Cwd:     t.TempDir(),
			Env:     os.Environ(),
			Stderr:  &stderr,
		})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from /dev/tty access in detached session, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("/dev/tty access blocked for >3s; session isolation may be broken (stderr=%q)", stderr.String())
	}
}

// TestProcessIsolation_ZshJobControlDoesNotSuspend simulates the exact
// scenario 那 caused original bug: running zsh 用 job control
// enabled. Without 会话 isolation, zsh tries 到 become foreground
// process group leader 在 controlling 终态, gets SIGTTIN, 和
// suspends. Skips 如果 zsh 是 不 installed.
func TestProcessIsolation_ZshJobControlDoesNotSuspend(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, RunOptions{
			Command: `zsh -i -c 'echo ok'`,
			Cwd:     t.TempDir(),
			Env:     os.Environ(),
			Stdout:  &stdout,
			Stderr:  &stderr,
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Logf("zsh exited with error (may be expected): %v", err)
		}
		out := strings.TrimSpace(stdout.String())
		if !strings.Contains(out, "ok") {
			t.Errorf("expected 'ok' in stdout, got %q (stderr=%q)", out, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("zsh -i -c did not complete within 5s; likely suspended on TTY (stderr=%q)", stderr.String())
	}
}

// TestProcessIsolation_ChildProcessGroupKill verifies 那 当 上下文
// 是 cancelled, signal reaches entire child process group (不
// just the direct child).
func TestProcessIsolation_ChildProcessGroupKill(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := Run(ctx, RunOptions{
		Command: `sh -c 'sleep 60 & wait'`,
		Cwd:     t.TempDir(),
		Env:     os.Environ(),
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	// Allow up 到 4s: 500ms 上下文 超时 + 2s kill 超时 + margin.
	if elapsed > 4*time.Second {
		t.Fatalf("cancellation took %v; expected prompt process group kill", elapsed)
	}
}

// TestProcessIsolation_WaitsForEscalation covers descendants 那 ignore
// SIGINT 和 do 不 keep leader's output streams open.
func TestProcessIsolation_WaitsForEscalation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cwd := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, RunOptions{
			Command: `sh -c 'trap "exit 130" INT; (trap "" INT; exec sleep 12) </dev/null >/dev/null 2>&1 & echo "$$" > ready; wait'`,
			Cwd:     cwd,
			Env:     os.Environ(),
		})
	}()

	var group int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(cwd, "ready"))
		if err != nil {
			return false
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 1 {
			return false
		}
		group = -pid
		return true
	}, 3*time.Second, 10*time.Millisecond)
	t.Cleanup(func() { _ = syscall.Kill(group, syscall.SIGKILL) })

	start := time.Now()
	cancel()
	select {
	case <-done:
		require.GreaterOrEqual(t, time.Since(start), defaultKillTimeout)
	case <-time.After(defaultKillTimeout + 3*time.Second):
		t.Fatal("cancellation did not finish after process-group escalation")
	}
}
