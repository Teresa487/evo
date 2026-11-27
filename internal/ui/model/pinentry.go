// pinentry.go —— 凭据输入弹窗的 UI 状态。

package model

import (
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/pinentry"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/notification"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// pinentryTouchMessage 是 shown 在 status bar while gpg waits 为
// security key touch after the passphrase dialog closed.
const pinentryTouchMessage = "GPG is waiting: touch your security key to continue"

// handlePinentryEvent applies pinentry watcher 事件: it hands
// 终态 over while 终态-based pinentry dialog 是 up, takes it
// back once dialog closes, 和 surfaces status hint while gpg waits
// 为 security key touch.
func (m *UI) handlePinentryEvent(ev pinentry.Event) tea.Cmd {
	var cmds []tea.Cmd

	if ev.Active != m.pinentryActive {
		m.pinentryActive = ev.Active
		m.pinentryCtrlL = ev.CtrlLRedraw
		if ev.Active {
			if cmd := m.releaseTerminalForPinentry(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			if cmd := m.sendNotification(notification.Notification{
				Title:   "Crush is waiting...",
				Message: "GPG is asking for your passphrase (pinentry)",
			}); cmd != nil {
				cmds = append(cmds, cmd)
			}
		} else if cmd := m.restoreTerminalForPinentry(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	if ev.TouchPending != m.pinentryTouchPending {
		m.pinentryTouchPending = ev.TouchPending
		if ev.TouchPending {
			cmds = append(cmds, m.status.Show(util.InfoMsg{
				Type: util.InfoTypeWarn,
				Msg:  pinentryTouchMessage,
			}))
		} else {
			m.status.ClearInfoMsg(m.status.msgSeq)
		}
	}

	return tea.Batch(cmds...)
}

// releaseTerminalForPinentry releases 终态 所以 pinentry dialog
// spawned 通过 gpg-agent can draw 在 it 和 读取 input directly.
func (m *UI) releaseTerminalForPinentry() tea.Cmd {
	program := m.program
	if program == nil {
		return nil
	}
	ctrlL := m.pinentryCtrlL
	return func() tea.Msg {
		// Bubble Tea runs each command 在 它的 own goroutine; serialize
		// release/restore pair 所以 restore can never overtake release.
		m.pinentryTermMu.Lock()
		defer m.pinentryTermMu.Unlock()
		if err := program.ReleaseTerminal(); err != nil {
			slog.Error("Failed to release terminal for pinentry", "error", err)
			return nil
		}
		pinentry.BeginHandover(ctrlL)
		return nil
	}
}

// restoreTerminalForPinentry reinitializes renderer 和 input reader
// after a pinentry dialog closed, redrawing the Crush UI.
func (m *UI) restoreTerminalForPinentry() tea.Cmd {
	program := m.program
	if program == nil {
		return nil
	}
	return func() tea.Msg {
		m.pinentryTermMu.Lock()
		defer m.pinentryTermMu.Unlock()
		pinentry.EndHandover()
		if err := program.RestoreTerminal(); err != nil {
			slog.Error("Failed to restore terminal after pinentry", "error", err)
		}
		return nil
	}
}

// openPinentryDialog shows integrated pinentry dialog 为 GPG
// credential 请求 (passphrase 或 security key PIN). Input 是 masked;
// secret 是 returned via [dialog.ActionPinentrySubmit].
func (m *UI) openPinentryDialog(req pinentry.PromptRequest) tea.Cmd {
	// Close any existing pinentry dialog first 到 prevent stacking.
	m.dialog.CloseDialog(dialog.PinentryID)
	m.dialog.OpenDialogWithGrace(dialog.NewPinentry(m.com, req))

	what := "passphrase"
	if req.Kind == pinentry.KindPIN {
		what = "security key PIN"
	}
	cmds := []tea.Cmd{m.sendNotification(notification.Notification{
		Title:   "Crush is waiting...",
		Message: "GPG needs your " + what + " to continue",
	})}
	// 一个 重试 carries why GPG rejected previous attempt; surface it
	// 作为 caution-level status 消息 rather than inside dialog.
	if req.Error != "" {
		cmds = append(cmds, util.ReportWarn(req.Error))
	}
	return tea.Batch(cmds...)
}

// handlePinentryNotification dismisses pinentry dialog once 它的
// 提示词 是 resolved, covering case where another 订阅者 (或
// cancelled GPG command) resolved it first. 一个 newer 提示词's dialog 是
// left alone: notifications can arrive after next 提示词 opened.
func (m *UI) handlePinentryNotification(n pinentry.Notification) {
	if d, ok := m.dialog.Dialog(dialog.PinentryID).(*dialog.Pinentry); ok && d.RequestID() == n.RequestID {
		m.dialog.CloseDialog(dialog.PinentryID)
	}
}
