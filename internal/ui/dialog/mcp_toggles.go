package dialog

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
)

// MCPTogglesID 是 identifier 为 MCP toggles dialog.
const MCPTogglesID = "mcp_toggles"

// MCPToggleItem describes one configured MCP 服务端 在 toggles dialog.
type MCPToggleItem struct {
	Name string
	// Disabled 是 repository-scoped override: 当 true 服务端's
	// tools 是 hidden 从 every 会话 在 这 repository.
	Disabled bool
	// ConfigDisabled 是 服务端's disabled flag 在 配置, before
	// any repository-scoped override. Global scope reads 和 writes
	// 这 flag.
	ConfigDisabled bool
	// EnabledOverride 是 repository-scoped enabled override: 配置
	// 服务端 enabled locally 为 这 repository. Only Local scope
	// considers it.
	EnabledOverride bool
	// Status 是 human-readable 连接 status.
	Status string
}

// localDisabled returns effective local state: 配置-disabled
// 服务端 stays disabled locally unless repository enabled override
// turned it 在.
func (i MCPToggleItem) localDisabled() bool {
	return i.Disabled || (i.ConfigDisabled && !i.EnabledOverride)
}

// ActionToggleMCP 是 sent 当 用户 toggles MCP 服务端. Local
// toggles persist repository-scoped override; global toggles 写入
// disabled flag 到 global 配置.
type ActionToggleMCP struct {
	Name     string
	Disabled bool
	Global   bool
}

// MCPToggleScope selects which 存储 toggle affects.
type MCPToggleScope int

const (
	// MCPToggleScopeLocal persists repository-scoped overrides.
	MCPToggleScopeLocal MCPToggleScope = iota
	// MCPToggleScopeGlobal writes disabled flag 到 配置.
	MCPToggleScopeGlobal
)

// String returns radio label 为 scope.
func (s MCPToggleScope) String() string {
	if s == MCPToggleScopeGlobal {
		return "Global"
	}
	return "Local"
}

// MCPToggles lets 用户 enable 和 disable MCP servers, either 为
// current repository (Local, 默认) 或 在 配置 (Global).
type MCPToggles struct {
	com    *common.Common
	width  int
	items  []MCPToggleItem
	cursor int
	// offset 是 first visible 行; 窗口 follows cursor.
	offset int
	scope  MCPToggleScope
	help   help.Model
	keyMap struct {
		Up     key.Binding
		Down   key.Binding
		Toggle key.Binding
		Scope  key.Binding
		Close  key.Binding
	}
}

var _ Dialog = (*MCPToggles)(nil)

// NewMCPToggles creates a new MCP toggles dialog.
func NewMCPToggles(com *common.Common, items []MCPToggleItem) *MCPToggles {
	t := com.Styles
	m := &MCPToggles{
		com:   com,
		width: 0, // Set dynamically in Draw().
		items: items,
	}

	m.help = help.New()
	m.help.Styles = t.DialogHelpStyles()

	m.keyMap.Up = key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	)
	m.keyMap.Down = key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	)
	m.keyMap.Toggle = key.NewBinding(
		key.WithKeys("enter", " ", "space"),
		key.WithHelp("enter", "toggle"),
	)
	m.keyMap.Scope = key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch scope"),
	)
	m.keyMap.Close = CloseKey

	return m
}

// ID implements Dialog.
func (m *MCPToggles) ID() string {
	return MCPTogglesID
}

// Items returns the current items.
func (m *MCPToggles) Items() []MCPToggleItem {
	return m.items
}

// Scope returns the selected toggle scope.
func (m *MCPToggles) Scope() MCPToggleScope {
	return m.scope
}

// HandleMsg implements Dialog.
func (m *MCPToggles) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keyMap.Up):
			m.cursor = max(0, m.cursor-1)
			m.offset = toggleVisibleOffset(m.cursor, m.offset, len(m.items), maxVisibleToggleRows)
		case key.Matches(msg, m.keyMap.Down):
			m.cursor = min(len(m.items)-1, m.cursor+1)
			m.offset = toggleVisibleOffset(m.cursor, m.offset, len(m.items), maxVisibleToggleRows)
		case key.Matches(msg, m.keyMap.Scope):
			if m.scope == MCPToggleScopeLocal {
				m.scope = MCPToggleScopeGlobal
			} else {
				m.scope = MCPToggleScopeLocal
			}
		case key.Matches(msg, m.keyMap.Toggle):
			if m.cursor < 0 || m.cursor >= len(m.items) {
				return nil
			}
			item := m.items[m.cursor]
			// Toggle based 在 effective state 为 活跃 scope:
			// local considers the repository overrides, global reads the
			// 配置's raw disabled flag.
			currentlyDisabled := item.localDisabled()
			if m.scope == MCPToggleScopeGlobal {
				currentlyDisabled = item.ConfigDisabled
			}
			newState := !currentlyDisabled
			if m.scope == MCPToggleScopeGlobal {
				m.items[m.cursor].ConfigDisabled = newState
			} else {
				m.items[m.cursor].Disabled = newState
				// 一个 服务端 enabled locally 是 started 在 runtime; surface
				// 那 immediately instead 的 waiting 为 连接
				// state 事件.
				if !newState {
					m.items[m.cursor].EnabledOverride = true
					m.items[m.cursor].Status = "starting"
				}
			}
			return ActionToggleMCP{
				Name:     item.Name,
				Disabled: newState,
				Global:   m.scope == MCPToggleScopeGlobal,
			}
		case key.Matches(msg, m.keyMap.Close):
			return ActionClose{}
		}
	}
	return nil
}

// Draw implements Dialog.
func (m *MCPToggles) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := m.com.Styles
	m.width = max(0, min(m.requiredWidth(t), area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	DrawCenter(scr, area, m.dialogContent())
	return nil
}

// requiredWidth returns width needed 到 fit widest 行 (status
// dot, name, 在 least one space, 和 status) 在 single line, plus 行
// padding 和 dialog frame. 一个 fixed 64-column cap word-wraps long
// 服务端 names onto second line.
func (m *MCPToggles) requiredWidth(t *styles.Styles) int {
	widest := minToggleDialogWidth
	for _, item := range m.items {
		row := 2 /* dot + space */ + lipgloss.Width(item.Name) + 1 + lipgloss.Width(m.itemStatus(item))
		widest = max(widest, row)
	}
	return widest + 2 /* row padding */ + t.Dialog.View.GetHorizontalFrameSize()
}

func (m *MCPToggles) dialogContent() string {
	t := m.com.Styles
	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()
	rc := NewRenderContext(t, m.width)
	rc.Title = "Toggle MCPs"
	rc.TitleInfo = m.scopeRadioView(t)
	rc.AddPart(m.innerContent())
	rc.Help = renderDialogHelp(t, &m.help, m, innerWidth)
	return rc.Render()
}

// scopeRadioView 渲染 Local/Global radio selector, mirroring
// command palette's System/用户 switch 在 标题 line.
func (m *MCPToggles) scopeRadioView(t *styles.Styles) string {
	radio := func(s MCPToggleScope) string {
		bullet := t.Radio.Off
		if s == m.scope {
			bullet = t.Radio.On
		}
		return bullet.Render() + t.Radio.Label.Padding(0, 1).Render(s.String())
	}
	return " " + radio(MCPToggleScopeLocal) + " " + radio(MCPToggleScopeGlobal)
}

func (m *MCPToggles) innerContent() string {
	t := m.com.Styles
	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()

	if len(m.items) == 0 {
		return t.Dialog.SecondaryText.
			Width(innerWidth).
			Padding(0, 1).
			Render("No MCP servers configured.")
	}

	// Cap visible rows 所以 long 服务端 list cannot make dialog
	// grow past screen; 窗口 follows cursor.
	visible := min(maxVisibleToggleRows, len(m.items))
	first := max(0, min(m.offset, len(m.items)-visible))
	last := min(len(m.items), first+visible)

	// 行 style adds Padding(0, 1), 所以 文本 area 是 two columns
	// narrower than the dialog's inner width. When the list scrolls,
	// reserve scrollbar column 所以 行 lines do 不 wrap 和 knock
	// track out 的 alignment.
	rowWidth := max(0, innerWidth-2)
	if len(m.items) > visible {
		rowWidth = max(0, rowWidth-scrollbarColumnWidth)
	}

	rows := make([]string, 0, visible)
	for i := first; i < last; i++ {
		item := m.items[i]
		status := m.itemStatus(item)
		// The status dot mirrors the sidebar: green connected, yellow
		// starting, red 错误, gray disabled/offline. Icon styles carry
		// their own "●" via SetString, 所以 Render() yields just dot.
		// 它 sits left 的 name, like sidebar rows.
		dot := statusDot(t, status)
		gap := max(1, rowWidth-2 /* dot + space */ -lipgloss.Width(item.Name)-lipgloss.Width(status))

		if i == m.cursor {
			// full 行 goes through selection style 在 plain
			// 文本: styled dot 或 status inside content would emit
			// ANSI resets 那 clear selection background 为
			// rest 的 line, leaving status unhighlighted.
			rows = append(rows, t.Dialog.SelectedItem.Render(
				"● "+item.Name+strings.Repeat(" ", gap)+status,
			))
			continue
		}

		// OnlineText (不 OnlineIcon) 是 文本 style; dot 是
		// rendered separately 所以 no "●" prefix sneaks into 文本
		// 和 pushes 行 over dialog width.
		statusStyle := t.Resource.OnlineText
		if status == "disabled" || status == "starting" {
			// UnsetPadding: SecondaryText carries 它的 own Padding(0, 1),
			// which would widen 行 one column past every other 行.
			statusStyle = t.Dialog.SecondaryText.UnsetPadding()
		}
		row := dot.Render() + " " +
			t.Dialog.NormalItem.UnsetPadding().Render(item.Name) +
			strings.Repeat(" ", gap) +
			statusStyle.Render(status)
		// 一个 single Padding(0, 1) around 行.
		rows = append(rows, lipgloss.NewStyle().Padding(0, 1).Render(row))
	}

	return joinToggleRows(t, rows, len(m.items), visible, first)
}

// statusDot maps status label 到 sidebar's status icon style.
func statusDot(t *styles.Styles, status string) lipgloss.Style {
	switch {
	case status == "connected" || status == "active":
		return t.Resource.OnlineIcon
	case status == "starting":
		return t.Resource.BusyIcon
	case status == "error" || strings.HasPrefix(status, "error:"):
		return t.Resource.ErrorIcon
	case status == "needs authentication":
		return t.Resource.NeedsAuthIcon
	default:
		// disabled, 离线
		return t.Resource.DisabledIcon
	}
}

// itemStatus returns right-hand status label 为 item. live
// 连接 state speaks 为 itself: 配置-disabled 服务端 那 was
// runtime-enabled shows "starting"/"connected", 原样 one shows
// "disabled" via 它的 连接 state. Only repository override (local
// scope) 或 配置 flag (global scope) forces "disabled" label
// over live 连接.
func (m *MCPToggles) itemStatus(item MCPToggleItem) string {
	if m.scope == MCPToggleScopeGlobal {
		if item.ConfigDisabled {
			return "disabled"
		}
		// live 连接 reflects local override, which takes
		// precedence; global setting itself 是 still enabled.
		if item.localDisabled() {
			return "enabled"
		}
		return item.Status
	}
	if item.localDisabled() {
		return "disabled"
	}
	return item.Status
}

// SetItemStatus refreshes one item's live 连接 status without
// touching 它的 Disabled override, 所以 open dialog updates 作为 servers
// finish connecting.
func (m *MCPToggles) SetItemStatus(name, status string) {
	for i, item := range m.items {
		if item.Name == name {
			m.items[i].Status = status
			return
		}
	}
}

// FullHelp implements help.KeyMap.
func (m *MCPToggles) FullHelp() [][]key.Binding {
	return [][]key.Binding{m.ShortHelp()}
}

// ShortHelp implements help.KeyMap.
func (m *MCPToggles) ShortHelp() []key.Binding {
	return []key.Binding{m.keyMap.Up, m.keyMap.Down, m.keyMap.Toggle, m.keyMap.Scope, m.keyMap.Close}
}
