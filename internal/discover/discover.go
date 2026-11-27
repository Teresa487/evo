package discover

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
)

// httpClient 是 shared across all discovery 和 enrichment calls. 它
// has reasonable 超时 所以 individual requests cannot block forever
// even 如果 caller forgets 到 set 上下文 期限.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// stripV1Suffix removes trailing /v1 从 base URL. Enricher
// endpoints (e.g. Ollama's /api/show, LM Studio's /api/v1/models) 是
// served 在 服务端 root, 不 under OpenAI-compatible /v1
// prefix. Since 提供方 configs typically include /v1 在 base URL
// 为 chat completions, enrichers must strip it before constructing
// their own 请求 paths.
func stripV1Suffix(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
}

// doRequest builds 和 executes authenticated HTTP 请求 using
// shared 客户端. 它 resolves variable references 在 base URL, API
// key, 和 extra headers via provided Resolver. 路径 是 joined
// 到 base URL 用 proper slash handling.
func doRequest(ctx context.Context, method, baseURL, path, apiKey string, extraHeaders map[string]string, resolver Resolver, body any) (*http.Response, error) {
	resolvedBase, _ := resolver.ResolveValue(baseURL)
	resolvedKey, _ := resolver.ResolveValue(apiKey)

	url := strings.TrimRight(resolvedBase, "/") + "/" + strings.TrimLeft(path, "/")

	var reqBody *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	var req *http.Request
	var err error
	if reqBody != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, reqBody)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return nil, err
	}

	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if resolvedKey != "" {
		req.Header.Set("Authorization", "Bearer "+resolvedKey)
	}
	for k, v := range extraHeaders {
		resolved, err := resolver.ResolveValue(v)
		if err != nil || resolved == "" {
			continue
		}
		req.Header.Set(k, resolved)
	}

	return httpClient.Do(req)
}

// 配置 holds 提供方 configuration needed 为 模型 discovery.
type Config struct {
	ID           string
	BaseURL      string
	APIKey       string
	ExtraHeaders map[string]string
	// Existing models 从 配置 — IDs present 在 这 list 是 skipped
	// during discovery (用户-specified models win).
	ExistingModels []catwalk.Model
}

// Resolver resolves variable references (e.g. $ENV_VAR) 在 配置 values.
type Resolver interface {
	ResolveValue(val string) (string, error)
}

type modelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// DiscoverModels fetches available models 从 提供方's /models endpoint.
// 它 uses provided 上下文 为 cancellation 和 超时; callers should set
// 期限 (e.g. 上下文.WithTimeout) 到 avoid blocking indefinitely.
// Models whose IDs already appear 在 cfg.ExistingModels 是 skipped —
// 用户-specified models take precedence.
func DiscoverModels(ctx context.Context, cfg Config, resolver Resolver) ([]catwalk.Model, error) {
	resp, err := doRequest(ctx, http.MethodGet, cfg.BaseURL, "/models", cfg.APIKey, cfg.ExtraHeaders, resolver, nil)
	if err != nil {
		return nil, fmt.Errorf("discover models for provider %s: %w", cfg.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discover models for provider %s: %s", cfg.ID, resp.Status)
	}

	var modelsResp modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, fmt.Errorf("discover models for provider %s: %w", cfg.ID, err)
	}

	// Build set 的 existing 模型 IDs 到 skip.
	existing := make(map[string]struct{}, len(cfg.ExistingModels))
	for _, m := range cfg.ExistingModels {
		existing[m.ID] = struct{}{}
	}

	// Start 用 用户-specified models.
	result := make([]catwalk.Model, len(cfg.ExistingModels))
	copy(result, cfg.ExistingModels)

	// Append discovered models 不 already 在 list.
	for _, e := range modelsResp.Data {
		if _, ok := existing[e.ID]; ok {
			continue
		}
		result = append(result, catwalk.Model{
			ID:   e.ID,
			Name: e.ID,
		})
	}

	return result, nil
}
