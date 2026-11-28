package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// loadEvoSh writes crush.sh into isolated project 和 loads it through
// real 配置 pipeline (discovery -> shell execution -> merge -> typed
// 配置). Asserting 在 resulting *配置.配置 是 black-box 测试 的
// what shell 配置 command actually produces, 和 it stays valid across
// internal changes 到 how 配置 是 assembled.
func loadEvoSh(t *testing.T, script string) *config.ConfigStore {
	t.Helper()
	store, err := loadEvoShErr(t, script)
	require.NoError(t, err)
	return store
}

// loadEvoShErr 是 loadEvoSh without asserting success, 为 cases 那 是
// expected 到 fail 在 load time.
func loadEvoShErr(t *testing.T, script string) (*config.ConfigStore, error) {
	t.Helper()
	// Isolate 从 developer's real global 配置 所以 只 script
	// under 测试 contributes. No t.Parallel(): these tests set env vars.
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("EVO_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "crush"))
	t.Setenv("EVO_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "crush"))

	workDir := t.TempDir()
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "evorc"), []byte(script), 0o644))

	return config.Load(t.Context(), workDir, dataDir, false)
}

func TestShellConfigPermissionsAllow(t *testing.T) {
	store := loadEvoSh(t, `permissions allow bash view`)

	require.NotNil(t, store.Config().Permissions)
	require.ElementsMatch(t, []string{"bash", "view"}, store.Config().Permissions.AllowedTools)
}

func TestShellConfigPermissionsAccumulateAndDedup(t *testing.T) {
	store := loadEvoSh(t, `permissions allow bash
permissions allow view
permissions allow bash`)

	require.Equal(t, []string{"bash", "view"}, store.Config().Permissions.AllowedTools)
}

func TestShellConfigPermissionsLegacyFlagFails(t *testing.T) {
	_, err := loadEvoShErr(t, `permissions --allow bash`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown subcommand")
}

func TestShellConfigPermissionsAllowRequiresTool(t *testing.T) {
	_, err := loadEvoShErr(t, `permissions allow`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "usage: permissions allow")
}

// deny hides tools 从 agent 通过 writing options.disabled_tools.
func TestShellConfigPermissionsDeny(t *testing.T) {
	store := loadEvoSh(t, `permissions deny bash sourcegraph
permissions deny bash`)

	require.Equal(t, []string{"bash", "sourcegraph"}, store.Config().Options.DisabledTools)
}

// When 工具 是 both allowed 和 denied, deny wins: 工具 lands 在
// disabled_tools which removes it 从 agent entirely, regardless 的
// 它的 presence 在 allow-list.
func TestShellConfigPermissionsDenyWinsOverAllow(t *testing.T) {
	store := loadEvoSh(t, `permissions allow bash view
permissions deny bash`)

	require.ElementsMatch(t, []string{"bash", "view"}, store.Config().Permissions.AllowedTools)
	require.Equal(t, []string{"bash"}, store.Config().Options.DisabledTools)

	// SetupAgents resolves effective 工具 set; denied tools 是 excluded.
	cfg := store.Config()
	cfg.SetupAgents()
	require.NotContains(t, cfg.Agents[config.AgentCoder].AllowedTools, "bash")
	require.Contains(t, cfg.Agents[config.AgentCoder].AllowedTools, "view")
}

func TestShellConfigPermissionsSafeCommands(t *testing.T) {
	// 一个 command line 是 one 条目, 所以 it 是 given 作为 single quoted word.
	store := loadEvoSh(t, `permissions safe "go build" "cargo check"
permissions safe "go vet"`)

	require.NotNil(t, store.Config().Permissions)
	require.Equal(t, []string{"go build", "cargo check", "go vet"},
		store.Config().Permissions.SafeCommands)
}

func TestShellConfigPermissionsBlockAndUnblock(t *testing.T) {
	store := loadEvoSh(t, `permissions block kubectl terraform
permissions unblock curl`)

	perms := store.Config().Permissions
	require.NotNil(t, perms)
	require.Equal(t, []string{"kubectl", "terraform"}, perms.BlockedCommands)
	require.Equal(t, []string{"curl"}, perms.AllowedCommands)
}

func TestShellConfigPermissionsCommandListsDedupe(t *testing.T) {
	store := loadEvoSh(t, `permissions block kubectl
permissions block kubectl
permissions safe "go build"
permissions safe "go build"`)

	perms := store.Config().Permissions
	require.Equal(t, []string{"kubectl"}, perms.BlockedCommands)
	require.Equal(t, []string{"go build"}, perms.SafeCommands)
}

func TestShellConfigPermissionsCommandListsCoexistWithTools(t *testing.T) {
	// 工具 verbs 和 command verbs 写入 到 different places 和
	// must 不 tread 在 each other.
	store := loadEvoSh(t, `permissions allow view
permissions deny write
permissions safe "go build"
permissions block kubectl`)

	cfg := store.Config()
	require.Equal(t, []string{"view"}, cfg.Permissions.AllowedTools)
	require.Contains(t, cfg.Options.DisabledTools, "write")
	require.Equal(t, []string{"go build"}, cfg.Permissions.SafeCommands)
	require.Equal(t, []string{"kubectl"}, cfg.Permissions.BlockedCommands)
}

func TestShellConfigPermissionsUnknownSubcommand(t *testing.T) {
	_, err := loadEvoShErr(t, `permissions frobnicate bash`)
	require.Error(t, err)
}
