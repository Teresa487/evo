// subagents.go —— 子 agent 列表与状态展示。

package model

import (
	"fmt"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/ui/anim"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/common"
)

// subAgentSpinnerFrames animates running marker. sidebar 是 rebuilt
// 在 every animation tick while 派发 item 是 spinning, 所以 deriving
// frame 从 clock 是 enough 到 make it move without 它的 own ticker.
var subAgentSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// subAgentSpinner returns frame 为 given moment. glyph period
// 是 whole number 的 animation frames 所以 every glyph holds equally long.
func subAgentSpinner(now time.Time) string {
	frameEvery := 2 * anim.FrameInterval()
	i := (now.UnixNano() / int64(frameEvery)) % int64(len(subAgentSpinnerFrames))
	return subAgentSpinnerFrames[i]
}

// subAgentIdleRetention 是 how long finished 子代理 stays 在
// sidebar. Without it section 只 ever grows, since every 派发
// leaves 行 behind 为 rest 的 会话. 一个 retired 子代理 是
// hidden, 不 gone: agent_send can still reach it.
const subAgentIdleRetention = 5 * time.Minute

// subAgentStatuses reads dispatched subagents out 的 chat itself.
// Every 派发 是 工具调用 item 在 transcript, 和 子代理's
// 工具 calls already 流 在 作为 那 item's nested tools, 所以 chat
// already knows both who 是 running 和 what they 是 doing.
//
// Subagents 那 finished more than [subAgentIdleRetention] ago 是 left
// out. Nothing repaints 在 timer once every 子代理 是 done, 所以 行
// retires 在 next render rather than exactly 在 期限.
func (m *UI) subAgentStatuses() []chat.DispatchStatus {
	var statuses []chat.DispatchStatus
	for i := range m.chat.Len() {
		item, ok := m.chat.MessageItemAt(i).(*chat.AgentToolMessageItem)
		if !ok {
			continue
		}
		status, ok := item.DispatchStatus()
		if !ok {
			continue
		}
		if !status.Running && !status.Finished.IsZero() &&
			time.Since(status.Finished) > subAgentIdleRetention {
			continue
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// runningSubAgents counts the subagents still working.
func (m *UI) runningSubAgents() int {
	var n int
	for _, s := range m.subAgentStatuses() {
		if s.Running {
			n++
		}
	}
	return n
}

// subAgentRow renders one 子代理's line: spinner 和 elapsed time while
// it works; once finished, icon's color alone says 所以.
func (m *UI) subAgentRow(status chat.DispatchStatus, width int) string {
	t := m.com.Styles
	now := time.Now()

	icon := t.Resource.WorkingIcon.Render(subAgentSpinner(now))
	state := "starting"
	switch {
	case !status.Running:
		icon, state = t.Resource.OfflineIcon.String(), ""
	case !status.Started.IsZero():
		state = common.FormatDuration(common.ElapsedSince(status.Started, now))
	}
	var extra string
	if state != "" {
		extra = t.Resource.RowDescBase.Foreground(t.Resource.DefaultDescFg).Render(state)
	}

	// label 是 description 所以 Status truncates it, 不 state.
	first, label := agent.SubagentDisplayName(status.Label)
	title := t.Tool.AgentName.Render(first)
	if first == "" {
		title, label = t.Tool.AgentLabel.Render(label), ""
	}
	return common.Status(t, common.StatusOpts{
		Icon:             icon,
		Title:            title,
		Description:      label,
		DescriptionColor: t.Resource.DefaultTitleFg,
		ExtraContent:     extra,
	}, width)
}

// subAgentsInfo renders dispatched 子代理 section. 它 renders empty
// 当 nothing has been dispatched, 所以 it costs no sidebar space until it has
// something 到 say.
func (m *UI) subAgentsInfo(statuses []chat.DispatchStatus, width, maxItems int, isSection bool) string {
	if len(statuses) == 0 {
		return ""
	}

	t := m.com.Styles
	title := t.Resource.Heading.Render("Subagents")
	if isSection {
		title = common.Section(t, title, width)
	}

	rows := make([]string, 0, len(statuses))
	for _, status := range statuses {
		rows = append(rows, m.subAgentRow(status, width))
	}
	rows = truncateRows(rows, maxItems, t.Resource.AdditionalText)

	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf(
		"%s\n\n%s", title, lipgloss.JoinVertical(lipgloss.Left, rows...),
	))
}
