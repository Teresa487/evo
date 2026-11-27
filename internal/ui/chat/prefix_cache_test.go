package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// finishedAssistantMessage builds 助手消息 用 文本 content 和
// finish 片段 所以 AssistantMessageItem.isSpinning returns false 和
// prefix 缓存 是 exercised.
func finishedAssistantMessage(id, text string) *message.Message {
	return &message.Message{
		ID:   id,
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: text},
			message.Finish{Reason: message.FinishReasonEndTurn, Time: time.Now().Unix()},
		},
	}
}

// TestAssistantMessageItemRender_PrefixCacheFocusBlur covers the F3 invariant
// 那 focus → blur → focus produces correct prefix every time 和 never
// leaks previous focus state out 的 缓存.
func TestAssistantMessageItemRender_PrefixCacheFocusBlur(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := finishedAssistantMessage("m1", "Hello world from the cache test.")
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	const width = 60

	item.SetFocused(true)
	focused1 := item.Render(width)
	focused2 := item.Render(width)
	require.Equal(t, focused1, focused2, "second render must hit the cache and match the first")

	item.SetFocused(false)
	blurred1 := item.Render(width)
	require.NotEqual(t, focused1, blurred1, "blur must produce a different prefixed render than focus")

	item.SetFocused(true)
	focused3 := item.Render(width)
	require.Equal(t, focused1, focused3, "re-focus must produce identical output to the original focused render")
}

// TestAssistantMessageItemRender_PrefixCacheWidthInvalidates asserts 那
// width change does 不 return previous width's cached output.
func TestAssistantMessageItemRender_PrefixCacheWidthInvalidates(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := finishedAssistantMessage("m2", "Some content that wraps differently at different widths so the rendered output diverges.")
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	item.SetFocused(true)

	narrow := item.Render(40)
	wide := item.Render(100)
	require.NotEqual(t, narrow, wide, "different widths must produce different rendered output")

	narrowAgain := item.Render(40)
	require.Equal(t, narrow, narrowAgain, "returning to the original width must hit (or repopulate) the cache with the same output")
}

// TestAssistantMessageItemRender_PrefixCacheHighlightOnTop guarantees 那
// activating highlight range bypasses prefix 缓存 所以 selection drags
// reflect immediately, 和 那 clearing highlight returns 到 cached
// prefixed output unchanged.
func TestAssistantMessageItemRender_PrefixCacheHighlightOnTop(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := finishedAssistantMessage("m3", "Hello world from the highlight test.")
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	item.SetFocused(true)

	const width = 60
	plain := item.Render(width)

	// Activating highlight 必须 change rendered output (selection
	// painted 在 top) without poisoning 缓存 为 un-highlighted
	// state 那 follows.
	item.SetHighlight(0, 0, 0, 5)
	highlighted := item.Render(width)
	require.NotEqual(t, plain, highlighted, "active highlight must change Render output")

	// Clear highlight; cached un-highlighted prefix render 必须
	// 是 returned unchanged.
	item.SetHighlight(-1, -1, -1, -1)
	plainAfter := item.Render(width)
	require.Equal(t, plain, plainAfter, "clearing the highlight must restore the cached prefixed output exactly")
}

// TestUserMessageItemRender_PrefixCacheFocusBlur 是 用户-消息
// counterpart 的 助手 focus/blur 缓存 测试.
func TestUserMessageItemRender_PrefixCacheFocusBlur(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:   "u1",
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: "Hello from the user."},
		},
	}
	r := attachments.NewRenderer(sty.Attachments)
	item := NewUserMessageItem(&sty, msg, r).(*UserMessageItem)

	const width = 60

	item.SetFocused(true)
	focused1 := item.Render(width)
	focused2 := item.Render(width)
	require.Equal(t, focused1, focused2)

	item.SetFocused(false)
	blurred := item.Render(width)
	require.NotEqual(t, focused1, blurred)

	item.SetFocused(true)
	focused3 := item.Render(width)
	require.Equal(t, focused1, focused3)
}

// TestCachedMessageItem_PrefixCacheSemantics covers the constant-prefix
// 路径 used 通过 AssistantInfoItem 和 (width, key) keying used 通过 every
// item, against underlying cachedMessageItem helper directly. 这
// avoids constructing full *配置.配置 用 initialized 提供方
// map just 到 exercise 缓存 plumbing 那 是 identical 为 all callers.
func TestCachedMessageItem_PrefixCacheSemantics(t *testing.T) {
	t.Parallel()

	c := &cachedMessageItem{}

	// Empty 缓存: miss.
	_, ok := c.getCachedPrefixedRender(80, 0)
	require.False(t, ok)

	// Set then hit 在 same (width, key).
	c.setCachedPrefixedRender("hello", 80, 0)
	got, ok := c.getCachedPrefixedRender(80, 0)
	require.True(t, ok)
	require.Equal(t, "hello", got)

	// Different width: miss.
	_, ok = c.getCachedPrefixedRender(120, 0)
	require.False(t, ok)

	// Different key (focused vs blurred): miss.
	_, ok = c.getCachedPrefixedRender(80, 1)
	require.False(t, ok)

	// clearCache drops prefixed 缓存 too.
	c.setCachedRender("raw", 80, 1)
	c.setCachedPrefixedRender("hello", 80, 0)
	c.clearCache()
	_, ok = c.getCachedPrefixedRender(80, 0)
	require.False(t, ok, "clearCache must drop the prefixed render cache")
	_, _, ok = c.getCachedRender(80)
	require.False(t, ok, "clearCache must also drop the raw render cache")
}

// TestAssistantMessageItemRender_PrefixCacheNoCacheLeak guards against a
// regression where 缓存 returned prefixed output 的 previous
// width. We verify 那 cached output 为 width=W contains W-sized
// prefix 和 不 stale wider one 通过 checking 那 line lengths 是
// consistent 在 缓存 hit.
func TestAssistantMessageItemRender_PrefixCacheNoCacheLeak(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := finishedAssistantMessage("m4", strings.Repeat("word ", 40))
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	item.SetFocused(true)

	out80 := item.Render(80)
	out120 := item.Render(120)
	require.NotEqual(t, out80, out120)

	// Hit each cached 条目 again 和 confirm stability.
	require.Equal(t, out80, item.Render(80))
	require.Equal(t, out120, item.Render(120))
}
