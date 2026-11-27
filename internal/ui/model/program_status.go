// program_status.go —— 程序运行级状态（连接、错误提示）。

package model

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/ui/dialog"
)

// programStatusApp 是 stable program name reported 到 终态.
const programStatusApp = "crush"

// programStatus reports what Crush 是 doing 到 terminals 那 support
// Program Status Protocol (OSC 7501), 所以 they can show whether Crush 是
// working, waiting 在 用户, 或 finished. Terminals without support
// ignore it.
func (m *UI) programStatus() *tea.ProgramStatus {
	ps := &tea.ProgramStatus{State: tea.ProgramStateIdle, App: programStatusApp}
	switch {
	case m.dialog != nil && m.dialog.ContainsDialog(dialog.PermissionsID):
		ps.State = tea.ProgramStateBlocked
		ps.Kind = tea.ProgramStatusKindPermission
		ps.Message = "Permission required"
	case m.hasPendingQuestion():
		ps.State = tea.ProgramStateBlocked
		ps.Kind = tea.ProgramStatusKindQuestion
		ps.Message = "Questions need your input"
	case m.dialog != nil && m.dialog.ContainsDialog(dialog.AWSSSOID):
		ps.State = tea.ProgramStateBlocked
		ps.Kind = tea.ProgramStatusKindAuth
		ps.Message = "AWS sign-in required"
	case m.isAgentBusy():
		ps.State = tea.ProgramStateWorking
	case m.turnOutcome != "":
		ps.State = m.turnOutcome
	}
	return ps
}

func (m *UI) hasPendingQuestion() bool {
	_, ok := m.activeInline.(*dialog.QuestionForm)
	return ok
}

// trackTurnOutcome records how open 会话's last 运行 ended until
// 用户 returns 到 窗口 或 submits something new.
func (m *UI) trackTurnOutcome(msg tea.Msg) {
	switch msg := msg.(type) {
	case tea.FocusMsg:
		m.turnOutcome = ""
	case pubsub.Event[notify.RunComplete]:
		if msg.Payload.SessionID != m.currentSessionID() {
			return
		}
		switch {
		case msg.Payload.Cancelled:
			m.turnOutcome = ""
		case msg.Payload.Error != "":
			m.turnOutcome = tea.ProgramStateError
		default:
			m.turnOutcome = tea.ProgramStateDone
		}
	}
}
