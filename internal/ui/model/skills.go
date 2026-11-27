// skills.go —— UI 的技能列表与查看。
//
// 前端对后端：通过 Workspace.ListSkills 获取技能。

package model

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
)

type skillStatusItem struct {
	icon  string
	name  string
	title string
	// description 是 reserved 为 future use (e.g. showing 错误 details).
	description string
}

var builtinSkillsCache struct {
	once   sync.Once
	skills []*skills.Skill
}

func cachedBuiltinSkills() []*skills.Skill {
	builtinSkillsCache.once.Do(func() {
		builtinSkillsCache.skills = skills.DiscoverBuiltin()
	})
	return builtinSkillsCache.skills
}

// skillsInfo renders skill discovery status section showing loaded 和
// invalid skills.
func (m *UI) skillsInfo(width, maxItems int, isSection bool) string {
	t := m.com.Styles

	title := t.Resource.Heading.Render("Skills")
	if isSection {
		title = common.Section(t, title, width)
	}

	items := m.skillStatusItems()
	if len(items) == 0 {
		list := t.Resource.AdditionalText.Render("None")
		return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
	}

	list := skillsList(t, items, width, maxItems)
	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
}

func (m *UI) skillStatusItems() []skillStatusItem {
	t := m.com.Styles
	var items []skillStatusItem
	stateNames := make(map[string]struct{}, len(m.skillStates))

	disabledSet := make(map[string]bool)
	if m.com != nil && m.com.Workspace != nil {
		if cfg := m.com.Config(); cfg != nil {
			for _, name := range cfg.Options.DisabledSkills {
				disabledSet[name] = true
			}
		}
		// Repository overrides mirror the toggle dialog: repo-disabled
		// skills hide, 和 配置-disabled skills re-enabled 为 这
		// repository show again.
		for name, disabled := range m.skillsDisabledOverrides {
			if disabled {
				disabledSet[name] = true
			}
		}
		for name := range m.skillsEnabledOverrides {
			delete(disabledSet, name)
		}
	}

	states := slices.Clone(m.skillStates)
	slices.SortStableFunc(states, func(a, b *skills.SkillState) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, state := range states {
		name := state.Name
		if name == "" {
			name = filepath.Base(filepath.Dir(state.Path))
		}
		if disabledSet[name] {
			continue
		}
		if _, exists := stateNames[name]; exists {
			continue
		}
		stateNames[name] = struct{}{}
		icon := t.Resource.OnlineIcon.String()
		if state.State == skills.StateError {
			icon = t.Resource.ErrorIcon.String()
		}
		items = append(items, skillStatusItem{
			icon:  icon,
			name:  name,
			title: t.Resource.Name.Render(name),
		})
	}

	builtin := cachedBuiltinSkills()
	slices.SortStableFunc(builtin, func(a, b *skills.Skill) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, skill := range builtin {
		if _, ok := stateNames[skill.Name]; ok {
			continue
		}
		if disabledSet[skill.Name] {
			continue
		}
		items = append(items, skillStatusItem{
			icon:  t.Resource.OnlineIcon.String(),
			name:  skill.Name,
			title: t.Resource.Name.Render(skill.Name),
		})
	}

	slices.SortStableFunc(items, func(a, b skillStatusItem) int {
		return strings.Compare(a.name, b.name)
	})

	return items
}

func skillsList(t *styles.Styles, items []skillStatusItem, width, maxItems int) string {
	renderedItems := make([]string, 0, len(items))
	for _, item := range items {
		renderedItems = append(renderedItems, common.Status(t, common.StatusOpts{
			Icon:        item.icon,
			Title:       item.title,
			Description: item.description,
		}, width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, truncateRows(renderedItems, maxItems, t.Resource.AdditionalText)...)
}

// skillsTogglesList builds toggle-skills dialog items 从
// discovered skills, 配置's disabled list, 和 repository
// overrides mirrored 在 UI.
func (m *UI) skillsTogglesList() []dialog.SkillToggleItem {
	cfg := m.com.Config()
	items := make([]dialog.SkillToggleItem, 0)

	seen := make(map[string]struct{})
	// errors carries discovery 错误 per skill name 所以 errored rows
	// can show red x 和 错误 label 在 toggles dialog.
	errors := make(map[string]string)
	add := func(name string) {
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		_, configDisabled := disabledSet(cfg)[name]
		_, enabledOverride := m.skillsEnabledOverrides[name]
		_, disabledOverride := m.skillsDisabledOverrides[name]
		items = append(items, dialog.SkillToggleItem{
			Name:            name,
			ConfigDisabled:  configDisabled,
			EnabledOverride: enabledOverride,
			Disabled:        disabledOverride,
			Error:           errors[name],
		})
	}

	for _, state := range m.skillStates {
		name := state.Name
		if name == "" {
			name = filepath.Base(filepath.Dir(state.Path))
		}
		if state.State == skills.StateError && state.Err != nil {
			errors[name] = state.Err.Error()
		}
		add(name)
	}
	for _, skill := range cachedBuiltinSkills() {
		add(skill.Name)
	}
	for name := range disabledSet(cfg) {
		add(name)
	}
	// Override names must always appear, even 当 underlying skill
	// 是 gone 从 配置 或 disk, 所以 it can still 是 re-enabled.
	for name := range m.skillsDisabledOverrides {
		add(name)
	}
	for name := range m.skillsEnabledOverrides {
		add(name)
	}

	slices.SortFunc(items, func(a, b dialog.SkillToggleItem) int {
		return strings.Compare(a.Name, b.Name)
	})
	return items
}

// disabledSet returns 配置's disabled-skills set.
func disabledSet(cfg *config.Config) map[string]struct{} {
	set := make(map[string]struct{})
	if cfg == nil {
		return set
	}
	for _, name := range cfg.Options.DisabledSkills {
		set[name] = struct{}{}
	}
	return set
}

// refreshSkillOverrides fetches the repository-scoped skill toggle sets
// 和 emits them 作为 消息 所以 sidebar 和 dialog 更新 without
// blocking 在 工作区 calls during render. Fetch failures ( older
// 服务端 without endpoints, say) fail open: they 是 logged 和
// existing mirrors 是 kept instead 的 toasting 错误 在 every
// 会话 load.
func (m *UI) refreshSkillOverrides() tea.Cmd {
	return func() tea.Msg {
		disabled, err := m.com.Workspace.SkillsDisabled(context.Background())
		if err != nil {
			slog.Warn("Failed to list disabled skills; keeping current overrides", "error", err)
			return nil
		}
		enabled, err := m.com.Workspace.SkillsEnabled(context.Background())
		if err != nil {
			slog.Warn("Failed to list enabled skills; keeping current overrides", "error", err)
			return nil
		}
		disabledSet := make(map[string]bool, len(disabled))
		for _, name := range disabled {
			disabledSet[name] = true
		}
		enabledSet := make(map[string]bool, len(enabled))
		for _, name := range enabled {
			enabledSet[name] = true
		}
		return skillsOverridesLoadedMsg{disabled: disabledSet, enabled: enabledSet}
	}
}

// openSkillsTogglesDialog opens the repository-scoped skills toggles
// dialog. dialog starts 用 overrides UI has mirrored;
// fresh fetch refreshes both afterwards.
func (m *UI) openSkillsTogglesDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.SkillsTogglesID) {
		m.dialog.BringToFront(dialog.SkillsTogglesID)
		return m.refreshSkillOverrides()
	}
	m.dialog.OpenDialog(dialog.NewSkillsToggles(m.com, m.skillsTogglesList()))
	return m.refreshSkillOverrides()
}

// applySkillToggle persists skills toggle. Local toggles 写入
// repository-scoped override; global toggles edit 配置's
// options.disabled_skills list. Mirrors applyMCPToggle.
func (m *UI) applySkillToggle(msg dialog.ActionToggleSkill) tea.Cmd {
	name := msg.Name
	disable := msg.Disabled
	status := "enabled"
	if disable {
		status = "disabled"
	}
	return func() tea.Msg {
		if msg.Global {
			if err := m.com.Workspace.SkillSetConfigDisabled(context.TODO(), name, disable); err != nil {
				return util.NewErrorMsg(err)
			}
			return util.NewInfoMsg(fmt.Sprintf("Skill %q %s globally from the next message", name, status))
		}
		if err := m.com.Workspace.SkillSetDisabled(context.TODO(), name, disable); err != nil {
			return util.NewErrorMsg(err)
		}
		return util.NewInfoMsg(fmt.Sprintf("Skill %q %s for this repository from the next message", name, status))
	}
}
