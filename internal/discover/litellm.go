package discover

import (
	"context"
	"encoding/json"
	"net/http"

	"charm.land/catwalk/v3/pkg/catwalk"
)

// litellmModelInfoResponse mirrors 响应 从 LiteLLM's
// /模型/info endpoint, which returns rich metadata including 上下文
// windows, max tokens, 和 pricing.
type litellmModelInfoResponse struct {
	Data []litellmModelInfo `json:"data"`
}

// litellmModelInfo 是 single 条目 从 /模型/info.
type litellmModelInfo struct {
	ModelName string           `json:"model_name"`
	ModelInfo litellmModelMeta `json:"model_info"`
}

// litellmModelMeta holds metadata fields we care about 从
// LiteLLM's model_info block.
type litellmModelMeta struct {
	MaxInputTokens     *int64   `json:"max_input_tokens"`
	MaxOutputTokens    *int64   `json:"max_output_tokens"`
	InputCostPerToken  *float64 `json:"input_cost_per_token"`
	OutputCostPerToken *float64 `json:"output_cost_per_token"`
	Mode               string   `json:"mode"`
}

func init() {
	RegisterEnricher("litellm", &litellmEnricher{})
}

// litellmEnricher fetches 模型 metadata 从 LiteLLM's /模型/info
// endpoint 和 populates 上下文 窗口, max tokens, 和 pricing 在
// discovered models.
type litellmEnricher struct{}

func (e *litellmEnricher) EnrichModels(ctx context.Context, cfg Config, resolver Resolver, models []catwalk.Model) ([]catwalk.Model, error) {
	resp, err := doRequest(ctx, http.MethodGet, stripV1Suffix(cfg.BaseURL), "/model/info", cfg.APIKey, cfg.ExtraHeaders, resolver, nil)
	if err != nil {
		return models, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return models, nil
	}

	var infoResp litellmModelInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&infoResp); err != nil {
		return models, nil
	}

	// Index metadata 通过 模型 name 为 O(1) lookup.
	metaByID := make(map[string]litellmModelMeta, len(infoResp.Data))
	for _, entry := range infoResp.Data {
		metaByID[entry.ModelName] = entry.ModelInfo
	}

	// Apply metadata 到 discovered models, preserving existing
	// non-zero values (用户 overrides win).
	for i := range models {
		meta, ok := metaByID[models[i].ID]
		if !ok {
			continue
		}
		if models[i].ContextWindow == 0 && meta.MaxInputTokens != nil {
			models[i].ContextWindow = *meta.MaxInputTokens
		}
		if models[i].DefaultMaxTokens == 0 && meta.MaxOutputTokens != nil {
			models[i].DefaultMaxTokens = *meta.MaxOutputTokens
		}
		if models[i].Pricing.Input == 0 && meta.InputCostPerToken != nil {
			models[i].Pricing.Input = *meta.InputCostPerToken * 1_000_000
		}
		if models[i].Pricing.Output == 0 && meta.OutputCostPerToken != nil {
			models[i].Pricing.Output = *meta.OutputCostPerToken * 1_000_000
		}
	}

	return models, nil
}
