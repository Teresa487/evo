package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/oauth"
)

// APIBaseURL 是 xAI API Grok account's access token authorizes.
const APIBaseURL = "https://api.x.ai/v1"

// modelsEndpoint 是 variable 所以 tests can point it 在 stub 服务端.
var modelsEndpoint = APIBaseURL + "/models"

// modelCapabilities mirrors 请求 parameters 模型 accepts.
type modelCapabilities struct {
	// ReasoningEffort lists effort values 模型 honors.
	ReasoningEffort []string `json:"reasoning_effort"`
	// DefaultReasoningEffort 是 used 当 no effort 是 selected.
	DefaultReasoningEffort string `json:"default_reasoning_effort"`
}

// modelInfo mirrors one 条目 的 xAI 模型 listing.
type modelInfo struct {
	ID string `json:"id"`
	// ContextLength 是 maximum 上下文 length 在 tokens.
	ContextLength int64 `json:"context_length"`
	// CompletionTextTokenPrice 是 在 USD cents per 100M tokens 和 是
	// set 只 为 文本-generating models; 图片 和 video generation
	// models carry image_price instead. Its presence 是
	// language-模型 filter.
	CompletionTextTokenPrice *int64 `json:"completion_text_token_price"`
	// PromptImageTokenPrice 是 在 USD cents per 100M tokens; non-zero
	// 值 marks models 那 受理 图片 input.
	PromptImageTokenPrice int64              `json:"prompt_image_token_price"`
	Capabilities          *modelCapabilities `json:"capabilities"`
}

type modelsResponse struct {
	Models []modelInfo `json:"data"`
}

// Models fetches 模型 catalog Grok plan grants 从 xAI API.
// Only 文本-generating entries 是 returned: listing also carries
// 图片 和 video generation models, which 是 不 chat models.
func Models(ctx context.Context, token *oauth.Token) ([]catwalk.Model, error) {
	if token == nil {
		return nil, fmt.Errorf("an OAuth token is required to list Grok models")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch Grok model catalog: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read Grok model catalog: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &oauth.TokenExchangeError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	var mr modelsResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, fmt.Errorf("decode Grok model catalog: %w", err)
	}

	models := make([]catwalk.Model, 0, len(mr.Models))
	for _, m := range mr.Models {
		// Entries without completion pricing generate images 或 video
		// instead 的 文本; they 是 不 chat models.
		if m.ID == "" || m.CompletionTextTokenPrice == nil {
			continue
		}
		var levels []string
		var defaultEffort string
		if m.Capabilities != nil {
			levels = m.Capabilities.ReasoningEffort
			defaultEffort = m.Capabilities.DefaultReasoningEffort
		}
		reasoning := catwalk.Reasoning{Thinking: catwalk.ThinkingNever}
		if len(levels) > 0 {
			reasoning = catwalk.Reasoning{
				Thinking:           catwalk.ThinkingToggleable,
				EffortLevels:       catwalk.NewEffortLevels(levels...),
				DefaultEffortLevel: defaultEffort,
			}
		}
		models = append(models, catwalk.Model{
			ID:            m.ID,
			Name:          modelName(m.ID),
			ContextWindow: m.ContextLength,
			Reasoning:     reasoning,
			Capabilities:  catwalk.Capabilities{Vision: m.PromptImageTokenPrice > 0},
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("the Grok model catalog was empty")
	}
	return models, nil
}

// modelName derives display name 从 模型 ID: "grok-4-fast"
// becomes "Grok 4 Fast". The xAI listing carries no display names.
func modelName(id string) string {
	words := make([]string, 0, strings.Count(id, "-")+1)
	for w := range strings.SplitSeq(id, "-") {
		if w == "" {
			continue
		}
		words = append(words, strings.ToUpper(w[:1])+w[1:])
	}
	return strings.Join(words, " ")
}
