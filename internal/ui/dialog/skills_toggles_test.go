package dialog

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func newSkillsTogglesForTest(items []SkillToggleItem) *SkillToggles {
	s := styles.CharmtonePantera()
	com := &common.Common{Styles: &s}
	return NewSkillsToggles(com, items)
}

func TestSkillsToggles_Toggle(t *testing.T) {
	t.Parallel()

	m := newSkillsTogglesForTest([]SkillToggleItem{
		{Name: "charmtone"},
		{Name: "pair", Disabled: true},
		{Name: "frozen", ConfigDisabled: true},
	})

	action := m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	toggled, ok := action.(ActionToggleSkill)
	require.True(t, ok)
	require.Equal(t, "charmtone", toggled.Name)
	require.True(t, toggled.Disabled, "toggling an active skill disables it")
	require.True(t, m.Items()[0].Disabled)

	require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown}))
	action = m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	toggled, ok = action.(ActionToggleSkill)
	require.True(t, ok)
	require.Equal(t, "pair", toggled.Name)
	require.False(t, toggled.Disabled, "enter should re-enable a repo-disabled skill")
	require.False(t, m.Items()[1].Disabled)
}

func TestSkillsToggles_ConfigDisabledCanBeEnabled(t *testing.T) {
	t.Parallel()

	m := newSkillsTogglesForTest([]SkillToggleItem{
		{Name: "frozen", ConfigDisabled: true},
	})

	_, ok := m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}).(ActionToggleSkill)
	require.True(t, ok)
	item := m.Items()[0]
	require.False(t, item.localDisabled(), "enabling a config-disabled skill must flip the override")
	require.True(t, item.EnabledOverride)
	require.False(t, m.itemDisabled(item))
}

func TestSkillsToggles_GlobalScopeUsesConfigFlag(t *testing.T) {
	t.Parallel()

	m := newSkillsTogglesForTest([]SkillToggleItem{
		// 配置-disabled 但 locally re-enabled: Global must still show
		// 和 toggle raw 配置 flag.
		{Name: "pair", ConfigDisabled: true, EnabledOverride: true},
	})
	require.Equal(t, MCPToggleScopeLocal, m.Scope())
	require.False(t, m.itemDisabled(m.Items()[0]))

	require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab}))
	require.Equal(t, MCPToggleScopeGlobal, m.Scope())
	require.True(t, m.itemDisabled(m.Items()[0]))

	action := m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	toggled, ok := action.(ActionToggleSkill)
	require.True(t, ok)
	require.False(t, toggled.Disabled)
	require.True(t, toggled.Global)
	require.False(t, m.Items()[0].ConfigDisabled)
}

func TestSkillsToggles_ScrollsToCursor(t *testing.T) {
	t.Parallel()

	items := make([]SkillToggleItem, 30)
	for i := range items {
		items[i] = SkillToggleItem{Name: fmt.Sprintf("skill-%02d", i)}
	}
	m := newSkillsTogglesForTest(items)

	// 窗口 shows 在 most maxVisibleToggleRows items.
	rendered := lipgloss.Height(m.innerContent())
	require.LessOrEqual(t, rendered, maxVisibleToggleRows+2 /* blank padding lines */)

	// Moving past bottom scrolls 窗口 用 cursor.
	for range 25 {
		m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	require.Equal(t, 25, m.cursor)
	require.Equal(t, 25-maxVisibleToggleRows+1, m.visibleOffset(maxVisibleToggleRows))
	require.LessOrEqual(t, m.cursor-m.visibleOffset(maxVisibleToggleRows), maxVisibleToggleRows-1)

	// Scrolling back up must move selected 行 visually up inside
	// stationary 窗口 until it reaches 窗口 top; 只 then does
	// 窗口 scroll, one 行 per press.
	m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Equal(t, 24, m.cursor)
	require.Equal(t, 14, m.visibleOffset(maxVisibleToggleRows), "one up-press inside the window must not scroll")
	require.Equal(t, 24-m.visibleOffset(maxVisibleToggleRows), 10, "the selected row moved up within the window")

	// Walk cursor 到 窗口's top 行.
	for m.cursor > m.visibleOffset(maxVisibleToggleRows) {
		m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	require.Equal(t, 14, m.cursor)
	require.Equal(t, 14, m.visibleOffset(maxVisibleToggleRows))

	// next up-press scrolls 窗口 和 cursor together.
	m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Equal(t, 13, m.cursor)
	require.Equal(t, 13, m.visibleOffset(maxVisibleToggleRows))

	// Back 到 top scrolls 窗口 back.
	for range 30 {
		m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	require.Equal(t, 0, m.cursor)
	require.Equal(t, 0, m.visibleOffset(maxVisibleToggleRows))
}

func TestSkillsToggles_ErroredSkill(t *testing.T) {
	t.Parallel()

	m := newSkillsTogglesForTest([]SkillToggleItem{
		{Name: "charmtone"},
		{Name: "broken", Error: "yaml: cannot unmarshal"},
	})

	// An errored skill 是 不 disabled 和 不 enabled: it shows red
	// x 标记 regardless 的 toggle state.
	require.False(t, m.itemDisabled(m.Items()[1]))

	// Toggle still works 在 it (disable it 所以 it stops loading).
	action := m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	toggled, ok := action.(ActionToggleSkill)
	require.True(t, ok)
	require.Equal(t, "charmtone", toggled.Name)

	// Move 到 errored skill 和 disable it.
	require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown}))
	action = m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	toggled, ok = action.(ActionToggleSkill)
	require.True(t, ok)
	require.Equal(t, "broken", toggled.Name)
	require.True(t, toggled.Disabled)
	require.True(t, m.Items()[1].Disabled)
}
