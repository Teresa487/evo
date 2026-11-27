package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/oauth"
)

// ModelInfo mirrors one 条目 的 Codex backend 模型 catalog.
type ModelInfo struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
	Visibility    string `json:"visibility"`
	ContextWindow int64  `json:"context_window"`

	DefaultReasoningLevel string `json:"default_reasoning_level"`
	// SupportedReasoningLevels 是 list 的 {effort, description}
	// presets 在 backend's own shape.
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

type modelsResponse struct {
	Models []ModelInfo `json:"models"`
}

// clientVersion 是 codex 客户端 version reported 到 models
// endpoint, which requires it. backend gates models 通过 codex
// 客户端's feature support; crush runs 它的 own agent loop 和 does 不
// depend 在 those features, 所以 deliberately high version requests
// full 目录 订阅 grants.
const clientVersion = "999.0.0"

// Models fetches 模型 catalog ChatGPT plan grants access 到
// 从 Codex backend. Only models backend lists 作为 visible 是
// returned; rest 是 internal 或 retired entries.
func Models(ctx context.Context, token *oauth.Token) ([]catwalk.Model, error) {
	if token == nil {
		return nil, fmt.Errorf("an OAuth token is required to list ChatGPT models")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsEndpoint+"?client_version="+clientVersion, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	if token.AccountID != "" {
		req.Header.Set("chatgpt-account-id", token.AccountID)
	}
	req.Header.Set("originator", "crush")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch Codex model catalog: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read Codex model catalog: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &oauth.TokenExchangeError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	var mr modelsResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, fmt.Errorf("decode Codex model catalog: %w", err)
	}

	models := make([]catwalk.Model, 0, len(mr.Models))
	for _, m := range mr.Models {
		// backend marks internal 和 retired entries 作为 anything
		// other than "list"; 只 listed models 是 用户-selectable.
		if m.Slug == "" || m.Visibility != "list" {
			continue
		}
		levels := make([]string, 0, len(m.SupportedReasoningLevels))
		for _, level := range m.SupportedReasoningLevels {
			if level.Effort != "" {
				levels = append(levels, level.Effort)
			}
		}
		reasoning := catwalk.Reasoning{Thinking: catwalk.ThinkingNever}
		if len(levels) > 0 {
			reasoning = catwalk.Reasoning{
				Thinking:           catwalk.ThinkingToggleable,
				EffortLevels:       catwalk.NewEffortLevels(levels...),
				DefaultEffortLevel: m.DefaultReasoningLevel,
			}
		}
		models = append(models, catwalk.Model{
			ID:            m.Slug,
			Name:          m.DisplayName,
			ContextWindow: m.ContextWindow,
			Reasoning:     reasoning,
			Capabilities:  catwalk.Capabilities{Vision: true},
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("the Codex model catalog was empty")
	}
	return models, nil
}
