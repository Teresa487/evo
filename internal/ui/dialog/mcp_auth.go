// mcp_auth.go —— MCP 服务器授权弹窗。

package dialog

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/pkg/browser"
)

// MCPAuthID 是 identifier 为 MCP authentication dialog.
const MCPAuthID = "mcp_auth"

// MCPAuthState represents current state 的 MCP auth flow.
type MCPAuthState int

const (
	MCPAuthStatePrompt MCPAuthState = iota
	MCPAuthStateAuthenticating
	MCPAuthStateSuccess
	MCPAuthStateError
)

// MCPAuth handles MCP OAuth 认证 dialog.
type MCPAuth struct {
	com   *common.Common
	width int

	pending   []mcptools.PendingAuthServer
	current   int
	state     MCPAuthState
	err       error
	authURLFn func(name string) string

	cancelAuth context.CancelFunc

	spinner spinner.Model
	help    help.Model
	keyMap  struct {
		Submit key.Binding
		Copy   key.Binding
		Skip   key.Binding
		Close  key.Binding
	}
}

var _ Dialog = (*MCPAuth)(nil)

// NewMCPAuth creates new MCP 认证 dialog.
func NewMCPAuth(com *common.Common, pending []mcptools.PendingAuthServer, authURLFn func(string) string) (*MCPAuth, tea.Cmd) {
	t := com.Styles
	m := &MCPAuth{
		com:       com,
		width:     0, // Set dynamically in Draw().
		pending:   pending,
		state:     MCPAuthStatePrompt,
		authURLFn: authURLFn,
	}

	m.spinner = spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(t.Dialog.OAuth.Spinner),
	)

	m.help = help.New()
	m.help.Styles = t.DialogHelpStyles()

	m.keyMap.Submit = key.NewBinding(
		key.WithKeys("enter", "ctrl+y"),
		key.WithHelp("enter", "open browser"),
	)
	m.keyMap.Copy = key.NewBinding(
		key.WithKeys("c", "u"),
		key.WithHelp("c", "copy url"),
	)
	m.keyMap.Skip = key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "skip"),
	)
	m.keyMap.Close = CloseKey

	return m, m.spinner.Tick
}

// ID implements Dialog.
func (m *MCPAuth) ID() string {
	return MCPAuthID
}

// CancelAuth cancels any 在-progress authentication.
func (m *MCPAuth) CancelAuth() {
	if m.cancelAuth != nil {
		m.cancelAuth()
		m.cancelAuth = nil
	}
}

// HandleMsg processes messages 和 returns actions.
func (m *MCPAuth) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		switch m.state {
		case MCPAuthStatePrompt, MCPAuthStateAuthenticating:
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds := []tea.Cmd{}
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			if len(cmds) > 0 {
				return ActionCmd{tea.Batch(cmds...)}
			}
		}

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keyMap.Submit):
			switch m.state {
			case MCPAuthStatePrompt:
				return m.startAuth()
			case MCPAuthStateAuthenticating:
				m.openAuthURL()
			case MCPAuthStateSuccess:
				return m.advance()
			}
		case key.Matches(msg, m.keyMap.Copy):
			// Copy whatever URL 是 available without opening browser.
			// During 认证 授权 URL exists; during
			// 提示词 we fall back 到 服务端 URL. Starting flow
			// (和 opening browser) 是 enter's job, 不 c's.
			if u := m.authURL(); u != "" {
				return ActionCmd{common.CopyToClipboard(u, "URL copied to clipboard")}
			}
			if u := m.currentServer().URL; u != "" {
				return ActionCmd{common.CopyToClipboard(u, "URL copied to clipboard")}
			}
		case key.Matches(msg, m.keyMap.Skip):
			if m.state == MCPAuthStatePrompt {
				return m.advance()
			}
		case key.Matches(msg, m.keyMap.Close):
			m.CancelAuth()
			return ActionClose{}
		}

	case ActionMCPAuthComplete:
		m.state = MCPAuthStateSuccess
		m.cancelAuth = nil
		return nil

	case ActionMCPAuthErrored:
		m.state = MCPAuthStateError
		m.err = msg.Error
		m.cancelAuth = nil
		return nil
	}
	return nil
}

func (m *MCPAuth) startAuth() Action {
	if m.current >= len(m.pending) {
		return ActionClose{}
	}
	m.state = MCPAuthStateAuthenticating
	m.err = nil
	name := m.pending[m.current].Name

	// 创建 cancellable 上下文 owned 通过 dialog. UI will
	// use 这 上下文 为 auth 调用, 和 we 取消 it 如果
	// 用户 closes dialog 或 moves 在.
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelAuth = cancel

	return ActionCmd{tea.Batch(
		m.spinner.Tick,
		func() tea.Msg {
			return ActionMCPAuthStarted{Name: name, Ctx: ctx}
		},
	)}
}

func (m *MCPAuth) advance() Action {
	m.CancelAuth()
	m.current++
	if m.current >= len(m.pending) {
		return ActionClose{}
	}
	m.state = MCPAuthStatePrompt
	m.err = nil
	return nil
}

func (m *MCPAuth) openAuthURL() {
	if u := m.authURL(); u != "" {
		browser.OpenURL(u)
	}
}

// authURL returns browser authorization URL 为 current 服务端,
// 或 empty 如果 flow has 不 produced one yet.
func (m *MCPAuth) authURL() string {
	if m.authURLFn == nil {
		return ""
	}
	return m.authURLFn(m.currentServer().Name)
}

func (m *MCPAuth) currentServer() mcptools.PendingAuthServer {
	if m.current < len(m.pending) {
		return m.pending[m.current]
	}
	return mcptools.PendingAuthServer{}
}

// Draw renders dialog, sizing it 到 available area 所以 it never
// overflows narrow 终态.
func (m *MCPAuth) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := m.com.Styles
	m.width = max(0, min(60, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	dialogStyle := t.Dialog.View.Width(m.width)
	view := dialogStyle.Render(m.dialogContent())
	DrawCenter(scr, area, view)
	return nil
}

func (m *MCPAuth) dialogContent() string {
	t := m.com.Styles
	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()
	elements := []string{
		m.headerContent(),
		m.innerContent(),
		renderDialogHelp(t, &m.help, m, innerWidth),
	}
	return strings.Join(elements, "\n")
}

func (m *MCPAuth) headerContent() string {
	t := m.com.Styles
	titleStyle := t.Dialog.Title
	dialogStyle := t.Dialog.View.Width(m.width)
	headerOffset := titleStyle.GetHorizontalFrameSize() + dialogStyle.GetHorizontalFrameSize()

	title := fmt.Sprintf("Authenticate with %s", m.currentServer().Name)
	return common.DialogTitle(t, titleStyle.Render(title), m.width-headerOffset, t.Dialog.TitleGradFromColor, t.Dialog.TitleGradToColor)
}

func (m *MCPAuth) innerContent() string {
	t := m.com.Styles
	instructionStyle := t.Dialog.OAuth.Instructions
	enterStyle := t.Dialog.OAuth.Enter
	successStyle := t.Dialog.OAuth.Success
	linkStyle := t.Dialog.OAuth.Link
	errorStyle := t.Dialog.OAuth.ErrorText
	statusStyle := t.Dialog.OAuth.StatusText

	// innerWidth 是 dialog's content area: total width minus
	// View frame (border). Every block sizes 到 这 所以 nothing gets
	// re-wrapped 当 dialog frame renders it.
	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()
	server := m.currentServer()

	block := func(s string) string {
		return lipgloss.NewStyle().Width(innerWidth).Padding(0, 1).Render(s)
	}

	progress := ""
	if len(m.pending) > 1 {
		progress = fmt.Sprintf(" (%d/%d)", m.current+1, len(m.pending))
	}

	// urlText renders URL 作为 green hyperlink, matching Hyper
	// OAuth dialog's link style. No label, no box — just the URL.
	urlText := func(u string) string {
		if u == "" {
			return ""
		}
		link := linkStyle.Hyperlink(u, "id=mcp-oauth").Render(u)
		return lipgloss.NewStyle().
			Width(innerWidth).
			Padding(0, 1).
			Render(link)
	}

	switch m.state {
	case MCPAuthStatePrompt:
		instructions := instructionStyle.Render("Press ") +
			enterStyle.Render("enter") +
			instructionStyle.Render(" to open your browser.") +
			statusStyle.Render(progress)
		return lipgloss.JoinVertical(
			lipgloss.Left,
			"",
			block(instructions),
			"",
			urlText(server.URL),
			"",
		)

	case MCPAuthStateAuthenticating:
		waiting := successStyle.Render(m.spinner.View()) +
			statusStyle.Render(" Waiting for authorization...")
		return lipgloss.JoinVertical(
			lipgloss.Left,
			"",
			block(waiting),
			"",
			urlText(m.authURL()),
			"",
		)

	case MCPAuthStateSuccess:
		return successStyle.
			Width(innerWidth).
			Padding(1).
			Render("Authentication successful!")

	case MCPAuthStateError:
		errMsg := "Authentication failed."
		if m.err != nil {
			errMsg = m.err.Error()
		}
		return errorStyle.
			Width(innerWidth).
			Padding(1).
			Render(errMsg)

	default:
		return ""
	}
}

// FullHelp returns the full help view.
func (m *MCPAuth) FullHelp() [][]key.Binding {
	return [][]key.Binding{m.ShortHelp()}
}

// ShortHelp returns the short help view.
func (m *MCPAuth) ShortHelp() []key.Binding {
	switch m.state {
	case MCPAuthStatePrompt:
		bindings := []key.Binding{m.keyMap.Submit, m.keyMap.Copy}
		if len(m.pending) > 1 {
			bindings = append(bindings, m.keyMap.Skip)
		}
		return append(bindings, m.keyMap.Close)
	case MCPAuthStateAuthenticating:
		return []key.Binding{m.keyMap.Submit, m.keyMap.Copy, m.keyMap.Close}
	case MCPAuthStateSuccess:
		label := "finish"
		if m.current+1 < len(m.pending) {
			label = "next"
		}
		return []key.Binding{
			key.NewBinding(
				key.WithKeys("enter", "ctrl+y"),
				key.WithHelp("enter", label),
			),
			m.keyMap.Close,
		}
	case MCPAuthStateError:
		return []key.Binding{m.keyMap.Close}
	default:
		return []key.Binding{m.keyMap.Close}
	}
}
