package model

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

const (
	// pillHeightWithBorder 是 height 的 pill including 它的 border.
	pillHeightWithBorder = 3
	// maxTaskDisplayLength 是 maximum length 的 task name 在 pill.
	maxTaskDisplayLength = 40
	// maxQueueDisplayLength 是 maximum length 的 队列 item 在 list.
	maxQueueDisplayLength = 60
)

// pillSection represents which section 的 pills panel 是 focused.
type pillSection int

const (
	pillSectionTodos pillSection = iota
	pillSectionQueue
)

// hasIncompleteTodos returns true 如果 there 是 any non-completed todos.
func hasIncompleteTodos(todos []session.Todo) bool {
	return session.HasIncompleteTodos(todos)
}

// hasInProgressTodo returns true 如果 there 是 在 least one 在-progress todo.
func hasInProgressTodo(todos []session.Todo) bool {
	for _, todo := range todos {
		if todo.Status == session.TodoStatusInProgress {
			return true
		}
	}
	return false
}

// queuePill renders 队列 count pill 用 gradient triangles. Pills always
// render 用 border; focus within expanded panel 是 conveyed 通过 list
// shown below pills, 不 通过 hiding pill's border.
func queuePill(queue int, t *styles.Styles) string {
	if queue <= 0 {
		return ""
	}
	triangles := styles.ForegroundGrad(t.Pills.QueueIconBase, "▶▶▶▶▶▶▶▶▶", false, t.Pills.QueueGradFromColor, t.Pills.QueueGradToColor)
	if queue < len(triangles) {
		triangles = triangles[:queue]
	}

	text := t.Pills.QueueLabel.Render(fmt.Sprintf("%d Queued", queue))
	content := fmt.Sprintf("%s %s", strings.Join(triangles, ""), text)
	return t.Pills.Focused.Render(content)
}

// todoPill renders todo progress pill 用 optional spinner 和 task name.
func todoPill(todos []session.Todo, spinnerView string, panelFocused bool, t *styles.Styles) string {
	if !hasIncompleteTodos(todos) {
		return ""
	}

	completed := 0
	var currentTodo *session.Todo
	for i := range todos {
		switch todos[i].Status {
		case session.TodoStatusCompleted:
			completed++
		case session.TodoStatusInProgress:
			if currentTodo == nil {
				currentTodo = &todos[i]
			}
		}
	}

	total := len(todos)

	label := t.Pills.TodoLabel.Render("To-Do")
	progress := t.Pills.TodoProgress.Render(fmt.Sprintf("%d/%d", completed, total))

	var content string
	if panelFocused {
		content = fmt.Sprintf("%s %s", label, progress)
	} else if currentTodo != nil {
		taskText := currentTodo.Content
		if currentTodo.ActiveForm != "" {
			taskText = currentTodo.ActiveForm
		}
		if ansi.StringWidth(taskText) > maxTaskDisplayLength {
			taskText = ansi.Truncate(taskText, maxTaskDisplayLength-1, "…")
		}
		task := t.Pills.TodoCurrentTask.Render(taskText)
		content = fmt.Sprintf("%s %s %s  %s", spinnerView, label, progress, task)
	} else {
		content = fmt.Sprintf("%s %s", label, progress)
	}

	return t.Pills.Focused.Render(content)
}

// todoList 渲染 expanded todo list.
func todoList(sessionTodos []session.Todo, spinnerView string, t *styles.Styles, width int) string {
	return chat.FormatTodosList(t, sessionTodos, spinnerView, width)
}

// queueList renders expanded 队列 items list.
func queueList(queueItems []string, t *styles.Styles) string {
	if len(queueItems) == 0 {
		return ""
	}

	prefix := t.Pills.QueueItemPrefix.Render() + " "
	lines := make([]string, 0, len(queueItems))
	for _, item := range queueItems {
		lines = append(lines, prefix+ansi.Truncate(queueItem(item, t), maxQueueDisplayLength, "…"))
	}

	return strings.Join(lines, "\n")
}

// queueItem renders one queued 提示词 在 single line. 一个 子代理 报告
// reads like 它的 transcript header, 不 raw markup 模型 gets.
func queueItem(item string, t *styles.Styles) string {
	report, ok := message.ParseSubAgentReportPrompt(item)
	if !ok {
		return t.Pills.QueueItemText.Render(singleLine(item))
	}
	return chat.SubagentReportTitle(t, report.Label)
}

// singleLine collapses runs 的 whitespace, newlines included, 所以 queued
// item takes exactly one 行 pills area reserves 为 it.
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// pillsHeightReasonableTerminalHeight 是 minimum 终态 height 在 which
// we auto-expand pills 当 there 是 incomplete todos.
const pillsHeightReasonableTerminalHeight = 40

// autoExpandPillsIfReasonable expands pills panel 如果 终态 has
// enough vertical space 到 show expanded list comfortably.
func (m *UI) autoExpandPillsIfReasonable() tea.Cmd {
	if !m.hasSession() {
		return nil
	}
	if m.activeInline != nil {
		return nil
	}
	if m.height < pillsHeightReasonableTerminalHeight {
		return nil
	}
	hasPills := hasIncompleteTodos(m.session.Todos) || m.promptQueue.Shown() > 0
	if !hasPills {
		return nil
	}
	if m.pillsExpanded {
		return nil
	}
	if m.pillsAutoExpanded {
		return nil
	}
	m.pillsExpanded = true
	m.pillsAutoExpanded = true
	if hasIncompleteTodos(m.session.Todos) {
		m.focusedPillSection = pillSectionTodos
	} else {
		m.focusedPillSection = pillSectionQueue
	}
	m.updateLayoutAndSize()
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
	}
	return nil
}

// togglePillsExpanded toggles the pills panel expansion state.
func (m *UI) togglePillsExpanded() tea.Cmd {
	if !m.hasSession() {
		return nil
	}
	hasPills := hasIncompleteTodos(m.session.Todos) || m.promptQueue.Shown() > 0
	if !hasPills {
		return nil
	}
	m.pillsExpanded = !m.pillsExpanded
	if m.pillsExpanded {
		if hasIncompleteTodos(m.session.Todos) {
			m.focusedPillSection = pillSectionTodos
		} else {
			m.focusedPillSection = pillSectionQueue
		}
	}
	m.updateLayoutAndSize()

	// Make sure 到 follow scroll 如果 follow 是 enabled 当 toggling pills.
	// Note: uses ScrollToBottom (no scrollbar) since 这 是 layout adjustment,
	// 不 用户-initiated scrolling.
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
	}

	return nil
}

// switchPillSection changes focus between todo 和 队列 sections.
func (m *UI) switchPillSection(dir int) tea.Cmd {
	if !m.pillsExpanded || !m.hasSession() {
		return nil
	}
	hasIncompleteTodos := hasIncompleteTodos(m.session.Todos)
	hasQueue := m.promptQueue.Shown() > 0

	if dir < 0 && m.focusedPillSection == pillSectionQueue && hasIncompleteTodos {
		m.focusedPillSection = pillSectionTodos
		m.updateLayoutAndSize()
		return nil
	}
	if dir > 0 && m.focusedPillSection == pillSectionTodos && hasQueue {
		m.focusedPillSection = pillSectionQueue
		m.updateLayoutAndSize()
		return nil
	}
	return nil
}

// effectiveFocusedSection returns pill section 那 should 是 treated 作为
// focused 为 rendering. 存储 focusedPillSection can go stale 当 它的
// section loses all content (为 example todos 完成 while panel 是 open,
// 或 it defaults 到 todos before any todos exist). In 那 case we fall through
// 到 whichever section still has content 所以 expanded list stays populated.
func (m *UI) effectiveFocusedSection() pillSection {
	hasIncomplete := hasIncompleteTodos(m.session.Todos)
	hasQueue := m.promptQueue.Shown() > 0
	switch m.focusedPillSection {
	case pillSectionQueue:
		if hasQueue {
			return pillSectionQueue
		}
		if hasIncomplete {
			return pillSectionTodos
		}
	default: // pillSectionTodos
		if hasIncomplete {
			return pillSectionTodos
		}
		if hasQueue {
			return pillSectionQueue
		}
	}
	return m.focusedPillSection
}

// pillsAttachmentGap reports whether open list needs blank 行 above
// attachments, which otherwise sit 刷盘 against it.
func (m *UI) pillsAttachmentGap() bool {
	return m.pillsExpanded && m.hasAttachments()
}

// hasAttachments reports whether the editor holds any attachments.
func (m *UI) hasAttachments() bool {
	return m.attachments != nil && len(m.attachments.List()) > 0
}

// changeAttachments runs change 和 relays out 如果 it moved pills gap.
func (m *UI) changeAttachments(change func() bool) bool {
	hadGap := m.pillsAttachmentGap()
	changed := change()
	if m.pillsAttachmentGap() != hadGap {
		m.updateLayoutAndSize()
		m.invalidateFrames()
	}
	return changed
}

// pillsAreaHeight calculates total height needed 为 pills area.
func (m *UI) pillsAreaHeight() int {
	if !m.hasSession() {
		return 0
	}
	// Suppress pills 当 inline editor (e.g. question form) 是 活跃
	// 到 avoid competing 为 screen space.
	if m.activeInline != nil {
		return 0
	}
	hasIncomplete := hasIncompleteTodos(m.session.Todos)
	hasQueue := m.promptQueue.Shown() > 0
	hasPills := hasIncomplete || hasQueue
	if !hasPills {
		return 0
	}

	pillsAreaHeight := pillHeightWithBorder
	if m.pillsExpanded {
		switch m.effectiveFocusedSection() {
		case pillSectionTodos:
			if hasIncomplete {
				pillsAreaHeight += len(m.session.Todos)
			}
		case pillSectionQueue:
			if hasQueue {
				pillsAreaHeight += m.promptQueue.Shown()
			}
		}
		if m.pillsAttachmentGap() {
			pillsAreaHeight++
		}
	}
	return pillsAreaHeight
}

// renderPills renders pills panel 和 stores it 在 m.pillsView.
func (m *UI) renderPills() {
	m.pillsView = ""
	if !m.hasSession() {
		return
	}
	// Suppress pills 当 inline editor (e.g. question form) 是 活跃.
	if m.activeInline != nil {
		return
	}

	width := m.layout.pills.Dx()
	if width <= 0 {
		return
	}

	paddingLeft := 3
	contentWidth := max(width-paddingLeft, 0)

	hasIncomplete := hasIncompleteTodos(m.session.Todos)
	hasQueue := m.promptQueue.Shown() > 0

	if !hasIncomplete && !hasQueue {
		return
	}

	t := m.com.Styles
	effective := m.effectiveFocusedSection()
	todosFocused := m.pillsExpanded && effective == pillSectionTodos
	queueFocused := m.pillsExpanded && effective == pillSectionQueue

	inProgressIcon := t.Tool.TodoInProgressIcon.Render(styles.SpinnerIcon)
	if m.todoIsSpinning {
		inProgressIcon = m.todoSpinner.View()
	}

	var pills []string
	if hasIncomplete {
		pills = append(pills, todoPill(m.session.Todos, inProgressIcon, m.pillsExpanded, t))
	}
	if hasQueue {
		pills = append(pills, queuePill(m.promptQueue.Shown(), t))
	}

	var expandedList string
	if m.pillsExpanded {
		if todosFocused && hasIncomplete {
			expandedList = todoList(m.session.Todos, inProgressIcon, t, contentWidth)
		} else if queueFocused && hasQueue {
			// Render 从 memoized 队列 (fetched off-thread, see
			// workspace_cache.go): renderPills runs 在 更新/View
			// 路径 和 must never block 在 工作区 round-trip.
			if items := m.promptQueue.Items(); len(items) > 0 {
				expandedList = queueList(items, t)
			}
		}
	}

	if len(pills) == 0 {
		return
	}

	pillsRow := lipgloss.JoinHorizontal(lipgloss.Top, pills...)

	helpDesc := "open"
	if m.pillsExpanded {
		helpDesc = "close"
	}
	helpKey := t.Pills.HelpKey.Render("ctrl+t")
	helpText := t.Pills.HelpText.Render(helpDesc)
	helpHint := lipgloss.JoinHorizontal(lipgloss.Center, helpKey, " ", helpText)
	pillsRow = lipgloss.JoinHorizontal(lipgloss.Center, pillsRow, " ", helpHint)

	pillsArea := pillsRow
	if expandedList != "" {
		pillsArea = lipgloss.JoinVertical(lipgloss.Left, pillsRow, expandedList)
	}
	if m.pillsAttachmentGap() {
		pillsArea += "\n"
	}

	m.pillsView = t.Pills.Area.MaxWidth(width).PaddingLeft(paddingLeft).Render(pillsArea)
}
