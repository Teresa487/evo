package model

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// mcpStatesTTL bounds how long the memoized MCP state may go without a
// re-probe being scheduled; MCP events normally 刷新 it much sooner.
// backstop covers state_changed events missed 在 客户端/服务端 mode:
// SSE pipeline 是 events-只, 所以 transition 那 completes before
// 流 attaches, 或 while it 是 down, 是 otherwise invisible until
// next state change. 一个 few seconds 的 stale MCP status 是 invisible,
// matching LSP backstop's cadence; see lsp.go. Package var 所以 tests
// 可以 pin it.
var mcpStatesTTL = 5 * time.Second

// mcpStartingRetryDelay 是 initial cadence 的 重试 loop 那 keeps
// re-probing MCP states while any 服务端 是 still connecting, 所以
// "starting..." 条目 converges 到 服务端's settled state even 当
// state_changed 事件 never arrives. cadence backs off
// exponentially 到 mcpStartingRetryMaxDelay 所以 服务端 那 never settles
// cannot drive round-trip per second 为 whole 会话. Package vars
// 所以 tests can pin them.
var mcpStartingRetryDelay = 1 * time.Second

// mcpStartingRetryMaxDelay caps starting-重试 backoff. Once loop
// has backed off 这 far, TTL backstop (mcpStatesTTL) dominates
// probe cadence anyway.
var mcpStartingRetryMaxDelay = 30 * time.Second

// mcpStartingRetryBackoff returns 重试 delay 为 given
// zero-based attempt, doubling 从 mcpStartingRetryDelay 和 clamping 在
// mcpStartingRetryMaxDelay.
func mcpStartingRetryBackoff(attempt int) time.Duration {
	delay := mcpStartingRetryDelay
	for i := 0; i < attempt && delay < mcpStartingRetryMaxDelay; i++ {
		delay *= 2
	}
	return min(delay, mcpStartingRetryMaxDelay)
}

// mcpStartingRetryMsg re-dispatches MCP state refresh after 重试
// delay; applyMCPStates arms it while any 服务端 是 在 StateStarting.
type mcpStartingRetryMsg struct{}

// requestMCPRefresh schedules off-thread refresh 的 memoized MCP
// states. While fetch 是 already 在 flight it 只 marks state dirty;
// applyMCPStates re-dispatches 所以 freshest data still lands.
func (m *UI) requestMCPRefresh() tea.Cmd {
	if m.mcpFetchInFlight {
		m.mcpRefreshQueued = true
		return nil
	}
	return m.dispatchMCPRefresh()
}

// dispatchMCPRefresh returns command 那 fetches MCP states off
// 更新 goroutine ( synchronous HTTP round-trip 在 客户端/服务端 mode),
// delivering mcpStateChangedMsg. 它 returns nil while fetch 是 already
// 在 flight. closure captures 只 locals (never m) 所以 it 是 safe
// off-thread.
func (m *UI) dispatchMCPRefresh() tea.Cmd {
	if m.mcpFetchInFlight || m.com == nil || m.com.Workspace == nil {
		return nil
	}
	m.mcpFetchInFlight = true
	// Stamp check time 在 派发 too 所以 TTL backstop doesn't
	// keep re-requesting while 这 fetch 是 在 flight.
	m.mcpCheckedAt = time.Now()
	ws := m.com.Workspace
	return func() tea.Msg {
		return mcpStateChangedMsg{
			states: ws.MCPGetStates(),
		}
	}
}

// applyMCPStates stores off-thread MCP fetch 结果, re-dispatches 当
// refresh was requested while it was 在 flight, 和 keeps 重试 loop
// running while any 服务端 是 still connecting. 一个 failed fetch keeps
// last-known-good states instead 的 blanking sidebar. Runs 在
// 更新 goroutine.
func (m *UI) applyMCPStates(msg mcpStateChangedMsg) []tea.Cmd {
	m.mcpFetchInFlight = false
	m.mcpCheckedAt = time.Now()
	if msg.states == nil {
		// 一个 failed fetch (e.g. transient HTTP 错误 在 客户端/服务端
		// mode) keeps last-known-good states 在 screen.
		if m.mcpRefreshQueued {
			m.mcpRefreshQueued = false
			if cmd := m.dispatchMCPRefresh(); cmd != nil {
				return []tea.Cmd{cmd}
			}
		}
		return nil
	}

	// Auto-open MCP auth dialog 当 服务端 newly needs
	// 认证. Comparing against previous states 保持
	// backstop-driven refreshes 从 re-opening dialog 用户 already
	// dismissed while 服务端 stays 在 StateNeedsAuth.
	newlyNeedsAuth := false
	for name, info := range msg.states {
		if info.State != mcp.StateNeedsAuth {
			continue
		}
		if prev, ok := m.mcpStates[name]; !ok || prev.State != mcp.StateNeedsAuth {
			newlyNeedsAuth = true
			break
		}
	}
	m.mcpStates = msg.states

	var cmds []tea.Cmd
	if newlyNeedsAuth {
		if cmd := m.openMCPAuthDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if m.mcpRefreshQueued {
		m.mcpRefreshQueued = false
		if cmd := m.dispatchMCPRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// 一个 服务端 still connecting keeps refresh loop armed: next
	// re-probe lands within backoff delay even 如果 state_changed
	// 事件 是 never delivered (late SSE attach, reconnect gap, 或
	// lossy 总线 在 客户端/服务端 mode). delay doubles per
	// consecutive starting observation up 到 mcpStartingRetryMaxDelay 所以
	// 服务端 那 never settles stops driving per-second round-trips.
	if anyMCPStarting(msg.states) {
		cmds = append(cmds, tea.Tick(mcpStartingRetryBackoff(m.mcpStartingRetries), func(time.Time) tea.Msg {
			return mcpStartingRetryMsg{}
		}))
		m.mcpStartingRetries++
	} else {
		m.mcpStartingRetries = 0
	}
	return cmds
}

// anyMCPStarting reports whether any MCP 客户端 是 still connecting.
func anyMCPStarting(states map[string]mcp.ClientInfo) bool {
	for _, info := range states {
		if info.State == mcp.StateStarting {
			return true
		}
	}
	return false
}

// mcpInfo renders MCP status section showing 活跃 MCP clients 和 their
// 工具/提示词 counts.
func (m *UI) mcpInfo(width, maxItems int, isSection bool) string {
	var mcps []mcp.ClientInfo
	t := m.com.Styles

	for _, mcp := range m.com.Config().MCP.Sorted() {
		if state, ok := m.mcpStates[mcp.Name]; ok {
			mcps = append(mcps, state)
		}
	}

	title := t.Resource.Heading.Render("MCPs")
	if isSection {
		title = common.Section(t, title, width)
	}
	list := t.Resource.AdditionalText.Render("None")
	if len(mcps) > 0 {
		list = mcpList(t, mcps, width, maxItems)
	}

	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
}

// mcpCounts formats 工具, 提示词, 和 resource counts 为 display.
func mcpCounts(t *styles.Styles, counts mcp.Counts) string {
	var parts []string
	if counts.Tools > 0 {
		parts = append(parts, t.Resource.CapabilityCount.Render(fmt.Sprintf("%d tools", counts.Tools)))
	}
	if counts.Prompts > 0 {
		parts = append(parts, t.Resource.CapabilityCount.Render(fmt.Sprintf("%d prompts", counts.Prompts)))
	}
	if counts.Resources > 0 {
		parts = append(parts, t.Resource.CapabilityCount.Render(fmt.Sprintf("%d resources", counts.Resources)))
	}
	return strings.Join(parts, " ")
}

// mcpList renders list 的 MCP clients 用 their status 和 counts,
// truncating 到 maxItems 如果 needed.
func mcpList(t *styles.Styles, mcps []mcp.ClientInfo, width, maxItems int) string {
	var renderedMcps []string

	for _, m := range mcps {
		var icon string
		title := m.Name
		// Show "Docker MCP" instead 的 配置 name 为 Docker MCP.
		if m.Name == config.DockerMCPName {
			title = "Docker MCP"
		}
		title = t.Resource.Name.Render(title)
		var description string
		var extraContent string

		switch m.State {
		case mcp.StateStarting:
			icon = t.Resource.BusyIcon.String()
			description = t.Resource.StatusText.Render("starting...")
		case mcp.StateConnected:
			icon = t.Resource.OnlineIcon.String()
			extraContent = mcpCounts(t, m.Counts)
			if m.Channel {
				badge := t.Resource.StatusText.Render("channel")
				if extraContent != "" {
					extraContent += " " + badge
				} else {
					extraContent = badge
				}
			}
		case mcp.StateError:
			icon = t.Resource.ErrorIcon.String()
			description = t.Resource.StatusText.Render("error")
			if m.Error != nil {
				description = t.Resource.StatusText.Render(fmt.Sprintf("error: %s", m.Error.Error()))
			}
		case mcp.StateNeedsAuth:
			icon = t.Resource.NeedsAuthIcon.String()
			description = t.Resource.StatusText.Render("needs authentication")
		case mcp.StateDisabled:
			icon = t.Resource.DisabledIcon.String()
			description = t.Resource.StatusText.Render("disabled")
		default:
			icon = t.Resource.OfflineIcon.String()
		}

		renderedMcps = append(renderedMcps, common.Status(t, common.StatusOpts{
			Icon:         icon,
			Title:        title,
			Description:  description,
			ExtraContent: extraContent,
		}, width))
	}

	return lipgloss.JoinVertical(lipgloss.Left, truncateRows(renderedMcps, maxItems, t.Resource.AdditionalText)...)
}

// openMCPTogglesDialog opens the repository-scoped MCP toggles dialog.
func (m *UI) openMCPTogglesDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.MCPTogglesID) {
		m.dialog.BringToFront(dialog.MCPTogglesID)
		return nil
	}
	items, err := m.mcpToggleItems()
	if err != nil {
		return util.ReportError(err)
	}
	m.dialog.OpenDialog(dialog.NewMCPToggles(m.com, items))
	return nil
}

// mcpToggleItems builds dialog items 从 configured servers,
// live 连接 states, 和 repository's disabled overrides.
func (m *UI) mcpToggleItems() ([]dialog.MCPToggleItem, error) {
	disabledServers, err := m.com.Workspace.MCPServersDisabled(context.Background())
	if err != nil {
		return nil, err
	}
	disabled := make(map[string]struct{}, len(disabledServers))
	for _, name := range disabledServers {
		disabled[name] = struct{}{}
	}
	// 一个 repository-scoped enabled override turns 配置-disabled 服务端
	// back 在 为 这 repository; it 是 force-started 在 startup.
	enabledServers, err := m.com.Workspace.MCPServersEnabled(context.Background())
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]struct{}, len(enabledServers))
	for _, name := range enabledServers {
		enabled[name] = struct{}{}
	}
	items := make([]dialog.MCPToggleItem, 0, len(m.com.Config().MCP))
	for _, configured := range m.com.Config().MCP.Sorted() {
		_, enabledOverride := enabled[configured.Name]
		item := dialog.MCPToggleItem{
			Name:            configured.Name,
			ConfigDisabled:  configured.MCP.Disabled,
			EnabledOverride: enabledOverride,
			Status:          mcpStatusText(m.mcpStates[configured.Name]),
		}
		if _, off := disabled[configured.Name]; off {
			item.Disabled = true
		}
		items = append(items, item)
	}
	return items, nil
}

// applyMCPToggle persists MCP toggle 和 applies it 到 running
// 客户端. Local toggles 写入 repository-scoped override; global toggles
// 写入 disabled flag 到 配置. dialog keeps 它的 own
// optimistic state; failures surface 作为 错误 toast.
func (m *UI) applyMCPToggle(msg dialog.ActionToggleMCP) tea.Cmd {
	name := msg.Name
	disable := msg.Disabled
	status := "enabled"
	if disable {
		status = "disabled"
	}
	return func() tea.Msg {
		if msg.Global {
			if err := m.com.Workspace.MCPSetServerConfigDisabled(context.TODO(), name, disable); err != nil {
				return util.NewErrorMsg(err)
			}
			return util.NewInfoMsg(fmt.Sprintf("MCP %q %s globally", name, status))
		}
		if err := m.com.Workspace.MCPSetServerDisabled(context.TODO(), name, disable); err != nil {
			return util.NewErrorMsg(err)
		}
		return util.NewInfoMsg(fmt.Sprintf("MCP %q %s for this repository", name, status))
	}
}

// mcpStatusText renders 连接 state 作为 plain dialog 文本.
func mcpStatusText(info mcp.ClientInfo) string {
	switch info.State {
	case mcp.StateStarting:
		// No ellipsis: 文本 stays put 当 state transitions,
		// instead 的 jumping 作为 trailing dots appear 和 vanish.
		return "starting"
	case mcp.StateConnected:
		return "connected"
	case mcp.StateError:
		if info.Error != nil {
			return fmt.Sprintf("error: %s", info.Error.Error())
		}
		return "error"
	case mcp.StateNeedsAuth:
		return "needs authentication"
	case mcp.StateDisabled:
		return "disabled"
	default:
		return "offline"
	}
}
