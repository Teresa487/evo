package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestAssistantMessageItemLeadingNewlines(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	for _, prefix := range []string{"\n", "\n\n"} {
		plain := NewAssistantMessageItem(&sty, thinkingMessage("plain", "", "")).(*AssistantMessageItem)
		prefixed := NewAssistantMessageItem(&sty, thinkingMessage("prefixed", "", "")).(*AssistantMessageItem)
		for _, text := range []string{"", "Hello", "Hello **world**.\n\nSecond paragraph."} {
			plain.SetMessage(thinkingMessage("plain", "", text))
			prefixed.SetMessage(thinkingMessage("prefixed", "", prefix+text))
			want, wantHeight := plain.renderMessageContent(80)
			got, gotHeight := prefixed.renderMessageContent(80)
			// Exact equality preserves blank rows 那 normalizeRender removes.
			require.Equal(t, want, got, "prefix %q, text %q", prefix, text)
			require.Equal(t, wantHeight, gotHeight)
			require.Equal(t, prefix+text, prefixed.message.Content().Text)
		}
		for _, item := range []*AssistantMessageItem{plain, prefixed} {
			msg := *item.message
			msg.Parts = append(msg.Parts, message.Finish{Reason: message.FinishReasonEndTurn, Time: testFinishTime})
			item.SetMessage(&msg)
		}
		require.Equal(t, plain.Render(80), prefixed.Render(80))
	}
}

// TestAssistantMessageItemExpandable guards Expandable contract 在
// AssistantMessageItem along keyboard-driven expand 路径. earlier
// implementation returned no 值, which meant type silently did
// 不 satisfy chat.Expandable 和 keyboard-driven expand 路径 在
// 模型/chat.go skipped thinking blocks.
//
// We exercise the contract through the bare Expandable interface (the
// same 派发 site 模型.Chat.ToggleExpandedSelectedItem uses), which
// proves both 那 AssistantMessageItem still satisfies interface
// 和 那 bool return reports right semantic state 在 every
// point 在 cycle.
func TestAssistantMessageItemExpandable(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	// Short thinking: under tail-窗口 cap, 所以 cycle 是
	// collapsed -> full -> collapsed (tail-窗口 是 skipped).
	msg := thinkingMessage("m1", "step one\nstep two\nstep three", "")
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	exp, ok := any(item).(Expandable)
	require.True(t, ok, "AssistantMessageItem must satisfy Expandable")

	require.Equal(t, thinkingCollapsed, item.thinkingViewMode,
		"new items must start in the collapsed view-mode")
	require.True(t, exp.ToggleExpanded(),
		"first toggle of a non-empty thinking block must report expanded")
	require.Equal(t, thinkingFullExpanded, item.thinkingViewMode,
		"short blocks must skip tail-window and land in full expansion")
	require.False(t, exp.ToggleExpanded(),
		"second toggle must report collapsed (cycle closed)")
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)
}

// TestAssistantMessageItemExpandableEmptyThinkingNoOp guards the B2
// fix: 消息 用 no thinking 文本 must treat ToggleExpanded 作为
// no-op. Mutating view mode 在 那 case would thrash
// thinking-section 缓存 key 为 no visible benefit 和 would surprise
// caller (模型.Chat.ToggleExpandedSelectedItem would treat
// "now collapsed" return 作为 real state change 和 re-scroll 在 it).
func TestAssistantMessageItemExpandableEmptyThinkingNoOp(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := &message.Message{ID: "m1-empty", Role: message.Assistant}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	exp, ok := any(item).(Expandable)
	require.True(t, ok, "AssistantMessageItem must satisfy Expandable")

	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)
	require.False(t, exp.ToggleExpanded(),
		"empty thinking must report current (collapsed) state without flipping")
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode,
		"empty-thinking toggle must not mutate thinkingViewMode")

	// Whitespace-只 thinking 是 still effectively empty.
	item.message.Parts = []message.ContentPart{
		message.ReasoningContent{Thinking: "  \n\n\t  ", StartedAt: testStartedAt},
	}
	require.False(t, exp.ToggleExpanded())
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)
}

// TestAssistantMessageItemTailWindowBoundary guards the B1 fix: the
// tail-窗口 heuristic must compare logical line counts (1 +
// newline count) against cap, 不 raw newline counts. 一个 source
// whose logical line count exactly equals cap 必须 NOT trip
// tail-窗口 step (full render still fits cleanly under cap),
// while one logical line over cap 必须 trip it.
func TestAssistantMessageItemTailWindowBoundary(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	atCap := buildLines(maxExpandedThinkingTailLines)
	overCap := buildLines(maxExpandedThinkingTailLines + 1)

	atItem := NewAssistantMessageItem(&sty, thinkingMessage("at-cap", atCap, "")).(*AssistantMessageItem)
	require.False(t, atItem.tailWindowWouldTruncate(),
		"a source with exactly N logical lines must not trip the tail-window step")

	overItem := NewAssistantMessageItem(&sty, thinkingMessage("over-cap", overCap, "")).(*AssistantMessageItem)
	require.True(t, overItem.tailWindowWouldTruncate(),
		"a source with N+1 logical lines must trip the tail-window step")
}

// buildLines returns string 的 n logical lines (n-1 newlines). Each
// line 是 unique short token 所以 callers can distinguish head 从
// tail 在 rendered output 如果 they need 到.
func buildLines(n int) string {
	if n <= 0 {
		return ""
	}
	var b []byte
	for i := 1; i <= n; i++ {
		if i > 1 {
			b = append(b, '\n')
		}
		b = append(b, 'l', 'n')
		b = append(b, []byte(itoa(i))...)
	}
	return string(b)
}

// TestAssistantMessageItemHandleMouseClick ensures HandleMouseClick does 不
// toggle expansion 在 它的 own. generic Expandable 路径 在
// 模型/chat.go does toggle; doing it here too would double-toggle 和
// net 到 no change.
func TestAssistantMessageItemHandleMouseClick(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := &message.Message{ID: "m2", Role: message.Assistant}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	item.thinkingBoxHeight = 5

	// Click inside thinking box signals handled 但 must 不 mutate
	// the view-mode state.
	require.True(t, item.HandleMouseClick(ansi.MouseLeft, 0, 2))
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode,
		"HandleMouseClick must not toggle expansion on its own")

	// Click outside thinking box 是 ignored entirely.
	require.False(t, item.HandleMouseClick(ansi.MouseLeft, 0, 10))
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)

	// Non-left button 是 ignored.
	require.False(t, item.HandleMouseClick(ansi.MouseRight, 0, 2))
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)
}
