package login

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/tone"
	"github.com/charmbracelet/x/ansi"
	"github.com/pkg/browser"

	"github.com/charmbracelet/crush/internal/oauth"
)

// authState represents state 的 OAuth TUI flow.
type authState int

const (
	authStateIntro authState = iota
	authStatePreparing
	authStateWaiting
	authStateExchanging
	authStateError
)

// authKeyMap represents key bindings 为 OAuth TUI.
type authKeyMap struct {
	Continue key.Binding
	Cancel   key.Binding
	Submit   key.Binding
}

// defaultAuthKeybinds returns 默认 key bindings 为 OAuth TUI.
func defaultAuthKeybinds() authKeyMap {
	return authKeyMap{
		Continue: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "continue"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("ctrl+c", "esc"),
			key.WithHelp("ctrl+c", "cancel"),
		),
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "submit code"),
		),
	}
}

// ShortHelp returns key bindings 为 short help screen.
func (k authKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Continue, k.Submit, k.Cancel}
}

// FullHelp returns key bindings 为 full help screen.
func (k authKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Continue, k.Submit, k.Cancel}}
}

// updateAuthKeymap enables/disables key bindings based 在 current state.
func (m *authModel) updateAuthKeymap() {
	m.keymap.Continue.SetEnabled(m.state == authStateIntro)
	m.keymap.Submit.SetEnabled(m.state == authStateWaiting && m.codeEntry)
}

// authModel 是 Bubble Tea 模型 为 OAuth authorization flow.
type authModel struct {
	platform string
	newFlow  func() flow
	flow     flow

	state authState

	spinner spinner.Model

	verificationURL string
	userCode        string

	// codeEntry 是 set 当 flow can finish 用 pasted code 从
	// authorization page; codeInput 是 它的 paste 字段, shown while
	// waiting 为 browser callback, 和 codeErr last paste
	// failure. callback 和 paste race: whichever lands first
	// wins, 和 failed paste keeps flow waiting.
	codeEntry bool
	codeInput textinput.Model
	codeErr   string

	help   help.Model
	keymap authKeyMap

	token    *oauth.Token
	err      error
	canceled bool
	quitting bool
	// blankLines 是 height 的 last rendered frame, used 到 blank it
	// out 在 final render 在 quit.
	blankLines int
	width      int
}

type authReadyMsg struct {
	flow     flow
	url      string
	userCode string
}

type authTokenMsg struct {
	token *oauth.Token
	err   error
}

// authCodeResultMsg carries outcome 的 pasted-code exchange. 一个
// failure 保持 流程 waiting: 回环 callback may still
// arrive, 和 用户 can 重试 paste.
type authCodeResultMsg struct {
	token *oauth.Token
	err   error
}

type authErrMsg struct {
	err error
}

func newAuthModel(platform string, newFlow func() flow) authModel {
	s := spinner.New(spinner.WithSpinner(spinner.Dot))
	s.Style = lipgloss.NewStyle().Foreground(tone.Julep)
	m := authModel{
		platform: platform,
		newFlow:  newFlow,
		state:    authStateIntro,
		spinner:  s,
		help:     help.New(),
		keymap:   defaultAuthKeybinds(),
	}
	m.updateAuthKeymap()
	return m
}

// runTUI runs OAuth authorization flow 用 small TUI 那 guides
// 用户 through opening browser 和 waiting 为 callback.
func runTUI(platform string, newFlow func() flow) (*oauth.Token, error) {
	// Don't leak errors 到 TUI
	browser.Stderr = nil
	browser.Stdout = nil
	p := tea.NewProgram(newAuthModel(platform, newFlow))
	final, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("running auth program: %w", err)
	}
	m, ok := final.(authModel)
	if !ok {
		return nil, errors.New("unexpected auth program model")
	}
	if m.flow != nil {
		m.flow.Close()
	}
	if m.err != nil {
		return nil, m.err
	}
	if m.canceled {
		return nil, errors.New("authentication canceled")
	}
	return m.token, nil
}

// Init initializes the auth TUI.
func (m authModel) Init() tea.Cmd {
	return nil
}

// 更新 handles messages 为 auth TUI.
func (m authModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case authReadyMsg:
		m.flow = msg.flow
		m.verificationURL = msg.url
		m.userCode = msg.userCode
		m.state = authStateWaiting
		m.updateAuthKeymap()
		if msg.userCode == "" {
			if _, ok := m.flow.(codeEntryFlow); ok {
				m.codeEntry = true
				m.codeInput = textinput.New()
				m.codeInput.Prompt = "> "
				m.codeInput.Placeholder = "Paste the code from the browser..."
				m.codeInput.SetWidth(max(0, m.authWidth()-4))
				m.codeInput.Focus()
				m.updateAuthKeymap()
			}
		}
		cmds := []tea.Cmd{openURLCmd(msg.url), waitCmd(m.flow), m.spinner.Tick}
		if msg.userCode != "" {
			cmds = append(cmds, tea.SetClipboard(msg.userCode))
		}
		return m, tea.Batch(cmds...)

	case authTokenMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.token = msg.token
		}
		m.blankLines = m.contentHeight()
		m.quitting = true
		return m, tea.Quit

	case authErrMsg:
		m.err = msg.err
		m.blankLines = m.contentHeight()
		m.quitting = true
		return m, tea.Quit

	case authCodeResultMsg:
		if msg.err != nil {
			// Keep waiting: callback can still arrive 和 paste
			// can 是 retried.
			m.codeErr = msg.err.Error()
			m.codeInput.SetValue("")
			return m, nil
		}
		m.token = msg.token
		m.blankLines = m.contentHeight()
		m.quitting = true
		return m, tea.Quit

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		switch m.state {
		case authStatePreparing, authStateWaiting, authStateExchanging:
			return m, cmd
		default:
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.help.SetWidth(msg.Width)
		if m.codeEntry {
			m.codeInput.SetWidth(max(0, m.authWidth()-4))
		}
		return m, nil

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keymap.Cancel):
			m.canceled = true
			m.blankLines = m.contentHeight()
			m.quitting = true
			return m, tea.Quit
		case key.Matches(msg, m.keymap.Continue):
			if m.state == authStateIntro {
				m.state = authStatePreparing
				m.updateAuthKeymap()
				return m, tea.Batch(startFlowCmd(m.newFlow), m.spinner.Tick)
			}
		}
		// While paste 字段 是 up, typing lands 在 it: enter submits
		// code, 和 取消 binding above still quits.
		if m.state == authStateWaiting && m.codeEntry {
			if key.Matches(msg, m.keymap.Submit) {
				value := strings.TrimSpace(m.codeInput.Value())
				if value == "" {
					return m, nil
				}
				f, ok := m.flow.(codeEntryFlow)
				if !ok {
					return m, nil
				}
				return m, submitCodeCmd(f, value)
			}
			var cmd tea.Cmd
			m.codeInput, cmd = m.codeInput.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.help, cmd = m.help.Update(msg)
		return m, cmd

	case tea.PasteMsg:
		if m.state == authStateWaiting && m.codeEntry {
			var cmd tea.Cmd
			m.codeInput, cmd = m.codeInput.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

func (m authModel) authHeader() string {
	title := "Let’s authenticate with " + titles[m.platform]
	w := m.authWidth()
	if lipgloss.Width(title) > w {
		title = ansi.Truncate(title, w, "…")
		return "\n  " + headerStyle.Render(title)
	}
	header := headerStyle.Render(title)
	if remaining := w - lipgloss.Width(title) - 1; remaining > 0 {
		header += " " + m.slashes(min(remaining, maxHeaderSlashes))
	}
	return "\n  " + header
}

// maxHeaderSlashes caps how wide decorative slash 运行 can get.
const maxHeaderSlashes = 50

// slashes renders n diagonal slashes 用 horizontal gradient 那 fades
// 从 header purple 到 pink, echoing dialog 标题 treatment.
func (authModel) slashes(n int) string {
	ramp := lipgloss.Blend1D(n, tone.Charple, tone.Dolly)
	var b strings.Builder
	for _, c := range ramp {
		b.WriteString(slashStyle.Foreground(c).Render("╱"))
	}
	return b.String()
}

// authWidth returns usable 文本 width 为 auth TUI, accounting 为
// 2-space indent. Falls back 到 80 当 终态 width 是 unknown.
func (m authModel) authWidth() int {
	const indent = 2
	w := m.width - indent
	if w < 20 {
		w = 80 - indent
	}
	return w
}

// View 渲染 auth TUI.
func (m authModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	return tea.NewView(m.content())
}

// content 渲染 full auth view. trailing zero-width space 保持
// the final blank line alive (the renderer erases trailing empty lines).
func (m authModel) content() string {
	var b strings.Builder
	wrap := lipgloss.NewStyle().MaxWidth(m.authWidth())

	b.WriteString(m.authHeader())
	b.WriteString("\n\n  ")

	switch m.state {
	case authStateIntro:
		b.WriteString(wrap.Render("To authenticate we're going to open the browser. Ready?"))
	case authStatePreparing:
		b.WriteString(m.spinner.View())
		b.WriteString(wrap.Render("Preparing..."))
	case authStateWaiting:
		if m.userCode != "" {
			b.WriteString(wrap.Render("Your code is "))
			b.WriteString(lipgloss.NewStyle().Foreground(tone.Julep).Render(m.userCode))
			b.WriteString(" ")
			b.WriteString(lipgloss.NewStyle().Foreground(tone.Oyster).Render("copied to clipboard"))
			b.WriteString("\n\n  ")
		}
		urlStyle := lipgloss.NewStyle().
			Foreground(tone.Guac).
			Underline(true).
			Hyperlink(m.verificationURL).
			MaxWidth(m.authWidth())
		b.WriteString(wrap.Render("Browser not opening? Visit:"))
		b.WriteString("\n  ")
		b.WriteString(urlStyle.Render(m.verificationURL))
		b.WriteString("\n\n  ")
		b.WriteString(m.spinner.View())
		b.WriteString(wrap.Render("Waiting for authorization..."))
		if m.codeEntry {
			b.WriteString("\n\n  ")
			b.WriteString(wrap.Render("Declined or browser didn't connect? Enter the code from the page:"))
			b.WriteString("\n\n  ")
			b.WriteString(m.codeInput.View())
			if m.codeErr != "" {
				b.WriteString("\n\n  ")
				b.WriteString(errorStyle.Render(wrap.Render(m.codeErr)))
			}
		}
	case authStateExchanging:
		b.WriteString(m.spinner.View())
		b.WriteString(wrap.Render("Exchanging token..."))
	case authStateError:
		b.WriteString(errorStyle.Render(wrap.Render(m.err.Error())))
	default:
		return ""
	}

	b.WriteString("\n\n  ")
	b.WriteString(m.help.View(m.keymap))
	b.WriteString("\n")
	b.WriteString(trailingSpace)
	return b.String()
}

// contentHeight returns height 的 current view 在 lines.
func (m authModel) contentHeight() int {
	return lipgloss.Height(m.content())
}

// startFlowCmd kicks off platform's OAuth flow 和 returns URL 到
// open 在 browser along 用 optional 用户 code.
func startFlowCmd(newFlow func() flow) tea.Cmd {
	return func() tea.Msg {
		f := newFlow()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		url, userCode, err := f.Start(ctx)
		if err != nil {
			return authErrMsg{err: err}
		}

		return authReadyMsg{
			flow:     f,
			url:      url,
			userCode: userCode,
		}
	}
}

// openURLCmd opens browser off 更新 loop. Failure needs no
// handling: waiting view always shows URL 到 visit 通过 hand.
func openURLCmd(url string) tea.Cmd {
	return func() tea.Msg {
		_ = browser.OpenURL(url)
		return nil
	}
}

// waitCmd blocks until 用户 completes authorization 在 browser 和
// returns resulting 令牌.
func waitCmd(f flow) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		token, err := f.Wait(ctx)
		return authTokenMsg{token: token, err: err}
	}
}

// submitCodeCmd exchanges pasted code 为 token off 更新 loop,
// racing loopback callback still being waited 在.
func submitCodeCmd(f codeEntryFlow, input string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		token, err := f.CompleteWithCode(ctx, input)
		return authCodeResultMsg{token: token, err: err}
	}
}
