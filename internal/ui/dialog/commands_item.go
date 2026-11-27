// commands_item.go —— 命令列表的单项渲染。

package dialog

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
)

// CommandItem wraps uicmd.Command 到 implement ListItem interface.
type CommandItem struct {
	*list.Versioned
	id          string
	title       string
	shortcut    string
	description string
	action      Action
	aliases     []string
	t           *styles.Styles
	m           fuzzy.Match
	cache       map[int]string
	focused     bool
	hideInfo    bool
}

var _ ListItem = &CommandItem{Versioned: list.NewVersioned()}

// NewCommandItem creates a new CommandItem.
func NewCommandItem(t *styles.Styles, id, title, shortcut string, action Action) *CommandItem {
	return &CommandItem{
		Versioned: list.NewVersioned(),
		id:        id,
		t:         t,
		title:     title,
		shortcut:  shortcut,
		action:    action,
	}
}

// WithAliases returns CommandItem 用 given aliases 为 filtering.
func (c *CommandItem) WithAliases(aliases ...string) *CommandItem {
	c.aliases = aliases
	return c
}

// WithDescription returns CommandItem 用 description displayed below
// 标题.
func (c *CommandItem) WithDescription(desc string) *CommandItem {
	c.description = desc
	return c
}

// Filter implements ListItem.
func (c *CommandItem) Filter() string {
	base := c.title
	if len(c.aliases) > 0 {
		base = c.title + " " + strings.Join(c.aliases, " ")
	}
	if c.description != "" {
		base = base + " " + c.description
	}
	return base
}

// ID implements ListItem.
func (c *CommandItem) ID() string {
	return c.id
}

// SetFocused implements ListItem.
func (c *CommandItem) SetFocused(focused bool) {
	if c.focused == focused {
		return
	}
	c.cache = nil
	c.focused = focused
	if c.Versioned != nil {
		c.Bump()
	}
}

// SetMatch implements ListItem.
func (c *CommandItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(c.m, m) {
		return
	}
	c.cache = nil
	c.m = m
	if c.Versioned != nil {
		c.Bump()
	}
}

// Action returns action associated 用 command item.
func (c *CommandItem) Action() Action {
	return c.action
}

// Shortcut returns shortcut associated 用 command item.
func (c *CommandItem) Shortcut() string {
	return c.shortcut
}

// InfoText implements infoColumnItem; command shortcut 是 它的 info.
func (c *CommandItem) InfoText() string {
	return c.shortcut
}

// SetHideInfo controls whether shortcut hint column 是 shown.
// dialog hides it uniformly 当 it would crowd command names.
func (c *CommandItem) SetHideInfo(v bool) {
	if c.hideInfo == v {
		return
	}
	c.cache = nil
	c.hideInfo = v
	if c.Versioned != nil {
		c.Bump()
	}
}

// Render implements ListItem.
func (c *CommandItem) Render(width int) string {
	styles := ListItemStyles{
		ItemBlurred:     c.t.Dialog.NormalItem,
		ItemFocused:     c.t.Dialog.SelectedItem,
		InfoTextBlurred: c.t.Dialog.ListItem.InfoBlurred,
		InfoTextFocused: c.t.Dialog.ListItem.InfoFocused,
	}
	shortcut := c.shortcut
	if c.hideInfo {
		shortcut = ""
	}
	rendered := renderItem(styles, c.title, shortcut, c.focused, width, c.cache, &c.m)
	if c.description != "" {
		descStyle := c.t.Dialog.SecondaryText
		if c.focused {
			descStyle = c.t.Dialog.SelectedItem
		}
		contentWidth := max(0, width-descStyle.GetHorizontalFrameSize()+1)
		description := ansi.Truncate(strings.TrimSpace(c.description), contentWidth, "...")
		descVisWidth := lipgloss.Width(description)
		gap := strings.Repeat(" ", max(0, contentWidth-descVisWidth))
		if description == "" {
			description = " "
		}
		rendered = lipgloss.JoinVertical(lipgloss.Left, rendered, descStyle.Render(description+gap))
	}
	return rendered
}
