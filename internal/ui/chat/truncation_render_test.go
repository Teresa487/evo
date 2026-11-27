package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// TestTruncationFinishRendersBanner asserts 那 max-tokens finish
// paints visible TRUNCATED banner rather than turn 那 simply
// looks 完成 while stopping mid-sentence. As 用 refusals,
// agent persists 只 reason; TUI owns 这 copy.
func TestTruncationFinishRendersBanner(t *testing.T) {
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "truncated-1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "The answer starts here and"},
			message.Finish{
				Reason: message.FinishReasonMaxTokens,
				Time:   testFinishTime,
			},
		},
	}

	require.True(t, ShouldRenderAssistantMessage(msg),
		"a truncated turn must still render so the banner is visible")

	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	out := item.Render(80)
	require.Contains(t, out, truncatedTagLabel, "banner tag")
	require.Contains(t, out, truncatedTitle, "title")
	require.Contains(t, strings.ToLower(out), "max tokens", "actionable hint")
}

// TestTruncationEmptyBodyStillRenders covers worst case 为 silent
// truncation: reasoning consumed entire token budget, 所以 turn
// has neither 文本 nor 工具 calls. 消息 must still render 所以
// banner 可以 explain why nothing came back.
func TestTruncationEmptyBodyStillRenders(t *testing.T) {
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "truncated-2",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call-1", Name: "bash", Input: `{"command": "ls"}`},
			message.Finish{
				Reason: message.FinishReasonMaxTokens,
				Time:   testFinishTime,
			},
		},
	}

	// 工具 calls 但 no body: normally unrendered intermediate step.
	require.True(t, ShouldRenderAssistantMessage(msg),
		"a truncated tool-only turn must render its banner")

	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	require.Contains(t, item.Render(80), truncatedTagLabel)
}

// TestTruncationPersistedCopyWins asserts 那 当 finish 片段
// carries 它的 own 消息/details (e.g. 提供方 那 supplies
// them), the persisted copy takes precedence over the TUI defaults.
func TestTruncationPersistedCopyWins(t *testing.T) {
	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "truncated-3",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.Finish{
				Reason:  message.FinishReasonMaxTokens,
				Message: "Custom truncation title",
				Details: "Custom truncation details.",
				Time:    testFinishTime,
			},
		},
	}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	out := item.Render(80)
	require.Contains(t, out, truncatedTagLabel)
	require.Contains(t, out, "Custom truncation title")
	require.Contains(t, out, "Custom truncation details.")
}

// TestTruncationInfoItemKeepsFullFooter covers the compact turn
// header: truncated turn 是 last turn 的 它的 提示词 ( 模型
// cannot continue past token limit), 所以 it must render full
// footer 用 duration 和 separator line rather than compact
// intermediate-step header, 或 banner reads 作为 one step 的 loop
// 那 是 still running.
func TestTruncationInfoItemKeepsFullFooter(t *testing.T) {
	sty := styles.CharmtonePantera()
	// GetModel dereferences Providers map, 所以 it must 是
	// initialized even though no models 是 configured:
	// Prism-routed branch supplies 模型 name.
	cfg := &config.Config{Providers: csync.NewMap[string, config.ProviderConfig]()}
	finishTime := time.Unix(testFinishTime, 0)

	build := func(reason message.FinishReason) *message.Message {
		return &message.Message{
			ID:             "info",
			Role:           message.Assistant,
			Provider:       "hyper",
			Model:          "prism",
			PrismModelName: "Prism Router",
			Parts: []message.ContentPart{
				message.Finish{Reason: reason, Time: finishTime.Unix()},
			},
		}
	}
	// Prism-routed turns show info item even 为 non-EndTurn
	// finishes; 那 是 exactly compact-header 路径.
	require.True(t, ShouldShowAssistantInfo(build(message.FinishReasonMaxTokens)))

	info := NewAssistantInfoItem(&sty, build(message.FinishReasonMaxTokens), cfg, time.Unix(0, 0))
	out := info.Render(80)
	require.Contains(t, out, "in ", "duration footer")
	require.Contains(t, out, "Prism Router", "model line")

	// same holds without Prism route: truncated turn 在
	// ordinary 模型 是 still last turn 的 它的 提示词, 所以 it gets
	// info footer rather than compact header 那 reads 作为
	// running loop.
	plain := build(message.FinishReasonMaxTokens)
	plain.PrismModelName = ""
	require.True(t, ShouldShowAssistantInfo(plain))
	plainInfo := NewAssistantInfoItem(&sty, plain, cfg, time.Unix(0, 0))
	require.Contains(t, plainInfo.Render(80), "in ", "duration footer without Prism")

	// An intermediate 工具-use turn still renders compact header.
	compact := NewAssistantInfoItem(&sty, build(message.FinishReasonToolUse), cfg, time.Unix(0, 0))
	require.NotContains(t, compact.Render(80), "in ",
		"intermediate turns keep the compact header")
}
