package chat

import (
	"encoding/json"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// ReplaceSymbolToolMessageItem 是 消息 item 那 represents replace symbol 工具调用.
type ReplaceSymbolToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*ReplaceSymbolToolMessageItem)(nil)

// NewReplaceSymbolToolMessageItem creates a new [ReplaceSymbolToolMessageItem].
func NewReplaceSymbolToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &ReplaceSymbolToolRenderContext{}, canceled)
}

// ReplaceSymbolToolRenderContext renders replace symbol 工具 messages.
type ReplaceSymbolToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *ReplaceSymbolToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	// Replace symbol uses full width 为 diffs, like edit.
	if opts.IsStreamingInput() {
		return pendingTool(sty, "Replace Symbol", opts.Anim, opts.Compact)
	}

	var params tools.ReplaceSymbolParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	file := fsext.PrettyPath(params.FilePath)
	header := toolHeader(sty, opts.Status, "Replace Symbol", width, opts, params.Symbol, file)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}

	if !opts.HasResult() {
		return header
	}

	// Try 到 render 作为 diff using metadata.
	var meta tools.ReplaceSymbolResponseMetadata
	if err := json.Unmarshal([]byte(opts.Result.Metadata), &meta); err == nil && meta.OldContent != "" || meta.NewContent != "" {
		diff := toolOutputDiffContent(sty, file, meta.OldContent, meta.NewContent, width, opts.ExpandedContent)

		// On 错误, show 错误 above diff.
		if opts.Result.IsError {
			errLine := toolErrorContent(sty, opts.Result, width)
			return joinToolParts(header, errLine+"\n"+diff)
		}

		return joinToolParts(header, diff)
	}

	// Fallback 到 plain 文本 如果 no metadata.
	bodyWidth := width - toolBodyLeftPaddingTotal
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
	return joinToolParts(header, body)
}
