//go:build !windows

package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Each 测试 uses distinct sleep duration 所以 它的 pgrep/pkill guards 只
// see 它的 own descendants, even 当 tests 运行 在 parallel.

// reapSleep removes leftover sleep processes started 通过 测试.
func reapSleep(t *testing.T, duration string) {
	t.Helper()
	// 上下文.Background, 不 t.上下文: 这 runs after 测试's
	// 上下文 是 already cancelled.
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "pkill", "-f", "sleep "+duration).Run()
	})
}

// requireDescendant waits until process matching pattern 是 alive, 所以
// 测试 kills job whose descendant has definitely started.
func requireDescendant(t *testing.T, pattern string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return exec.CommandContext(context.Background(), "pgrep", "-f", pattern).Run() == nil
	}, 5*time.Second, 50*time.Millisecond, "descendant %q never started", pattern)
}

// killWithin calls Kill 和 fails 测试 如果 it blocks past 期限,
// rather than hanging until package 测试 超时.
func killWithin(t *testing.T, manager *BackgroundShellManager, id string, limit time.Duration) {
	t.Helper()
	result := make(chan error, 1)
	start := time.Now()
	go func() { result <- manager.Kill(t.Context(), id) }()
	select {
	case err := <-result:
		require.NoError(t, err)
		require.Less(t, time.Since(start), limit, "Kill returned too slowly")
	case <-time.After(limit):
		t.Fatal("Kill hung: a descendant is holding the shell's pipes open")
	}
}

// 一个 setsid'd grandchild leaves job's process group 和 keeps stdout
// pipe open after 它的 parent exits. exec 处理器's WaitDelay must unblock
// cmd.Wait 所以 job finishes 和 Kill returns.
func TestKill_SetsidDescendantHoldsPipe(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}
	reapSleep(t, "17")
	manager := newBackgroundShellManager()

	bgShell, err := manager.Start(context.Background(), nil, t.TempDir(), nil, CoreutilsTurtle, "sh -c 'setsid sleep 17 &'", "", testMaxJobs)
	require.NoError(t, err)
	requireDescendant(t, "sleep 17")

	killWithin(t, manager, bgShell.ID, 6*time.Second)
	require.True(t, bgShell.IsDone())
}

// 一个 backgrounded child 那 outlives 它的 `sh -c` parent holds output pipe
// open. Without WaitDelay job never completes 在 它的 own; 用 one it
// finishes about WaitDelay (killTimeout + 1s) after the parent exits.
func TestJobCompletesWhenBackgroundedChildHoldsPipe(t *testing.T) {
	reapSleep(t, "8")
	manager := newBackgroundShellManager()

	start := time.Now()
	bgShell, err := manager.Start(context.Background(), nil, t.TempDir(), nil, CoreutilsTurtle, "sh -c 'sleep 8 &'; echo hi", "", testMaxJobs)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	t.Cleanup(cancel)
	require.True(t, bgShell.WaitContext(ctx), "job never completed: cmd.Wait was held open by the backgrounded child")
	require.Less(t, time.Since(start), 6*time.Second)
}

// shebang 派发 路径 must kill interpreter's whole process group,
// 不 just interpreter PID, 或 script's child keeps pipes open
// 和 cmd.Wait blocks forever.
func TestKill_ShebangScriptChild(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hang.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 15 &\nwait\n"), 0o755))
	reapSleep(t, "15")
	manager := newBackgroundShellManager()

	bgShell, err := manager.Start(context.Background(), nil, dir, nil, CoreutilsTurtle, "./hang.sh", "", testMaxJobs)
	require.NoError(t, err)
	requireDescendant(t, "sleep 15")

	killWithin(t, manager, bgShell.ID, 6*time.Second)
	require.True(t, bgShell.IsDone())
}
