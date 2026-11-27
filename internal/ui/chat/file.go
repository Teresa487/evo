// file.go —— UI 消息渲染：把后端来的消息/工具调用转成 TUI 可显示的项。
// 属于产物链的末端：db/message → Workspace.ListMessages → ui/chat → 屏幕。

package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// -----------------------------------------------------------------------------
// View 工具
// -----------------------------------------------------------------------------

// ViewToolMessageItem 是 消息 item 那 represents view 工具调用.
type ViewToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*ViewToolMessageItem)(nil)

// NewViewToolMessageItem creates a new [ViewToolMessageItem].
func NewViewToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &ViewToolRenderContext{}, canceled)
}

// ViewToolRenderContext renders view 工具 messages.
type ViewToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (v *ViewToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsStreamingInput() {
		return pendingTool(sty, "View", opts.Anim, opts.Compact)
	}

	var params tools.ViewParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	file := fsext.PrettyPath(params.FilePath)
	toolParams := []string{file}
	if params.Limit != 0 {
		toolParams = append(toolParams, "limit", fmt.Sprintf("%d", params.Limit))
	}
	if params.Offset != 0 {
		toolParams = append(toolParams, "offset", fmt.Sprintf("%d", params.Offset))
	}

	header := toolHeader(sty, opts.Status, "View", cappedWidth, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if !opts.HasResult() {
		return header
	}

	// Handle 图片 content.
	if opts.Result.Data != "" && strings.HasPrefix(opts.Result.MIMEType, "image/") {
		body := toolOutputImageContent(sty, opts.Result.Data, opts.Result.MIMEType)
		return joinToolParts(header, body)
	}

	// Try 到 get content 从 metadata first (contains actual 文件 content).
	var meta tools.ViewResponseMetadata
	content := opts.Result.Content
	if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err == nil && meta.Content != "" {
		content = meta.Content
	}

	// Handle skill content.
	if meta.ResourceType == tools.ViewResourceSkill {
		body := toolOutputSkillContent(sty, meta.ResourceName, meta.ResourceDescription)
		return joinToolParts(header, body)
	}

	if content == "" {
		return header
	}

	// Render code content 用 syntax highlighting.
	body := toolOutputCodeContent(sty, params.FilePath, content, params.Offset, cappedWidth, opts.ExpandedContent)
	return joinToolParts(header, body)
}

// -----------------------------------------------------------------------------
// 写入 工具
// -----------------------------------------------------------------------------

// WriteToolMessageItem 是 消息 item 那 represents 写入 工具调用.
type WriteToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*WriteToolMessageItem)(nil)

// NewWriteToolMessageItem creates a new [WriteToolMessageItem].
func NewWriteToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &WriteToolRenderContext{}, canceled)
}

// WriteToolRenderContext renders 写入 工具 messages.
type WriteToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (w *WriteToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsStreamingInput() {
		return pendingTool(sty, "Write", opts.Anim, opts.Compact)
	}

	var params tools.WriteParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	file := fsext.PrettyPath(params.FilePath)
	header := toolHeader(sty, opts.Status, "Write", cappedWidth, opts, file)
	if opts.Compact {
		return header
	}

	if !opts.HasResult() {
		if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
			return joinToolParts(header, earlyState)
		}
		return header
	}

	// On 错误 用 diff metadata (e.g. denied 权限), show 错误 + diff.
	if opts.Result.IsError {
		var meta tools.WriteResponseMetadata
		if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err == nil && meta.Diff != "" {
			errLine := toolErrorContent(sty, opts.Result, cappedWidth)
			diff := toolOutputDiffContentFromUnified(sty, meta.Diff, cappedWidth, opts.ExpandedContent)
			return strings.Join([]string{header, "", errLine, "", diff}, "\n")
		}
		return joinToolParts(header, toolErrorContent(sty, opts.Result, cappedWidth))
	}

	// Render code content 用 syntax highlighting.
	if params.Content != "" {
		body := toolOutputCodeContent(sty, params.FilePath, params.Content, 0, cappedWidth, opts.ExpandedContent)
		return joinToolParts(header, body)
	}

	return header
}

// -----------------------------------------------------------------------------
// Edit 工具
// -----------------------------------------------------------------------------

// EditToolMessageItem 是 消息 item 那 represents edit 工具调用.
type EditToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*EditToolMessageItem)(nil)

// NewEditToolMessageItem creates a new [EditToolMessageItem].
func NewEditToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &EditToolRenderContext{}, canceled)
}

// EditToolRenderContext renders edit 工具 messages.
type EditToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (e *EditToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	// Edit 工具 uses full width 为 diffs.
	if opts.IsStreamingInput() {
		return pendingTool(sty, "Edit", opts.Anim, opts.Compact)
	}

	var params tools.EditParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, width)
	}

	file := fsext.PrettyPath(params.FilePath)
	header := toolHeader(sty, opts.Status, "Edit", width, opts, file)
	if opts.Compact {
		return header
	}

	if !opts.HasResult() {
		if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
			return joinToolParts(header, earlyState)
		}
		return header
	}

	// Get diff content 从 metadata.
	var meta tools.EditResponseMetadata
	if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err != nil {
		bodyWidth := width - toolBodyLeftPaddingTotal
		body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
		return joinToolParts(header, body)
	}

	diff := toolOutputDiffContent(sty, file, meta.OldContent, meta.NewContent, width, opts.ExpandedContent)

	// On 错误 (e.g. denied 权限), show 错误 above diff. An
	// edit can also fail before there 是 any diff 到 show — refusing 到 touch
	// 文件 那 was never 读取, 为 one — 和 then metadata carries no
	// content. Rendering diff 的 nothing still emits 它的 padding, which
	// reads 作为 stray blank line hanging under 错误.
	if opts.Result.IsError {
		errLine := toolErrorContent(sty, opts.Result, width)
		if meta.OldContent == "" && meta.NewContent == "" {
			return joinToolParts(header, errLine)
		}
		return strings.Join([]string{header, "", errLine, "", diff}, "\n")
	}

	return joinToolParts(header, diff)
}

// -----------------------------------------------------------------------------
// MultiEdit 工具
// -----------------------------------------------------------------------------

// MultiEditToolMessageItem 是 消息 item 那 represents multi-edit 工具调用.
type MultiEditToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*MultiEditToolMessageItem)(nil)

// NewMultiEditToolMessageItem creates a new [MultiEditToolMessageItem].
func NewMultiEditToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &MultiEditToolRenderContext{}, canceled)
}

// MultiEditToolRenderContext renders multi-edit 工具 messages.
type MultiEditToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (m *MultiEditToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	// MultiEdit 工具 uses full width 为 diffs.
	if opts.IsStreamingInput() {
		return pendingTool(sty, "Multi-Edit", opts.Anim, opts.Compact)
	}

	var params tools.MultiEditParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, width)
	}

	file := fsext.PrettyPath(params.FilePath)
	toolParams := []string{file}
	if len(params.Edits) > 0 {
		toolParams = append(toolParams, "edits", fmt.Sprintf("%d", len(params.Edits)))
	}

	header := toolHeader(sty, opts.Status, "Multi-Edit", width, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if !opts.HasResult() {
		if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
			return joinToolParts(header, earlyState)
		}
		return header
	}

	// Get diff content 从 metadata.
	var meta tools.MultiEditResponseMetadata
	if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err != nil {
		bodyWidth := width - toolBodyLeftPaddingTotal
		body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
		return joinToolParts(header, body)
	}

	// Render diff 用 optional failed edits note.
	diff := toolOutputMultiEditDiffContent(sty, file, meta, len(params.Edits), width, opts.ExpandedContent)

	// On 错误 (e.g. denied 权限), show 错误 above diff. An
	// edit can also fail before there 是 any diff 到 show — refusing 到 touch
	// 文件 那 was never 读取, 为 one — 和 then metadata carries no
	// content. Rendering diff 的 nothing still emits 它的 padding, which
	// reads 作为 stray blank line hanging under 错误.
	if opts.Result.IsError {
		errLine := toolErrorContent(sty, opts.Result, width)
		if meta.OldContent == "" && meta.NewContent == "" {
			return joinToolParts(header, errLine)
		}
		return strings.Join([]string{header, "", errLine, "", diff}, "\n")
	}

	return joinToolParts(header, diff)
}

// -----------------------------------------------------------------------------
// Download 工具
// -----------------------------------------------------------------------------

// DownloadToolMessageItem 是 消息 item 那 represents download 工具调用.
type DownloadToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*DownloadToolMessageItem)(nil)

// NewDownloadToolMessageItem creates a new [DownloadToolMessageItem].
func NewDownloadToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &DownloadToolRenderContext{}, canceled)
}

// DownloadToolRenderContext renders download 工具 messages.
type DownloadToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (d *DownloadToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsStreamingInput() {
		return pendingTool(sty, "Download", opts.Anim, opts.Compact)
	}

	var params tools.DownloadParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	toolParams := []string{params.URL}
	if params.FilePath != "" {
		toolParams = append(toolParams, "file_path", fsext.PrettyPath(params.FilePath))
	}
	if params.Timeout != 0 {
		toolParams = append(toolParams, "timeout", formatTimeout(params.Timeout))
	}

	header := toolHeader(sty, opts.Status, "Download", cappedWidth, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	bodyWidth := cappedWidth - toolBodyLeftPaddingTotal
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
	return joinToolParts(header, body)
}
