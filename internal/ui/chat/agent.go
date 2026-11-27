package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/tree"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// -----------------------------------------------------------------------------
// Agent 工具
// -----------------------------------------------------------------------------

// NestedToolContainer 是 interface 为 工具 items 那 can contain nested 工具 calls.
type NestedToolContainer interface {
	NestedTools() []ToolMessageItem
	SetNestedTools(tools []ToolMessageItem)
	AddNestedTool(tool ToolMessageItem)
}

// AgentToolMessageItem 是 消息 item 那 represents agent 工具调用.
type AgentToolMessageItem struct {
	*baseToolMessageItem

	nestedTools []ToolMessageItem

	// dispatchLabel 是 set 为 agent_dispatch calls, which return 作为 soon
	// 作为 子代理 starts. Such item keeps spinning until
	// 子代理's 报告 arrives 和 clears it, since 工具结果
	// landing says 只 那 子代理 launched, 不 那 it 是 done.
	dispatchLabel    string
	dispatchReported bool
	dispatchStarted  time.Time
	// dispatchReportedAt 是 当 报告 landed. sidebar retires
	// 子代理 那 has been finished while, 所以 section does 不
	// grow 为 whole 会话.
	dispatchReportedAt time.Time
}

var (
	_ ToolMessageItem     = (*AgentToolMessageItem)(nil)
	_ NestedToolContainer = (*AgentToolMessageItem)(nil)
)

// NewAgentToolMessageItem creates a new [AgentToolMessageItem].
func NewAgentToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) *AgentToolMessageItem {
	t := &AgentToolMessageItem{}
	t.baseToolMessageItem = newBaseToolMessageItem(sty, toolCall, result, &AgentToolRenderContext{agent: t}, canceled)
	t.parseDispatchLabel(toolCall)
	t.adoptDispatchName(result)
	// For agent 工具 we keep spinning until 工具调用 是 finished.
	// 一个 dispatched 子代理 runs 在 past 它的 工具结果, 所以 it spins
	// until 它的 报告 arrives instead.
	t.spinningFunc = func(state SpinningState) bool {
		if t.dispatchLabel != "" {
			return !t.dispatchSettled() && !state.IsCanceled()
		}
		return !state.HasResult() && !state.IsCanceled()
	}
	return t
}

// parseDispatchLabel reads 子代理's label out 的 agent_dispatch
// 调用. 工具调用 arrives 用 它的 input still streaming, 所以 label 是
// re-读取 在 every 更新 until it parses rather than once 在 construction.
func (a *AgentToolMessageItem) parseDispatchLabel(toolCall message.ToolCall) {
	if toolCall.Name != agent.AgentDispatchToolName || a.dispatchLabel != "" {
		return
	}
	var params agent.AgentDispatchParams
	if json.Unmarshal([]byte(toolCall.Input), &params) == nil && strings.TrimSpace(params.Label) != "" {
		a.dispatchLabel = strings.TrimSpace(params.Label)
		a.dispatchStarted = time.Now()
	}
}

// adoptDispatchName takes 子代理 name 结果 gave, which 是
// name 报告 arrives under. 一个 blocking agent 调用 那 moved 到
// background becomes dispatched 子代理 here.
func (a *AgentToolMessageItem) adoptDispatchName(result *message.ToolResult) {
	if result == nil || result.Metadata == "" {
		return
	}
	var meta struct {
		Label        string `json:"label"`
		Backgrounded bool   `json:"backgrounded"`
	}
	if json.Unmarshal([]byte(result.Metadata), &meta) != nil || meta.Label == "" {
		return
	}
	if a.dispatchLabel == "" && meta.Backgrounded {
		a.dispatchStarted = time.Now()
	}
	if a.dispatchLabel != "" || meta.Backgrounded {
		a.dispatchLabel = meta.Label
	}
}

// SetResult records 结果 和, 为 派发, name it returned.
func (a *AgentToolMessageItem) SetResult(res *message.ToolResult) {
	a.baseToolMessageItem.SetResult(res)
	a.adoptDispatchName(res)
}

// SetToolCall updates 工具调用, picking up 派发 label once
// streamed input 是 完成 enough 到 parse.
func (a *AgentToolMessageItem) SetToolCall(tc message.ToolCall) {
	a.baseToolMessageItem.SetToolCall(tc)
	a.parseDispatchLabel(tc)
}

// DispatchLabel returns label 的 子代理 这 item dispatched, 或
// empty string 为 synchronous agent 调用.
func (a *AgentToolMessageItem) DispatchLabel() string { return a.dispatchLabel }

// MarkDispatchRunning puts item back into 它的 working state after 它的
// 子代理 was reopened 用 follow-up.
func (a *AgentToolMessageItem) MarkDispatchRunning() {
	if !a.dispatchReported {
		return
	}
	a.dispatchReported = false
	a.dispatchStarted = time.Now()
	a.dispatchReportedAt = time.Time{}
	a.clearCache()
	a.Bump()
}

// MarkDispatchReported records 那 dispatched 子代理 reported back,
// which stops the item's spinner.
func (a *AgentToolMessageItem) MarkDispatchReported() {
	if a.dispatchReported {
		return
	}
	a.dispatchReported = true
	a.dispatchReportedAt = time.Now()
	a.clearCache()
	a.Bump()
}

// dispatchSettled reports whether dispatched 子代理 是 done: it reported
// back, 或 派发 was refused 和 nothing ever started.
func (a *AgentToolMessageItem) dispatchSettled() bool {
	return a.dispatchReported || (a.result != nil && a.result.IsError)
}

// Advance implements [Animatable].
//
// Advances parent's own spinner 和 every spinning nested 工具 在
// one frame, bumping parent's F6 list-缓存 version. Nested tools
// 是 不 list entries 的 their own — their IDs map 到 这 parent's
// index 在 idInxMap 和 their renders 是 embedded inline 在 这
// parent's output — 所以 list 只 checks parent's version.
// Without bump, list 缓存 would serve previously rendered
// frame indefinitely 和 spinner would appear frozen.
func (a *AgentToolMessageItem) Advance() bool {
	if a.Status() == ToolStatusCanceled {
		return false
	}
	// 一个 dispatched 子代理 keeps working after 它的 工具结果 lands, 所以
	// 它的 animation runs until 报告 arrives.
	if a.dispatchLabel == "" && a.result != nil {
		return false
	}
	if a.dispatchLabel != "" && a.dispatchSettled() {
		return false
	}
	changed := a.anim.Advance()
	changed = advanceNested(a.nestedTools) || changed
	if changed {
		a.Bump()
	}
	return changed
}

// restyleAnim implements [animRestyler]. 它 also reaches nested tools,
// which 是 不 list entries 的 their own 和 therefore never receive
// 调用 directly.
func (a *AgentToolMessageItem) restyleAnim() {
	a.baseToolMessageItem.restyleAnim()
	for _, nested := range a.nestedTools {
		if ar, ok := nested.(animRestyler); ok {
			ar.restyleAnim()
		}
	}
}

// advanceNested advances every spinning animatable 工具 在 tools 和
// reports whether any 的 them changed.
func advanceNested(tools []ToolMessageItem) bool {
	changed := false
	for _, nestedTool := range tools {
		if s, ok := nestedTool.(Animatable); ok && s.Spinning() && s.Advance() {
			changed = true
		}
	}
	return changed
}

// NestedTools returns the nested tools.
func (a *AgentToolMessageItem) NestedTools() []ToolMessageItem {
	return a.nestedTools
}

// SetNestedTools sets the nested tools.
//
// SetNestedTools always bumps the version. The previous design
// deduped 当 slice's length 和 element pointers were
// unchanged, 但 live 更新 路径 在 internal/ui/模型/ui.go
// mutates existing children 在 place (SetToolCall / SetResult 在
// same pointers) 和 then calls SetNestedTools 用 same slice.
// 指针-equality dedupe 在 那 case skips parent Bump even
// though the parent's rendered output (which embeds the children
// inline) has changed, leaving stale parent 条目 在 list
// 缓存. Always bumping 是 cheap (one uint64 increment) 和 called
// 在 most once per agent 事件; 在 rare case slice 是
// truly unchanged worst case 是 one extra parent re-render
// while every child 缓存 hit stays warm.
func (a *AgentToolMessageItem) SetNestedTools(tools []ToolMessageItem) {
	a.nestedTools = tools
	a.clearCache()
	a.Bump()
}

// AddNestedTool adds nested 工具.
func (a *AgentToolMessageItem) AddNestedTool(tool ToolMessageItem) {
	// 标记 nested tools 作为 simple (compact) rendering.
	if s, ok := tool.(Compactable); ok {
		s.SetCompact(true)
	}
	a.nestedTools = append(a.nestedTools, tool)
	a.clearCache()
	a.Bump()
}

// DispatchStatus reports what dispatched 子代理 是 up 到. second
// return 是 false 为 synchronous agent 调用 或 refused 派发.
func (a *AgentToolMessageItem) DispatchStatus() (DispatchStatus, bool) {
	if a.dispatchLabel == "" || (a.result != nil && a.result.IsError) {
		return DispatchStatus{}, false
	}
	status := DispatchStatus{
		Label:    a.dispatchLabel,
		Running:  !a.dispatchSettled() && a.Status() != ToolStatusCanceled,
		Started:  a.dispatchStarted,
		Finished: a.dispatchReportedAt,
	}
	return status, true
}

// DispatchStatus describes what dispatched 子代理 是 up 到.
type DispatchStatus struct {
	Label    string
	Running  bool
	Started  time.Time
	Finished time.Time // When its report landed; zero while running.
}

// AgentToolRenderContext renders agent 工具 messages.
type AgentToolRenderContext struct {
	agent *AgentToolMessageItem
}

// RenderTool implements the [ToolRenderer] interface.
func (r *AgentToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if !opts.ToolCall.Finished && !opts.IsCanceled() && len(r.agent.nestedTools) == 0 {
		return pendingTool(sty, prettifyToolName(opts.ToolCall.Name), opts.Anim, opts.Compact)
	}

	var params agent.AgentParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	prompt := params.Prompt
	if !opts.ExpandedContent {
		prompt = strings.ReplaceAll(prompt, "\n", " ")
	}

	// 一个 派发 是 named 为 子代理 it launched, 所以 它的 header
	// pairs 用 报告 item 那 arrives later under same label.
	var header string
	if r.agent.dispatchLabel != "" {
		header = subagentHeader(sty, opts.Status, prettifyToolName(opts.ToolCall.Name), r.agent.dispatchLabel, dispatchRunModel(opts), cappedWidth, opts)
	} else {
		header = toolHeader(sty, opts.Status, "Agent", cappedWidth, opts)
	}
	if opts.Compact {
		return header
	}

	// Build task tag 和 提示词.
	taskTag := sty.Tool.AgentTaskTag.Render("Task")
	taskTagWidth := lipgloss.Width(taskTag)

	// Calculate remaining width 为 提示词.
	remainingWidth := min(cappedWidth-taskTagWidth-3, maxTextWidth-taskTagWidth-3) // -3 for spacing

	promptText := sty.Tool.AgentPrompt.Width(remainingWidth).Render(prompt)

	header = lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		lipgloss.JoinHorizontal(
			lipgloss.Left,
			taskTag,
			" ",
			promptText,
		),
	)

	// Build tree 用 nested 工具 calls.
	childTools := tree.Root(header)

	addNestedTools(sty, childTools, r.agent.nestedTools, remainingWidth, opts.ExpandedContent)

	// Build parts.
	var parts []string
	parts = append(parts, roundedTree(childTools, taskTagWidth-5).String())

	// Show animation 如果 still running. 一个 派发 has 它的 结果
	// moment 子代理 starts, 所以 it animates until 报告 lands
	// instead.
	stillRunning := !opts.HasResult()
	if r.agent.dispatchLabel != "" {
		stillRunning = !r.agent.dispatchSettled()
	}
	if stillRunning && !opts.IsCanceled() {
		parts = append(parts, "", opts.Anim.Render())
	}

	result := lipgloss.JoinVertical(lipgloss.Left, parts...)

	// Add body content 当 completed. 一个 派发 结果 只 says
	// 子代理 started, which 是 bookkeeping rather than output; 它的 real
	// output arrives later 作为 它的 own 报告 item.
	if r.agent.dispatchLabel != "" {
		return result
	}
	if opts.HasResult() && opts.Result.Content != "" {
		body := toolOutputMarkdownContent(sty, opts.Result.Content, cappedWidth-toolBodyLeftPaddingTotal, opts.ExpandedContent)
		return joinToolParts(result, body)
	}

	return result
}

// -----------------------------------------------------------------------------
// Agentic Fetch 工具
// -----------------------------------------------------------------------------

// AgenticFetchToolMessageItem 是 消息 item 那 represents agentic fetch 工具调用.
type AgenticFetchToolMessageItem struct {
	*baseToolMessageItem

	nestedTools []ToolMessageItem
}

var (
	_ ToolMessageItem     = (*AgenticFetchToolMessageItem)(nil)
	_ NestedToolContainer = (*AgenticFetchToolMessageItem)(nil)
)

// NewAgenticFetchToolMessageItem creates a new [AgenticFetchToolMessageItem].
func NewAgenticFetchToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) *AgenticFetchToolMessageItem {
	t := &AgenticFetchToolMessageItem{}
	t.baseToolMessageItem = newBaseToolMessageItem(sty, toolCall, result, &AgenticFetchToolRenderContext{fetch: t}, canceled)
	// For agentic fetch 工具 we keep spinning until 工具调用 是 finished.
	t.spinningFunc = func(state SpinningState) bool {
		return !state.HasResult() && !state.IsCanceled()
	}
	return t
}

// Advance implements [Animatable]. See [AgentToolMessageItem.Advance]
// 为 parent-bump rationale; without override embedded base
// Advance would never advance the nested children.
func (a *AgenticFetchToolMessageItem) Advance() bool {
	if a.result != nil || a.Status() == ToolStatusCanceled {
		return false
	}
	changed := a.anim.Advance()
	changed = advanceNested(a.nestedTools) || changed
	if changed {
		a.Bump()
	}
	return changed
}

// restyleAnim implements [animRestyler]. See
// [AgentToolMessageItem.restyleAnim] 为 nested-工具 rationale.
func (a *AgenticFetchToolMessageItem) restyleAnim() {
	a.baseToolMessageItem.restyleAnim()
	for _, nested := range a.nestedTools {
		if ar, ok := nested.(animRestyler); ok {
			ar.restyleAnim()
		}
	}
}

// NestedTools returns the nested tools.
func (a *AgenticFetchToolMessageItem) NestedTools() []ToolMessageItem {
	return a.nestedTools
}

// SetNestedTools sets the nested tools. Always bumps the version;
// see [AgentToolMessageItem.SetNestedTools] 为 rationale.
func (a *AgenticFetchToolMessageItem) SetNestedTools(tools []ToolMessageItem) {
	a.nestedTools = tools
	a.clearCache()
	a.Bump()
}

// AddNestedTool adds nested 工具.
func (a *AgenticFetchToolMessageItem) AddNestedTool(tool ToolMessageItem) {
	// 标记 nested tools 作为 simple (compact) rendering.
	if s, ok := tool.(Compactable); ok {
		s.SetCompact(true)
	}
	a.nestedTools = append(a.nestedTools, tool)
	a.clearCache()
	a.Bump()
}

// AgenticFetchToolRenderContext renders agentic fetch 工具 messages.
type AgenticFetchToolRenderContext struct {
	fetch *AgenticFetchToolMessageItem
}

// agenticFetchParams matches tools.AgenticFetchParams.
type agenticFetchParams struct {
	URL    string `json:"url,omitempty"`
	Prompt string `json:"prompt"`
}

// RenderTool implements the [ToolRenderer] interface.
func (r *AgenticFetchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if !opts.ToolCall.Finished && !opts.IsCanceled() && len(r.fetch.nestedTools) == 0 {
		return pendingTool(sty, "Agentic Fetch", opts.Anim, opts.Compact)
	}

	var params agenticFetchParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	prompt := params.Prompt
	if !opts.ExpandedContent {
		prompt = strings.ReplaceAll(prompt, "\n", " ")
	}

	// Build header 用 optional URL param.
	var toolParams []string
	if params.URL != "" {
		toolParams = append(toolParams, params.URL)
	}

	if name := backgroundedName(opts); name != "" {
		toolParams = []string{subagentTarget(sty, name)}
	}
	header := toolHeader(sty, opts.Status, "Agentic Fetch", cappedWidth, opts, toolParams...)
	if opts.Compact {
		return header
	}

	// Build 提示词 tag.
	promptTag := sty.Tool.AgenticFetchPromptTag.Render("Prompt")
	promptTagWidth := lipgloss.Width(promptTag)

	// Calculate remaining width 为 提示词 文本.
	remainingWidth := min(cappedWidth-promptTagWidth-3, maxTextWidth-promptTagWidth-3) // -3 for spacing

	promptText := sty.Tool.AgentPrompt.Width(remainingWidth).Render(prompt)

	header = lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		lipgloss.JoinHorizontal(
			lipgloss.Left,
			promptTag,
			" ",
			promptText,
		),
	)

	// Build tree 用 nested 工具 calls.
	childTools := tree.Root(header)

	addNestedTools(sty, childTools, r.fetch.nestedTools, remainingWidth, opts.ExpandedContent)

	// Build parts.
	var parts []string
	parts = append(parts, roundedTree(childTools, promptTagWidth-5).String())

	// Show animation 如果 still running.
	if !opts.HasResult() && !opts.IsCanceled() {
		parts = append(parts, "", opts.Anim.Render())
	}

	result := lipgloss.JoinVertical(lipgloss.Left, parts...)

	// Add body content 当 completed.
	if opts.HasResult() && opts.Result.Content != "" {
		body := toolOutputMarkdownContent(sty, opts.Result.Content, cappedWidth-toolBodyLeftPaddingTotal, opts.ExpandedContent)
		return joinToolParts(result, body)
	}

	return result
}

// collapsedNestedTools 是 how many 的 子代理's latest 工具 calls show
// while 它的 item 是 collapsed. Drawing every 调用 made busy 子代理's
// item slower 在 each frame 和 taller than screen.
const collapsedNestedTools = 10

// addNestedTools adds 子代理's 工具 calls 到 它的 tree, 只 latest
// ones unless item 是 expanded.
func addNestedTools(sty *styles.Styles, root *tree.Tree, nested []ToolMessageItem, width int, expanded bool) {
	if hidden := len(nested) - collapsedNestedTools; hidden > 0 && !expanded {
		root.Child(sty.Tool.ParamMain.Render(fmt.Sprintf("… %d earlier tool calls", hidden)))
		nested = nested[hidden:]
	}
	for _, tool := range nested {
		root.Child(tool.Render(width))
	}
}

// subagentHeader renders "✓ 创建 子代理 → Betty date-impl (模型=hyper/glm-5)", truncating 模型 rather than dropping it.
func subagentHeader(sty *styles.Styles, status ToolStatus, name, label, model string, width int, opts *ToolRenderOpts) string {
	header := toolHeader(sty, status, name, width, opts, subagentTarget(sty, label))
	const minModelWidth = 12
	room := width - lipgloss.Width(header) - 1
	if model == "" || room < minModelWidth {
		return header
	}
	return header + " " + sty.Tool.ParamMain.Render(ansi.Truncate("(model="+model+")", room, "…)"))
}

// SubagentReportTitle renders "子代理 报告 → Name label" 作为 报告's
// header does, 为 places 那 show 报告 without 它的 status icon.
func SubagentReportTitle(sty *styles.Styles, label string) string {
	return sty.Tool.NameNormal.Render("Subagent Report") + " " + subagentTarget(sty, label)
}

// subagentTarget renders "→ Name label", how every 子代理 header names one.
func subagentTarget(sty *styles.Styles, label string) string {
	return sty.Tool.MCPArrow.String() + " " + RenderSubagentName(sty, label)
}

// RenderSubagentName renders accented first name apart 从 label.
func RenderSubagentName(sty *styles.Styles, label string) string {
	first, rest := agent.SubagentDisplayName(label)
	if first == "" {
		return sty.Tool.AgentLabel.Render(rest)
	}
	return sty.Tool.AgentName.Render(first) + " " + sty.Tool.AgentLabel.Render(rest)
}

// backgroundedName 是 name blocking 调用 took 当 it was backgrounded.
func backgroundedName(opts *ToolRenderOpts) string {
	if !opts.HasResult() || opts.Result.Metadata == "" {
		return ""
	}
	var meta agent.AgentBackgroundMetadata
	if json.Unmarshal([]byte(opts.Result.Metadata), &meta) != nil || !meta.Backgrounded {
		return ""
	}
	return meta.Label
}

// dispatchRunModel reads 模型 派发 recorded once 它的 子代理 started.
func dispatchRunModel(opts *ToolRenderOpts) string {
	var meta agent.AgentDispatchResponseMetadata
	if opts.HasResult() {
		_ = json.Unmarshal([]byte(opts.Result.Metadata), &meta)
	}
	return meta.RunModel
}
