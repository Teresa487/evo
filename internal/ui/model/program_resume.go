// program_resume.go —— TUI 重启后恢复现场（session、busy 状态）。

package model

import (
	"slices"

	tea "charm.land/bubbletea/v2"
)

// SetResumeCommand sets command 那 brings Crush back after
// 终态 restarts. open 会话 是 added 到 it.
func (m *UI) SetResumeCommand(args []string) {
	m.resumeCommand = args
}

// programResume tells terminals 那 support Program Resume Protocol
// (OSC 7577) how 到 bring Crush back, 用 open 会话, after they
// restart. 它 是 nil once 用户 quits, 所以 终态 forgets it.
// Terminals without support ignore it.
func (m *UI) programResume() *tea.ProgramResume {
	if len(m.resumeCommand) == 0 || m.quitting {
		return nil
	}
	args := m.resumeCommand
	if id := m.currentSessionID(); id != "" {
		args = append(slices.Clip(args), "--session", id)
	}
	return &tea.ProgramResume{Args: args, Dir: m.com.Workspace.WorkingDir()}
}
