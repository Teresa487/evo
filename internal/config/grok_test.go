package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/stretchr/testify/require"
)

// TestSetProviderAPIKeyXAIIsEitherOr proves xAI 提供方 holds exactly
// one credential: a Grok login replaces a previously entered API key (the
// access token mirrors into it), 和 entering API key retires
// previous Grok login 所以 它的 refreshes cannot shadow key.
func TestSetProviderAPIKeyXAIIsEitherOr(t *testing.T) {
	// Not parallel: t.Setenv below.

	// Point 配置 discovery 在 测试 sandbox: 写入 below
	// triggers auto-reload, which must 不 pick up developer's
	// real crush.json.
	t.Setenv("EVO_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("EVO_GLOBAL_DATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	token := &oauth.Token{
		AccessToken:  "grok-at",
		RefreshToken: "grok-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}

	newStore := func(t *testing.T, initialConfig string) *ConfigStore {
		t.Helper()
		dir := t.TempDir()
		configPath := filepath.Join(dir, "evo.json")
		require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

		// 在-memory 配置 mirrors what real load would produce.
		// SetConfigFields auto-reloads 从 configPath, 所以 any state
		// assertions expect 到 survive must 是 在 disk.
		providers := csync.NewMap[string, ProviderConfig]()
		require.NoError(t, json.Unmarshal([]byte(initialConfig), &struct {
			Providers *csync.Map[string, ProviderConfig] `json:"providers"`
		}{Providers: providers}))

		originalFetch := fetchGrokModels
		fetchGrokModels = func(context.Context, *oauth.Token) ([]catwalk.Model, error) {
			return []catwalk.Model{{ID: "grok-4-fast"}}, nil
		}
		t.Cleanup(func() { fetchGrokModels = originalFetch })

		return &ConfigStore{
			config:         &Config{Providers: providers},
			globalDataPath: configPath,
			workingDir:     dir,
		}
	}

	t.Run("grok login replaces the api key", func(t *testing.T) {
		store := newStore(t, `{
			"providers": {
				"xai": {
					"id": "xai",
					"api_key": "sk-keep",
					"models": [{"id": "grok-4.5", "name": "Grok 4.5"}]
				}
			}
		}`)

		require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, "xai", token))

		pc, ok := store.Config().Providers.Get("xai")
		require.True(t, ok)
		// access token mirrors into api_key 为 non-OpenAI
		// providers: it 是 credential API 客户端 sends.
		require.Equal(t, token.AccessToken, pc.APIKey)
		require.Equal(t, token, pc.OAuthToken)
		require.Equal(t, "grok-4-fast", pc.GrokModels[0].ID, "the subscription catalog lands in its own field")
		require.Equal(t, "grok-4.5", pc.Models[0].ID, "the API catalog is untouched")

		disk, err := os.ReadFile(store.globalDataPath)
		require.NoError(t, err)
		require.NotContains(t, string(disk), "sk-keep", "the retired key is gone from the config file")
		require.Contains(t, string(disk), "grok-rt", "the login is persisted")
		require.Contains(t, string(disk), "grok-4-fast", "the fetched catalog is persisted")
	})

	t.Run("api key retires the grok login", func(t *testing.T) {
		store := newStore(t, `{
			"providers": {
				"xai": {
					"id": "xai",
					"api_key": "",
					"oauth": {"access_token": "old-at", "refresh_token": "old-rt"},
					"grok_models": [{"id": "grok-4.20"}]
				}
			}
		}`)

		require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, "xai", "sk-new"))

		pc, ok := store.Config().Providers.Get("xai")
		require.True(t, ok)
		require.Equal(t, "sk-new", pc.APIKey)
		require.Nil(t, pc.OAuthToken, "the API key retires the Grok login")
		require.Empty(t, pc.GrokModels, "the subscription catalog goes with it")

		disk, err := os.ReadFile(store.globalDataPath)
		require.NoError(t, err)
		require.NotContains(t, string(disk), "old-rt", "the retired login is gone from the config file")
		require.NotContains(t, string(disk), "grok-4.20", "the retired catalog is gone from the config file")
		require.Contains(t, string(disk), "sk-new")
	})
}

// TestRefetchGrokModelsRefreshesExpiredToken pins behavior 的
// catalog refresh 当 存储 Grok token has expired: refresh
// renews token before fetching, 所以 请求 是 不 rejected 用
// 401, 和 fetched catalog replaces persisted one.
func TestRefetchGrokModelsRefreshesExpiredToken(t *testing.T) {
	// Not parallel: swaps the fetchGrokModels package variable.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "evo.json")

	expired := &oauth.Token{
		AccessToken:  "expired-at",
		RefreshToken: "expired-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(-time.Hour).Unix(),
	}
	fresh := &oauth.Token{
		AccessToken:  "fresh-at",
		RefreshToken: "fresh-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("xai", ProviderConfig{
		ID:         "xai",
		OAuthToken: expired,
		GrokModels: []catwalk.Model{{ID: "grok-stale"}},
	})
	store := &ConfigStore{
		config:         &Config{Providers: providers},
		globalDataPath: configPath,
		exchangeToken: func(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error) {
			require.Equal(t, "xai", providerID)
			require.Equal(t, "expired-rt", refreshToken)
			return fresh, nil
		},
	}

	originalFetch := fetchGrokModels
	var fetchToken *oauth.Token
	fetchGrokModels = func(_ context.Context, token *oauth.Token) ([]catwalk.Model, error) {
		fetchToken = token
		return []catwalk.Model{{ID: "grok-fresh"}}, nil
	}
	t.Cleanup(func() { fetchGrokModels = originalFetch })

	store.refetchGrokModels(t.Context(), ScopeGlobal)

	require.Equal(t, fresh, fetchToken, "the catalog is fetched with the renewed token")

	pc, ok := store.Config().Providers.Get("xai")
	require.True(t, ok)
	require.Equal(t, fresh, pc.OAuthToken, "the renewed token is kept in the config")
	require.Equal(t, "grok-fresh", pc.GrokModels[0].ID, "the existing catalog is replaced")

	disk, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(disk), "grok-fresh", "the fetched catalog is persisted")
	require.Contains(t, string(disk), "fresh-rt", "the renewed token is persisted")
}

// grokLoginConfig 是 project 配置 为 Grok login whose persisted
// 模型 catalog has gone stale.
func grokLoginConfig(token *oauth.Token) string {
	tokenJSON, err := json.Marshal(token)
	if err != nil {
		panic(err)
	}
	return `{
		"providers": {
			"xai": {
				"id": "xai",
				"oauth": ` + string(tokenJSON) + `,
				"grok_models": [{"id": "grok-stale", "name": "Grok Stale"}]
			}
		}
	}`
}

// stubGrokModels replaces Grok catalog fetch 用 one 那 reports
// whether it ran 和 what it returns.
func stubGrokModels(t *testing.T, models []catwalk.Model) *bool {
	t.Helper()

	originalFetch := fetchGrokModels
	called := false
	fetchGrokModels = func(context.Context, *oauth.Token) ([]catwalk.Model, error) {
		called = true
		return models, nil
	}
	t.Cleanup(func() { fetchGrokModels = originalFetch })
	return &called
}

// TestLoadRefreshesGrokModelsOnStartup covers the startup wiring:
// every load re-fetches the Grok 模型 catalog, 和 配置 Load
// publishes afterwards still carries resolved 默认 models
// ( refresh swaps 配置 out 从 under Load, which must
// re-读取 it).
func TestLoadRefreshesGrokModelsOnStartup(t *testing.T) {
	// Not parallel: swaps the fetch stub 和 sets environment
	// variables.
	workDir, dataDir := isolateLoadEnv(t)
	resetProviderState()
	t.Cleanup(resetProviderState)

	require.NoError(t, os.WriteFile(
		filepath.Join(workDir, "evo.json"),
		[]byte(grokLoginConfig(chatGPTLoginToken())),
		0o600,
	))

	fetched := stubGrokModels(t, []catwalk.Model{{ID: "grok-fresh", Name: "Grok Fresh"}})

	store, err := Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err)

	require.True(t, *fetched, "startup refreshes the Grok catalog")

	pc, ok := store.Config().Providers.Get("xai")
	require.True(t, ok)
	require.Equal(t, "grok-fresh", pc.GrokModels[0].ID, "the stale catalog is replaced")
}

// TestLoadKeepsGrokModelsWhenFetchFails verifies 那 failed startup
// refresh leaves persisted 目录 alone. Load never fails over it:
// login still works 用 whatever catalog 是 already there.
func TestLoadKeepsGrokModelsWhenFetchFails(t *testing.T) {
	// Not parallel: swaps the fetch stub 和 sets environment
	// variables.
	workDir, dataDir := isolateLoadEnv(t)
	resetProviderState()
	t.Cleanup(resetProviderState)

	require.NoError(t, os.WriteFile(
		filepath.Join(workDir, "evo.json"),
		[]byte(grokLoginConfig(chatGPTLoginToken())),
		0o600,
	))

	originalFetch := fetchGrokModels
	fetchGrokModels = func(context.Context, *oauth.Token) ([]catwalk.Model, error) {
		return nil, errors.New("catalog unavailable")
	}
	t.Cleanup(func() { fetchGrokModels = originalFetch })

	store, err := Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err, "a failed catalog refresh must not fail the load")

	pc, ok := store.Config().Providers.Get("xai")
	require.True(t, ok)
	require.Equal(t, "grok-stale", pc.GrokModels[0].ID, "the persisted catalog is kept")
}
