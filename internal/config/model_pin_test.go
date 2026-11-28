package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// twoProviderConfig 是 配置 文件 naming large 作为 given
// 提供方/模型, 用 both providers available 所以 either choice resolves.
func twoProviderConfig(provider, model string) string {
	return `{
		"models": {
			"large": {"provider": "` + provider + `", "model": "` + model + `"}
		},
		"providers": {
			"openai": {
				"api_key": "test-key",
				"models": [{"id": "gpt-4", "name": "GPT-4"}]
			},
			"anthropic": {
				"api_key": "test-key-2",
				"models": [{"id": "claude-3", "name": "Claude 3"}]
			}
		}
	}`
}

// TestModelSelectionSurvivesPeerWrite 是 regression 测试 为 模型
// switching out 从 under 用户 当 several Crush instances share
// global 配置 文件. 一个 sibling instance selecting different 模型 must
// 不 change ours, even though unrelated 配置 写入 ( token refresh,
// 为 example) reloads 文件 we both 写入 到.
func TestModelSelectionSurvivesPeerWrite(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	t.Setenv("CRUSH_GLOBAL_CONFIG", dir)
	t.Setenv("CRUSH_GLOBAL_DATA", dir)
	resetProviderState()
	t.Cleanup(resetProviderState)

	require.NoError(t, os.WriteFile(configPath, []byte(twoProviderConfig("openai", "gpt-4")), 0o600))

	store, err := Load(t.Context(), dir, dir, false)
	require.NoError(t, err)
	store.globalDataPath = configPath
	store.CaptureStalenessSnapshot([]string{configPath})

	// 用户 picks Claude 在 这 instance.
	require.NoError(t, store.UpdatePreferredModel(ScopeGlobal, SelectedModelTypeLarge, SelectedModel{
		Provider: "anthropic",
		Model:    "claude-3",
	}))

	// 一个 sibling instance then picks GPT-4 和 writes it 到 shared 文件.
	require.NoError(t, os.WriteFile(configPath, []byte(twoProviderConfig("openai", "gpt-4")), 0o600))

	// Any reload (here explicit; 在 practice triggered 通过 unrelated
	// 写入 such 作为 OAuth token refresh) must leave our choice alone.
	require.NoError(t, store.ReloadFromDisk(context.Background()))

	large := store.Config().Models[SelectedModelTypeLarge]
	require.Equal(t, "anthropic", large.Provider)
	require.Equal(t, "claude-3", large.Model)
}

// TestModelSelectionYieldsToDiskWhenUnchosen verifies other half 的
// rule: 模型 type 这 instance never selected still follows 配置
// 文件, 所以 external edits 和 `crush login` defaults keep working.
func TestModelSelectionYieldsToDiskWhenUnchosen(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	t.Setenv("CRUSH_GLOBAL_CONFIG", dir)
	t.Setenv("CRUSH_GLOBAL_DATA", dir)
	resetProviderState()
	t.Cleanup(resetProviderState)

	require.NoError(t, os.WriteFile(configPath, []byte(twoProviderConfig("openai", "gpt-4")), 0o600))

	store, err := Load(t.Context(), dir, dir, false)
	require.NoError(t, err)
	store.globalDataPath = configPath
	store.CaptureStalenessSnapshot([]string{configPath})

	require.NoError(t, os.WriteFile(configPath, []byte(twoProviderConfig("anthropic", "claude-3")), 0o600))
	require.NoError(t, store.ReloadFromDisk(context.Background()))

	large := store.Config().Models[SelectedModelTypeLarge]
	require.Equal(t, "anthropic", large.Provider)
	require.Equal(t, "claude-3", large.Model)
}
