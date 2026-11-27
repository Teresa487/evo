package discover

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"

	"charm.land/catwalk/v3/pkg/catwalk"
)

func init() {
	RegisterEnricher("ollama", &ollamaEnricher{})
}

// ollamaShowResponse mirrors 响应 从 Ollama's POST /api/show
// endpoint. Only fields we care about 是 decoded.
type ollamaShowResponse struct {
	ModelInfo    map[string]any `json:"model_info"`
	Capabilities []string       `json:"capabilities"`
}

// ollamaEnricher fetches 模型 metadata 从 Ollama's /api/show
// endpoint 和 populates 上下文 窗口 在 discovered models. 一个 模型
// Ollama says cannot 调用 tools 是 left out, since Crush sends tools 在
// every 请求 和 Ollama refuses them 为 such 模型.
type ollamaEnricher struct{}

func (e *ollamaEnricher) EnrichModels(ctx context.Context, cfg Config, resolver Resolver, models []catwalk.Model) ([]catwalk.Model, error) {
	// Collect indices 那 need enrichment.
	var needEnrichment []int
	for i := range models {
		if models[i].ContextWindow == 0 {
			needEnrichment = append(needEnrichment, i)
		}
	}
	if len(needEnrichment) == 0 {
		return models, nil
	}

	// Fetch metadata concurrently 用 bounded parallelism.
	type result struct {
		index         int
		contextLength int64
		noTools       bool
	}

	results := make([]result, len(needEnrichment))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5) // Max 5 concurrent requests.

	for ri, idx := range needEnrichment {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			resp, err := doRequest(ctx, http.MethodPost, stripV1Suffix(cfg.BaseURL), "/api/show",
				cfg.APIKey, cfg.ExtraHeaders, resolver,
				map[string]string{"model": models[idx].ID})
			if err != nil {
				return
			}
			defer resp.Body.Close()

			var showResp ollamaShowResponse
			if err := json.NewDecoder(resp.Body).Decode(&showResp); err != nil {
				return
			}

			results[ri] = result{
				index:         idx,
				contextLength: extractContextLength(showResp.ModelInfo),
				noTools:       lacksTools(showResp.Capabilities),
			}
		})
	}
	wg.Wait()

	var dropped []int
	for _, r := range results {
		if r.noTools {
			slog.Warn("Leaving out an Ollama model that cannot call tools",
				"provider", cfg.ID, "model", models[r.index].ID,
				"hint", "its template has no tools; Ollama's /api/show lists its capabilities")
			dropped = append(dropped, r.index)
			continue
		}
		if r.contextLength > 0 {
			models[r.index].ContextWindow = r.contextLength
		}
	}
	if len(dropped) == 0 {
		return models, nil
	}
	kept := make([]catwalk.Model, 0, len(models)-len(dropped))
	for i, m := range models {
		if !slices.Contains(dropped, i) {
			kept = append(kept, m)
		}
	}
	return kept, nil
}

// lacksTools reports whether Ollama's capabilities 为 模型 say it
// cannot 调用 tools. An Ollama too old 到 报告 capabilities says
// nothing, 和 模型 是 kept.
func lacksTools(capabilities []string) bool {
	return len(capabilities) > 0 && !slices.Contains(capabilities, "tools")
}

// extractContextLength finds context_length 值 在 Ollama's
// model_info map. key 是 architecture-specific (e.g.
// "llama.context_length", "qwen2.context_length"), 所以 we scan 为 any
// key ending 在 ".context_length".
func extractContextLength(info map[string]any) int64 {
	for k, v := range info {
		if !strings.HasSuffix(k, ".context_length") {
			continue
		}
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int64:
			return n
		case json.Number:
			i, err := n.Int64()
			if err == nil {
				return i
			}
		}
	}
	return 0
}
