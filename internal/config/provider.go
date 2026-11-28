// provider.go —— provider 的解析与可用性判断。
//
// 从产物反推：config.Load → configureProviders 用到这里的逻辑，
// 决定一个 provider 是否可用（有 key、OAuth 已登录、keyless 等）。

package config

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/catwalk/v3/pkg/embedded"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/x/etag"
)

type syncer[T any] interface {
	Get(context.Context) (T, error)
}

// providerList 只缓存 Hyper provider（它的端点与模型列表不在用户配置里，
// 由 OAuth 流程取得后经 UpdateProviderInList 写入）。
var providerList []catwalk.Provider

// 文件 到 缓存 提供方 data. 它 resolves through GlobalConfigData 所以
// catalog follows EVO_GLOBAL_DATA like rest 的 data 目录.
func cachePathFor(name string) string {
	return filepath.Join(filepath.Dir(GlobalConfigData()), name+".json")
}

// UpdateProviders updates Catwalk providers list 从 specified source.
func UpdateProviders(pathOrURL string) error {
	var providers []catwalk.Provider
	pathOrURL = cmp.Or(pathOrURL, os.Getenv("CATWALK_URL"), defaultCatwalkURL)

	switch {
	case pathOrURL == "embedded":
		providers = embedded.GetAll()
	case strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://"):
		var err error
		providers, err = catwalk.NewWithURL(pathOrURL).GetProviders(context.Background(), "")
		if err != nil {
			return fmt.Errorf("failed to fetch providers from Catwalk: %w", err)
		}
	default:
		content, err := os.ReadFile(pathOrURL)
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}
		if err := json.Unmarshal(content, &providers); err != nil {
			return fmt.Errorf("failed to unmarshal provider data: %w", err)
		}
		if len(providers) == 0 {
			return fmt.Errorf("no providers found in the provided source")
		}
	}

	if err := newCache[[]catwalk.Provider](cachePathFor("providers")).Store(providers); err != nil {
		return fmt.Errorf("failed to save providers to cache: %w", err)
	}

	slog.Info("Providers updated successfully", "count", len(providers), "from", pathOrURL, "to", cachePathFor)
	return nil
}

// ResolveHyperAPIKey returns Hyper API key 从 environment 或
// raw 配置 值. env var takes precedence.
func ResolveHyperAPIKey(cfg *Config) string {
	if key := os.Getenv("HYPER_API_KEY"); key != "" {
		return key
	}
	if cfg == nil || cfg.Providers == nil {
		return ""
	}
	pc, ok := cfg.Providers.Get("hyper")
	if !ok {
		return ""
	}
	return pc.APIKey
}

// HyperTokenRefresher 是 function 那 refreshes Hyper OAuth
// token. 它 是 passed 到 Providers 所以 catalog fetch can 重试 在
// 401 without relying 在 package-global state.
type HyperTokenRefresher func(context.Context) error

// UpdateHyper updates Hyper 提供方 information 从 specified URL.
func UpdateHyper(pathOrURL string) error {
	var provider catwalk.Provider
	pathOrURL = cmp.Or(pathOrURL, hyper.BaseURL())

	switch {
	case pathOrURL == "embedded":
		provider = hyper.Embedded()
	case strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://"):
		client := realHyperClient{
			baseURL:    pathOrURL,
			resolveKey: func() string { return ResolveHyperAPIKey(nil) },
		}
		var err error
		provider, err = client.Get(context.Background(), "")
		if err != nil {
			return fmt.Errorf("failed to fetch provider from Hyper: %w", err)
		}
	default:
		content, err := os.ReadFile(pathOrURL)
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}
		if err := json.Unmarshal(content, &provider); err != nil {
			return fmt.Errorf("failed to unmarshal provider data: %w", err)
		}
	}

	if err := newCache[catwalk.Provider](cachePathFor("hyper")).Store(provider); err != nil {
		return fmt.Errorf("failed to save Hyper provider to cache: %w", err)
	}

	slog.Info("Hyper provider updated successfully", "from", pathOrURL, "to", cachePathFor("hyper"))
	return nil
}

var hyperSyncer = &hyperSync{}

// Providers returns list 的 providers, taking into account cached results
// 和 whether 或 不 auto 更新 是 enabled.
//
// 它 will:
// 1. 如果 auto 更新 是 disabled, it'll return embedded providers 在
// time 的 release.
// 2. load the cached providers
// 3. try 到 get fresh list 的 providers, 和 return either 这 new list,
// cached list, 或 embedded list 如果 all others fail.
//
// 一个 returned 错误 是 advisory: it reports 那 catalog could 不 是
// cached, 或 那 upstream returned nothing usable. 它 never means 那 no
// providers 是 available, 所以 callers should surface it 作为 warning 和 keep
// using returned list. 一个 refresh 那 simply could 不 reach network
// 是 不 错误 在 all: cached 或 embedded catalog 是 sound answer, 所以
// those 是 logged 和 fallback 是 returned.
func Providers(cfg *Config) ([]catwalk.Provider, error) {
	return LoadProviders(context.Background(), cfg, nil)
}

// signInProviderIDs lists the providers whose credentials come from an
// OAuth login rather than a hand-written config entry. Their endpoint and
// model catalog cannot be typed into evo.json, so they keep an embedded
// catalog; everything else comes from the user's config only. Hyper leads
// the list because the models dialog expects it at the front.
var signInProviderIDs = []string{
	hyper.Name,
	string(catwalk.InferenceProviderOpenAI),
	string(catwalk.InferenceProviderXAI),
	string(catwalk.InferenceProviderCopilot),
}

// signInProviders returns the embedded catalog for the sign-in providers.
// Keeping just these four means /model lists what the user configured plus
// what they can actually log in to, and a login can still resolve an
// endpoint and model list for a provider that has no config entry yet.
func signInProviders() []catwalk.Provider {
	embeddedProviders := embedded.GetAll()
	providers := make([]catwalk.Provider, 0, len(signInProviderIDs))
	for _, id := range signInProviderIDs {
		if id == hyper.Name {
			providers = append(providers, hyper.Embedded())
			continue
		}
		for _, p := range embeddedProviders {
			if string(p.ID) == id {
				providers = append(providers, p)
				break
			}
		}
	}
	return providers
}

// LoadProviders 返回可用的 provider 列表。
//
// 本仓库不再下载或内置完整 provider 目录，provider 一律来自用户配置文件里
// 的手写定义；唯一的例外是支持登录的 openai、xai、copilot 和 hyper，它们的
// 端点与模型列表无法手写，只能继续内置，否则登录后无法还原 provider。
//
// 这样 /model 里只会出现用户自己配置的 provider，以及可以登录的这几个。
func LoadProviders(ctx context.Context, cfg *Config, refreshHyper HyperTokenRefresher) ([]catwalk.Provider, error) {
	_ = ctx
	_ = cfg
	_ = refreshHyper
	return signInProviders(), nil
}

// UpdateProviderInList replaces 提供方 在 memoized 提供方 list
// returned 通过 Providers(). 这 是 used after re-fetching single
// 提供方 (e.g. Hyper after OAuth) 所以 那 all callers 的 Providers()
// see updated 条目 without needing 到 reset sync.Once.
func UpdateProviderInList(provider catwalk.Provider) {
	for i, p := range providerList {
		if p.ID == provider.ID {
			providerList[i] = provider
			return
		}
	}
	// 提供方 不 found 在 list; prepend it.
	providerList = append([]catwalk.Provider{provider}, providerList...)
}

type cache[T any] struct {
	path string
}

func newCache[T any](path string) cache[T] {
	return cache[T]{path: path}
}

func (c cache[T]) Get() (T, string, error) {
	var v T
	data, err := readFile(c.path)
	if err != nil {
		return v, "", fmt.Errorf("failed to read provider cache file: %w", err)
	}

	if err := json.Unmarshal(data, &v); err != nil {
		return v, "", fmt.Errorf("failed to unmarshal provider data from cache: %w", err)
	}

	return v, etag.Of(data), nil
}

func (c cache[T]) Store(v T) error {
	slog.Info("Saving provider data to disk", "path", c.path)
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory for provider cache: %w", err)
	}

	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal provider data: %w", err)
	}

	// Written through temporary 文件 和 renamed into place. Several Crush
	// instances start independently 和 race 到 refresh 这 缓存, 和
	// truncating 写入 would let one 的 them 读取 half-written catalog 和
	// silently fall back 到 bundled copy.
	if err := atomicWriteFile(c.path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write provider data to cache: %w", err)
	}
	return nil
}
