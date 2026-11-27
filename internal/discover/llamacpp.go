package discover

import (
	"context"
	"encoding/json"
	"net/http"

	"charm.land/catwalk/v3/pkg/catwalk"
)

func init() {
	RegisterEnricher("llamacpp", &llamacppEnricher{})
}

// llamacppModelsResponse mirrors 响应 从 llama-服务端's
// GET /v1/models 端点. Unlike standard OpenAI listing,
// llama-服务端 embeds meta block 用 模型 architecture details.
type llamacppModelsResponse struct {
	Data []llamacppModelEntry `json:"data"`
}

// llamacppModelEntry 是 single 条目 从 /v1/models.
type llamacppModelEntry struct {
	ID   string       `json:"id"`
	Meta llamacppMeta `json:"meta"`
}

// llamacppMeta holds per-模型 architecture metadata exposed 通过
// llama-服务端 在 /v1/models 响应.
type llamacppMeta struct {
	NCtx      int64 `json:"n_ctx"`
	NCtxTrain int64 `json:"n_ctx_train"`
	NParams   int64 `json:"n_params"`
	Size      int64 `json:"size"`
}

// llamacppEnricher fetches 模型 metadata 从 llama-服务端's
// /v1/models endpoint 和 populates 上下文 窗口 在 discovered
// models. 它 prefers n_ctx ( configured runtime 上下文) 和
// falls back 到 n_ctx_train ( 模型's trained maximum).
type llamacppEnricher struct{}

func (e *llamacppEnricher) EnrichModels(ctx context.Context, cfg Config, resolver Resolver, models []catwalk.Model) ([]catwalk.Model, error) {
	// llama-服务端 serves /v1/models 在 服务端 root, 所以 路径 是
	// built 从 root 和 不 从 configured base URL, which
	// commonly already ends 在 /v1. Without strip 那 配置 asks
	// 为 /v1/v1/models, llama-服务端 answers 404, 和 enricher
	// silently returns models 用 no 上下文 窗口 在 all.
	resp, err := doRequest(ctx, http.MethodGet, stripV1Suffix(cfg.BaseURL), "/v1/models", cfg.APIKey, cfg.ExtraHeaders, resolver, nil)
	if err != nil {
		return models, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return models, nil
	}

	var modelsResp llamacppModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return models, nil
	}

	// Index 通过 ID 为 O(1) lookup.
	metaByID := make(map[string]llamacppMeta, len(modelsResp.Data))
	for _, m := range modelsResp.Data {
		metaByID[m.ID] = m.Meta
	}

	for i := range models {
		meta, ok := metaByID[models[i].ID]
		if !ok {
			continue
		}

		// 上下文 窗口: prefer configured n_ctx, fall back 到
		// 模型's trained maximum.
		if models[i].ContextWindow == 0 {
			if meta.NCtx > 0 {
				models[i].ContextWindow = meta.NCtx
			} else if meta.NCtxTrain > 0 {
				models[i].ContextWindow = meta.NCtxTrain
			}
		}
	}

	return models, nil
}
