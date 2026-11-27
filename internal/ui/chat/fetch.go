package chat

import (
	"encoding/json"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// -----------------------------------------------------------------------------
// Fetch 工具
// -----------------------------------------------------------------------------

// FetchToolMessageItem 是 消息 item 那 represents fetch 工具调用.
type FetchToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*FetchToolMessageItem)(nil)

// NewFetchToolMessageItem creates a new [FetchToolMessageItem].
func NewFetchToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &FetchToolRenderContext{}, canceled)
}

// FetchToolRenderContext renders fetch 工具 messages.
type FetchToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (f *FetchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsStreamingInput() {
		return pendingTool(sty, "Fetch", opts.Anim, opts.Compact)
	}

	var params tools.FetchParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	toolParams := []string{params.URL}
	if params.Format != "" {
		toolParams = append(toolParams, "format", params.Format)
	}
	if params.Timeout != 0 {
		toolParams = append(toolParams, "timeout", formatTimeout(params.Timeout))
	}

	header := toolHeader(sty, opts.Status, "Fetch", cappedWidth, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	// Determine 文件 extension 为 syntax highlighting based 在 format.
	file := getFileExtensionForFormat(params.Format)
	body := toolOutputCodeContent(sty, file, opts.Result.Content, 0, cappedWidth, opts.ExpandedContent)
	return joinToolParts(header, body)
}

// getFileExtensionForFormat returns filename 用 appropriate extension 为 syntax highlighting.
func getFileExtensionForFormat(format string) string {
	switch format {
	case "text":
		return "fetch.txt"
	case "html":
		return "fetch.html"
	default:
		return "fetch.md"
	}
}

// -----------------------------------------------------------------------------
// WebFetch 工具
// -----------------------------------------------------------------------------

// WebFetchToolMessageItem 是 消息 item 那 represents web_fetch 工具调用.
type WebFetchToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*WebFetchToolMessageItem)(nil)

// NewWebFetchToolMessageItem creates a new [WebFetchToolMessageItem].
func NewWebFetchToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &WebFetchToolRenderContext{}, canceled)
}

// WebFetchToolRenderContext renders web_fetch 工具 messages.
type WebFetchToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (w *WebFetchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsStreamingInput() {
		return pendingTool(sty, "Fetch", opts.Anim, opts.Compact)
	}

	var params tools.WebFetchParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	toolParams := []string{params.URL}
	header := toolHeader(sty, opts.Status, "Fetch", cappedWidth, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	body := toolOutputMarkdownContent(sty, opts.Result.Content, cappedWidth, opts.ExpandedContent)
	return joinToolParts(header, body)
}

// -----------------------------------------------------------------------------
// WebSearch 工具
// -----------------------------------------------------------------------------

// WebSearchToolMessageItem 是 消息 item 那 represents web_search 工具调用.
type WebSearchToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*WebSearchToolMessageItem)(nil)

// NewWebSearchToolMessageItem creates a new [WebSearchToolMessageItem].
func NewWebSearchToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &WebSearchToolRenderContext{}, canceled)
}

// WebSearchToolRenderContext renders web_search 工具 messages.
type WebSearchToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (w *WebSearchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsStreamingInput() {
		return pendingTool(sty, "Search", opts.Anim, opts.Compact)
	}

	var params tools.WebSearchParams
	if err := json.Unmarshal([]byte(opts.ToolCall.Input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	toolParams := []string{params.Query}
	header := toolHeader(sty, opts.Status, "Search", cappedWidth, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	body := toolOutputMarkdownContent(sty, opts.Result.Content, cappedWidth, opts.ExpandedContent)
	return joinToolParts(header, body)
}
