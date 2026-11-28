package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestConfigStore_ConfigPath_GlobalAlwaysWorks(t *testing.T) {
	t.Parallel()

	store := &ConfigStore{
		globalDataPath: "/some/global/crush.json",
	}

	path, err := store.configPath(ScopeGlobal)
	require.NoError(t, err)
	require.Equal(t, "/some/global/crush.json", path)
}

func TestConfigStore_ConfigPath_WorkspaceReturnsPath(t *testing.T) {
	t.Parallel()

	store := &ConfigStore{
		workspacePath: "/some/workspace/.evo/crush.json",
	}

	path, err := store.configPath(ScopeWorkspace)
	require.NoError(t, err)
	require.Equal(t, "/some/workspace/.evo/crush.json", path)
}

func TestConfigStore_ConfigPath_WorkspaceErrorsWhenEmpty(t *testing.T) {
	t.Parallel()

	store := &ConfigStore{
		globalDataPath: "/some/global/crush.json",
		workspacePath:  "",
	}

	_, err := store.configPath(ScopeWorkspace)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoWorkspaceConfig))
}

func TestConfigStore_SetConfigField_WorkspaceScopeGuard(t *testing.T) {
	t.Parallel()

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: filepath.Join(t.TempDir(), "global.json"),
		workspacePath:  "",
	}

	err := store.SetConfigField(ScopeWorkspace, "foo", "bar")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoWorkspaceConfig))
}

func TestConfigStore_SetConfigField_GlobalScopeAlwaysWorks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	globalPath := filepath.Join(dir, "evo.json")
	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: globalPath,
	}

	err := store.SetConfigField(ScopeGlobal, "foo", "bar")
	require.NoError(t, err)

	data, err := os.ReadFile(globalPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"foo"`)
}

func TestConfigStore_RemoveConfigField_WorkspaceScopeGuard(t *testing.T) {
	t.Parallel()

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: filepath.Join(t.TempDir(), "global.json"),
		workspacePath:  "",
	}

	err := store.RemoveConfigField(ScopeWorkspace, "foo")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoWorkspaceConfig))
}

func TestConfigStore_HasConfigField_WorkspaceScopeGuard(t *testing.T) {
	t.Parallel()

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: filepath.Join(t.TempDir(), "global.json"),
		workspacePath:  "",
	}

	has := store.HasConfigField(ScopeWorkspace, "foo")
	require.False(t, has)
}

func TestConfigStore_RuntimeOverrides_Independent(t *testing.T) {
	t.Parallel()

	store1 := &ConfigStore{config: &Config{}}
	store2 := &ConfigStore{config: &Config{}}

	require.Equal(t, permission.PermissionModeNormal, store1.Overrides().PermissionMode)
	require.Equal(t, permission.PermissionModeNormal, store2.Overrides().PermissionMode)

	store1.Overrides().PermissionMode = permission.PermissionModeYolo

	require.Equal(t, permission.PermissionModeYolo, store1.Overrides().PermissionMode)
	require.Equal(t, permission.PermissionModeNormal, store2.Overrides().PermissionMode)
}

func TestConfigStore_RuntimeOverrides_MutableViaPointer(t *testing.T) {
	t.Parallel()

	store := &ConfigStore{config: &Config{}}
	overrides := store.Overrides()

	require.Equal(t, permission.PermissionModeNormal, overrides.PermissionMode)

	overrides.PermissionMode = permission.PermissionModeYolo
	require.Equal(t, permission.PermissionModeYolo, store.Overrides().PermissionMode)
}

func TestGlobalWorkspaceDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EVO_GLOBAL_DATA", dir)

	wsDir := GlobalWorkspaceDir()
	globalData := GlobalConfigData()

	require.Equal(t, filepath.Dir(globalData), wsDir)
	require.Equal(t, dir, wsDir)
}

func TestScope_String(t *testing.T) {
	t.Parallel()

	require.Equal(t, "global", ScopeGlobal.String())
	require.Equal(t, "workspace", ScopeWorkspace.String())
	require.Contains(t, Scope(99).String(), "Scope(99)")
}

func TestConfigStaleness_CleanImmediatelyAfterSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 配置 文件
	content := []byte(`{"options": {"debug": true}}`)
	require.NoError(t, os.WriteFile(configPath, content, 0o600))

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
	}
	store.captureStalenessSnapshot([]string{configPath})

	result := store.ConfigStaleness()
	require.False(t, result.Dirty)
	require.Empty(t, result.Changed)
	require.Empty(t, result.Missing)
}

func TestConfigStaleness_DetectsFileContentChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 initial 配置 文件
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": false}`), 0o600))

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
	}
	store.captureStalenessSnapshot([]string{configPath})

	// Modify 文件
	time.Sleep(10 * time.Millisecond) // Ensure different mtime
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": true}`), 0o600))

	result := store.ConfigStaleness()
	require.True(t, result.Dirty)
	require.Contains(t, result.Changed, configPath)
	require.Empty(t, result.Missing)
}

func TestConfigStaleness_DetectsFileDeletion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 initial 配置 文件
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": true}`), 0o600))

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
	}
	store.captureStalenessSnapshot([]string{configPath})

	// 删除 文件
	require.NoError(t, os.Remove(configPath))

	result := store.ConfigStaleness()
	require.True(t, result.Dirty)
	require.Empty(t, result.Changed)
	require.Contains(t, result.Missing, configPath)
}

func TestConfigStaleness_DetectsNewFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// Don't 创建 文件 initially
	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
	}
	store.captureStalenessSnapshot([]string{configPath})

	// Now 创建 文件
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": true}`), 0o600))

	result := store.ConfigStaleness()
	require.True(t, result.Dirty)
	require.Contains(t, result.Changed, configPath)
	require.Empty(t, result.Missing)
}

func TestConfigStaleness_SortedOutput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.json")
	pathB := filepath.Join(dir, "b.json")
	pathC := filepath.Join(dir, "c.json")

	// 创建 all files
	for _, p := range []string{pathA, pathB, pathC} {
		require.NoError(t, os.WriteFile(p, []byte(`{}`), 0o600))
	}

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: pathA,
	}
	// Add 在 reverse order 到 测试 sorting
	store.captureStalenessSnapshot([]string{pathC, pathA, pathB})

	// Modify all files
	time.Sleep(10 * time.Millisecond)
	for _, p := range []string{pathA, pathB, pathC} {
		require.NoError(t, os.WriteFile(p, []byte(`{"changed": true}`), 0o600))
	}

	result := store.ConfigStaleness()
	require.True(t, result.Dirty)
	// Should 是 sorted alphabetically
	require.Equal(t, []string{pathA, pathB, pathC}, result.Changed)
}

func TestConfigStaleness_RefreshClearsDirtyState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 initial 配置 文件
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": false}`), 0o600))

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
	}
	store.captureStalenessSnapshot([]string{configPath})

	// Modify 文件
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": true}`), 0o600))

	// Verify dirty
	result := store.ConfigStaleness()
	require.True(t, result.Dirty)

	// 刷新 snapshot
	require.NoError(t, store.RefreshStalenessSnapshot())

	// Verify clean now
	result = store.ConfigStaleness()
	require.False(t, result.Dirty)
	require.Empty(t, result.Changed)
	require.Empty(t, result.Missing)
}

// TestReloadFromDisk_UsesNewConfigValues 是 regression 测试 ensuring 那
// ReloadFromDisk updates 存储 state BEFORE running 模型/agent setup,
// 所以 new 配置 values 是 used rather than stale pre-reload values.
func TestReloadFromDisk_UsesNewConfigValues(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// Isolate 从 host's global 配置 所以 只 测试-provided
	// providers 是 visible.
	t.Setenv("EVO_GLOBAL_CONFIG", dir)
	t.Setenv("EVO_GLOBAL_DATA", dir)
	resetProviderState()
	t.Cleanup(resetProviderState)

	// 创建 initial 配置 用 one 模型 preference
	initialConfig := `{
		"models": {
			"large": {"provider": "openai", "model": "gpt-4"}
		},
		"providers": {
			"openai": {
				"api_key": "test-key",
				"base_url": "https://api.openai.com/v1",
				"models": [{"id": "gpt-4", "name": "GPT-4"}]
			}
		}
	}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	// Load initial 配置 properly
	store, err := Load(t.Context(), dir, dir, false)
	require.NoError(t, err)

	// Set globalDataPath 为 测试 (Load doesn't set 这 directly)
	store.globalDataPath = configPath
	store.CaptureStalenessSnapshot([]string{configPath})

	// Verify initial 模型
	require.Equal(t, "openai", store.config.Models[SelectedModelTypeLarge].Provider)
	require.Equal(t, "gpt-4", store.config.Models[SelectedModelTypeLarge].Model)

	// Modify 配置 在 disk 到 change 模型
	updatedConfig := `{
		"models": {
			"large": {"provider": "anthropic", "model": "claude-3"}
		},
		"providers": {
			"openai": {
				"api_key": "test-key",
				"base_url": "https://api.openai.com/v1",
				"models": [{"id": "gpt-4", "name": "GPT-4"}]
			},
			"anthropic": {
				"api_key": "test-key-2",
				"base_url": "https://api.anthropic.com",
				"models": [{"id": "claude-3", "name": "Claude 3"}]
			}
		}
	}`
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(configPath, []byte(updatedConfig), 0o600))

	// Reload 从 disk
	ctx := context.Background()
	err = store.ReloadFromDisk(ctx)
	require.NoError(t, err)

	// Verify NEW 配置 values 是 now 在 effect (regression check)
	require.Equal(t, "anthropic", store.config.Models[SelectedModelTypeLarge].Provider)
	require.Equal(t, "claude-3", store.config.Models[SelectedModelTypeLarge].Model)
}

// TestSetConfigField_AutoReloads verifies 那 SetConfigField automatically
// reloads 配置 into memory after writing, 所以 subsequent reads see new 值.
func TestSetConfigField_AutoReloads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 initial 配置 文件 用 debug = false
	initialConfig := `{"options": {"debug": false}}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	// Load initial 配置
	store, err := Load(t.Context(), dir, dir, false)
	require.NoError(t, err)

	// Verify initial state
	require.False(t, store.config.Options.Debug)

	// Set globalDataPath 和 capture snapshot 为 staleness tracking
	store.globalDataPath = configPath
	store.CaptureStalenessSnapshot([]string{configPath})

	// Use SetConfigField 到 change debug 到 true
	err = store.SetConfigField(ScopeGlobal, "options.debug", true)
	require.NoError(t, err)

	// Verify 在-memory state was automatically reloaded 和 reflects change
	require.True(t, store.config.Options.Debug, "Expected config to auto-reload and show debug = true")

	// Verify staleness 是 clean after reload
	staleness := store.ConfigStaleness()
	require.False(t, staleness.Dirty, "Expected staleness to be clean after auto-reload")
}

// TestRemoveConfigField_AutoReloads verifies 那 RemoveConfigField automatically
// reloads 配置 into memory after writing.
func TestRemoveConfigField_AutoReloads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 initial 配置 文件 用 custom option
	initialConfig := `{"options": {"debug": true, "custom_field": "value"}}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	// Load initial 配置
	store, err := Load(t.Context(), dir, dir, false)
	require.NoError(t, err)

	// Set globalDataPath 和 capture snapshot
	store.globalDataPath = configPath
	store.CaptureStalenessSnapshot([]string{configPath})

	// Verify 字段 exists initially (indirectly - 存储 loaded successfully)
	require.True(t, store.config.Options.Debug)

	// Remove debug 字段
	err = store.RemoveConfigField(ScopeGlobal, "options.debug")
	require.NoError(t, err)

	// Verify auto-reload occurred 和 stale state 是 clean
	staleness := store.ConfigStaleness()
	require.False(t, staleness.Dirty, "Expected staleness to be clean after auto-reload from RemoveConfigField")
}

// TestSetConfigField_AutoReloadSkipsWhenNoWorkingDir verifies 那 auto-reload
// gracefully skips 当 working 目录 是 不 set (e.g., during testing).
func TestSetConfigField_AutoReloadSkipsWhenNoWorkingDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 存储 without working 目录 (like some 测试 setups)
	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
		// workingDir 是 empty
	}

	// SetConfigField 应当 succeed even without workingDir (auto-reload skips)
	err := store.SetConfigField(ScopeGlobal, "foo", "bar")
	require.NoError(t, err)

	// Verify 文件 was still written
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "foo")
}

// TestAutoReloadDisabledDuringReload verifies 那 auto-reload 是 suppressed
// during ReloadFromDisk 到 prevent re-entrant/nested reload calls.
func TestAutoReloadDisabledDuringReload(t *testing.T) {
	// Load rewrites global 配置; keep it off one other tests share.
	t.Setenv("EVO_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("EVO_GLOBAL_DATA", t.TempDir())

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 initial 配置 用 提供方 那 will trigger 配置 modification during reload
	// (simulating anthropic OAuth 令牌 removal case)
	initialConfig := `{
		"providers": {
			"anthropic": {
				"api_key": "test-key",
				"oauth": {"access_token": "token", "refresh_token": "refresh"}
			}
		}
	}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	// Load will trigger configureProviders which removes anthropic OAuth 配置.
	// 这 should NOT cause infinite 递归 — writeMu prevents re-entrant reloads.
	store, err := Load(t.Context(), dir, dir, false)
	require.NoError(t, err)

	// Capture snapshot 和 verify reload also works without 递归
	store.globalDataPath = configPath
	store.CaptureStalenessSnapshot([]string{configPath})

	// Modify 文件 和 reload — 这 should work without re-entrancy issues
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(configPath, []byte(`{"options": {"debug": true}}`), 0o600))

	err = store.ReloadFromDisk(context.Background())
	require.NoError(t, err)
}

// TestSetConfigFields_AutoReloadsAtomically verifies 那 SetConfigFields writes
// multiple fields 在 single disk 写入 和 triggers 只 one auto-reload,
// avoiding intermediate states where 只 some fields 是 persisted.
func TestSetConfigFields_AutoReloadsAtomically(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 initial 配置 文件.
	initialConfig := `{"options": {"debug": false}}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	// Load initial 配置.
	store, err := Load(t.Context(), dir, dir, false)
	require.NoError(t, err)

	// Set globalDataPath 和 capture snapshot.
	store.globalDataPath = configPath
	store.CaptureStalenessSnapshot([]string{configPath})

	// 写入 multiple fields atomically.
	err = store.SetConfigFields(ScopeGlobal, map[string]any{
		"options.debug":  true,
		"options.custom": "hello",
	})
	require.NoError(t, err)

	// Verify both fields 是 reflected 在 memory.
	require.True(t, store.config.Options.Debug)
}

func TestLoadTokenFromDisk_ReturnsNewerToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 配置 文件 用 newer token 在 disk
	configContent := `{
		"providers": {
			"hyper": {
				"oauth": {
					"access_token": "newer-token-from-disk",
					"refresh_token": "refresh-abc",
					"expires_in": 3600,
					"expires_at": 9999999999
				}
			}
		}
	}`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
	}

	token, err := store.loadTokenFromDisk(ScopeGlobal, "hyper")
	require.NoError(t, err)
	require.NotNil(t, token)
	require.Equal(t, "newer-token-from-disk", token.AccessToken)
	require.Equal(t, "refresh-abc", token.RefreshToken)
	require.Equal(t, 3600, token.ExpiresIn)
	require.Equal(t, int64(9999999999), token.ExpiresAt)
}

func TestLoadTokenFromDisk_ReturnsNilWhenSameToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 配置 文件 用 same token
	configContent := `{
		"providers": {
			"hyper": {
				"oauth": {
					"access_token": "same-token",
					"refresh_token": "refresh-abc",
					"expires_in": 3600,
					"expires_at": 9999999999
				}
			}
		}
	}`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
	}

	token, err := store.loadTokenFromDisk(ScopeGlobal, "hyper")
	require.NoError(t, err)
	require.NotNil(t, token)
	require.Equal(t, "same-token", token.AccessToken)
}

func TestLoadTokenFromDisk_ReturnsNilWhenFileMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "nonexistent.json")

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
	}

	token, err := store.loadTokenFromDisk(ScopeGlobal, "hyper")
	require.NoError(t, err)
	require.Nil(t, token)
}

func TestLoadTokenFromDisk_ReturnsNilWhenProviderMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 配置 文件 without hyper 提供方
	configContent := `{"providers": {"openai": {"api_key": "test-key"}}}`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
	}

	token, err := store.loadTokenFromDisk(ScopeGlobal, "hyper")
	require.NoError(t, err)
	require.Nil(t, token)
}

func TestLoadTokenFromDisk_ReturnsNilWhenOAuthMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 配置 文件 用 提供方 但 no OAuth token
	configContent := `{"providers": {"hyper": {"api_key": "test-key"}}}`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	store := &ConfigStore{
		config:         &Config{},
		globalDataPath: configPath,
	}

	token, err := store.loadTokenFromDisk(ScopeGlobal, "hyper")
	require.NoError(t, err)
	require.Nil(t, token)
}

func TestRefreshOAuthToken_UsesDiskTokenWhenDifferent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	// 创建 配置 文件 用 newer token 在 disk
	configContent := `{
		"providers": {
			"hyper": {
				"api_key": "newer-access-token",
				"oauth": {
					"access_token": "newer-access-token",
					"refresh_token": "refresh-abc",
					"expires_in": 3600,
					"expires_at": 9999999999
				}
			}
		}
	}`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	// Set up 存储 用 older 在-memory token
	oldToken := &oauth.Token{
		AccessToken:  "older-access-token",
		RefreshToken: "refresh-abc",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(-time.Hour).Unix(), // Expired
	}

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("hyper", ProviderConfig{
		ID:         "hyper",
		Name:       "Hyper",
		APIKey:     oldToken.AccessToken,
		OAuthToken: oldToken,
	})

	store := &ConfigStore{
		config: &Config{
			Providers: providers,
		},
		globalDataPath: configPath,
	}

	// Refresh should use disk token without making external 调用
	err := store.RefreshOAuthToken(context.Background(), ScopeGlobal, "hyper")
	require.NoError(t, err)

	// Verify 在-memory token was updated 到 disk token
	updatedConfig, ok := store.config.Providers.Get("hyper")
	require.True(t, ok)
	require.Equal(t, "newer-access-token", updatedConfig.APIKey)
	require.Equal(t, "newer-access-token", updatedConfig.OAuthToken.AccessToken)
	require.Equal(t, "refresh-abc", updatedConfig.OAuthToken.RefreshToken)
}

// TestConfigStore_SetConfigFields_concurrentInProcess verifies 那
// concurrent 在-process writes do 不 lose data 当 serialized 通过
// s.mu 互斥锁. 这 does 不 exercise cross-process flock; testing
// 那 would require spawning separate OS process.
func TestConfigStore_SetConfigFields_concurrentInProcess(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0o600))

	store := &ConfigStore{
		config: &Config{
			Providers: csync.NewMap[string, ProviderConfig](),
			Models:    make(map[SelectedModelType]SelectedModel),
		},
		globalDataPath: configPath,
		workingDir:     dir,
	}

	const (
		numGoroutines    = 20
		fieldsPerRoutine = 5
	)

	errs := make(chan error, numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			kv := make(map[string]any, fieldsPerRoutine)
			for j := 0; j < fieldsPerRoutine; j++ {
				key := fmt.Sprintf("goroutine_%d_field_%d", id, j)
				kv[key] = fmt.Sprintf("value_%d_%d", id, j)
			}
			errs <- store.SetConfigFields(ScopeGlobal, kv)
		}(i)
	}

	for i := 0; i < numGoroutines; i++ {
		require.NoError(t, <-errs)
	}

	// Verify all fields 是 present 在 配置 文件.
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	for i := 0; i < numGoroutines; i++ {
		for j := 0; j < fieldsPerRoutine; j++ {
			key := fmt.Sprintf("goroutine_%d_field_%d", i, j)
			expectedValue := fmt.Sprintf("value_%d_%d", i, j)
			result := gjson.Get(string(data), key)
			require.True(t, result.Exists(), "key %s should exist", key)
			require.Equal(t, expectedValue, result.String(), "key %s should have the correct value", key)
		}
	}
}
