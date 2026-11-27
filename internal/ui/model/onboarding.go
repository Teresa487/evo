package model

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// markProjectInitializedCmd marks current project 作为 initialized 在 配置.
func (m *UI) markProjectInitializedCmd() tea.Cmd {
	return func() tea.Msg {
		if err := m.com.Workspace.MarkProjectInitialized(); err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("Failed to mark project as initialized: %v", err),
				TTL:  15 * time.Second,
			}
		}
		return nil
	}
}

// updateInitializeView handles keyboard input 为 project initialization 提示词.
func (m *UI) updateInitializeView(msg tea.KeyPressMsg) (cmds []tea.Cmd) {
	switch {
	case key.Matches(msg, m.keyMap.Initialize.Enter):
		if m.onboarding.yesInitializeSelected {
			cmds = append(cmds, m.initializeProject())
		} else {
			cmds = append(cmds, m.skipInitializeProject())
		}
	case key.Matches(msg, m.keyMap.Initialize.Switch):
		m.onboarding.yesInitializeSelected = !m.onboarding.yesInitializeSelected
	case key.Matches(msg, m.keyMap.Initialize.Yes):
		cmds = append(cmds, m.initializeProject())
	case key.Matches(msg, m.keyMap.Initialize.No):
		cmds = append(cmds, m.skipInitializeProject())
	}
	return cmds
}

// initializeProject starts project initialization 和 transitions 到 landing view.
func (m *UI) initializeProject() tea.Cmd {
	// clear 会话
	var cmds []tea.Cmd
	if cmd := m.newSession(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	initialize := func() tea.Msg {
		initPrompt, err := m.com.Workspace.InitializePrompt()
		if err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("Failed to initialize project: %v", err),
			}
		}
		return sendMessageMsg{Content: initPrompt}
	}
	// 标记 project 作为 initialized
	cmds = append(cmds, initialize, m.markProjectInitializedCmd())

	return tea.Sequence(cmds...)
}

// skipInitializeProject skips project initialization 和 transitions 到 landing view.
func (m *UI) skipInitializeProject() tea.Cmd {
	// TODO: initialize the project
	m.setState(uiLanding, uiFocusEditor)
	// 标记 project 作为 initialized
	return m.markProjectInitializedCmd()
}

// initializeView renders project initialization 提示词 用 Yes/No buttons.
func (m *UI) initializeView() string {
	s := m.com.Styles.Initialize
	cwd := home.Short(m.com.Workspace.WorkingDir())
	initFile := m.com.Config().Options.InitializeAs

	header := s.Header.Render("Would you like to initialize this project?")
	path := s.Accent.PaddingLeft(2).Render(cwd)
	desc := s.Content.Render(fmt.Sprintf("When I initialize your codebase I examine the project and put the result into an %s file which serves as general context.", initFile))
	hint := s.Content.Render("You can also initialize anytime via ") + s.Accent.Render("ctrl+p") + s.Content.Render(".")
	prompt := s.Content.Render("Would you like to initialize now?")

	buttons := common.ButtonGroup(m.com.Styles, []common.ButtonOpts{
		{Text: "Yep!", Selected: m.onboarding.yesInitializeSelected},
		{Text: "Nope", Selected: !m.onboarding.yesInitializeSelected},
	}, " ")

	// max width 60 所以 文本 是 compact
	width := min(m.layout.main.Dx(), 60)

	return lipgloss.NewStyle().
		Width(width).
		Height(m.layout.main.Dy()).
		PaddingBottom(1).
		AlignVertical(lipgloss.Bottom).
		Render(strings.Join(
			[]string{
				header,
				path,
				desc,
				hint,
				prompt,
				buttons,
			},
			"\n\n",
		))
}
