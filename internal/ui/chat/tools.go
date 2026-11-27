// tools.go —— UI 消息渲染：把后端来的消息/工具调用转成 TUI 可显示的项。
// 属于产物链的末端：db/message → Workspace.ListMessages → ui/chat → 屏幕。

package chat

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/tree"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/stringext"
	"github.com/charmbracelet/crush/internal/ui/anim"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// responseContextHeight limits number 的 lines displayed 在 工具 output.
const responseContextHeight = 10

// toolBodyLeftPaddingTotal represents padding 那 should 是 applied 到 each 工具 body
const toolBodyLeftPaddingTotal = 2

// collapsedMaxLines returns number 的 lines 到 display 当 content
// 是 collapsed. If collapsing would hide 只 single line, all lines
// 是 shown instead.
func collapsedMaxLines(totalLines int) int {
	if totalLines <= responseContextHeight+1 {
		return totalLines
	}
	return responseContextHeight
}

// ToolStatus represents current state 的 工具调用.
type ToolStatus int

const (
	ToolStatusAwaitingPermission ToolStatus = iota
	ToolStatusRunning
	ToolStatusSuccess
	ToolStatusError
	ToolStatusCanceled
)

// ToolMessageItem represents 工具调用 消息 在 chat UI.
type ToolMessageItem interface {
	MessageItem

	ToolCall() message.ToolCall
	SetToolCall(tc message.ToolCall)
	SetResult(res *message.ToolResult)
	MessageID() string
	SetMessageID(id string)
	SetStatus(status ToolStatus)
	Status() ToolStatus
}

// Compactable 是 interface 为 工具 items 那 can render 在 compacted mode.
// When compact mode 是 enabled, tools render 作为 compact single-line header.
type Compactable interface {
	SetCompact(compact bool)
}

// SpinningState contains state passed 到 SpinningFunc 为 custom spinning logic.
type SpinningState struct {
	ToolCall message.ToolCall
	Result   *message.ToolResult
	Status   ToolStatus
}

// IsCanceled returns true 如果 工具 status 是 canceled.
func (s *SpinningState) IsCanceled() bool {
	return s.Status == ToolStatusCanceled
}

// HasResult returns true 如果 结果 是 不 nil.
func (s *SpinningState) HasResult() bool {
	return s.Result != nil
}

// SpinningFunc 是 function type 为 custom spinning logic.
// Returns true 如果 工具 should show spinning animation.
type SpinningFunc func(state SpinningState) bool

// DefaultToolRenderContext implements 默认 [ToolRenderer] interface.
type DefaultToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (d *DefaultToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return "TODO: Implement Tool Renderer For: " + opts.ToolCall.Name
}

// ToolRenderOpts contains data needed 到 render 工具调用.
type ToolRenderOpts struct {
	ToolCall        message.ToolCall
	Result          *message.ToolResult
	Anim            *anim.Anim
	ExpandedContent bool
	Compact         bool
	IsSpinning      bool
	Status          ToolStatus
	// Elapsed 是 how long 调用 has 运行, 或 zero 当 那 是 unknown.
	Elapsed time.Duration
	// LiveOutput 是 output streamed while 工具 runs, shown instead 的
	// 结果 until 工具 finishes.
	LiveOutput string
}

// IsStreamingInput reports whether 模型 是 still writing 调用's
// arguments, 所以 there 是 no 完成 input 到 show yet.
func (o *ToolRenderOpts) IsStreamingInput() bool {
	return !o.ToolCall.Finished && !o.IsCanceled()
}

// IsCanceled returns true 如果 工具 status 是 canceled.
func (o *ToolRenderOpts) IsCanceled() bool {
	return o.Status == ToolStatusCanceled
}

// HasResult returns true 如果 结果 是 不 nil.
func (o *ToolRenderOpts) HasResult() bool {
	return o.Result != nil
}

// HasEmptyResult returns true 如果 结果 是 nil 或 has empty content.
func (o *ToolRenderOpts) HasEmptyResult() bool {
	return o.Result == nil || o.Result.Content == ""
}

// ToolRenderer represents interface 为 rendering 工具 calls.
type ToolRenderer interface {
	RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string
}

// ToolRendererFunc 是 function type 那 implements [ToolRenderer] interface.
type ToolRendererFunc func(sty *styles.Styles, width int, opts *ToolRenderOpts) string

// RenderTool implements the ToolRenderer interface.
func (f ToolRendererFunc) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return f(sty, width, opts)
}

// baseToolMessageItem represents 工具调用 消息 那 can 是 displayed 在 UI.
type baseToolMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	toolRenderer ToolRenderer
	toolCall     message.ToolCall
	result       *message.ToolResult
	messageID    string
	status       ToolStatus
	// we use 这 所以 we can efficiently 缓存
	// tools 那 have capped width (e.x bash.. 和 others)
	hasCappedWidth bool
	// isCompact indicates 这 工具 should render 在 compact mode.
	isCompact bool
	// spinningFunc overrides 默认: spin until 结果 或 取消.
	spinningFunc SpinningFunc
	// liveOutput 是 output streamed while 工具 runs. 它 是 display
	// 只 和 是 cleared once 结果 arrives.
	liveOutput string

	sty             *styles.Styles
	anim            *anim.Anim
	expandedContent bool
}

// LiveOutputItem 是 implemented 通过 工具 items 那 can show output while
// 工具 是 still running.
type LiveOutputItem interface {
	SetLiveOutput(output string) bool
}

// SetLiveOutput sets output 到 show while 工具 runs. 它 是 ignored
// once 结果 是 set 或 工具 是 canceled, 和 reports whether
// item changed.
func (t *baseToolMessageItem) SetLiveOutput(output string) bool {
	if t.result != nil || t.status == ToolStatusCanceled || t.liveOutput == output {
		return false
	}
	t.liveOutput = output
	t.clearCache()
	t.Bump()
	return true
}

var _ Expandable = (*baseToolMessageItem)(nil)

// newBaseToolMessageItem 是 internal constructor 为 base 工具 消息 items.
func newBaseToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	toolRenderer ToolRenderer,
	canceled bool,
) *baseToolMessageItem {
	// we 只 do full width 为 diffs (作为 far 作为 I know)
	hasCappedWidth := toolCall.Name != tools.EditToolName && toolCall.Name != tools.MultiEditToolName

	status := ToolStatusRunning
	if canceled {
		status = ToolStatusCanceled
	}

	v := list.NewVersioned()
	t := &baseToolMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		sty:                      sty,
		toolRenderer:             toolRenderer,
		toolCall:                 toolCall,
		result:                   result,
		status:                   status,
		hasCappedWidth:           hasCappedWidth,
	}
	t.anim = anim.New(anim.Settings{
		ID:          toolCall.ID,
		Size:        15,
		GradColorA:  sty.WorkingGradFromColor,
		GradColorB:  sty.WorkingGradToColor,
		LabelColor:  sty.WorkingLabelColor,
		CycleColors: true,
		// Timer 在 pending spinner: quick tools (view, grep, edit)
		// show the overall turn time, long-running ones their own
		// elapsed time once it becomes the more useful signal.
		Suffix:      t.spinnerTimer,
		SuffixColor: sty.WorkingTimerColor,
	})

	return t
}

// NewToolMessageItem creates new [ToolMessageItem] based 在 工具调用 name.
//
// 它 returns specific 工具 消息 item type 如果 implemented, otherwise it
// returns generic 工具 消息 item. messageID 是 ID 的 助手
// 消息 containing 这 工具调用.
func NewToolMessageItem(
	sty *styles.Styles,
	messageID string,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
	workingDir string,
) ToolMessageItem {
	var item ToolMessageItem
	switch toolCall.Name {
	case tools.BashToolName:
		item = NewBashToolMessageItem(sty, toolCall, result, canceled, workingDir)
	case tools.JobOutputToolName:
		item = NewJobOutputToolMessageItem(sty, toolCall, result, canceled, workingDir)
	case tools.JobKillToolName:
		item = NewJobKillToolMessageItem(sty, toolCall, result, canceled)
	case tools.ViewToolName:
		item = NewViewToolMessageItem(sty, toolCall, result, canceled)
	case tools.WriteToolName:
		item = NewWriteToolMessageItem(sty, toolCall, result, canceled)
	case tools.EditToolName:
		item = NewEditToolMessageItem(sty, toolCall, result, canceled)
	case tools.MultiEditToolName:
		item = NewMultiEditToolMessageItem(sty, toolCall, result, canceled)
	case tools.GlobToolName:
		item = NewGlobToolMessageItem(sty, toolCall, result, canceled)
	case tools.GrepToolName:
		item = NewGrepToolMessageItem(sty, toolCall, result, canceled)
	case tools.LSToolName:
		item = NewLSToolMessageItem(sty, toolCall, result, canceled)
	case tools.DownloadToolName:
		item = NewDownloadToolMessageItem(sty, toolCall, result, canceled)
	case tools.FetchToolName:
		item = NewFetchToolMessageItem(sty, toolCall, result, canceled)
	case tools.SourcegraphToolName:
		item = NewSourcegraphToolMessageItem(sty, toolCall, result, canceled)
	case tools.DiagnosticsToolName:
		item = NewDiagnosticsToolMessageItem(sty, toolCall, result, canceled)
	case agent.AgentToolName, agent.AgentDispatchToolName:
		item = NewAgentToolMessageItem(sty, toolCall, result, canceled)
	case agent.AgentSendToolName, agent.AgentStatusToolName, agent.AgentStopToolName:
		item = NewAgentControlToolMessageItem(sty, toolCall, result, canceled)
	case tools.AgenticFetchToolName:
		item = NewAgenticFetchToolMessageItem(sty, toolCall, result, canceled)
	case tools.WebFetchToolName:
		item = NewWebFetchToolMessageItem(sty, toolCall, result, canceled)
	case tools.WebSearchToolName:
		item = NewWebSearchToolMessageItem(sty, toolCall, result, canceled)
	case tools.TodosToolName:
		item = NewTodosToolMessageItem(sty, toolCall, result, canceled)
	case tools.QuestionToolName:
		item = NewQuestionToolMessageItem(sty, toolCall, result, canceled)
	case tools.ReferencesToolName:
		item = NewReferencesToolMessageItem(sty, toolCall, result, canceled)
	case tools.DefinitionToolName:
		item = NewDefinitionToolMessageItem(sty, toolCall, result, canceled)
	case tools.RenameToolName:
		item = NewRenameToolMessageItem(sty, toolCall, result, canceled)
	case tools.ReplaceSymbolToolName:
		item = NewReplaceSymbolToolMessageItem(sty, toolCall, result, canceled)
	case tools.CallHierarchyToolName:
		item = NewCallHierarchyToolMessageItem(sty, toolCall, result, canceled)
	case tools.SymbolsToolName:
		item = NewSymbolsToolMessageItem(sty, toolCall, result, canceled)
	case tools.LSPRestartToolName:
		item = NewLSPRestartToolMessageItem(sty, toolCall, result, canceled)
	default:
		if IsDockerMCPTool(toolCall.Name) {
			item = NewDockerMCPToolMessageItem(sty, toolCall, result, canceled)
		} else if strings.HasPrefix(toolCall.Name, "mcp_") {
			item = NewMCPToolMessageItem(sty, toolCall, result, canceled)
		} else {
			item = NewGenericToolMessageItem(sty, toolCall, result, canceled)
		}
	}
	item.SetMessageID(messageID)
	return item
}

// SetCompact implements the Compactable interface.
func (t *baseToolMessageItem) SetCompact(compact bool) {
	if t.isCompact == compact {
		return
	}
	t.isCompact = compact
	t.clearCache()
	t.Bump()
}

// ID returns unique identifier 为 这 工具 消息 item.
func (t *baseToolMessageItem) ID() string {
	return t.toolCall.ID
}

// Spinning implements [Animatable].
func (t *baseToolMessageItem) Spinning() bool {
	return t.isSpinning()
}

// restyleAnim implements [animRestyler].
func (t *baseToolMessageItem) restyleAnim() {
	t.anim.SetColors(t.sty.WorkingLabelColor, t.sty.WorkingGradFromColor, t.sty.WorkingGradToColor, nil)
}

// Advance implements [Animatable].
//
// Bumps F6 list-缓存 version 所以 next draw re-renders 这
// item: spinner frame mutates anim's internal 计数器, which changes
// rendered output 但 是 invisible 到 per-item caches. Without
// bump list 缓存 would serve previously rendered frame
// indefinitely 和 spinner would appear frozen.
func (t *baseToolMessageItem) Advance() bool {
	if !t.isSpinning() || !t.anim.Advance() {
		return false
	}
	t.Bump()
	return true
}

// RawRender implements [MessageItem].
func (t *baseToolMessageItem) RawRender(width int) string {
	toolItemWidth := width - MessageLeftPaddingTotal
	if t.hasCappedWidth {
		toolItemWidth = cappedMessageWidth(width)
	}

	content, height, ok := t.getCachedRender(toolItemWidth)
	// 如果 we 是 spinning 或 there 是 no 缓存 rerender
	if !ok || t.isSpinning() {
		content = t.renderTool(toolItemWidth)

		// Prepend 钩子 indicator 如果 hooks ran 为 这 工具调用.
		if t.result != nil {
			if hookLine := toolOutputHookIndicator(t.sty, t.result.Metadata, toolItemWidth); hookLine != "" {
				content = hookLine + "\n\n" + content
			}
		}

		height = lipgloss.Height(content)
		// 缓存 rendered content
		t.setCachedRender(content, toolItemWidth, height)
	}

	return t.renderHighlighted(content, toolItemWidth, height)
}

// renderTool renders 调用 用 它的 工具's renderer, except 调用 那
// failed before any input was written: 它的 renderer would 只 say
// input 是 invalid, 所以 it gets 它的 name 和 real 错误 instead.
func (t *baseToolMessageItem) renderTool(width int) string {
	if t.result != nil && t.result.IsError && strings.TrimSpace(t.toolCall.Input) == "" {
		width = cappedMessageWidth(width)
		header := toolHeader(t.sty, t.computeStatus(), prettifyToolName(t.toolCall.Name), width, nil)
		if t.isCompact {
			return header
		}
		return joinToolParts(header, toolErrorContent(t.sty, t.result, width))
	}
	return t.toolRenderer.RenderTool(t.sty, width, &ToolRenderOpts{
		ToolCall:        t.toolCall,
		Result:          t.result,
		Anim:            t.anim,
		ExpandedContent: t.expandedContent,
		Compact:         t.isCompact,
		IsSpinning:      t.isSpinning(),
		Status:          t.computeStatus(),
		Elapsed:         t.elapsed(),
		LiveOutput:      t.liveOutput,
	})
}

// Render renders 工具 消息 item 在 given width.
func (t *baseToolMessageItem) Render(width int) string {
	// 缓存 prefixed output keyed 通过 (width, prefix variant).
	// Bypass 缓存 while spinning (RawRender output 是
	// frame-dependent) 或 while highlight range 是 活跃.
	useCache := !t.isSpinning() && !t.isHighlighted()
	var key uint64
	switch {
	case t.isCompact:
		key = 2
	case t.focused:
		key = 1
	default:
		key = 0
	}
	if useCache {
		if cached, ok := t.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	var prefix string
	if t.isCompact {
		prefix = t.sty.Messages.ToolCallCompact.Render()
	} else if t.focused {
		prefix = t.sty.Messages.ToolCallFocused.Render()
	} else {
		prefix = t.sty.Messages.ToolCallBlurred.Render()
	}
	lines := strings.Split(t.RawRender(width), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	out := strings.Join(lines, "\n")
	if useCache {
		t.setCachedPrefixedRender(out, width, key)
	}
	return out
}

// ToolCall returns 工具调用 associated 用 这 消息 item.
func (t *baseToolMessageItem) ToolCall() message.ToolCall {
	return t.toolCall
}

// SetToolCall sets 工具调用 associated 用 这 消息 item.
func (t *baseToolMessageItem) SetToolCall(tc message.ToolCall) {
	t.toolCall = tc
	t.clearCache()
	t.Bump()
}

// elapsed returns how long 调用 has 运行, 从 它的 recorded start 到 它的
// reply 或 到 now. Zero 当 either end 是 unknown.
func (t *baseToolMessageItem) elapsed() time.Duration {
	start, end := t.toolCall.StartedAtMs, time.Now().UnixMilli()
	switch {
	case t.result != nil:
		end = t.result.FinishedAtMs
	case t.status == ToolStatusCanceled:
		end = 0
	}
	if start == 0 || end < start {
		return 0
	}
	return common.ElapsedSince(time.UnixMilli(start), time.UnixMilli(end))
}

// toolTimerOwnTimeThreshold 是 how long 工具调用 must 运行 before 它的
// own elapsed time takes over the pending spinner's timer. Quick tools
// (view, grep, edit) never cross it 和 keep showing overall turn
// time, which 是 more useful signal 为 them; long-running ones
// (bash, fetch, sub-agents) switch 到 their own duration once it starts
// 到 matter.
const toolTimerOwnTimeThreshold = 10 * time.Second

// spinnerTimer returns timer 文本 shown next 到 pending
// spinner: overall turn time while 工具 是 quick, 它的 own
// elapsed time once it has been running long enough 到 matter. Empty
// 当 no turn 是 活跃 和 start time 是 unknown, 所以 renders 的
// restored sessions 是 unaffected.
func (t *baseToolMessageItem) spinnerTimer() string {
	if d := t.elapsed(); d >= toolTimerOwnTimeThreshold {
		return common.FormatDuration(d)
	}
	return common.CurrentElapsed()
}

// SetResult sets 工具结果 associated 用 这 消息 item.
func (t *baseToolMessageItem) SetResult(res *message.ToolResult) {
	t.result = res
	if res != nil {
		t.liveOutput = ""
	}
	t.clearCache()
	t.Bump()
}

// MessageID returns ID 的 消息 containing 这 工具调用.
func (t *baseToolMessageItem) MessageID() string {
	return t.messageID
}

// SetMessageID sets ID 的 消息 containing 这 工具调用.
// MessageID 是 metadata 只 和 does 不 affect rendered output,
// 所以 we deliberately do 不 bump version here.
func (t *baseToolMessageItem) SetMessageID(id string) {
	t.messageID = id
}

// SetStatus sets 工具 status.
func (t *baseToolMessageItem) SetStatus(status ToolStatus) {
	if t.status == status {
		return
	}
	t.status = status
	if status == ToolStatusCanceled {
		t.liveOutput = ""
	}
	t.clearCache()
	t.Bump()
}

// Status returns current 工具 status.
func (t *baseToolMessageItem) Status() ToolStatus {
	return t.status
}

// computeStatus computes effective status considering 结果.
func (t *baseToolMessageItem) computeStatus() ToolStatus {
	switch {
	case t.result == nil:
		return t.status
	case t.result.IsError && t.status == ToolStatusCanceled:
		return ToolStatusCanceled // an interruption, not a real error
	case t.result.IsError:
		return ToolStatusError
	}
	return ToolStatusSuccess
}

// isSpinning reports whether 工具 是 still running; Finished 只
// means 它的 arguments 是 完成.
func (t *baseToolMessageItem) isSpinning() bool {
	if t.spinningFunc != nil {
		return t.spinningFunc(SpinningState{
			ToolCall: t.toolCall,
			Result:   t.result,
			Status:   t.status,
		})
	}
	return t.result == nil && t.status != ToolStatusCanceled
}

// SetSpinningFunc sets custom function 到 determine 如果 工具 should spin.
func (t *baseToolMessageItem) SetSpinningFunc(fn SpinningFunc) {
	t.spinningFunc = fn
}

// ToggleExpanded toggles expanded state 的 thinking box.
func (t *baseToolMessageItem) ToggleExpanded() bool {
	t.expandedContent = !t.expandedContent
	t.clearCache()
	t.Bump()
	return t.expandedContent
}

// HandleMouseClick implements MouseClickable.
func (t *baseToolMessageItem) HandleMouseClick(btn ansi.MouseButton, x, y int) bool {
	return btn == ansi.MouseLeft
}

// HandleKeyEvent implements KeyEventHandler.
func (t *baseToolMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if k := key.String(); k == "c" || k == "y" {
		text := t.formatToolForCopy()
		return true, common.CopyToClipboard(text, "Tool content copied to clipboard")
	}
	return false, nil
}

// pendingTool renders 工具 那 是 still 在 progress 用 animation.
func pendingTool(sty *styles.Styles, name string, anim *anim.Anim, nested bool) string {
	icon := sty.Tool.IconPending.Render()
	nameStyle := sty.Tool.NameNormal
	if nested {
		nameStyle = sty.Tool.NameNested
	}
	toolName := nameStyle.Render(name)

	var animView string
	if anim != nil {
		animView = anim.Render()
	}

	return fmt.Sprintf("%s %s %s", icon, toolName, animView)
}

// partialJSONStringValue matches raw remainder 的 partial JSON
// object after decoder stopped, 当 值 的 pending key
// 是 在-flight string, e.g. `: "hel`.
var partialJSONStringValue = regexp.MustCompile(`^\s*:\s*"((?:[^"\\]|\\.)*\\?)$`)

// partialJSONFields extracts every 完成 top-level scalar 字段 的
// JSON object 那 may 是 cut off mid-流, e.g. while 工具调用
// input 是 still being streamed. 值 currently streaming 是 also
// recovered 作为 far 作为 it 是 unambiguous: string 值 是 included
// while it grows ( cut escape 在 它的 end 是 dropped), while partial
// keys, numbers, booleans, 和 nested values 是 omitted. Nested
// objects 和 arrays 是 skipped: 工具 parameter schemas 是 flat.
func partialJSONFields(input string) map[string]any {
	fields := make(map[string]any)
	dec := json.NewDecoder(strings.NewReader(input))
	var key string
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
				if depth == 1 {
					// 一个 nested 值 的 top-level object just closed;
					// drop 它的 dangling key 所以 next key/值 pair stays
					// 在 sync.
					key = ""
				}
			}
		case string:
			if depth != 1 {
				continue
			}
			if key == "" {
				key = v
			} else {
				fields[key] = v
				key = ""
			}
		default:
			if depth == 1 && key != "" {
				fields[key] = v
				key = ""
			}
		}
	}
	if key != "" && depth == 1 {
		offset := min(int(dec.InputOffset()), len(input))
		if m := partialJSONStringValue.FindStringSubmatch(input[offset:]); m != nil {
			fields[key] = unescapePartialString(m[1])
		}
	}
	return fields
}

// unescapePartialString unescapes string 值 cut off mid-流,
// dropping trailing incomplete escape such 作为 lone backslash 或
// short \u 序号. Unescaping 是 best-effort: 值 是 prefix
// 的 final one 和 self-corrects 作为 more input arrives.
func unescapePartialString(s string) string {
	// JSON allows \/ where Go string syntax does 不.
	s = strings.ReplaceAll(s, `\/`, `/`)
	for {
		if v, err := strconv.Unquote(`"` + s + `"`); err == nil {
			return v
		}
		idx := strings.LastIndexByte(s, '\\')
		if idx < 0 || len(s)-idx > len(`\uXXXX`) {
			// No escape 到 trim: failure 是 不 cut tail, 所以
			// raw prefix 是 best display we have.
			return s
		}
		s = s[:idx]
	}
}

// waitingForToolMessage builds "Waiting 为 工具 响应..." label,
// including how long 工具 has been running 和, 当 turn 是
// 活跃, total elapsed turn time.
func waitingForToolMessage(opts *ToolRenderOpts) string {
	msg := "Waiting for tool response"
	if opts.Elapsed > 0 {
		msg += fmt.Sprintf(" for %s", common.FormatDuration(opts.Elapsed))
	}
	if total := common.CurrentElapsed(); total != "" {
		msg += fmt.Sprintf(" (%s total)", total)
	}
	return msg + "..."
}

// toolEarlyStateContent handles 错误/cancelled/pending states before content rendering.
// Returns rendered output 和 true 如果 early state was handled.
func toolEarlyStateContent(sty *styles.Styles, opts *ToolRenderOpts, width int) (string, bool) {
	var msg string
	switch opts.Status {
	case ToolStatusError:
		msg = toolErrorContent(sty, opts.Result, width)
	case ToolStatusCanceled:
		msg = sty.Tool.StateCancelled.Render("Canceled.")
	case ToolStatusAwaitingPermission:
		msg = sty.Tool.StateWaiting.Render("Requesting permission...") + toolTimerSuffix(sty)
	case ToolStatusRunning:
		msg = sty.Tool.StateWaiting.Render(waitingForToolMessage(opts))
	default:
		return "", false
	}
	return msg, true
}

// toolTimerSuffix renders live turn timer shown next 到 工具 calls
// 那 是 still 在 flight. Returns empty string 当 no turn 是
// 活跃, 所以 renders 的 persisted sessions 是 unaffected.
func toolTimerSuffix(sty *styles.Styles) string {
	elapsed := common.CurrentElapsed()
	if elapsed == "" {
		return ""
	}
	return " " + lipgloss.NewStyle().Foreground(sty.WorkingTimerColor).Render(elapsed)
}

// toolErrorContent formats 错误 消息 用 错误 或 WARN tag.
func toolErrorContent(sty *styles.Styles, result *message.ToolResult, width int) string {
	if result == nil {
		return ""
	}
	errContent := strings.ReplaceAll(result.Content, "\n", " ")
	if strings.Contains(errContent, "User denied permission") ||
		strings.Contains(errContent, "User cancelled") {
		deniedTag := sty.Tool.WarnTag.Render("WARN")
		deniedTagWidth := lipgloss.Width(deniedTag)
		errContent = ansi.Truncate(errContent, width-deniedTagWidth-3, "…")
		return fmt.Sprintf("%s %s", deniedTag, sty.Tool.WarnMessage.Render(errContent))
	}
	errTag := sty.Tool.ErrorTag.Render("ERROR")
	tagWidth := lipgloss.Width(errTag)
	errContent = ansi.Truncate(errContent, width-tagWidth-3, "…")
	return fmt.Sprintf("%s %s", errTag, sty.Tool.ErrorMessage.Render(errContent))
}

// toolIcon returns status icon 为 工具调用.
// toolIcon returns status icon 为 工具调用 based 在 它的 status.
func toolIcon(sty *styles.Styles, status ToolStatus) string {
	switch status {
	case ToolStatusSuccess:
		return sty.Tool.IconSuccess.String()
	case ToolStatusError:
		return sty.Tool.IconError.String()
	case ToolStatusCanceled:
		return sty.Tool.IconCancelled.String()
	default:
		return sty.Tool.IconPending.String()
	}
}

// toolParamList formats 工具 parameters 作为 "main (key=值, ...)" 用 truncation.
// When opts.ExpandedContent 是 true, output wraps instead 的 truncating,
// unless 工具 是 compact (nested 在 agent tree), which always truncates.
func toolParamList(sty *styles.Styles, params []string, width int, opts *ToolRenderOpts) string {
	// minSpaceForMainParam 是 min space required 为 main param
	// 如果 这 是 less 那 值 set we will 只 show main param nothing else
	const minSpaceForMainParam = 30
	if len(params) == 0 {
		return ""
	}

	mainParam := params[0]

	// Build key=值 pairs 从 remaining params (consecutive key, 值 pairs).
	var kvPairs []string
	for i := 1; i+1 < len(params); i += 2 {
		if params[i+1] != "" {
			kvPairs = append(kvPairs, fmt.Sprintf("%s=%s", params[i], params[i+1]))
		}
	}

	// Try 到 include key=值 pairs 如果 there's enough space.
	output := mainParam
	if len(kvPairs) > 0 {
		partsStr := strings.Join(kvPairs, ", ")
		if remaining := width - lipgloss.Width(partsStr) - 3; remaining >= minSpaceForMainParam {
			// main param 是 syntax highlighted 所以 style param 作为 well
			pairs := sty.Tool.ParamMain.Render(fmt.Sprintf("(%s)", partsStr))
			output = fmt.Sprintf("%s %s", mainParam, pairs)
		}
	}

	// Compact tools 是 children 的 agent tree, where wrapped
	// 请求头 would push continuation lines past tree's branch drawing
	// 和 break bars. They truncate even 当 expanded, 和 stay 在 one
	// line even 当 parameter has newlines 的 它的 own.
	if opts != nil && opts.Compact {
		output = strings.ReplaceAll(output, "\n", " ")
	}
	if width >= 0 && (opts == nil || !opts.ExpandedContent || opts.Compact) {
		return sty.Tool.ParamMain.Render(ansi.Truncate(output, width, "…"))
	}
	if opts != nil && opts.ExpandedContent && width > 0 && lipgloss.Width(output) > width {
		// Let Lip Gloss do wrapping rather than wrapping 和 styling 在
		// separate steps. 一个 parameter 是 often syntax highlighted, 和
		// wrap falls wherever width runs out, usually 片段-way through
		// coloured token. Lip Gloss carries colours 那 是 open 在
		// break 和 re-opens them 在 next line, 在 top 的 parameter
		// style. Hard-wrapping first 和 styling after would emit 只
		// parameter colour there 和 drop highlight; styling first 和
		// hard-wrapping after would emit nothing there 和 drop both.
		return capLineWidths(sty.Tool.ParamMain.Width(width).Render(output), width)
	}
	return sty.Tool.ParamMain.Render(output)
}

// capLineWidths holds every line 的 s 到 width.
//
// Word wrapping keeps space it broke 在, 所以 word 那 ends exactly 在
// edge leaves 它的 line column wider than space it was given, 和
// header spills past pane. space 是 invisible 和 sits before
// closing reset, out 的 reach 的 plain trim, 所以 line 是 cut 用
// escape sequences accounted 为.
func capLineWidths(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = ansi.Truncate(line, width, "")
		}
	}
	return strings.Join(lines, "\n")
}

// toolHeader builds 工具 header line: "● ToolName params..."
// When opts.ExpandedContent 是 true, long parameters wrap instead 的 truncating.
func toolHeader(sty *styles.Styles, status ToolStatus, name string, width int, opts *ToolRenderOpts, params ...string) string {
	prefix := toolHeaderPrefix(sty, status, name, opts)
	prefixWidth := lipgloss.Width(prefix)
	paramsStr := toolParamList(sty, params, width-prefixWidth, opts)

	// When expanded, toolParamList may return multiple lines. Indent
	// continuation lines 到 align 用 first line's param 文本.
	if first, rest, found := strings.Cut(paramsStr, "\n"); found {
		return prefix + first + "\n" + indentLines(rest, prefixWidth)
	}
	return prefix + paramsStr
}

// toolHeaderPrefix returns styled "● ToolName " 那 leads 工具 header.
func toolHeaderPrefix(sty *styles.Styles, status ToolStatus, name string, opts *ToolRenderOpts) string {
	nameStyle := sty.Tool.NameNormal
	if opts != nil && opts.Compact {
		nameStyle = sty.Tool.NameNested
	}
	return fmt.Sprintf("%s %s ", toolIcon(sty, status), nameStyle.Render(name))
}

// indentLines indents every line 的 s 通过 n spaces.
func indentLines(s string, n int) string {
	indent := strings.Repeat(" ", n)
	return indent + strings.ReplaceAll(s, "\n", "\n"+indent)
}

// toolOutputPlainContent renders plain 文本 用 optional expansion support.
func toolOutputPlainContent(sty *styles.Styles, content string, width int, expanded bool) string {
	return renderPlainToolOutput(sty, content, width, expanded, false)
}

func renderPlainToolOutput(sty *styles.Styles, content string, width int, expanded, tail bool) string {
	content = stringext.NormalizeSpace(content)
	content = common.StripCursorControl(content)
	if tail && content == "" {
		return ""
	}
	content = common.RemapANSI16(content, sty.ANSI)
	lines := strings.Split(content, "\n")

	maxLines := collapsedMaxLines(len(lines))
	if expanded {
		maxLines = len(lines) // Show all
	}

	hidden := max(len(lines)-maxLines, 0)
	visible := lines
	if hidden > 0 {
		if tail {
			visible = lines[len(lines)-maxLines:]
		} else {
			visible = lines[:maxLines]
		}
	}

	var out []string
	if tail && !expanded && hidden > 0 {
		out = append(out, sty.Tool.ContentTruncation.
			Width(width).
			Render(fmt.Sprintf(assistantMessageTruncateFormat, hidden)))
	}
	for _, ln := range visible {
		ln = " " + ln
		if lipgloss.Width(ln) > width {
			ln = ansi.Truncate(ln, width, "…")
		}
		out = append(out, sty.Tool.ContentLine.Width(width).Render(ln))
	}
	if !tail && !expanded && hidden > 0 {
		out = append(out, sty.Tool.ContentTruncation.
			Width(width).
			Render(fmt.Sprintf(assistantMessageTruncateFormat, hidden)))
	}

	return strings.Join(out, "\n")
}

// toolOutputCodeContent renders code 用 syntax highlighting 和 line numbers.
func toolOutputCodeContent(sty *styles.Styles, path, content string, offset, width int, expanded bool) string {
	content = stringext.NormalizeSpace(content)

	lines := strings.Split(content, "\n")
	maxLines := collapsedMaxLines(len(lines))
	if expanded {
		maxLines = len(lines)
	}

	// Truncate 如果 needed.
	displayLines := lines
	if len(lines) > maxLines {
		displayLines = lines[:maxLines]
	}

	bg := sty.Tool.ContentCodeBg
	highlighted, _ := common.SyntaxHighlight(sty, strings.Join(displayLines, "\n"), path, bg)
	highlightedLines := strings.Split(highlighted, "\n")

	// Calculate line number width.
	maxLineNumber := len(displayLines) + offset
	maxDigits := getDigits(maxLineNumber)
	numFmt := fmt.Sprintf("%%%dd", maxDigits)

	bodyWidth := width - toolBodyLeftPaddingTotal
	codeWidth := bodyWidth - maxDigits

	var out []string
	for i, ln := range highlightedLines {
		lineNum := sty.Tool.ContentLineNumber.Render(fmt.Sprintf(numFmt, i+1+offset))

		// Truncate accounting 为 padding 那 will 是 added.
		ln = ansi.Truncate(ln, codeWidth-sty.Tool.ContentCodeLine.GetHorizontalPadding(), "…")

		codeLine := sty.Tool.ContentCodeLine.
			Width(codeWidth).
			Render(ln)

		out = append(out, lipgloss.JoinHorizontal(lipgloss.Left, lineNum, codeLine))
	}

	// Add truncation 消息 如果 needed.
	if len(lines) > maxLines && !expanded {
		out = append(
			out, sty.Tool.ContentCodeTruncation.
				Width(width).
				Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-maxLines)),
		)
	}

	return sty.Tool.Body.Render(strings.Join(out, "\n"))
}

// toolOutputImageContent renders 图片 data 用 size info.
func toolOutputImageContent(sty *styles.Styles, data, mediaType string) string {
	dataSize := len(data) * 3 / 4
	sizeStr := formatSize(dataSize)

	return sty.Tool.Body.Render(fmt.Sprintf(
		"%s %s %s %s",
		sty.Tool.ResourceLoadedText.Render("Loaded Image"),
		sty.Tool.ResourceLoadedIndicator.Render(styles.ArrowRightIcon),
		sty.Tool.MediaType.Render(mediaType),
		sty.Tool.ResourceSize.Render(sizeStr),
	))
}

// toolOutputSkillContent 渲染 skill loaded indicator.
func toolOutputSkillContent(sty *styles.Styles, name, description string) string {
	return sty.Tool.Body.Render(fmt.Sprintf(
		"%s %s %s %s",
		sty.Tool.ResourceLoadedText.Render("Loaded Skill"),
		sty.Tool.ResourceLoadedIndicator.Render(styles.ArrowRightIcon),
		sty.Tool.ResourceName.Render(name),
		sty.Tool.ResourceSize.Render(description),
	))
}

// toolOutputHookIndicator renders 钩子 indicator lines 从 工具 metadata.
// Returns empty string 如果 no 钩子 metadata 是 present. 钩子 names 是
// sanitized (newlines replaced 用 ¶) 和 truncated 到 fit available
// horizontal space.
func toolOutputHookIndicator(sty *styles.Styles, metadata string, width int) string {
	if metadata == "" {
		return ""
	}
	var meta struct {
		Hook *hooks.HookMetadata `json:"hook"`
	}
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil || meta.Hook == nil {
		return ""
	}
	h := meta.Hook
	if len(h.Hooks) == 0 {
		return ""
	}

	// Sanitize names (replace newlines 用 ¶) 和 compute max widths
	// 为 name, matcher, 和 detail columns 所以 they align. name
	// column 是 capped 在 maxHookNameWidth characters.
	const maxHookNameWidth = 30
	sanitizedNames := make([]string, len(h.Hooks))
	details := make([]string, len(h.Hooks))
	maxNameWidth := 0
	maxMatcherWidth := 0
	maxDetailWidth := 0
	for i, hi := range h.Hooks {
		sanitizedNames[i] = strings.ReplaceAll(hi.Name, "\n", "¶")
		w := lipgloss.Width(sty.Tool.HookName.Render(sanitizedNames[i]))
		if w > maxNameWidth {
			maxNameWidth = w
		}
		if hi.Matcher != "" {
			mw := lipgloss.Width(sty.Tool.HookMatcher.Render(hi.Matcher))
			if mw > maxMatcherWidth {
				maxMatcherWidth = mw
			}
		}
		details[i] = hookDetail(sty, hi)
		if dw := lipgloss.Width(details[i]); dw > maxDetailWidth {
			maxDetailWidth = dw
		}
	}

	if maxNameWidth > maxHookNameWidth {
		maxNameWidth = maxHookNameWidth
	}

	// Cap name column 所以 widest line still fits 在 width.
	// per-line layout 是:
	// "钩子 " + name(padded) + [" " + matcher(padded)] + " → " + detail
	if width > 0 {
		fixed := lipgloss.Width(sty.Tool.HookLabel.Render("Hook")) + 1
		if maxMatcherWidth > 0 {
			fixed += 1 + maxMatcherWidth
		}
		fixed += 1 + lipgloss.Width(sty.Tool.HookArrow.Render(styles.ArrowRightIcon)) + 1
		fixed += maxDetailWidth
		if budget := width - fixed; budget < maxNameWidth {
			maxNameWidth = max(1, budget)
		}
	}

	var lines []string
	for i, hi := range h.Hooks {
		name := truncateHookName(sanitizedNames[i], maxNameWidth)
		lines = append(lines, renderHookLine(sty, hi, name, details[i], maxNameWidth, maxMatcherWidth))
	}
	return strings.Join(lines, "\n")
}

// truncateHookName truncates 钩子 name 到 fit within maxWidth cells,
// using left-truncation 为 absolute paths (e.g. `…/format.sh`) 和
// right-truncation 为 everything else. Left-truncation 是 只 applied
// 当 name looks unambiguously like 路径: absolute, single-line,
// 和 contains no spaces.
func truncateHookName(name string, maxWidth int) string {
	if ansi.StringWidth(name) <= maxWidth {
		return name
	}
	if isLikelyPath(name) {
		// ansi.TruncateLeft removes n graphemes 从 start; pick n
		// 所以 结果 plus "…" prefix fits 在 maxWidth.
		n := ansi.StringWidth(name) - maxWidth + 1
		return ansi.TruncateLeft(name, n, "…")
	}
	return ansi.Truncate(name, maxWidth, "…")
}

// isLikelyPath reports whether s looks unambiguously like a filesystem
// 路径, suitable 为 left-truncation. We 受理 absolute paths 和
// relative paths 那 contain separator 和 no shell-ish characters.
func isLikelyPath(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\n¶'\"|&;<>$`*?(){}[]\\") {
		return false
	}
	if filepath.IsAbs(s) {
		return true
	}
	return strings.Contains(s, "/")
}

// renderHookLine renders single 钩子 indicator line 用 aligned columns.
func renderHookLine(sty *styles.Styles, hi hooks.HookInfo, rawName, detail string, maxNameWidth, maxMatcherWidth int) string {
	name := sty.Tool.HookName.Render(rawName)
	namePad := strings.Repeat(" ", max(0, maxNameWidth-lipgloss.Width(name)))

	var matcherPart string
	if maxMatcherWidth > 0 {
		if hi.Matcher != "" {
			matcher := sty.Tool.HookMatcher.Render(hi.Matcher)
			matcherPad := strings.Repeat(" ", maxMatcherWidth-lipgloss.Width(matcher))
			matcherPart = " " + matcher + matcherPad
		} else {
			matcherPart = " " + strings.Repeat(" ", maxMatcherWidth)
		}
	}

	labelStyle := sty.Tool.HookLabel
	arrowStyle := sty.Tool.HookArrow
	if hi.Decision == "deny" {
		labelStyle = sty.Tool.HookDeniedLabel
		arrowStyle = sty.Tool.HookDeniedLabel
	}

	return fmt.Sprintf(
		"%s %s%s%s %s %s",
		labelStyle.Render("Hook"),
		name,
		namePad,
		matcherPart,
		arrowStyle.Render(styles.ArrowRightIcon),
		detail,
	)
}

// hookDetail returns styled detail 文本 为 single 钩子 结果.
func hookDetail(sty *styles.Styles, hi hooks.HookInfo) string {
	const (
		okMessage      = "OK"
		denialMessage  = "Denied"
		rewroteMessage = "Rewrote Output"
	)
	switch hi.Decision {
	case "deny":
		if hi.Reason != "" {
			return sty.Tool.HookDenied.Render(denialMessage) + " " + sty.Tool.HookDeniedReason.Render(hi.Reason)
		}
		return sty.Tool.HookDenied.Render(denialMessage)
	case "allow":
		result := sty.Tool.HookOK.Render(okMessage)
		if hi.InputRewrite || hi.OutputRewrite {
			result += " " + sty.Tool.HookRewrote.Render(rewroteMessage)
		}
		return result
	default:
		result := sty.Tool.HookOK.Render(okMessage)
		if hi.InputRewrite || hi.OutputRewrite {
			result += " " + sty.Tool.HookRewrote.Render(rewroteMessage)
		}
		return result
	}
}

// getDigits returns number 的 digits 在 number.
func getDigits(n int) int {
	if n == 0 {
		return 1
	}
	if n < 0 {
		n = -n
	}
	digits := 0
	for n > 0 {
		n /= 10
		digits++
	}
	return digits
}

// formatSize formats byte size into human readable format.
func formatSize(bytes int) string {
	const (
		kb = 1024
		mb = kb * 1024
	)
	switch {
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// toolOutputDiffContent renders diff between old 和 new content.
func toolOutputDiffContent(sty *styles.Styles, file, oldContent, newContent string, width int, expanded bool) string {
	bodyWidth := width - toolBodyLeftPaddingTotal

	formatter := common.DiffFormatter(sty).
		Before(file, oldContent).
		After(file, newContent).
		Width(bodyWidth)

	// Use split view 为 wide terminals.
	if width > maxTextWidth {
		formatter = formatter.Split()
	}

	formatted := formatter.String()
	lines := strings.Split(formatted, "\n")

	// Truncate 如果 needed.
	maxLines := collapsedMaxLines(len(lines))
	if expanded {
		maxLines = len(lines)
	}

	if len(lines) > maxLines && !expanded {
		truncMsg := sty.Tool.DiffTruncation.
			Width(bodyWidth).
			Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-maxLines))
		formatted = strings.Join(lines[:maxLines], "\n") + "\n" + truncMsg
	}

	return sty.Tool.Body.Render(formatted)
}

// formatTimeout converts 超时 seconds 到 duration string (e.g., "30s").
// Returns empty string 如果 超时 是 0.
func formatTimeout(timeout int) string {
	if timeout == 0 {
		return ""
	}
	return fmt.Sprintf("%ds", timeout)
}

// formatNonZero returns string representation 的 non-zero integers, empty string 为 zero.
func formatNonZero(value int) string {
	if value == 0 {
		return ""
	}
	return fmt.Sprintf("%d", value)
}

// toolOutputMultiEditDiffContent renders diff 用 optional failed edits note.
func toolOutputMultiEditDiffContent(sty *styles.Styles, file string, meta tools.MultiEditResponseMetadata, totalEdits, width int, expanded bool) string {
	bodyWidth := width - toolBodyLeftPaddingTotal

	formatter := common.DiffFormatter(sty).
		Before(file, meta.OldContent).
		After(file, meta.NewContent).
		Width(bodyWidth)

	// Use split view 为 wide terminals.
	if width > maxTextWidth {
		formatter = formatter.Split()
	}

	formatted := formatter.String()
	lines := strings.Split(formatted, "\n")

	// Truncate 如果 needed.
	maxLines := collapsedMaxLines(len(lines))
	if expanded {
		maxLines = len(lines)
	}

	if len(lines) > maxLines && !expanded {
		truncMsg := sty.Tool.DiffTruncation.
			Width(bodyWidth).
			Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-maxLines))
		formatted = truncMsg + "\n" + strings.Join(lines[:maxLines], "\n")
	}

	// Add failed edits note 如果 any exist.
	if len(meta.EditsFailed) > 0 {
		noteTag := sty.Tool.NoteTag.Render("Note")
		noteMsg := fmt.Sprintf("%d of %d edits succeeded", meta.EditsApplied, totalEdits)
		note := fmt.Sprintf("%s %s", noteTag, sty.Tool.NoteMessage.Render(noteMsg))
		formatted = formatted + "\n\n" + note
	}

	return sty.Tool.Body.Render(formatted)
}

// roundedTree styles 工具 tree 所以 multiline children stay aligned:
// enumerator draws each branch 和 indenter continues it down child's
// wrapped lines. They share one width because continuation bar 只 lines
// up 用 branch 当 their geometry matches.
func roundedTree(t *tree.Tree, width int) *tree.Tree {
	return t.
		Enumerator(roundedEnumerator(2, width)).
		Indenter(roundedIndenter(2, width))
}

// roundedEnumerator creates tree enumerator 用 rounded corners.
func roundedEnumerator(lPadding, width int) tree.Enumerator {
	lPadding, width = enumeratorDims(lPadding, width)
	return func(children tree.Children, index int) string {
		line := strings.Repeat("─", width)
		padding := strings.Repeat(" ", lPadding)
		if children.Length()-1 == index {
			return padding + "╰" + line
		}
		return padding + "├" + line
	}
}

// roundedIndenter continues a branch down a child's wrapped lines, matching
// roundedEnumerator's geometry 所以 bar stays under branch.
func roundedIndenter(lPadding, width int) tree.Indenter {
	lPadding, width = enumeratorDims(lPadding, width)
	return func(children tree.Children, index int) string {
		padding := strings.Repeat(" ", lPadding)
		if children.Length()-1 == index {
			return padding + " " + strings.Repeat(" ", width)
		}
		return padding + "│" + strings.Repeat(" ", width)
	}
}

// enumeratorDims applies shared defaults 所以 enumerator 和 indenter
// agree 在 geometry.
func enumeratorDims(lPadding, width int) (int, int) {
	if width == 0 {
		width = 2
	}
	if lPadding == 0 {
		lPadding = 1
	}
	return lPadding, width
}

// toolOutputMarkdownContent renders markdown content 用 optional truncation.
func toolOutputMarkdownContent(sty *styles.Styles, content string, width int, expanded bool) string {
	content = stringext.NormalizeSpace(content)

	// Cap width 为 readability.
	if width > maxTextWidth {
		width = maxTextWidth
	}

	renderer := common.QuietMarkdownRenderer(sty, width)
	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	rendered, err := renderer.Render(content)
	mu.Unlock()
	if err != nil {
		return toolOutputPlainContent(sty, content, width, expanded)
	}

	// Glamour ends 用 blank, still-painted line, sometimes 只
	// code block's margin wide; drop it 所以 box ends 用 它的 文本.
	lines := strings.Split(rendered, "\n")
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	maxLines := collapsedMaxLines(len(lines))
	if expanded {
		maxLines = len(lines)
	}

	var out []string
	for i, ln := range lines {
		if i >= maxLines {
			break
		}
		out = append(out, ln)
	}

	if len(lines) > maxLines && !expanded {
		out = append(
			out, sty.Tool.ContentTruncation.
				Width(width).
				Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-maxLines)),
		)
	}

	return sty.Tool.Body.Render(strings.Join(out, "\n"))
}

// formatToolForCopy formats 工具调用 为 clipboard copying.
func (t *baseToolMessageItem) formatToolForCopy() string {
	var parts []string

	toolName := prettifyToolName(t.toolCall.Name)
	parts = append(parts, fmt.Sprintf("## %s Tool Call", toolName))

	if t.toolCall.Input != "" {
		params := t.formatParametersForCopy()
		if params != "" {
			parts = append(parts, "### Parameters:")
			parts = append(parts, params)
		}
	}

	if t.result != nil && t.result.ToolCallID != "" {
		if t.result.IsError {
			parts = append(parts, "### Error:")
			parts = append(parts, t.result.Content)
		} else {
			parts = append(parts, "### Result:")
			content := t.formatResultForCopy()
			if content != "" {
				parts = append(parts, content)
			}
		}
	} else if t.status == ToolStatusCanceled {
		parts = append(parts, "### Status:")
		parts = append(parts, "Cancelled")
	} else {
		parts = append(parts, "### Status:")
		parts = append(parts, "Pending...")
	}

	return strings.Join(parts, "\n\n")
}

// formatParametersForCopy formats 工具 parameters 为 clipboard copying.
func (t *baseToolMessageItem) formatParametersForCopy() string {
	switch t.toolCall.Name {
	case tools.BashToolName:
		var params tools.BashParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			cmd := strings.ReplaceAll(params.Command, "\n", " ")
			cmd = strings.ReplaceAll(cmd, "\t", "    ")
			return fmt.Sprintf("**Command:** %s", cmd)
		}
	case tools.ViewToolName:
		var params tools.ViewParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**File:** %s", fsext.PrettyPath(params.FilePath)))
			if params.Limit > 0 {
				parts = append(parts, fmt.Sprintf("**Limit:** %d", params.Limit))
			}
			if params.Offset > 0 {
				parts = append(parts, fmt.Sprintf("**Offset:** %d", params.Offset))
			}
			return strings.Join(parts, "\n")
		}
	case tools.EditToolName:
		var params tools.EditParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			return fmt.Sprintf("**File:** %s", fsext.PrettyPath(params.FilePath))
		}
	case tools.MultiEditToolName:
		var params tools.MultiEditParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**File:** %s", fsext.PrettyPath(params.FilePath)))
			parts = append(parts, fmt.Sprintf("**Edits:** %d", len(params.Edits)))
			return strings.Join(parts, "\n")
		}
	case tools.WriteToolName:
		var params tools.WriteParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			return fmt.Sprintf("**File:** %s", fsext.PrettyPath(params.FilePath))
		}
	case tools.FetchToolName:
		var params tools.FetchParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**URL:** %s", params.URL))
			if params.Format != "" {
				parts = append(parts, fmt.Sprintf("**Format:** %s", params.Format))
			}
			if params.Timeout > 0 {
				parts = append(parts, fmt.Sprintf("**Timeout:** %ds", params.Timeout))
			}
			return strings.Join(parts, "\n")
		}
	case tools.AgenticFetchToolName:
		var params tools.AgenticFetchParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			var parts []string
			if params.URL != "" {
				parts = append(parts, fmt.Sprintf("**URL:** %s", params.URL))
			}
			if params.Prompt != "" {
				parts = append(parts, fmt.Sprintf("**Prompt:** %s", params.Prompt))
			}
			return strings.Join(parts, "\n")
		}
	case tools.WebFetchToolName:
		var params tools.WebFetchParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			return fmt.Sprintf("**URL:** %s", params.URL)
		}
	case tools.GrepToolName:
		var params tools.GrepParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**Pattern:** %s", params.Pattern))
			if params.Path != "" {
				parts = append(parts, fmt.Sprintf("**Path:** %s", params.Path))
			}
			if params.Include != "" {
				parts = append(parts, fmt.Sprintf("**Include:** %s", params.Include))
			}
			if params.LiteralText {
				parts = append(parts, "**Literal:** true")
			}
			return strings.Join(parts, "\n")
		}
	case tools.GlobToolName:
		var params tools.GlobParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**Pattern:** %s", params.Pattern))
			if params.Path != "" {
				parts = append(parts, fmt.Sprintf("**Path:** %s", params.Path))
			}
			return strings.Join(parts, "\n")
		}
	case tools.LSToolName:
		var params tools.LSParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			path := params.Path
			if path == "" {
				path = "."
			}
			return fmt.Sprintf("**Path:** %s", fsext.PrettyPath(path))
		}
	case tools.DownloadToolName:
		var params tools.DownloadParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**URL:** %s", params.URL))
			parts = append(parts, fmt.Sprintf("**File Path:** %s", fsext.PrettyPath(params.FilePath)))
			if params.Timeout > 0 {
				parts = append(parts, fmt.Sprintf("**Timeout:** %s", (time.Duration(params.Timeout)*time.Second).String()))
			}
			return strings.Join(parts, "\n")
		}
	case tools.SourcegraphToolName:
		var params tools.SourcegraphParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			var parts []string
			parts = append(parts, fmt.Sprintf("**Query:** %s", params.Query))
			if params.Count > 0 {
				parts = append(parts, fmt.Sprintf("**Count:** %d", params.Count))
			}
			if params.ContextWindow > 0 {
				parts = append(parts, fmt.Sprintf("**Context:** %d", params.ContextWindow))
			}
			return strings.Join(parts, "\n")
		}
	case tools.DiagnosticsToolName:
		return "**Project:** diagnostics"
	case agent.AgentToolName:
		var params agent.AgentParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			return fmt.Sprintf("**Task:**\n%s", params.Prompt)
		}
	case agent.AgentDispatchToolName:
		var params agent.AgentDispatchParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			return fmt.Sprintf("**Task (%s):**\n%s", params.Label, params.Prompt)
		}
	case agent.AgentSendToolName:
		var params agent.AgentSendParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			return fmt.Sprintf("**To %s:**\n%s", params.Label, params.Message)
		}
	case agent.AgentStatusToolName:
		var params agent.AgentStatusParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			if params.Label == "" {
				return "**Subagents:** all"
			}
			return fmt.Sprintf("**Subagent:** %s", params.Label)
		}
	case agent.AgentStopToolName:
		var params agent.AgentStopParams
		if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
			if params.Reason == "" {
				return fmt.Sprintf("**Stop:** %s", params.Label)
			}
			return fmt.Sprintf("**Stop:** %s\n**Reason:** %s", params.Label, params.Reason)
		}
	}

	var params map[string]any
	if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
		var parts []string
		for key, value := range params {
			displayKey := strings.ReplaceAll(key, "_", " ")
			if len(displayKey) > 0 {
				displayKey = strings.ToUpper(displayKey[:1]) + displayKey[1:]
			}
			parts = append(parts, fmt.Sprintf("**%s:** %v", displayKey, value))
		}
		return strings.Join(parts, "\n")
	}

	return ""
}

// formatResultForCopy formats 工具 results 为 clipboard copying.
func (t *baseToolMessageItem) formatResultForCopy() string {
	if t.result == nil {
		return ""
	}

	if t.result.Data != "" {
		if strings.HasPrefix(t.result.MIMEType, "image/") {
			return fmt.Sprintf("[Image: %s]", t.result.MIMEType)
		}
		return fmt.Sprintf("[Media: %s]", t.result.MIMEType)
	}

	switch t.toolCall.Name {
	case tools.BashToolName:
		return t.formatBashResultForCopy()
	case tools.ViewToolName:
		return t.formatViewResultForCopy()
	case tools.EditToolName:
		return t.formatEditResultForCopy()
	case tools.MultiEditToolName:
		return t.formatMultiEditResultForCopy()
	case tools.WriteToolName:
		return t.formatWriteResultForCopy()
	case tools.FetchToolName:
		return t.formatFetchResultForCopy()
	case tools.AgenticFetchToolName:
		return t.formatAgenticFetchResultForCopy()
	case tools.WebFetchToolName:
		return t.formatWebFetchResultForCopy()
	case agent.AgentToolName, agent.AgentDispatchToolName:
		return t.formatAgentResultForCopy()
	case tools.DownloadToolName, tools.GrepToolName, tools.GlobToolName, tools.LSToolName, tools.SourcegraphToolName, tools.DiagnosticsToolName, tools.TodosToolName:
		return fmt.Sprintf("```\n%s\n```", t.result.Content)
	default:
		return t.result.Content
	}
}

// formatBashResultForCopy formats bash 工具 results 为 clipboard.
func (t *baseToolMessageItem) formatBashResultForCopy() string {
	if t.result == nil {
		return ""
	}

	var meta tools.BashResponseMetadata
	if t.result.Metadata != "" {
		json.Unmarshal([]byte(t.result.Metadata), &meta)
	}

	output := meta.Output
	if output == "" && t.result.Content != tools.BashNoOutput {
		output = t.result.Content
	}

	if output == "" {
		return ""
	}

	return fmt.Sprintf("```bash\n%s\n```", output)
}

// formatViewResultForCopy formats view 工具 results 为 clipboard.
func (t *baseToolMessageItem) formatViewResultForCopy() string {
	if t.result == nil {
		return ""
	}

	var meta tools.ViewResponseMetadata
	if t.result.Metadata != "" {
		json.Unmarshal([]byte(t.result.Metadata), &meta)
	}

	if meta.Content == "" {
		return t.result.Content
	}

	lang := ""
	if meta.FilePath != "" {
		ext := strings.ToLower(filepath.Ext(meta.FilePath))
		switch ext {
		case ".go":
			lang = "go"
		case ".js", ".mjs":
			lang = "javascript"
		case ".ts":
			lang = "typescript"
		case ".py":
			lang = "python"
		case ".rs":
			lang = "rust"
		case ".java":
			lang = "java"
		case ".c":
			lang = "c"
		case ".cpp", ".cc", ".cxx":
			lang = "cpp"
		case ".sh", ".bash":
			lang = "bash"
		case ".json":
			lang = "json"
		case ".yaml", ".yml":
			lang = "yaml"
		case ".xml":
			lang = "xml"
		case ".html":
			lang = "html"
		case ".css":
			lang = "css"
		case ".md":
			lang = "markdown"
		}
	}

	var result strings.Builder
	if lang != "" {
		fmt.Fprintf(&result, "```%s\n", lang)
	} else {
		result.WriteString("```\n")
	}
	result.WriteString(meta.Content)
	result.WriteString("\n```")

	return result.String()
}

// formatEditResultForCopy formats edit 工具 results 为 clipboard.
func (t *baseToolMessageItem) formatEditResultForCopy() string {
	if t.result == nil || t.result.Metadata == "" {
		if t.result != nil {
			return t.result.Content
		}
		return ""
	}

	var meta tools.EditResponseMetadata
	if json.Unmarshal([]byte(t.result.Metadata), &meta) != nil {
		return t.result.Content
	}

	var params tools.EditParams
	json.Unmarshal([]byte(t.toolCall.Input), &params)

	var result strings.Builder

	if meta.OldContent != "" || meta.NewContent != "" {
		fileName := params.FilePath
		if fileName != "" {
			fileName = fsext.PrettyPath(fileName)
		}
		diffContent, additions, removals := diff.GenerateDiff(meta.OldContent, meta.NewContent, fileName)

		fmt.Fprintf(&result, "Changes: +%d -%d\n", additions, removals)
		result.WriteString("```diff\n")
		result.WriteString(diffContent)
		result.WriteString("\n```")
	}

	return result.String()
}

// formatMultiEditResultForCopy formats multi-edit 工具 results 为 clipboard.
func (t *baseToolMessageItem) formatMultiEditResultForCopy() string {
	if t.result == nil || t.result.Metadata == "" {
		if t.result != nil {
			return t.result.Content
		}
		return ""
	}

	var meta tools.MultiEditResponseMetadata
	if json.Unmarshal([]byte(t.result.Metadata), &meta) != nil {
		return t.result.Content
	}

	var params tools.MultiEditParams
	json.Unmarshal([]byte(t.toolCall.Input), &params)

	var result strings.Builder
	if meta.OldContent != "" || meta.NewContent != "" {
		fileName := params.FilePath
		if fileName != "" {
			fileName = fsext.PrettyPath(fileName)
		}
		diffContent, additions, removals := diff.GenerateDiff(meta.OldContent, meta.NewContent, fileName)

		fmt.Fprintf(&result, "Changes: +%d -%d\n", additions, removals)
		result.WriteString("```diff\n")
		result.WriteString(diffContent)
		result.WriteString("\n```")
	}

	return result.String()
}

// formatWriteResultForCopy formats 写入 工具 results 为 clipboard.
func (t *baseToolMessageItem) formatWriteResultForCopy() string {
	if t.result == nil {
		return ""
	}

	var params tools.WriteParams
	if json.Unmarshal([]byte(t.toolCall.Input), &params) != nil {
		return t.result.Content
	}

	lang := ""
	if params.FilePath != "" {
		ext := strings.ToLower(filepath.Ext(params.FilePath))
		switch ext {
		case ".go":
			lang = "go"
		case ".js", ".mjs":
			lang = "javascript"
		case ".ts":
			lang = "typescript"
		case ".py":
			lang = "python"
		case ".rs":
			lang = "rust"
		case ".java":
			lang = "java"
		case ".c":
			lang = "c"
		case ".cpp", ".cc", ".cxx":
			lang = "cpp"
		case ".sh", ".bash":
			lang = "bash"
		case ".json":
			lang = "json"
		case ".yaml", ".yml":
			lang = "yaml"
		case ".xml":
			lang = "xml"
		case ".html":
			lang = "html"
		case ".css":
			lang = "css"
		case ".md":
			lang = "markdown"
		}
	}

	var result strings.Builder
	fmt.Fprintf(&result, "File: %s\n", fsext.PrettyPath(params.FilePath))
	if lang != "" {
		fmt.Fprintf(&result, "```%s\n", lang)
	} else {
		result.WriteString("```\n")
	}
	result.WriteString(params.Content)
	result.WriteString("\n```")

	return result.String()
}

// formatFetchResultForCopy formats fetch 工具 results 为 clipboard.
func (t *baseToolMessageItem) formatFetchResultForCopy() string {
	if t.result == nil {
		return ""
	}

	var params tools.FetchParams
	if json.Unmarshal([]byte(t.toolCall.Input), &params) != nil {
		return t.result.Content
	}

	var result strings.Builder
	if params.URL != "" {
		fmt.Fprintf(&result, "URL: %s\n", params.URL)
	}
	if params.Format != "" {
		fmt.Fprintf(&result, "Format: %s\n", params.Format)
	}
	if params.Timeout > 0 {
		fmt.Fprintf(&result, "Timeout: %ds\n", params.Timeout)
	}
	result.WriteString("\n")

	result.WriteString(t.result.Content)

	return result.String()
}

// formatAgenticFetchResultForCopy formats agentic fetch 工具 results 为 clipboard.
func (t *baseToolMessageItem) formatAgenticFetchResultForCopy() string {
	if t.result == nil {
		return ""
	}

	var params tools.AgenticFetchParams
	if json.Unmarshal([]byte(t.toolCall.Input), &params) != nil {
		return t.result.Content
	}

	var result strings.Builder
	if params.URL != "" {
		fmt.Fprintf(&result, "URL: %s\n", params.URL)
	}
	if params.Prompt != "" {
		fmt.Fprintf(&result, "Prompt: %s\n\n", params.Prompt)
	}

	result.WriteString("```markdown\n")
	result.WriteString(t.result.Content)
	result.WriteString("\n```")

	return result.String()
}

// formatWebFetchResultForCopy formats web fetch 工具 results 为 clipboard.
func (t *baseToolMessageItem) formatWebFetchResultForCopy() string {
	if t.result == nil {
		return ""
	}

	var params tools.WebFetchParams
	if json.Unmarshal([]byte(t.toolCall.Input), &params) != nil {
		return t.result.Content
	}

	var result strings.Builder
	fmt.Fprintf(&result, "URL: %s\n\n", params.URL)
	result.WriteString("```markdown\n")
	result.WriteString(t.result.Content)
	result.WriteString("\n```")

	return result.String()
}

// formatAgentResultForCopy formats agent 工具 results 为 clipboard.
func (t *baseToolMessageItem) formatAgentResultForCopy() string {
	if t.result == nil {
		return ""
	}

	var result strings.Builder

	if t.result.Content != "" {
		fmt.Fprintf(&result, "```markdown\n%s\n```", t.result.Content)
	}

	return result.String()
}

// prettifyToolName returns human-readable name 为 工具 names.
func prettifyToolName(name string) string {
	switch name {
	case agent.AgentToolName:
		return "Agent"
	case agent.AgentDispatchToolName:
		return "Create Subagent"
	case agent.AgentSendToolName:
		return "Message Subagent"
	case agent.AgentStatusToolName:
		return "Subagent Status"
	case agent.AgentStopToolName:
		return "Stop Subagent"
	case tools.BashToolName:
		return "Bash"
	case tools.JobOutputToolName:
		return "Job: Output"
	case tools.JobKillToolName:
		return "Job: Kill"
	case tools.DownloadToolName:
		return "Download"
	case tools.EditToolName:
		return "Edit"
	case tools.MultiEditToolName:
		return "Multi-Edit"
	case tools.FetchToolName:
		return "Fetch"
	case tools.AgenticFetchToolName:
		return "Agentic Fetch"
	case tools.WebFetchToolName:
		return "Fetch"
	case tools.WebSearchToolName:
		return "Search"
	case tools.GlobToolName:
		return "Glob"
	case tools.GrepToolName:
		return "Grep"
	case tools.LSToolName:
		return "List"
	case tools.SourcegraphToolName:
		return "Sourcegraph"
	case tools.TodosToolName:
		return "To-Do"
	case tools.ViewToolName:
		return "View"
	case tools.WriteToolName:
		return "Write"
	default:
		return humanizedToolName(name)
	}
}
