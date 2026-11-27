package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyLSPDefaults(t *testing.T) {
	t.Parallel()

	// 创建 配置 用 LSP 那 should get defaults
	config := &Config{
		LSP: map[string]LSPConfig{
			"gopls": {
				Command: "gopls", // This should get defaults from powernap
			},
			"custom": {
				Command:     "custom-lsp",
				RootMarkers: []string{"custom.toml"}, // This should keep its explicit config
			},
		},
	}

	// Apply defaults
	config.applyLSPDefaults()

	// Check 那 gopls got defaults (it should have some root markers now)
	goplsConfig := config.LSP["gopls"]
	require.NotEmpty(t, goplsConfig.RootMarkers, "gopls should have received default root markers")

	// Check 那 custom LSP kept 它的 explicit 配置
	customConfig := config.LSP["custom"]
	require.Equal(t, []string{"custom.toml"}, customConfig.RootMarkers, "custom LSP should keep its explicit root markers")
}
