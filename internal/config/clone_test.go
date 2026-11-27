package config

import (
	"testing"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/stretchr/testify/require"
)

// TestCloneForWrite_Isolation verifies 那 mutating clone never reaches
// back into original 配置. 这 是 contract 存储's
// copy-在-写入 mutators depend 在 为 race-free publishing.
func TestCloneForWrite_Isolation(t *testing.T) {
	t.Parallel()

	orig := &Config{
		Models: map[SelectedModelType]SelectedModel{
			SelectedModelTypeLarge: {Provider: "openai", Model: "gpt-4"},
		},
		RecentModels: map[SelectedModelType][]SelectedModel{
			SelectedModelTypeLarge: {{Provider: "openai", Model: "gpt-4"}},
		},
		MCP:       MCPs{"a": {}},
		Providers: csync.NewMap[string, ProviderConfig](),
		Options: &Options{
			TUI: &TUIOptions{CompactMode: false},
		},
	}

	clone := orig.cloneForWrite()

	// Mutate every 字段 typed mutators touch.
	clone.Models[SelectedModelTypeLarge] = SelectedModel{Provider: "anthropic", Model: "claude"}
	clone.RecentModels[SelectedModelTypeLarge] = []SelectedModel{{Provider: "anthropic", Model: "claude"}}
	clone.MCP["b"] = MCPConfig{}
	clone.Options.TUI.CompactMode = true
	enabled := true
	clone.Options.TUI.Transparent = &enabled
	disabled := false
	clone.Options.TUI.Mouse = &disabled

	// original must 是 untouched.
	require.Equal(t, "openai", orig.Models[SelectedModelTypeLarge].Provider, "Models leaked")
	require.Equal(t, "openai", orig.RecentModels[SelectedModelTypeLarge][0].Provider, "RecentModels leaked")
	require.NotContains(t, orig.MCP, "b", "MCP leaked")
	require.False(t, orig.Options.TUI.CompactMode, "Options.TUI.CompactMode leaked")
	require.Nil(t, orig.Options.TUI.Transparent, "Options.TUI.Transparent leaked")
	require.Nil(t, orig.Options.TUI.Mouse, "Options.TUI.Mouse leaked")
}
