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

// SkillsTogglesID 是 identifier 为 skills toggles dialog.
const SkillsTogglesID = "skills_toggles"

// SkillToggleItem describes one skill 在 toggles dialog.
type SkillToggleItem struct {
	Name string
	// Disabled 是 repository-scoped override: 当 true skill 是
	// hidden 从 agent 在 这 repository.
	Disabled bool
	// ConfigDisabled 是 skill's disabled state 在 配置, before
	// any repository-scoped override. Global scope reads 和 writes
	// 这 flag.
	ConfigDisabled bool
	// EnabledOverride 是 repository-scoped enabled override:
	// 配置-disabled skill 用户 enabled here. Only Local scope
	// considers it.
	EnabledOverride bool
	// 错误 是 non-empty 当 skill failed discovery (malformed
	// frontmatter, unreadable 文件, ...). Errored skills show red ×
	// 和 错误 在 right edge instead 的 enabled check.
	Error string
}

// localDisabled returns effective local state: 配置-disabled
// skill stays disabled locally unless the repository enabled override
// turned it 在.
func (i SkillToggleItem) localDisabled() bool {
	return i.Disabled || (i.ConfigDisabled && !i.EnabledOverride)
}

// ActionToggleSkill 是 sent 当 用户 toggles skill. Local toggles
// persist repository-scoped override; global toggles edit 配置's
// options.disabled_skills list.
type ActionToggleSkill struct {
	Name     string
	Disabled bool
	Global   bool
}

// SkillToggles lets 用户 enable 和 disable skills, either 为
// current repository (Local, 默认) 或 在 配置 (Global).
type SkillToggles struct {
	com    *common.Common
	width  int
	items  []SkillToggleItem
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

var _ Dialog = (*SkillToggles)(nil)

// NewSkillsToggles creates a new skills toggles dialog.
func NewSkillsToggles(com *common.Common, items []SkillToggleItem) *SkillToggles {
	t := com.Styles
	m := &SkillToggles{
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
func (m *SkillToggles) ID() string {
	return SkillsTogglesID
}

// Items returns the current items.
func (m *SkillToggles) Items() []SkillToggleItem {
	return m.items
}

// SetItems replaces items, keeping cursor clamped, 所以 open
// dialog updates after toggle 或 overrides refresh.
func (m *SkillToggles) SetItems(items []SkillToggleItem) {
	m.items = items
	m.cursor = min(m.cursor, max(0, len(items)-1))
}

// Scope returns the selected toggle scope.
func (m *SkillToggles) Scope() MCPToggleScope {
	return m.scope
}

// HandleMsg implements Dialog.
func (m *SkillToggles) HandleMsg(msg tea.Msg) Action {
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
			currentlyDisabled := item.localDisabled()
			if m.scope == MCPToggleScopeGlobal {
				currentlyDisabled = item.ConfigDisabled
			}
			newState := !currentlyDisabled
			if m.scope == MCPToggleScopeGlobal {
				m.items[m.cursor].ConfigDisabled = newState
			} else {
				m.items[m.cursor].Disabled = newState
				if item.ConfigDisabled && !newState {
					m.items[m.cursor].EnabledOverride = true
				}
			}
			return ActionToggleSkill{
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
func (m *SkillToggles) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := m.com.Styles
	m.width = max(0, min(m.requiredWidth(t), area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	DrawCenter(scr, area, m.dialogContent())
	return nil
}

// requiredWidth returns width needed 到 fit widest 行 (status
// dot, name, 在 least one space, 和 status) 在 single line, plus 行
// padding 和 dialog frame.
func (m *SkillToggles) requiredWidth(t *styles.Styles) int {
	widest := minToggleDialogWidth
	for _, item := range m.items {
		row := 2 /* check column */ + lipgloss.Width(item.Name)
		if item.Error != "" {
			// + 1 gap + red "错误" label 在 right edge.
			row += 1 + lipgloss.Width("error")
		}
		widest = max(widest, row)
	}
	return widest + 2 /* row padding */ + t.Dialog.View.GetHorizontalFrameSize()
}

func (m *SkillToggles) dialogContent() string {
	t := m.com.Styles
	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()
	rc := NewRenderContext(t, m.width)
	rc.Title = "Toggle Skills"
	rc.TitleInfo = m.scopeRadioView(t)
	rc.AddPart(m.innerContent())
	rc.Help = renderDialogHelp(t, &m.help, m, innerWidth)
	return rc.Render()
}

// scopeRadioView 渲染 Local/Global radio selector, mirroring
// command palette's System/用户 switch 在 标题 line.
func (m *SkillToggles) scopeRadioView(t *styles.Styles) string {
	radio := func(s MCPToggleScope) string {
		bullet := t.Radio.Off
		if s == m.scope {
			bullet = t.Radio.On
		}
		return bullet.Render() + t.Radio.Label.Padding(0, 1).Render(s.String())
	}
	return " " + radio(MCPToggleScopeLocal) + " " + radio(MCPToggleScopeGlobal)
}

func (m *SkillToggles) innerContent() string {
	t := m.com.Styles
	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()

	if len(m.items) == 0 {
		return t.Dialog.SecondaryText.
			Width(innerWidth).
			Padding(0, 1).
			Render("No skills configured.")
	}

	// Cap visible rows 所以 long skill list cannot make dialog
	// grow past screen; 窗口 follows cursor.
	visible := min(maxVisibleToggleRows, len(m.items))
	first := m.visibleOffset(visible)
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
		// Enabled skills get green check 在 fixed-width column 所以
		// names align; disabled skills show nothing 在 it — skills have
		// no 连接 state worth status column like MCPs. Errored
		// skills show red × regardless 的 toggle state: they 是
		// "在" 但 failed 到 load.
		mark := " " // Reserved check column so names align.
		switch {
		case item.Error != "":
			mark = t.Tool.IconError.Render()
		case !m.itemDisabled(item):
			mark = t.Tool.IconSuccess.Render()
		}

		// Errored skills carry red "错误" label 在 right edge.
		statusCol := ""
		if item.Error != "" {
			statusCol = t.Tool.IconError.SetString("error").Render()
		}
		statusWidth := lipgloss.Width(statusCol)

		if i == m.cursor {
			// full 行 goes through selection style 在 plain
			// 文本: styled check inside content would emit ANSI
			// resets 那 clear selection background 为 rest 的
			// line. Padding 到 rowWidth keeps highlight spanning
			// 行 和 scrollbar column 在 right edge.
			// Disabled skills keep empty gutter here too, 所以 check
			// never appears just because 行 happens 到 是 selected.
			glyph := styles.CheckIcon
			if m.itemDisabled(item) {
				glyph = " "
			}
			if item.Error != "" {
				glyph = styles.ToolError
			}
			row := glyph + " " + item.Name +
				strings.Repeat(" ", max(1, rowWidth-2-lipgloss.Width(item.Name)-statusWidth)) +
				statusCol
			rows = append(rows, t.Dialog.SelectedItem.Render(row))
			continue
		}

		row := mark + " " +
			t.Dialog.NormalItem.UnsetPadding().Render(item.Name) +
			strings.Repeat(" ", max(1, rowWidth-2-lipgloss.Width(item.Name)-statusWidth)) +
			statusCol
		rows = append(rows, lipgloss.NewStyle().Padding(0, 1).Render(row))
	}

	return joinToggleRows(t, rows, len(m.items), visible, first)
}

// visibleOffset returns 存储 first visible 行 index, clamped 到
// the current item count.
func (m *SkillToggles) visibleOffset(visible int) int {
	return max(0, min(m.offset, len(m.items)-visible))
}

// itemDisabled reports whether item shows 作为 disabled given
// 活跃 scope: Local reads repository overrides, Global reads
// 配置's raw disabled flag.
func (m *SkillToggles) itemDisabled(item SkillToggleItem) bool {
	if m.scope == MCPToggleScopeGlobal {
		return item.ConfigDisabled
	}
	return item.localDisabled()
}

// FullHelp implements help.KeyMap.
func (m *SkillToggles) FullHelp() [][]key.Binding {
	return [][]key.Binding{m.ShortHelp()}
}

// ShortHelp implements help.KeyMap.
func (m *SkillToggles) ShortHelp() []key.Binding {
	return []key.Binding{m.keyMap.Up, m.keyMap.Down, m.keyMap.Toggle, m.keyMap.Scope, m.keyMap.Close}
}
