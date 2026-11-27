// assistant.go —— UI 消息渲染：把后端来的消息/工具调用转成 TUI 可显示的项。
// 属于产物链的末端：db/message → Workspace.ListMessages → ui/chat → 屏幕。

package chat

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/anim"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// assistantMessageTruncateFormat 是 文本 shown 当 助手消息 是
// truncated 在 collapsed state.
const assistantMessageTruncateFormat = "… (%d lines hidden) [click or space to expand]"

// assistantMessageTailWindowFormat 是 shown above tail-windowed thinking
// block 到 advertise 那 earlier lines exist 和 那 用户 can
// promote view 到 full expansion. promotion 是 wired through
// existing ToggleExpanded 路径 (click / space) — F5 deliberately
// does 不 add new keybinding.
const assistantMessageTailWindowFormat = "… %d earlier lines hidden [click or space for full view]"

// maxCollapsedThinkingHeight defines maximum height 的 thinking
const maxCollapsedThinkingHeight = 10

// 默认 copy 为 提供方-拒绝 banner. agent persists 只
// FinishReasonContentFilter reason; TUI owns 这 文本 和
// fills it 在 当 finish 片段 carries no 消息/details (
// normal live 路径, 和 restored sessions). Kept here 作为 single
// source 的 truth 所以 render 路径 和 tests cannot drift apart.
const (
	refusalTagLabel = "REFUSED"
	refusalTitle    = "Model refused to continue"
	refusalDetails  = "The provider's safety classifier stopped this response before any usable content was produced. Rephrase the request, start a fresh session, or try a different model."
)

// 默认 copy 为 truncated-响应 banner. As 用 refusals,
// agent persists 只 FinishReasonMaxTokens reason 和 TUI owns
// 文本, 所以 响应 那 stops mid-sentence says why instead 的
// looking 完成. hint matters: raising max tokens 是 one
// thing 用户 can do about it.
const (
	truncatedTagLabel = "TRUNCATED"
	truncatedTitle    = "Response cut off at the token limit"
	truncatedDetails  = "The model reached its output token limit and stopped mid-response. Raise the model's max tokens to get the full answer, or retry with a narrower request."
)

// maxExpandedThinkingTailLines 是 F5 tail-窗口 cap. When 用户
// expands thinking block whose post-glamour line count exceeds 这
// threshold, 只 last N lines 是 shown 用 affordance line
// indicating how many earlier lines 是 hidden. Clicking / pressing
// space again promotes view 到 full expansion. slice 是
// taken AFTER glamour render (不 before) 所以 fenced code blocks,
// lists, 和 tables 是 不 torn 在 arbitrary boundaries.
const maxExpandedThinkingTailLines = 200

// thinkingViewMode 是 F5 three-state view machine 为 thinking
// block. ToggleExpanded cycles
// collapsed → tail-窗口 → full-expanded → collapsed, skipping
// tail-窗口 step 当 rendered thinking fits within cap 所以
// short blocks still toggle 在 two clicks.
type thinkingViewMode uint8

const (
	thinkingCollapsed thinkingViewMode = iota
	thinkingTailWindow
	thinkingFullExpanded
)

// assistantSection 是 per-section render 缓存 为 AssistantMessageItem.
// Each section (thinking, content, 错误) carries 它的 own keys 所以 那
// streaming section does 不 invalidate different — often more
// expensive — section's cached render. srcHash 是 FNV-64 的
// section's source 文本; extra captures any other state 那 changes
// the rendered output (e.g. thinkingExpanded, the thinking footer
// inputs). valid disambiguates real 缓存 hit 从 zero 值
// 当 both source 文本 和 extras hash 到 zero. aux carries any
// per-section side data 那 caller needs 到 recover 在 hit
// (e.g. thinking box height 为 click detection).
type assistantSection struct {
	width   int
	srcHash uint64
	extra   uint64
	out     string
	h       int
	aux     int
	valid   bool
}

// hit reports whether 缓存 条目 matches requested key.
func (s *assistantSection) hit(width int, srcHash, extra uint64) bool {
	return s.valid && s.width == width && s.srcHash == srcHash && s.extra == extra
}

// 存储 records rendered output under given key.
func (s *assistantSection) store(width int, srcHash, extra uint64, out string, aux int) {
	s.width = width
	s.srcHash = srcHash
	s.extra = extra
	s.out = out
	s.h = lipgloss.Height(out)
	s.aux = aux
	s.valid = true
}

// reset drops the cached output.
func (s *assistantSection) reset() {
	*s = assistantSection{}
}

// fnv64 hashes single string 用 FNV-64.
func fnv64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// countLines returns number 的 lines 在 s (i.e. number 的
// newline-separated segments). Equivalent 到 len(strings.Split(s,
// "\n")) 但 allocates nothing. See CHARM-1785.
func countLines(s string) int {
	if s == "" {
		return 1
	}
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}

// tailLines returns last n lines 的 s 和 count 的 hidden
// (earlier) lines. totalLines 是 pre-computed line count 的 s
// (从 countLines). 它 finds cut point 用 bounded backward
// scan 所以 cost 是 O(n) 在 number 的 kept lines, 不 O(L)
// 在 total document length. See CHARM-1785.
func tailLines(s string, n, totalLines int) (tail string, hidden int) {
	if n <= 0 {
		return "", totalLines
	}
	if totalLines <= n {
		return s, 0
	}
	// Find nth newline 从 end. tail starts after it.
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count == n {
				return s[i+1:], totalLines - n
			}
		}
	}
	return s, 0
}

// fnvFields hashes list 的 byte fields 用 length-prefix framing
// 所以 那 no concatenation collision can occur between distinct
// 字段 tuples ( NUL inside one 字段 cannot impersonate
// boundary between two fields). Each 字段 是 preceded 通过 它的
// length encoded 作为 8 bytes little-endian.
func fnvFields(fields ...[]byte) uint64 {
	h := fnv.New64a()
	var lenBuf [8]byte
	for _, f := range fields {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(f)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(f)
	}
	return h.Sum64()
}

// AssistantMessageItem represents 助手消息 在 chat UI.
//
// 这 item includes thinking, 和 content 但 does 不 include 工具 calls.
type AssistantMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	message           *message.Message
	sty               *styles.Styles
	anim              *anim.Anim
	thinkingViewMode  thinkingViewMode
	thinkingBoxHeight int // Tracks the rendered thinking box height for click detection.

	// planAgent marks 这 item 作为 plan-agent output. While plan
	// streams ( 消息 是 不 finished) 和 plan-start marker
	// has arrived, content renders 作为 open plan card: top 和
	// side borders 只, 用 bottom border withheld until
	// plan-ready marker lands.
	planAgent bool

	// Incremental FNV-64a hash 的 thinking 文本. Avoids
	// re-hashing entire accumulated 文本 在 every streaming
	// tick. thinkingHashSample holds short prefix 的 hashed
	// 文本 所以 we can detect divergence (e.g. 用户 重试 那
	// rewrites thinking 从 scratch) without re-hashing
	// whole thing. See CHARM-1785.
	thinkingHash       uint64
	thinkingHashLen    int
	thinkingHashSample string

	// Per-section render caches. Splitting these out means content
	// streaming does 不 invalidate (often expensive) thinking
	// render, 和 vice versa.
	thinkingSec assistantSection
	contentSec  assistantSection
	errorSec    assistantSection

	// streamingContent caches "stable prefix" glamour render 的
	// 助手 content body 所以 each streaming 刷盘 只
	// re-renders trailing partial. F8 的
	// docs/备注/2026-05-12-chat-rendering-perf.md. See
	// streaming_markdown.go 为 full algorithm.
	streamingContent streamingMarkdown

	// streamingThinking applies same stable-prefix caching 到
	// thinking/reasoning section. Without 这, every streaming
	// delta forces full glamour re-render 的 entire accumulated
	// thinking 文本, which burns CPU 和 starves 终态 emulator
	// during long reasoning traces.
	streamingThinking streamingMarkdown

	// streamingPlan applies same stable-prefix caching 到 plan
	// card while plan streams, 所以 each streaming 刷盘 只
	// re-renders trailing partial 的 plan document. Kept apart
	// 从 streamingContent because it renders through plan
	// renderer, whose cached prefix 是 不 interchangeable.
	streamingPlan streamingMarkdown
}

var _ Expandable = (*AssistantMessageItem)(nil)

// NewAssistantMessageItem creates a new AssistantMessageItem.
func NewAssistantMessageItem(sty *styles.Styles, message *message.Message) MessageItem {
	v := list.NewVersioned()
	a := &AssistantMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		message:                  message,
		sty:                      sty,
	}

	// 摘要 messages 是 generated outside regular turn, 所以 turn
	// timer 是 不 活跃 while they 流; give them their own elapsed
	// timer anchored 在 item creation instead.
	if message.IsSummaryMessage {
		startedAt := time.Now()
		a.anim = anim.New(anim.Settings{
			ID:          a.ID(),
			Size:        15,
			GradColorA:  sty.WorkingGradFromColor,
			GradColorB:  sty.WorkingGradToColor,
			LabelColor:  sty.WorkingLabelColor,
			CycleColors: true,
			Suffix:      func() string { return common.FormatDuration(common.ElapsedSince(startedAt, time.Now())) },
			SuffixColor: sty.WorkingTimerColor,
		})
	} else {
		a.anim = newTurnAnim(sty, a.ID(), message.SessionID)
	}
	return a
}

// Spinning implements [Animatable].
func (a *AssistantMessageItem) Spinning() bool {
	return a.isSpinning()
}

// restyleAnim implements [animRestyler].
func (a *AssistantMessageItem) restyleAnim() {
	a.anim.SetColors(
		a.sty.ThinkingLabelColor,
		a.sty.WorkingGradFromColor,
		a.sty.WorkingGradToColor,
		a.sty.WorkingTimerColor,
	)
}

// Advance implements [Animatable].
func (a *AssistantMessageItem) Advance() bool {
	if !a.isSpinning() || !a.anim.Advance() {
		return false
	}
	// Bump F6 list-缓存 version 所以 next draw re-renders
	// 这 item: spinner frame mutates anim's internal 计数器,
	// which changes rendered output 但 是 invisible 到
	// per-section content hashes. Without bump list 缓存
	// would serve previously rendered frame indefinitely 和
	// the spinner would appear frozen.
	a.Bump()
	return true
}

// ID implements MessageItem.
func (a *AssistantMessageItem) ID() string {
	return a.message.ID
}

// RawRender implements [MessageItem].
func (a *AssistantMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	var spinner string
	if a.isSpinning() {
		spinner = a.renderSpinning()
	}

	content, height := a.renderMessageContent(cappedWidth)
	highlightedContent := a.renderHighlighted(content, cappedWidth, height)
	if spinner != "" {
		if highlightedContent != "" {
			highlightedContent += "\n\n"
		}
		return highlightedContent + spinner
	}

	return highlightedContent
}

// Render implements MessageItem.
func (a *AssistantMessageItem) Render(width int) string {
	// XXX: Here, we're manually applying the focused/blurred styles because
	// using lipgloss.Render can degrade performance 为 long messages due 到
	// it's wrapping logic.
	// We already know 那 content 是 wrapped 到 correct width 在
	// RawRender, 所以 we can just apply styles directly 到 each line.
	//
	// split + per-line prefix loop 是 O(L); 缓存 结果 keyed
	// 通过 (width, focused, sectionsFingerprint) 所以 steady-state Render
	// becomes 指针 return. sectionsFingerprint folds 在
	// per-section srcHash/extra 所以 那 any sub-缓存 change
	// invalidates 这 prefix 缓存 without requiring explicit
	// drop. Bypass 缓存 while spinning (RawRender's spinner
	// suffix changes every animation frame) 或 while highlight
	// range 是 活跃 (selection drag).
	useCache := !a.isSpinning() && !a.isHighlighted()
	cappedWidth := cappedMessageWidth(width)
	key := a.prefixCacheKey(cappedWidth)
	if useCache {
		if cached, ok := a.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	focused := a.sty.Messages.AssistantFocused.Render()
	blurred := a.sty.Messages.AssistantBlurred.Render()
	rendered := a.RawRender(width)
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		if a.focused {
			lines[i] = focused + line
		} else {
			lines[i] = blurred + line
		}
	}
	out := strings.Join(lines, "\n")
	if useCache {
		a.setCachedPrefixedRender(out, width, key)
	}
	return out
}

// prefixCacheKey builds F3 prefixed-render 缓存 key. We pack
// focus bit into bit 0 和 fingerprint 的 section caches into
// upper bits, 所以 any change 到 sub-section's source 文本 或
// extras forces prefix 缓存 到 miss without needing explicit
// drop. cappedWidth 是 included 所以 cached prefix never survives
// section-缓存 miss caused 通过 width change. finish reason 是
// folded 在 too because it controls composition 的
// renderMessageContent (e.g. appending the constant "Canceled"
// string) — 那 decision lives outside any section's own hash.
func (a *AssistantMessageItem) prefixCacheKey(cappedWidth int) uint64 {
	thinkSrc, thinkExtra := a.thinkingKey()
	contentSrc, contentExtra := a.contentKey()
	errSrc, errExtra := a.errorKey()
	h := fnv.New64a()
	var buf [8]byte
	writeU64 := func(v uint64) {
		for i := range 8 {
			buf[i] = byte(v >> (8 * i))
		}
		_, _ = h.Write(buf[:])
	}
	writeU64(uint64(cappedWidth))
	writeU64(thinkSrc)
	writeU64(thinkExtra)
	writeU64(contentSrc)
	writeU64(contentExtra)
	writeU64(errSrc)
	writeU64(errExtra)
	writeU64(a.compositionKey())
	fingerprint := h.Sum64()
	var focusBit uint64
	if a.focused {
		focusBit = 1
	}
	return (fingerprint &^ 1) | focusBit
}

// compositionKey hashes inputs 到 renderMessageContent's structural
// decisions (which sections 到 include, whether 到 append
// constant "Canceled" footer) 所以 那 flipping IsFinished 或
// finish reason invalidates prefix 缓存 even 当 no section's
// own source 文本 changed.
func (a *AssistantMessageItem) compositionKey() uint64 {
	var finishedFlag byte
	var reason string
	if a.message.IsFinished() {
		finishedFlag = 1
		reason = string(a.message.FinishReason())
	}
	// Length-prefixed framing keeps finished flag 和 reason
	// string 从 blending into one another.
	return fnvFields([]byte{finishedFlag}, []byte(reason))
}

// renderMessageContent renders 消息 content including thinking, main
// content, 和 finish reason. Each section 是 served 从 它的 own 缓存;
// 只 section whose source 文本 或 extras changed since last
// render 是 recomputed.
func (a *AssistantMessageItem) renderMessageContent(width int) (string, int) {
	var messageParts []string
	thinking := strings.TrimSpace(a.message.ReasoningContent().Thinking)
	content := strings.TrimSpace(a.message.Content().Text)

	if thinking != "" {
		messageParts = append(messageParts, a.cachedThinking(width))
	}

	if content != "" {
		if thinking != "" {
			messageParts = append(messageParts, "")
		}
		messageParts = append(messageParts, a.cachedContent(width))
	}

	if a.message.IsFinished() {
		var banner string
		switch {
		case a.message.FinishReason() == message.FinishReasonCanceled:
			// 工具 calls render below 这 item, 和 cancelling turn
			// closes each unfinished one out 用 它的 own interrupted
			// 结果. Saying it here too would put "Canceled" above
			// tools it 是 describing.
			if len(a.message.ToolCalls()) > 0 {
				break
			}
			banner = a.sty.Messages.AssistantCanceled.Render("Canceled")
		case a.message.IsErrorLike(), a.message.IsTruncated():
			// 错误, REFUSED 和 TRUNCATED all render through same
			// banner section: tag, one-line 标题, 和 details
			// finish 片段 carries (或 TUI's defaults).
			banner = a.cachedError(width)
		}
		if banner != "" {
			// Set banner off 从 文本 above it, 或 it reads 作为
			// end 的 sentence 模型 was 在 middle 的.
			if len(messageParts) > 0 {
				messageParts = append(messageParts, "")
			}
			messageParts = append(messageParts, banner)
		}
	}

	out := strings.Join(messageParts, "\n")
	return out, lipgloss.Height(out)
}

// thinkingKey returns (srcHash, extra) 缓存 key components 为
// thinking section. extra folds 在 everything other than raw
// thinking 文本 那 affects rendered output: view mode
// (collapsed / tail-窗口 / full) 和 footer state (which
// depends 在 IsThinking, ToolCalls, 和 ThinkingDuration).
//
// source hash 是 computed incrementally: during streaming
// thinking 文本 只 grows 通过 appending, 所以 we continue FNV-64a
// hash 从 saved state rather than re-hashing entire
// accumulated 文本. See CHARM-1785.
func (a *AssistantMessageItem) thinkingKey() (uint64, uint64) {
	thinking := a.message.ReasoningContent().Thinking
	srcHash := a.thinkingHashIncremental(thinking)

	showFooter := !a.message.IsThinking() || len(a.message.ToolCalls()) > 0
	var durationStr string
	if showFooter {
		duration := a.message.ThinkingDuration()
		if duration.String() != "0s" {
			durationStr = duration.String()
		}
	}
	var footer byte
	if showFooter {
		footer = 1
	}
	// Length-prefixed framing avoids any delimiter collision between
	// flag bytes 和 duration string. view mode 是 folded
	// 在 所以 那 toggling collapsed ↔ tail-窗口 ↔ full invalidates
	// 只 thinking section, 不 content/错误.
	extra := fnvFields([]byte{byte(a.thinkingViewMode), footer}, []byte(durationStr))
	return srcHash, extra
}

// thinkingHashIncremental returns FNV-64a hash 的 thinking,
// continuing 从 saved state 当 thinking 是 prefix-extension
// 的 previously hashed 文本. Falls back 到 full re-hash 当
// 文本 shrinks 或 diverges (e.g. 用户 retried turn).
func (a *AssistantMessageItem) thinkingHashIncremental(thinking string) uint64 {
	// Detect divergence: 如果 saved sample no longer matches
	// start 的 current 文本, content was rewritten (重试)
	// 和 we must re-hash 从 scratch.
	sampleLen := min(len(thinking), 64)
	if a.thinkingHashLen > 0 && len(thinking) >= a.thinkingHashLen &&
		thinking[:sampleLen] == a.thinkingHashSample {
		// Fast 路径: continue hashing 从 saved state.
		h := a.thinkingHash
		for i := a.thinkingHashLen; i < len(thinking); i++ {
			h ^= uint64(thinking[i])
			h *= 1099511628211
		}
		a.thinkingHash = h
		a.thinkingHashLen = len(thinking)
		return h
	}
	// Full re-hash (first 调用, 或 文本 diverged/shrank).
	h := fnv64(thinking)
	a.thinkingHash = h
	a.thinkingHashLen = len(thinking)
	a.thinkingHashSample = thinking[:sampleLen]
	return h
}

// contentKey returns (srcHash, extra) 缓存 key components 为
// main content section. extra folds 在 streaming-plan state 所以
// 消息 flips between plain, open-card, 和 closed-card renders
// 作为 plan 运行 progresses.
func (a *AssistantMessageItem) contentKey() (uint64, uint64) {
	var planStreaming byte
	if a.planStreaming() {
		planStreaming = 1
	}
	return fnv64(a.message.Content().Text), uint64(planStreaming)
}

// SetPlanAgent flags 这 item 作为 plan-agent output (或 clears
// flag). flag scopes plan-card rendering 到 plan mode; plan
// start marker decides which 消息 是 plan.
func (a *AssistantMessageItem) SetPlanAgent(plan bool) {
	if a.planAgent == plan {
		return
	}
	a.planAgent = plan
	a.Bump()
}

// planStreaming reports whether plan 是 still streaming into
// 这 消息: item belongs 到 plan agent 和 消息
// has 不 finished. bottom border 是 withheld until
// plan-ready marker lands.
func (a *AssistantMessageItem) planStreaming() bool {
	return a.planAgent && !a.message.IsFinished()
}

// errorKey returns (srcHash, extra) 缓存 key components 为
// 错误 / 拒绝 / truncation section. Returns (0, 0) 当 no banner
// finish 是 present 所以 缓存 stays no-op 为 normal messages.
func (a *AssistantMessageItem) errorKey() (uint64, uint64) {
	if !a.message.IsFinished() || (!a.message.IsErrorLike() && !a.message.IsTruncated()) {
		return 0, 0
	}
	finishPart := a.message.FinishPart()
	if finishPart == nil {
		return 0, 0
	}
	// Length-prefixed framing prevents 消息+Details collisions
	// between distinct (消息, Details) tuples 那 would
	// otherwise concatenate 到 same byte 序号. Fold
	// reason 在 所以 错误 vs REFUSED banners never share 缓存 slot.
	return fnvFields([]byte(finishPart.Reason), []byte(finishPart.Message), []byte(finishPart.Details)), 0
}

// cachedThinking returns rendered thinking section, computing 和
// caching it 在 miss. thinking-box height (used 为 click target
// detection) 是 preserved across hits via assistantSection.aux 所以
// cached 路径 never desyncs click detection.
func (a *AssistantMessageItem) cachedThinking(width int) string {
	srcHash, extra := a.thinkingKey()
	if a.thinkingSec.hit(width, srcHash, extra) {
		a.thinkingBoxHeight = a.thinkingSec.aux
		return a.thinkingSec.out
	}
	out := a.renderThinking(a.message.ReasoningContent().Thinking, width)
	a.thinkingSec.store(width, srcHash, extra, out, a.thinkingBoxHeight)
	return out
}

// cachedContent returns the rendered content section.
func (a *AssistantMessageItem) cachedContent(width int) string {
	srcHash, extra := a.contentKey()
	if a.contentSec.hit(width, srcHash, extra) {
		return a.contentSec.out
	}
	text := a.message.Content().Text
	// In plan mode agent ends 它的 final plan 用 sentinel marker.
	// Hide end plan marker 和 wrap 消息 在 background "card" 所以
	// plan stands out 从 regular 助手 replies. Mirrors
	// ThinkingBox treatment.
	var out string
	switch {
	case common.PlanReadyMarkerPresent(text):
		out = a.renderPlanCard(common.StripPlanMarkers(text), width)
	case a.planStreaming() && common.PlanStartMarkerPresent(text):
		// While plan streams, draw card 作为 open box: top
		// 和 side borders 只. bottom border closes once
		// plan-ready marker arrives. The plan-start marker gates the
		// card 所以 intermediate exploratory replies 在 plan mode never
		// grow a border.
		out = a.renderPlanCardStreaming(common.StripPlanMarkers(text), width)
	default:
		out = a.renderMarkdown(common.StripPlanMarkers(text), width)
	}
	a.contentSec.store(width, srcHash, extra, out, 0)
	return out
}

// renderPlanCard renders final plan 消息 作为 full-width bordered
// card. markdown 是 rendered 在 card's inner width (accounting 为
// PlanBox's horizontal frame) 用 PlanMarkdown style; PlanBox then
// draws border 和 padding 和 pads each line out 到 full width.
// plan 是 final 通过 time marker appears, 所以 这 bypasses
// streaming-markdown 缓存 和 renders directly, like renderThinking.
func (a *AssistantMessageItem) renderPlanCard(text string, width int) string {
	box, innerWidth := planBoxLayout(a.sty.Messages.PlanBox, width)
	renderer := common.PlanMarkdownRenderer(a.sty, innerWidth)
	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	rendered, err := renderer.Render(text)
	mu.Unlock()
	if err != nil {
		rendered = text
	}
	return renderPlanBox(box, rendered, width, true)
}

// renderPlanCardStreaming renders plan while it 是 still streaming
// 作为 open card: top 和 side borders 只, no bottom border. 它
// routes through stable-prefix streaming 缓存 所以 each streaming
// 刷盘 只 re-renders trailing partial 的 plan document.
func (a *AssistantMessageItem) renderPlanCardStreaming(text string, width int) string {
	box, innerWidth := planBoxLayout(a.sty.Messages.PlanBox, width)
	renderer := common.PlanMarkdownRenderer(a.sty, innerWidth)
	rendered := a.streamingPlan.Render(text, innerWidth, renderer)
	return renderPlanBox(box, rendered, width, false)
}

// renderPlanBox applies the card layout: an un-filled bordered box whose
// padding lets 终态 background show through. Only intentional
// backgrounds ( inline-code chip, H1 badge) keep color 的 their
// own; everything else renders 在 终态 background. With closed
// false bottom border 是 withheld, leaving card open while
// plan streams.
func renderPlanBox(style lipgloss.Style, content string, width int, closed bool) string {
	style, innerWidth := planBoxLayout(style, width)
	if !closed {
		style = style.BorderBottom(false)
	}
	lines := strings.Split(strings.TrimSpace(content), "\n")
	needsCellLayout := false
	for i, line := range lines {
		graphemeWidth := ansi.StringWidth(line)
		needsCellLayout = needsCellLayout || graphemeWidth != ansi.WcWidth.StringWidth(line)
		if graphemeWidth > innerWidth {
			// Keep existing clipping 的 intentionally overlong lines.
			lines[i] = ansi.WcWidth.Truncate(line, innerWidth, "")
		}
	}
	content = strings.Join(lines, "\n")
	if !needsCellLayout {
		// In lipgloss v2 Width 是 total box width: border 和 padding live
		// inside it, 所以 content area ends up Width minus frame. Glamour
		// already wrapped 在 innerWidth (width minus frame), 所以 size box
		// 到 full width 或 every line gets re-wrapped narrower.
		return style.Width(max(1, width)).Render(content)
	}

	// Lay 文本 out 在 cells 用 UI's widths, re-wrap lines 那
	// no longer fit, 和 splice rows into empty frame 所以 lipgloss
	// never measures them 通过 grapheme width.
	canvasWidth := 1
	for _, line := range lines {
		canvasWidth = max(canvasWidth, ansi.WcWidth.StringWidth(line))
	}
	canvas := uv.NewScreenBuffer(canvasWidth, len(lines))
	uv.NewStyledString(content).Draw(canvas, canvas.Bounds())
	var rows []uv.Line
	for _, line := range canvas.Lines {
		rows = append(rows, wrapPlanLine(line, innerWidth)...)
	}
	frame := style.Width(max(1, width)).Render(strings.Repeat("\n", len(rows)-1))
	frameLines := strings.Split(frame, "\n")
	frameWidth := lipgloss.Width(frame)
	left := style.GetBorderLeftSize() + style.GetPaddingLeft()
	top := style.GetBorderTopSize() + style.GetPaddingTop()
	for i, row := range rows {
		rowWidth := 0
		for j := range row {
			rowWidth += row[j].Width
			if row[j].Width > 0 && row[j].Style.Fg == nil {
				row[j].Style.Fg = style.GetForeground()
			}
		}
		frameRow := frameLines[top+i]
		frameLines[top+i] = ansi.Cut(frameRow, 0, left) + row.Render() +
			strings.Repeat(" ", innerWidth-rowWidth) +
			ansi.Cut(frameRow, left+innerWidth, frameWidth)
	}
	return strings.Join(frameLines, "\n")
}

// wrapPlanLine fits line 的 cells 到 width way glamour wraps: it
// breaks 在 last space 那 fits, 和 starts each added 行 用
// line's indent (leading spaces 和 blockquote bars), 所以 list items 和
// quotes stay aligned. Padding past edge 是 dropped, 和 所以 是
// cluster too wide 为 any 行, without losing 文本 after it.
func wrapPlanLine(line uv.Line, width int) []uv.Line {
	lineWidth := 0
	for _, c := range line {
		lineWidth += c.Width
	}
	for lineWidth > width && len(line) > 0 && line[len(line)-1].Content == " " {
		line = line[:len(line)-1]
		lineWidth--
	}
	if lineWidth <= width {
		return []uv.Line{line}
	}

	indent := 0
	for indent < len(line) && (line[indent].Content == " " || line[indent].Content == "│") {
		indent++
	}
	var rows []uv.Line
	row := append(uv.Line(nil), line[:indent]...)
	rowWidth := indent
	// last space 在 行, 和 行's width up 到 it.
	space, spaceWidth := -1, 0
	for _, c := range line[indent:] {
		if c.Width > width-indent {
			continue
		}
		for rowWidth+c.Width > width {
			next := append(uv.Line(nil), line[:indent]...)
			nextWidth := indent
			if space >= 0 {
				next = append(next, row[space+1:]...)
				nextWidth += rowWidth - spaceWidth - 1
				row = row[:space]
			}
			rows = append(rows, row)
			row, rowWidth, space = next, nextWidth, -1
		}
		if c.Content == " " {
			space, spaceWidth = len(row), rowWidth
		}
		row = append(row, c)
		rowWidth += c.Width
	}
	return append(rows, row)
}

// planBoxLayout returns style 和 content width whose combined horizontal
// frame fits within available 消息 width.
func planBoxLayout(style lipgloss.Style, width int) (lipgloss.Style, int) {
	width = max(1, width)
	frameWidth := style.GetHorizontalFrameSize()
	if frameWidth >= width {
		style = style.PaddingLeft(0).PaddingRight(0)
		frameWidth = style.GetHorizontalFrameSize()
	}
	return style, max(1, width-frameWidth)
}

// cachedError returns rendered 错误 section.
func (a *AssistantMessageItem) cachedError(width int) string {
	srcHash, extra := a.errorKey()
	if a.errorSec.hit(width, srcHash, extra) {
		return a.errorSec.out
	}
	out := a.renderError(width)
	a.errorSec.store(width, srcHash, extra, out, 0)
	return out
}

// renderThinking renders thinking/reasoning content 用 footer.
//
// Slicing happens AFTER glamour rendering 所以 fenced code blocks, list
// continuations, 和 tables 是 不 split mid-block — same
// boundary problem §4.4 的 design note flags. bordered
// ThinkingBox style 是 applied 在 top 的 (already-windowed)
// lines 所以 visual box matches what 用户 sees today.
func (a *AssistantMessageItem) renderThinking(thinking string, width int) string {
	renderer := common.QuietMarkdownRenderer(a.sty, width)
	rendered := a.streamingThinking.Render(thinking, width, renderer)
	rendered = strings.TrimSpace(rendered)
	// renderer already knows 这 count 从 它的 cached prefix, 所以
	// take it rather than rescanning document 那 只 grows.
	renderedLines := a.streamingThinking.LastLines()

	// Count lines 和, 为 windowed view modes, slice tail
	// WITHOUT splitting the entire rendered document. Splitting a
	// 1200-line render just 到 keep last 10 lines 是 O(n) per
	// tick; tailLines finds cut point 用 bounded backward
	// scan. See CHARM-1785.
	var lines []string
	var totalLines int
	switch a.thinkingViewMode {
	case thinkingCollapsed:
		totalLines = renderedLines
		// Avoid hiding a single line; showing it beats the hint.
		if totalLines > maxCollapsedThinkingHeight+1 {
			tail, hidden := tailLines(rendered, maxCollapsedThinkingHeight, totalLines)
			hint := a.sty.Messages.ThinkingTruncationHint.Render(
				fmt.Sprintf(assistantMessageTruncateFormat, hidden),
			)
			lines = append([]string{hint, ""}, strings.Split(tail, "\n")...)
		} else {
			lines = strings.Split(rendered, "\n")
		}
	case thinkingTailWindow:
		totalLines = renderedLines
		if totalLines > maxExpandedThinkingTailLines+1 {
			tail, hidden := tailLines(rendered, maxExpandedThinkingTailLines, totalLines)
			hint := a.sty.Messages.ThinkingTruncationHint.Render(
				fmt.Sprintf(assistantMessageTailWindowFormat, hidden),
			)
			lines = append([]string{hint, ""}, strings.Split(tail, "\n")...)
		} else {
			lines = strings.Split(rendered, "\n")
		}
	default:
		lines = strings.Split(rendered, "\n")
	}

	thinkingStyle := a.sty.Messages.ThinkingBox.Width(width)
	result := thinkingStyle.Render(strings.Join(lines, "\n"))
	a.thinkingBoxHeight = lipgloss.Height(result)

	var footer string
	// 如果 thinking 是 done add thought 为 footer
	if !a.message.IsThinking() || len(a.message.ToolCalls()) > 0 {
		duration := a.message.ThinkingDuration()
		if duration.String() != "0s" {
			footer = a.sty.Messages.ThinkingFooterTitle.Render("Thought for ") +
				a.sty.Messages.ThinkingFooterDuration.Render(duration.String())
		}
	}

	if footer != "" {
		result += "\n\n" + footer
	}

	return result
}

// renderMarkdown renders content 作为 markdown. F8 routes 调用
// through streamingContent, which caches glamour render 的
// "stable prefix" 所以 each streaming 刷盘 只 re-renders
// trailing partial. streaming 缓存 invalidates itself 在
// width change 和 在 any content 那 是 不 prefix-extension
// 的 previously rendered content (e.g. 用户 retried
// turn), 和 falls back 到 full render whenever boundary
// detection has the slightest doubt — see
// findSafeMarkdownBoundary.
func (a *AssistantMessageItem) renderMarkdown(content string, width int) string {
	renderer := common.MarkdownRenderer(a.sty, width)
	return a.streamingContent.Render(content, width, renderer)
}

func (a *AssistantMessageItem) renderSpinning() string {
	if a.message.IsThinking() {
		a.anim.SetLabel("Thinking")
	} else if a.message.IsSummaryMessage {
		a.anim.SetLabel("Summarizing")
	}
	return a.anim.Render()
}

// renderError renders 错误, 提供方-拒绝, 或 truncation banner.
func (a *AssistantMessageItem) renderError(width int) string {
	finishPart := a.message.FinishPart()
	tagLabel := "ERROR"
	titleText := finishPart.Message
	detailsText := finishPart.Details
	switch finishPart.Reason {
	case message.FinishReasonContentFilter:
		tagLabel = refusalTagLabel
		titleText = cmp.Or(titleText, refusalTitle)
		detailsText = cmp.Or(detailsText, refusalDetails)
	case message.FinishReasonMaxTokens:
		tagLabel = truncatedTagLabel
		titleText = cmp.Or(titleText, truncatedTitle)
		detailsText = cmp.Or(detailsText, truncatedDetails)
	}
	errTag := a.sty.Messages.ErrorTag.Render(tagLabel)
	truncated := ansi.Truncate(titleText, width-2-lipgloss.Width(errTag), "...")
	title := fmt.Sprintf("%s %s", errTag, a.sty.Messages.ErrorTitle.Render(truncated))
	if detailsText == "" {
		return title
	}
	details := a.sty.Messages.ErrorDetails.Width(width - 2).Render(detailsText)
	return fmt.Sprintf("%s\n\n%s", title, details)
}

// isSpinning returns true 如果 助手消息 是 still generating.
func (a *AssistantMessageItem) isSpinning() bool {
	isThinking := a.message.IsThinking()
	isFinished := a.message.IsFinished()
	hasContent := strings.TrimSpace(a.message.Content().Text) != ""
	hasToolCalls := len(a.message.ToolCalls()) > 0
	return (isThinking || !isFinished) && !hasContent && !hasToolCalls
}

// SetMessage 是 used 到 更新 underlying 消息. Only
// sub-section caches whose source 文本 或 extras changed 是
// invalidated; others survive 和 serve 缓存 hits 在 next
// RawRender.
func (a *AssistantMessageItem) SetMessage(msg *message.Message) {
	a.message = msg
	// Bump F6 version even 如果 underlying *消息.消息
	// 指针 是 identical: callers may have mutated 消息 在
	// place (delta append) 和 we cannot tell 从 here.
	// per-section caches dedupe identical content via FNV-64 hashes,
	// 所以 redundant bump 只 costs one list-缓存 repopulation.
	a.Bump()
	// prefix 缓存 是 keyed 通过 fingerprint 那 includes every
	// section's source hash, 所以 unchanged section keeps 它的 prefix
	// 缓存 valid while changed section forces miss naturally.
	// Section caches themselves 是 content-keyed, 所以 they do 不
	// need explicit drop here either. If 消息 started
	// spinning UI's animation clock picks it up 在 next
	// 更新.
}

// clearCache drops every cached render 为 这 item, including
// per-section caches. Shadows the embedded cachedMessageItem.clearCache
// 所以 ClearItemCache (style change) wipes section caches too.
// F8: also drop streaming-markdown stable-prefix 缓存 because
// the cached glamour render embeds the OLD style's ANSI sequences
// 和 是 no longer visually consistent 用 new style.
func (a *AssistantMessageItem) clearCache() {
	a.cachedMessageItem.clearCache()
	a.thinkingSec.reset()
	a.contentSec.reset()
	a.errorSec.reset()
	a.streamingContent.Reset()
	a.streamingThinking.Reset()
	a.streamingPlan.Reset()
	a.thinkingHash = 0
	a.thinkingHashLen = 0
	a.thinkingHashSample = ""
}

// ToggleExpanded advances F5 thinking view-mode cycle 和 returns
// whether item 是 now 在 any expanded state (tail-窗口 或 full).
// cycle 是 collapsed → tail-窗口 → full → collapsed, 用
// tail-窗口 step skipped 当 rendered thinking fits within
// maxExpandedThinkingTailLines 所以 short blocks remain two-click
// toggle. Both thinking section 缓存 和 F3 prefix 缓存
// fold thinkingViewMode into their keys, 所以 no explicit invalidation
// 是 required here.
//
// When 消息 carries no thinking 文本 toggle 是 no-op:
// there 是 nothing 到 expand, 和 mutating view mode would
// thrash thinking-section 缓存 key 为 no visible benefit.
func (a *AssistantMessageItem) ToggleExpanded() bool {
	if strings.TrimSpace(a.message.ReasoningContent().Thinking) == "" {
		return a.thinkingViewMode != thinkingCollapsed
	}
	switch a.thinkingViewMode {
	case thinkingCollapsed:
		if a.tailWindowWouldTruncate() {
			a.thinkingViewMode = thinkingTailWindow
		} else {
			a.thinkingViewMode = thinkingFullExpanded
		}
	case thinkingTailWindow:
		a.thinkingViewMode = thinkingFullExpanded
	case thinkingFullExpanded:
		a.thinkingViewMode = thinkingCollapsed
	}
	// View-mode changes alter the windowing slice applied after
	// glamour render. streaming prefix 缓存 may have been
	// seeded under different slice regime, 和 glued renders 是
	// 不 byte-identical 到 monolithic ones. Drop prefix 缓存
	// 所以 next render 是 clean.
	a.streamingThinking.Reset()
	a.Bump()
	return a.thinkingViewMode != thinkingCollapsed
}

// tailWindowWouldTruncate reports whether current thinking 文本
// 是 long enough 那 tail-窗口 step 是 worth inserting into
// toggle cycle. We use cheap source-文本 logical-line count
// 作为 heuristic rather than peeking into 缓存: 缓存
// may 是 populated 在 collapsed state (where 它的 height 是 bounded
// 通过 maxCollapsedThinkingHeight 和 tells us nothing about
// underlying length), 和 re-running glamour just 到 count lines
// would defeat 缓存. heuristic can over-trigger ( source
// 用 many short lines may wrap 到 fewer than N lines), 在 which
// case tail-窗口 render 是 visually identical 到 full 和
// cycle costs 用户 one extra toggle — preferred over
// alternative 的 failing 到 show affordance 在 genuinely
// long block.
//
// Logical line count 是 `1 + newlineCount` ( string 用 no
// newlines 是 one line). Comparing newline count alone introduced
// off-通过-one 那 let source whose post-newline-split length
// equalled cap skip tail-窗口 step.
func (a *AssistantMessageItem) tailWindowWouldTruncate() bool {
	lineCount := 1 + strings.Count(a.message.ReasoningContent().Thinking, "\n")
	return lineCount > maxExpandedThinkingTailLines
}

// HandleMouseClick implements MouseClickable. 它 signals (via true return)
// 那 click lies 在 thinking box 所以 caller can invoke
// [AssistantMessageItem.ToggleExpanded] through the generic [Expandable]
// 路径. Toggling here directly would double-toggle because caller always
// runs generic 路径 after handled click.
func (a *AssistantMessageItem) HandleMouseClick(btn ansi.MouseButton, x, y int) bool {
	if btn != ansi.MouseLeft {
		return false
	}
	// Only thinking box 是 clickable; other regions 的 助手
	// 消息 should 不 trigger expansion.
	return a.thinkingBoxHeight > 0 && y < a.thinkingBoxHeight
}

// HandleKeyEvent implements KeyEventHandler.
func (a *AssistantMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if k := key.String(); k == "c" || k == "y" {
		text := a.message.Content().Text
		return true, common.CopyToClipboard(text, "Message copied to clipboard")
	}
	return false, nil
}
