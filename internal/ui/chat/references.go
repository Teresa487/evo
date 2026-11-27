package chat

import (
	"encoding/json"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// ReferencesToolMessageItem 是 消息 item 那 represents references 工具调用.
type ReferencesToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*ReferencesToolMessageItem)(nil)

// NewReferencesToolMessageItem creates a new [ReferencesToolMessageItem].
func NewReferencesToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &ReferencesToolRenderContext{}, canceled)
}

// ReferencesToolRenderContext renders references 工具 messages.
type ReferencesToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *ReferencesToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsStreamingInput() {
		return pendingTool(sty, "Find References", opts.Anim, opts.Compact)
	}

	var params tools.ReferencesParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	toolParams := []string{params.Symbol}
	if params.Path != "" {
		toolParams = append(toolParams, "path", fsext.PrettyPath(params.Path))
	}

	header := toolHeader(sty, opts.Status, "Find References", cappedWidth, opts, toolParams...)
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
