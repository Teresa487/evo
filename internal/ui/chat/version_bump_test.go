package chat

import (
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// versionedItem 是 cross-cutting interface every chat item type
// 必须 satisfy under F6: every documented mutator 必须 bump
// shared version 计数器 所以 list-level memo invalidates.
type versionedItem interface {
	list.Item
	Version() uint64
}

// requireBump asserts 那 supplied mutator advances item's
// Version(). mutator runs once; absent bump 是 regression
// ( list 缓存 would keep serving stale output).
func requireBump(t *testing.T, name string, item versionedItem, mutate func()) {
	t.Helper()
	before := item.Version()
	mutate()
	after := item.Version()
	require.Greaterf(t, after, before, "%s must bump Version() (before=%d, after=%d)", name, before, after)
}

// TestAssistantMessageItem_MutatorsBumpVersion enumerates every
// documented mutator 在 AssistantMessageItem 和 asserts each one
// advances Version().
func TestAssistantMessageItem_MutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	build := func(thinking, content string) *message.Message {
		parts := []message.ContentPart{
			message.ReasoningContent{
				Thinking:   thinking,
				StartedAt:  testStartedAt,
				FinishedAt: testFinishedAt,
			},
		}
		if content != "" {
			parts = append(parts, message.TextContent{Text: content})
		}
		return &message.Message{ID: "a-mut", Role: message.Assistant, Parts: parts}
	}

	item := NewAssistantMessageItem(&sty, build("thinking", "content")).(*AssistantMessageItem)

	requireBump(t, "SetMessage", item, func() {
		item.SetMessage(build("thinking", "more content"))
	})
	requireBump(t, "SetFocused", item, func() {
		item.SetFocused(true)
	})
	requireBump(t, "SetHighlight", item, func() {
		item.SetHighlight(0, 0, 0, 5)
	})
	// ToggleExpanded 只 mutates state 当 there 是 non-empty
	// thinking 文本 — which build helper provides.
	requireBump(t, "ToggleExpanded", item, func() {
		item.ToggleExpanded()
	})
}

// TestUserMessageItem_MutatorsBumpVersion enumerates UserMessageItem
// mutators.
func TestUserMessageItem_MutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	r := attachments.NewRenderer(sty.Attachments)
	msg := &message.Message{
		ID:   "u-mut",
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "Hello"},
		},
	}
	item := NewUserMessageItem(&sty, msg, r).(*UserMessageItem)

	requireBump(t, "SetFocused", item, func() {
		item.SetFocused(true)
	})
	requireBump(t, "SetHighlight", item, func() {
		item.SetHighlight(0, 0, 0, 3)
	})
}

// TestAssistantInfoItem_Versioned sanity-checks the AssistantInfoItem
// wiring. item carries 只 immutable data after construction; we
// still assert Version() 是 callable.
func TestAssistantInfoItem_Versioned(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	cfg := &config.Config{}
	msg := &message.Message{
		ID:    "info",
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.Finish{Reason: message.FinishReasonEndTurn, Time: time.Now().Unix()}},
	}
	item := NewAssistantInfoItem(&sty, msg, cfg, time.Unix(0, 0)).(*AssistantInfoItem)

	// Version() 是 callable 和 starts 在 zero.
	require.Equal(t, uint64(0), item.Version())
}

// TestBaseToolMessageItem_MutatorsBumpVersion enumerates the base
// 工具 item mutators. Specific 工具 types layer 在 top 的 这
// base; the base bumps cover the shared mutator surface.
func TestBaseToolMessageItem_MutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{ID: "tc1", Name: "bash", Input: "{}", Finished: false}
	item := NewToolMessageItem(&sty, "msg", tc, nil, false, "")

	v := item.(versionedItem)

	requireBump(t, "SetFocused", v, func() {
		if f, ok := item.(list.Focusable); ok {
			f.SetFocused(true)
		}
	})
	requireBump(t, "SetHighlight", v, func() {
		if h, ok := item.(list.Highlightable); ok {
			h.SetHighlight(0, 0, 0, 3)
		}
	})
	requireBump(t, "SetToolCall", v, func() {
		tc2 := tc
		tc2.Input = `{"command":"echo"}`
		item.SetToolCall(tc2)
	})
	requireBump(t, "SetResult", v, func() {
		item.SetResult(&message.ToolResult{ToolCallID: "tc1", Content: "ok"})
	})
	requireBump(t, "SetStatus", v, func() {
		item.SetStatus(ToolStatusSuccess)
	})
	requireBump(t, "ToggleExpanded", v, func() {
		if e, ok := item.(Expandable); ok {
			e.ToggleExpanded()
		}
	})
	requireBump(t, "SetCompact", v, func() {
		if c, ok := item.(Compactable); ok {
			c.SetCompact(true)
		}
	})
}

// TestAssistantMessageItem_AdvanceBumpsVersion covers the spinner
// regression: while 助手消息 是 spinning, every clock
// frame fed through Advance must bump Version() 所以 list-level
// 缓存 invalidates 和 next draw re-renders advanced spinner
// frame. Without 这 bump cached 条目's version stays put 和
// the spinner appears frozen.
func TestAssistantMessageItem_AdvanceBumpsVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	streaming := &message.Message{
		ID:   "spin",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "thinking..."},
		},
	}
	item := NewAssistantMessageItem(&sty, streaming).(*AssistantMessageItem)
	require.True(t, item.Spinning())

	requireBump(t, "Advance", item, func() {
		item.Advance()
	})

	// 一个 non-spinning item must 不 bump 在 Advance: bump 只
	// makes sense while spinner 是 live, 和 stray bump 在
	// finished item would needlessly invalidate 它的 缓存 条目.
	finished := &message.Message{
		ID:   "spin",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "done"},
			message.Finish{Reason: message.FinishReasonEndTurn, Time: testFinishTime},
		},
	}
	item.SetMessage(finished)
	require.False(t, item.Spinning())
	before := item.Version()
	item.Advance()
	require.Equal(t, before, item.Version(), "Advance must not bump Version() on a non-spinning item")
}

// TestAgentToolMessageItem_NestedToolMutatorsBumpVersion covers B1:
// nested-工具 mutators 在 AgentToolMessageItem must bump
// Version() 所以 list 缓存 invalidates 它的 条目 当
// nested 工具 是 added 或 slice changes. SetNestedTools always
// bumps unconditionally — live 更新 路径 在
// internal/ui/模型/ui.go mutates existing children 在 place 和
// then re-passes same slice, 所以 指针-equality dedupe would
// hide observable child-render changes. AddNestedTool also always
// observably mutates state 和 always bumps.
func TestAgentToolMessageItem_NestedToolMutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	parent := message.ToolCall{ID: "agent-parent", Name: "agent", Input: `{}`, Finished: false}
	item := NewAgentToolMessageItem(&sty, parent, nil, false)

	mkChild := func(id string) ToolMessageItem {
		tc := message.ToolCall{ID: id, Name: "bash", Input: `{}`, Finished: false}
		return NewToolMessageItem(&sty, "msg", tc, nil, false, "")
	}

	// AddNestedTool always bumps.
	requireBump(t, "AddNestedTool", item, func() {
		item.AddNestedTool(mkChild("c1"))
	})

	// SetNestedTools always bumps, even 用 指针-equal slice.
	current := append([]ToolMessageItem(nil), item.NestedTools()...)
	requireBump(t, "SetNestedTools[pointer-equal]", item, func() {
		item.SetNestedTools(current)
	})

	// SetNestedTools 用 different slice (extra element) bumps.
	requireBump(t, "SetNestedTools[different]", item, func() {
		item.SetNestedTools(append(current, mkChild("c2")))
	})

	// SetNestedTools 到 empty slice 从 non-empty state bumps.
	requireBump(t, "SetNestedTools[empty]", item, func() {
		item.SetNestedTools(nil)
	})
}

// TestAgenticFetchToolMessageItem_NestedToolMutatorsBumpVersion 是
// agentic-fetch counterpart 到 agent-工具 nested mutator
// bump 测试 above.
func TestAgenticFetchToolMessageItem_NestedToolMutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	parent := message.ToolCall{ID: "fetch-parent", Name: "agentic_fetch", Input: `{}`, Finished: false}
	item := NewAgenticFetchToolMessageItem(&sty, parent, nil, false)

	mkChild := func(id string) ToolMessageItem {
		tc := message.ToolCall{ID: id, Name: "fetch", Input: `{}`, Finished: false}
		return NewToolMessageItem(&sty, "msg", tc, nil, false, "")
	}

	requireBump(t, "AddNestedTool", item, func() {
		item.AddNestedTool(mkChild("c1"))
	})

	current := append([]ToolMessageItem(nil), item.NestedTools()...)
	requireBump(t, "SetNestedTools[pointer-equal]", item, func() {
		item.SetNestedTools(current)
	})

	requireBump(t, "SetNestedTools[different]", item, func() {
		item.SetNestedTools(append(current, mkChild("c2")))
	})

	requireBump(t, "SetNestedTools[empty]", item, func() {
		item.SetNestedTools(nil)
	})
}

// TestAgentToolMessageItem_NestedChildInPlaceMutationBumpsParent 是
// T5 regression 测试: it mirrors live 更新 flow 在
// internal/ui/模型/ui.go:1242-1281 where nested 工具 calls 是
// updated 在 place (SetToolCall / SetResult 在 same child
// pointers) 和 then same slice 是 handed back 到 parent
// via SetNestedTools. parent must still bump 它的 version 所以
// list 缓存 invalidates parent's pre-rendered string 和
// the freshly-rendered child output becomes visible.
func TestAgentToolMessageItem_NestedChildInPlaceMutationBumpsParent(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	parent := message.ToolCall{ID: "agent-parent", Name: "agent", Input: `{}`, Finished: false}
	item := NewAgentToolMessageItem(&sty, parent, nil, false)

	childTC := message.ToolCall{ID: "c1", Name: "bash", Input: `{}`, Finished: false}
	child := NewToolMessageItem(&sty, "msg", childTC, nil, false, "")
	item.AddNestedTool(child)

	v0 := item.Version()
	childVersionBefore := child.(versionedItem).Version()

	// In-place mutate the existing child, exactly like the live
	// flow 在 ui.go:1271-1278 does.
	child.SetResult(&message.ToolResult{ToolCallID: "c1", Content: "ok"})
	require.Greaterf(t, child.(versionedItem).Version(), childVersionBefore,
		"child SetResult must bump child version")

	// Hand same slice back 到 parent (pointers unchanged).
	same := item.NestedTools()
	item.SetNestedTools(same)
	require.Greaterf(t, item.Version(), v0,
		"parent SetNestedTools must bump even when child pointers are unchanged (in-place child mutation invalidates parent's pre-rendered output)")
}

// TestAgenticFetchToolMessageItem_NestedChildInPlaceMutationBumpsParent
// 是 agentic-fetch counterpart 的 T5 regression 测试.
func TestAgenticFetchToolMessageItem_NestedChildInPlaceMutationBumpsParent(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	parent := message.ToolCall{ID: "fetch-parent", Name: "agentic_fetch", Input: `{}`, Finished: false}
	item := NewAgenticFetchToolMessageItem(&sty, parent, nil, false)

	childTC := message.ToolCall{ID: "c1", Name: "fetch", Input: `{}`, Finished: false}
	child := NewToolMessageItem(&sty, "msg", childTC, nil, false, "")
	item.AddNestedTool(child)

	v0 := item.Version()
	childVersionBefore := child.(versionedItem).Version()

	child.SetResult(&message.ToolResult{ToolCallID: "c1", Content: "ok"})
	require.Greaterf(t, child.(versionedItem).Version(), childVersionBefore,
		"child SetResult must bump child version")

	same := item.NestedTools()
	item.SetNestedTools(same)
	require.Greaterf(t, item.Version(), v0,
		"parent SetNestedTools must bump even when child pointers are unchanged")
}

// requireNoBump asserts the supplied mutator leaves the item's
// Version() unchanged. The mutator runs once; an unexpected bump
// would force F6 list memo 到 re-render item whose output
// did 不 change, churning 缓存.
func requireNoBump(t *testing.T, name string, item versionedItem, mutate func()) {
	t.Helper()
	before := item.Version()
	mutate()
	after := item.Version()
	require.Equalf(t, before, after,
		"%s must not bump Version() (before=%d, after=%d)", name, before, after)
}

// TestBaseToolMessageItem_AdvanceBumpsVersion 是 spinner
// regression 测试 为 non-agent tools: while 工具 是 spinning,
// every clock frame must bump Version() 所以 list-level 缓存
// invalidates 和 next draw re-renders advanced spinner
// frame. 一个 finished 工具 must 不 bump 和 must 报告 那 it no
// longer needs frames.
func TestBaseToolMessageItem_AdvanceBumpsVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{ID: "tc-spin", Name: "bash", Input: "{}", Finished: false}
	item := NewToolMessageItem(&sty, "msg", tc, nil, false, "")
	v := item.(versionedItem)
	a, ok := item.(Animatable)
	require.True(t, ok, "base tool message item must implement Animatable")
	require.True(t, a.Spinning())

	requireBump(t, "Advance[spinning]", v, func() {
		a.Advance()
	})

	// Finished → no bump. 一个 stray bump would needlessly invalidate
	// 缓存 条目.
	tcFinished := tc
	tcFinished.Finished = true
	item.SetToolCall(tcFinished)
	item.SetResult(&message.ToolResult{ToolCallID: tc.ID, Content: "ok"})
	require.False(t, a.Spinning(), "finished tool must not request frames")

	requireNoBump(t, "Advance[finished]", v, func() {
		a.Advance()
	})
}

// TestAgentToolMessageItem_AdvanceBumpsVersion 是 spinner
// regression 测试 为 agent tools. One clock frame must advance
// parent 和 every spinning nested 工具 和 bump parent, because
// list 只 checks parent's version — nested tools 是 不
// list entries 的 their own. 一个 parent 用 结果 must 不 bump.
func TestAgentToolMessageItem_AdvanceBumpsVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	parentTC := message.ToolCall{ID: "agent-parent", Name: "agent", Input: `{}`, Finished: false}
	parent := NewAgentToolMessageItem(&sty, parentTC, nil, false)

	childTC := message.ToolCall{ID: "agent-child", Name: "bash", Input: `{}`, Finished: false}
	child := NewToolMessageItem(&sty, "msg", childTC, nil, false, "")
	parent.AddNestedTool(child)
	childV := child.(versionedItem)

	requireBump(t, "Advance[spinning,parent]", parent, func() {
		parent.Advance()
	})
	// nested child must have advanced 在 same frame.
	requireBump(t, "Advance[spinning,nested]", childV, func() {
		parent.Advance()
	})

	// Once parent has 结果, nothing bumps.
	parent.SetResult(&message.ToolResult{ToolCallID: parentTC.ID, Content: "done"})
	require.False(t, parent.Spinning())
	requireNoBump(t, "Advance[finished,parent]", parent, func() {
		parent.Advance()
	})
	requireNoBump(t, "Advance[finished,nested]", childV, func() {
		parent.Advance()
	})
}

// TestAgenticFetchToolMessageItem_AdvanceBumpsVersion 是
// agentic-fetch counterpart 的 agent-工具 Advance bump 测试.
// Without an explicit override the embedded base Advance would never
// advance nested children; 这 测试 locks 在 override.
func TestAgenticFetchToolMessageItem_AdvanceBumpsVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	parentTC := message.ToolCall{ID: "fetch-parent", Name: "agentic_fetch", Input: `{}`, Finished: false}
	parent := NewAgenticFetchToolMessageItem(&sty, parentTC, nil, false)

	childTC := message.ToolCall{ID: "fetch-child", Name: "fetch", Input: `{}`, Finished: false}
	child := NewToolMessageItem(&sty, "msg", childTC, nil, false, "")
	parent.AddNestedTool(child)
	childV := child.(versionedItem)

	requireBump(t, "Advance[spinning,parent]", parent, func() {
		parent.Advance()
	})
	requireBump(t, "Advance[spinning,nested]", childV, func() {
		parent.Advance()
	})

	parent.SetResult(&message.ToolResult{ToolCallID: parentTC.ID, Content: "done"})
	requireNoBump(t, "Advance[finished,parent]", parent, func() {
		parent.Advance()
	})
	requireNoBump(t, "Advance[finished,nested]", childV, func() {
		parent.Advance()
	})
}
