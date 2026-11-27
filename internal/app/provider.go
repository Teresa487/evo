// provider.go —— app 层的 provider/model 解析：根据配置选出实际使用的模型与客户端。

package app

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	xstrings "github.com/charmbracelet/x/exp/strings"
)

// parseModelStr parses 模型 string into 提供方 filter 和 模型 ID.
// Format: "模型-name" 或 "提供方/模型-name" 或 "synthetic/moonshot/kimi-k2".
// 这 function 只 checks 如果 first component 是 valid 提供方 name; 如果 不,
// it treats entire string 作为 模型 ID (which may contain slashes).
func parseModelStr(providers map[string]config.ProviderConfig, modelStr string) (providerFilter, modelID string) {
	parts := strings.Split(modelStr, "/")
	if len(parts) == 1 {
		return "", parts[0]
	}
	// Check 如果 first 片段 是 valid 提供方 name
	if _, ok := providers[parts[0]]; ok {
		return parts[0], strings.Join(parts[1:], "/")
	}

	// First 片段 是 不 valid 提供方, treat entire string 作为 模型 ID
	return "", modelStr
}

// modelMatch represents found 模型.
type modelMatch struct {
	provider string
	modelID  string
}

func findModels(providers map[string]config.ProviderConfig, largeModel, smallModel string) ([]modelMatch, []modelMatch, error) {
	largeProviderFilter, largeModelID := parseModelStr(providers, largeModel)
	smallProviderFilter, smallModelID := parseModelStr(providers, smallModel)

	// Validate 提供方 filters exist.
	for _, pf := range []struct {
		filter, label string
	}{
		{largeProviderFilter, "large"},
		{smallProviderFilter, "small"},
	} {
		if pf.filter != "" {
			if _, ok := providers[pf.filter]; !ok {
				return nil, nil, fmt.Errorf("%s model: provider %q not found in configuration. Use 'crush models' to list available models", pf.label, pf.filter)
			}
		}
	}

	// Find matching models 在 single pass.
	var largeMatches, smallMatches []modelMatch
	for name, provider := range providers {
		if provider.Disable {
			continue
		}
		for _, m := range provider.Models {
			if filter(largeModelID, largeProviderFilter, m.ID, name) {
				largeMatches = append(largeMatches, modelMatch{provider: name, modelID: m.ID})
			}
			if filter(smallModelID, smallProviderFilter, m.ID, name) {
				smallMatches = append(smallMatches, modelMatch{provider: name, modelID: m.ID})
			}
		}
	}

	return largeMatches, smallMatches, nil
}

func filter(modelFilter, providerFilter, model, provider string) bool {
	return modelFilter != "" && strings.EqualFold(model, modelFilter) &&
		(providerFilter == "" || strings.EqualFold(provider, providerFilter))
}

// Validate 和 return single match.
func validateMatches(matches []modelMatch, modelID, label string) (modelMatch, error) {
	switch {
	case len(matches) == 0:
		return modelMatch{}, fmt.Errorf("%s model %q not found", label, modelID)
	case len(matches) > 1:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.provider
		}
		return modelMatch{}, fmt.Errorf(
			"%s model: model %q found in multiple providers: %s. Please specify provider using 'provider/model' format",
			label,
			modelID,
			xstrings.EnglishJoin(names, true),
		)
	}
	return matches[0], nil
}
