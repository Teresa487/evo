package list

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// trackedItem 是 测试 helper 那 counts Render calls. body 的
// Render 是 item's content concatenated 用 调用 计数器 所以
// 那 "served 从 缓存" vs "freshly rendered" 是 observable 从
// the rendered string itself.
type trackedItem struct {
	*Versioned
	id         string
	body       string
	renderHits int
}

func newTrackedItem(id, body string) *trackedItem {
	return &trackedItem{
		Versioned: NewVersioned(),
		id:        id,
		body:      body,
	}
}

func (t *trackedItem) Render(width int) string {
	t.renderHits++
	return t.body + ":w=" + strconv.Itoa(width)
}

// TestList_RenderMemo_PointerKey covers F6 invariant 那
// list-level 缓存 是 keyed 通过 item 指针, 不 slice index, 所以
// PrependItems 和 AppendItems do 不 shift cached entries 到
// wrong item.
func TestList_RenderMemo_PointerKey(t *testing.T) {
	t.Parallel()

	a := newTrackedItem("a", "alpha")
	b := newTrackedItem("b", "bravo")
	c := newTrackedItem("c", "charlie")

	l := NewList(a, b, c)
	l.SetSize(40, 10)

	// First render populates 缓存 为 every item.
	first := l.Render()
	require.Equal(t, 1, a.renderHits)
	require.Equal(t, 1, b.renderHits)
	require.Equal(t, 1, c.renderHits)

	// Prepending new item must 不 shift existing entries 到
	// the wrong key. The existing items render exactly once more
	// 只 如果 their 缓存 was lost, which would 是 bug. Scroll 到
	// top 所以 prepended item 是 visible 和 gets rendered.
	z := newTrackedItem("z", "zulu")
	l.PrependItems(z)
	l.ScrollToTop()
	_ = l.Render()
	require.Equal(t, 1, z.renderHits, "prepended item rendered once")
	require.Equal(t, 1, a.renderHits, "stable item must keep its cached entry across PrependItems")
	require.Equal(t, 1, b.renderHits, "stable item must keep its cached entry across PrependItems")
	require.Equal(t, 1, c.renderHits, "stable item must keep its cached entry across PrependItems")

	// AppendItems 是 symmetric.
	d := newTrackedItem("d", "delta")
	l.AppendItems(d)
	_ = l.Render()
	require.Equal(t, 1, d.renderHits, "appended item rendered once")
	require.Equal(t, 1, a.renderHits)
	require.Equal(t, 1, b.renderHits)
	require.Equal(t, 1, c.renderHits)

	// output 是 non-trivial.
	require.Contains(t, first, "alpha")
}

// TestList_SetSize_WidthChangeInvalidates covers the F6 invariant
// 那 width change drops every cached 条目 但 height-只
// change leaves 缓存 intact.
func TestList_SetSize_WidthChangeInvalidates(t *testing.T) {
	t.Parallel()

	a := newTrackedItem("a", "alpha")
	b := newTrackedItem("b", "bravo")

	l := NewList(a, b)
	l.SetSize(40, 10)
	_ = l.Render()
	require.Equal(t, 1, a.renderHits)
	require.Equal(t, 1, b.renderHits)

	// Height-只 change: no invalidation.
	l.SetSize(40, 20)
	_ = l.Render()
	require.Equal(t, 1, a.renderHits, "height-only change must keep cache entries")
	require.Equal(t, 1, b.renderHits, "height-only change must keep cache entries")

	// Width change: every 条目 invalidates.
	l.SetSize(80, 20)
	_ = l.Render()
	require.Equal(t, 2, a.renderHits, "width change must invalidate cache entries")
	require.Equal(t, 2, b.renderHits, "width change must invalidate cache entries")
}

// TestList_RemoveItem_DropsEntry covers F6 invariant 那
// RemoveItem drops 缓存 条目 为 removed item 但 leaves
// surviving entries 在 place.
func TestList_RemoveItem_DropsEntry(t *testing.T) {
	t.Parallel()

	a := newTrackedItem("a", "alpha")
	b := newTrackedItem("b", "bravo")
	c := newTrackedItem("c", "charlie")

	l := NewList(a, b, c)
	l.SetSize(40, 10)
	_ = l.Render()
	require.Equal(t, 1, a.renderHits)
	require.Equal(t, 1, b.renderHits)
	require.Equal(t, 1, c.renderHits)

	l.RemoveItem(1) // remove b
	_ = l.Render()
	// 和 c still cached.
	require.Equal(t, 1, a.renderHits, "stable item must keep cached entry across RemoveItem")
	require.Equal(t, 1, c.renderHits, "stable item must keep cached entry across RemoveItem")
	// removed item's 条目 是 dropped — verify 通过 re-adding b
	// 和 confirming it renders 作为 如果 fresh.
	l.AppendItems(b)
	_ = l.Render()
	require.Equal(t, 2, b.renderHits, "re-added item must re-render")
}

// TestList_Cache_NotReRendered pins 那 cached item 是 never
// re-rendered until width change 或 version bump.
func TestList_Cache_NotReRendered(t *testing.T) {
	t.Parallel()

	a := newTrackedItem("a", "alpha")
	b := newTrackedItem("b", "bravo")

	l := NewList(a, b)
	l.SetSize(40, 10)
	_ = l.Render()
	require.Equal(t, 1, a.renderHits, "items render exactly once on first draw")
	require.Equal(t, 1, b.renderHits, "items render exactly once on first draw")

	// Many subsequent renders must 不 re-render cached items.
	for range 5 {
		_ = l.Render()
	}
	require.Equal(t, 1, a.renderHits, "cached items must not re-render across redraws")
	require.Equal(t, 1, b.renderHits, "cached items must not re-render across redraws")
}

// TestList_Cache_VersionBumpInvalidates pins 那 version bump
// re-renders item, 所以 no stale output 是 served.
func TestList_Cache_VersionBumpInvalidates(t *testing.T) {
	t.Parallel()

	a := newTrackedItem("a", "alpha")
	l := NewList(a)
	l.SetSize(40, 10)

	_ = l.Render()
	_ = l.Render()
	require.Equal(t, 1, a.renderHits)

	a.Bump()
	_ = l.Render()
	require.Equal(t, 2, a.renderHits, "version bump must invalidate the entry")

	// Re-renders without bumping go back 到 缓存 hits.
	_ = l.Render()
	_ = l.Render()
	require.Equal(t, 2, a.renderHits, "post-bump render is cached again")
}

// TestList_Cache_ResizeInvalidates pins 那 width change
// invalidates cached entries.
func TestList_Cache_ResizeInvalidates(t *testing.T) {
	t.Parallel()

	a := newTrackedItem("a", "alpha")
	l := NewList(a)
	l.SetSize(40, 10)

	_ = l.Render()
	require.Equal(t, 1, a.renderHits)

	l.SetSize(80, 10)
	_ = l.Render()
	require.Equal(t, 2, a.renderHits, "width change must invalidate the entry")
}

// TestList_Cache_SelectionDrag pins 那 beginning 和 ending
// selection drag each re-render items inside 它的 range once.
func TestList_Cache_SelectionDrag(t *testing.T) {
	t.Parallel()

	a := newTrackedItem("a", "alpha")
	b := newTrackedItem("b", "bravo")
	c := newTrackedItem("c", "charlie")

	l := NewList(a, b, c)
	l.SetSize(40, 10)
	_ = l.Render()
	require.Equal(t, 1, a.renderHits)
	require.Equal(t, 1, b.renderHits)
	require.Equal(t, 1, c.renderHits)

	// Begin a selection drag spanning items 0..1. Items inside the
	// range re-render exactly once.
	l.BeginSelectionDrag(0, 1)
	_ = l.Render()
	require.Equal(t, 2, a.renderHits, "drag-spanned item must re-render once on entering the drag")
	require.Equal(t, 2, b.renderHits, "drag-spanned item must re-render once on entering the drag")
	require.Equal(t, 1, c.renderHits, "out-of-range item stays cached")

	// While drag 是 活跃, renders without state changes 是
	// 缓存 hits.
	_ = l.Render()
	require.Equal(t, 2, a.renderHits, "version-stable item hits the cache")
	require.Equal(t, 2, b.renderHits, "version-stable item hits the cache")

	// End the drag. Items inside the range re-render once.
	l.EndSelectionDrag()
	_ = l.Render()
	require.Equal(t, 3, a.renderHits, "ending the drag re-renders the entry")
	require.Equal(t, 3, b.renderHits, "ending the drag re-renders the entry")

	// Subsequent renders 是 缓存 hits again.
	for range 3 {
		_ = l.Render()
	}
	require.Equal(t, 3, a.renderHits, "cached after drag end")
	require.Equal(t, 3, b.renderHits, "cached after drag end")
}

// TestList_RenderOutputStableAcrossDraws 是 F6 byte-equality
// invariant: rendering same list multiple times 必须 produce
// same bytes.
func TestList_RenderOutputStableAcrossDraws(t *testing.T) {
	t.Parallel()

	items := make([]Item, 0, 5)
	for i := range 5 {
		items = append(items, newTrackedItem(strconv.Itoa(i), "item-"+strconv.Itoa(i)))
	}
	l := NewList(items...)
	l.SetSize(40, 20)

	first := l.Render()
	for range 4 {
		require.Equal(t, first, l.Render(), "render output must be byte-stable across draws")
	}
	// And output 是 non-trivial.
	require.True(t, strings.Contains(first, "item-0"))
}

// TestList_SetItems_PointerOverlapRetainsCache covers F6 §4.5
// invalidation semantics 为 SetItems. When new slice shares
// some pointers 用 previous slice ( typical "swap few
// items, keep rest" scenario), 缓存 entries 为
// surviving items must 是 retained — re-rendering them would defeat
// memo. Entries 为 items 那 were removed must 是
// dropped 所以 they can't serve stale output 如果 same 指针 是
// re-introduced later.
func TestList_SetItems_PointerOverlapRetainsCache(t *testing.T) {
	t.Parallel()

	a := newTrackedItem("a", "alpha")
	b := newTrackedItem("b", "bravo")
	c := newTrackedItem("c", "charlie")
	d := newTrackedItem("d", "delta")

	l := NewList(a, b, c)
	l.SetSize(40, 10)
	_ = l.Render()
	require.Equal(t, 1, a.renderHits)
	require.Equal(t, 1, b.renderHits)
	require.Equal(t, 1, c.renderHits)

	// Replace slice 用 one 那 shares 和 c (b 是 dropped,
	// d 是 added). 和 c must keep their 缓存 entries; d renders
	// once 在 next draw.
	l.SetItems(a, c, d)
	_ = l.Render()
	require.Equal(t, 1, a.renderHits, "stable item must keep cached entry across SetItems")
	require.Equal(t, 1, c.renderHits, "stable item must keep cached entry across SetItems")
	require.Equal(t, 1, d.renderHits, "new item renders once")

	// Re-introducing b after it was dropped must rebuild 它的
	// 条目 (它的 previous 缓存 条目 was invalidated 通过 SetItems).
	l.SetItems(a, b, c)
	_ = l.Render()
	require.Equal(t, 2, b.renderHits, "re-introduced item must re-render — its old entry was dropped")
	// 和 c remained throughout both swaps.
	require.Equal(t, 1, a.renderHits, "stable item retained across multiple SetItems")
	require.Equal(t, 1, c.renderHits, "stable item retained across multiple SetItems")
}

// TestList_SetItems_AllNewDropsEveryEntry covers F6 §4.5: 当
// SetItems slice has no 指针 overlap 用 previous slice,
// every 缓存 条目 从 previous slice 是 dropped. 这 是
// pure-replace case (e.g. 会话 switch).
func TestList_SetItems_AllNewDropsEveryEntry(t *testing.T) {
	t.Parallel()

	a := newTrackedItem("a", "alpha")
	b := newTrackedItem("b", "bravo")
	c := newTrackedItem("c", "charlie")

	l := NewList(a, b, c)
	l.SetSize(40, 10)
	_ = l.Render()
	require.Equal(t, 1, a.renderHits)
	require.Equal(t, 1, b.renderHits)
	require.Equal(t, 1, c.renderHits)

	// Replace 用 fully disjoint slice. Every 条目 从
	// previous slice must 是 dropped.
	x := newTrackedItem("x", "xray")
	y := newTrackedItem("y", "yankee")
	l.SetItems(x, y)
	_ = l.Render()
	require.Equal(t, 1, x.renderHits, "new item renders once")
	require.Equal(t, 1, y.renderHits, "new item renders once")

	// Re-introducing originals must rebuild every 条目.
	l.SetItems(a, b, c)
	_ = l.Render()
	require.Equal(t, 2, a.renderHits, "previously-dropped item must re-render")
	require.Equal(t, 2, b.renderHits, "previously-dropped item must re-render")
	require.Equal(t, 2, c.renderHits, "previously-dropped item must re-render")
}

// TestVersioned_BumpMonotonic covers the basic Versioned contract:
// Version() starts 在 zero 和 Bump() advances it monotonically.
func TestVersioned_BumpMonotonic(t *testing.T) {
	t.Parallel()

	v := NewVersioned()
	require.Equal(t, uint64(0), v.Version())
	v.Bump()
	require.Equal(t, uint64(1), v.Version())
	v.Bump()
	v.Bump()
	require.Equal(t, uint64(3), v.Version())
}

// multiLineItem 是 测试 helper whose Render returns fixed
// multi-line body. Each line 是 uniquely identifiable (id:N) 所以
// 测试 can reconstruct expected visible 窗口 通过 index. F7's
// byte-identity matrix 是 built around these.
type multiLineItem struct {
	*Versioned
	id     string
	height int
}

func newMultiLineItem(id string, height int) *multiLineItem {
	return &multiLineItem{
		Versioned: NewVersioned(),
		id:        id,
		height:    height,
	}
}

func (m *multiLineItem) Render(_ int) string {
	if m.height <= 0 {
		return ""
	}
	parts := make([]string, m.height)
	for i := range m.height {
		parts[i] = m.id + ":" + strconv.Itoa(i)
	}
	return strings.Join(parts, "\n")
}

// expectedRender computes what list.Render *should* produce 从
// first principles given the item heights, viewport, offsetIdx,
// offsetLine, gap, 和 reverse settings. 它 mirrors pre-F7
// "build everything, trim 到 height, reverse" semantics 所以 we can
// assert byte-identity against new bounded 路径.
func expectedRender(items []*multiLineItem, height, offsetIdx, offsetLine, gap int, reverse bool) string {
	if len(items) == 0 {
		return ""
	}
	budget := max(height, 0)
	var lines []string
	currentOffset := offsetLine
	for idx := offsetIdx; idx < len(items) && len(lines) < budget; idx++ {
		body := items[idx].Render(0)
		body = strings.TrimRight(body, "\n")
		itemLines := strings.Split(body, "\n")
		itemHeight := len(itemLines)

		if currentOffset >= 0 && currentOffset < itemHeight {
			lines = append(lines, itemLines[currentOffset:]...)
			for range gap {
				lines = append(lines, "")
			}
		} else {
			gapOffset := currentOffset - itemHeight
			gapRemaining := gap - gapOffset
			for range max(gapRemaining, 0) {
				lines = append(lines, "")
			}
		}
		currentOffset = 0
	}
	if len(lines) > budget {
		lines = lines[:budget]
	}
	if reverse {
		for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
			lines[i], lines[j] = lines[j], lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// TestList_F7_ByteIdentityMatrix 是 T1 从 F7 plan: sweep
// over (item heights × viewport heights × offsets × gaps × reverse)
// 那 asserts list.Render produces output byte-identical 到
// pre-F7-equivalent reference (build full 缓冲, trim 在 end).
func TestList_F7_ByteIdentityMatrix(t *testing.T) {
	t.Parallel()

	itemHeights := [][]int{
		{1},
		{5},
		{1, 1, 1},
		{3, 7, 2},
		{20, 5, 30},
		{50, 1, 50, 1},
	}
	viewportHeights := []int{1, 3, 5, 10, 25, 100}
	offsetIdxs := []int{0, 1, 2}
	offsetLines := []int{0, 1, 4}
	gaps := []int{0, 1, 3}
	reverses := []bool{false, true}

	for _, heights := range itemHeights {
		for _, vh := range viewportHeights {
			for _, oIdx := range offsetIdxs {
				if oIdx >= len(heights) {
					continue
				}
				for _, oLine := range offsetLines {
					maxOffset := heights[oIdx]
					if oLine >= maxOffset {
						continue
					}
					for _, gap := range gaps {
						for _, reverse := range reverses {
							items := make([]*multiLineItem, len(heights))
							asItems := make([]Item, len(heights))
							for i, h := range heights {
								items[i] = newMultiLineItem("i"+strconv.Itoa(i), h)
								asItems[i] = items[i]
							}
							l := NewList(asItems...)
							l.SetSize(40, vh)
							l.SetGap(gap)
							l.SetReverse(reverse)
							l.offsetIdx = oIdx
							l.offsetLine = oLine

							got := l.Render()
							want := expectedRender(items, vh, oIdx, oLine, gap, reverse)
							if n := strings.Count(want, "\n") + 1; n < vh && (oIdx > 0 || oLine > 0) {
								// offset 是 past end, 所以 Render
								// clamps it. output 必须 still match
								// reference 在 clamped offset,
								// 和 是 full unless 那 是 top.
								cIdx, cLine := l.ScrollPosition()
								want = expectedRender(items, vh, cIdx, cLine, gap, reverse)
								if cIdx > 0 || cLine > 0 {
									require.Equalf(t, vh, strings.Count(got, "\n")+1,
										"clamped render underfilled heights=%v vh=%d oIdx=%d oLine=%d gap=%d",
										heights, vh, oIdx, oLine, gap)
								}
							}
							require.Equalf(t, want, got,
								"mismatch heights=%v vh=%d oIdx=%d oLine=%d gap=%d reverse=%v",
								heights, vh, oIdx, oLine, gap, reverse)
						}
					}
				}
			}
		}
	}
}

// TestList_F7_GiantItemBoundedRender 是 T2: single 10,000-line
// item 用 50-line viewport. Render must return exactly 50
// lines — no off-通过-one, no trim issue. 这 是 F7 win 在
// 测试 form: per-frame work 是 bounded 通过 viewport, 不 item
// height.
func TestList_F7_GiantItemBoundedRender(t *testing.T) {
	t.Parallel()

	const itemHeight = 10000
	const viewport = 50

	giant := newMultiLineItem("giant", itemHeight)
	l := NewList(giant)
	l.SetSize(40, viewport)

	out := l.Render()
	got := strings.Count(out, "\n") + 1
	require.Equal(t, viewport, got, "render output must be exactly viewport lines for an oversized item")

	// And lines 是 prefix 的 item starting 在
	// offset 0.
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		require.Equal(t, "giant:"+strconv.Itoa(i), line, "line %d does not match expected slice", i)
	}
}

// TestList_F7_GiantItemWithOffsetBoundedRender complements T2 用
// non-zero offsetLine 所以 we exercise both "skip prefix" 和
// "bound suffix" sides 的 slice.
func TestList_F7_GiantItemWithOffsetBoundedRender(t *testing.T) {
	t.Parallel()

	const itemHeight = 10000
	const viewport = 50
	const offset = 1234

	giant := newMultiLineItem("giant", itemHeight)
	l := NewList(giant)
	l.SetSize(40, viewport)
	l.offsetLine = offset

	out := l.Render()
	lines := strings.Split(out, "\n")
	require.Len(t, lines, viewport, "render output must be exactly viewport lines for an oversized item")
	for i, line := range lines {
		require.Equal(t, "giant:"+strconv.Itoa(offset+i), line, "line %d does not match expected slice", i)
	}
}

// TestList_F7_GapOverflow 是 T3: viewport height 5, two items each
// 10 lines, gap 的 3. Render returns exactly 5 lines 和 never
// includes gap rows beyond the viewport.
func TestList_F7_GapOverflow(t *testing.T) {
	t.Parallel()

	a := newMultiLineItem("a", 10)
	b := newMultiLineItem("b", 10)
	l := NewList(a, b)
	l.SetSize(40, 5)
	l.SetGap(3)

	out := l.Render()
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 5, "viewport must clamp output to height even with gap rows pending")

	// Gap rows after item would 只 appear 如果 viewport
	// extended past the first 10 lines, which it doesn't here.
	for i, line := range lines {
		require.Equal(t, "a:"+strconv.Itoa(i), line, "line %d", i)
	}
}

// TestList_F7_GapOverflow_BoundaryStraddle exercises a viewport
// 那 lands inside gap region between two items: 12 lines
// viewport, item a height 10, item b height 10, gap 3 — first 10
// lines 从 , then 2 的 3 gap rows, no b lines yet.
func TestList_F7_GapOverflow_BoundaryStraddle(t *testing.T) {
	t.Parallel()

	a := newMultiLineItem("a", 10)
	b := newMultiLineItem("b", 10)
	l := NewList(a, b)
	l.SetSize(40, 12)
	l.SetGap(3)

	out := l.Render()
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 12)

	for i := range 10 {
		require.Equal(t, "a:"+strconv.Itoa(i), lines[i])
	}
	require.Equal(t, "", lines[10])
	require.Equal(t, "", lines[11])
}

// TestList_F7_ReverseGiantItem 是 T4: same bounded-slicing
// invariant 在 reverse mode. Reverse mode keeps same final
// trim semantics; bounded slicing must produce same 窗口
// (just reversed).
func TestList_F7_ReverseGiantItem(t *testing.T) {
	t.Parallel()

	const itemHeight = 10000
	const viewport = 50

	giant := newMultiLineItem("giant", itemHeight)
	l := NewList(giant)
	l.SetSize(40, viewport)
	l.SetReverse(true)

	out := l.Render()
	lines := strings.Split(out, "\n")
	require.Len(t, lines, viewport)

	// Expected: same first-50 slice 作为 non-reverse 路径
	// 但 reversed.
	for i, line := range lines {
		expectedIdx := viewport - 1 - i
		require.Equal(t, "giant:"+strconv.Itoa(expectedIdx), line, "reverse line %d", i)
	}
}

// TestList_F7_OffsetLineAtItemBoundary 是 T5: offsetLine ==
// itemHeight lands exactly past last visible line 的 item
// 在 offsetIdx. renderer must 不 address line N (which does
// 不 exist); visible 窗口 starts 在 gap rows (当 gap
// > 0) 或 在 next item (当 gap == 0).
func TestList_F7_OffsetLineAtItemBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		gap      int
		viewport int
		want     []string
	}{
		{
			name:     "gap zero jumps straight to next item",
			gap:      0,
			viewport: 5,
			want: []string{
				"b:0", "b:1", "b:2", "b:3", "b:4",
			},
		},
		{
			name:     "gap two emits gap rows then next item",
			gap:      2,
			viewport: 5,
			want: []string{
				"", "", "b:0", "b:1", "b:2",
			},
		},
	}

	const itemHeight = 4
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a := newMultiLineItem("a", itemHeight)
			b := newMultiLineItem("b", 10)
			l := NewList(a, b)
			l.SetSize(40, tc.viewport)
			l.SetGap(tc.gap)
			l.offsetIdx = 0
			l.offsetLine = itemHeight // exactly at the boundary

			out := l.Render()
			require.Equal(t, strings.Join(tc.want, "\n"), out)
		})
	}
}

// TestList_F7_OffsetLineInsideGap 是 T6: offsetLine 是 one 行
// past end 的 item 在 offsetIdx, landing inside gap
// region. visible 窗口 starts 在 second gap 行 和
// then continues into the next item.
func TestList_F7_OffsetLineInsideGap(t *testing.T) {
	t.Parallel()

	const itemHeight = 4
	const gap = 3
	const viewport = 5

	a := newMultiLineItem("a", itemHeight)
	b := newMultiLineItem("b", 10)
	l := NewList(a, b)
	l.SetSize(40, viewport)
	l.SetGap(gap)
	l.offsetIdx = 0
	l.offsetLine = itemHeight + 1 // one row into the gap

	out := l.Render()
	want := strings.Join([]string{
		"",    // second gap row
		"",    // third gap row
		"b:0", // next item starts
		"b:1",
		"b:2",
	}, "\n")
	require.Equal(t, want, out)
}

// TestList_F7_ViewportZeroOrNegative 是 T7: non-positive
// viewport height must produce empty string 用 no panic 和
// must normalize l.height 到 zero ( budget := max(l.height, 0)
// side effect).
func TestList_F7_ViewportZeroOrNegative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		height int
	}{
		{name: "height zero", height: 0},
		{name: "height negative", height: -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a := newMultiLineItem("a", 5)
			l := NewList(a)
			l.SetSize(40, tc.height)

			out := l.Render()
			require.Equal(t, "", out, "render must be empty for non-positive viewport")
			require.Equal(t, 0, l.height, "render must normalize height to zero")
		})
	}
}

// TestList_Prewarm covers incremental 缓存 warming: Prewarm renders
// batch 的 items into width 缓存 所以 later TotalHeight 是 free, 和
// re-setting same width afterward must 不 drop 那 warmed 缓存.
func TestList_Prewarm(t *testing.T) {
	t.Parallel()

	items := make([]Item, 6)
	tracked := make([]*trackedItem, 6)
	for i := range items {
		it := newTrackedItem(strconv.Itoa(i), "body")
		tracked[i] = it
		items[i] = it
	}
	l := NewList(items...)
	l.SetSize(40, 3)

	// Warm first three items; 只 those render.
	next := l.Prewarm(0, 3)
	require.Equal(t, 3, next)
	for i := 0; i < 3; i++ {
		require.Equal(t, 1, tracked[i].renderHits, "warmed item %d", i)
	}
	for i := 3; i < 6; i++ {
		require.Equal(t, 0, tracked[i].renderHits, "unwarmed item %d", i)
	}

	// Warm rest, then TotalHeight must 不 render anything again.
	l.Prewarm(next, 3)
	before := totalRenderHits(tracked)
	_ = l.TotalHeight()
	require.Equal(t, before, totalRenderHits(tracked), "TotalHeight re-rendered after full warm")

	// Re-setting same width 是 no-op 和 must preserve 缓存.
	l.SetSize(40, 3)
	_ = l.TotalHeight()
	require.Equal(t, before, totalRenderHits(tracked), "same-width SetSize dropped the warmed cache")
}

// TestList_Overflows covers the cheap bounded overflow check: it stops
// early once viewport 是 exceeded 和 reports overflow correctly.
func TestList_Overflows(t *testing.T) {
	t.Parallel()

	items := make([]Item, 10)
	for i := range items {
		items[i] = newTrackedItem(strconv.Itoa(i), "body") // 1 line each
	}
	l := NewList(items...)
	l.SetSize(40, 4)

	require.True(t, l.Overflows(4), "10 one-line items overflow a height of 4")
	require.False(t, l.Overflows(20), "10 one-line items fit within a height of 20")
}

func totalRenderHits(items []*trackedItem) int {
	n := 0
	for _, it := range items {
		n += it.renderHits
	}
	return n
}

// tallItem renders fixed number 的 lines, 所以 tests can build lists
// whose first visible item 是 taller than viewport.
type tallItem struct {
	*Versioned
	lines int
}

func newTallItem(lines int) *tallItem {
	return &tallItem{Versioned: NewVersioned(), lines: lines}
}

func (t *tallItem) Render(width int) string {
	return strings.TrimSuffix(strings.Repeat("x\n", t.lines), "\n")
}

// TestList_AtBottom_TallFirstVisibleItem covers the case where the item
// 在 offsetIdx 是 taller than viewport: accumulated height passes
// viewport height long before end 的 list, 但 lines
// scrolled out 的 view (offsetLine) make up difference. AtBottom must
// account 为 offsetLine before bailing out early.
func TestList_AtBottom_TallFirstVisibleItem(t *testing.T) {
	t.Parallel()

	for _, gap := range []int{0, 1} {
		l := NewList(newTallItem(50), newTallItem(3))
		l.SetGap(gap)
		l.SetSize(40, 10)

		l.ScrollToBottom()
		require.True(t, l.AtBottom(), "gap=%d: at bottom after ScrollToBottom", gap)

		l.ScrollBy(-1)
		require.False(t, l.AtBottom(), "gap=%d: not at bottom after scrolling up", gap)

		l.ScrollBy(1)
		require.True(t, l.AtBottom(), "gap=%d: back at bottom after scrolling down", gap)
	}
}

// fiveByFour builds six four-line items 用 one-line gap 在
// eight-line viewport, 所以 tail can 是 scrolled past 和 shrunk.
func fiveByFour() (*List, []*multiLineItem) {
	items := make([]*multiLineItem, 0, 6)
	listItems := make([]Item, 0, 6)
	for i := range 6 {
		it := newMultiLineItem(string(rune('a'+i)), 4)
		items = append(items, it)
		listItems = append(listItems, it)
	}
	l := NewList(listItems...)
	l.SetGap(1)
	l.SetSize(20, 8)
	return l, items
}

// 一个 tail 那 shrinks while view 是 scrolled near end must 不
// leave view parked past last line 用 blank rows below it.
func TestList_Render_ClampsWhenTailShrinks(t *testing.T) {
	t.Parallel()
	l, items := fiveByFour()
	l.ScrollToBottom()
	l.ScrollBy(-1)

	items[5].height = 1
	items[5].Bump()
	got := strings.Split(l.Render(), "\n")

	require.Len(t, got, 8, "viewport must stay full")
	require.Equal(t, "f:0", got[len(got)-1], "last line must be the end of the content")
	require.True(t, l.AtBottom())
}

// Removing item view starts inside must show next item 从
// 它的 top, 不 skip 作为 many 的 它的 lines 作为 removed item had scrolled.
func TestList_RemoveItem_TopItemResetsLineOffset(t *testing.T) {
	t.Parallel()
	l, items := fiveByFour()
	items[2].height = 1
	l.offsetIdx, l.offsetLine = 1, 3

	l.RemoveItem(1)
	got := strings.Split(l.Render(), "\n")

	require.Equal(t, "c:0", got[0])
}

// Shrinking item set below scroll offset (作为 filter does) must
// show end 的 what remains rather than one item 在 top.
func TestList_SetItems_FewerItemsStaysFull(t *testing.T) {
	t.Parallel()
	l, items := fiveByFour()
	l.ScrollToBottom()

	l.SetItems(items[0], items[1], items[2])
	got := strings.Split(l.Render(), "\n")

	require.Len(t, got, 8)
	require.Equal(t, "c:3", got[len(got)-1])
}

// TestList_Restyle_KeepsHeights covers theme-swap 路径: scroll geometry
// 是 served 从 cached heights, 只 drawn items re-render, 和
// item 那 changed before restyle still re-renders 为 它的 height.
func TestList_Restyle_KeepsHeights(t *testing.T) {
	t.Parallel()

	items := make([]*trackedItem, 0, 20)
	listItems := make([]Item, 0, 20)
	for idx := range 20 {
		item := newTrackedItem(strconv.Itoa(idx), "item "+strconv.Itoa(idx))
		items = append(items, item)
		listItems = append(listItems, item)
	}
	l := NewList(listItems...)
	l.SetSize(40, 3)
	l.ScrollToIndex(17)
	_ = l.Render()
	total := l.TotalHeight()
	offset := l.Offset()
	before := totalRenderHits(items)

	// An off-screen item 那 mutated before restyle has no current
	// height 到 keep.
	items[0].Bump()

	restyled := 0
	l.Restyle(func(item Item) {
		restyled++
		item.(*trackedItem).Bump()
	})
	require.Equal(t, len(items), restyled)

	require.Equal(t, total, l.TotalHeight())
	require.Equal(t, offset, l.Offset())
	require.Equal(t, before+1, totalRenderHits(items), "only the mutated item re-renders for its height")

	_ = l.Render()
	require.Equal(t, before+4, totalRenderHits(items), "visible items re-render their content")
	require.Equal(t, 2, items[19].renderHits)
	require.Equal(t, 1, items[10].renderHits, "off-screen items keep their cached render")
}
