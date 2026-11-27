package discover

import (
	"context"
	"encoding/json"
	"net/http"

	"charm.land/catwalk/v3/pkg/catwalk"
)

func init() {
	RegisterEnricher("lmstudio", &lmstudioEnricher{})
}

// lmstudioModelsResponse mirrors 响应 从 LM Studio's native
// GET /api/v1/models endpoint. 模型 array 是 returned under
// "models" key (不 "data" like OpenAI-compatible endpoint). Only
// fields we care about 是 decoded.
type lmstudioModelsResponse struct {
	Models []lmstudioModelEntry `json:"models"`
}

// lmstudioModelEntry 是 single 条目 从 /api/v1/models.
type lmstudioModelEntry struct {
	Key              string               `json:"key"`
	DisplayName      string               `json:"display_name"`
	MaxContextLength int64                `json:"max_context_length"`
	LoadedInstances  []lmstudioInstance   `json:"loaded_instances"`
	Capabilities     lmstudioCapabilities `json:"capabilities"`
}

// lmstudioCapabilities holds optional 模型 能力 flags 从
// LM Studio's /api/v1/models 端点.
type lmstudioCapabilities struct {
	Vision bool `json:"vision"`
}

// lmstudioInstance 是 currently loaded 模型 instance 用 它的
// runtime 配置.
type lmstudioInstance struct {
	Config lmstudioInstanceConfig `json:"config"`
}

// lmstudioInstanceConfig holds per-instance runtime settings.
type lmstudioInstanceConfig struct {
	ContextLength int64 `json:"context_length"`
}

// lmstudioEnricher fetches 模型 metadata 从 LM Studio's native
// /api/v1/models endpoint 和 populates 上下文 窗口, display name,
// 和 vision support 在 discovered models.
type lmstudioEnricher struct{}

func (e *lmstudioEnricher) EnrichModels(ctx context.Context, cfg Config, resolver Resolver, models []catwalk.Model) ([]catwalk.Model, error) {
	resp, err := doRequest(ctx, http.MethodGet, stripV1Suffix(cfg.BaseURL), "/api/v1/models", cfg.APIKey, cfg.ExtraHeaders, resolver, nil)
	if err != nil {
		return models, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return models, nil
	}

	var modelsResp lmstudioModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return models, nil
	}

	// Index 通过 key 为 O(1) lookup.
	metaByKey := make(map[string]lmstudioModelEntry, len(modelsResp.Models))
	for _, m := range modelsResp.Models {
		metaByKey[m.Key] = m
	}

	for i := range models {
		meta, ok := metaByKey[models[i].ID]
		if !ok {
			continue
		}

		// 上下文 窗口: prefer loaded instance 配置, fall back
		// 到 模型-level max.
		if models[i].ContextWindow == 0 {
			if len(meta.LoadedInstances) > 0 && meta.LoadedInstances[0].Config.ContextLength > 0 {
				models[i].ContextWindow = meta.LoadedInstances[0].Config.ContextLength
			} else if meta.MaxContextLength > 0 {
				models[i].ContextWindow = meta.MaxContextLength
			}
		}

		// Display name 如果 不 already set 通过 用户.
		if models[i].Name == models[i].ID && meta.DisplayName != "" {
			models[i].Name = meta.DisplayName
		}

		// Vision support 从 capabilities, 如果 不 already set 通过 用户.
		if !models[i].Capabilities.Vision {
			models[i].Capabilities.Vision = meta.Capabilities.Vision
		}
	}

	return models, nil
}
