package config_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/stretchr/testify/require"
)

// TestReloadFromDisk_CompilesHookMatchers 是 regression 测试 为 bug
// where ReloadFromDisk dropped compiled matcher regex 在 every 钩子,
// causing matcher like "^bash$" 到 match every 工具调用 after any
// SetConfigField-triggered reload.
//
// assertion 是 phrased 在 terms 的 observable Runner behavior (不
// internal 字段 presence) 所以 it stays valid 如果 Runner later owns
// matcher compilation itself.
func TestReloadFromDisk_CompilesHookMatchers(t *testing.T) {
	// No t.Parallel(): we Setenv HOME/XDG_CONFIG_HOME 到 isolate 从
	// developer's real global 配置, which may define 它的 own hooks.
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))

	workDir := t.TempDir()
	dataDir := t.TempDir()
	configPath := filepath.Join(workDir, "crush.json")
	cfgJSON := `{
        "hooks": {
            "PreToolUse": [
                {"matcher": "^bash$", "command": "exit 0"}
            ]
        }
    }`
	require.NoError(t, os.WriteFile(configPath, []byte(cfgJSON), 0o600))

	store, err := config.Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err)

	// Sanity: 钩子 filtering works immediately after Load.
	assertHookFilters(t, store)

	require.NoError(t, store.ReloadFromDisk(context.Background()))

	// actual regression check: filtering 必须 still work after
	// reload, 不 silently collapse 到 match-everything.
	assertHookFilters(t, store)
}

// assertHookFilters builds Runner 从 存储's current hooks 和
// verifies "^bash$" matcher rejects non-bash 工具 while accepting
// bash.
func assertHookFilters(t *testing.T, store *config.ConfigStore) {
	t.Helper()
	preHooks := store.Config().Hooks[hooks.EventPreToolUse]
	require.Len(t, preHooks, 1)

	// runner reads 存储 在 every 事件, exactly 作为 app
	// wires it, 所以 reload 那 rebuilds HookConfig values 是 covered.
	runner := hooks.NewRunner(func() map[string][]config.HookConfig {
		return store.Config().Hooks
	}, t.TempDir(), t.TempDir())

	nonMatch := runner.Run(context.Background(), hooks.Event{Name: hooks.EventPreToolUse, SessionID: "sess", Tool: &hooks.ToolCall{Name: "view", Input: `{}`}})
	require.Equal(t, 0, nonMatch.HookCount, "view must not match ^bash$ matcher")

	match := runner.Run(context.Background(), hooks.Event{Name: hooks.EventPreToolUse, SessionID: "sess", Tool: &hooks.ToolCall{Name: "bash", Input: `{}`}})
	require.Equal(t, 1, match.HookCount, "bash must match ^bash$ matcher")
}

// TestSetConfigField_AutoReload_PreservesHookMatcherFiltering verifies the
// dominant real-world trigger 路径: 配置 writes 调用 autoReload,
// autoReload calls ReloadFromDisk, 和 钩子 matching must remain correct.
func TestSetConfigField_AutoReload_PreservesHookMatcherFiltering(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))

	workDir := t.TempDir()
	dataDir := t.TempDir()
	configPath := filepath.Join(workDir, "crush.json")
	cfgJSON := `{
        "hooks": {
            "PreToolUse": [
                {"matcher": "^bash$", "command": "exit 0"}
            ]
        }
    }`
	require.NoError(t, os.WriteFile(configPath, []byte(cfgJSON), 0o600))

	store, err := config.Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err)
	assertHookFilters(t, store)

	require.NoError(t, store.SetConfigField(config.ScopeGlobal, "options.debug", true))

	assertHookFilters(t, store)
}
