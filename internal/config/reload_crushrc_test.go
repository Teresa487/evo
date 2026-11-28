package config_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// isolateReloadEnv points HOME/XDG/CRUSH_* 在 throwaway 目录 所以
// reload 只 sees 配置 files 测试 writes. No t.Parallel(): these
// tests Setenv.
func isolateReloadEnv(t *testing.T) (workDir, dataDir string) {
	t.Helper()
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "crush"))
	return t.TempDir(), t.TempDir()
}

// TestReloadFromDisk_PicksUpEditedCrushrc verifies 那 crushrc edit 在
// disk 是 reflected 在 memory after reload, matching how JSON 配置
// already behaves.
func TestReloadFromDisk_PicksUpEditedCrushrc(t *testing.T) {
	workDir, dataDir := isolateReloadEnv(t)
	rcPath := filepath.Join(workDir, "evorc")
	require.NoError(t, os.WriteFile(rcPath, []byte("option notifications bell\n"), 0o644))

	store, err := config.Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err)
	require.Equal(t, "bell", store.Config().Options.Notifications)

	require.NoError(t, os.WriteFile(rcPath, []byte("option notifications osc\n"), 0o644))
	require.NoError(t, store.ReloadFromDisk(context.Background()))
	require.Equal(t, "osc", store.Config().Options.Notifications,
		"reload must re-execute the crushrc and pick up the new value")
}

// TestReloadFromDisk_FailingCrushrcKeepsOldConfig verifies 那 crushrc
// which fails 到 load during reload does 不 clobber 在-memory 配置.
// reload must return 错误 和 leave previously-loaded 配置
// intact ( swap happens 只 after successful parse).
func TestReloadFromDisk_FailingCrushrcKeepsOldConfig(t *testing.T) {
	workDir, dataDir := isolateReloadEnv(t)
	rcPath := filepath.Join(workDir, "evorc")
	require.NoError(t, os.WriteFile(rcPath, []byte("option notifications bell\n"), 0o644))

	store, err := config.Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err)
	require.Equal(t, "bell", store.Config().Options.Notifications)

	// Overwrite 用 crushrc 那 errors 在 load time (unknown option key).
	require.NoError(t, os.WriteFile(rcPath, []byte("option totally-bogus-key value\n"), 0o644))

	err = store.ReloadFromDisk(context.Background())
	require.Error(t, err, "a failing crushrc must surface a reload error")
	require.Equal(t, "bell", store.Config().Options.Notifications,
		"in-memory config must be preserved when a reload fails")
}

// TestReloadFromDisk_HangingCrushrcIsInterruptible 是 core regression 测试
// 为 reload-路径 blocker: 配置 reloads 运行 while holding 存储's
// 写入 锁, 所以 crushrc 那 blocks (busy loop, hung command substitution)
// must 是 interruptible via reload 上下文 rather than wedging 存储
// forever. 一个 cancelled reload must return 错误 和 leave old 配置
// 在 place. 测试 bounds 它的 own wait 所以 regression can't hang CI.
func TestReloadFromDisk_HangingCrushrcIsInterruptible(t *testing.T) {
	workDir, dataDir := isolateReloadEnv(t)
	rcPath := filepath.Join(workDir, "evorc")
	require.NoError(t, os.WriteFile(rcPath, []byte("option notifications bell\n"), 0o644))

	store, err := config.Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err)
	require.Equal(t, "bell", store.Config().Options.Notifications)

	// Overwrite 用 crushrc 那 never terminates.
	require.NoError(t, os.WriteFile(rcPath, []byte("while true; do :; done\n"), 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() {
		done <- store.ReloadFromDisk(ctx)
	}()

	select {
	case err := <-done:
		require.Error(t, err, "a cancelled reload must fail, not succeed")
	case <-time.After(3 * time.Second):
		t.Fatal("ReloadFromDisk did not return after context cancellation; the store would be wedged")
	}

	require.Equal(t, "bell", store.Config().Options.Notifications,
		"in-memory config must be preserved when a reload is cancelled")
}

// TestLoad_TracksNotYetCreatedGlobalCrushrc verifies 那 配置.Load tracks
// global crushrc 路径 even 当 文件 does 不 exist yet, 所以 crushrc
// created after startup 是 detected 作为 staleness change. Previously 只
// successfully-loaded paths were tracked, 所以 mid-会话 global crushrc went
// unnoticed until something else triggered a reload.
//
// Project-level crushrc files 是 discovered 通过 walking tree 为 existing
// files, 所以 不-yet-created project crushrc 是 still 只 picked up 在
// next reload; global 路径 是 common case 这 covers.
func TestLoad_TracksNotYetCreatedGlobalCrushrc(t *testing.T) {
	workDir, dataDir := isolateReloadEnv(t)
	globalRC := filepath.Join(t.TempDir(), "evorc")
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Dir(globalRC))

	// 一个 提供方 must 是 configured 所以 Load runs past 它的 early
	// "不 configured" return 和 reaches staleness snapshot capture.
	require.NoError(t, os.WriteFile(
		filepath.Join(workDir, "evorc"),
		[]byte("provider add openai --api-key k\n"), 0o644,
	))

	// Load 用 no global crushrc present.
	store, err := config.Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err)
	require.False(t, store.ConfigStaleness().Dirty, "fresh load should be clean")

	// 创建 global crushrc after startup.
	require.NoError(t, os.WriteFile(globalRC, []byte("option debug true\n"), 0o644))

	staleness := store.ConfigStaleness()
	require.True(t, staleness.Dirty, "creating a global crushrc must be detected")
	require.Contains(t, staleness.Changed, globalRC)
}
