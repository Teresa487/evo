package model

import (
	"errors"
	"image"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
)

type channelsTestWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *channelsTestWorkspace) Config() *config.Config { return w.cfg }

func (w *channelsTestWorkspace) MCPPendingAuth() []mcp.PendingAuthServer { return nil }

// newChannelsTestUI builds UI whose 配置 lists given MCP names 和
// whose mcpStates map holds given per-服务端 ClientInfo.
func newChannelsTestUI(t *testing.T, mcpNames []string, states map[string]mcp.ClientInfo) *UI {
	t.Helper()
	mcps := config.MCPs{}
	for _, n := range mcpNames {
		mcps[n] = config.MCPConfig{}
	}
	com := &common.Common{
		Workspace: &channelsTestWorkspace{cfg: &config.Config{MCP: mcps}},
		Styles:    common.DefaultCommon(nil).Styles,
	}
	return &UI{com: com, mcpStates: states, chat: NewChat(com, config.ScrollbarDefault)}
}

// TestChannelStatusItems_FiltersSortsAndMapsState covers the core logic:
// servers opted 在 作为 channels 是 listed whatever their 连接 state,
// 在 name order, 和 each state maps 到 expected status 文本 (including
// 错误 消息). Servers 那 是 merely 渠道-capable, 或 plain MCP
// servers, 是 不 listed.
func TestChannelStatusItems_FiltersSortsAndMapsState(t *testing.T) {
	t.Parallel()

	states := map[string]mcp.ClientInfo{
		"zeta-chan": {Name: "zeta-chan", State: mcp.StateConnected, Channel: true, ChannelOptIn: true},
		// Opted 在 但 crashed: 渠道 是 false, yet it must stay listed.
		"alpha-chan": {Name: "alpha-chan", State: mcp.StateError, ChannelOptIn: true, Error: errors.New("boom")},
		// Opted 在 和 connected, 但 服务端 never declared claude/渠道.
		"mid-chan":  {Name: "mid-chan", State: mcp.StateConnected, ChannelOptIn: true},
		"capable":   {Name: "capable", State: mcp.StateConnected, ChannelCapable: true}, // not opted in
		"plain-mcp": {Name: "plain-mcp", State: mcp.StateConnected},                     // not a channel
		// "orphan" has MCP 配置 但 no 条目 在 mcpStates → excluded.
	}
	m := newChannelsTestUI(t, []string{"zeta-chan", "alpha-chan", "mid-chan", "capable", "plain-mcp", "orphan"}, states)

	items := m.channelStatusItems()

	require.Len(t, items, 3, "only opted-in channels with a known state are listed")
	require.Equal(t, "alpha-chan", items[0].name)
	require.Equal(t, "mid-chan", items[1].name)
	require.Equal(t, "zeta-chan", items[2].name)

	require.Contains(t, ansi.Strip(items[0].description), "error: boom")
	require.Contains(t, ansi.Strip(items[1].description), "no channel capability")
	require.Contains(t, ansi.Strip(items[2].description), "connected")
}

// TestChannelStatusItems_StateVariants exercises remaining state → 文本
// mappings 为 opted-在 channels (starting, needs auth, bare 错误, 和
// unknown state → 离线).
func TestChannelStatusItems_StateVariants(t *testing.T) {
	t.Parallel()

	states := map[string]mcp.ClientInfo{
		"starting":  {Name: "starting", State: mcp.StateStarting, ChannelOptIn: true},
		"auth":      {Name: "auth", State: mcp.StateNeedsAuth, ChannelOptIn: true},
		"errorless": {Name: "errorless", State: mcp.StateError, ChannelOptIn: true}, // error state, nil Error
		"unknown":   {Name: "unknown", State: mcp.State(99), ChannelOptIn: true},    // out-of-range → offline
	}
	m := newChannelsTestUI(t, []string{"starting", "auth", "errorless", "unknown"}, states)

	got := map[string]string{}
	for _, it := range m.channelStatusItems() {
		got[it.name] = ansi.Strip(it.description)
	}
	require.Contains(t, got["starting"], "starting")
	require.Contains(t, got["auth"], "needs authentication")
	require.Equal(t, "error", got["errorless"], "error state with no Error shows bare 'error'")
	require.Contains(t, got["unknown"], "offline", "unknown state falls back to offline")
}

// TestChannelsInfo_EmptyShowsNone verifies empty list 渲染 section
// 标题 plus "None" rather than blank section.
func TestChannelsInfo_EmptyShowsNone(t *testing.T) {
	t.Parallel()

	m := newChannelsTestUI(t, nil, nil)

	out := ansi.Strip(m.channelsInfo(nil, 40, 10, false))
	require.Contains(t, out, "Channels")
	require.Contains(t, out, "None")
}

// TestChannelList_Truncation covers "…和 N more" overflow behavior 和
// maxItems<=0 guard.
func TestChannelList_Truncation(t *testing.T) {
	t.Parallel()

	styles := common.DefaultCommon(nil).Styles
	items := []channelStatusItem{
		{name: "a", title: "a"},
		{name: "b", title: "b"},
		{name: "c", title: "c"},
	}

	// maxItems=2 用 3 items → one item shown plus "…和 2 more" line.
	out := ansi.Strip(channelList(styles, items, 80, 2))
	require.Contains(t, out, "and 2 more")

	// Non-positive budget 渲染 nothing.
	require.Empty(t, channelList(styles, items, 80, 0))
}

// TestMCPStateChangeRefreshesOpenChannelsDialog drives a real state-change
// 消息 through 更新 和 checks open Channels dialog picks it up,
// covering the wiring rather than just the dialog's own SetStates.
func TestMCPStateChangeRefreshesOpenChannelsDialog(t *testing.T) {
	t.Parallel()

	m := newChannelsTestUI(t, []string{"signal"}, nil)
	m.dialog = dialog.NewOverlay()
	m.mcpStates = map[string]mcp.ClientInfo{
		"signal": {Name: "signal", State: mcp.StateStarting, ChannelOptIn: true},
	}
	d := dialog.NewChannels(m.com, m.mcpStates)
	m.dialog.OpenDialog(d)

	m.Update(mcpStateChangedMsg{states: map[string]mcp.ClientInfo{
		"signal":  {Name: "signal", State: mcp.StateConnected, Channel: true, ChannelOptIn: true},
		"webhook": {Name: "webhook", State: mcp.StateConnected, ChannelCapable: true},
	}})

	scr := uv.NewScreenBuffer(100, 30)
	d.Draw(scr, image.Rect(0, 0, 100, 30))
	out := ansi.Strip(scr.String())
	require.Contains(t, out, "webhook", "a server that appeared after opening should be listed")
	require.Contains(t, out, "connected", "signal's refreshed state should be shown")
}
