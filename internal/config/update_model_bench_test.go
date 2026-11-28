package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkUpdatePreferredModel measures full 写入 cost 的 single
// 模型 selection, which 是 what 用户 experiences 当 picking 模型
// 在 TUI. 它 guards against regressing back 到 full 配置 reload 在
// every selection.
func BenchmarkUpdatePreferredModel(b *testing.B) {
	dir := b.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	b.Setenv("EVO_GLOBAL_CONFIG", dir)
	b.Setenv("EVO_GLOBAL_DATA", dir)
	resetProviderState()
	b.Cleanup(resetProviderState)

	cfg := `{
		"models": {
			"large": {"provider": "openai", "model": "gpt-4"},
			"small": {"provider": "openai", "model": "gpt-4o-mini"}
		},
		"providers": {
			"openai": {
				"api_key": "test-key",
				"base_url": "https://api.openai.com/v1",
				"models": [
					{"id": "gpt-4", "name": "GPT-4"},
					{"id": "gpt-4o", "name": "GPT-4o"},
					{"id": "gpt-4o-mini", "name": "GPT-4o mini"}
				]
			},
			"anthropic": {
				"api_key": "test-key-2",
				"base_url": "https://api.anthropic.com",
				"models": [
					{"id": "claude-3", "name": "Claude 3"},
					{"id": "claude-3-5", "name": "Claude 3.5"}
				]
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		b.Fatal(err)
	}

	store, err := Load(b.Context(), dir, dir, false)
	if err != nil {
		b.Fatal(err)
	}
	store.globalDataPath = configPath

	models := []SelectedModel{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
		{Provider: "openai", Model: "gpt-4o"},
		{Provider: "anthropic", Model: "claude-3-5"},
	}

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		m := models[i%len(models)]
		if err := store.UpdatePreferredModel(ScopeGlobal, SelectedModelTypeLarge, m); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// BenchmarkReloadFromDisk isolates full reload cost 为 comparison 用
// BenchmarkUpdatePreferredModel.
func BenchmarkReloadFromDisk(b *testing.B) {
	dir := b.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	b.Setenv("EVO_GLOBAL_CONFIG", dir)
	b.Setenv("EVO_GLOBAL_DATA", dir)
	resetProviderState()
	b.Cleanup(resetProviderState)

	cfg := `{
		"models": {"large": {"provider": "openai", "model": "gpt-4"}},
		"providers": {
			"openai": {"api_key": "test-key", "models": [{"id": "gpt-4", "name": "GPT-4"}]}
		}
	}`
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		b.Fatal(err)
	}

	store, err := Load(b.Context(), dir, dir, false)
	if err != nil {
		b.Fatal(err)
	}
	store.globalDataPath = configPath

	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := store.ReloadFromDisk(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
