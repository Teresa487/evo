package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestScopeB_InPlaceMutationRace probes whether 存储's 在-place 字段
// mutators (e.g. SetCompactMode) race 用 concurrent 配置() readers 那
// walk same 字段. 这 是 "Scope B" race left open after
// 指针-swap fix. 运行 用 -race.
// TestScopeB_InPlaceMutationRace verifies 那 存储's typed 字段
// mutators (e.g. SetCompactMode) no longer race concurrent 配置()
// readers walking same 字段. Copy-在-写入 publishing means
// mutator swaps 在 fresh 配置 rather than writing through live
// 指针, 所以 reader always sees immutable snapshot. 运行 用 -race.
func TestScopeB_InPlaceMutationRace(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	t.Setenv("CRUSH_GLOBAL_CONFIG", dir)
	t.Setenv("CRUSH_GLOBAL_DATA", dir)
	resetProviderState()
	t.Cleanup(resetProviderState)

	cfg := `{
		"models": {"large": {"provider": "openai", "model": "gpt-4"}},
		"providers": {"openai": {"api_key": "k", "models": [{"id": "gpt-4", "name": "GPT-4"}]}},
		"options": {"tui": {"compact_mode": false}}
	}`
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := Load(t.Context(), dir, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	store.globalDataPath = configPath

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Reader: walks Options.TUI.CompactMode, 字段 SetCompactMode writes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				c := store.Config()
				if c != nil && c.Options != nil {
					_ = c.Options.TUI.CompactMode
				}
			}
		}
	}()

	// Writer: flips compact mode, mutating Options 在 place.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = store.SetCompactMode(ScopeGlobal, i%2 == 0)
		}
		close(stop)
	}()

	wg.Wait()
}
