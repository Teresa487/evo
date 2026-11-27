package list

import (
	"strings"
)

// List represents list 的 items 那 can 是 lazily rendered. 一个 list 是
// always rendered like chat conversation where items 是 stacked vertically
// 从 top 到 bottom.
type List struct {
	// Viewport size
	width, height int

	// Items 在 list
	items []Item

	// Gap between items (0 或 less means no gap)
	gap int

	// show list 在 reverse order
	reverse bool

	// Focus 和 selection state
	focused     bool
	selectedIdx int // The current selected index -1 means no selection

	// offsetIdx 是 index 的 first visible item 在 viewport.
	offsetIdx int
	// offsetLine 是 number 的 lines 的 item 在 offsetIdx 那 是
	// scrolled out 的 view (above viewport).
	// 它 must always 是 >= 0.
	offsetLine int

	// renderCallbacks 是 list 的 callbacks 到 apply 当 rendering items.
	renderCallbacks []func(idx, selectedIdx int, item Item) Item

	// totalHeightCache 是 cached 值 的 total rendered height 的
	// all items. 它 是 invalidated whenever item set changes 或
	// viewport width changes (which 可以 alter per-item line counts).
	totalHeightCache int
	totalHeightValid bool

	// 缓存 是 F6 list-level render memo, keyed 通过 item 指针 和
	// valid while item's width 和 version match.
	cache map[Item]*listCacheEntry

	// dragItems 是 items inside 活跃 selection drag, whose
	// entries 是 dropped 当 drag begins 和 again 当 it ends.
	dragItems map[Item]struct{}
}

// listCacheEntry 是 per-item 条目 在 list-level render memo.
type listCacheEntry struct {
	width   int
	version uint64
	height  int
	lines   []string
	// stale entries keep exact height 但 must re-render 到 是 drawn.
	stale bool
}

// NewList creates a new lazy-loaded list.
func NewList(items ...Item) *List {
	l := new(List)
	l.items = items
	l.selectedIdx = -1
	l.cache = make(map[Item]*listCacheEntry)
	l.dragItems = make(map[Item]struct{})
	return l
}

// RenderCallback defines function 那 can modify item before it 是
// rendered.
type RenderCallback func(idx, selectedIdx int, item Item) Item

// RegisterRenderCallback registers callback 到 是 called 当 rendering
// items. 这 can 是 used 到 modify items before they 是 rendered.
func (l *List) RegisterRenderCallback(cb RenderCallback) {
	l.renderCallbacks = append(l.renderCallbacks, cb)
}

// SetSize sets size 的 list viewport. 一个 width change drops
// entire render 缓存 because every 条目's wrapped output depends 在
// width; height-只 change 是 no-op 为 缓存.
func (l *List) SetSize(width, height int) {
	if l.width != width {
		l.invalidateAll()
	}
	l.width = width
	l.height = height
}

// SetGap sets the gap between items.
func (l *List) SetGap(gap int) {
	l.gap = gap
}

// Gap returns the gap between items.
func (l *List) Gap() int {
	return l.gap
}

// AtBottom returns whether list 是 showing last item 在 bottom.
func (l *List) AtBottom() bool {
	if len(l.items) == 0 {
		return true
	}

	// Calculate height 从 offsetIdx 到 end. comparison 是
	// against visible height (totalHeight minus lines 的 first
	// item 那 是 scrolled out 的 view), otherwise first item taller
	// than viewport reports "不 在 bottom" while it 是 在 fact
	// pinned there.
	var totalHeight int
	for idx := l.offsetIdx; idx < len(l.items); idx++ {
		if totalHeight-l.offsetLine > l.height {
			// No need 到 calculate further, we're already past viewport height
			return false
		}
		itemHeight := l.itemHeight(idx)
		if l.gap > 0 && idx > l.offsetIdx {
			itemHeight += l.gap
		}
		totalHeight += itemHeight
	}

	return totalHeight-l.offsetLine <= l.height
}

// SetReverse shows list 在 reverse order.
func (l *List) SetReverse(reverse bool) {
	l.reverse = reverse
}

// Width returns width 的 list viewport.
func (l *List) Width() int {
	return l.width
}

// Height returns height 的 list viewport.
func (l *List) Height() int {
	return l.height
}

// Len returns number 的 items 在 list.
func (l *List) Len() int {
	return len(l.items)
}

// TotalHeight returns total height 的 all items 在 list.
// 结果 是 cached 和 只 recomputed 当 item set 或
// viewport width changes.
func (l *List) TotalHeight() int {
	if l.totalHeightValid {
		return l.totalHeightCache
	}
	total := 0
	for idx := range l.items {
		total += l.itemHeight(idx)
		if l.gap > 0 && idx < len(l.items)-1 {
			total += l.gap
		}
	}
	l.totalHeightCache = total
	l.totalHeightValid = true
	return total
}

// Prewarm renders items 在 range [从, 从+batch) into width
// 缓存 和 returns next index 到 warm (len(items) 当 done). 它 lets
// caller populate per-item render 缓存 incrementally across frames
// 所以 later TotalHeight 是 instant instead 的 rendering everything 在
// once. Rendering 是 otherwise identical 到 what TotalHeight would do.
func (l *List) Prewarm(from, batch int) int {
	if from < 0 {
		from = 0
	}
	end := min(from+batch, len(l.items))
	for idx := from; idx < end; idx++ {
		l.itemHeight(idx)
	}
	return end
}

// Overflows reports whether the items' total height exceeds the given
// viewport height. 它 walks 从 bottom 和 stops 作为 soon 作为
// threshold 是 crossed, 所以 为 content taller than viewport (
// common case) it renders 只 viewport's worth 的 items rather than
// all 的 them — much cheaper than TotalHeight 当 只 boolean 是
// needed (e.g. deciding whether scrollbar 是 required).
func (l *List) Overflows(height int) bool {
	total := 0
	for idx := len(l.items) - 1; idx >= 0; idx-- {
		total += l.itemHeight(idx)
		if l.gap > 0 && idx < len(l.items)-1 {
			total += l.gap
		}
		if total > height {
			return true
		}
	}
	return false
}

// ItemsVersion folds every item's version into one number. 它 changes
// whenever any item 在 这 list mutates 在 way 那 affects 它的 rendered
// output, since 那 是 exactly contract [Versioned.Bump] carries.
//
// 它 reads one 字段 per item 和 renders nothing, 所以 it 是 cheap enough 到
// 调用 once per frame. Callers 那 memoize whole rendered frame fold it
// into their 缓存 key 到 pick up item mutations they do 不 otherwise know
// about.
func (l *List) ItemsVersion() uint64 {
	var v uint64
	for _, item := range l.items {
		v = v*31 + item.Version()
	}
	return v
}

// ScrollPosition returns index 的 first visible item 和 line
// offset into it. Unlike Offset it 是 O(1) 和 does 不 render items.
func (l *List) ScrollPosition() (offsetIdx, offsetLine int) {
	return l.offsetIdx, l.offsetLine
}

// Offset returns current scroll offset 在 lines 从 top.
func (l *List) Offset() int {
	offset := 0
	for idx := 0; idx < l.offsetIdx; idx++ {
		offset += l.itemHeight(idx)
		if l.gap > 0 && idx < len(l.items)-1 {
			offset += l.gap
		}
	}
	offset += l.offsetLine
	return offset
}

// lastOffsetItem returns index 和 line offsets 的 last item 那 can
// 是 partially visible 在 viewport.
func (l *List) lastOffsetItem() (int, int, int) {
	var totalHeight int
	var idx int
	for idx = len(l.items) - 1; idx >= 0; idx-- {
		itemHeight := l.itemHeight(idx)
		if l.gap > 0 && idx < len(l.items)-1 {
			itemHeight += l.gap
		}
		totalHeight += itemHeight
		if totalHeight > l.height {
			break
		}
	}

	// Calculate line offset within the item
	lineOffset := max(totalHeight-l.height, 0)
	idx = max(idx, 0)

	return idx, lineOffset, totalHeight
}

// itemHeight returns height 的 item 在 given index, rendering
// it 只 当 it has no current 条目. 一个 stale 条目's height 是 exact.
func (l *List) itemHeight(idx int) int {
	item, entry := l.lookup(idx)
	if item == nil {
		return 0
	}
	if entry == nil {
		entry = l.render(idx, item)
	}
	return entry.height
}

// drawEntry returns current, drawable 条目 为 given index,
// rendering it 在 miss 或 当 条目 是 stale. 结果 must 不
// 是 retained past next invalidation (SetSize width change, SetItems,
// etc.).
func (l *List) drawEntry(idx int) *listCacheEntry {
	item, entry := l.lookup(idx)
	if item == nil {
		return nil
	}
	if entry == nil || entry.stale {
		entry = l.render(idx, item)
	}
	return entry
}

// lookup runs render callbacks 为 item 在 idx 和 returns
// item 到 render along 用 它的 缓存 条目, 或 nil 条目 当 那 是
// out 的 date. Callbacks always 运行: they 是 how list discovers
// per-frame state (selection, highlight), 和 they bump item's
// version 当 那 state changes 它的 output.
func (l *List) lookup(idx int) (Item, *listCacheEntry) {
	if idx < 0 || idx >= len(l.items) {
		return nil, nil
	}
	rawItem := l.items[idx]
	item := rawItem
	for _, cb := range l.renderCallbacks {
		if it := cb(idx, l.selectedIdx, item); it != nil {
			item = it
		}
	}
	entry := l.cache[rawItem]
	if entry == nil || entry.width != l.width || entry.version != rawItem.Version() {
		return item, nil
	}
	return item, entry
}

// render renders item, callback-adjusted form 的 item 在 idx, 和
// stores 结果 在 缓存.
func (l *List) render(idx int, item Item) *listCacheEntry {
	rawItem := l.items[idx]
	rendered := strings.TrimRight(item.Render(l.width), "\n")
	lines := strings.Split(rendered, "\n")

	entry := l.cache[rawItem]
	if entry == nil {
		entry = &listCacheEntry{}
		l.cache[rawItem] = entry
	}
	if entry.height != len(lines) {
		l.totalHeightValid = false
	}
	// version 是 读取 after Render 所以 bump Render itself causes 是
	// 不 mistaken 为 later mutation.
	*entry = listCacheEntry{width: l.width, version: rawItem.Version(), height: len(lines), lines: lines}
	return entry
}

// invalidateAll drops every 缓存 条目. Called 在 width changes.
func (l *List) invalidateAll() {
	for k := range l.cache {
		delete(l.cache, k)
	}
	l.totalHeightValid = false
}

// Invalidate drops 缓存 条目 为 given item, forcing re-render
// 在 next use. No-op 如果 item 是 不 在 缓存.
func (l *List) Invalidate(item Item) {
	delete(l.cache, item)
}

// Restyle runs restyle 在 every item 为 change 那 affects colours 但
// 不 layout, such 作为 theme swap. Cached heights 那 were current stay
// valid, 所以 scroll geometry needs no re-render; each item re-renders 它的
// content once it 是 drawn.
func (l *List) Restyle(restyle func(Item)) {
	for _, item := range l.items {
		entry := l.cache[item]
		current := entry != nil && entry.width == l.width && entry.version == item.Version()
		restyle(item)
		if current {
			entry.version = item.Version()
			entry.stale = true
		}
	}
}

// retainCacheFor drops every 缓存 条目 whose key 是 不 在 given
// item set. Used 通过 SetItems 到 keep entries 为 stable items while
// dropping entries 为 removed ones.
func (l *List) retainCacheFor(items []Item) {
	if len(l.cache) == 0 {
		return
	}
	keep := make(map[Item]struct{}, len(items))
	for _, it := range items {
		keep[it] = struct{}{}
	}
	for k := range l.cache {
		if _, ok := keep[k]; !ok {
			delete(l.cache, k)
		}
	}
}

// BeginSelectionDrag drops cached entries 的 items 在
// inclusive [startIdx, endIdx] range, 所以 they re-render 用 live
// selection overlay. EndSelectionDrag drops them again 为 clean render.
// Indices outside items slice 是 clipped silently.
func (l *List) BeginSelectionDrag(startIdx, endIdx int) {
	if len(l.items) == 0 {
		return
	}
	if startIdx > endIdx {
		startIdx, endIdx = endIdx, startIdx
	}
	startIdx = max(startIdx, 0)
	endIdx = min(endIdx, len(l.items)-1)
	for _, it := range l.items[startIdx : endIdx+1] {
		l.dragItems[it] = struct{}{}
		delete(l.cache, it)
	}
}

// EndSelectionDrag drops entries 的 items drag covered 所以 they
// re-render without the selection overlay.
func (l *List) EndSelectionDrag() {
	for it := range l.dragItems {
		delete(l.dragItems, it)
		delete(l.cache, it)
	}
}

// ScrollToIndex scrolls list 到 given item index.
func (l *List) ScrollToIndex(index int) {
	if index < 0 {
		index = 0
	}
	if index >= len(l.items) {
		index = len(l.items) - 1
	}
	l.offsetIdx = index
	l.offsetLine = 0
}

// ScrollBy scrolls list 通过 given number 的 lines.
func (l *List) ScrollBy(lines int) {
	if len(l.items) == 0 || lines == 0 {
		return
	}

	if l.reverse {
		lines = -lines
	}

	if lines > 0 {
		if l.AtBottom() {
			// Already 在 bottom
			return
		}

		// Scroll down
		l.offsetLine += lines
		currentHeight := l.itemHeight(l.offsetIdx)
		for l.offsetLine >= currentHeight {
			l.offsetLine -= currentHeight
			if l.gap > 0 {
				l.offsetLine = max(0, l.offsetLine-l.gap)
			}

			// Move 到 next item
			l.offsetIdx++
			if l.offsetIdx > len(l.items)-1 {
				// Reached bottom
				l.ScrollToBottom()
				return
			}
			currentHeight = l.itemHeight(l.offsetIdx)
		}

		lastOffsetIdx, lastOffsetLine, _ := l.lastOffsetItem()
		if l.offsetIdx > lastOffsetIdx || (l.offsetIdx == lastOffsetIdx && l.offsetLine > lastOffsetLine) {
			// Clamp 到 bottom
			l.offsetIdx = lastOffsetIdx
			l.offsetLine = lastOffsetLine
		}
	} else if lines < 0 {
		// Scroll up
		l.offsetLine += lines // lines is negative
		for l.offsetLine < 0 {
			// Move 到 previous item
			l.offsetIdx--
			if l.offsetIdx < 0 {
				// Reached top
				l.ScrollToTop()
				break
			}
			totalHeight := l.itemHeight(l.offsetIdx)
			if l.gap > 0 {
				totalHeight += l.gap
			}
			l.offsetLine += totalHeight
		}
	}
}

// VisibleItemIndices finds range 的 items 那 是 visible 在 viewport.
// 这 是 used 为 checking 如果 selected item 是 在 view.
func (l *List) VisibleItemIndices() (startIdx, endIdx int) {
	if len(l.items) == 0 {
		return 0, 0
	}

	startIdx = l.offsetIdx
	currentIdx := startIdx
	visibleHeight := -l.offsetLine

	for currentIdx < len(l.items) {
		visibleHeight += l.itemHeight(currentIdx)
		if l.gap > 0 {
			visibleHeight += l.gap
		}

		if visibleHeight >= l.height {
			break
		}
		currentIdx++
	}

	endIdx = currentIdx
	if endIdx >= len(l.items) {
		endIdx = len(l.items) - 1
	}

	return startIdx, endIdx
}

// Render renders list 和 returns visible lines.
//
// F7: per-item slicing 是 bounded 通过 remaining viewport budget 所以
// per-frame work 是 O(viewport) rather than O(total item heights).
// We never append beyond l.height lines 到 output 缓冲;
// final trim 是 therefore unnecessary. Reverse mode applies same
// final reversal 作为 before, which 是 byte-identical because
// pre-F7 trim happened 在 tail 的 joined 缓冲 ( same
// lines we now drop implicitly per item).
func (l *List) Render() string {
	if len(l.items) == 0 {
		return ""
	}

	budget := max(l.height, 0)
	lines := l.renderWindow(budget)
	// 一个 窗口 那 ends before viewport 是 full while scrolled means
	// offset 是 past end: content below it shrank 或 went away
	// ( item removed, tail 那 collapsed, filter). Clamp 所以
	// view never sits lifted off bottom. Checking 只 underfilled
	// case keeps common 路径 free 的 tail walk.
	if len(lines) < budget && (l.offsetIdx > 0 || l.offsetLine > 0) {
		l.ScrollToBottom()
		lines = l.renderWindow(budget)
	}

	l.height = budget

	if l.reverse {
		// Reverse lines 所以 list renders bottom-到-top.
		for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
			lines[i], lines[j] = lines[j], lines[i]
		}
	}

	return strings.Join(lines, "\n")
}

// renderWindow collects 在 most budget lines starting 在 scroll offset.
func (l *List) renderWindow(budget int) []string {
	lines := make([]string, 0, budget)
	currentIdx := l.offsetIdx
	currentOffset := l.offsetLine

	for currentIdx < len(l.items) {
		remaining := budget - len(lines)
		if remaining <= 0 {
			break
		}

		entry := l.drawEntry(currentIdx)
		if entry == nil {
			break
		}
		itemLines := entry.lines
		itemHeight := len(itemLines)

		if currentOffset >= 0 && currentOffset < itemHeight {
			// Append 只 visible slice 那 fits 在
			// remaining viewport budget. Anything past the
			// budget would 是 discarded 通过 pre-F7 tail
			// trim, 所以 skipping append here 是
			// byte-identical 和 bounded.
			visible := itemLines[currentOffset:]
			if len(visible) > remaining {
				visible = visible[:remaining]
			}
			lines = append(lines, visible...)

			// Gap rows after item, capped 到
			// remaining budget 所以 30k-line item 用
			// trailing gap 可以't push past viewport.
			if l.gap > 0 {
				gapBudget := min(budget-len(lines), l.gap)
				for range gapBudget {
					lines = append(lines, "")
				}
			}
		} else {
			// offsetLine starts inside the gap.
			gapOffset := currentOffset - itemHeight
			gapRemaining := l.gap - gapOffset
			if gapRemaining > 0 {
				gapBudget := min(budget-len(lines), gapRemaining)
				for range gapBudget {
					lines = append(lines, "")
				}
			}
		}

		currentIdx++
		currentOffset = 0 // Reset offset for subsequent items.
	}
	return lines
}

// PrependItems prepends items 到 list.
func (l *List) PrependItems(items ...Item) {
	l.items = append(items, l.items...)

	// Keep view position relative 到 content 那 was visible
	l.offsetIdx += len(items)

	// 更新 selection index 如果 valid
	if l.selectedIdx != -1 {
		l.selectedIdx += len(items)
	}
	l.totalHeightValid = false
}

// SetItems sets items 在 list. 缓存 entries 为 items 那
// remain after swap 是 preserved; entries 为 removed items 是
// dropped.
func (l *List) SetItems(items ...Item) {
	l.items = items
	l.selectedIdx = min(l.selectedIdx, len(l.items)-1)
	l.offsetIdx = min(l.offsetIdx, len(l.items)-1)
	l.offsetLine = 0
	l.retainCacheFor(items)
	l.totalHeightValid = false
}

// AppendItems appends items 到 list.
func (l *List) AppendItems(items ...Item) {
	l.items = append(l.items, items...)
	l.totalHeightValid = false
}

// RemoveItem removes item 在 given index 从 list.
func (l *List) RemoveItem(idx int) {
	if idx < 0 || idx >= len(l.items) {
		return
	}

	removed := l.items[idx]

	// Remove the item
	l.items = append(l.items[:idx], l.items[idx+1:]...)

	// Drop 缓存 条目 为 removed item; entries 为 stable
	// items stay valid because they 是 keyed 通过 指针, 不 index.
	delete(l.cache, removed)
	delete(l.dragItems, removed)

	// Adjust selection 如果 needed
	if l.selectedIdx == idx {
		l.selectedIdx = -1
	} else if l.selectedIdx > idx {
		l.selectedIdx--
	}

	// Adjust offset 如果 needed. Removing item view starts inside
	// starts view 在 top 的 item 那 slid into 它的 place;
	// old line offset belonged 到 removed item 和 would skip into,
	// 或 past, new one.
	if l.offsetIdx > idx {
		l.offsetIdx--
	} else if l.offsetIdx == idx {
		l.offsetIdx = min(l.offsetIdx, max(0, len(l.items)-1))
		l.offsetLine = 0
	}
	l.totalHeightValid = false
}

// Focused returns whether list 是 focused.
func (l *List) Focused() bool {
	return l.focused
}

// Focus sets focus state 的 list.
func (l *List) Focus() {
	l.focused = true
}

// Blur removes focus state 从 list.
func (l *List) Blur() {
	l.focused = false
}

// ScrollToTop scrolls list 到 top.
func (l *List) ScrollToTop() {
	l.offsetIdx = 0
	l.offsetLine = 0
}

// ScrollToBottom scrolls list 到 bottom.
func (l *List) ScrollToBottom() {
	if len(l.items) == 0 {
		return
	}

	lastOffsetIdx, lastOffsetLine, _ := l.lastOffsetItem()
	l.offsetIdx = lastOffsetIdx
	l.offsetLine = lastOffsetLine
}

// ScrollToSelected scrolls list 到 selected item.
func (l *List) ScrollToSelected() {
	if l.selectedIdx < 0 || l.selectedIdx >= len(l.items) {
		return
	}

	// list may 不 have been sized yet 当 caller sets up 它的
	// selection, e.g. dialog constructor 那 runs before first
	// Draw. With no viewport height there 是 no visibility 窗口 到 fit
	// selection into, 所以 pin selected item 到 top 的
	// viewport; first render then shows it instead 的 computing
	// bogus offset 那 skips past it entirely.
	if l.height <= 0 {
		l.offsetIdx = l.selectedIdx
		l.offsetLine = 0
		return
	}

	startIdx, endIdx := l.VisibleItemIndices()
	if l.selectedIdx < startIdx {
		// Selected item 是 above visible range
		l.offsetIdx = l.selectedIdx
		l.offsetLine = 0
	} else if l.selectedIdx > endIdx {
		// Selected item 是 below visible range
		// Scroll 所以 那 selected item 是 在 bottom
		var totalHeight int
		for i := l.selectedIdx; i >= 0; i-- {
			totalHeight += l.itemHeight(i)
			if l.gap > 0 && i < l.selectedIdx {
				totalHeight += l.gap
			}
			if totalHeight >= l.height {
				l.offsetIdx = i
				l.offsetLine = totalHeight - l.height
				break
			}
		}
		if totalHeight < l.height {
			// All items fit 在 viewport
			l.ScrollToTop()
		}
	}
}

// SelectedItemInView returns whether selected item 是 currently 在 view.
func (l *List) SelectedItemInView() bool {
	if l.selectedIdx < 0 || l.selectedIdx >= len(l.items) {
		return false
	}
	startIdx, endIdx := l.VisibleItemIndices()
	return l.selectedIdx >= startIdx && l.selectedIdx <= endIdx
}

// SetSelected sets selected item index 在 list.
// 它 returns -1 如果 index 是 out 的 bounds.
func (l *List) SetSelected(index int) {
	if index < 0 || index >= len(l.items) {
		l.selectedIdx = -1
	} else {
		l.selectedIdx = index
	}
}

// Selected returns index 的 currently selected item. 它 returns -1 如果
// no item 是 selected.
func (l *List) Selected() int {
	return l.selectedIdx
}

// IsSelectedFirst returns whether first item 是 selected.
func (l *List) IsSelectedFirst() bool {
	return l.selectedIdx == 0
}

// IsSelectedLast returns whether last item 是 selected.
func (l *List) IsSelectedLast() bool {
	return l.selectedIdx == len(l.items)-1
}

// SelectPrev selects the visually previous item (moves toward visual top).
// 它 returns whether selection changed.
func (l *List) SelectPrev() bool {
	if l.reverse {
		// In reverse, visual up = higher index
		if l.selectedIdx < len(l.items)-1 {
			l.selectedIdx++
			return true
		}
	} else {
		// Normal: visual up = lower index
		if l.selectedIdx > 0 {
			l.selectedIdx--
			return true
		}
	}
	return false
}

// SelectNext selects next item 在 list.
// 它 returns whether selection changed.
func (l *List) SelectNext() bool {
	if l.reverse {
		// In reverse, visual down = lower index
		if l.selectedIdx > 0 {
			l.selectedIdx--
			return true
		}
	} else {
		// Normal: visual down = higher index
		if l.selectedIdx < len(l.items)-1 {
			l.selectedIdx++
			return true
		}
	}
	return false
}

// SelectFirst selects first item 在 list.
// 它 returns whether selection changed.
func (l *List) SelectFirst() bool {
	if len(l.items) == 0 {
		return false
	}
	l.selectedIdx = 0
	return true
}

// SelectLast selects last item 在 list (highest index).
// 它 returns whether selection changed.
func (l *List) SelectLast() bool {
	if len(l.items) == 0 {
		return false
	}
	l.selectedIdx = len(l.items) - 1
	return true
}

// WrapToStart wraps selection 到 visual start (为 circular navigation).
// In normal mode, 这 是 index 0. In reverse mode, 这 是 highest index.
func (l *List) WrapToStart() bool {
	if len(l.items) == 0 {
		return false
	}
	if l.reverse {
		l.selectedIdx = len(l.items) - 1
	} else {
		l.selectedIdx = 0
	}
	return true
}

// WrapToEnd wraps selection 到 visual end (为 circular navigation).
// In normal mode, 这 是 highest index. In reverse mode, 这 是 index 0.
func (l *List) WrapToEnd() bool {
	if len(l.items) == 0 {
		return false
	}
	if l.reverse {
		l.selectedIdx = 0
	} else {
		l.selectedIdx = len(l.items) - 1
	}
	return true
}

// SelectedItem returns currently selected item. 它 may 是 nil 如果 no item
// 是 selected.
func (l *List) SelectedItem() Item {
	if l.selectedIdx < 0 || l.selectedIdx >= len(l.items) {
		return nil
	}
	return l.items[l.selectedIdx]
}

// SelectFirstInView selects first item currently 在 view.
func (l *List) SelectFirstInView() {
	startIdx, _ := l.VisibleItemIndices()
	l.selectedIdx = startIdx
}

// SelectLastInView selects last item currently 在 view.
func (l *List) SelectLastInView() {
	_, endIdx := l.VisibleItemIndices()
	l.selectedIdx = endIdx
}

// ItemAt returns item 在 given index.
func (l *List) ItemAt(index int) Item {
	if index < 0 || index >= len(l.items) {
		return nil
	}
	return l.items[index]
}

// ItemIndexAtPosition returns item 在 given viewport-relative y
// coordinate. Returns item index 和 y offset within 那 item. 它
// returns -1, -1 如果 no item 是 found.
func (l *List) ItemIndexAtPosition(x, y int) (itemIdx int, itemY int) {
	return l.findItemAtY(x, y)
}

// findItemAtY finds item 在 given viewport y coordinate.
// Returns item index 和 y offset within 那 item. 它 returns -1, -1
// 如果 no item 是 found.
func (l *List) findItemAtY(_, y int) (itemIdx int, itemY int) {
	if y < 0 || y >= l.height {
		return -1, -1
	}

	// Walk through visible items 到 find which one contains 这 y
	currentIdx := l.offsetIdx
	currentLine := -l.offsetLine // Negative because offsetLine is how many lines are hidden

	for currentIdx < len(l.items) && currentLine < l.height {
		itemEndLine := currentLine + l.itemHeight(currentIdx)

		// Check 如果 y 是 within 这 item's visible range
		if y >= currentLine && y < itemEndLine {
			// Found the item, calculate itemY (offset within the item)
			itemY = y - currentLine
			return currentIdx, itemY
		}

		// Move 到 next item
		currentLine = itemEndLine
		if l.gap > 0 {
			currentLine += l.gap
		}
		currentIdx++
	}

	return -1, -1
}
