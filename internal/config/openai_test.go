package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestGetModelIncludesChatGPTModels(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("openai", ProviderConfig{
		Models:        []catwalk.Model{{ID: "gpt-5.1"}},
		ChatGPTModels: []catwalk.Model{{ID: "gpt-5.1-codex", Name: "GPT-5.1 Codex"}},
	})
	cfg := &Config{Providers: providers}

	model := cfg.GetModel("openai", "gpt-5.1-codex")
	require.NotNil(t, model)
	require.Equal(t, "GPT-5.1 Codex", model.Name)

	require.NotNil(t, cfg.GetModel("openai", "gpt-5.1"))
	require.Nil(t, cfg.GetModel("openai", "missing"))
}

func TestTokenFields(t *testing.T) {
	t.Parallel()

	t.Run("openai keeps the api key", func(t *testing.T) {
		t.Parallel()

		fields := tokenFields("openai", &oauth.Token{AccessToken: "at"})
		require.Contains(t, fields, "providers.openai.oauth")
		require.NotContains(t, fields, "providers.openai.api_key")
	})

	t.Run("other providers mirror the access token", func(t *testing.T) {
		t.Parallel()

		fields := tokenFields("hyper", &oauth.Token{AccessToken: "at"})
		require.Contains(t, fields, "providers.hyper.oauth")
		require.Equal(t, "at", fields["providers.hyper.api_key"])
	})
}

func TestTokenAccountIDRoundTrip(t *testing.T) {
	t.Parallel()

	// account ID 是 persisted 用 token 在 crush.json; verify
	// it survives the JSON round trip loadTokenFromDisk performs.
	token := &oauth.Token{
		AccessToken:  "at",
		RefreshToken: "rt",
		IDToken:      "idt",
		AccountID:    "acct-42",
		ExpiresIn:    3600,
		ExpiresAt:    4102444800,
	}

	data, err := json.Marshal(token)
	require.NoError(t, err)

	var restored oauth.Token
	require.NoError(t, json.Unmarshal(data, &restored))
	require.Equal(t, "acct-42", restored.AccountID)
	require.Equal(t, "idt", restored.IDToken)
	require.Equal(t, "rt", restored.RefreshToken)
}

// testResolver mimics shell resolver 为 templates: variables
// resolve 到 empty 当 unset, literals pass through.
type testResolver struct{}

func (testResolver) ResolveValue(v string) (string, error) {
	if strings.HasPrefix(v, "$") {
		return "", nil
	}
	return v, nil
}

func TestHasAPIKey(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()

		require.False(t, (&ProviderConfig{}).HasAPIKey(testResolver{}))
	})

	t.Run("literal key", func(t *testing.T) {
		t.Parallel()

		require.True(t, (&ProviderConfig{APIKey: "sk-key"}).HasAPIKey(testResolver{}))
	})

	t.Run("unresolved template", func(t *testing.T) {
		t.Parallel()

		require.False(t, (&ProviderConfig{APIKey: "$OPENAI_API_KEY"}).HasAPIKey(testResolver{}))
	})
}

// TestSetProviderAPIKeyOpenAIIsEitherOr proves OpenAI 提供方 holds
// exactly one credential: a ChatGPT login retires a previously entered
// API key, 和 entering API key retires previous ChatGPT login.
// Copilot keeps mirroring access token into api_key 作为 before.
func TestSetProviderAPIKeyOpenAIIsEitherOr(t *testing.T) {
	// Not parallel: t.Setenv below.

	// Point 配置 discovery 在 测试 sandbox: 写入 below
	// triggers auto-reload, which must 不 pick up developer's
	// real crush.json.
	t.Setenv("CRUSH_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("CRUSH_GLOBAL_DATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	token := &oauth.Token{
		AccessToken:  "chatgpt-at",
		RefreshToken: "chatgpt-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}

	newStore := func(t *testing.T, providerID string, initialConfig string) *ConfigStore {
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

		originalFetch := fetchOpenAIModels
		fetchOpenAIModels = func(context.Context, *oauth.Token) ([]catwalk.Model, error) {
			return []catwalk.Model{{ID: "gpt-5.1-codex"}}, nil
		}
		t.Cleanup(func() { fetchOpenAIModels = originalFetch })

		return &ConfigStore{
			config:         &Config{Providers: providers},
			globalDataPath: configPath,
			workingDir:     dir,
		}
	}

	t.Run("chatgpt login retires the api key", func(t *testing.T) {
		store := newStore(t, "openai", `{
			"providers": {
				"openai": {
					"id": "openai",
					"api_key": "sk-keep",
					"models": [{"id": "gpt-5.1", "name": "GPT-5.1"}]
				}
			}
		}`)

		require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, "openai", token))

		pc, ok := store.Config().Providers.Get("openai")
		require.True(t, ok)
		require.Empty(t, pc.APIKey, "the ChatGPT login replaces the API key")
		require.Equal(t, token, pc.OAuthToken)
		require.Equal(t, "gpt-5.1-codex", pc.ChatGPTModels[0].ID, "the subscription catalog lands in its own field")
		require.Equal(t, "gpt-5.1", pc.Models[0].ID, "the API catalog is untouched")

		disk, err := os.ReadFile(store.globalDataPath)
		require.NoError(t, err)
		require.NotContains(t, string(disk), "sk-keep", "the retired key is gone from the config file")
		require.Contains(t, string(disk), "chatgpt-rt", "the login is persisted")
	})

	t.Run("api key retires the chatgpt login", func(t *testing.T) {
		store := newStore(t, "openai", `{
			"providers": {
				"openai": {
					"id": "openai",
					"api_key": "",
					"oauth": {"access_token": "old-at", "refresh_token": "old-rt"},
					"chatgpt_models": [{"id": "gpt-5.6-luna"}]
				}
			}
		}`)

		require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, "openai", "sk-new"))

		pc, ok := store.Config().Providers.Get("openai")
		require.True(t, ok)
		require.Equal(t, "sk-new", pc.APIKey)
		require.Nil(t, pc.OAuthToken, "the API key replaces the ChatGPT login")
		require.Empty(t, pc.ChatGPTModels, "the subscription catalog goes with it")

		disk, err := os.ReadFile(store.globalDataPath)
		require.NoError(t, err)
		require.NotContains(t, string(disk), "old-rt", "the retired login is gone from the config file")
		require.NotContains(t, string(disk), "gpt-5.6-luna")
	})

	t.Run("copilot", func(t *testing.T) {
		store := newStore(t, "copilot", `{"providers":{"copilot":{"id":"copilot"}}}`)

		require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, "copilot", token))

		pc, ok := store.Config().Providers.Get("copilot")
		require.True(t, ok)
		require.Equal(t, "chatgpt-at", pc.APIKey, "copilot still mirrors the access token into api_key")
		require.Equal(t, token, pc.OAuthToken)
	})
}

// TestRefetchOpenAIModelsRefreshesExpiredToken pins behavior 的
// catalog refresh 当 存储 ChatGPT token has expired: refresh
// renews token before fetching, 所以 请求 是 不 rejected 用
// 401, 和 fetched catalog replaces persisted one.
func TestRefetchOpenAIModelsRefreshesExpiredToken(t *testing.T) {
	// Not parallel: swaps the fetchOpenAIModels package variable.
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
	providers.Set("openai", ProviderConfig{
		ID:            "openai",
		OAuthToken:    expired,
		ChatGPTModels: []catwalk.Model{{ID: "gpt-stale"}},
	})
	store := &ConfigStore{
		config:         &Config{Providers: providers},
		globalDataPath: configPath,
		exchangeToken: func(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error) {
			require.Equal(t, "openai", providerID)
			require.Equal(t, "expired-rt", refreshToken)
			return fresh, nil
		},
	}

	originalFetch := fetchOpenAIModels
	var fetchToken *oauth.Token
	fetchOpenAIModels = func(_ context.Context, token *oauth.Token) ([]catwalk.Model, error) {
		fetchToken = token
		return []catwalk.Model{{ID: "gpt-fresh"}}, nil
	}
	t.Cleanup(func() { fetchOpenAIModels = originalFetch })

	store.refetchOpenAIModels(t.Context(), ScopeGlobal)

	require.Equal(t, fresh, fetchToken, "the catalog is fetched with the renewed token")

	pc, ok := store.Config().Providers.Get("openai")
	require.True(t, ok)
	require.Equal(t, fresh, pc.OAuthToken, "the renewed token is kept in the config")
	require.Equal(t, "gpt-fresh", pc.ChatGPTModels[0].ID, "the existing catalog is replaced")

	disk, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(disk), "gpt-fresh", "the fetched catalog is persisted")
	require.Contains(t, string(disk), "fresh-rt", "the renewed token is persisted")
}

// chatGPTLoginConfig 是 project 配置 为 ChatGPT login whose
// persisted 模型 catalog has gone stale.
func chatGPTLoginConfig(token *oauth.Token) string {
	tokenJSON, err := json.Marshal(token)
	if err != nil {
		panic(err)
	}
	return `{
		"providers": {
			"openai": {
				"id": "openai",
				"oauth": ` + string(tokenJSON) + `,
				"chatgpt_models": [{"id": "gpt-stale", "name": "GPT Stale"}]
			}
		}
	}`
}

// chatGPTLoginToken returns token 那 是 valid long enough 为 测试
// 运行, 所以 catalog refresh does 不 spend time renewing it.
func chatGPTLoginToken() *oauth.Token {
	return &oauth.Token{
		AccessToken:  "chatgpt-at",
		RefreshToken: "chatgpt-rt",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}
}

// isolateLoadEnv points 配置 discovery 在 throwaway directories.
func isolateLoadEnv(t *testing.T) (workDir, dataDir string) {
	t.Helper()

	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "crush"))
	return t.TempDir(), t.TempDir()
}

// stubChatGPTModels replaces ChatGPT catalog fetch 用 one 那
// reports whether it ran 和 what it returns.
func stubChatGPTModels(t *testing.T, models []catwalk.Model) *bool {
	t.Helper()

	originalFetch := fetchOpenAIModels
	called := false
	fetchOpenAIModels = func(context.Context, *oauth.Token) ([]catwalk.Model, error) {
		called = true
		return models, nil
	}
	t.Cleanup(func() { fetchOpenAIModels = originalFetch })
	return &called
}

// TestLoadRefreshesChatGPTModelsOnStartup covers the startup
// wiring: every load re-fetches the ChatGPT 模型 catalog, 和
// 配置 Load publishes afterwards still carries resolved 默认
// models ( refresh swaps 配置 out 从 under Load, which must
// re-读取 it).
func TestLoadRefreshesChatGPTModelsOnStartup(t *testing.T) {
	// Not parallel: swaps the fetch stub 和 sets environment
	// variables.
	workDir, dataDir := isolateLoadEnv(t)
	resetProviderState()
	t.Cleanup(resetProviderState)

	require.NoError(t, os.WriteFile(
		filepath.Join(workDir, "evo.json"),
		[]byte(chatGPTLoginConfig(chatGPTLoginToken())),
		0o600,
	))

	fetched := stubChatGPTModels(t, []catwalk.Model{{ID: "gpt-fresh", Name: "GPT Fresh"}})

	store, err := Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err)

	require.True(t, *fetched, "startup refreshes the ChatGPT catalog")

	pc, ok := store.Config().Providers.Get("openai")
	require.True(t, ok)
	require.Equal(t, "gpt-fresh", pc.ChatGPTModels[0].ID, "the stale catalog is replaced")
}

// TestLoadKeepsChatGPTModelsWhenFetchFails verifies 那 failed
// startup refresh leaves persisted 目录 alone. Load never fails
// over it: login still works 用 whatever catalog 是 already there.
func TestLoadKeepsChatGPTModelsWhenFetchFails(t *testing.T) {
	// Not parallel: swaps the fetch stub 和 sets environment
	// variables.
	workDir, dataDir := isolateLoadEnv(t)
	resetProviderState()
	t.Cleanup(resetProviderState)

	require.NoError(t, os.WriteFile(
		filepath.Join(workDir, "evo.json"),
		[]byte(chatGPTLoginConfig(chatGPTLoginToken())),
		0o600,
	))

	originalFetch := fetchOpenAIModels
	fetchOpenAIModels = func(context.Context, *oauth.Token) ([]catwalk.Model, error) {
		return nil, errors.New("catalog unavailable")
	}
	t.Cleanup(func() { fetchOpenAIModels = originalFetch })

	store, err := Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err, "a failed catalog refresh must not fail the load")

	pc, ok := store.Config().Providers.Get("openai")
	require.True(t, ok)
	require.Equal(t, "gpt-stale", pc.ChatGPTModels[0].ID, "the persisted catalog is kept")
}
