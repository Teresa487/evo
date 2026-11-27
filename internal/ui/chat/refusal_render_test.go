package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// TestRefusalFinishRendersBanner asserts 那 content-filter finish
// (Anthropic stop_reason=拒绝 mapped through fantasy) paints
// visible REFUSED banner rather than empty 助手 turn.
// agent persists 只 reason, 所以 banner 文本 comes 从
// TUI's canonical 拒绝 copy.
func TestRefusalFinishRendersBanner(t *testing.T) {
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "refusal-1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.Finish{
				Reason: message.FinishReasonContentFilter,
				Time:   testFinishTime,
			},
		},
	}

	require.True(t, ShouldRenderAssistantMessage(msg),
		"empty-body refusal turns must still render so the banner is visible")

	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	out := item.Render(80)
	require.Contains(t, out, refusalTagLabel, "banner tag")
	require.Contains(t, out, refusalTitle, "title")
	require.Contains(t, strings.ToLower(out), "safety classifier", "details")
}

// TestRefusalPersistedCopyWins asserts 那 当 finish 片段 does
// carry 它的 own 消息/details (e.g. future 提供方 那 supplies
// them), the persisted copy takes precedence over the TUI defaults.
func TestRefusalPersistedCopyWins(t *testing.T) {
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "refusal-2",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.Finish{
				Reason:  message.FinishReasonContentFilter,
				Message: "Custom refusal title",
				Details: "Custom refusal details.",
				Time:    testFinishTime,
			},
		},
	}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	out := item.Render(80)
	require.Contains(t, out, refusalTagLabel)
	require.Contains(t, out, "Custom refusal title")
	require.Contains(t, out, "Custom refusal details.")
}

// TestErrorEmptyDetailsNoTrailingBlock locks 在 behavior 那
// 错误 finish 用 no details renders 只 标题 line, without
// empty styled details block 或 trailing blank lines.
func TestErrorEmptyDetailsNoTrailingBlock(t *testing.T) {
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "error-1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.Finish{
				Reason:  message.FinishReasonError,
				Message: "boom",
				Time:    testFinishTime,
			},
		},
	}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	out := item.Render(80)
	require.Contains(t, out, "ERROR")
	require.Contains(t, out, "boom")
	require.False(t, strings.HasSuffix(out, "\n"),
		"empty-details error must not render a trailing details block")
}
