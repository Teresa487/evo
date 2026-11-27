// subagent.go —— UI 消息渲染：把后端来的消息/工具调用转成 TUI 可显示的项。
// 属于产物链的末端：db/message → Workspace.ListMessages → ui/chat → 屏幕。

package chat

import (
	"fmt"
	"strings"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// subAgentSeq provides unique IDs 为 SubAgentReportItems even 当 two
// subagents share a label across sessions.
var subAgentSeq atomic.Int64

// SubAgentReportItem renders 结果 detached 子代理 reported back.
// 它 reads 作为 工具调用 rather than 作为 消息: 子代理 finishing 是
// same kind 的 事件 作为 工具 returning, 和 transcript stays legible
// 当 every non-prose block shares one shape.
type SubAgentReportItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	id              string
	label           string
	model           string
	output          string
	failed          bool
	expandedContent bool
	sty             *styles.Styles
}

var _ MessageItem = (*SubAgentReportItem)(nil)

// NewSubAgentReportItem creates a [SubAgentReportItem].
func NewSubAgentReportItem(sty *styles.Styles, label, model, output string, failed bool) MessageItem {
	v := list.NewVersioned()
	return &SubAgentReportItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		id:                       fmt.Sprintf("subagent-%d-%s", subAgentSeq.Add(1), label),
		label:                    label,
		model:                    model,
		output:                   output,
		failed:                   failed,
		sty:                      sty,
	}
}

func (s *SubAgentReportItem) ID() string          { return s.id }
func (s *SubAgentReportItem) FilterValue() string { return s.label + " " + s.output }

// ToggleExpanded toggles between collapsed 和 full 报告.
func (s *SubAgentReportItem) ToggleExpanded() bool {
	s.expandedContent = !s.expandedContent
	s.clearCache()
	s.Bump()
	return s.expandedContent
}

// HandleMouseClick implements MouseClickable 所以 clicks select 这 item.
func (s *SubAgentReportItem) HandleMouseClick(btn ansi.MouseButton, x, y int) bool {
	return btn == ansi.MouseLeft
}

// HandleKeyEvent implements KeyEventHandler 为 copying 报告.
func (s *SubAgentReportItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "c", "y":
		return true, common.CopyToClipboard(ansi.Strip(s.output), "Subagent report copied to clipboard")
	}
	return false, nil
}

func (s *SubAgentReportItem) Render(width int) string {
	// Same left gutter 工具 items use, 所以 报告 lines up 用
	// 工具 calls around it instead 的 sitting 在 它的 own column.
	prefix := s.sty.Messages.ToolCallBlurred.Render()
	if s.focused {
		prefix = s.sty.Messages.ToolCallFocused.Render()
	}
	lines := strings.Split(s.RawRender(width-MessageLeftPaddingTotal), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	out := strings.Join(lines, "\n")
	return s.renderHighlighted(out, width, lipgloss.Height(out))
}

func (s *SubAgentReportItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	status := ToolStatusSuccess
	if s.failed {
		status = ToolStatusError
	}
	header := subagentHeader(s.sty, status, "Subagent Report", s.label, s.model, cappedWidth, nil)

	bodyWidth := cappedWidth - toolBodyLeftPaddingTotal
	// Already padded 到 sit under header 文本.
	body := toolOutputMarkdownContent(s.sty, s.output, bodyWidth, s.expandedContent)
	return joinToolParts(header, body)
}
