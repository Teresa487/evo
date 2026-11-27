package model

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
)

// hostThemeState tracks theme 主机 application ( editor running
// Crush 在 one 的 它的 panes, 为 example) pushes through 服务端. 一个 host
// theme 是 applied 在 memory 只: it never touches 配置.
//
// host 和 用户 take turns: host's latest change applies, 和
// any theme 用户 applies inside Crush afterwards wins until host
// changes 它的 theme again.
type hostThemeState struct {
	// last 是 most recent host theme received, nil 当 there 是 none
	// 或 host cleared it. 服务端 replays current theme every
	// time 事件 流 attaches, 和 客户端 re-reads it after
	// reconnect, 所以 theme equal 到 last 是 重放 和 是 ignored;
	// otherwise reconnect would undo theme 用户 picked after
	// 主机's.
	last *proto.HostTheme
	// seq counts host themes received 和 用户's theme changes.
	// Styles built 为 older seq 是 stale 和 dropped.
	seq uint64
	// 活跃 reports 那 styles 在 use (或 ones theme preview
	// reverts 到) came 从 host.
	active bool
	// transparent reports 那 m.isTransparent follows host theme's
	// override instead 的 配置.
	transparent bool
}

// transparencyToggledMsg reports 那 用户's transparent background
// toggle reached 配置.
type transparencyToggledMsg struct {
	transparent bool
}

// hostThemeStylesMsg carries styles built off 更新 loop 为 host
// theme, 或, 当 theme 是 nil, Crush's own theme 到 revert 到.
type hostThemeStylesMsg struct {
	seq    uint64
	theme  *proto.HostTheme
	styles styles.Styles
	err    error
}

// handleHostThemeEvent reacts 到 host setting 或 clearing 它的 theme.
// Building styles may 读取 theme files, 所以 it happens 在 command.
func (m *UI) handleHostThemeEvent(msg workspace.HostThemeEvent) tea.Cmd {
	if msg.Theme.Equal(m.hostTheme.last) {
		return nil
	}
	m.hostTheme.last = msg.Theme
	m.hostTheme.seq++

	if theme := msg.Theme; theme != nil {
		return m.buildHostThemeStyles(m.hostTheme.seq, theme)
	}
	if !m.hostTheme.active {
		// 用户's own theme 是 在 screen already; 只 transparency
		// override can 是 left 到 undo.
		m.restoreConfigTransparency()
		return nil
	}
	return m.buildOwnThemeStyles(m.hostTheme.seq)
}

// buildHostThemeStyles builds styles 为 host theme.
func (m *UI) buildHostThemeStyles(seq uint64, theme *proto.HostTheme) tea.Cmd {
	return func() tea.Msg {
		s, err := styles.HostThemeStyles(theme.Base, theme.Palette, theme.Syntax)
		return hostThemeStylesMsg{seq: seq, theme: theme, styles: s, err: err}
	}
}

// buildOwnThemeStyles builds theme Crush shows without 主机 theme:
// configured one, else one 为 current 提供方.
func (m *UI) buildOwnThemeStyles(seq uint64) tea.Cmd {
	name := common.ThemeNameFromConfig(m.com.Config())
	key := m.themeKey
	return func() tea.Msg {
		s := styles.ThemeForKey(key)
		if name != "" {
			s = styles.ThemeFromConfig(name)
		}
		return hostThemeStylesMsg{seq: seq, styles: s}
	}
}

// handleHostThemeStyles applies styles built 通过 [UI.handleHostThemeEvent]
// unless newer host theme, 或 theme 用户 applied, superseded them
// 在 meantime.
func (m *UI) handleHostThemeStyles(msg hostThemeStylesMsg) tea.Cmd {
	if msg.seq != m.hostTheme.seq {
		return nil
	}
	if msg.err != nil {
		return util.ReportError(fmt.Errorf("apply host theme %q: %w", msg.theme.Name, msg.err))
	}

	if m.preThemeStyles != nil {
		// 一个 theme preview 是 open: make host's styles ones it
		// reverts 到, 所以 esc lands 在 them rather than 在 stale ones.
		s := msg.styles
		m.preThemeStyles = &s
	} else {
		m.applyTheme(msg.styles)
	}
	m.hostTheme.active = msg.theme != nil

	if msg.theme != nil && msg.theme.Transparent != nil {
		m.isTransparent = *msg.theme.Transparent
		m.hostTheme.transparent = true
	} else {
		m.restoreConfigTransparency()
	}
	return nil
}

// userAppliedTheme records 那 用户 applied theme inside Crush. 它
// wins over 主机 theme, including 主机's transparency override,
// until host changes 它的 theme again; host styles still being built
// 是 dropped.
func (m *UI) userAppliedTheme() {
	m.hostTheme.active = false
	m.hostTheme.seq++
	m.restoreConfigTransparency()
}

// restoreConfigTransparency hands background transparency back 到
// 配置 当 it follows host override.
func (m *UI) restoreConfigTransparency() {
	if !m.hostTheme.transparent {
		return
	}
	m.hostTheme.transparent = false
	cfg := m.com.Config()
	m.isTransparent = cfg != nil && cfg.Options != nil && cfg.Options.TUI.IsTransparent()
}
