package chat

import (
	"fmt"
	"strings"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/crush/internal/ui/anim"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// shellSeq provides unique IDs 为 ShellItems even 当 same
// command 是 运行 multiple times.
var shellSeq atomic.Int64

const (
	shellMaxCollapsedLines = 10
	shellHScrollStep       = 5
)

// ShellItem renders bang-mode shell command 结果 在 chat 用
// vertical bar 在 left 和 plain-文本 output.
type ShellItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	id              string
	command         string
	output          strings.Builder
	exitCode        int
	expandedContent bool
	xOffset         int
	maxLineWidth    int // computed during render, used to clamp xOffset
	sty             *styles.Styles
	pending         bool
	anim            *anim.Anim
}

var (
	_ Expandable         = (*ShellItem)(nil)
	_ list.Highlightable = (*ShellItem)(nil)
	_ KeyEventHandler    = (*ShellItem)(nil)
	_ Animatable         = (*ShellItem)(nil)
)

// NewShellItem creates new ShellItem 为 displaying bang-mode results.
func NewShellItem(sty *styles.Styles, command, output string, exitCode int) MessageItem {
	v := list.NewVersioned()
	s := &ShellItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		id:                       fmt.Sprintf("shell-%d-%s", shellSeq.Add(1), command),
		command:                  command,
		exitCode:                 exitCode,
		sty:                      sty,
	}
	s.replaceOutput(output)
	return s
}

// NewPendingShellItem creates ShellItem 在 pending/running state 那
// displays spinner until 完成 是 called 用 results.
func NewPendingShellItem(sty *styles.Styles, command string) *ShellItem {
	v := list.NewVersioned()
	id := fmt.Sprintf("shell-%d-%s", shellSeq.Add(1), command)
	s := &ShellItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		id:                       id,
		command:                  command,
		sty:                      sty,
		pending:                  true,
	}
	s.anim = anim.New(anim.Settings{
		ID:         id,
		Label:      "Running",
		LabelColor: sty.WorkingLabelColor,
		GradColorA: sty.WorkingGradFromColor,
		GradColorB: sty.WorkingGradToColor,
		NoScramble: true,
	})
	return s
}

// 完成 transitions pending ShellItem 到 finished state 用 output.
func (s *ShellItem) Complete(output string, exitCode int) {
	s.replaceOutput(output)
	s.exitCode = exitCode
	s.pending = false
	s.Bump()
}

// AppendOutput appends incremental output 到 pending ShellItem.
func (s *ShellItem) AppendOutput(chunk string) {
	if !s.pending {
		return
	}
	s.output.WriteString(chunk)
	s.Bump()
}

func (s *ShellItem) replaceOutput(output string) {
	s.output.Reset()
	s.output.Grow(len(output))
	s.output.WriteString(output)
}

func (s *ShellItem) ID() string          { return s.id }
func (s *ShellItem) FilterValue() string { return s.command }

// Spinning implements [Animatable].
func (s *ShellItem) Spinning() bool {
	return s.pending
}

// restyleAnim implements [animRestyler].
func (s *ShellItem) restyleAnim() {
	if s.anim == nil {
		return
	}
	s.anim.SetColors(s.sty.WorkingLabelColor, s.sty.WorkingGradFromColor, s.sty.WorkingGradToColor, nil)
}

// Advance advances spinner animation 为 pending shell items.
func (s *ShellItem) Advance() bool {
	if !s.pending || !s.anim.Advance() {
		return false
	}
	s.Bump()
	return true
}

func (s *ShellItem) Render(width int) string {
	innerWidth := max(0, width-MessageLeftPaddingTotal)
	content := s.RawRender(innerWidth)

	var prefix string
	if s.focused {
		prefix = s.sty.Messages.ShellBarFocused.Render()
	} else {
		prefix = s.sty.Messages.ShellBarBlurred.Render()
	}
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	out := strings.Join(lines, "\n")

	return s.renderHighlighted(out, width, lipgloss.Height(out))
}

// HandleMouseClick implements MouseClickable 所以 clicks select 这 item.
func (s *ShellItem) HandleMouseClick(btn ansi.MouseButton, x, y int) bool {
	return btn == ansi.MouseLeft
}

// HandleKeyEvent implements KeyEventHandler 为 copy 和 horizontal scrolling.
func (s *ShellItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	switch k := key.String(); k {
	case "c", "y":
		text := "$ " + s.command + "\n" + ansi.Strip(s.output.String())
		return true, common.CopyToClipboard(text, "Shell output copied to clipboard")
	case "shift+left", "H":
		if s.xOffset > 0 {
			s.xOffset = max(0, s.xOffset-shellHScrollStep)
			s.Bump()
			return true, nil
		}
	case "shift+right", "L":
		s.xOffset = min(s.xOffset+shellHScrollStep, s.maxLineWidth)
		s.Bump()
		return true, nil
	}
	return false, nil
}

// ScrollHorizontal adjusts horizontal scroll offset 通过 delta columns.
func (s *ShellItem) ScrollHorizontal(delta int) {
	s.xOffset = min(max(0, s.xOffset+delta), s.maxLineWidth)
	s.Bump()
}

// ToggleExpanded toggles expanded state 和 invalidates 缓存.
func (s *ShellItem) ToggleExpanded() bool {
	s.expandedContent = !s.expandedContent
	s.Bump()
	return s.expandedContent
}

func (s *ShellItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	cmd := strings.ReplaceAll(s.command, "\n", " ")
	cmd = strings.ReplaceAll(cmd, "\t", "    ")

	var prompt string
	if s.focused {
		prompt = s.sty.Messages.ShellPrompt.Render("$")
	} else {
		prompt = s.sty.Messages.ShellPromptBlurred.Render("$")
	}

	highlighted := s.sty.Messages.ShellCommand.Render(cmd)
	header := prompt + " " + highlighted

	if s.pending {
		if s.output.Len() == 0 {
			// Nothing streamed yet: show spinner under 请求头.
			return header + "\n" + s.anim.Render()
		}
	} else if s.exitCode != 0 {
		header += " " + s.sty.Messages.ShellExitCode.Render(fmt.Sprintf("(exit %d)", s.exitCode))
	}

	if s.output.Len() == 0 {
		return header
	}

	// Remap raw ANSI 16-color codes onto legible Charmtone colors 所以
	// dark 终态 defaults don't render illegibly 在 Crush's
	// 后台.
	// Strip trailing whitespace 和 bare ANSI resets before remapping.
	// Programs like `task` emit "\x1b[0m\n" after their last line 的
	// output; trimming 只 "\n" misses these because reset bytes
	// sit between content 和 newline.
	fullOutput := s.output.String()
	raw := fullOutput
	for {
		trimmed := strings.TrimRight(raw, " \t\r\n")
		trimmed = strings.TrimSuffix(trimmed, "\x1b[0m")
		if trimmed == raw {
			break
		}
		raw = trimmed
	}
	if raw == "" {
		return header
	}

	// Count lines 从 trimmed output directly 所以 truncation
	// logic cannot drift 从 trimming loop above.
	totalLines := strings.Count(raw, "\n") + 1
	truncatedCount := 0
	if !s.expandedContent && totalLines > shellMaxCollapsedLines {
		truncatedCount = totalLines - shellMaxCollapsedLines
		if s.pending {
			raw = lastLines(raw, shellMaxCollapsedLines)
		} else {
			raw = firstLines(raw, shellMaxCollapsedLines)
		}
	}

	// Normalize CRLF before stripping cursor controls 所以 bare CR still
	// resets 它的 line 当 followed 通过 erase 序号.
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = common.StripCursorControl(raw)
	output := common.RemapANSI16(raw, s.sty.ANSI)
	lines := strings.Split(output, "\n")

	// Compute max line width 为 scroll clamping.
	maxW := 0
	for _, ln := range lines {
		w := ansi.StringWidth(ln)
		if w > maxW {
			maxW = w
		}
	}
	s.maxLineWidth = max(0, maxW-cappedWidth)
	// 一个 wider item 或 collapsed output can leave offset past
	// widest line, which would scroll every line out 的 view.
	s.xOffset = min(s.xOffset, s.maxLineWidth)

	var body strings.Builder

	// When streaming, hidden lines 是 above visible tail, 所以 show
	// the "more lines" notice before the output.
	if truncatedCount > 0 && s.pending {
		body.WriteString(s.sty.Messages.ShellTruncation.Render(
			fmt.Sprintf("… %d earlier lines", truncatedCount),
		))
		body.WriteString("\n")
	}

	for _, ln := range lines {
		scrolled := ansi.GraphemeWidth.Cut(ln, s.xOffset, len(ln))
		truncated := ansi.Truncate(scrolled, cappedWidth, "…")
		if s.xOffset > 0 && strings.TrimSpace(truncated) != "" {
			truncated = "…" + truncated
		}
		body.WriteString(s.sty.Messages.ShellOutput.Render(truncated))
		body.WriteString("\n")
	}

	// When finished, hidden lines 是 below, 所以 show notice after.
	if truncatedCount > 0 && !s.pending && !s.expandedContent {
		body.WriteString(s.sty.Messages.ShellTruncation.Render(
			fmt.Sprintf("… %d more lines", truncatedCount),
		))
		return header + "\n" + body.String()
	}

	result := header + "\n" + strings.TrimRight(body.String(), "\n")

	// While streaming, keep the spinner pinned below the latest output.
	if s.pending {
		result += "\n" + s.anim.Render()
	}

	return result
}

func firstLines(s string, count int) string {
	if count <= 0 {
		return ""
	}
	end := 0
	for i := range count {
		next := strings.IndexByte(s[end:], '\n')
		if next < 0 {
			return s
		}
		end += next
		if i == count-1 {
			// Remove both bytes 如果 delimiter 是 CRLF.
			return strings.TrimSuffix(s[:end], "\r")
		}
		end++
	}
	return s
}

func lastLines(s string, count int) string {
	if count <= 0 {
		return ""
	}
	start := len(s)
	for range count {
		previous := strings.LastIndexByte(s[:start], '\n')
		if previous < 0 {
			return s
		}
		start = previous
	}
	return s[start+1:]
}
