package chat

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// thinkingMessageWithLines builds still-thinking 助手消息
// whose reasoning content 是 `count` short paragraphs separated 通过
// blank lines. blank-line separation 是 what matters: glamour
// renders paragraph blocks one-per-line 在 output (用
// trailing blank line between paragraphs) instead 的 reflowing
// entire input into one big wrapped paragraph. That gives us a
// post-glamour line count we can drive past tail-窗口
// threshold deterministically. Each paragraph 是 tagged 用 它的
// (1-based) index 所以 测试 can identify head vs tail 在
// rendered output.
//
// 消息 has no 文本 content 和 no Finish 片段, 所以
// IsThinking() returns true 和 render 路径 skips
// "Thought 为" footer — keeping rendered height computation
// simple.
func thinkingMessageWithLines(id string, count int) *message.Message {
	var b strings.Builder
	for i := 1; i <= count; i++ {
		b.WriteString("ln")
		b.WriteString(itoa(i))
		if i < count {
			// Blank line between paragraphs: glamour preserves the
			// per-paragraph structure rather than reflowing into one
			// wrapped block, 所以 totalLines tracks count predictably.
			b.WriteString("\n\n")
		}
	}
	return &message.Message{
		ID:   id,
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{
				Thinking:  b.String(),
				StartedAt: testStartedAt,
			},
		},
	}
}

// itoa 是 local stdlib-free integer formatter used 只 通过 these
// tests; pulling fmt 在 just 为 %d would 是 wasteful 当 测试
// fixtures already churn 5000+ short strings.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// renderedThinkingHeight returns line count 的 cached
// thinking section render 只 (不 full RawRender, which also
// includes content 和 错误 sections). Drives render 在
// `width` first 到 populate 缓存.
func renderedThinkingHeight(t *testing.T, item *AssistantMessageItem, width int) int {
	t.Helper()
	_ = item.RawRender(width)
	require.NotEmpty(t, item.thinkingSec.out,
		"thinking section must be populated after RawRender")
	return lipgloss.Height(item.thinkingSec.out)
}

// TestThinkingWindow_CollapsedCapPreserved guards 那 F5 did 不
// regress the existing collapsed-mode behaviour: a 5000-line
// thinking block 在 默认 (collapsed) state still renders 在
// most a small bounded height — the last `maxCollapsedThinkingHeight`
// lines plus truncation hint. thinking 消息 keeps
// IsThinking() == true, 所以 optional "Thought 为" footer 是
// suppressed 和 section height equals box height.
func TestThinkingWindow_CollapsedCapPreserved(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := thinkingMessageWithLines("collapsed", 5000)
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	// 默认 state must 是 collapsed.
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)

	// Unique odd width avoids sharing glamour renderer 缓存 用
	// any other parallel 测试 ( renderer instance 是 memoized per
	// width 和 是 不 safe 为 concurrent Render calls).
	const width = 91
	height := renderedThinkingHeight(t, item, width)

	// Collapsed mode 保持 existing cap: last 10 lines +
	// 2-line hint prefix (hint + blank). Allow small slack 为
	// any future style-driven padding 所以 测试 是 robust 到
	// cosmetic tweaks while still being orders 的 magnitude below
	// the 5000-line source.
	const collapsedUpperBound = maxCollapsedThinkingHeight + 5
	require.LessOrEqual(t, height, collapsedUpperBound,
		"collapsed mode must remain bounded by the small cap; got %d", height)
}

// TestThinkingWindow_ExpandedShortSkipsTailWindow guards 那
// short thinking block (well under tail-窗口 cap) still
// toggles directly 到 full expansion without intermediate
// tail-窗口 step 和 shows no affordance footer. cycle 是
// collapsed -> full -> collapsed 为 short blocks; tail-窗口 是
// 只 inserted 当 it would actually elide content.
func TestThinkingWindow_ExpandedShortSkipsTailWindow(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	const lines = 50
	require.Less(t, lines, maxExpandedThinkingTailLines,
		"this test relies on the source being well under the tail cap")
	msg := thinkingMessageWithLines("short", lines)
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	require.True(t, item.ToggleExpanded(),
		"first toggle should report expanded")
	require.Equal(t, thinkingFullExpanded, item.thinkingViewMode,
		"short blocks must skip tail-window and go straight to full expansion")

	const width = 93
	_ = item.RawRender(width)
	out := item.thinkingSec.out
	plain := ansi.Strip(out)

	require.NotContains(t, plain, "earlier lines hidden",
		"short blocks must not show the tail-window affordance")
	require.NotContains(t, plain, "lines hidden",
		"short expanded blocks must not show any truncation hint")
	require.Contains(t, plain, "ln1 ",
		"a fully expanded short block must include the very first source paragraph")
	require.Contains(t, plain, "ln50 ",
		"a fully expanded short block must include the last source paragraph")
}

// TestThinkingWindow_TailWindowed asserts the central F5 behaviour:
// expanding long thinking block produces tail 窗口 的 size
// `maxExpandedThinkingTailLines` plus affordance footer, 用
// LAST source line present (i.e. we tailed, 不 headed) 和
// earlier lines elided.
//
// Beyond presence/absence 的 sentinels, 这 测试 verifies true
// `tail -K` relationship between tail-windowed render 和
// fully-expanded render 的 same source 在 same width:
// last K plain-ANSI lines 的 windowed render must byte-equal
// last K lines 的 unwindowed render.
//
// K 是 sized below cap 到 absorb affordance prefix (hint +
// blank line) 和 any small framing differences introduced 通过
// bordered ThinkingBox. The cap minus 5 leaves a comfortable margin
// 为 padding/footer rows while still asserting 那 bulk 的
// rendered tail 是 identical.
func TestThinkingWindow_TailWindowed(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	const total = 5000
	const width = 95

	// Tail-windowed render.
	tailMsg := thinkingMessageWithLines("tail", total)
	tailItem := NewAssistantMessageItem(&sty, tailMsg).(*AssistantMessageItem)
	require.True(t, tailItem.ToggleExpanded(), "first toggle should report expanded")
	require.Equal(t, thinkingTailWindow, tailItem.thinkingViewMode,
		"a long block must enter tail-window after the first toggle")

	height := renderedThinkingHeight(t, tailItem, width)

	// visible 窗口 是 N tail lines plus affordance line
	// 和 blank-line spacer (matching existing collapsed-mode
	// hint structure). Allow small slack 为 style-driven
	// padding.
	const expectedFloor = maxExpandedThinkingTailLines + 1
	const expectedCeil = maxExpandedThinkingTailLines + 5
	require.GreaterOrEqual(t, height, expectedFloor,
		"tail-window must include at least N + affordance lines; got %d", height)
	require.LessOrEqual(t, height, expectedCeil,
		"tail-window must not exceed N + a small padding budget; got %d", height)

	tailPlain := ansi.Strip(tailItem.thinkingSec.out)

	require.Contains(t, tailPlain, "earlier lines hidden",
		"tail-windowed render must include the affordance footer")
	require.Contains(t, tailPlain, "ln5000",
		"tail-windowed render must include the LAST source paragraph — we tailed, not headed")
	require.NotContains(t, tailPlain, "ln1 ",
		"tail-windowed render must elide early source paragraphs")

	// Independent reference render: same source, same width, full
	// expansion (no tail slice). The tail-windowed output's last K
	// lines 必须 byte-equal unwindowed output's last K lines.
	fullMsg := thinkingMessageWithLines("tail-full-ref", total)
	fullItem := NewAssistantMessageItem(&sty, fullMsg).(*AssistantMessageItem)
	fullItem.thinkingViewMode = thinkingFullExpanded
	_ = fullItem.RawRender(width)
	fullPlain := ansi.Strip(fullItem.thinkingSec.out)

	tailLines := strings.Split(tailPlain, "\n")
	fullLines := strings.Split(fullPlain, "\n")

	// K 是 cap minus small budget 那 covers affordance
	// prefix (hint line + blank line) 和 any framing differences
	// the bordered ThinkingBox style may introduce around the
	// edges. Documented inline because going much larger lets the
	// affordance 行 leak into comparison; going much smaller
	// dilutes the assertion.
	const K = maxExpandedThinkingTailLines - 5
	require.GreaterOrEqual(t, len(tailLines), K,
		"tail render must contain at least K lines; got %d", len(tailLines))
	require.GreaterOrEqual(t, len(fullLines), K,
		"full render must contain at least K lines; got %d", len(fullLines))

	tailTail := tailLines[len(tailLines)-K:]
	fullTail := fullLines[len(fullLines)-K:]
	require.Equal(t, fullTail, tailTail,
		"tail-windowed render's last %d lines must byte-equal the unwindowed render's last %d lines (true tail -K relationship)",
		K, K)
}

// TestThinkingWindow_PromoteToFull verifies cycle continues 从
// tail-窗口 到 full expansion: second toggle drops
// affordance, removes tail slice, 和 produces render 那
// matches fresh item rendered directly 在 full-expanded
// state.
func TestThinkingWindow_PromoteToFull(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	const total = 1500
	msg := thinkingMessageWithLines("promote", total)
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 97

	require.True(t, item.ToggleExpanded())
	require.Equal(t, thinkingTailWindow, item.thinkingViewMode)
	_ = item.RawRender(width)
	tailOut := item.thinkingSec.out
	require.Contains(t, ansi.Strip(tailOut), "earlier lines hidden")

	require.True(t, item.ToggleExpanded(), "second toggle stays expanded (full)")
	require.Equal(t, thinkingFullExpanded, item.thinkingViewMode)
	_ = item.RawRender(width)
	fullOut := item.thinkingSec.out
	fullPlain := ansi.Strip(fullOut)

	require.NotContains(t, fullPlain, "earlier lines hidden",
		"full expansion must drop the tail-window affordance")
	require.Contains(t, fullPlain, "ln1 ",
		"full expansion must include the first source paragraph")
	require.Contains(t, fullPlain, "ln1500 ",
		"full expansion must include the last source paragraph")

	// Independent reference: a fresh item, rendered straight into
	// full-expanded state, 必须 produce byte-equal output.
	freshMsg := thinkingMessageWithLines("promote-fresh", total)
	fresh := NewAssistantMessageItem(&sty, freshMsg).(*AssistantMessageItem)
	fresh.thinkingViewMode = thinkingFullExpanded
	_ = fresh.RawRender(width)
	require.Equal(t, fresh.thinkingSec.out, fullOut,
		"cached full-expanded output must match a fresh full-expanded render")

	// And cycle closes back 到 collapsed.
	require.False(t, item.ToggleExpanded(), "third toggle must report collapsed")
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)
}

// sectionKey 是 tuple 那 defines 缓存-hit identity 为
// assistantSection: (width, srcHash, extra). Comparing 这 tuple
// across mutations 是 stronger invariant than byte-equality 的
// rendered output: byte-equality could 在 principle hold even 如果
// 缓存 invalidated 和 re-rendered identical bytes, while
// tuple-equality 证明 lookup key never moved.
type sectionKey struct {
	width   int
	srcHash uint64
	extra   uint64
}

func keyOf(s assistantSection) sectionKey {
	return sectionKey{width: s.width, srcHash: s.srcHash, extra: s.extra}
}

// TestThinkingWindow_ContentChangeKeepsThinkingCacheInTailWindow
// guards the F4/F5 boundary: streaming the main content while the
// thinking block sits 在 tail-窗口 mode must NOT invalidate
// thinking section 缓存. Tail-窗口 state 是 folded into
// thinkingKey()'s extra hash, 所以 changing 只 content 文本
// keeps thinking's (srcHash, extra) tuple identical 和 缓存
// hits.
//
// assertion 是 在 缓存 key tuple, 不 just rendered bytes:
// equal output could 在 principle survive re-render 用
// identical inputs, 但 identical (width, srcHash, extra) tuples
// across SetMessage cycle prove thinking 缓存 was never
// invalidated 到 begin 用. mirror tuple 在 content
// section MUST move ( source 文本 changed), 或 测试 isn't
// exercising what it claims 到.
func TestThinkingWindow_ContentChangeKeepsThinkingCacheInTailWindow(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	const total = 1000

	build := func(content string) *message.Message {
		var b strings.Builder
		for i := 1; i <= total; i++ {
			b.WriteString("ln")
			b.WriteString(itoa(i))
			if i < total {
				b.WriteString("\n\n")
			}
		}
		parts := []message.ContentPart{
			message.ReasoningContent{
				Thinking:   b.String(),
				StartedAt:  testStartedAt,
				FinishedAt: testFinishedAt,
			},
		}
		if content != "" {
			parts = append(parts, message.TextContent{Text: content})
		}
		return &message.Message{ID: "tail-stream", Role: message.Assistant, Parts: parts}
	}

	item := NewAssistantMessageItem(&sty, build("first answer")).(*AssistantMessageItem)
	item.thinkingViewMode = thinkingTailWindow

	const width = 99
	_ = item.RawRender(width)
	first := snapshot(item)
	firstThinkingKey := keyOf(item.thinkingSec)
	firstContentKey := keyOf(item.contentSec)
	require.NotEmpty(t, first.thinking)

	item.SetMessage(build("first answer with more streaming text"))
	_ = item.RawRender(width)
	second := snapshot(item)
	secondThinkingKey := keyOf(item.thinkingSec)
	secondContentKey := keyOf(item.contentSec)

	require.Equal(t, firstThinkingKey, secondThinkingKey,
		"thinking section's (width, srcHash, extra) tuple must not move "+
			"across a content-only update — proves the cache key never invalidated")
	require.Equal(t, first.thinking, second.thinking,
		"content streaming must not invalidate the tail-windowed thinking cache")
	require.NotEqual(t, firstContentKey, secondContentKey,
		"content section's tuple MUST move; otherwise this test isn't exercising a real content change")
	require.NotEqual(t, first.content, second.content,
		"content section must have re-rendered")
}

// TestThinkingWindow_ToggleInvalidatesOnlyThinking verifies 那
// cycling thinkingViewMode invalidates thinking section 缓存
// alone — content 和 错误 caches survive across toggle.
//
// Like TestThinkingWindow_ContentChangeKeepsThinkingCacheInTailWindow,
// assertion 是 在 缓存 key tuple (width, srcHash, extra)
// 在 each section, 不 just 在 rendered bytes:
// - thinking's tuple MUST move (extra folds 在 thinkingViewMode)
// - content's 和 错误's tuples MUST NOT move (their keys depend
// 只 在 their own source 文本, untouched 通过 toggle).
func TestThinkingWindow_ToggleInvalidatesOnlyThinking(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	const total = 1500
	build := func() *message.Message {
		var b strings.Builder
		for i := 1; i <= total; i++ {
			b.WriteString("ln")
			b.WriteString(itoa(i))
			if i < total {
				b.WriteString("\n\n")
			}
		}
		return &message.Message{
			ID:   "toggle-iso",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{
					Thinking:   b.String(),
					StartedAt:  testStartedAt,
					FinishedAt: testFinishedAt,
				},
				message.TextContent{Text: "answer text"},
				message.Finish{
					Reason:  message.FinishReasonError,
					Message: "boom",
					Details: "details",
					Time:    testFinishTime,
				},
			},
		}
	}

	item := NewAssistantMessageItem(&sty, build()).(*AssistantMessageItem)

	const width = 101
	_ = item.RawRender(width)
	first := snapshot(item)
	firstThink := keyOf(item.thinkingSec)
	firstContent := keyOf(item.contentSec)
	firstErr := keyOf(item.errorSec)
	require.NotEmpty(t, first.thinking)
	require.NotEmpty(t, first.content)
	require.NotEmpty(t, first.errSec)

	// Cycle: collapsed -> tail-窗口. Only thinking should change.
	require.True(t, item.ToggleExpanded())
	require.Equal(t, thinkingTailWindow, item.thinkingViewMode)
	_ = item.RawRender(width)
	second := snapshot(item)
	secondThink := keyOf(item.thinkingSec)
	secondContent := keyOf(item.contentSec)
	secondErr := keyOf(item.errorSec)

	require.NotEqual(t, firstThink, secondThink,
		"thinking section's tuple MUST move on toggle (extra folds in thinkingViewMode)")
	require.Equal(t, firstContent, secondContent,
		"content section's tuple must not move on a thinking toggle")
	require.Equal(t, firstErr, secondErr,
		"error section's tuple must not move on a thinking toggle")
	require.NotEqual(t, first.thinking, second.thinking,
		"toggling into tail-window must re-render the thinking section")
	require.Equal(t, first.content, second.content,
		"toggling thinking view-mode must not invalidate the content section")
	require.Equal(t, first.errSec, second.errSec,
		"toggling thinking view-mode must not invalidate the error section")

	// Cycle: tail-窗口 -> full. Same expectation.
	require.True(t, item.ToggleExpanded())
	require.Equal(t, thinkingFullExpanded, item.thinkingViewMode)
	_ = item.RawRender(width)
	third := snapshot(item)
	thirdThink := keyOf(item.thinkingSec)
	thirdContent := keyOf(item.contentSec)
	thirdErr := keyOf(item.errorSec)

	require.NotEqual(t, secondThink, thirdThink,
		"thinking section's tuple MUST move on the second toggle as well")
	require.Equal(t, secondContent, thirdContent,
		"content section's tuple must remain stable across the second toggle")
	require.Equal(t, secondErr, thirdErr,
		"error section's tuple must remain stable across the second toggle")
	require.NotEqual(t, second.thinking, third.thinking,
		"toggling into full expansion must re-render the thinking section")
	require.Equal(t, second.content, third.content)
	require.Equal(t, second.errSec, third.errSec)
}

// TestThinkingWindow_BoxHeightTracksWindow asserts 那
// thinkingBoxHeight reflects WINDOWED render's height 在
// tail-窗口 mode, 不 (much larger) full thinking height.
// 这 是 what HandleMouseClick uses 到 detect whether click
// landed 在 thinking box, 所以 getting it wrong would make
// click detection extend off bottom 的 visible box.
func TestThinkingWindow_BoxHeightTracksWindow(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	const total = 5000
	msg := thinkingMessageWithLines("box-height", total)
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 103

	// Tail-窗口: height should 是 roughly cap.
	item.thinkingViewMode = thinkingTailWindow
	_ = item.RawRender(width)
	tailHeight := item.thinkingBoxHeight
	require.Greater(t, tailHeight, 0)
	require.LessOrEqual(t, tailHeight, maxExpandedThinkingTailLines+5,
		"tail-window box height must reflect the windowed render, not the full thinking height; got %d",
		tailHeight)

	// Full expansion: height 应当 grow well past tail cap.
	item.thinkingViewMode = thinkingFullExpanded
	_ = item.RawRender(width)
	fullHeight := item.thinkingBoxHeight
	require.Greater(t, fullHeight, maxExpandedThinkingTailLines*2,
		"full expansion box height must reflect the full thinking render; got %d",
		fullHeight)
}
