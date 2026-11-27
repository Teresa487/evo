package model

import (
	"image"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/displaywidth"
	"github.com/clipperhouse/uax29/v2/words"
)

// Constants 为 multi-click detection.
const (
	doubleClickThreshold = 400 * time.Millisecond // 0.4s is typical double-click threshold
	clickTolerance       = 2                      // x,y tolerance for double/tripple click
)

// DelayedClickMsg 是 sent after double-click threshold 到 trigger
// single-click action (like expansion) 如果 no double-click occurred.
type DelayedClickMsg struct {
	ClickID int
	ItemIdx int
	X, Y    int
}

// scrollbarHideMsg 是 sent 到 hide scrollbar after 超时 period.
type scrollbarHideMsg struct {
	seq int // sequence number to ignore stale messages
}

// sidebarScrollbarHideMsg 是 sent 到 hide sidebar scrollbar after 超时.
type sidebarScrollbarHideMsg struct {
	seq int
}

// scrollbarHideCmd returns command 那 sends scrollbarHideMsg after 超时.
func scrollbarHideCmd(seq int) tea.Cmd {
	return tea.Tick(scrollbarHideDuration, func(_ time.Time) tea.Msg {
		return scrollbarHideMsg{seq: seq}
	})
}

// resizeSettleDuration 是 how long after last resize 事件 chat
// waits before it starts warming 消息 缓存 it skipped mid-drag.
const resizeSettleDuration = 120 * time.Millisecond

// warmBatchSize 是 how many messages chat renders into width 缓存
// per warming step. Kept small 所以 no single step blocks UI thread 为
// more than frame 或 所以, even 在 slow-到-render items.
const warmBatchSize = 25

// chatWarmMsg drives one incremental 缓存-warming step. first one 是
// delayed until the resize settles; the rest fire immediately, one per
// batch, 所以 warming spreads across frames instead 的 blocking.
type chatWarmMsg struct {
	seq int // guards against stale timers from superseded resizes
}

// chatWarmCmd schedules next warming step after delay (zero fires 作为
// soon 作为 runtime delivers it).
func chatWarmCmd(seq int, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		return func() tea.Msg { return chatWarmMsg{seq: seq} }
	}
	return tea.Tick(delay, func(_ time.Time) tea.Msg {
		return chatWarmMsg{seq: seq}
	})
}

// sidebarScrollbarHideCmd returns command 那 sends sidebarScrollbarHideMsg
// after 超时.
func sidebarScrollbarHideCmd(seq int) tea.Cmd {
	return tea.Tick(scrollbarHideDuration, func(_ time.Time) tea.Msg {
		return sidebarScrollbarHideMsg{seq: seq}
	})
}

// Chat represents chat UI 模型 那 handles chat interactions 和
// messages.
type Chat struct {
	com      *common.Common
	list     *list.List
	idInxMap map[string]int // Map of message IDs to their indices in the list

	// Mouse state
	mouseDown     bool
	mouseDownItem int // Item index where mouse was pressed
	mouseDownX    int // X position in item content (character offset)
	mouseDownY    int // Y position in item (line offset)
	mouseDragItem int // Current item index being dragged over
	mouseDragX    int // Current X in item content
	mouseDragY    int // Current Y in item

	// Click tracking 为 double/triple clicks
	lastClickTime time.Time
	lastClickX    int
	lastClickY    int
	clickCount    int

	// Pending single click action (delayed 到 detect double-click)
	pendingClickID int // Incremented on each click to invalidate old pending clicks

	// follow 是 flag 到 indicate whether view should auto-scroll 到
	// bottom 在 new messages.
	follow bool

	// drawCache memoizes decoded form 的 last list.Render output 所以
	// repeat frames 用 byte-identical content skip per-cell ANSI
	// reparse 那 uv.StyledString.Draw performs every 调用. See F9
	// (docs/备注/2026-05-12-chat-rendering-perf.md §4.8). Bounded 到 one
	// 条目; invalidated implicitly 通过 string inequality 在 next Draw.
	drawCache *chatDrawCache

	// Scrollbar visibility state
	scrollbarVisible bool
	scrollbarHideSeq int    // current sequence number for hide timer
	scrollbarMode    string // "default", "always", or "never"
	// scrollbarHideArmed 是 hide 超时 already scheduled, 或 zero 当
	// none 是 outstanding. tick 是 armed 从 UI's 更新 tail rather
	// than returned 通过 scroll calls, 所以 caller ignoring command can
	// no longer lose the auto-hide.
	scrollbarHideArmed int

	// resizing suppresses O(N) total-height scan while resize 是 在
	// flight (和 during incremental warm afterward), 所以 drag 只
	// reflows the visible items. resizeSettleSeq guards stale settle/warm
	// timers; warmNext tracks warming progress through 消息 list.
	resizing        bool
	resizeSettleSeq int
	warmNext        int
}

// scrollbarHideDuration 是 how long scrollbar remains visible after scroll activity.
const scrollbarHideDuration = 2 * time.Second

// chatDrawCache holds pre-decoded form 的 last list.Render output.
// 缓存 是 keyed 通过 rendered string 和 screen's width method
// (graphemes vs wcwidth pick different decoders inside ultraviolet's
// printString, 所以 cached 缓冲 是 只 valid 为 method it was
// decoded 用). cached 缓冲 是 independent 的 draw area, 所以
// resize / scroll changes 那 produce same string still hit. We cannot
// use uv.StyledString.Lines because it bottoms out 在 first iteration
// against a zero-bounds rectangle (see ultraviolet styled.go line 45 — the
// shared printString loop's `y >= bounds.Max.Y` exit applies 到
// line-building branch too). 一个 缓冲 的 rendered 文本's natural
// dimensions 是 cheapest correct shape: StyledString.Draw runs once 在
// miss 到 populate it, 和 缓冲.Draw 是 O(cells) cell copy 用 no
// ANSI re-parse 在 hit.
type chatDrawCache struct {
	rendered string
	method   ansi.Method
	buf      uv.ScreenBuffer
}

// NewChat creates new instance 的 [Chat] 那 handles chat interactions 和
// messages.
func NewChat(com *common.Common, scrollbarMode string) *Chat {
	c := &Chat{
		com:           com,
		idInxMap:      make(map[string]int),
		scrollbarMode: scrollbarMode,
	}
	l := list.NewList()
	l.SetGap(1)
	l.RegisterRenderCallback(c.applyHighlightRange)
	l.RegisterRenderCallback(list.FocusedRenderCallback(l))
	c.list = l
	c.mouseDownItem = -1
	c.mouseDragItem = -1
	return c
}

// Height returns height 的 chat view port.
func (m *Chat) Height() int {
	return m.list.Height()
}

// Draw renders chat UI component 到 screen 和 given area.
//
// list's rendered output 是 cached 在 decoded form (see chatDrawCache) 所以
// 那 frames 用 byte-identical content skip ANSI reparse 那
// uv.StyledString.Draw performs 在 every 调用. 缓存 是 keyed 通过
// rendered string 和 screen's width method; area / scroll changes do 不
// invalidate it.
func (m *Chat) Draw(scr uv.Screen, area uv.Rectangle) {
	// Determine scrollbar visibility. Skip it entirely while resizing: the
	// thumb needs the exact total height (O(N) after a width change), which
	// 是 dominant resize cost. 它 returns once resize settles 和
	// 缓存 has been warmed. needs-scrollbar 测试 itself uses
	// cheap bounded overflow check.
	listHeight := m.list.Height() - 1
	needsScrollbar := false
	if !m.resizing {
		needsScrollbar = m.list.Overflows(m.list.Height())
	}

	// Determine visibility based 在 scrollbar mode.
	showScrollbar := false
	switch m.scrollbarMode {
	case config.ScrollbarAlways:
		showScrollbar = needsScrollbar
	case config.ScrollbarDefault:
		showScrollbar = needsScrollbar && m.scrollbarVisible
	case config.ScrollbarNever:
		showScrollbar = false
	}

	rendered := m.list.Render()
	// In follow mode view stays pinned 到 end. Re-anchor whenever
	// render moved end: content growing past it (streaming), 或
	// shrinking away 从 it ( item removed, such 作为 提示词 echo
	// giving way 到 存储 提示词), which would otherwise leave
	// transcript lifted off the bottom until something else scrolls.
	if m.follow {
		idx, line := m.list.ScrollPosition()
		m.list.ScrollToBottom()
		if newIdx, newLine := m.list.ScrollPosition(); newIdx != idx || newLine != line {
			rendered = m.list.Render()
		}
	}
	method, ok := scr.WidthMethod().(ansi.Method)
	if !ok {
		// Width method isn't ansi.Method (unlikely 在 practice — both
		// TerminalScreen 和 ScreenBuffer 存储 ansi.Method). Fall back
		// 到 uncached 路径 所以 behavior matches upstream exactly.
		uv.NewStyledString(rendered).Draw(scr, area)
	} else {
		if m.drawCache == nil ||
			m.drawCache.rendered != rendered ||
			m.drawCache.method != method {
			m.drawCache = newChatDrawCache(rendered, method)
		}
		drawCachedBuffer(scr, area, m.drawCache.buf)
	}

	// Never drawn while resizing.
	if showScrollbar {
		scrollbar := common.Scrollbar(m.com.Styles, listHeight, m.list.TotalHeight()-1, listHeight, m.list.Offset())
		if scrollbar != "" {
			scrollbarArea := image.Rectangle{
				Min: image.Point{X: area.Max.X - m.scrollbarCols(), Y: area.Min.Y},
				Max: image.Point{X: area.Max.X, Y: area.Max.Y},
			}
			uv.NewStyledString(scrollbar).Draw(scr, scrollbarArea)
		}
	}
}

// newChatDrawCache builds chatDrawCache 为 given rendered string 通过
// running uv.StyledString.Draw into fresh 缓冲 sized 到 文本's
// natural bounds under 活跃 width method. 这 是 只 place
// ANSI decoding happens 为 cached frames — subsequent draws reuse buf
// via drawCachedBuffer.
//
// We can't use uv.StyledString.Bounds() here: it 是 hard-coded 到
// ansi.GraphemeWidth, while StyledString.Draw lays cells using the
// destination 缓冲's WidthMethod (which we capture 在 `method`). For
// strings where graphemes 和 wcwidth disagree (emoji ZWJ sequences,
// some CJK, certain combining marks) the two answers diverge, leaving
// cached 缓冲 either too small (trailing cells dropped 在 hit) 或
// too large (dead cells past the live content). Computing dimensions
// 用 `method.StringWidth` per line matches what printString tallies
// cell-通过-cell, since both decode ANSI sequences 和 use same width
// 方法.
func newChatDrawCache(rendered string, method ansi.Method) *chatDrawCache {
	w, h := renderedBounds(rendered, method)
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	buf := uv.NewScreenBuffer(w, h)
	buf.Method = method
	uv.NewStyledString(rendered).Draw(buf, buf.Bounds())
	return &chatDrawCache{
		rendered: rendered,
		method:   method,
		buf:      buf,
	}
}

// renderedBounds returns (width, height) cell extent 的 rendered
// 当 laid out 通过 method. Width 是 widest line's StringWidth (which
// strips ANSI sequences 和 tallies cells via method, exactly like
// printString); height 是 line count. Both match what
// uv.StyledString.Draw will 写入 into 缓冲 whose WidthMethod 是
// method, 所以 缓存 缓冲 是 always sized 到 fit live content.
func renderedBounds(rendered string, method ansi.Method) (w, h int) {
	for line := range strings.SplitSeq(rendered, "\n") {
		w = max(w, method.StringWidth(line))
		h++
	}
	return w, h
}

// drawCachedBuffer blits previously-decoded 缓冲 into scr 在 area,
// mirroring uv.StyledString.Draw's screen-mode behavior 为 默认
// Wrap=false, Tail="" case. The clear loop matches StyledString.Draw line
// 51-56; buf.Draw 调用 replaces per-cell ANSI decode 那
// printString does 在 every uncached frame 用 pure cell copy.
func drawCachedBuffer(scr uv.Screen, area uv.Rectangle, buf uv.ScreenBuffer) {
	// Clear area first 到 match StyledString.Draw — leftover cells
	// 从 previous frame outside new content must 是 zeroed,
	// because 缓冲.Draw skips empty cells (it doesn't clear).
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			scr.SetCell(x, y, nil)
		}
	}
	buf.Draw(scr, area)
}

// BeginResize marks chat 作为 actively resizing 所以 next draws skip
// full-height scan (和 scrollbar), reflowing 只 visible
// items. 它 returns command 那, once resizing settles, starts warming
// 缓存 所以 scrollbar can recompute without blocking.
func (m *Chat) BeginResize() tea.Cmd {
	m.resizing = true
	m.resizeSettleSeq++
	m.warmNext = 0
	return chatWarmCmd(m.resizeSettleSeq, resizeSettleDuration)
}

// WarmStep renders next batch 的 messages into width 缓存 和
// returns command 到 continue warming plus whether warming finished. On
// completion resize suppression 是 cleared 所以 next draw recomputes
// (now instant) total height 和 scrollbar. 一个 stale seq — 从 resize
// 那 has since been superseded — 是 no-op returning (nil, false).
func (m *Chat) WarmStep(seq int) (cmd tea.Cmd, done bool) {
	if seq != m.resizeSettleSeq {
		return nil, false
	}
	m.warmNext = m.list.Prewarm(m.warmNext, warmBatchSize)
	if m.warmNext >= m.list.Len() {
		m.resizing = false
		return nil, true
	}
	return chatWarmCmd(seq, 0), false
}

// SetSize sets size 的 chat view port.
func (m *Chat) SetSize(width, height int) {
	// Capture whether we should stay pinned 到 bottom *before* size
	// change. 一个 width change rewraps every item, 所以 list's line offsets
	// (offsetIdx/offsetLine) become stale 和 AtBottom() can no longer 是
	// trusted afterward. follow short-circuits AtBottom() walk 在
	// common streaming case.
	wasFollowing := m.follow || m.AtBottom()
	m.list.SetSize(max(0, width-m.scrollbarCols()), height)
	// Re-anchor 到 bottom 如果 we were pinned there before resize.
	if wasFollowing {
		m.ScrollToBottom()
	}
}

// scrollbarCols 是 width reserved 为 scrollbar. 它 does 不 depend
// 在 overflow, 所以 list width (和 every cached render) never flips 作为
// the transcript crosses the viewport height.
func (m *Chat) scrollbarCols() int {
	if m.scrollbarMode == config.ScrollbarNever {
		return 0
	}
	return 1
}

// Len returns number 的 items 在 chat list.
func (m *Chat) Len() int {
	return m.list.Len()
}

// Restyle drops cached rendered output 在 every 消息 item 所以 it
// re-renders 用 current styles once drawn. Styles never change
// layout, 所以 list keeps 它的 heights 和 只 visible items render 在
// the next frame.
func (m *Chat) Restyle() {
	m.list.Restyle(func(item list.Item) {
		if msg, ok := item.(chat.MessageItem); ok {
			chat.ClearItemCache(msg)
		}
	})
}

// SetMessages sets chat messages 到 provided list 的 消息 items.
func (m *Chat) SetMessages(msgs ...chat.MessageItem) tea.Cmd {
	m.idInxMap = make(map[string]int)
	m.resetScrollbar() // Reset scrollbar visibility on new session load

	items := make([]list.Item, len(msgs))
	for i, msg := range msgs {
		m.idInxMap[msg.ID()] = i
		// Register nested 工具 IDs 为 tools 那 contain nested tools.
		if container, ok := msg.(chat.NestedToolContainer); ok {
			for _, nested := range container.NestedTools() {
				m.idInxMap[nested.ID()] = i
			}
		}
		items[i] = msg
	}
	m.list.SetItems(items...)
	m.ScrollToBottom()
	return nil
}

// AppendMessages appends new 消息 item 到 chat list.
//
// turn placeholder 是 carried 到 end, 所以 消息 那 lands while
// it 是 up goes above it rather than below: spinner marks bottom 的
// transcript 为 whole turn instead 的 hopping over each arrival.
func (m *Chat) AppendMessages(msgs ...chat.MessageItem) {
	if pending := m.MessageItem(chat.PendingAssistantID); pending != nil && !containsMessage(msgs, chat.PendingAssistantID) {
		m.RemoveMessage(chat.PendingAssistantID)
		msgs = append(append(make([]chat.MessageItem, 0, len(msgs)+1), msgs...), pending)
	}

	items := make([]list.Item, len(msgs))
	indexOffset := m.list.Len()
	for i, msg := range msgs {
		m.idInxMap[msg.ID()] = indexOffset + i
		// Register nested 工具 IDs 为 tools 那 contain nested tools.
		if container, ok := msg.(chat.NestedToolContainer); ok {
			for _, nested := range container.NestedTools() {
				m.idInxMap[nested.ID()] = indexOffset + i
			}
		}
		items[i] = msg
	}
	m.list.AppendItems(items...)
}

// LastPromptID returns id 的 last 存储 提示词 在 view, 或 "".
func (m *Chat) LastPromptID() string {
	for i := m.list.Len() - 1; i >= 0; i-- {
		item, ok := m.list.ItemAt(i).(*chat.UserMessageItem)
		if ok && item.ID() != chat.PendingUserID && item.Message().IsPrompt() {
			return item.ID()
		}
	}
	return ""
}

// containsMessage reports whether any 的 msgs carries given id.
func containsMessage(msgs []chat.MessageItem, id string) bool {
	return slices.ContainsFunc(msgs, func(msg chat.MessageItem) bool {
		return msg.ID() == id
	})
}

// UpdateNestedToolIDs updates ID map 为 nested tools within container.
// 调用 这 after modifying nested tools 到 ensure animations work correctly.
func (m *Chat) UpdateNestedToolIDs(containerID string) {
	idx, ok := m.idInxMap[containerID]
	if !ok {
		return
	}

	item, ok := m.list.ItemAt(idx).(chat.MessageItem)
	if !ok {
		return
	}

	container, ok := item.(chat.NestedToolContainer)
	if !ok {
		return
	}

	// Register all nested 工具 IDs 到 point 到 container's index.
	for _, nested := range container.NestedTools() {
		m.idInxMap[nested.ID()] = idx
	}
}

// hasVisibleAnimation reports whether any item 在 viewport 是 spinning.
func (m *Chat) hasVisibleAnimation() bool {
	if m.list.Len() == 0 {
		return false
	}
	startIdx, endIdx := m.list.VisibleItemIndices()
	for idx := startIdx; idx <= endIdx; idx++ {
		if animatable, ok := m.list.ItemAt(idx).(chat.Animatable); ok && animatable.Spinning() {
			return true
		}
	}
	return false
}

// advanceVisible steps every visible spinning item 通过 one frame 和 reports
// whether any rendered output changed.
func (m *Chat) advanceVisible() (changed bool) {
	if m.list.Len() == 0 {
		return false
	}
	startIdx, endIdx := m.list.VisibleItemIndices()
	for idx := startIdx; idx <= endIdx; idx++ {
		if animatable, ok := m.list.ItemAt(idx).(chat.Animatable); ok && animatable.Spinning() && animatable.Advance() {
			changed = true
		}
	}
	return changed
}

// Focus sets focus state 的 chat component.
func (m *Chat) Focus() {
	m.list.Focus()
}

// Blur removes focus state 从 chat component.
func (m *Chat) Blur() {
	m.list.Blur()
}

// ScrollPosition returns list's first visible item index 和 line
// offset into it.
func (m *Chat) ScrollPosition() (offsetIdx, offsetLine int) {
	return m.list.ScrollPosition()
}

// Offset returns scroll offset 在 lines 从 top 的 list.
func (m *Chat) Offset() int {
	return m.list.Offset()
}

// Selected returns index 的 selected item.
func (m *Chat) Selected() int {
	return m.list.Selected()
}

// Focused returns whether chat list 是 focused.
func (m *Chat) Focused() bool {
	return m.list.Focused()
}

// RenderState captures everything about chat 那 affects 它的 rendered
// output 和 是 不 carried 通过 item versions. Anything added 到 Chat 那
// Draw reads belongs here, 所以 callers 那 memoize whole frames stay correct
// without knowing Chat's internals.
type RenderState struct {
	OffsetIdx        int
	OffsetLine       int
	Selected         int
	Focused          bool
	ScrollbarVisible bool
	// ItemsVersion changes 当 any 消息 mutates 它的 rendered output.
	ItemsVersion uint64
}

// RenderState returns current render-affecting chat state. 它 renders
// nothing; 只 non-constant 片段 是 item version fold, which reads
// one 字段 per 消息.
func (m *Chat) RenderState() RenderState {
	offsetIdx, offsetLine := m.ScrollPosition()
	return RenderState{
		OffsetIdx:        offsetIdx,
		OffsetLine:       offsetLine,
		Selected:         m.Selected(),
		Focused:          m.Focused(),
		ScrollbarVisible: m.scrollbarVisible,
		ItemsVersion:     m.list.ItemsVersion(),
	}
}

// AtBottom returns whether chat list 是 currently scrolled 到 bottom.
func (m *Chat) AtBottom() bool {
	return m.list.AtBottom()
}

// Follow returns whether chat view 是 在 follow mode (auto-scroll 到
// bottom 在 new messages).
func (m *Chat) Follow() bool {
	return m.follow
}

// ScrollToBottom scrolls chat view 到 bottom.
// Does 不 trigger scrollbar visibility (auto-scroll 到 show new content).
func (m *Chat) ScrollToBottom() {
	m.list.ScrollToBottom()
	m.follow = true
}

// ScrollToTop scrolls chat view 到 top.
func (m *Chat) ScrollToTop() {
	m.list.ScrollToTop()
	m.follow = false // Disable follow mode when user scrolls up
	m.showScrollbar()
}

// ScrollBy scrolls chat view 通过 given number 的 line deltas.
func (m *Chat) ScrollBy(lines int) {
	m.list.ScrollBy(lines)
	if lines < 0 {
		// Scrolling up always disables follow mode.
		m.follow = false
	} else if m.AtBottom() {
		// Scrolling down re-enables follow 当 we reach bottom.
		m.follow = true
	}
	m.showScrollbar()
}

// ScrollToSelected scrolls chat view 到 selected item.
func (m *Chat) ScrollToSelected() {
	m.list.ScrollToSelected()
	m.follow = m.AtBottom() // Disable follow mode if user scrolls up
	m.showScrollbar()
}

// ScrollToIndex scrolls chat view 到 item 在 given index.
func (m *Chat) ScrollToIndex(index int) {
	m.list.ScrollToIndex(index)
	m.follow = m.AtBottom() // Disable follow mode if user scrolls up
	m.showScrollbar()
}

// showScrollbar reveals scrollbar 和 restarts 它的 auto-hide 超时.
// tick itself 是 scheduled 通过 EnsureScrollbarHide.
func (m *Chat) showScrollbar() {
	// Only start timer 为 "默认" mode
	if m.scrollbarMode != config.ScrollbarDefault {
		return
	}
	m.scrollbarVisible = true
	m.scrollbarHideSeq++
}

// EnsureScrollbarHide schedules hide tick 为 visible scrollbar whose
// current 超时 has none outstanding. Each scroll bumps 序号, 所以
// fresh 期限 是 armed 为 newest activity while earlier ticks go stale
// 和 get ignored. 这 是 sole place 超时 是 armed, mirroring
// shared animation clock, 所以 no 调用 site can drop it.
func (m *Chat) EnsureScrollbarHide() tea.Cmd {
	if m.scrollbarMode != config.ScrollbarDefault || !m.scrollbarVisible {
		return nil
	}
	if m.scrollbarHideArmed == m.scrollbarHideSeq {
		return nil
	}
	m.scrollbarHideArmed = m.scrollbarHideSeq
	return scrollbarHideCmd(m.scrollbarHideSeq)
}

// HideScrollbar hides scrollbar 如果 序号 matches.
func (m *Chat) HideScrollbar(seq int) {
	// Only hide scrollbar 为 "默认" mode
	if m.scrollbarMode != config.ScrollbarDefault {
		return
	}
	if seq == m.scrollbarHideSeq {
		m.scrollbarVisible = false
		m.scrollbarHideArmed = 0
	}
}

// resetScrollbar hides scrollbar 和 forgets any outstanding 超时, 为
// 当 list stops showing content it was scrolled through.
func (m *Chat) resetScrollbar() {
	m.scrollbarVisible = false
	m.scrollbarHideArmed = 0
}

// ScrollToBottomAndSelectLast scrolls chat view 到 bottom 和 selects
// the last item.
func (m *Chat) ScrollToBottomAndSelectLast() {
	m.ScrollToBottom()
	m.SelectLast()
}

// SelectedItemInView returns whether selected item 是 currently 在 view.
func (m *Chat) SelectedItemInView() bool {
	return m.list.SelectedItemInView()
}

func (m *Chat) isSelectable(index int) bool {
	item := m.list.ItemAt(index)
	if item == nil {
		return false
	}
	_, ok := item.(list.Focusable)
	return ok
}

// SetSelected sets selected 消息 index 在 chat list.
func (m *Chat) SetSelected(index int) {
	m.list.SetSelected(index)
	if index < 0 || index >= m.list.Len() {
		return
	}
	for {
		if m.isSelectable(m.list.Selected()) {
			return
		}
		if m.list.SelectNext() {
			continue
		}
		// If we're 在 end 和 last item isn't selectable, walk backwards
		// 到 find nearest selectable item.
		for {
			if !m.list.SelectPrev() {
				return
			}
			if m.isSelectable(m.list.Selected()) {
				return
			}
		}
	}
}

// SelectPrev selects previous 消息 在 chat list.
func (m *Chat) SelectPrev() {
	for {
		if !m.list.SelectPrev() {
			return
		}
		if m.isSelectable(m.list.Selected()) {
			return
		}
	}
}

// SelectNext selects next 消息 在 chat list.
func (m *Chat) SelectNext() {
	for {
		if !m.list.SelectNext() {
			return
		}
		if m.isSelectable(m.list.Selected()) {
			return
		}
	}
}

// SelectFirst selects first 消息 在 chat list.
func (m *Chat) SelectFirst() {
	if !m.list.SelectFirst() {
		return
	}
	if m.isSelectable(m.list.Selected()) {
		return
	}
	for {
		if !m.list.SelectNext() {
			return
		}
		if m.isSelectable(m.list.Selected()) {
			return
		}
	}
}

// SelectLast selects last 消息 在 chat list.
func (m *Chat) SelectLast() {
	if !m.list.SelectLast() {
		return
	}
	if m.isSelectable(m.list.Selected()) {
		return
	}
	for {
		if !m.list.SelectPrev() {
			return
		}
		if m.isSelectable(m.list.Selected()) {
			return
		}
	}
}

// SelectFirstInView selects first 消息 currently 在 view.
func (m *Chat) SelectFirstInView() {
	startIdx, endIdx := m.list.VisibleItemIndices()
	for i := startIdx; i <= endIdx; i++ {
		if m.isSelectable(i) {
			m.list.SetSelected(i)
			return
		}
	}
}

// SelectLastInView selects last 消息 currently 在 view.
func (m *Chat) SelectLastInView() {
	startIdx, endIdx := m.list.VisibleItemIndices()
	for i := endIdx; i >= startIdx; i-- {
		if m.isSelectable(i) {
			m.list.SetSelected(i)
			return
		}
	}
}

// SelectNearestInView moves out-的-view selection 到 visible edge
// nearest 到 it: top 行 当 selection 是 above viewport,
// bottom 行 当 it 是 below. With no selection, scrolledUp picks
// bottom 行 ( content 用户 是 moving towards) 和 otherwise
// top 行.
func (m *Chat) SelectNearestInView(scrolledUp bool) {
	startIdx, _ := m.list.VisibleItemIndices()
	sel := m.list.Selected()
	switch {
	case sel < 0:
		if scrolledUp {
			m.SelectLastInView()
		} else {
			m.SelectFirstInView()
		}
	case sel < startIdx:
		m.SelectFirstInView()
	default:
		m.SelectLastInView()
	}
}

// ClearMessages removes all messages 从 chat list.
func (m *Chat) ClearMessages() {
	m.idInxMap = make(map[string]int)
	m.resetScrollbar()
	m.list.SetItems()
	m.ClearMouse()
}

// RemoveMessage removes 消息 从 chat list 通过 它的 ID.
func (m *Chat) RemoveMessage(id string) {
	idx, ok := m.idInxMap[id]
	if !ok {
		return
	}

	// Remove 从 list
	m.list.RemoveItem(idx)

	// Remove 从 index map
	delete(m.idInxMap, id)

	// Rebuild index map 为 all items after removed one
	for i := idx; i < m.list.Len(); i++ {
		if item, ok := m.list.ItemAt(i).(chat.MessageItem); ok {
			m.idInxMap[item.ID()] = i
		}
	}
}

// RemoveStaleToolCalls removes 工具 items 的 消息 messageID whose
// calls 是 不 在 keep, 作为 retried 响应 drops what 它的 failed attempt
// streamed. Only live 消息 是 retried, 所以 只 tail 是 scanned.
func (m *Chat) RemoveStaleToolCalls(messageID string, keep []message.ToolCall) {
	var stale []string
	for i := m.list.Len() - 1; i >= 0; i-- {
		item, ok := m.list.ItemAt(i).(chat.MessageItem)
		if !ok {
			break
		}
		tool, isTool := item.(chat.ToolMessageItem)
		switch {
		case isTool && tool.MessageID() == messageID:
			id := tool.ToolCall().ID
			if !slices.ContainsFunc(keep, func(tc message.ToolCall) bool { return tc.ID == id }) {
				stale = append(stale, id)
			}
		case item.ID() == messageID || item.ID() == chat.AssistantInfoID(messageID):
		default:
			i = 0
		}
	}
	for _, id := range stale {
		m.RemoveMessage(id)
	}
}

// MessageItem returns 消息 item 用 given ID, 或 nil 如果 不 found.
func (m *Chat) MessageItem(id string) chat.MessageItem {
	idx, ok := m.idInxMap[id]
	if !ok {
		return nil
	}
	item, ok := m.list.ItemAt(idx).(chat.MessageItem)
	if !ok {
		return nil
	}
	return item
}

// MessageItemAt returns 消息 item 在 index i, 或 nil 如果 i 是 out 的
// range 或 item 是 不 [chat.MessageItem].
func (m *Chat) MessageItemAt(i int) chat.MessageItem {
	if i < 0 || i >= m.list.Len() {
		return nil
	}
	item, ok := m.list.ItemAt(i).(chat.MessageItem)
	if !ok {
		return nil
	}
	return item
}

// ToggleExpandedSelectedItem expands selected 消息 item 如果 it 是 expandable.
func (m *Chat) ToggleExpandedSelectedItem() {
	if expandable, ok := m.list.SelectedItem().(chat.Expandable); ok {
		wasFollowing := m.follow
		expandable.ToggleExpanded()
		if wasFollowing {
			m.ScrollToBottom()
		}
	}
}

// IsSelectedShellItem returns true 如果 currently selected item 是
// ShellItem (bang-mode 结果).
func (m *Chat) IsSelectedShellItem() bool {
	_, ok := m.list.SelectedItem().(*chat.ShellItem)
	return ok
}

// ScrollSelectedShellHorizontal scrolls the selected ShellItem horizontally
// 通过 delta columns. No-op 如果 selected item 是 不 ShellItem.
func (m *Chat) ScrollSelectedShellHorizontal(delta int) {
	if shell, ok := m.list.SelectedItem().(*chat.ShellItem); ok {
		shell.ScrollHorizontal(delta)
	}
}

// HandleKeyMsg handles key events 为 chat component.
func (m *Chat) HandleKeyMsg(key tea.KeyMsg) (bool, tea.Cmd) {
	if m.list.Focused() {
		if handler, ok := m.list.SelectedItem().(chat.KeyEventHandler); ok {
			return handler.HandleKeyEvent(key)
		}
	}
	return false, nil
}

// HandleMouseDown handles mouse down events 为 chat component.
// 它 detects single, double, 和 triple clicks 为 文本 selection.
// Returns whether click was handled 和 optional command 为 delayed
// single-click actions.
func (m *Chat) HandleMouseDown(x, y int) (bool, tea.Cmd) {
	if m.list.Len() == 0 {
		return false, nil
	}

	itemIdx, itemY := m.list.ItemIndexAtPosition(x, y)
	if itemIdx < 0 {
		return false, nil
	}
	if !m.isSelectable(itemIdx) {
		return false, nil
	}

	// Increment pending click ID 到 invalidate any previous pending clicks.
	m.pendingClickID++
	clickID := m.pendingClickID

	// Detect multi-click (double/triple)
	now := time.Now()
	if now.Sub(m.lastClickTime) <= doubleClickThreshold &&
		abs(x-m.lastClickX) <= clickTolerance &&
		abs(y-m.lastClickY) <= clickTolerance {
		m.clickCount++
	} else {
		m.clickCount = 1
	}
	m.lastClickTime = now
	m.lastClickX = x
	m.lastClickY = y

	// Select item 那 was clicked
	m.list.SetSelected(itemIdx)

	var cmd tea.Cmd

	switch m.clickCount {
	case 1:
		// Single click - start selection 和 schedule delayed click action.
		m.mouseDown = true
		m.mouseDownItem = itemIdx
		m.mouseDownX = x
		m.mouseDownY = itemY
		m.mouseDragItem = itemIdx
		m.mouseDragX = x
		m.mouseDragY = itemY

		// Schedule delayed click action (e.g., expansion) after a short delay.
		// If double-click occurs, clickID will 是 invalidated.
		cmd = tea.Tick(doubleClickThreshold, func(t time.Time) tea.Msg {
			return DelayedClickMsg{
				ClickID: clickID,
				ItemIdx: itemIdx,
				X:       x,
				Y:       itemY,
			}
		})
	case 2:
		// Double click - select word (no delayed action)
		m.selectWord(itemIdx, x, itemY)
	case 3:
		// Triple click - select line (no delayed action)
		m.selectLine(itemIdx, itemY)
		m.clickCount = 0 // Reset after triple click
	}

	return true, cmd
}

// HandleDelayedClick handles a delayed single-click action (like expansion).
// 它 只 executes 如果 click ID matches (i.e., no double-click occurred)
// 和 no 文本 selection was made (drag 到 select).
func (m *Chat) HandleDelayedClick(msg DelayedClickMsg) bool {
	// Ignore 如果 这 click was superseded 通过 newer click (double/triple).
	if msg.ClickID != m.pendingClickID {
		return false
	}

	// Don't expand 如果 用户 dragged 到 select 文本.
	if m.HasHighlight() {
		return false
	}

	// Execute the click action (e.g., expansion).
	selectedItem := m.list.SelectedItem()
	if clickable, ok := selectedItem.(list.MouseClickable); ok {
		handled := clickable.HandleMouseClick(ansi.MouseButton1, msg.X, msg.Y)
		// Toggle expansion 只 当 item signalled it handled
		// click. Items like AssistantMessageItem 只 报告 handled 当
		// click 是 在 their expandable region, 所以 这 avoids
		// toggling expansion 为 clicks outside clickable area.
		if handled {
			if expandable, ok := selectedItem.(chat.Expandable); ok {
				wasFollowing := m.follow
				expandable.ToggleExpanded()
				if wasFollowing {
					m.ScrollToBottom()
				}
			}
		}
		return handled
	}

	return false
}

// HandleMouseUp handles mouse up events 为 chat component.
func (m *Chat) HandleMouseUp(x, y int) bool {
	if !m.mouseDown {
		return false
	}

	m.mouseDown = false
	return true
}

// HandleMouseDrag handles mouse drag events 为 chat component.
func (m *Chat) HandleMouseDrag(x, y int) bool {
	if !m.mouseDown {
		return false
	}

	if m.list.Len() == 0 {
		return false
	}

	itemIdx, itemY := m.list.ItemIndexAtPosition(x, y)
	if itemIdx < 0 {
		return false
	}

	m.mouseDragItem = itemIdx
	m.mouseDragX = x
	m.mouseDragY = itemY

	return true
}

// HasHighlight returns whether there 是 currently highlighted content.
func (m *Chat) HasHighlight() bool {
	startItemIdx, startLine, startCol, endItemIdx, endLine, endCol := m.getHighlightRange()
	return startItemIdx >= 0 && endItemIdx >= 0 && (startLine != endLine || startCol != endCol)
}

// HighlightContent returns currently highlighted content based 在 mouse
// selection. 它 returns empty string 如果 no content 是 highlighted.
func (m *Chat) HighlightContent() string {
	startItemIdx, startLine, startCol, endItemIdx, endLine, endCol := m.getHighlightRange()
	if startItemIdx < 0 || endItemIdx < 0 || startLine == endLine && startCol == endCol {
		return ""
	}

	var sb strings.Builder
	for i := startItemIdx; i <= endItemIdx; i++ {
		item := m.list.ItemAt(i)
		if hi, ok := item.(list.Highlightable); ok {
			startLine, startCol, endLine, endCol := hi.Highlight()
			listWidth := m.list.Width()
			var rendered string
			if rr, ok := item.(list.RawRenderable); ok {
				rendered = rr.RawRender(listWidth)
			} else {
				rendered = item.Render(listWidth)
			}
			sb.WriteString(list.HighlightContent(
				rendered,
				uv.Rect(0, 0, listWidth, lipgloss.Height(rendered)),
				startLine,
				startCol,
				endLine,
				endCol,
			))
			sb.WriteString(strings.Repeat("\n", m.list.Gap()))
		}
	}

	return strings.TrimSpace(sb.String())
}

// ClearMouse clears the current mouse interaction state.
func (m *Chat) ClearMouse() {
	m.mouseDown = false
	m.mouseDownItem = -1
	m.mouseDragItem = -1
	m.lastClickTime = time.Time{}
	m.lastClickX = 0
	m.lastClickY = 0
	m.clickCount = 0
	m.pendingClickID++ // Invalidate any pending delayed click
}

// applyHighlightRange applies current highlight range 到 chat items.
func (m *Chat) applyHighlightRange(idx, selectedIdx int, item list.Item) list.Item {
	if hi, ok := item.(list.Highlightable); ok {
		// Apply highlight
		startItemIdx, startLine, startCol, endItemIdx, endLine, endCol := m.getHighlightRange()
		sLine, sCol, eLine, eCol := -1, -1, -1, -1
		if idx >= startItemIdx && idx <= endItemIdx {
			if idx == startItemIdx && idx == endItemIdx {
				// Single item selection
				sLine = startLine
				sCol = startCol
				eLine = endLine
				eCol = endCol
			} else if idx == startItemIdx {
				// First item - 从 start position 到 end 的 item
				sLine = startLine
				sCol = startCol
				eLine = -1
				eCol = -1
			} else if idx == endItemIdx {
				// Last item - 从 start 的 item 到 end position
				sLine = 0
				sCol = 0
				eLine = endLine
				eCol = endCol
			} else {
				// Middle item - fully highlighted
				sLine = 0
				sCol = 0
				eLine = -1
				eCol = -1
			}
		}

		hi.SetHighlight(sLine, sCol, eLine, eCol)
		return hi.(list.Item)
	}

	return item
}

// getHighlightRange returns the current highlight range.
func (m *Chat) getHighlightRange() (startItemIdx, startLine, startCol, endItemIdx, endLine, endCol int) {
	if m.mouseDownItem < 0 {
		return -1, -1, -1, -1, -1, -1
	}

	downItemIdx := m.mouseDownItem
	dragItemIdx := m.mouseDragItem

	// Determine selection direction
	draggingDown := dragItemIdx > downItemIdx ||
		(dragItemIdx == downItemIdx && m.mouseDragY > m.mouseDownY) ||
		(dragItemIdx == downItemIdx && m.mouseDragY == m.mouseDownY && m.mouseDragX >= m.mouseDownX)

	if draggingDown {
		// Normal forward selection
		startItemIdx = downItemIdx
		startLine = m.mouseDownY
		startCol = m.mouseDownX
		endItemIdx = dragItemIdx
		endLine = m.mouseDragY
		endCol = m.mouseDragX
	} else {
		// Backward selection (dragging up)
		startItemIdx = dragItemIdx
		startLine = m.mouseDragY
		startCol = m.mouseDragX
		endItemIdx = downItemIdx
		endLine = m.mouseDownY
		endCol = m.mouseDownX
	}

	return startItemIdx, startLine, startCol, endItemIdx, endLine, endCol
}

// selectWord selects word 在 given position within item.
func (m *Chat) selectWord(itemIdx, x, itemY int) {
	item := m.list.ItemAt(itemIdx)
	if item == nil {
		return
	}

	// Get rendered content 为 这 item
	var rendered string
	if rr, ok := item.(list.RawRenderable); ok {
		rendered = rr.RawRender(m.list.Width())
	} else {
		rendered = item.Render(m.list.Width())
	}

	lines := strings.Split(rendered, "\n")
	if itemY < 0 || itemY >= len(lines) {
		return
	}

	// Adjust x 为 item's left padding (border + padding) 到 get content column.
	// mouse x 是 在 viewport space, 但 we need content space 为 boundary detection.
	offset := chat.MessageLeftPaddingTotal
	contentX := max(x-offset, 0)

	line := ansi.Strip(lines[itemY])
	startCol, endCol := findWordBoundaries(line, contentX)
	if startCol == endCol {
		// No word found 在 position, fallback 到 single click behavior
		m.mouseDown = true
		m.mouseDownItem = itemIdx
		m.mouseDownX = x
		m.mouseDownY = itemY
		m.mouseDragItem = itemIdx
		m.mouseDragX = x
		m.mouseDragY = itemY
		return
	}

	// Set selection 到 word boundaries (convert back 到 viewport space).
	// Keep mouseDown true 所以 HandleMouseUp triggers copy.
	m.mouseDown = true
	m.mouseDownItem = itemIdx
	m.mouseDownX = startCol + offset
	m.mouseDownY = itemY
	m.mouseDragItem = itemIdx
	m.mouseDragX = endCol + offset
	m.mouseDragY = itemY
}

// selectLine selects entire line 在 given position within item.
func (m *Chat) selectLine(itemIdx, itemY int) {
	item := m.list.ItemAt(itemIdx)
	if item == nil {
		return
	}

	// Get rendered content 为 这 item
	var rendered string
	if rr, ok := item.(list.RawRenderable); ok {
		rendered = rr.RawRender(m.list.Width())
	} else {
		rendered = item.Render(m.list.Width())
	}

	lines := strings.Split(rendered, "\n")
	if itemY < 0 || itemY >= len(lines) {
		return
	}

	// Get line length (stripped 的 ANSI codes) 和 account 为 padding.
	// SetHighlight will subtract offset, 所以 we need 到 add it here.
	offset := chat.MessageLeftPaddingTotal
	lineLen := ansi.StringWidth(lines[itemY])

	// Set selection 到 entire line.
	// Keep mouseDown true 所以 HandleMouseUp triggers copy.
	m.mouseDown = true
	m.mouseDownItem = itemIdx
	m.mouseDownX = 0
	m.mouseDownY = itemY
	m.mouseDragItem = itemIdx
	m.mouseDragX = lineLen + offset
	m.mouseDragY = itemY
}

// findWordBoundaries finds start 和 end column 的 word 在 given column.
// Returns (startCol, endCol) where endCol 是 exclusive.
func findWordBoundaries(line string, col int) (startCol, endCol int) {
	if line == "" || col < 0 {
		return 0, 0
	}

	i := displaywidth.StringGraphemes(line)
	for i.Next() {
	}

	// Segment the line into words using UAX#29.
	lineCol := 0 // tracks the visited column widths
	lastCol := 0 // tracks the start of the current token
	iter := words.FromString(line)
	for iter.Next() {
		token := iter.Value()
		tokenWidth := displaywidth.String(token)

		graphemeStart := lineCol
		graphemeEnd := lineCol + tokenWidth
		lineCol += tokenWidth

		// If clicked before 这 token, return previous token boundaries.
		if col < graphemeStart {
			return lastCol, lastCol
		}

		// 更新 lastCol 到 end 的 这 token 为 next iteration.
		lastCol = graphemeEnd

		// If clicked within 这 token, return 它的 boundaries.
		if col >= graphemeStart && col < graphemeEnd {
			// If clicked 在 whitespace, return empty selection.
			if strings.TrimSpace(token) == "" {
				return col, col
			}
			return graphemeStart, graphemeEnd
		}
	}

	return col, col
}

// abs returns absolute 值 的 integer.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
