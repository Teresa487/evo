package tools

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

const testMaxJobs = 50

func TestBackgroundShell_Integration(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start 后台 shell
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, nil, workingDir, nil, shell.CoreutilsTurtle, "echo 'hello background' && echo 'done'", "", testMaxJobs)
	require.NoError(t, err)
	require.NotEmpty(t, bgShell.ID)

	// Wait 为 completion
	bgShell.Wait()

	// Check final output
	stdout, stderr, done, err := bgShell.GetOutput()
	require.NoError(t, err)
	require.Contains(t, stdout, "hello background")
	require.Contains(t, stdout, "done")
	require.True(t, done)
	require.Empty(t, stderr)

	// Clean up
	bgManager.Kill(ctx, bgShell.ID)
}

func TestBackgroundShell_Kill(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start long-running 后台 shell
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, nil, workingDir, nil, shell.CoreutilsTurtle, "sleep 100", "", testMaxJobs)
	require.NoError(t, err)

	// Kill it
	err = bgManager.Kill(ctx, bgShell.ID)
	require.NoError(t, err)

	// Verify it's gone
	_, ok := bgManager.Get(bgShell.ID)
	require.False(t, ok)

	// Verify shell 是 done
	require.True(t, bgShell.IsDone())
}

func TestBackgroundShell_MultipleOutputCalls(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start 后台 shell
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, nil, workingDir, nil, shell.CoreutilsTurtle, "echo 'step 1' && echo 'step 2' && echo 'step 3'", "", testMaxJobs)
	require.NoError(t, err)
	defer bgManager.Kill(ctx, bgShell.ID)

	// Check 那 we can 调用 GetOutput multiple times while running
	for range 5 {
		_, _, done, _ := bgShell.GetOutput()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Wait 为 completion
	bgShell.Wait()

	// Multiple calls after completion should return same 结果
	stdout1, _, done1, _ := bgShell.GetOutput()
	require.True(t, done1)
	require.Contains(t, stdout1, "step 1")
	require.Contains(t, stdout1, "step 2")
	require.Contains(t, stdout1, "step 3")

	stdout2, _, done2, _ := bgShell.GetOutput()
	require.True(t, done2)
	require.Equal(t, stdout1, stdout2, "Multiple GetOutput calls should return same result")
}

func TestBackgroundShell_EmptyOutput(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("This test is flacky on Windows for some reason")
	}

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start background shell 用 no output
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, nil, workingDir, nil, shell.CoreutilsTurtle, "sleep 0.1", "", testMaxJobs)
	require.NoError(t, err)
	defer bgManager.Kill(ctx, bgShell.ID)

	// Wait 为 completion
	bgShell.Wait()

	stdout, stderr, done, err := bgShell.GetOutput()
	require.NoError(t, err)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	require.True(t, done)
}

func TestBackgroundShell_ExitCode(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start background shell 那 exits 用 non-zero code
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, nil, workingDir, nil, shell.CoreutilsTurtle, "echo 'failing' && exit 42", "", testMaxJobs)
	require.NoError(t, err)
	defer bgManager.Kill(ctx, bgShell.ID)

	// Wait 为 completion
	bgShell.Wait()

	stdout, _, done, execErr := bgShell.GetOutput()
	require.True(t, done)
	require.Contains(t, stdout, "failing")
	require.Error(t, execErr)

	exitCode := shell.ExitCode(execErr)
	require.Equal(t, 42, exitCode)
}

func TestBackgroundShell_WithBlockFuncs(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	blockFuncs := []shell.BlockFunc{
		shell.CommandsBlocker([]string{"curl", "wget"}),
	}

	// Start background shell 用 blocked command
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, nil, workingDir, blockFuncs, shell.CoreutilsTurtle, "curl example.com", "", testMaxJobs)
	require.NoError(t, err)
	defer bgManager.Kill(ctx, bgShell.ID)

	// Wait 为 completion
	bgShell.Wait()

	stdout, stderr, done, execErr := bgShell.GetOutput()
	require.True(t, done)

	// command should have been blocked, check stderr 或 错误
	if execErr != nil {
		// 错误 might contain 消息
		require.Contains(t, execErr.Error(), "is a dangerous command")
	} else {
		// Or it might 是 在 stderr
		output := stdout + stderr
		require.Contains(t, output, "is a dangerous command")
	}
}

func TestBackgroundShell_StdoutAndStderr(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start background shell 用 both stdout 和 stderr
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, nil, workingDir, nil, shell.CoreutilsTurtle, "echo 'stdout message' && echo 'stderr message' >&2", "", testMaxJobs)
	require.NoError(t, err)
	defer bgManager.Kill(ctx, bgShell.ID)

	// Wait 为 completion
	bgShell.Wait()

	stdout, stderr, done, err := bgShell.GetOutput()
	require.NoError(t, err)
	require.True(t, done)
	require.Contains(t, stdout, "stdout message")
	require.Contains(t, stderr, "stderr message")
}

func TestBackgroundShell_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start 后台 shell
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, nil, workingDir, nil, shell.CoreutilsTurtle, "for i in 1 2 3 4 5; do echo \"line $i\"; sleep 0.05; done", "", testMaxJobs)
	require.NoError(t, err)
	defer bgManager.Kill(ctx, bgShell.ID)

	// Access output concurrently 从 multiple goroutines
	done := make(chan struct{})
	errors := make(chan error, 10)

	for range 10 {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					_, _, _, err := bgShell.GetOutput()
					if err != nil {
						errors <- err
					}
					dir := bgShell.WorkingDir
					if dir == "" {
						errors <- err
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()
	}

	// Let it 运行 为 bit
	time.Sleep(300 * time.Millisecond)
	close(done)

	// Check 为 any errors
	select {
	case err := <-errors:
		t.Fatalf("Concurrent access caused error: %v", err)
	case <-time.After(100 * time.Millisecond):
		// No errors - success
	}
}

func TestBackgroundShell_List(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	bgManager := shell.GetBackgroundShellManager()

	// Start multiple 后台 shells
	shells := make([]*shell.BackgroundShell, 3)
	for i := range 3 {
		bgShell, err := bgManager.Start(ctx, nil, workingDir, nil, shell.CoreutilsTurtle, "sleep 1", "", testMaxJobs)
		require.NoError(t, err)
		shells[i] = bgShell
	}

	// Get the list
	ids := bgManager.List()

	// Verify all our shells 是 在 list
	for _, sh := range shells {
		require.Contains(t, ids, sh.ID, "Shell %s not found in list", sh.ID)
	}

	// Clean up
	for _, sh := range shells {
		bgManager.Kill(ctx, sh.ID)
	}
}

func TestBackgroundShell_AutoBackground(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// 测试 那 quick command completes synchronously
	t.Run("quick command completes synchronously", func(t *testing.T) {
		t.Parallel()
		bgManager := shell.GetBackgroundShellManager()
		bgShell, err := bgManager.Start(ctx, nil, workingDir, nil, shell.CoreutilsTurtle, "echo 'quick'", "", testMaxJobs)
		require.NoError(t, err)

		// Wait threshold time
		time.Sleep(5 * time.Second)

		// Should 是 done 通过 now
		stdout, stderr, done, err := bgShell.GetOutput()
		require.NoError(t, err)
		require.True(t, done, "Quick command should be done")
		require.Contains(t, stdout, "quick")
		require.Empty(t, stderr)

		// Clean up
		bgManager.Kill(ctx, bgShell.ID)
	})

	// 测试 那 long command stays 在 background
	t.Run("long command stays in background", func(t *testing.T) {
		t.Parallel()
		bgManager := shell.GetBackgroundShellManager()
		bgShell, err := bgManager.Start(ctx, nil, workingDir, nil, shell.CoreutilsTurtle, "sleep 20 && echo '20 seconds completed'", "", testMaxJobs)
		require.NoError(t, err)
		defer bgManager.Kill(ctx, bgShell.ID)

		// Wait threshold time
		time.Sleep(5 * time.Second)

		// Should still 是 running
		stdout, stderr, done, err := bgShell.GetOutput()
		require.NoError(t, err)
		require.False(t, done, "Long command should still be running")
		require.Empty(t, stdout, "No output yet from sleep command")
		require.Empty(t, stderr)

		// Verify we can get shell 从 manager
		retrieved, ok := bgManager.Get(bgShell.ID)
		require.True(t, ok, "Should be able to retrieve background shell")
		require.Equal(t, bgShell.ID, retrieved.ID)
	})
}
