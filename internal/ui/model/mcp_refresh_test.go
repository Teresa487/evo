package model

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// TestMCPEventRefreshIsOffThreadAndDeduped pins MCP side 的
// no-IO-在-更新 invariant: MCPGetStates 是 synchronous HTTP round-trip
// 在 客户端/服务端 mode, 所以 state_changed 事件 must schedule one
// off-thread fetch, dedup while one 是 在 flight, 和 re-派发 queued
// refresh 当 在-flight fetch lands.
func TestMCPEventRefreshIsOffThreadAndDeduped(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{
		ready: true,
		mcpStates: map[string]mcp.ClientInfo{
			"ctx": {Name: "ctx", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 4, Prompts: 2}},
		},
	}
	m := newBusyUI(ws)
	warmCaches(m, false)
	ws.resetCounters()

	_, cmd := m.Update(pubsub.Event[mcp.Event]{
		Type:    pubsub.UpdatedEvent,
		Payload: mcp.Event{Type: mcp.EventStateChanged, Name: "ctx", State: mcp.StateConnected},
	})
	require.Zero(t, ws.syncProbes(), "the MCP event handler must not probe synchronously")
	require.True(t, m.mcpFetchInFlight, "an MCP state change must schedule an off-thread refresh")

	// 一个 second 事件 while fetch 是 在 flight queues re-fetch instead
	// 的 stacking another 派发.
	m.Update(pubsub.Event[mcp.Event]{
		Type:    pubsub.UpdatedEvent,
		Payload: mcp.Event{Type: mcp.EventStateChanged, Name: "ctx", State: mcp.StateConnected},
	})
	require.Zero(t, ws.syncProbes())
	require.True(t, m.mcpRefreshQueued, "an event during an in-flight fetch must queue a re-fetch")

	runCmds(m, cmd)
	require.False(t, m.mcpFetchInFlight)
	require.False(t, m.mcpRefreshQueued, "the queued flag must clear once the re-dispatched fetch lands")
	require.Equal(t, 4, m.mcpStates["ctx"].Counts.Tools, "fetched states must land in the cache")
	require.Equal(t, 2, m.mcpStates["ctx"].Counts.Prompts, "fetched prompt counts must land in the cache")
	require.Equal(t, 2, ws.mcpStateCalls, "one fetch plus the queued re-fetch")
}

// TestMCPStartingRetryLoop pins convergence fix 为 stuck
// "starting..." case: 服务端 still connecting arms re-probe tick, 所以
// sidebar settles without depending 在 state_changed events (which can 是
// missed 在 客户端/服务端 mode 当 SSE 流 attaches late, drops 和
// reconnects, 或 总线 drops 事件). 终态 states disarm
// loop.
func TestMCPStartingRetryLoop(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{
		ready: true,
		mcpStates: map[string]mcp.ClientInfo{
			"slow": {Name: "slow", State: mcp.StateStarting},
		},
	}
	m := newBusyUI(ws)
	warmCaches(m, false)

	cmds := m.applyMCPStates(mcpStateChangedMsg{states: map[string]mcp.ClientInfo{
		"slow": {Name: "slow", State: mcp.StateStarting},
	}})
	require.Len(t, cmds, 1, "a starting server must arm the retry tick")
	require.Equal(t, mcp.StateStarting, m.mcpStates["slow"].State)

	// 服务端 settles before tick fires; re-probe lands
	// connected state 和 loop stops.
	ws.mcpStates = map[string]mcp.ClientInfo{
		"slow": {Name: "slow", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 2}},
	}
	_, cmd := m.Update(mcpStartingRetryMsg{})
	require.True(t, m.mcpFetchInFlight, "the retry tick must re-dispatch a state refresh")
	runCmds(m, cmd)
	require.False(t, m.mcpFetchInFlight)
	require.Equal(t, mcp.StateConnected, m.mcpStates["slow"].State,
		"the retry loop must converge on the settled state")

	cmds = m.applyMCPStates(mcpStateChangedMsg{states: map[string]mcp.ClientInfo{
		"slow": {Name: "slow", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 2}},
	}})
	require.Empty(t, cmds, "terminal states must not arm the retry tick")
}

// TestMCPStartingRetryBackoff pins bound 在 重试 loop: consecutive
// starting observations back re-probe cadence off exponentially 从
// mcpStartingRetryDelay up 到 mcpStartingRetryMaxDelay, 所以 服务端 那
// never settles cannot drive HTTP round-trip per second 为 whole
// 会话. Settled states reset budget.
func TestMCPStartingRetryBackoff(t *testing.T) {
	require.Equal(t, mcpStartingRetryDelay, mcpStartingRetryBackoff(0))
	require.Equal(t, 2*mcpStartingRetryDelay, mcpStartingRetryBackoff(1))
	require.Equal(t, 4*mcpStartingRetryDelay, mcpStartingRetryBackoff(2))
	require.Equal(t, mcpStartingRetryMaxDelay, mcpStartingRetryBackoff(64),
		"the backoff must clamp at the max delay")

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)

	starting := map[string]mcp.ClientInfo{"slow": {Name: "slow", State: mcp.StateStarting}}
	for attempt := 1; attempt <= 3; attempt++ {
		cmds := m.applyMCPStates(mcpStateChangedMsg{states: starting})
		require.Len(t, cmds, 1, "a starting server must keep the retry tick armed")
		require.Equal(t, attempt, m.mcpStartingRetries,
			"each consecutive starting observation must advance the backoff")
	}

	m.applyMCPStates(mcpStateChangedMsg{states: map[string]mcp.ClientInfo{
		"slow": {Name: "slow", State: mcp.StateConnected},
	}})
	require.Zero(t, m.mcpStartingRetries, "settled states must reset the backoff budget")
}

// TestMCPStatesTTLBackstop pins backstop 在 更新 tail: 当
// memoized MCP states outlive their TTL — safety valve 为 state_changed
// events missed 在 客户端/服务端 mode — refresh 是 re-dispatched.
func TestMCPStatesTTLBackstop(t *testing.T) {
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)

	// check time starts zero, 所以 first 更新 sees MCP 缓存 作为
	// stale 和 dispatches refresh.
	_, cmd := m.Update(plainMsg{})
	require.True(t, m.mcpFetchInFlight, "a stale MCP cache must re-dispatch a refresh")
	runCmds(m, cmd)
	require.False(t, m.mcpFetchInFlight)

	// 一个 fresh stamp keeps backstop quiet.
	m.Update(plainMsg{})
	require.False(t, m.mcpFetchInFlight, "a fresh MCP cache must not re-dispatch")

	// Once TTL elapses, next 更新 re-dispatches.
	m.mcpCheckedAt = time.Now().Add(-2 * mcpStatesTTL)
	m.Update(plainMsg{})
	require.True(t, m.mcpFetchInFlight, "an expired MCP cache must re-dispatch a refresh")
}

// TestMCPFailedFetchKeepsLastKnownGood pins 错误 路径: fetch 那
// fails (nil states, e.g. transient HTTP 错误 在 客户端/服务端 mode)
// keeps last-known-good states 在 screen instead 的 blanking
// sidebar.
func TestMCPFailedFetchKeepsLastKnownGood(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{
		ready: true,
		mcpStates: map[string]mcp.ClientInfo{
			"ctx": {Name: "ctx", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 3}},
		},
	}
	m := newBusyUI(ws)
	warmCaches(m, false)

	runCmds(m, m.requestMCPRefresh())
	require.Equal(t, mcp.StateConnected, m.mcpStates["ctx"].State,
		"the seeded states must land in the cache")

	// Simulate next fetch failing ( 客户端 工作区 returns nil
	// states 当 round-trip errors).
	cmds := m.applyMCPStates(mcpStateChangedMsg{states: nil})
	require.Empty(t, cmds)
	require.False(t, m.mcpFetchInFlight)
	require.Equal(t, mcp.StateConnected, m.mcpStates["ctx"].State,
		"a failed fetch must keep the last-known-good states")
}

// TestConnectionRecoveredResyncsMCPStates pins the reconnect recovery: the
// subscription loop documents 那 events published while SSE 流 was
// down 是 gone, 所以 recovery must re-sync memoized MCP states.
func TestConnectionRecoveredResyncsMCPStates(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	ws.resetCounters()

	m.Update(workspace.ConnectionEvent{State: workspace.ConnectionRecovered})
	require.Zero(t, ws.syncProbes(), "the recovery handler must not probe synchronously")
	require.True(t, m.mcpFetchInFlight,
		"recovery must re-sync the MCP states: events published while the stream was down are gone")
}

// TestMCPAuthCompleteRefreshesStates pins OAuth completion 路径: 当
// dialog reports flow finished, memoized states must refresh 所以
// sidebar reflects outcome even 当 state_changed 事件 was
// missed.
func TestMCPAuthCompleteRefreshesStates(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	ws.resetCounters()

	for _, action := range []tea.Msg{
		dialog.ActionMCPAuthComplete{Name: "ctx"},
		dialog.ActionMCPAuthErrored{Name: "ctx", Error: errors.New("authentication timed out")},
	} {
		m.mcpFetchInFlight = false
		m.mcpCheckedAt = time.Now()
		m.Update(action)
		require.Zero(t, ws.mcpStateCalls, "the auth action handler must not probe synchronously")
		require.True(t, m.mcpFetchInFlight,
			"auth completion must refresh the MCP states (%T)", action)
	}
}

// TestMCPNeedsAuthDialogOpensOnTransitionOnly pins the dialog gating: the
// auth dialog opens 当 服务端 newly enters StateNeedsAuth (including
// first observation), 但 backstop-driven refreshes 用 unchanged states
// must 不 re-open dialog 用户 already dismissed.
func TestMCPNeedsAuthDialogOpensOnTransitionOnly(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{
		ready:          true,
		mcpPendingAuth: []mcp.PendingAuthServer{{Name: "ctx", URL: "https://example.com/mcp"}},
	}
	m := newBusyUI(ws)
	warmCaches(m, false)

	m.applyMCPStates(mcpStateChangedMsg{states: map[string]mcp.ClientInfo{
		"ctx": {Name: "ctx", State: mcp.StateNeedsAuth},
	}})
	require.Equal(t, 1, ws.mcpPendingCalls, "a newly needs-auth server must prompt authentication")
	require.True(t, m.dialog.ContainsDialog(dialog.MCPAuthID))

	// Unchanged needs-auth states ( backstop refresh, 不 transition)
	// must 不 re-提示词.
	m.applyMCPStates(mcpStateChangedMsg{states: map[string]mcp.ClientInfo{
		"ctx": {Name: "ctx", State: mcp.StateNeedsAuth},
	}})
	require.Equal(t, 1, ws.mcpPendingCalls, "unchanged needs-auth states must not re-prompt")
}
