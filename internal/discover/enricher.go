package discover

import (
	"context"
	"sort"

	"charm.land/catwalk/v3/pkg/catwalk"
)

// Enricher fills 在 模型 metadata (上下文 窗口, max tokens, pricing,
// etc.) 为 discovered models. Providers 那 expose richer endpoints
// beyond /v1/models can register Enricher 到 populate fields 那
// standard listing 端点 omits.
type Enricher interface {
	// EnrichModels takes slice 的 bare discovered models 和 returns
	// them 用 metadata populated. Implementations should preserve
	// existing non-zero fields (用户 overrides take precedence).
	EnrichModels(ctx context.Context, cfg Config, resolver Resolver, models []catwalk.Model) ([]catwalk.Model, error)
}

// enrichers maps 提供方 type strings 到 their enrichment
// implementations. Each enricher self-registers via init() 所以 那
// adding new 提供方 requires 只 new 文件 — no changes 到
// load.go 或 any existing enricher.
var enrichers = map[string]Enricher{}

// RegisterEnricher registers Enricher 为 given 提供方 type.
// Called 从 init() 在 each enricher implementation 文件.
func RegisterEnricher(providerType string, e Enricher) {
	enrichers[providerType] = e
}

// GetEnricher returns Enricher 为 given 提供方 type, 或 nil
// 如果 no enricher 是 registered.
func GetEnricher(providerType string) Enricher {
	return enrichers[providerType]
}

// IsKnownCustomProvider reports whether given 提供方 type has
// registered enricher. 这 signals 那 提供方 type 是
// recognized 和 speaks OpenAI-compat protocol, without exposing
// enricher itself 到 callers 那 只 need compatibility check.
func IsKnownCustomProvider(providerType string) bool {
	return enrichers[providerType] != nil
}

// RegisteredProviderTypes returns 提供方 type strings 那 have
// registered enricher, sorted 为 stable output. These 是 custom,
// locally-discovered providers (e.g. ollama, omlx) 那 Crush accepts 作为
// `type` 值 even though they 是 不 catwalk 提供方 types.
// schema generator uses 这 所以 published enum stays 在 sync 用
// registry instead 的 drifting 从 hand-maintained list.
func RegisteredProviderTypes() []string {
	types := make([]string, 0, len(enrichers))
	for providerType := range enrichers {
		types = append(types, providerType)
	}
	sort.Strings(types)
	return types
}
