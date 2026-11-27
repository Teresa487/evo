// aws_sso.go —— AWS SSO 登录的弹窗与提示。

package dialog

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/pkg/browser"
)

// AWSSSOID 是 identifier 为 AWS SSO auth dialog.
const AWSSSOID = "aws_sso"

// awsSSOState represents current state 的 AWS SSO flow.
type awsSSOState int

const (
	awsSSOStateWaiting awsSSOState = iota
	awsSSOStateSuccess
	awsSSOStateError
)

// AWSSSO displays progress 的 AWS SSO refresh. command itself runs
// 在 coordinator (所以 refreshed credentials land where 模型 calls
// 是 made); 这 dialog 是 pure view driven 通过 agent notifications: it
// shows a spinner while the command runs, the verification URL once it
// appears, 和 final success 或 错误. 只 local action it performs
// 是 opening URL 在 用户's browser.
type AWSSSO struct {
	com *common.Common

	state   awsSSOState
	command string

	spinner spinner.Model
	help    help.Model
	keyMap  struct {
		Open  key.Binding
		Close key.Binding
	}

	width  int
	url    string
	errMsg string
}

var _ Dialog = (*AWSSSO)(nil)

// NewAWSSSO creates new AWS SSO authentication dialog 为 given refresh
// command. dialog starts 在 waiting state; SetURL 和 Finish drive it
// 作为 coordinator reports progress.
func NewAWSSSO(com *common.Common, command string) (*AWSSSO, tea.Cmd) {
	t := com.Styles

	m := &AWSSSO{
		com:     com,
		command: command,
		width:   0, // Set dynamically in Draw().
		state:   awsSSOStateWaiting,
	}

	m.spinner = spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(t.Dialog.OAuth.Spinner),
	)

	m.help = help.New()
	m.help.Styles = t.DialogHelpStyles()

	m.keyMap.Open = key.NewBinding(
		key.WithKeys("enter", "ctrl+y"),
		key.WithHelp("enter", "open in browser"),
	)
	m.keyMap.Close = CloseKey

	return m, m.spinner.Tick
}

// SetURL records SSO verification URL 所以 dialog can display it 和
// offer 到 open it 在 browser.
func (m *AWSSSO) SetURL(url string) {
	if m.state == awsSSOStateWaiting {
		m.url = url
	}
}

// Finish transitions dialog 到 它的 终态 state. 一个 nil 错误 means
// refresh succeeded; non-empty 消息 means it failed.
func (m *AWSSSO) Finish(errMsg string) {
	if errMsg != "" {
		m.state = awsSSOStateError
		m.errMsg = errMsg
		return
	}
	m.state = awsSSOStateSuccess
}

// ID implements Dialog.
func (m *AWSSSO) ID() string {
	return AWSSSOID
}

// HandleMsg handles messages 和 state transitions.
func (m *AWSSSO) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if m.state == awsSSOStateWaiting {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			if cmd != nil {
				return ActionCmd{cmd}
			}
		}

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keyMap.Open):
			if m.state == awsSSOStateWaiting && m.url != "" {
				return ActionCmd{m.openURLCmd()}
			}
			if m.state == awsSSOStateSuccess {
				return ActionClose{}
			}

		case key.Matches(msg, m.keyMap.Close):
			return ActionClose{}
		}
	}

	return nil
}

// Draw 渲染 AWS SSO auth dialog.
func (m *AWSSSO) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	var (
		t           = m.com.Styles
		dialogWidth = max(0, min(60, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
		dialogStyle = t.Dialog.View.Width(dialogWidth)
	)
	m.width = dialogWidth
	view := dialogStyle.Render(m.dialogContent())
	DrawCenter(scr, area, view)
	return nil
}

func (m *AWSSSO) dialogContent() string {
	t := m.com.Styles
	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()

	elements := []string{
		m.headerContent(),
		m.innerDialogContent(),
		renderDialogHelp(t, &m.help, m, innerWidth),
	}
	return strings.Join(elements, "\n")
}

func (m *AWSSSO) headerContent() string {
	var (
		t            = m.com.Styles
		titleStyle   = t.Dialog.Title
		dialogStyle  = t.Dialog.View.Width(m.width)
		headerOffset = titleStyle.GetHorizontalFrameSize() + dialogStyle.GetHorizontalFrameSize()
		dialogTitle  = "AWS SSO Authentication"
	)
	return common.DialogTitle(t, titleStyle.Render(dialogTitle), m.width-headerOffset, t.Dialog.TitleGradFromColor, t.Dialog.TitleGradToColor)
}

func (m *AWSSSO) innerDialogContent() string {
	var (
		t                = m.com.Styles
		instructionStyle = t.Dialog.OAuth.Instructions
		enterKeyStyle    = t.Dialog.OAuth.Enter
		successStyle     = t.Dialog.OAuth.Success
		linkStyle        = t.Dialog.OAuth.Link
		errorStyle       = t.Dialog.OAuth.ErrorText
		statusTextStyle  = t.Dialog.OAuth.StatusText
	)

	// innerWidth 是 dialog's content area (total minus View
	// border). Every block sizes 到 这 和 uses padding 为 inset, 所以
	// nothing 是 re-wrapped 当 dialog frame renders it.
	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()

	switch m.state {
	case awsSSOStateWaiting:
		if m.url != "" {
			// URL found; show it 和 wait 为 auth 到 完成. Render each
			// 文本 segment 用 它的 own style: wrapping whole string 在
			// one style would drop 文本 color after enterKeyStyle's
			// reset code.
			instructionText := instructionStyle.Render("Press ") +
				enterKeyStyle.Render("enter") +
				instructionStyle.Render(" to open the authorization page.")
			instructions := lipgloss.NewStyle().
				Width(innerWidth).
				Padding(0, 1).
				Render(instructionText)

			displayURL := ansi.Truncate(m.url, max(0, innerWidth-2), "…") // -2 for padding
			link := linkStyle.Hyperlink(m.url, "id=aws-sso-verify").Render(displayURL)
			urlBox := lipgloss.NewStyle().
				Width(innerWidth).
				Padding(0, 1).
				Align(lipgloss.Center).
				Render(link)

			waiting := statusTextStyle.
				Width(innerWidth).
				Padding(0, 1).
				Render(
					successStyle.Render(m.spinner.View()) +
						statusTextStyle.Render("Waiting for authentication..."),
				)

			return lipgloss.JoinVertical(
				lipgloss.Left,
				"",
				instructions,
				"",
				urlBox,
				"",
				waiting,
				"",
			)
		}

		// No URL yet; still waiting 为 command output.
		spinnerLine := statusTextStyle.
			Width(innerWidth).
			Padding(0, 1).
			Render(
				successStyle.Render(m.spinner.View()) +
					statusTextStyle.Render("Starting "+m.command+"..."),
			)
		return lipgloss.JoinVertical(lipgloss.Left, "", spinnerLine, "")

	case awsSSOStateSuccess:
		return successStyle.
			Width(innerWidth).
			Align(lipgloss.Center).
			Render("✓ Authentication successful!")

	case awsSSOStateError:
		header := errorStyle.
			Width(innerWidth).
			Padding(0, 1).
			Render("Authentication failed.")

		if m.errMsg == "" {
			return header
		}

		flattened := strings.Join(strings.Fields(strings.TrimSpace(m.errMsg)), " ")
		detail := statusTextStyle.
			Width(innerWidth).
			Padding(0, 1).
			Render(flattened)

		return lipgloss.JoinVertical(lipgloss.Left, "", header, "", detail, "")

	default:
		return ""
	}
}

// FullHelp returns the full help view.
func (m *AWSSSO) FullHelp() [][]key.Binding {
	return [][]key.Binding{m.ShortHelp()}
}

// ShortHelp returns the short help view.
func (m *AWSSSO) ShortHelp() []key.Binding {
	switch m.state {
	case awsSSOStateError:
		return []key.Binding{m.keyMap.Close}
	case awsSSOStateSuccess:
		return []key.Binding{
			key.NewBinding(
				key.WithKeys("enter", "ctrl+y", "esc"),
				key.WithHelp("enter", "close"),
			),
		}
	default:
		if m.url != "" {
			return []key.Binding{m.keyMap.Open, m.keyMap.Close}
		}
		return []key.Binding{m.keyMap.Close}
	}
}

func (m *AWSSSO) openURLCmd() tea.Cmd {
	return func() tea.Msg {
		if m.url == "" {
			return nil
		}
		_ = browser.OpenURL(m.url)
		return nil
	}
}
