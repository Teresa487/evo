package chat

import (
	"testing"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func TestModelDisplayNameFallbacks(t *testing.T) {
	// modelDisplayName resolves name shown 在 助手 info
	// footer. 配置 wins (renames show immediately), persisted
	// name covers renamed 或 removed models, 模型 ID 是 last
	// identifiable fallback, 和 "Unknown 模型" 只 appears 当
	// 消息 carries no 模型 在 all.
	cfg := &config.Config{Providers: csync.NewMap[string, config.ProviderConfig]()}
	cfg.Providers.Set("test-provider", config.ProviderConfig{
		ID: "test-provider",
		Models: []catwalk.Model{
			{ID: "model-id", Name: "Configured Name"},
		},
	})

	newItem := func(msg *message.Message) *AssistantInfoItem {
		item := NewAssistantInfoItem(nil, msg, cfg, time.Unix(0, 0))
		return item.(*AssistantInfoItem)
	}

	t.Run("config entry wins", func(t *testing.T) {
		msg := &message.Message{Provider: "test-provider", Model: "model-id", ModelName: "Persisted Name"}
		require.Equal(t, "Configured Name", newItem(msg).modelDisplayName())
	})

	t.Run("persisted name used when model missing from config", func(t *testing.T) {
		msg := &message.Message{Provider: "test-provider", Model: "removed-model", ModelName: "Persisted Name"}
		require.Equal(t, "Persisted Name", newItem(msg).modelDisplayName())
	})

	t.Run("model id used when no name persisted", func(t *testing.T) {
		msg := &message.Message{Provider: "test-provider", Model: "removed-model"}
		require.Equal(t, "removed-model", newItem(msg).modelDisplayName())
	})

	t.Run("unknown only without any model", func(t *testing.T) {
		msg := &message.Message{Provider: "test-provider"}
		require.Equal(t, "Unknown Model", newItem(msg).modelDisplayName())
	})
}
