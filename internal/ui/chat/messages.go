// messages.go —— UI 消息渲染：把后端来的消息/工具调用转成 TUI 可显示的项。
// 属于产物链的末端：db/message → Workspace.ListMessages → ui/chat → 屏幕。

package chat

import (
	"cmp"
	"encoding/xml"
	"fmt"
	"image"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// MessageLeftPaddingTotal 是 total width 那 是 taken up 通过 border +
// padding. We also cap width 所以 文本 是 readable 到 maxTextWidth(120).
const MessageLeftPaddingTotal = 2

// maxTextWidth 是 maximum width 文本 messages can 是
const maxTextWidth = 120

// Identifiable 是 interface 为 items 那 can provide unique identifier.
type Identifiable interface {
	ID() string
}

// Animatable 是 interface 为 items 那 support animation. Items do
// 不 schedule their own frames: UI runs single animation clock 和
// calls Advance 在 every visible item 那 reports Spinning.
type Animatable interface {
	// Spinning reports whether the item currently shows a running
	// animation 和 therefore needs clock ticks.
	Spinning() bool
	// Advance moves animation forward 通过 one frame 和 reports
	// whether rendered output changed. Implementations 必须 bump
	// their version 当 it did 所以 list 缓存 re-renders item.
	Advance() bool
}

// Expandable 是 interface 为 items 那 can 是 expanded 或 collapsed.
type Expandable interface {
	// ToggleExpanded toggles expanded state 的 item. 它 returns
	// whether item 是 now expanded.
	ToggleExpanded() bool
}

// KeyEventHandler 是 interface 为 items 那 can handle key events.
type KeyEventHandler interface {
	HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd)
}

// MessageItem represents [消息.消息] item 那 can 是 displayed 在
// UI 和 是 片段 的 [list.List] identifiable 通过 unique ID.
type MessageItem interface {
	list.Item
	list.RawRenderable
	Identifiable
}

// HighlightableMessageItem 是 消息 item 那 supports highlighting.
type HighlightableMessageItem interface {
	MessageItem
	list.Highlightable
}

// FocusableMessageItem 是 消息 item 那 supports focus.
type FocusableMessageItem interface {
	MessageItem
	list.Focusable
}

// SendMsg represents 消息 到 send chat 消息.
type SendMsg struct {
	Text        string
	Attachments []message.Attachment
}

type highlightableMessageItem struct {
	// version 是 parent item's version 计数器. SetHighlight
	// bumps it 在 every observable change 所以 F6 list memo 是
	// invalidated 当 selection drag enters 或 leaves item.
	version *list.Versioned

	startLine int
	startCol  int
	endLine   int
	endCol    int
	// sty supplies selection style. 它 是 读取 当 highlighting, 不
	// copied 当 item 是 built, 所以 theme switch recolors selections
	// 在 existing items too.
	sty *styles.Styles
}

var _ list.Highlightable = (*highlightableMessageItem)(nil)

// isHighlighted returns true 如果 item has highlight range set.
func (h *highlightableMessageItem) isHighlighted() bool {
	return h.startLine != -1 || h.endLine != -1
}

// renderHighlighted highlights content 如果 necessary.
func (h *highlightableMessageItem) renderHighlighted(content string, width, height int) string {
	if !h.isHighlighted() {
		return content
	}
	area := image.Rect(0, 0, width, height)
	return list.Highlight(content, area, h.startLine, h.startCol, h.endLine, h.endCol, list.ToHighlighter(h.sty.TextSelection))
}

// SetHighlight implements list.Highlightable.
func (h *highlightableMessageItem) SetHighlight(startLine int, startCol int, endLine int, endCol int) {
	// Adjust columns 为 style's left inset (border + padding) since we
	// highlight content 只.
	offset := MessageLeftPaddingTotal
	newStartCol := max(0, startCol-offset)
	newEndCol := endCol
	if endCol >= 0 {
		newEndCol = max(0, endCol-offset)
	}
	if h.startLine == startLine && h.startCol == newStartCol && h.endLine == endLine && h.endCol == newEndCol {
		return
	}
	h.startLine = startLine
	h.startCol = newStartCol
	h.endLine = endLine
	h.endCol = newEndCol
	if h.version != nil {
		h.version.Bump()
	}
}

// Highlight implements list.Highlightable.
func (h *highlightableMessageItem) Highlight() (startLine int, startCol int, endLine int, endCol int) {
	return h.startLine, h.startCol, h.endLine, h.endCol
}

func defaultHighlighter(sty *styles.Styles, v *list.Versioned) *highlightableMessageItem {
	return &highlightableMessageItem{
		version:   v,
		startLine: -1,
		startCol:  -1,
		endLine:   -1,
		endCol:    -1,
		sty:       sty,
	}
}

// cacheClearable 是 implemented 通过 消息 items 那 缓存 rendered
// output 和 can 是 asked 到 drop 缓存.
type cacheClearable interface {
	clearCache()
}

// animRestyler 是 implemented 通过 消息 items 那 own animated
// spinner whose colors 是 baked into pre-rendered frames 和 therefore
// need rebuilding 当 theme changes.
type animRestyler interface {
	restyleAnim()
}

// ClearItemCache drops item's cached rendered output, 和 那 的 any
// nested tools, 所以 next render uses current styles.
func ClearItemCache(item MessageItem) {
	if cc, ok := item.(cacheClearable); ok {
		cc.clearCache()
	}
	if ar, ok := item.(animRestyler); ok {
		ar.restyleAnim()
	}
	if c, ok := item.(NestedToolContainer); ok {
		for _, nested := range c.NestedTools() {
			if cc, ok := nested.(cacheClearable); ok {
				cc.clearCache()
			}
			if v, ok := nested.(interface{ Bump() }); ok {
				v.Bump()
			}
		}
	}
	if v, ok := item.(interface{ Bump() }); ok {
		v.Bump()
	}
}

// cachedMessageItem caches rendered 消息 content 到 avoid re-rendering.
//
// 这 should 是 used 通过 any 消息 那 can 存储 cached version 的 它的 render. e.x 用户,助手... 和 所以 在
//
// THOUGHT(kujtim): we should consider 如果 它的 efficient 到 存储 render 为 different widths
// issue 用 那 could 是 memory usage
type cachedMessageItem struct {
	// rendered 是 cached rendered string
	rendered string
	// width 和 height 是 dimensions 的 cached render
	width  int
	height int

	// prefixedRendered caches the per-line-prefixed Render output (the
	// 结果 的 splitting RawRender 通过 newlines 和 prepending focus
	// 或 selection prefix 到 every line). Items rebuild 这 every
	// frame today; caching it keyed 通过 (prefixedWidth, prefixedKey)
	// turns Render into 指针 return 当 item state 是 stable.
	//
	// Invalidation lives 在 clearCache; callers must additionally
	// bypass 这 缓存 whenever prefixed output would 不 是
	// stable (spinner ticks, 活跃 highlight ranges) 通过 不 calling
	// setCachedPrefixedRender 为 those frames.
	prefixedRendered string
	prefixedWidth    int
	prefixedKey      uint64
}

// getCachedRender returns cached render 如果 it exists 为 given width.
func (c *cachedMessageItem) getCachedRender(width int) (string, int, bool) {
	if c.width == width && c.rendered != "" {
		return c.rendered, c.height, true
	}
	return "", 0, false
}

// setCachedRender sets the cached render.
func (c *cachedMessageItem) setCachedRender(rendered string, width, height int) {
	c.rendered = rendered
	c.width = width
	c.height = height
}

// getCachedPrefixedRender returns cached prefixed render 如果 it exists
// 为 given (width, key). key encodes any state 那 changes
// per-line prefix (focused/blurred, compact, ...).
func (c *cachedMessageItem) getCachedPrefixedRender(width int, key uint64) (string, bool) {
	if c.prefixedRendered != "" && c.prefixedWidth == width && c.prefixedKey == key {
		return c.prefixedRendered, true
	}
	return "", false
}

// setCachedPrefixedRender stores the cached prefixed render.
func (c *cachedMessageItem) setCachedPrefixedRender(rendered string, width int, key uint64) {
	c.prefixedRendered = rendered
	c.prefixedWidth = width
	c.prefixedKey = key
}

// clearCache clears the cached render.
func (c *cachedMessageItem) clearCache() {
	c.rendered = ""
	c.width = 0
	c.height = 0
	c.prefixedRendered = ""
	c.prefixedWidth = 0
	c.prefixedKey = 0
}

// focusableMessageItem 是 base struct 为 消息 items 那 can 是 focused.
type focusableMessageItem struct {
	// version 是 parent item's version 计数器. SetFocused
	// bumps it whenever focus actually flips 所以 F6 list memo
	// invalidates the per-line focus prefix.
	version *list.Versioned
	focused bool
}

// newFocusableMessageItem returns focusableMessageItem wired 到
// shared version 计数器.
func newFocusableMessageItem(v *list.Versioned) *focusableMessageItem {
	return &focusableMessageItem{version: v}
}

// SetFocused implements MessageItem.
func (f *focusableMessageItem) SetFocused(focused bool) {
	if f.focused == focused {
		return
	}
	f.focused = focused
	if f.version != nil {
		f.version.Bump()
	}
}

// AssistantInfoID returns stable ID 为 助手 info items.
func AssistantInfoID(messageID string) string {
	return fmt.Sprintf("%s:assistant-info", messageID)
}

// ShouldShowAssistantInfo reports whether 助手消息 should
// render 它的 info footer. turn 那 ends 提示词 always gets one;
// intermediate turns 只 get one 当 Prism-routed 模型 name 是
// available, since 那 是 只 case where footer adds
// per-turn information. 一个 truncated turn always gets one: it 是
// final turn 通过 definition ( 模型 cannot continue past token
// limit), 和 without footer it would render 用 compact
// intermediate-step header, reading 作为 step 的 loop still running.
func ShouldShowAssistantInfo(msg *message.Message) bool {
	finishData := msg.FinishPart()
	if finishData == nil {
		return false
	}
	return finishData.Reason == message.FinishReasonEndTurn ||
		finishData.Reason == message.FinishReasonMaxTokens ||
		msg.PrismModelName != ""
}

// AssistantInfoItem renders 模型 info 和 响应 time after 助手 completes.
type AssistantInfoItem struct {
	*list.Versioned
	*cachedMessageItem

	id                  string
	message             *message.Message
	sty                 *styles.Styles
	cfg                 *config.Config
	lastUserMessageTime time.Time
}

// NewAssistantInfoItem creates a new AssistantInfoItem.
func NewAssistantInfoItem(sty *styles.Styles, message *message.Message, cfg *config.Config, lastUserMessageTime time.Time) MessageItem {
	return &AssistantInfoItem{
		Versioned:           list.NewVersioned(),
		cachedMessageItem:   &cachedMessageItem{},
		id:                  AssistantInfoID(message.ID),
		message:             message,
		sty:                 sty,
		cfg:                 cfg,
		lastUserMessageTime: lastUserMessageTime,
	}
}

// ID implements MessageItem.
func (a *AssistantInfoItem) ID() string {
	return a.id
}

// RawRender implements MessageItem.
func (a *AssistantInfoItem) RawRender(width int) string {
	innerWidth := max(0, width-MessageLeftPaddingTotal)
	content, _, ok := a.getCachedRender(innerWidth)
	if !ok {
		content = a.renderContent(innerWidth)
		height := lipgloss.Height(content)
		a.setCachedRender(content, innerWidth, height)
	}
	return content
}

// Render implements MessageItem.
func (a *AssistantInfoItem) Render(width int) string {
	// AssistantInfoItem uses a single, state-independent prefix; key 0
	// 是 sufficient. 缓存 是 invalidated whenever underlying
	// cachedMessageItem render 是 cleared.
	if cached, ok := a.getCachedPrefixedRender(width, 0); ok {
		return cached
	}
	prefix := a.sty.Messages.SectionHeader.Render()
	lines := strings.Split(a.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	a.setCachedPrefixedRender(out, width, 0)
	return out
}

// modelDisplayName resolves 模型 name shown 在 助手 info
// footer. 它 prefers current 配置 条目 (所以 renamed 模型 shows
// 它的 fresh name), then name persisted 用 消息 (所以
// renamed 或 removed 模型 still shows friendly name 为 old turns),
// then 模型 ID, which 是 always identifiable. "Unknown 模型" 是
// 只 used 当 消息 carries no 模型 在 all.
func (a *AssistantInfoItem) modelDisplayName() string {
	if model := a.cfg.GetModel(a.message.Provider, a.message.Model); model != nil && model.Name != "" {
		return model.Name
	}
	if a.message.ModelName != "" {
		return a.message.ModelName
	}
	if a.message.Model != "" {
		return a.message.Model
	}
	return "Unknown Model"
}

func (a *AssistantInfoItem) renderContent(width int) string {
	finishData := a.message.FinishPart()
	if finishData == nil {
		return ""
	}
	// final turn 的 提示词 keeps full footer (duration 和
	// separator line); intermediate turns render compact header. 一个
	// truncated turn never gets follow-up step ( 模型 cannot
	// continue past token limit), 所以 it renders full footer
	// too: otherwise compact header reads 作为 just another step 的
	// loop 那 是 still running, 和 TRUNCATED banner 是 只
	// hint turn 是 over.
	isFinalTurn := finishData.Reason == message.FinishReasonEndTurn ||
		finishData.Reason == message.FinishReasonMaxTokens

	icon := a.sty.Messages.AssistantInfoIcon.Render(styles.ModelIcon)
	mainModelName := a.modelDisplayName()
	modelFormatted := a.sty.Messages.AssistantInfoModel.Render(mainModelName)
	// 一个 Prism-routed turn shows 模型 那 actually served
	// 请求, 用 arrow 和 any savings suffix subdued.
	if a.message.PrismModelName != "" {
		routedModel := a.sty.Messages.AssistantInfoModel.Render(a.message.PrismModelName)
		arrow := a.sty.Messages.AssistantInfoProvider.Render("→")
		modelFormatted = fmt.Sprintf("%s %s %s", modelFormatted, arrow, routedModel)
	}
	savings := prismSavingsSuffix(a.sty, a.message)
	if !isFinalTurn {
		if savings != "" {
			return fmt.Sprintf("%s %s %s", icon, modelFormatted, savings)
		}
		return fmt.Sprintf("%s %s", icon, modelFormatted)
	}
	providerName := a.message.Provider
	if providerConfig, ok := a.cfg.Providers.Get(a.message.Provider); ok {
		providerName = providerConfig.Name
	}
	provider := a.sty.Messages.AssistantInfoProvider.Render(fmt.Sprintf("via %s", providerName))
	duration := time.Unix(finishData.Time, 0).Sub(a.lastUserMessageTime)
	infoMsg := a.sty.Messages.AssistantInfoDuration.Render(fmt.Sprintf("in %s", duration))
	assistant := fmt.Sprintf("%s %s %s %s", icon, modelFormatted, provider, infoMsg)
	if savings != "" {
		assistant = fmt.Sprintf("%s %s", assistant, savings)
	}
	return common.Section(a.sty, assistant, width)
}

// ChannelInfoID returns stable ID 为 渠道-info items.
func ChannelInfoID(messageID string) string {
	return fmt.Sprintf("%s:channel-info", messageID)
}

// ChannelInfoItem renders sender/渠道/timestamp metadata 为
// 渠道-originated 用户消息. 它 是 emitted 作为 separate list item
// below 消息 body, mirroring how AssistantInfoItem appears below
// 助手 messages.
type ChannelInfoItem struct {
	*list.Versioned
	*cachedMessageItem

	id      string
	message *message.Message
	sty     *styles.Styles
}

// NewChannelInfoItem creates a new ChannelInfoItem.
func NewChannelInfoItem(sty *styles.Styles, msg *message.Message) MessageItem {
	return &ChannelInfoItem{
		Versioned:         list.NewVersioned(),
		cachedMessageItem: &cachedMessageItem{},
		id:                ChannelInfoID(msg.ID),
		message:           msg,
		sty:               sty,
	}
}

// Finished implements list.Item. 渠道 metadata 是 immutable once
// rendered, 所以 条目 是 always safe 到 freeze.
func (c *ChannelInfoItem) Finished() bool {
	return true
}

// ID implements MessageItem.
func (c *ChannelInfoItem) ID() string {
	return c.id
}

// RawRender implements MessageItem.
func (c *ChannelInfoItem) RawRender(width int) string {
	innerWidth := max(0, width-MessageLeftPaddingTotal)
	content, _, ok := c.getCachedRender(innerWidth)
	if !ok {
		content = c.renderContent(innerWidth)
		height := lipgloss.Height(content)
		c.setCachedRender(content, innerWidth, height)
	}
	return content
}

// Render implements MessageItem.
func (c *ChannelInfoItem) Render(width int) string {
	if cached, ok := c.getCachedPrefixedRender(width, 0); ok {
		return cached
	}
	prefix := c.sty.Messages.SectionHeader.Render()
	lines := strings.Split(c.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	c.setCachedPrefixedRender(out, width, 0)
	return out
}

func (c *ChannelInfoItem) renderContent(width int) string {
	info, ok := parseChannelInfo(c.message)
	if !ok {
		return ""
	}

	parts := make([]string, 0, 3)
	if info.sender != "" {
		parts = append(parts, c.sty.Messages.ChannelInfoSender.Render(info.sender))
	}
	if info.source != "" {
		parts = append(parts, c.sty.Messages.ChannelInfoProvider.Render(fmt.Sprintf("via %s", info.source)))
	}
	if info.time != "" {
		parts = append(parts, c.sty.Messages.ChannelInfoTimestamp.Render(fmt.Sprintf("at %s", info.time)))
	}

	icon := c.sty.Messages.ChannelInfoIcon.Render(styles.ChannelIcon)
	metaStr := fmt.Sprintf("%s %s", icon, strings.Join(parts, " "))
	return common.Section(c.sty, metaStr, width)
}

// channelInfo 是 metadata shown 在 渠道 消息's info line.
type channelInfo struct {
	sender string
	source string
	time   string
}

// parseChannelInfo extracts info-line metadata 从 渠道-originated
// 消息. ok 是 false 当 content 是 不 well-formed <渠道>
// element 或 carries nothing 到 show, 在 which case no info line 是 added.
func parseChannelInfo(msg *message.Message) (channelInfo, bool) {
	var ch channelMessage
	if err := xml.Unmarshal([]byte(strings.TrimSpace(msg.Content().Text)), &ch); err != nil {
		return channelInfo{}, false
	}
	info := channelInfo{
		sender: cmp.Or(ch.SenderName, ch.Sender),
		source: ch.Source,
		time:   formatChannelTime(ch.Time, msg.CreatedAt),
	}
	return info, info != channelInfo{}
}

// formatChannelTime 渲染 push's time 属性 way CreatedAt
// fallback 是 rendered: local HH:MM:SS. attribute 是 whatever 服务端
// put 在 它的 meta, 所以 RFC 3339 timestamps 和 Unix epochs (seconds 或
// milliseconds) 是 reformatted, 和 anything else 是 shown 作为 sent rather
// than dropped.
func formatChannelTime(raw string, createdAt int64) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if createdAt > 0 {
			return time.Unix(createdAt, 0).Format(time.TimeOnly)
		}
		return ""
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.Local().Format(time.TimeOnly)
	}
	// Only values 在 epoch range, 所以 short number such 作为 "1430" 是 不
	// mistaken 为 1970 timestamp.
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		switch {
		case n >= 1e12:
			return time.UnixMilli(n).Format(time.TimeOnly)
		case n >= 1e9:
			return time.Unix(n, 0).Format(time.TimeOnly)
		}
	}
	return raw
}

// IsChannelMessage reports whether given 消息 content 是
// 渠道-originated 消息 (i.e. starts 用 <渠道 XML tag).
func IsChannelMessage(msg *message.Message) bool {
	return strings.HasPrefix(strings.TrimSpace(msg.Content().Text), "<channel")
}

// cappedMessageWidth returns maximum width 为 消息 content 为 readability.
func cappedMessageWidth(availableWidth int) int {
	return min(availableWidth-MessageLeftPaddingTotal, maxTextWidth)
}

// prismSavingsSuffix returns styled Prism savings suffix 为
// 消息, 或 empty string 当 none was reported. hypercredit
// symbol carries sidebar's hypercredit color; rest 是 subdued
// like 提供方. Hypercredits 是 preferred over dollars 当 both
// 是 present, matching Hyper's either/或 credit 模型.
func prismSavingsSuffix(sty *styles.Styles, msg *message.Message) string {
	switch {
	case msg.PrismHypercreditSavings != nil:
		icon := sty.Messages.SubduedHypercreditIcon.Render(styles.HypercreditIcon)
		rest := sty.Messages.AssistantInfoProvider.Render(fmt.Sprintf(" %s Saved", formatHypercreditSavings(*msg.PrismHypercreditSavings)))
		return icon + rest
	case msg.PrismDollarSavings != nil:
		return sty.Messages.AssistantInfoProvider.Render(fmt.Sprintf("• $%.2f Saved", *msg.PrismDollarSavings))
	default:
		return ""
	}
}

// formatHypercreditSavings rounds hypercredits 到 whole numbers 在 1 和
// above, keeping single decimal below 那.
func formatHypercreditSavings(v float64) string {
	if v >= 1 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// ExtractMessageItems extracts [MessageItem]s 从 [消息.消息]. 它
// returns all parts 的 消息 作为 [MessageItem]s.
//
// For 助手 messages 用 工具 calls, pass toolResults map 到 link results.
// Use BuildToolResultMap 到 创建 这 map 从 all messages 在 会话.
func ExtractMessageItems(sty *styles.Styles, msg *message.Message, toolResults map[string]message.ToolResult, workingDir string) []MessageItem {
	switch msg.Role {
	case message.User:
		if msg.Content().Hidden {
			return nil
		}
		// Reconstruct shell command 和 子代理 报告 items 从 their
		// parts. Both 是 carried 在 用户-角色 messages 但 neither 是
		// something 用户 typed, 所以 they render 作为 their own items.
		var items []MessageItem
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case message.ShellCommand:
				items = append(items, NewShellItem(sty, p.Command, p.Output, p.ExitCode))
			case message.SubAgentReport:
				items = append(items, NewSubAgentReportItem(sty, p.Label, p.Model, p.Output, p.Failed))
			}
		}
		if len(items) > 0 {
			return items
		}
		items = []MessageItem{NewUserMessageItem(sty, msg, newAttachmentRenderer(sty))}
		// Skip info line 当 there 是 no metadata 到 show (或
		// element 是 malformed), rather than adding blank 行.
		if IsChannelMessage(msg) {
			if _, ok := parseChannelInfo(msg); ok {
				items = append(items, NewChannelInfoItem(sty, msg))
			}
		}
		return items
	case message.Assistant:
		var items []MessageItem
		if ShouldRenderAssistantMessage(msg) {
			items = append(items, NewAssistantMessageItem(sty, msg))
		}
		for _, tc := range msg.ToolCalls() {
			var result *message.ToolResult
			if tr, ok := toolResults[tc.ID]; ok {
				result = &tr
			}
			item := NewToolMessageItem(
				sty,
				msg.ID,
				tc,
				result,
				msg.FinishReason() == message.FinishReasonCanceled,
				workingDir,
			)
			// History items have no trustworthy start time; clear the
			// constructor timestamp 所以 no misleading timer 是 shown.
			if !tc.Finished {
				if restorable, ok := item.(interface{ markRestored() }); ok {
					restorable.markRestored()
				}
			}
			items = append(items, item)
		}
		return items
	}
	return []MessageItem{}
}

// ShouldRenderAssistantMessage determines 如果 助手消息 should 是 rendered
//
// In some cases 助手消息 只 has tools 所以 we do 不 want 到 render
// empty 消息.
func ShouldRenderAssistantMessage(msg *message.Message) bool {
	content := strings.TrimSpace(msg.Content().Text)
	thinking := strings.TrimSpace(msg.ReasoningContent().Thinking)
	isCancelled := msg.FinishReason() == message.FinishReasonCanceled
	hasToolCalls := len(msg.ToolCalls()) > 0
	// 一个 工具-只 turn renders no 助手 body, 但 错误, 拒绝,
	// 或 truncation banner still has 到 appear, 所以 those finishes force
	// 消息 到 render.
	return !hasToolCalls || content != "" || thinking != "" || msg.IsThinking() || msg.IsErrorLike() || msg.IsTruncated() || isCancelled
}

// BuildToolResultMap creates map 的 工具调用 IDs 到 their results 从 list 的 messages.
// 工具结果 messages (角色 == 消息.工具) contain results 那 should 是 linked
// 到 工具 calls 在 助手 messages.
func BuildToolResultMap(messages []*message.Message) map[string]message.ToolResult {
	resultMap := make(map[string]message.ToolResult)
	for _, msg := range messages {
		if msg.Role == message.Tool {
			for _, result := range msg.ToolResults() {
				if result.ToolCallID != "" {
					resultMap[result.ToolCallID] = result
				}
			}
		}
	}
	return resultMap
}
