// framecache.go —— 渲染帧缓存。

package model

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	// frameCacheTTL bounds how long memoized frame may 是 served after it
	// was rendered. 它 是 safety valve: even 如果 state change slips past
	// invalidation, stale frame 是 replaced within one TTL 的 next
	// 更新, 和 通过 空闲 GC within two TTLs 当 there 是 none.
	frameCacheTTL = 3 * time.Second

	// frameCacheMaxEntries caps number 的 memoized frames. 一个 frame 是
	// full styled screen 作为 文本; 在 large 终态 用 heavily
	// styled chat content 这 can reach several hundred KB each, 所以
	// cap bounds worst-case memory 到 roughly 10-20 MB.
	frameCacheMaxEntries = 32
)

// frameGCMsg 是 sent 在 timer 到 sweep expired frames 当 UI 是
// otherwise 空闲, 所以 burst 的 scrolling does 不 pin memory indefinitely.
type frameGCMsg struct{}

// frameKey identifies rendered frame 为 given chat scroll position.
// Everything else 那 affects frame (layout, dialogs, items, sidebar
// content, textarea) invalidates whole 缓存 via reset, 所以 key 只
// needs 到 capture what scroll-只 更新 是 allowed 到 change. State
// 那 是 读取 live 在 render time rather than delivered 通过 消息
// (e.g. agent busy flag) can lag 为 在 most one TTL within scroll
// burst; TTL 是 safety valve 为 那.
type frameKey struct {
	width, height int
	chat          RenderState
}

type frameEntry struct {
	content string
	cursor  *tea.Cursor
	// 在 是 render time 和 drives TTL expiry; lastUsed 是 last hit
	// 和 drives eviction, 所以 hot frame (e.g. clamped bottom during
	// overscroll) 是 kept while still expiring 在 schedule.
	at       time.Time
	lastUsed time.Time
}

// frameCache memoizes fully rendered frames keyed 通过 chat scroll position.
//
// Bubble Tea calls View after every 更新, 所以 flood 的 wheel events pays
// 为 full redraw per 事件 even 当 scroll offset did 不 move
// (clamped 在 top 或 bottom) 或 moved back 到 position rendered
// moment ago. Serving those frames 从 memory keeps 事件 loop
// draining instead 的 blocking input behind redraw work.
type frameCache struct {
	entries map[frameKey]*frameEntry
	ttl     time.Duration
	max     int
	now     func() time.Time

	hits, misses int
	// putSkips counts frames dropped because the state moved while Draw
	// was rendering them.
	putSkips int
}

func newFrameCache(ttl time.Duration, maxEntries int) *frameCache {
	return &frameCache{
		entries: make(map[frameKey]*frameEntry, maxEntries),
		ttl:     ttl,
		max:     maxEntries,
		now:     time.Now,
	}
}

// Len returns number 的 memoized frames, including expired ones 那
// have 不 yet been swept.
func (c *frameCache) Len() int {
	return len(c.entries)
}

func (c *frameCache) expired(e *frameEntry, now time.Time) bool {
	return now.Sub(e.at) > c.ttl
}

// get returns memoized frame 为 key 如果 present 和 within TTL.
func (c *frameCache) get(key frameKey) (string, *tea.Cursor, bool) {
	e, ok := c.entries[key]
	if !ok {
		c.misses++
		return "", nil, false
	}
	now := c.now()
	if c.expired(e, now) {
		delete(c.entries, key)
		c.misses++
		return "", nil, false
	}
	c.hits++
	e.lastUsed = now
	if e.cursor == nil {
		return e.content, nil, true
	}
	cur := *e.cursor
	return e.content, &cur, true
}

// put memoizes frame, sweeping expired entries first 和 evicting
// least recently used 条目 当 缓存 是 full.
func (c *frameCache) put(key frameKey, content string, cursor *tea.Cursor) {
	now := c.now()
	c.gc(now)
	if _, ok := c.entries[key]; !ok && len(c.entries) >= c.max {
		c.evictLRU()
	}
	var cur *tea.Cursor
	if cursor != nil {
		copied := *cursor
		cur = &copied
	}
	c.entries[key] = &frameEntry{content: content, cursor: cur, at: now, lastUsed: now}
}

// gc removes every 条目 older than TTL.
func (c *frameCache) gc(now time.Time) {
	for key, e := range c.entries {
		if c.expired(e, now) {
			delete(c.entries, key)
		}
	}
}

func (c *frameCache) evictLRU() {
	var (
		victimKey frameKey
		victim    *frameEntry
	)
	for key, e := range c.entries {
		if victim == nil || e.lastUsed.Before(victim.lastUsed) {
			victimKey, victim = key, e
		}
	}
	if victim != nil {
		delete(c.entries, victimKey)
	}
}

// reset drops every memoized frame. Called whenever 更新 may have
// changed anything other than the chat scroll position.
func (c *frameCache) reset() {
	if len(c.entries) == 0 {
		return
	}
	clear(c.entries)
}

// markScrollOnly flags current 更新 作为 having changed nothing 但
// chat scroll position 或 selection, which lets View serve memoized frame
// 为 那 position. 更新 arms 空闲 GC timer 当 flag 是 set; it
// 是 不 returned here 所以 callers 那 序号 their commands do 不 put
// blocking tick ahead 的 real work.
func (m *UI) markScrollOnly() {
	m.scrollOnlyUpdate = true
}

// invalidateFrames forces next View 到 drop all memoized frames, even 如果
// current 更新 was marked scroll-只.
func (m *UI) invalidateFrames() {
	m.frameDirty = true
}

// beginFrameUpdate clears per-更新 memoization flag 所以 flag left
// over 从 更新 whose View never consumed it cannot leak into 这
// one.
func (m *UI) beginFrameUpdate() {
	m.scrollOnlyUpdate = false
	m.frameSkipPut = false
}

// endFrameUpdate returns command 那 arms 空闲 GC timer 当 这
// 更新 was scroll-只, coming View can actually memoize frame, 和
// no timer 是 pending.
func (m *UI) endFrameUpdate() tea.Cmd {
	if !m.scrollOnlyUpdate || !m.frameCacheable() {
		return nil
	}
	return m.armFrameGC()
}

// frameCacheable reports whether current UI state renders frames 那
// may 是 memoized.
func (m *UI) frameCacheable() bool {
	return m.frames != nil && m.state == uiChat && !m.dialog.HasDialogs()
}

func (m *UI) armFrameGC() tea.Cmd {
	if m.frames == nil || m.frameGCArmed {
		return nil
	}
	m.frameGCArmed = true
	return tea.Tick(m.frames.ttl, func(time.Time) tea.Msg { return frameGCMsg{} })
}

// handleFrameGC sweeps expired frames. The sweep itself changes nothing
// visible, 所以 更新 是 marked scroll-只 到 avoid discarding
// still-valid frames; 更新 re-arms timer through endFrameUpdate while
// frames remain. When sweep empties 缓存 UI has been 空闲 为
// full TTL: frame rendered 通过 following View 是 不 memoized 所以
// nothing stays pinned 和 timer 是 left disarmed until next scroll.
func (m *UI) handleFrameGC() {
	m.frameGCArmed = false
	if m.frames == nil {
		return
	}
	m.frames.gc(m.frames.now())
	if m.frames.Len() == 0 {
		m.frameSkipPut = true
		return
	}
	m.scrollOnlyUpdate = true
}

// frameKeyNow returns key 为 current state without touching
// per-更新 flags.
func (m *UI) frameKeyNow() (frameKey, bool) {
	if m.frames == nil || !m.frameCacheable() {
		return frameKey{}, false
	}
	return frameKey{
		width:  m.width,
		height: m.height,
		chat:   m.chat.RenderState(),
	}, true
}

// currentFrameKey consumes per-更新 memoization flags, resets 缓存
// 当 更新 was 不 scroll-只, 和 returns key 为 frame
// about 到 是 rendered. ok 是 false 当 current state 是 不 cacheable.
func (m *UI) currentFrameKey() (key frameKey, ok bool) {
	scrollOnly := m.scrollOnlyUpdate && !m.frameDirty
	m.scrollOnlyUpdate = false
	m.frameDirty = false
	if m.frames == nil {
		return frameKey{}, false
	}
	if !scrollOnly {
		m.frames.reset()
	}
	if !m.frameCacheable() {
		return frameKey{}, false
	}
	return m.frameKeyNow()
}

// storeFrame memoizes a freshly rendered frame under key. If Draw changed
// the layout while rendering (frameDirty set after currentFrameKey consumed
// it) frame no longer matches key 和 every earlier frame 是 stale,
// 所以 缓存 是 reset instead. GC-triggered renders 是 不 存储 所以
// 缓存 drains fully 当 空闲.
func (m *UI) storeFrame(key frameKey, content string, cursor *tea.Cursor) {
	if m.frameDirty {
		m.frameDirty = false
		m.frames.reset()
		return
	}
	if m.frameSkipPut {
		m.frameSkipPut = false
		return
	}
	// Re-读取 key after Draw. Rendering item may bump 它的 version
	// ( list does same re-读取 为 same reason), 和 Draw may
	// re-anchor list 在 follow mode. Either way frame no longer
	// depicts state key describes, 所以 storing it would serve frame
	// under position it does 不 match.
	if now, ok := m.frameKeyNow(); !ok || now != key {
		m.frames.putSkips++
		return
	}
	m.frames.put(key, content, cursor)
}
