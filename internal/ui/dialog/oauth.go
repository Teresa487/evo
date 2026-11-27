// oauth.go —— OAuth 登录弹窗的通用框架。前端对后端：Workspace.SetProviderAPIKey。

package dialog

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/pkg/browser"
)

type OAuthProvider interface {
	name() string
	initiateAuth() tea.Msg
	startPolling(deviceCode string, expiresIn int) tea.Cmd
	stopPolling() tea.Msg
	// supportsCodeEntry reports whether sign-在 can finish 用
	// code pasted back 从 authorization page instead 的
	// loopback redirect — 为 当 用户 declines browser
	// 权限 或 browser cannot reach callback.
	supportsCodeEntry() bool
	// submitCode exchanges pasted code (或 full callback URL) 为
	// token. Only called 当 supportsCodeEntry 是 true.
	submitCode(input string) tea.Cmd
}

// OAuthState represents current state 的 device flow.
type OAuthState int

const (
	OAuthStateInitializing OAuthState = iota
	OAuthStateDisplay
	OAuthStateSuccess
	OAuthStateSaving
	OAuthStateError
)

// OAuthID 是 identifier 为 模型 selection dialog.
const OAuthID = "oauth"

// OAuth handles OAuth 流程 认证.
type OAuth struct {
	com          *common.Common
	isOnboarding bool

	provider      catwalk.Provider
	model         config.SelectedModel
	modelType     config.SelectedModelType
	oAuthProvider OAuthProvider

	State OAuthState

	spinner spinner.Model
	help    help.Model
	keyMap  struct {
		Copy      key.Binding
		CopyURL   key.Binding
		Submit    key.Binding
		FocusCode key.Binding
		Close     key.Binding
	}

	width           int
	deviceCode      string
	userCode        string
	verificationURL string
	expiresIn       int
	interval        int
	token           *oauth.Token
	cancelFunc      context.CancelFunc
	// codeInput 是 paste 字段 为 providers whose browser flow
	// can finish 用 code entered back 在 终态.
	codeInput textinput.Model
}

var _ Dialog = (*OAuth)(nil)

// newOAuth creates new device 流程 component.
func newOAuth(
	com *common.Common,
	isOnboarding bool,
	provider catwalk.Provider,
	model config.SelectedModel,
	modelType config.SelectedModelType,
	oAuthProvider OAuthProvider,
) (*OAuth, tea.Cmd) {
	t := com.Styles

	m := OAuth{}
	m.com = com
	m.isOnboarding = isOnboarding
	m.provider = provider
	m.model = model
	m.modelType = modelType
	m.oAuthProvider = oAuthProvider
	m.width = 0 // Set dynamically in Draw().
	m.State = OAuthStateInitializing

	m.spinner = spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(t.Dialog.OAuth.Spinner),
	)

	m.help = help.New()
	m.help.Styles = t.DialogHelpStyles()

	m.keyMap.Copy = key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "copy code"),
	)
	m.keyMap.CopyURL = key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "copy url"),
	)
	m.keyMap.Submit = key.NewBinding(
		key.WithKeys("enter", "ctrl+y"),
		key.WithHelp("enter", "copy & open"),
	)
	m.keyMap.FocusCode = key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "enter code"),
	)
	m.keyMap.Close = CloseKey

	if m.oAuthProvider.supportsCodeEntry() {
		m.codeInput = textinput.New()
		m.codeInput.SetVirtualCursor(false)
		m.codeInput.Prompt = "> "
		m.codeInput.Placeholder = "Paste the code from the browser..."
		m.codeInput.SetStyles(t.TextInput)
		m.codeInput.Blur()
	}

	return &m, tea.Batch(m.spinner.Tick, m.oAuthProvider.initiateAuth)
}

// ID implements Dialog.
func (m *OAuth) ID() string {
	return OAuthID
}

// HandleMsg handles messages 和 state transitions.
func (m *OAuth) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		switch m.State {
		case OAuthStateInitializing, OAuthStateDisplay, OAuthStateSaving:
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			if cmd != nil {
				return ActionCmd{cmd}
			}
		}

	case tea.KeyPressMsg:
		// paste 字段 swallows keys while focused 所以 typing lands 在
		// it: enter submits pasted code, esc steps back 到
		// buttons.
		if m.codeEntryActive() && m.codeInput.Focused() {
			switch {
			case key.Matches(msg, m.keyMap.Submit):
				if value := m.codeInput.Value(); value != "" {
					m.codeInput.Blur()
					return ActionCmd{m.oAuthProvider.submitCode(value)}
				}
				cmd := m.copyCodeAndOpenURL()
				return ActionCmd{cmd}

			case key.Matches(msg, m.keyMap.Close):
				m.codeInput.Blur()
				return nil

			default:
				var cmd tea.Cmd
				m.codeInput, cmd = m.codeInput.Update(msg)
				if cmd != nil {
					return ActionCmd{cmd}
				}
				return nil
			}
		}

		switch {
		case m.codeEntryActive() && key.Matches(msg, m.keyMap.FocusCode):
			m.codeInput.Focus()
			return nil

		case key.Matches(msg, m.keyMap.Copy):
			cmd := m.copyCode()
			return ActionCmd{cmd}

		case key.Matches(msg, m.keyMap.CopyURL):
			cmd := m.copyURL()
			return ActionCmd{cmd}

		case key.Matches(msg, m.keyMap.Submit):
			switch m.State {
			case OAuthStateSuccess:
				return m.confirmAndSelectModel()

			case OAuthStateSaving:
				// Save 在 progress; ignore submits until it finishes.
				return nil

			default:
				cmd := m.copyCodeAndOpenURL()
				return ActionCmd{cmd}
			}

		case key.Matches(msg, m.keyMap.Close):
			switch m.State {
			case OAuthStateSuccess:
				return m.confirmAndSelectModel()

			case OAuthStateSaving:
				// Save 在 progress; ignore submits until it finishes.
				return nil

			default:
				// 取消 在-flight authorization 所以 dismissed dialog
				// does 不 leave poller 或 loopback listener behind.
				return ActionCloseOAuth{Cmd: m.oAuthProvider.stopPolling}
			}
		}

	case ActionInitiateOAuth:
		m.deviceCode = msg.DeviceCode
		m.userCode = msg.UserCode
		m.expiresIn = msg.ExpiresIn
		m.verificationURL = msg.VerificationURL
		m.interval = msg.Interval
		m.State = OAuthStateDisplay
		return ActionCmd{m.oAuthProvider.startPolling(msg.DeviceCode, msg.ExpiresIn)}

	case ActionCompleteOAuth:
		// device flow finished 和 we have token. Immediately
		// persist it 和 fetch models 在 background (这 triggers
		// 配置 reload 那 can take few seconds), showing spinner.
		// success screen 是 presented 只 once 那 work completes,
		// 所以 it truthfully means "ready 到 use" rather than gating
		// work behind a keypress.
		m.State = OAuthStateSaving
		m.token = msg.Token
		return ActionCmd{tea.Batch(
			m.oAuthProvider.stopPolling,
			m.spinner.Tick,
			m.saveCredential(),
		)}

	case ActionOAuthErrored:
		m.State = OAuthStateError
		cmd := tea.Batch(m.oAuthProvider.stopPolling, util.ReportError(msg.Error))
		return ActionCmd{cmd}

	case tea.PasteMsg:
		// Pasting into dialog goes straight into paste 字段,
		// no focus step needed.
		if m.codeEntryActive() {
			m.codeInput.Focus()
			var cmd tea.Cmd
			m.codeInput, cmd = m.codeInput.Update(msg)
			if cmd != nil {
				return ActionCmd{cmd}
			}
		}

	case oauthSaveDoneMsg:
		// Credential saved 和 models fetched. Present confirmation
		// screen; actual 模型 selection happens 当 用户
		// acknowledges it (fast, since work 是 already done).
		m.State = OAuthStateSuccess
		return nil

	case oauthSaveErrMsg:
		// Save failed; surface 错误 和 move 到 错误 state 所以
		// 用户 can dismiss 和 重试 flow.
		m.State = OAuthStateError
		return ActionCmd{util.ReportError(msg.err)}
	}
	return nil
}

// oauthSaveDoneMsg 是 emitted 通过 background save command once
// credential has been persisted 和 models fetched. 模型-selection
// details 是 读取 从 dialog's own fields 当 用户 confirms.
type oauthSaveDoneMsg struct{}

// oauthSaveErrMsg 是 emitted 通过 background save command 当 persisting
// the credential fails.
type oauthSaveErrMsg struct {
	err error
}

// View 渲染 device 流程 dialog.
func (m *OAuth) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	var (
		t           = m.com.Styles
		dialogWidth = max(0, min(60, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
		dialogStyle = t.Dialog.View.Width(dialogWidth)
	)
	m.width = dialogWidth
	if m.isOnboarding {
		view := m.dialogContent()
		cur := m.cursor()
		if cur != nil {
			cur = adjustOnboardingInputCursor(t, cur)
			DrawOnboardingCursor(scr, area, view, cur)
		} else {
			DrawOnboarding(scr, area, view)
		}
	} else {
		view := dialogStyle.Render(m.dialogContent())
		if cur := m.cursor(); cur != nil {
			DrawCenterCursor(scr, area, view, cur)
		} else {
			DrawCenter(scr, area, view)
		}
	}
	return nil
}

// cursor reports paste 字段's cursor 当 it 是 focused, 所以
// 终态 caret sits 在 input.
func (m *OAuth) cursor() *tea.Cursor {
	if m.codeEntryActive() && m.codeInput.Focused() {
		return InputCursor(m.com.Styles, m.codeInput.Cursor())
	}
	return nil
}

func (m *OAuth) dialogContent() string {
	t := m.com.Styles

	switch m.State {
	case OAuthStateInitializing, OAuthStateSaving:
		return m.innerDialogContent()

	default:
		innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()
		elements := []string{
			m.headerContent(),
			m.innerDialogContent(),
			renderDialogHelp(t, &m.help, m, innerWidth),
		}
		return strings.Join(elements, "\n")
	}
}

func (m *OAuth) headerContent() string {
	var (
		t            = m.com.Styles
		titleStyle   = t.Dialog.Title
		textStyle    = t.Dialog.PrimaryText
		dialogStyle  = t.Dialog.View.Width(m.width)
		headerOffset = titleStyle.GetHorizontalFrameSize() + dialogStyle.GetHorizontalFrameSize()
		dialogTitle  = fmt.Sprintf("Let’s authenticate with %s", m.oAuthProvider.name())
	)
	if m.isOnboarding {
		return textStyle.Render(dialogTitle)
	}
	return common.DialogTitle(t, titleStyle.Render(dialogTitle), m.width-headerOffset, t.Dialog.TitleGradFromColor, t.Dialog.TitleGradToColor)
}

func (m *OAuth) innerDialogContent() string {
	var (
		t                = m.com.Styles
		instructionStyle = t.Dialog.OAuth.Instructions
		enterKeyStyle    = t.Dialog.OAuth.Enter
		successStyle     = t.Dialog.OAuth.Success
		linkStyle        = t.Dialog.OAuth.Link
		errorStyle       = t.Dialog.OAuth.ErrorText
		statusTextStyle  = t.Dialog.OAuth.StatusText
	)

	// innerWidth 是 dialog's content area: total width minus
	// View frame (border). Every block sizes 到 这 所以 nothing gets
	// re-wrapped 当 dialog frame renders it.
	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()

	switch m.State {
	case OAuthStateInitializing:
		return lipgloss.NewStyle().
			Width(innerWidth).
			Align(lipgloss.Center).
			Render(
				successStyle.Render(m.spinner.View()) +
					statusTextStyle.Render("Initializing..."),
			)

	case OAuthStateDisplay:
		// Render each 文本 segment 用 它的 own style. Wrapping
		// whole concatenation 在 single style would lose 文本
		// color after enterKeyStyle's reset code.
		instructionText := instructionStyle.Render("Press ") +
			enterKeyStyle.Render("enter") +
			instructionStyle.Render(" to open the browser and authenticate.")
		if m.userCode != "" {
			instructionText = instructionStyle.Render("Press ") +
				enterKeyStyle.Render("enter") +
				instructionStyle.Render(" to copy the code below and open the browser.")
		}
		instructions := lipgloss.NewStyle().
			Width(innerWidth).
			Padding(0, 1).
			Render(instructionText)

		elements := []string{"", instructions, ""}
		if m.userCode != "" {
			codeBox := lipgloss.NewStyle().
				Width(innerWidth).
				Height(7).
				Align(lipgloss.Center, lipgloss.Center).
				Background(t.Dialog.OAuth.UserCodeBg).
				Render(
					t.Dialog.OAuth.UserCode.Render(m.userCode),
				)
			elements = append(elements, codeBox, "")
		}

		link := linkStyle.Hyperlink(m.verificationURL, "id=oauth-verify").Render(m.verificationURL)
		url := statusTextStyle.
			Width(innerWidth).
			Padding(0, 1).
			Render("Browser not opening? Pay a visit to:\n" + link)

		waitingMsg := "Waiting for browser authentication..."
		if m.userCode != "" {
			waitingMsg = "Verifying..."
		}
		waiting := statusTextStyle.
			Width(innerWidth).
			Padding(0, 1).
			Render(
				successStyle.Render(m.spinner.View()) + statusTextStyle.Render(waitingMsg),
			)

		elements = append(elements, url, "", waiting, "")

		if m.codeEntryActive() {
			// manual fallback: 授权 page shows code
			// 到 paste 当 redirect cannot come back 在 它的 own.
			m.codeInput.SetWidth(max(0, innerWidth-4))
			elements = append(elements,
				statusTextStyle.
					Width(innerWidth).
					Padding(0, 1).
					Render("Declined or browser didn't connect? Enter the code from the page:"),
				"",
				lipgloss.NewStyle().
					Width(innerWidth).
					Padding(0, 1).
					Render(m.codeInput.View()),
				"",
			)
		}

		return lipgloss.JoinVertical(lipgloss.Left, elements...)

	case OAuthStateSuccess:
		return successStyle.
			Width(innerWidth).
			Padding(1).
			Render("Authentication successful!")

	case OAuthStateSaving:
		return lipgloss.NewStyle().
			Width(innerWidth).
			Align(lipgloss.Center).
			Render(
				successStyle.Render(m.spinner.View()) +
					statusTextStyle.Render(" Fetching models..."),
			)

	case OAuthStateError:
		return errorStyle.
			Width(innerWidth).
			Padding(1).
			Render("Authentication failed.")

	default:
		return ""
	}
}

// FullHelp returns the full help view.
func (m *OAuth) FullHelp() [][]key.Binding {
	return [][]key.Binding{m.ShortHelp()}
}

// ShortHelp returns the full help view.
func (m *OAuth) ShortHelp() []key.Binding {
	switch m.State {
	case OAuthStateError:
		return []key.Binding{m.keyMap.Close}

	case OAuthStateSuccess:
		return []key.Binding{
			key.NewBinding(
				key.WithKeys("enter", "ctrl+y", "esc"),
				key.WithHelp("enter", "finish"),
			),
		}

	case OAuthStateSaving:
		// No actionable keys while the save completes.
		return nil

	default:
		if m.codeEntryActive() && m.codeInput.Focused() {
			return []key.Binding{
				key.NewBinding(
					key.WithKeys("enter", "ctrl+y"),
					key.WithHelp("enter", "submit code"),
				),
				key.NewBinding(
					key.WithKeys("esc", "alt+esc"),
					key.WithHelp("esc", "back"),
				),
			}
		}
		submit := m.keyMap.Submit
		if m.userCode == "" {
			submit = key.NewBinding(
				key.WithKeys("enter", "ctrl+y"),
				key.WithHelp("enter", "open browser"),
			)
		}
		h := []key.Binding{
			m.keyMap.CopyURL,
			submit,
			m.keyMap.Close,
		}
		if m.userCode != "" {
			h = append([]key.Binding{m.keyMap.Copy}, h...)
		}
		if m.codeEntryActive() {
			h = append([]key.Binding{m.keyMap.FocusCode}, h...)
		}
		return h
	}
}

// codeEntryActive reports whether paste 字段 是 shown: browser
// flow 在 它的 display state 用 no device code 在 play ( device
// flow's code 是 只 ever copied out, never pasted 在).
func (m *OAuth) codeEntryActive() bool {
	return m.oAuthProvider.supportsCodeEntry() &&
		m.State == OAuthStateDisplay && m.userCode == ""
}

func (m *OAuth) copyCode() tea.Cmd {
	if m.State != OAuthStateDisplay || m.userCode == "" {
		return nil
	}
	return common.CopyToClipboard(m.userCode, "Code copied to clipboard")
}

func (m *OAuth) copyURL() tea.Cmd {
	if m.State != OAuthStateDisplay {
		return nil
	}
	return common.CopyToClipboard(m.verificationURL, "URL copied to clipboard")
}

func (m *OAuth) copyCodeAndOpenURL() tea.Cmd {
	if m.State != OAuthStateDisplay {
		return nil
	}
	if m.userCode == "" {
		// Browser flows have no code 到 copy; just open URL.
		return func() tea.Msg {
			if err := browser.OpenURL(m.verificationURL); err != nil {
				return ActionOAuthErrored{fmt.Errorf("failed to open browser: %w", err)}
			}
			return nil
		}
	}
	return common.CopyToClipboardWithCallback(
		m.userCode,
		"Code copied and URL opened",
		func() tea.Msg {
			if err := browser.OpenURL(m.verificationURL); err != nil {
				return ActionOAuthErrored{fmt.Errorf("failed to open browser: %w", err)}
			}
			return nil
		},
	)
}

// saveCredential returns command 那 persists OAuth token 和
// triggers 配置 reload (including 模型 discovery) off UI 更新
// loop. 它 reports completion via oauthSaveDoneMsg 或 oauthSaveErrMsg.
func (m *OAuth) saveCredential() tea.Cmd {
	// Capture fields command needs 所以 it does 不 race 用
	// dialog state.
	var (
		com      = m.com
		provider = m.provider
		token    = m.token
	)
	return func() tea.Msg {
		if err := com.Workspace.SetProviderAPIKey(config.ScopeGlobal, string(provider.ID), token); err != nil {
			return oauthSaveErrMsg{err: fmt.Errorf("failed to save API key: %w", err)}
		}
		return oauthSaveDoneMsg{}
	}
}

// confirmAndSelectModel 是 invoked 当 用户 acknowledges success
// screen. credential 是 already saved, 所以 这 只 resumes 模型
// selection, which closes the dialog.
func (m *OAuth) confirmAndSelectModel() Action {
	return ActionSelectModel{
		Provider:  m.provider,
		Model:     m.model,
		ModelType: m.modelType,
	}
}
