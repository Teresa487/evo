package model

import (
	"strconv"
	"testing"

	"github.com/charmbracelet/crush/internal/ui/chat"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// drawTestArea 是 fixed area used 通过 缓存 tests; it must 是 在 least
// 作为 large 作为 测试 items 所以 we can compare every cell.
func drawTestArea(width, height int) uv.Rectangle {
	return uv.Rect(0, 0, width, height)
}

// renderToBuffer mirrors what Chat.Draw does 在 screen layer 所以 tests
// can assert byte-equivalence 用 fresh uv.NewStyledString render.
func renderToBuffer(t *testing.T, c *Chat, w, h int) string {
	t.Helper()
	scr := uv.NewScreenBuffer(w, h)
	c.Draw(scr, drawTestArea(w, h))
	return scr.Render()
}

// TestChatDrawCache_HitOnIdenticalRender asserts 那 two consecutive Draws
// against the same list output reuse the same chatDrawCache rather than
// allocating fresh one. Both wrapper 指针 和 embedded
// RenderBuffer 指针 must 是 stable across second Draw — 那's what
// 证明 no re-decoding happened.
func TestChatDrawCache_HitOnIdenticalRender(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.chat.SetMessages(
		testMessageItem{id: "a", text: "alpha"},
		testMessageItem{id: "b", text: "beta"},
	)
	u.updateLayoutAndSize()

	w, h := 80, 20
	_ = renderToBuffer(t, u.chat, w, h)
	require.NotNil(t, u.chat.drawCache, "first draw should populate cache")
	firstCache := u.chat.drawCache
	firstBuf := u.chat.drawCache.buf.RenderBuffer

	_ = renderToBuffer(t, u.chat, w, h)
	require.Same(t, firstCache, u.chat.drawCache,
		"identical rendered string must reuse the same cache pointer")
	require.Same(t, firstBuf, u.chat.drawCache.buf.RenderBuffer,
		"identical rendered string must reuse the same RenderBuffer pointer")
}

// TestChatDrawCache_MissOnDifferentRender asserts 那 当 list output
// changes, 缓存 是 rebuilt 和 new draw output matches fresh
// uv.NewStyledString render byte-为-byte.
func TestChatDrawCache_MissOnDifferentRender(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.chat.SetMessages(
		testMessageItem{id: "a", text: "alpha"},
	)
	u.updateLayoutAndSize()

	w, h := 80, 20
	_ = renderToBuffer(t, u.chat, w, h)
	require.NotNil(t, u.chat.drawCache)
	firstCache := u.chat.drawCache

	// Replace items 所以 rendered string differs.
	u.chat.SetMessages(
		testMessageItem{id: "c", text: "gamma delta"},
	)
	u.updateLayoutAndSize()

	got := renderToBuffer(t, u.chat, w, h)
	require.NotSame(t, firstCache, u.chat.drawCache,
		"changed rendered string must replace the cache entry")

	// Output must match fresh uv.NewStyledString render 的 current
	// list output 为 same area. 这 是 byte-equivalence guard
	// 那 protects against blit drift 从 StyledString.Draw.
	want := freshStyledRender(u.chat.list.Render(), w, h)
	require.Equal(t, want, got)
}

// TestChatDrawCache_ReusedAcrossDifferentArea asserts 那 cached
// decoded lines do 不 depend 在 screen / area: same string drawn
// into smaller area must hit 缓存, 和 output must match fresh
// uv.NewStyledString render against the new area.
func TestChatDrawCache_ReusedAcrossDifferentArea(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.chat.SetMessages(
		testMessageItem{id: "a", text: "alpha"},
		testMessageItem{id: "b", text: "beta"},
	)
	u.updateLayoutAndSize()

	w, h := 80, 20
	_ = renderToBuffer(t, u.chat, w, h)
	require.NotNil(t, u.chat.drawCache)
	firstCache := u.chat.drawCache
	firstBuf := u.chat.drawCache.buf.RenderBuffer

	// Draw same content into smaller 缓冲. list output 是
	// width-sensitive 在 production (list.Render depends 在 width), 所以 we
	// hold list width fixed via updateLayoutAndSize 和 只 shrink
	// destination area. 缓存 key 是 rendered string + width
	// 方法, both unchanged here.
	smallScr := uv.NewScreenBuffer(40, 10)
	u.chat.Draw(smallScr, uv.Rect(0, 0, 40, 10))

	require.Same(t, firstCache, u.chat.drawCache,
		"smaller area must still hit the cache")
	require.Same(t, firstBuf, u.chat.drawCache.buf.RenderBuffer,
		"cached RenderBuffer must be reused across area changes")

	want := freshStyledRender(u.chat.list.Render(), 40, 10)
	require.Equal(t, want, smallScr.Render())
}

// TestChatDrawCache_BoundedSize asserts draw 缓存 holds 在 most one
// 条目. 这 是 structural — any future change 那 introduces LRU 或
// ring would have 到 更新 这 测试.
func TestChatDrawCache_BoundedSize(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	w, h := 80, 20

	// Cycle through several distinct list outputs 和 confirm 那
	// drawCache 是 still single *chatDrawCache pointing 在 most
	// recent rendered string each time.
	for i := range 5 {
		u.chat.SetMessages(
			testMessageItem{id: "x", text: "tick " + strconv.Itoa(i)},
		)
		u.updateLayoutAndSize()
		_ = renderToBuffer(t, u.chat, w, h)
		require.NotNil(t, u.chat.drawCache)
		require.Equal(t, u.chat.list.Render(), u.chat.drawCache.rendered,
			"cache.rendered must always match the most recent list output")
	}

	// Sanity: type 的 drawCache 是 single 指针, 不 slice/map.
	// 这 是 enforced 在 compile time 但 checking it explicitly here
	// keeps bounded-size invariant visible 到 future readers.
	require.IsType(t, (*chatDrawCache)(nil), u.chat.drawCache)
}

// freshStyledRender 渲染 same string through uv.NewStyledString into
// fresh ScreenBuffer 的 given size. 它's reference implementation
// used 通过 缓存 tests 到 detect blit drift.
func freshStyledRender(s string, w, h int) string {
	scr := uv.NewScreenBuffer(w, h)
	uv.NewStyledString(s).Draw(scr, uv.Rect(0, 0, w, h))
	return scr.Render()
}

// TestChatDrawCache_InvalidatedByWidthMethodSwap asserts 缓存 rebuilds
// 当 destination screen's width method changes between frames.
// GraphemeWidth 和 WcWidth disagree 在 emoji ZWJ sequences 和 other
// modern unicode, 所以 decoded 缓冲 是 只 valid 为 method it
// was decoded 用. Reusing across methods would corrupt cell widths.
func TestChatDrawCache_InvalidatedByWidthMethodSwap(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	// Use content where two methods disagree 所以 any accidental
	// reuse would surface 作为 different rendered cell layout.
	// woman-technologist ZWJ 序号 是 one cell under GraphemeWidth
	// 但 two under WcWidth ( components combine).
	u.chat.SetMessages(
		testMessageItem{id: "a", text: "hi 👩\u200d💻 there"},
	)
	u.updateLayoutAndSize()

	w, h := 80, 5

	// First draw under GraphemeWidth.
	scrA := uv.ScreenBuffer{
		RenderBuffer: uv.NewRenderBuffer(w, h),
		Method:       ansi.GraphemeWidth,
	}
	u.chat.Draw(scrA, uv.Rect(0, 0, w, h))
	require.NotNil(t, u.chat.drawCache)
	require.Equal(t, ansi.GraphemeWidth, u.chat.drawCache.method)
	firstCache := u.chat.drawCache

	// Same string, swap method. 缓存 key includes method, 所以
	// 这 must rebuild even though `rendered` 是 byte-identical.
	scrB := uv.ScreenBuffer{
		RenderBuffer: uv.NewRenderBuffer(w, h),
		Method:       ansi.WcWidth,
	}
	u.chat.Draw(scrB, uv.Rect(0, 0, w, h))
	require.NotSame(t, firstCache, u.chat.drawCache,
		"width method change must invalidate the cache")
	require.Equal(t, ansi.WcWidth, u.chat.drawCache.method)
}

// fixedMethodScreen wraps uv.ScreenBuffer 但 reports custom (non
// ansi.Method) WidthMethod implementation. Chat.Draw 是 expected 到
// detect type-assertion miss 和 fall through 到 uncached 路径.
type fixedMethodScreen struct {
	uv.ScreenBuffer
	method uv.WidthMethod
}

func (s fixedMethodScreen) WidthMethod() uv.WidthMethod { return s.method }

// customWidth 是 WidthMethod whose concrete type 是 intentionally NOT
// ansi.Method, 所以 type assertion 在 Chat.Draw fails. actual
// width math just delegates 到 ansi.GraphemeWidth 所以 rendered
// output 是 well-defined 和 comparable.
type customWidth struct{}

func (customWidth) StringWidth(s string) int {
	return ansi.GraphemeWidth.StringWidth(s)
}

// TestChatDrawCache_FallbackOnNonAnsiMethod asserts 那 当 screen
// reports WidthMethod whose concrete type 是 不 ansi.Method, Chat.Draw
// skips 缓存 (it has no comparable key 为 arbitrary interface)
// 和 falls through 到 direct uv.NewStyledString.Draw, producing
// same output 作为 upstream uncached 路径.
func TestChatDrawCache_FallbackOnNonAnsiMethod(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.chat.SetMessages(
		testMessageItem{id: "a", text: "alpha"},
		testMessageItem{id: "b", text: "beta"},
	)
	u.updateLayoutAndSize()

	w, h := 40, 10
	scr := fixedMethodScreen{
		ScreenBuffer: uv.ScreenBuffer{
			RenderBuffer: uv.NewRenderBuffer(w, h),
			Method:       ansi.GraphemeWidth,
		},
		method: customWidth{},
	}
	// Sanity: wrapper actually returns non-ansi.Method type 所以
	// fallback 路径 是 exercised end-到-end.
	_, isAnsiMethod := scr.WidthMethod().(ansi.Method)
	require.False(t, isAnsiMethod,
		"test setup must hand Draw a non-ansi.Method WidthMethod")

	u.chat.Draw(scr, uv.Rect(0, 0, w, h))

	// fallback 路径 uses uv.NewStyledString(rendered).Draw — same
	// thing freshStyledRender does — 所以 output must match
	// reference render byte-为-byte.
	want := freshStyledRender(u.chat.list.Render(), w, h)
	require.Equal(t, want, scr.Render())

	// And no 缓存 was populated (或 如果 it was, it wasn't used 为
	// 这 draw). Either way: drawing through fallback must never
	// leave stale 缓存 那 future draws would reuse incorrectly.
	require.Nil(t, u.chat.drawCache,
		"fallback path must not populate the cache")
}

// TestRenderedBounds_MatchesPrintStringTallyForZWJ pins the invariant
// 那 renderedBounds (used 到 size 缓存 缓冲) returns same
// width 作为 cell tally StyledString.Draw will 写入 into 缓冲
// using same WidthMethod. We compare computed width 到
// reference render: lay string into oversized 缓冲, then count
// non-empty cells per 行. 一个 divergent-width sample (woman-technologist
// ZWJ 序号) 是 canonical case 那 would expose Bounds()
// vs printString mismatch — under GraphemeWidth 序号 是 one
// cell, under WcWidth it's two — 所以 same string must produce two
// different widths under the two methods, both matching the tally.
func TestRenderedBounds_MatchesPrintStringTallyForZWJ(t *testing.T) {
	t.Parallel()

	const sample = "x 👩\u200d💻 y"

	for _, m := range []ansi.Method{ansi.GraphemeWidth, ansi.WcWidth} {
		w, h := renderedBounds(sample, m)
		// Tally what printString would actually 写入.
		// Use oversized 缓冲 所以 nothing gets clipped.
		scr := uv.ScreenBuffer{
			RenderBuffer: uv.NewRenderBuffer(64, 4),
			Method:       m,
		}
		uv.NewStyledString(sample).Draw(scr, uv.Rect(0, 0, 64, 4))

		// Tally width: rightmost non-empty cell + 它的 width, per 行.
		gotW := 0
		gotH := 0
		for y := 0; y < 4; y++ {
			rowW := 0
			rowHasContent := false
			for x := 0; x < 64; x++ {
				cell := scr.CellAt(x, y)
				if cell == nil || cell.IsZero() ||
					cell.Content == " " && cell.Width == 1 &&
						cell.Style == (uv.Style{}) {
					continue
				}
				rowHasContent = true
				if x+cell.Width > rowW {
					rowW = x + cell.Width
				}
			}
			if rowHasContent {
				gotH = y + 1
				if rowW > gotW {
					gotW = rowW
				}
			}
		}

		require.Equal(t, gotW, w,
			"renderedBounds width must match printString cell tally for method %v", m)
		// Height 是 just '\n' count + 1 — no special-casing needed
		// here, 但 pin it 所以 future regressions 在 multi-line
		// inputs don't sneak 通过.
		require.Equal(t, gotH, h,
			"renderedBounds height must match printString row tally for method %v", m)
	}
}

// Compile-time guard: testMessageItem 是 reused 从 layout_test.go 和
// satisfies chat.MessageItem there. We rely 在 same shape here.
var _ chat.MessageItem = testMessageItem{}
