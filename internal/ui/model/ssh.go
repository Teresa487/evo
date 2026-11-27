// ssh.go —— SSH 相关会话的 UI 支持。

package model

import (
	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/sshaskpass"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/notification"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// handleSSHPrompt routes SSH askpass 请求: passive security key
// touch 是 confirmed automatically 和 surfaced 作为 warning, while
// passwords 和 decision questions open dialog.
func (m *UI) handleSSHPrompt(req sshaskpass.PromptRequest) []tea.Cmd {
	var cmds []tea.Cmd

	req = sshaskpass.Describe(req)

	if req.Kind == sshaskpass.KindTouch {
		// physical touch 是 approval; OpenSSH's askpass
		// confirmation 只 means "proceed", 所以 answer it 和 annotate
		// status 用 warning instead 的 opening input dialog.
		m.com.Workspace.SSHRespond(req.ID, "yes")
		cmds = append(cmds, util.ReportWarn(req.Message))
		if cmd := m.sendNotification(notification.Notification{
			Title:   "Crush is waiting...",
			Message: req.Message,
		}); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return cmds
	}

	if cmd := m.openSSHDialog(req); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.sendNotification(sshWaitingNotification(req)); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// openSSHDialog shows integrated SSH askpass dialog 为 credential
// 或 confirmation 请求. Input 是 masked; 值 是 returned via
// [dialog.ActionSSHSubmit].
func (m *UI) openSSHDialog(req sshaskpass.PromptRequest) tea.Cmd {
	// Close any existing SSH dialog first 到 prevent stacking.
	m.dialog.CloseDialog(dialog.SSHID)
	m.dialog.OpenDialogWithGrace(dialog.NewSSH(m.com, req))
	return nil
}

// handleSSHNotification dismisses SSH dialog once 提示词 是
// resolved, covering case where another 订阅者 (或 cancelled
// ssh command) resolved it first.
func (m *UI) handleSSHNotification(n sshaskpass.Notification) {
	if d, ok := m.dialog.Dialog(dialog.SSHID).(*dialog.SSH); ok && d.RequestID() == n.RequestID {
		m.dialog.CloseDialog(dialog.SSHID)
	}
}

// sshWaitingMessage describes pending 提示词 为 desktop
// notifications, phrased 为 credential kind.
func sshWaitingMessage(req sshaskpass.PromptRequest) string {
	if req.Message != "" {
		return req.Message
	}
	switch req.Kind {
	case sshaskpass.KindConfirm:
		return "SSH is waiting for you to confirm the security key or host"
	case sshaskpass.KindTouch:
		return "Touch your security key to continue"
	default:
		return "SSH needs your password to continue"
	}
}

// sshWaitingNotification 是 desktop 通知 shown alongside
// SSH 提示词 dialog.
func sshWaitingNotification(req sshaskpass.PromptRequest) notification.Notification {
	return notification.Notification{
		Title:   "Crush is waiting...",
		Message: sshWaitingMessage(req),
	}
}
