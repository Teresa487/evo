package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// Fixed Unix timestamps 为 deterministic 缓存-equality tests.
// thinking section's `extra` hash folds 在 ThinkingDuration, which
// 在 turn depends 在 (FinishedAt - StartedAt). Anchoring both
// timestamps removes any wall-clock dependency 从 缓存 key
// 所以 two builds across second boundary still hit 缓存.
const (
	testStartedAt  int64 = 1_700_000_000
	testFinishedAt int64 = 1_700_000_005
	testFinishTime int64 = 1_700_000_006
)

// thinkingMessage builds 助手消息 用 fixed reasoning
// content 和 optional 文本 content. When 文本 是 empty
// 消息 represents still-thinking turn (matches IsThinking()).
// Both reasoning timestamps 是 anchored 到 fixed Unix seconds 所以
// ThinkingDuration 是 deterministic 和 缓存-equality assertions
// don't depend 在 wall-clock time.
func thinkingMessage(id, thinking, text string) *message.Message {
	parts := []message.ContentPart{
		message.ReasoningContent{
			Thinking:   thinking,
			StartedAt:  testStartedAt,
			FinishedAt: testFinishedAt,
		},
	}
	if text != "" {
		parts = append(parts, message.TextContent{Text: text})
	}
	return &message.Message{
		ID:    id,
		Role:  message.Assistant,
		Parts: parts,
	}
}

// errorMessage builds finished 助手消息 whose finish 片段
// carries 错误 reason plus custom 消息 和 details.
func errorMessage(id, errMsg, errDetails string) *message.Message {
	return &message.Message{
		ID:   id,
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "partial output"},
			message.Finish{
				Reason:  message.FinishReasonError,
				Message: errMsg,
				Details: errDetails,
				Time:    testFinishTime,
			},
		},
	}
}

// renderTwoSetMessages drives SetMessage cycle 和 returns
// section-缓存 identity (out string pointers via direct comparison
// 的 cached fields). 测试 compares `out` strings; identical
// output across cycles 是 缓存-hit indicator we rely 在.
type sectionSnapshot struct {
	thinking string
	content  string
	errSec   string
}

func snapshot(a *AssistantMessageItem) sectionSnapshot {
	return sectionSnapshot{
		thinking: a.thinkingSec.out,
		content:  a.contentSec.out,
		errSec:   a.errorSec.out,
	}
}

// TestAssistantSectionCache_ContentChangeDoesNotInvalidateThinking covers
// the central F4 invariant: streaming the main content through SetMessage
// must keep cached thinking render intact, provided inputs 到
// thinking section render (文本, expanded flag, footer state) 是
// unchanged. We seed already-non-empty content 所以 那 IsThinking()
// 是 false 在 both renders — 那's steady streaming state where
// thinking block has finished 和 content keeps growing.
func TestAssistantSectionCache_ContentChangeDoesNotInvalidateThinking(t *testing.T) {
	sty := styles.CharmtonePantera()
	thinking := "Step 1\nStep 2\nStep 3"
	msg := thinkingMessage("a1", thinking, "Initial answer.")
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 71

	_ = item.RawRender(width)
	first := snapshot(item)
	require.NotEmpty(t, first.thinking, "thinking section must be populated after first render")

	// 流 more content into existing turn. Thinking 文本 和
	// footer state 是 byte-identical between two renders.
	updated := thinkingMessage("a1", thinking, "Initial answer. More streamed text.")
	item.SetMessage(updated)
	_ = item.RawRender(width)
	second := snapshot(item)

	require.Equal(t, first.thinking, second.thinking,
		"content streaming must not invalidate the thinking section render")
	require.NotEqual(t, first.content, second.content,
		"content section must have been re-rendered")
}

// TestAssistantSectionCache_ThinkingChangeDoesNotInvalidateContent 是
// mirror 的 previous 测试: extending thinking 文本 must 不 force
// re-render 的 content section.
func TestAssistantSectionCache_ThinkingChangeDoesNotInvalidateContent(t *testing.T) {
	sty := styles.CharmtonePantera()
	content := "Final answer goes here."
	msg := thinkingMessage("a2", "Step 1", content)
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 73

	_ = item.RawRender(width)
	first := snapshot(item)
	require.NotEmpty(t, first.content)

	updated := thinkingMessage("a2", "Step 1\nStep 2", content)
	item.SetMessage(updated)
	_ = item.RawRender(width)
	second := snapshot(item)

	require.Equal(t, first.content, second.content,
		"thinking streaming must not invalidate the content section render")
	require.NotEqual(t, first.thinking, second.thinking,
		"thinking text changed; thinking section must have re-rendered")
}

// TestAssistantSectionCache_HashKeyDiscrimination asserts 那 two
// messages 用 different source 文本 hash 到 different per-section
// keys, 和 那 messages 用 identical source 文本 hit 缓存.
func TestAssistantSectionCache_HashKeyDiscrimination(t *testing.T) {
	sty := styles.CharmtonePantera()
	msgA := thinkingMessage("a3", "thinking A", "content A")
	msgB := thinkingMessage("a3", "thinking B", "content B")

	itemA := NewAssistantMessageItem(&sty, msgA).(*AssistantMessageItem)
	itemB := NewAssistantMessageItem(&sty, msgB).(*AssistantMessageItem)

	thinkSrcA, _ := itemA.thinkingKey()
	thinkSrcB, _ := itemB.thinkingKey()
	require.NotEqual(t, thinkSrcA, thinkSrcB,
		"distinct thinking text must produce distinct FNV-64 source hashes")

	contentSrcA, _ := itemA.contentKey()
	contentSrcB, _ := itemB.contentKey()
	require.NotEqual(t, contentSrcA, contentSrcB,
		"distinct content text must produce distinct FNV-64 source hashes")

	// Identical source 文本 在 fresh item must produce same
	// hashes — keying invariant 为 缓存 hits.
	itemAClone := NewAssistantMessageItem(&sty, thinkingMessage("a3", "thinking A", "content A")).(*AssistantMessageItem)
	thinkSrcAClone, _ := itemAClone.thinkingKey()
	contentSrcAClone, _ := itemAClone.contentKey()
	require.Equal(t, thinkSrcA, thinkSrcAClone)
	require.Equal(t, contentSrcA, contentSrcAClone)
}

// TestAssistantSectionCache_CloneRoundTrip guards contract 那
// 消息.Clone() does 不 invalidate any section 缓存: re-keying off
// cloned 消息 must produce identical hashes 和 section
// caches 必须 serve byte-identical 渲染.
func TestAssistantSectionCache_CloneRoundTrip(t *testing.T) {
	sty := styles.CharmtonePantera()
	orig := thinkingMessage("a4", "Reasoning step.", "Answer text.")
	item := NewAssistantMessageItem(&sty, orig).(*AssistantMessageItem)

	const width = 75
	_ = item.RawRender(width)
	first := snapshot(item)

	cloned := orig.Clone()
	item.SetMessage(&cloned)
	_ = item.RawRender(width)
	second := snapshot(item)

	require.Equal(t, first.thinking, second.thinking, "clone must hit the thinking cache")
	require.Equal(t, first.content, second.content, "clone must hit the content cache")
}

// TestAssistantSectionCache_ResizeInvalidatesAll asserts 那 width
// change forces re-render 的 every section.
func TestAssistantSectionCache_ResizeInvalidatesAll(t *testing.T) {
	sty := styles.CharmtonePantera()
	msg := errorMessage("a5", "boom", strings.Repeat("detail line\n", 5))
	// errorMessage returns FinishReasonError; combine 用 thinking
	// content 所以 all three sections 是 exercised.
	msg.Parts = append([]message.ContentPart{
		message.ReasoningContent{
			Thinking:   "Considering options.",
			StartedAt:  testStartedAt,
			FinishedAt: testFinishedAt,
		},
	}, msg.Parts...)
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	_ = item.RawRender(77)
	first := snapshot(item)
	require.NotEmpty(t, first.thinking)
	require.NotEmpty(t, first.content)
	require.NotEmpty(t, first.errSec)

	_ = item.RawRender(117)
	second := snapshot(item)

	require.NotEqual(t, first.thinking, second.thinking, "resize must re-render the thinking section")
	require.NotEqual(t, first.content, second.content, "resize must re-render the content section")
	require.NotEqual(t, first.errSec, second.errSec, "resize must re-render the error section")
}

// TestAssistantSectionCache_ErrorIndependentOfThinkingAndContent guards
// 那 错误 section caches independently. Editing 错误
// 消息 must 不 invalidate other two sections, 和 editing
// content must 不 invalidate 错误 section.
func TestAssistantSectionCache_ErrorIndependentOfThinkingAndContent(t *testing.T) {
	sty := styles.CharmtonePantera()
	build := func(thinking, content, errMsg, errDetails string) *message.Message {
		return &message.Message{
			ID:   "a6",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{
					Thinking:   thinking,
					StartedAt:  testStartedAt,
					FinishedAt: testFinishedAt,
				},
				message.TextContent{Text: content},
				message.Finish{
					Reason:  message.FinishReasonError,
					Message: errMsg,
					Details: errDetails,
					Time:    testFinishTime,
				},
			},
		}
	}

	item := NewAssistantMessageItem(&sty, build("think", "content", "boom", "details")).(*AssistantMessageItem)
	_ = item.RawRender(79)
	first := snapshot(item)

	// Change 只 错误 文本. Thinking 和 content caches must
	// survive; 错误 缓存 must miss 和 re-render.
	item.SetMessage(build("think", "content", "different boom", "different details"))
	_ = item.RawRender(79)
	second := snapshot(item)

	require.Equal(t, first.thinking, second.thinking, "error change must not invalidate thinking")
	require.Equal(t, first.content, second.content, "error change must not invalidate content")
	require.NotEqual(t, first.errSec, second.errSec, "error change must re-render the error section")

	// Now change 只 content; 错误 缓存 must survive.
	item.SetMessage(build("think", "different content", "different boom", "different details"))
	_ = item.RawRender(79)
	third := snapshot(item)

	require.Equal(t, second.thinking, third.thinking)
	require.NotEqual(t, second.content, third.content)
	require.Equal(t, second.errSec, third.errSec, "content change must not invalidate the error section")
}

// TestAssistantSectionCache_PrefixCacheRespectsSectionChanges guards
// F3/F4 boundary: prefix 缓存 must invalidate 当 any
// underlying section changes. We verify 通过 comparing F3-cached
// Render output across SetMessage cycles.
func TestAssistantSectionCache_PrefixCacheRespectsSectionChanges(t *testing.T) {
	sty := styles.CharmtonePantera()
	build := func(content string) *message.Message {
		return &message.Message{
			ID:   "a7",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: content},
				message.Finish{Reason: message.FinishReasonEndTurn, Time: testFinishTime},
			},
		}
	}

	item := NewAssistantMessageItem(&sty, build("first content")).(*AssistantMessageItem)
	item.SetFocused(true)

	const width = 81
	first := item.Render(width)

	item.SetMessage(build("second content"))
	second := item.Render(width)
	require.NotEqual(t, first, second,
		"prefix cache must invalidate when the content section changes")

	// Re-set 到 original content; prefix 缓存 should
	// produce identical output again.
	item.SetMessage(build("first content"))
	third := item.Render(width)
	require.Equal(t, first, third)
}

// TestAssistantSectionCache_ByteIdenticalToFreshRender asserts 那
// F4 cached 路径 produces same bytes 作为 fresh-instance render 的
// equivalent 消息 — i.e. caching 是 invisible 从 outside.
// Drives 序号 的 mutations (thinking change, content change,
// finish) 和 compares every step against independent item rendered
// 从 scratch.
func TestAssistantSectionCache_ByteIdenticalToFreshRender(t *testing.T) {
	sty := styles.CharmtonePantera()
	const width = 83

	type step struct {
		name string
		msg  *message.Message
	}
	startedAt := testStartedAt
	finishedAt := testFinishedAt
	finishTime := testFinishTime
	steps := []step{
		{
			name: "thinking-only",
			msg: &message.Message{
				ID: "iso", Role: message.Assistant,
				Parts: []message.ContentPart{
					message.ReasoningContent{Thinking: "first reasoning", StartedAt: startedAt},
				},
			},
		},
		{
			name: "thinking-grew",
			msg: &message.Message{
				ID: "iso", Role: message.Assistant,
				Parts: []message.ContentPart{
					message.ReasoningContent{Thinking: "first reasoning more", StartedAt: startedAt},
				},
			},
		},
		{
			name: "content-arrived",
			msg: &message.Message{
				ID: "iso", Role: message.Assistant,
				Parts: []message.ContentPart{
					message.ReasoningContent{Thinking: "first reasoning more", StartedAt: startedAt, FinishedAt: finishedAt},
					message.TextContent{Text: "the answer"},
				},
			},
		},
		{
			name: "finished-end-turn",
			msg: &message.Message{
				ID: "iso", Role: message.Assistant,
				Parts: []message.ContentPart{
					message.ReasoningContent{Thinking: "first reasoning more", StartedAt: startedAt, FinishedAt: finishedAt},
					message.TextContent{Text: "the answer"},
					message.Finish{Reason: message.FinishReasonEndTurn, Time: finishTime},
				},
			},
		},
	}

	first := steps[0].msg.Clone()
	cached := NewAssistantMessageItem(&sty, &first).(*AssistantMessageItem)
	for _, s := range steps {
		cached.SetMessage(s.msg)
		freshMsg := s.msg.Clone()
		fresh := NewAssistantMessageItem(&sty, &freshMsg).(*AssistantMessageItem)
		require.Equal(t, fresh.RawRender(width), cached.RawRender(width),
			"step %q: cached path must match fresh render byte-for-byte", s.name)
	}
}

// TestAssistantSectionCache_PrefixCacheInvalidatesOnCompositionOnlyChange
// guards F3 prefix 缓存 against composition-只 changes:
// flipping finish reason 从 EndTurn 到 Canceled appends
// constant "Canceled" line via renderMessageContent, 但 no
// section's own source 文本 changes. prefix 缓存 must observe
// difference (compositionKey 是 folded into prefixCacheKey) 和
// resulting bytes 必须 differ. As second guarantee, fresh
// item built 用 same final state must produce byte-equal
// output 到 cached item — caching must never produce stale 或
// divergent 渲染.
func TestAssistantSectionCache_PrefixCacheInvalidatesOnCompositionOnlyChange(t *testing.T) {
	sty := styles.CharmtonePantera()
	const width = 87

	build := func(reason message.FinishReason) *message.Message {
		return &message.Message{
			ID:   "comp",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "hi"},
				message.Finish{Reason: reason, Time: testFinishTime},
			},
		}
	}

	item := NewAssistantMessageItem(&sty, build(message.FinishReasonEndTurn)).(*AssistantMessageItem)
	endTurnOut := item.Render(width)

	// Flip 只 finish reason. Thinking 是 empty 和 content
	// 文本 是 unchanged, 所以 no section's source hash moves; 只
	// compositionKey shifts. prefix 缓存 must miss.
	item.SetMessage(build(message.FinishReasonCanceled))
	canceledOut := item.Render(width)
	require.NotEqual(t, endTurnOut, canceledOut,
		"prefix cache must invalidate on composition-only change (finish reason)")

	// 一个 fresh item built 用 same final state must match
	// cached item byte-为-byte — caching 是 invisible 从
	// outside 和 never serves stale output.
	fresh := NewAssistantMessageItem(&sty, build(message.FinishReasonCanceled)).(*AssistantMessageItem)
	require.Equal(t, fresh.Render(width), canceledOut,
		"cached output must equal a fresh render of the same final state")
}

// TestAssistantSectionCache_ThinkingBoxHeightSurvivesCacheHit guards
// click-detection geometry across thinking-section 缓存 hits.
// thinking box height feeds HandleMouseClick; it 是 recomputed
// inside renderThinking 和 must 是 restored 从
// assistantSection.aux 当 thinking 缓存 hits. We render once
// 到 capture original height, trigger content-只 change 所以
// thinkingKey stays identical (thinking 文本, expanded flag, 和
// footer state all unchanged), render again, 和 assert
// thinkingBoxHeight 字段 是 preserved.
func TestAssistantSectionCache_ThinkingBoxHeightSurvivesCacheHit(t *testing.T) {
	sty := styles.CharmtonePantera()
	const width = 71

	thinking := strings.Join([]string{
		"Considering the request.",
		"Looking at the relevant files.",
		"Drafting a plan.",
		"Verifying constraints.",
	}, "\n")
	msg := thinkingMessage("hbox", thinking, "initial answer")
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	item.thinkingViewMode = thinkingFullExpanded

	_ = item.RawRender(width)
	originalHeight := item.thinkingBoxHeight
	require.Greater(t, originalHeight, 0,
		"thinking box height must be populated after first render")

	// Stomp 字段 所以 stale 读取 (缓存 hit 那 fails 到
	// restore aux) 是 detectable. Then trigger content-只
	// change: thinkingKey 是 byte-identical between renders, 所以
	// thinking section 缓存 must hit 和 restore
	// preserved height via assistantSection.aux.
	item.thinkingBoxHeight = -1
	updated := thinkingMessage("hbox", thinking, "initial answer with more streamed text")
	item.SetMessage(updated)
	_ = item.RawRender(width)

	require.Equal(t, originalHeight, item.thinkingBoxHeight,
		"thinkingBoxHeight must be preserved across thinking section cache hits "+
			"so HandleMouseClick keeps targeting the right rows")
}
