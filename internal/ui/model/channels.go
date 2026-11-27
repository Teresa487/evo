// channels.go —— 渠道（MCP messaging server）事件的 UI 侧处理。
//
// 客户端/服务端模式下后端负责注入渠道事件，这里只展示。

package model

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// channelsInfo renders 渠道 status section 为 items, 作为 built 通过
// channelStatusItems. Callers build list once 和 pass it 在, since they
// also use it 到 size section 或 decide whether 到 show it 在 all.
func (m *UI) channelsInfo(items []channelStatusItem, width, maxItems int, isSection bool) string {
	t := m.com.Styles

	title := t.Resource.Heading.Render("Channels")
	if isSection {
		title = common.Section(t, title, width)
	}

	if len(items) == 0 {
		list := t.Resource.AdditionalText.Render("None")
		return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
	}

	list := channelList(t, items, width, maxItems)
	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
}

// channelStatusItem holds display data 为 single 渠道 条目.
type channelStatusItem struct {
	name        string
	icon        string
	title       string
	description string
}

// channelStatusItems collects MCP servers opted 在 作为 channels (via
// --channels 或 channel_enabled) 在 whatever state they 是 在, 所以 渠道
// 那 是 starting, has crashed, 或 needs auth stays listed rather than
// dropping out. Items come back 在 name order, 作为 MCP.Sorted returns them.
func (m *UI) channelStatusItems() []channelStatusItem {
	t := m.com.Styles
	var items []channelStatusItem

	for _, mcpCfg := range m.com.Config().MCP.Sorted() {
		state, ok := m.mcpStates[mcpCfg.Name]
		if !ok || (!state.ChannelOptIn && !state.Channel) {
			continue
		}

		var icon string
		var description string
		switch state.State {
		case mcp.StateStarting:
			icon = t.Resource.BusyIcon.String()
			description = t.Resource.StatusText.Render("starting...")
		case mcp.StateConnected:
			if state.Channel {
				icon = t.Resource.OnlineIcon.String()
				description = t.Resource.StatusText.Render("connected")
			} else {
				// Opted 在, 但 服务端 never declared claude/渠道,
				// 所以 它的 pushes 是 ignored.
				icon = t.Resource.ErrorIcon.String()
				description = t.Resource.StatusText.Render("no channel capability")
			}
		case mcp.StateError:
			icon = t.Resource.ErrorIcon.String()
			description = t.Resource.StatusText.Render("error")
			if state.Error != nil {
				description = t.Resource.StatusText.Render(fmt.Sprintf("error: %s", state.Error.Error()))
			}
		case mcp.StateNeedsAuth:
			icon = t.Resource.NeedsAuthIcon.String()
			description = t.Resource.StatusText.Render("needs authentication")
		default:
			icon = t.Resource.OfflineIcon.String()
			description = t.Resource.StatusText.Render("offline")
		}

		items = append(items, channelStatusItem{
			name:        mcpCfg.Name,
			icon:        icon,
			title:       t.Resource.Name.Render(mcpCfg.Name),
			description: description,
		})
	}

	return items
}

func channelList(t *styles.Styles, items []channelStatusItem, width, maxItems int) string {
	if maxItems <= 0 {
		return ""
	}

	if len(items) > maxItems {
		visibleItems := items[:maxItems-1]
		remaining := len(items) - (maxItems - 1)
		items = append(visibleItems, channelStatusItem{
			title: t.Resource.AdditionalText.Render(fmt.Sprintf("…and %d more", remaining)),
		})
	}

	renderedItems := make([]string, 0, len(items))
	for _, item := range items {
		renderedItems = append(renderedItems, common.Status(t, common.StatusOpts{
			Icon:        item.icon,
			Title:       item.title,
			Description: item.description,
		}, width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, renderedItems...)
}
