package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/stretchr/testify/require"
)

// TestChatToggleExpandedSelectedItem_AssistantMessage 是 regression 测试
// 为 §4.8.1: before fix, AssistantMessageItem.ToggleExpanded returned no
// 值 所以 type did 不 satisfy chat.Expandable, 和 keyboard-driven
// ToggleExpandedSelectedItem 路径 silently skipped thinking blocks. 这
// wires real Chat 用 AssistantMessageItem, selects it, invokes
// ToggleExpandedSelectedItem, 和 asserts thinking block actually
// flipped.
func TestChatToggleExpandedSelectedItem_AssistantMessage(t *testing.T) {
	t.Parallel()

	u := newTestUI()

	msg := &message.Message{
		ID:   "m-assist",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "thinking about it"},
		},
	}
	item := chat.NewAssistantMessageItem(u.com.Styles, msg)

	// keyboard expand 路径 uses generic Expandable interface;
	// verifying satisfaction 在 runtime guards contract.
	exp, ok := item.(chat.Expandable)
	require.True(t, ok, "AssistantMessageItem must satisfy chat.Expandable")

	u.chat.SetMessages(item)
	u.chat.SetSelected(0)

	// First keyboard toggle should expand. Immediately follow 用
	// direct ToggleExpanded: it flips now-expanded item back 到
	// collapsed 和 returns false. If keyboard 路径 had silently
	// skipped item ( bug), item would still 是 collapsed 和
	// the direct toggle would return true.
	u.chat.ToggleExpandedSelectedItem()
	require.False(t, exp.ToggleExpanded(),
		"keyboard toggle did not expand the assistant thinking block")

	// Second keyboard toggle against re-collapsed item 应当 expand
	// it again. Direct ToggleExpanded then returns false because it
	// re-collapses.
	u.chat.ToggleExpandedSelectedItem()
	require.False(t, exp.ToggleExpanded(),
		"second keyboard toggle did not re-expand the assistant thinking block")
}
