package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// renderCountingItem wraps real chat item 和 counts Render calls
// 到 expose list-level 缓存 behaviour 到 tests. wrapper
// forwards list.Item methods exercised 通过 这 测试 — Render,
// Version, Finished — plus the list.Highlightable surface
// (SetHighlight / Highlight) used 通过 callback-driven scenario.
// Focus 是 不 exercised here, 所以 list.Focusable 是 不 forwarded;
// add SetFocused/IsFocused 如果 future 测试 needs 到 drive focus
// through the wrapper.
type renderCountingItem struct {
	inner       MessageItem
	renderHits  int
	highlightCb func(start [4]int)
}

func newRenderCountingItem(inner MessageItem) *renderCountingItem {
	return &renderCountingItem{inner: inner}
}

func (r *renderCountingItem) Render(width int) string {
	r.renderHits++
	return r.inner.Render(width)
}

func (r *renderCountingItem) Version() uint64 {
	return r.inner.(versionedItem).Version()
}

// SetHighlight forwards 到 embedded item; underlying
// highlightableMessageItem dedupes equivalent ranges 和 bumps
// shared version 在 observable change.
func (r *renderCountingItem) SetHighlight(startLine, startCol, endLine, endCol int) {
	if h, ok := r.inner.(list.Highlightable); ok {
		h.SetHighlight(startLine, startCol, endLine, endCol)
		if r.highlightCb != nil {
			r.highlightCb([4]int{startLine, startCol, endLine, endCol})
		}
	}
}

func (r *renderCountingItem) Highlight() (int, int, int, int) {
	if h, ok := r.inner.(list.Highlightable); ok {
		return h.Highlight()
	}
	return -1, -1, -1, -1
}

// TestList_CallbackDrivenHighlightCaching covers F6 缓存 along
// live applyHighlightRange 路径. Instead 的
// driving BeginSelectionDrag directly, 测试 registers render
// callback 那 mutates chat items' highlight ranges (just like
// Chat.applyHighlightRange does 在 production) 和 verifies
// resulting 缓存 behaviour:
//
// - Items inside 活跃 range pick up SetHighlight 调用,
// their version bumps, F6 缓存 invalidates, 和 list
// re-renders them 在 next draw, 所以 存储 output reflects
//     the highlight.
// - Subsequent draws while range 是 unchanged 是 缓存 hits:
// callback's SetHighlight 调用 dedupes (same range),
// version 是 stable, 和 list serves previous output
//     verbatim without calling Render.
//   - When the range moves OFF an item, the callback clears the
// highlight, version bumps, 和 item re-renders once;
// further draws 是 缓存 hits.
func TestList_CallbackDrivenHighlightCaching(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	// Build three finished 助手 messages. renderCountingItem
	// wrapper lets 测试 see per-item Render calls.
	mk := func(id, body string) *renderCountingItem {
		msg := &message.Message{
			ID:   id,
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{
					Thinking:   "thinking",
					StartedAt:  testStartedAt,
					FinishedAt: testFinishedAt,
				},
				message.TextContent{Text: body},
				message.Finish{Reason: message.FinishReasonEndTurn, Time: testFinishTime},
			},
		}
		inner := NewAssistantMessageItem(&sty, msg)
		return newRenderCountingItem(inner)
	}

	a := mk("a", "alpha")
	b := mk("b", "bravo")
	c := mk("c", "charlie")

	l := list.NewList(a, b, c)
	l.SetSize(80, 30)

	// activeRange holds the inclusive [start, end] item indexes the
	// callback should highlight. -1 means no 活跃 selection.
	activeRange := [2]int{-1, -1}

	cb := func(idx, _ int, item list.Item) list.Item {
		hi, ok := item.(list.Highlightable)
		if !ok {
			return item
		}
		if activeRange[0] >= 0 && idx >= activeRange[0] && idx <= activeRange[1] {
			// Inside the range: highlight the entire item.
			hi.SetHighlight(0, 0, -1, -1)
		} else {
			// Outside the range: clear highlight.
			hi.SetHighlight(-1, -1, -1, -1)
		}
		return item
	}
	l.RegisterRenderCallback(cb)

	// First render populates 缓存. Each item renders exactly
	// once even though callback runs 为 all three.
	_ = l.Render()
	require.Equal(t, 1, a.renderHits, "first render: a renders once")
	require.Equal(t, 1, b.renderHits, "first render: b renders once")
	require.Equal(t, 1, c.renderHits, "first render: c renders once")

	// Subsequent renders without 活跃 range 是 缓存 hits.
	// callback's SetHighlight 调用 dedupes (already cleared),
	// no version bump, cached entries served verbatim.
	for range 3 {
		_ = l.Render()
	}
	require.Equal(t, 1, a.renderHits, "cached item must not re-render across stable draws")
	require.Equal(t, 1, b.renderHits, "cached item must not re-render across stable draws")
	require.Equal(t, 1, c.renderHits, "cached item must not re-render across stable draws")

	// Activate selection range over items 和 b. callback
	// will SetHighlight 在 both during next render, bumping
	// their versions. 缓存 hit fails (version mismatch) 和
	// each 在-range item re-renders exactly once.
	activeRange = [2]int{0, 1}
	_ = l.Render()
	require.Equal(t, 2, a.renderHits, "in-range item must re-render after SetHighlight")
	require.Equal(t, 2, b.renderHits, "in-range item must re-render after SetHighlight")
	require.Equal(t, 1, c.renderHits, "out-of-range item stays cached")

	// Verify highlight actually landed 在 在-range items.
	sLine, _, eLine, _ := a.Highlight()
	require.Equal(t, 0, sLine)
	require.Equal(t, -1, eLine)
	sLine, _, eLine, _ = c.Highlight()
	require.Equal(t, -1, sLine, "out-of-range item must not be highlighted")
	require.Equal(t, -1, eLine)

	// While range stays same, subsequent renders 是 缓存
	// hits. The callback dedupes (same range), no version bump,
	// post-render 条目 served verbatim.
	for range 3 {
		_ = l.Render()
	}
	require.Equal(t, 2, a.renderHits, "in-range item is cached after the highlight render")
	require.Equal(t, 2, b.renderHits, "in-range item is cached after the highlight render")
	require.Equal(t, 1, c.renderHits, "out-of-range item stays cached")

	// Move the range off the items entirely. The callback clears
	// each 在-range item's highlight back 到 (-1,-1,-1,-1), which
	// bumps their versions 和 triggers exactly one re-render
	// each.
	activeRange = [2]int{-1, -1}
	_ = l.Render()
	require.Equal(t, 3, a.renderHits, "exiting-range item must re-render once when highlight clears")
	require.Equal(t, 3, b.renderHits, "exiting-range item must re-render once when highlight clears")
	require.Equal(t, 1, c.renderHits, "never-highlighted item stays cached")

	// Confirm the highlight has been fully cleared.
	sLine, _, eLine, _ = a.Highlight()
	require.Equal(t, -1, sLine)
	require.Equal(t, -1, eLine)

	// And subsequent renders 是 缓存 hits again.
	for range 3 {
		_ = l.Render()
	}
	require.Equal(t, 3, a.renderHits, "cached item must not re-render across stable draws")
	require.Equal(t, 3, b.renderHits, "cached item must not re-render across stable draws")
	require.Equal(t, 1, c.renderHits, "never-highlighted item stays cached")
}
