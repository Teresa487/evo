// rename.go —— UI 消息渲染：把后端来的消息/工具调用转成 TUI 可显示的项。
// 属于产物链的末端：db/message → Workspace.ListMessages → ui/chat → 屏幕。

package chat

import (
	"encoding/json"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// RenameToolMessageItem 是 消息 item 那 represents rename 工具调用.
type RenameToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*RenameToolMessageItem)(nil)

// NewRenameToolMessageItem creates a new [RenameToolMessageItem].
func NewRenameToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &RenameToolRenderContext{}, canceled)
}

// RenameToolRenderContext renders rename 工具 messages.
type RenameToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *RenameToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsStreamingInput() {
		return pendingTool(sty, "Rename Symbol", opts.Anim, opts.Compact)
	}

	var params tools.RenameParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	toolParams := []string{params.Symbol + " → " + params.NewName}
	if params.Path != "" {
		toolParams = append(toolParams, "path", fsext.PrettyPath(params.Path))
	}

	header := toolHeader(sty, opts.Status, "Rename Symbol", cappedWidth, opts, toolParams...)
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
