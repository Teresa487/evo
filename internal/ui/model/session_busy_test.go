package model

import (
	"context"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/workspace"
)

// countingWorkspace 是 工作区.工作区 stub 那 counts every probe
// 那 是 synchronous HTTP round-trip 在 客户端/服务端 mode, split per
// method 所以 tests can pin exactly which probes ran. embedded interface
// panics 在 anything unimplemented.
type countingWorkspace struct {
	workspace.Workspace

	ready          bool
	agentBusy      bool
	permMode       permission.PermissionMode
	queued         []string
	model          workspace.AgentModel
	lspStates      map[string]workspace.LSPClientInfo
	lspDiags       map[string]lsp.DiagnosticCounts
	mcpStates      map[string]mcp.ClientInfo
	mcpPendingAuth []mcp.PendingAuthServer

	readyCalls      int
	agentBusyCalls  int
	queuedCalls     int
	queueListCalls  int
	permCalls       int
	permSetCalls    int
	clearQueueCalls int
	cancelCalls     int
	modelCalls      int
	lspStateCalls   int
	lspDiagCalls    int
	mcpStateCalls   int
	mcpPendingCalls int
}

func (w *countingWorkspace) AgentIsReady() bool { w.readyCalls++; return w.ready }
func (w *countingWorkspace) AgentIsBusy() bool  { w.agentBusyCalls++; return w.agentBusy }

// AgentIsSessionBusy answers 为 single 会话 these tests use, 和 是
// deliberately uncounted: it 是 asked once 在 会话 load rather than 在
// the per-keystroke paths whose probe counts the other stubs pin.
func (w *countingWorkspace) AgentIsSessionBusy(string) bool { return w.agentBusy }

func (w *countingWorkspace) AgentReadyErr() error {
	w.readyCalls++
	if w.ready {
		return nil
	}
	return workspace.ErrAgentNotInitialized
}

func (w *countingWorkspace) AgentQueuedPrompts(string) int {
	w.queuedCalls++
	return len(w.queued)
}

func (w *countingWorkspace) AgentQueuedPromptsList(string) []string {
	w.queueListCalls++
	return w.queued
}

func (w *countingWorkspace) PermissionMode() permission.PermissionMode {
	w.permCalls++
	return w.permMode
}

func (w *countingWorkspace) PermissionSetMode(mode permission.PermissionMode) {
	w.permSetCalls++
	w.permMode = mode
}

func (w *countingWorkspace) AgentClearQueue(string) { w.clearQueueCalls++; w.queued = nil }
func (w *countingWorkspace) AgentCancel(string)     { w.cancelCalls++ }

func (w *countingWorkspace) AgentModel() workspace.AgentModel {
	w.modelCalls++
	return w.model
}

func (w *countingWorkspace) LSPGetStates() map[string]workspace.LSPClientInfo {
	w.lspStateCalls++
	return w.lspStates
}

func (w *countingWorkspace) LSPGetDiagnosticCounts(name string) lsp.DiagnosticCounts {
	w.lspDiagCalls++
	return w.lspDiags[name]
}

func (w *countingWorkspace) MCPGetStates() map[string]mcp.ClientInfo {
	w.mcpStateCalls++
	return w.mcpStates
}

func (w *countingWorkspace) ListMCPPrompts(context.Context) ([]commands.MCPPrompt, error) {
	return nil, nil
}

func (w *countingWorkspace) MCPPendingAuth() []mcp.PendingAuthServer {
	w.mcpPendingCalls++
	return w.mcpPendingAuth
}

func (w *countingWorkspace) MCPAuthURL(string) string { return "" }

func (w *countingWorkspace) MCPServersDisabled(context.Context) ([]string, error) {
	return nil, nil
}

func (w *countingWorkspace) MCPServersEnabled(context.Context) ([]string, error) {
	return nil, nil
}

func (w *countingWorkspace) ListMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (w *countingWorkspace) ListUserMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (w *countingWorkspace) WorkingDir() string { return "" }

func (w *countingWorkspace) LSPStart(context.Context, string) {}

func (w *countingWorkspace) Config() *config.Config { return nil }

// syncProbes sums every synchronous 计数器; 更新/View must keep 这 在
// zero — invariant 是 那 no 工作区 调用 ever happens 在 更新
// goroutine (which 是 also render loop).
func (w *countingWorkspace) syncProbes() int {
	return w.readyCalls + w.agentBusyCalls +
		w.queuedCalls + w.queueListCalls + w.permCalls +
		w.modelCalls + w.lspStateCalls + w.lspDiagCalls +
		w.mcpStateCalls
}

func (w *countingWorkspace) resetCounters() {
	w.readyCalls, w.agentBusyCalls = 0, 0
	w.queuedCalls, w.queueListCalls, w.permCalls = 0, 0, 0
	w.permSetCalls, w.clearQueueCalls, w.cancelCalls = 0, 0, 0
	w.modelCalls, w.lspStateCalls, w.lspDiagCalls = 0, 0, 0
	w.mcpStateCalls, w.mcpPendingCalls = 0, 0
}

// newBusyUI builds UI wired 到 stub 工作区 用 活跃 会话
// "s1", enough state 为 更新 到 运行 end 到 end.
func newBusyUI(ws *countingWorkspace) *UI {
	com := common.DefaultCommon(ws)
	return &UI{
		com:         com,
		status:      NewStatus(com, nil),
		chat:        NewChat(com, config.ScrollbarDefault),
		textarea:    textarea.New(),
		state:       uiChat,
		focus:       uiFocusEditor,
		width:       140,
		height:      45,
		session:     &session.Session{ID: "s1"},
		keyMap:      DefaultKeyMap(),
		dialog:      dialog.NewOverlay(),
		attachments: attachments.New(nil, attachments.Keymap{}),
	}
}

// pinTTLs makes TTL backstop inert 为 duration 的 测试 所以
// assertions about 事件-driven refreshes cannot flake 通过 straddling TTL
// boundary ( tests using it must 不 调用 t.Parallel).
func pinTTLs(t *testing.T) {
	t.Helper()
	oldBusy, oldQueue, oldLSP, oldMCP := busyCacheTTL, promptQueueTTL, lspStatesTTL, mcpStatesTTL
	busyCacheTTL = time.Hour
	promptQueueTTL = time.Hour
	lspStatesTTL = time.Hour
	mcpStatesTTL = time.Hour
	t.Cleanup(func() {
		busyCacheTTL, promptQueueTTL, lspStatesTTL, mcpStatesTTL = oldBusy, oldQueue, oldLSP, oldMCP
	})
}

// warmCaches marks all memoized 工作区 state fresh 所以 只 explicit
// invalidation (不 startup staleness) can trigger refresh dispatches.
func warmCaches(m *UI, busy bool) {
	m.agentBusyCache.set(busy)
	m.permModeCache.set(permission.PermissionModeNormal)
	m.agentReady = true
	m.promptQueueCheckedAt = time.Now()
	m.lspCheckedAt = time.Now()
	m.mcpCheckedAt = time.Now()
}

// runCmds executes a command tree the way the Bubble Tea runtime would,
// feeding 缓存-refresh messages back into 更新. Other leaf commands 是
// executed (为 their side effects 在 stub) 但 their messages dropped.
func runCmds(m *UI, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runCmds(m, c)
		}
	case busyStateMsg, promptQueueMsg, agentRunSubmittedMsg, lspStatesMsg, agentModelChangedMsg, mcpStateChangedMsg:
		_, next := m.Update(msg)
		runCmds(m, next)
	}
}

// plainMsg 是 arbitrary tea.Msg standing 在 为 keystroke/mouse/tick
// traffic through 更新.
type plainMsg struct{}

// TestUpdateDoesNotProbeWorkspacePerMessage pins hot-路径 fix: 更新
// used 到 调用 AgentQueuedPrompts ( synchronous HTTP GET 在 客户端/服务端
// mode) 在 top 的 every 消息 while agent was busy, 和
// placeholder 路径 probed AgentIsReady/AgentIsBusy/PermissionSkipRequests —
// every keystroke blocked single 更新 goroutine 在 network round-
// trips. Now 更新 performs no synchronous 工作区 调用 在 all; refreshes
// 是 dispatched 作为 commands.
func TestUpdateDoesNotProbeWorkspacePerMessage(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)

	for range 25 {
		m.Update(plainMsg{})
	}
	require.Zero(t, ws.queuedCalls,
		"Update must not call AgentQueuedPrompts per message (HTTP per keystroke in client mode)")
	require.Zero(t, ws.syncProbes(),
		"Update must not make any synchronous workspace call")
}

// TestReadsNeverProbeWorkspace pins 读取 side 的 invariant:
// busy/yolo getters used 通过 render paths serve memoized 值 和 never
// probe, 所以 View can never block 在 HTTP.
func TestReadsNeverProbeWorkspace(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true}
	m := newBusyUI(ws)

	for range 10 {
		m.isAgentBusy()
		m.permModeCached()
	}
	require.Zero(t, ws.syncProbes(), "cache reads must never probe the workspace")
}

// TestStreamingUpdatedEventsDoNotProbe pins streaming 路径: per-chunk
// 消息 UpdatedEvents arrive once per streamed token 和 must neither
// probe 工作区 synchronously nor schedule busy/队列 refreshes —
// 只 CreatedEvents (运行 boundaries) do.
func TestStreamingUpdatedEventsDoNotProbe(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, true)
	ws.resetCounters()

	for range 25 {
		m.Update(pubsub.Event[message.Message]{
			Type:    pubsub.UpdatedEvent,
			Payload: message.Message{ID: "m1", SessionID: "s1", Role: message.Assistant},
		})
	}
	require.Zero(t, ws.syncProbes(),
		"per-chunk UpdatedEvents must not probe the workspace")
	require.False(t, m.busyFetchInFlight,
		"per-chunk UpdatedEvents must not schedule a busy refresh")
	require.False(t, m.promptQueueInFlight,
		"per-chunk UpdatedEvents must not schedule a queue refresh")
}

// TestMessageCreatedEventRefreshesBusyAndQueue: CreatedEvent 是 运行
// boundary 和 must invalidate memoized busy state 和 fetch fresh
// busy/队列 values off-thread.
func TestMessageCreatedEventRefreshesBusyAndQueue(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true, queued: []string{"queued prompt"}}
	m := newBusyUI(ws)
	warmCaches(m, false)
	ws.resetCounters()

	_, cmd := m.Update(pubsub.Event[message.Message]{
		Type:    pubsub.CreatedEvent,
		Payload: message.Message{ID: "m1", SessionID: "s1", Role: message.User},
	})
	require.Zero(t, ws.syncProbes(), "the event handler itself must not probe synchronously")
	require.True(t, m.busyFetchInFlight, "CreatedEvent must schedule a busy refresh")
	require.True(t, m.promptQueueInFlight, "CreatedEvent must schedule a queue refresh")

	runCmds(m, cmd)
	require.True(t, m.isAgentBusy(), "refreshed busy state must land in the cache")
	require.Equal(t, 1, m.promptQueue.Shown(), "refreshed queue count must land in the cache")
	require.False(t, m.busyFetchInFlight)
	require.False(t, m.promptQueueInFlight)
}

// TestAgentTerminalNotificationsRefreshBusy pins busy→空闲 edge:
// agent clears 它的 活跃 请求 before publishing TypeAgentFinished (和
// TypeAgentError) precisely 所以 observers can re-probe. 处理器 must
// invalidate memoized busy state 和 re-fetch busy + 队列.
func TestAgentTerminalNotificationsRefreshBusy(t *testing.T) {
	pinTTLs(t)

	for _, typ := range []notify.Type{notify.TypeAgentFinished, notify.TypeAgentError} {
		t.Run(string(typ), func(t *testing.T) {
			ws := &countingWorkspace{ready: true} // agent now idle
			m := newBusyUI(ws)
			warmCaches(m, true) // stale: still busy
			ws.resetCounters()
			require.True(t, m.isAgentBusy())

			_, cmd := m.Update(pubsub.Event[notify.Notification]{
				Type:    pubsub.CreatedEvent,
				Payload: notify.Notification{Type: typ, SessionID: "s1"},
			})
			require.True(t, m.busyFetchInFlight, "terminal notification must schedule a busy refresh")
			require.True(t, m.promptQueueInFlight, "terminal notification must schedule a queue refresh")

			runCmds(m, cmd)
			require.False(t, m.isAgentBusy(),
				"busy→idle edge must reach the cache without waiting for the TTL")
		})
	}
}

// TestSessionSwitchRefreshesQueueAndBusy: switching sessions 必须 drop
// previous 会话's 队列 pill 和 memoized busy state 和 fetch new
// 会话's, 所以 esc never offers 到 clear wrong 队列.
func TestSessionSwitchRefreshesQueueAndBusy(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"a", "b"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueue.SetFetched([]string{"x", "y", "z", "w", "v"}) // stale queue pill from the previous session
	ws.resetCounters()

	_, cmd := m.Update(loadSessionMsg{session: &session.Session{ID: "s2"}})
	require.Zero(t, m.promptQueue.Shown(), "switching sessions must drop the old session's queue pill")
	require.True(t, m.promptQueueInFlight, "session switch must schedule a queue refresh")
	require.True(t, m.busyFetchInFlight, "session switch must schedule a busy refresh")

	runCmds(m, cmd)
	require.Equal(t, 2, m.promptQueue.Shown(), "the new session's queue must be fetched")
	require.Equal(t, []string{"a", "b"}, m.promptQueue.Items())
}

// TestToggleYoloWritesThroughCache: both 权限-mode toggle paths share
// toggleMode, which must 写入 known new 值 through 缓存 —
// no invalidation, no re-probe.
func TestToggleYoloWritesThroughCache(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, permMode: permission.PermissionModeNormal}
	m := newBusyUI(ws)

	got := m.toggleMode(permission.PermissionModeYolo)
	require.True(t, got)
	require.Equal(t, 1, ws.permSetCalls)
	readsAfterToggle := ws.permCalls
	require.Equal(t, 1, readsAfterToggle, "toggle reads the authoritative value exactly once")

	require.Equal(t, permission.PermissionModeYolo, m.permModeCached(), "the new value must be served from the cache")
	require.True(t, m.permModeCache.fresh(busyCacheTTL), "write-through must stamp the cache fresh")
	m.permModeCached()
	require.Equal(t, readsAfterToggle, ws.permCalls, "reads after the toggle must not re-probe")

	got = m.toggleMode(permission.PermissionModeYolo)
	require.False(t, got)
	require.Equal(t, permission.PermissionModeNormal, m.permModeCached())
}

// TestLocalYoloToggleSupersedesInFlightProbe pins generation bump 在
// toggleMode: busy/权限 probe dispatched before toggle carries
// old generation. Without advancing busyFetchGen 它的 stale 结果 would
// land 用 still-matching generation 和 clobber just-toggled 值.
func TestLocalYoloToggleSupersedesInFlightProbe(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, permMode: permission.PermissionModeNormal}
	m := newBusyUI(ws)
	warmCaches(m, false)

	// 一个 busy/权限 probe carrying pre-toggle generation 是 在 flight.
	m.busyFetchInFlight = true
	staleGen := m.busyFetchGen

	require.True(t, m.toggleMode(permission.PermissionModeYolo))
	require.NotEqual(t, staleGen, m.busyFetchGen,
		"toggle must advance the busy generation to supersede in-flight probes")
	require.Equal(t, permission.PermissionModeYolo, m.permModeCached(), "toggle must write the new value through the cache")

	// The stale probe (old generation, old mode=normal) lands.
	m.busyFetchInFlight = true
	cmds := m.applyBusyState(busyStateMsg{gen: staleGen, permMode: permission.PermissionModeNormal})
	require.Equal(t, permission.PermissionModeYolo, m.permModeCached(),
		"stale probe must not overwrite the freshly toggled value")
	require.NotEmpty(t, cmds, "stale probe must re-dispatch an authoritative refresh")
	require.True(t, m.busyFetchInFlight, "re-dispatched refresh must be in flight")
}

// TestSendMessageSetsOptimisticBusy pins the esc-after-enter behavior:
// submitting 提示词 optimistically marks agent busy 所以 immediate
// esc routes 到 cancelAgent instead 的 reading stale 空闲 值 和 doing
// nothing.
func TestSendMessageSetsOptimisticBusy(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true} // workspace still reports idle
	m := newBusyUI(ws)
	warmCaches(m, false)

	require.False(t, m.isAgentBusy())
	cmd := m.sendMessage("hello") // returned cmds (AgentRun etc.) deliberately not run
	require.NotNil(t, cmd)
	require.True(t, m.isAgentBusy(),
		"sendMessage must optimistically mark the agent busy")

	// esc right after enter: isAgentBusy gates cancelAgent, first press
	// arms double-press 取消.
	require.Zero(t, m.promptQueue.Shown())
	m.cancelAgent()
	require.True(t, m.isCanceling, "first esc press must arm cancellation")

	// Second press must actually 取消.
	m.cancelAgent()
	require.Equal(t, 1, ws.cancelCalls, "second esc press must cancel the agent")
}

// TestCancelAgentClearsQueueFromCachedCount: 队列-clear decision must
// come 从 memoized count — no synchronous AgentQueuedPrompts probe —
// 和 clearing must zero cached count immediately.
func TestCancelAgentClearsQueueFromCachedCount(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"a"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueue.SetFetched([]string{"a"})
	ws.resetCounters()

	cmd := m.cancelAgent()
	require.Nil(t, cmd)
	require.Equal(t, 1, ws.clearQueueCalls, "esc with a queue must clear it")
	require.Zero(t, ws.queuedCalls, "the decision must use the cached count, not a probe")
	require.Zero(t, ws.queueListCalls, "the decision must use the cached count, not a probe")
	require.Zero(t, m.promptQueue.Shown(), "the cached count must be zeroed immediately")
	require.Empty(t, m.promptQueue.Items())
	require.False(t, m.isCanceling, "clearing the queue must not arm cancellation")
}

// TestBackstopRefreshesStaleCaches: 当 memoized state outlives 它的 TTL
// 用 no 事件 edge, 更新 tail schedules exactly one off-thread
// refresh (deduplicated while 在 flight) 和 结果 lands 作为 消息.
func TestBackstopRefreshesStaleCaches(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true}
	m := newBusyUI(ws)
	// Caches start 在 their zero 值: stale 通过 definition.

	_, cmd := m.Update(plainMsg{})
	require.True(t, m.busyFetchInFlight, "stale caches must trigger a backstop refresh")
	require.Zero(t, ws.syncProbes(), "the backstop itself must not probe synchronously")

	// 一个 second 更新 while fetch 是 在 flight must 不 stack another.
	before := m.busyFetchInFlight
	m.Update(plainMsg{})
	require.Equal(t, before, m.busyFetchInFlight)
	require.Zero(t, ws.syncProbes())

	runCmds(m, cmd)
	require.False(t, m.busyFetchInFlight)
	require.True(t, m.isAgentBusy(), "the backstop result must land in the cache")
	require.Equal(t, 1, ws.agentBusyCalls, "exactly one probe per backstop refresh")

	// Freshly refreshed caches must 不 re-派发.
	m.Update(plainMsg{})
	require.False(t, m.busyFetchInFlight, "fresh caches must not re-dispatch the backstop")
}

// TestSetSessionMessagesGatesAnimationsOnBusy verifies 那 reloading
// 会话 does 不 运行 spinner animations 当 agent 是 不 busy.
// 一个 会话 那 was killed mid-generation can persist 助手消息
// 用 no Finish 片段, which still reports Spinning() even though nothing
// 是 running. Arming clock 为 it would leave ghost "working"
// spinner after 会话 是 reloaded.
func TestSetSessionMessagesGatesAnimationsOnBusy(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: false}
	m := newBusyUI(ws)
	warmCaches(m, false)

	// 一个 消息 那 looks unfinished (no Finish 片段, no content).
	msgs := []message.Message{
		{
			ID:        "m1",
			SessionID: "s1",
			Role:      message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "thinking..."},
			},
		},
	}

	// When agent 是 不 busy, setSessionMessages must freeze
	// animation clock 所以 ghost spinner stays still.
	cmd := m.setSessionMessages(msgs)
	require.Nil(t, cmd, "setSessionMessages must not start animations when agent is idle")
	require.True(t, m.animClock.frozen, "an idle session reload must freeze the animation clock")
	require.Nil(t, m.ensureAnimating(), "a frozen clock must not arm while idle")
	require.False(t, m.animClock.running)

	// When agent 是 busy, clock may 运行 为 same 消息.
	warmCaches(m, true)
	cmd = m.setSessionMessages(msgs)
	require.Nil(t, cmd, "setSessionMessages must not arm the clock itself")
	require.False(t, m.animClock.frozen, "setSessionMessages must allow animations when agent is busy")
	require.NotNil(t, m.ensureAnimating(), "a visible spinning message must arm the clock")
	require.True(t, m.animClock.running)
}

// TestBusyProbeReEnablesFrozenAnimationClock pins boot race: 会话
// loaded before first busy probe lands reads zero-值 memoized
// 缓存 和 freezes animation clock even though agent 是 working,
// 所以 spinner rendered 在 boot never ticks. When authoritative
// busy 结果 arrives it must re-enable clock 所以 更新 tail can
// arm it 为 freshly rendered spinner.
func TestBusyProbeReEnablesFrozenAnimationClock(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, agentBusy: true}
	m := newBusyUI(ws)

	// An unfinished 助手 turn (no Finish 片段) renders 用 spinner.
	msgs := []message.Message{
		{
			ID:        "m1",
			SessionID: "s1",
			Role:      message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "thinking..."},
			},
		},
	}

	// Boot: busy 缓存 still reads 它的 zero 值, 所以 会话 load
	// freezes animation clock even though agent 是 working.
	require.False(t, m.isAgentBusy(), "boot: the memoized busy state is not populated yet")
	cmd := m.setSessionMessages(msgs)
	require.Nil(t, cmd)
	require.True(t, m.animClock.frozen, "the boot reload must freeze the clock off the unpopulated cache")
	require.Nil(t, m.ensureAnimating(), "the frozen clock must not arm")
	require.False(t, m.animClock.running)

	// authoritative probe lands: agent 是 busy. gate must
	// re-open 所以 visible spinner ticks.
	m.applyBusyState(busyStateMsg{gen: m.busyFetchGen, ready: true, agentBusy: true})
	require.False(t, m.animClock.frozen, "a busy probe must re-enable the animation gate")
	require.NotNil(t, m.ensureAnimating(), "a visible spinning item must arm the clock once allowed")
	require.True(t, m.animClock.running)

	// An 空闲 probe must 不 re-freeze clock 在 它的 own: ghost
	// guard belongs 到 会话 reload, 不 到 probe.
	m.applyBusyState(busyStateMsg{gen: m.busyFetchGen, ready: true, agentBusy: false})
	require.False(t, m.animClock.frozen, "an idle probe must leave the gate alone")
}

// TestStaleBusyRefreshDiscardedAndReDispatched pins generation guard 为
// busy/权限 state: probe started before newer state transition
// (here optimistic busy 写入) must 不 overwrite newer 值 当 it
// lands, 和 authoritative refresh must 不 是 lost merely because
// older probe was 在 flight — stale 结果 re-dispatches it.
func TestStaleBusyRefreshDiscardedAndReDispatched(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)

	// 一个 busy probe 是 在 flight; capture generation it was dispatched
	// 用, then newer transition (optimistic send) supersedes it.
	m.busyFetchInFlight = true
	staleGen := m.busyFetchGen
	m.agentBusyCache.set(true) // optimistic busy
	m.busyFetchGen++           // newer state transition

	// stale probe (agent reported 空闲) lands 用 old generation.
	cmds := m.applyBusyState(busyStateMsg{gen: staleGen, agentBusy: false})
	require.True(t, m.isAgentBusy(),
		"a stale busy result must not overwrite the newer optimistic busy state")
	require.NotEmpty(t, cmds,
		"a stale busy result must re-dispatch the authoritative refresh")
	require.True(t, m.busyFetchInFlight, "the re-dispatched probe must be in flight")

	// fresh probe (matching generation) 是 applied normally.
	freshGen := m.busyFetchGen
	m.applyBusyState(busyStateMsg{gen: freshGen, agentBusy: false})
	require.False(t, m.isAgentBusy(), "a current-generation result must land in the cache")
}

// TestStalePromptQueueDiscardedAndReDispatched pins generation guard 为
// 队列: fetch started before newer transition (here 队列 clear)
// must 不 repopulate cleared 队列, 和 it must re-派发
// authoritative fetch instead 的 being applied.
func TestStalePromptQueueDiscardedAndReDispatched(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"real"}}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.promptQueue.SetFetched([]string{"real"})

	// 一个 fetch 是 在 flight; capture 它的 generation, then newer transition
	// (esc clears 队列) supersedes it.
	m.promptQueueInFlight = true
	staleGen := m.promptQueueGen
	m.invalidatePromptQueue()
	m.promptQueue.ClearServer()

	// stale fetch (still saw one 提示词) lands 为 same 会话.
	cmds := m.applyPromptQueue(promptQueueMsg{
		forSession: "s1",
		gen:        staleGen,
		prompts:    []string{"stale"},
	})
	require.Zero(t, m.promptQueue.Shown(),
		"a stale queue result must not repopulate the cleared queue")
	require.Empty(t, m.promptQueue.Items())
	require.NotEmpty(t, cmds,
		"a stale queue result must re-dispatch the authoritative fetch")
	require.True(t, m.promptQueueInFlight, "the re-dispatched fetch must be in flight")
}

// TestStalePromptQueuePreservesSessionScoping pins 那 generation guard
// does 不 weaken 会话 scoping: fetch scoped 到 different 会话 是
// still discarded 和 re-fetched even 当 它的 generation would otherwise
// match.
func TestStalePromptQueuePreservesSessionScoping(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws) // active session "s1"
	warmCaches(m, false)
	m.promptQueueInFlight = true
	gen := m.promptQueueGen

	cmds := m.applyPromptQueue(promptQueueMsg{
		forSession: "other",
		gen:        gen,
		prompts:    []string{"from other session"},
	})
	require.Zero(t, m.promptQueue.Shown(),
		"a result from a different session must never populate the queue")
	require.NotEmpty(t, cmds, "a session-mismatched result must re-fetch for the current session")
}

// TestRenderHelpersDoNotProbeWorkspace pins render-路径 side 的
// invariant 为 模型 和 LSP info: selectedLargeModel, lspInfo, 和
// lspErrorCount render 从 memoized state 只. They 运行 在 every frame
// (landing view, sidebar, compact header), 和 probes behind them
// (AgentIsReady, AgentModel, LSPGetStates, LSPGetDiagnosticCounts) 是
// synchronous HTTP round-trips 在 客户端/服务端 mode.
func TestRenderHelpersDoNotProbeWorkspace(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	m.agentReady = true
	m.lspStates = map[string]workspace.LSPClientInfo{
		"gopls": {Name: "gopls", State: lsp.StateReady, DiagnosticCount: 3},
	}
	m.lspDiagnostics = map[string]lsp.DiagnosticCounts{
		"gopls": {Error: 2, Warning: 1},
	}

	for range 10 {
		require.NotNil(t, m.selectedLargeModel())
		m.lspInfo(40, 5, true)
		require.Equal(t, 3, m.lspErrorCount())
	}

	// modelInfo reaches 提供方 配置 只 through memoized 模型;
	// 用 agent 不 ready it renders empty state.
	m.agentReady = false
	for range 10 {
		m.modelInfo(40)
	}

	require.Zero(t, ws.syncProbes(), "render helpers must never probe the workspace")
}

// TestBusyRefreshCarriesReadyAndModel: off-thread busy probe 必须 also
// 投递 coordinator's readiness 和 selected 模型 所以 sidebar 和
// landing view render them without per-frame probes.
func TestBusyRefreshCarriesReadyAndModel(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{
		ready: true,
		model: workspace.AgentModel{ModelCfg: config.SelectedModel{Model: "test-model", Provider: "prov"}},
	}
	m := newBusyUI(ws)
	require.Nil(t, m.selectedLargeModel(), "before any probe the model is unknown")

	_, cmd := m.Update(plainMsg{}) // stale caches: the backstop dispatches
	runCmds(m, cmd)

	require.True(t, m.agentReady, "the probe must land readiness in the cache")
	sel := m.selectedLargeModel()
	require.NotNil(t, sel)
	require.Equal(t, "test-model", sel.ModelCfg.Model, "the probe must land the model in the cache")
}

// TestAgentModelChangedRefreshesModel: after 模型 change
// (selection/thinking/reasoning cmds 序号 agentModelChangedCmd),
// 处理器 must re-fetch ready/模型 off-thread — no synchronous probe — 和
// fresh 模型 must replace memoized one.
func TestAgentModelChangedRefreshesModel(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{
		ready: true,
		model: workspace.AgentModel{ModelCfg: config.SelectedModel{Model: "new-model"}},
	}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.agentModel = workspace.AgentModel{ModelCfg: config.SelectedModel{Model: "old-model"}}
	ws.resetCounters()

	_, cmd := m.Update(agentModelChangedMsg{})
	require.Zero(t, ws.syncProbes(), "the model-change handler must not probe synchronously")
	require.True(t, m.busyFetchInFlight, "a model change must schedule a ready/model refresh")

	runCmds(m, cmd)
	require.Equal(t, "new-model", m.agentModel.ModelCfg.Model,
		"the refreshed model must land in the cache")
}

// TestMCPStateChangedRefreshesModel pins two edges 的 MCP state change:
// memoized sidebar states 刷新 off-thread ( authoritative re-fetch
// rather than 事件 payload), 和 agent rebuild — fourth
// UpdateAgentModel 调用 site — can change effective 模型, 所以
// memoized ready/模型 state must 是 re-fetched off-thread afterwards;
// updateAgentModelCmd helper makes 那 unforgettable.
func TestMCPStateChangedRefreshesModel(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{
		ready: true,
		model: workspace.AgentModel{ModelCfg: config.SelectedModel{Model: "post-mcp-model"}},
		mcpStates: map[string]mcp.ClientInfo{
			"ctx": {Name: "ctx", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 4}},
		},
	}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.agentModel = workspace.AgentModel{ModelCfg: config.SelectedModel{Model: "pre-mcp-model"}}
	ws.resetCounters()

	_, cmd := m.Update(pubsub.Event[mcp.Event]{
		Type:    pubsub.UpdatedEvent,
		Payload: mcp.Event{Type: mcp.EventStateChanged, Name: "ctx", State: mcp.StateConnected},
	})
	require.Zero(t, ws.syncProbes(), "the MCP event handler must not probe synchronously")
	require.True(t, m.mcpFetchInFlight, "an MCP state change must schedule a state refresh")
	runCmds(m, cmd)
	require.False(t, m.mcpFetchInFlight)
	require.Equal(t, mcp.StateConnected, m.mcpStates["ctx"].State, "fetched states must land in the cache")

	// rebuild 是 sequenced 用 agentModelChangedCmd; tea.序号's
	// wrapper msg 是 unexported, 所以 投递 invalidation 消息
	// way runtime would once 序号 finishes.
	_, cmd = m.Update(agentModelChangedMsg{})
	require.True(t, m.busyFetchInFlight, "an MCP state change must schedule a ready/model refresh")
	runCmds(m, cmd)

	require.True(t, m.agentReady)
	require.Equal(t, "post-mcp-model", m.agentModel.ModelCfg.Model,
		"an MCP state change must refresh the memoized model")
}

// TestLSPEventRefreshIsOffThreadAndDeduped pins LSP side 的
// invariant: LSP 事件 must 不 fetch states synchronously 在 更新
// (LSPGetStates + per-服务端 LSPGetDiagnosticCounts 是 HTTP round-trips 在
// 客户端/服务端 mode, 和 diagnostics events arrive per edited 文件). 它
// schedules one off-thread fetch, dedups while one 是 在 flight, 和
// re-dispatches queued refresh 当 在-flight fetch lands.
func TestLSPEventRefreshIsOffThreadAndDeduped(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{
		ready:     true,
		lspStates: map[string]workspace.LSPClientInfo{"gopls": {Name: "gopls", DiagnosticCount: 3}},
		lspDiags:  map[string]lsp.DiagnosticCounts{"gopls": {Error: 2, Warning: 1}},
	}
	m := newBusyUI(ws)
	warmCaches(m, false)
	ws.resetCounters()

	_, cmd := m.Update(pubsub.Event[workspace.LSPEvent]{
		Payload: workspace.LSPEvent{Type: workspace.LSPEventDiagnosticsChanged, Name: "gopls"},
	})
	require.Zero(t, ws.syncProbes(), "the LSP event handler must not probe synchronously")
	require.True(t, m.lspFetchInFlight, "an LSP event must schedule an off-thread refresh")

	// 一个 second 事件 while fetch 是 在 flight queues re-fetch instead
	// 的 stacking another 派发.
	m.Update(pubsub.Event[workspace.LSPEvent]{
		Payload: workspace.LSPEvent{Type: workspace.LSPEventDiagnosticsChanged, Name: "gopls"},
	})
	require.Zero(t, ws.syncProbes())
	require.True(t, m.lspRefreshQueued, "an event during an in-flight fetch must queue a re-fetch")

	runCmds(m, cmd)
	require.False(t, m.lspFetchInFlight)
	require.False(t, m.lspRefreshQueued, "the queued flag must clear once the re-dispatched fetch lands")
	require.Equal(t, 3, m.lspStates["gopls"].DiagnosticCount, "fetched states must land in the cache")
	require.Equal(t, 2, m.lspDiagnostics["gopls"].Error, "fetched severity counts must land in the cache")
	require.Equal(t, 3, m.lspErrorCount())
	require.Equal(t, 2, ws.lspStateCalls, "one fetch plus the queued re-fetch")
}

// TestRemoteYoloToggleUpdatesEditorPrompt pins second fix: 当
// asynchronous busy-state refresh reports yolo mode different 从
// cached one ( remote toggle), applyBusyState must 更新 textarea
// 提示词 function too, 不 just 缓存 — otherwise 提示词 icon/style
// 保持 rendering old mode.
func TestRemoteYoloToggleUpdatesEditorPrompt(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	m.textarea.Focus()
	m.textarea.SetWidth(40)
	m.permModeCache.set(permission.PermissionModeNormal)
	m.setEditorPrompt(permission.PermissionModeNormal)
	normalPrompt := ansi.Strip(m.textarea.View())

	// 一个 remote toggle flips yolo 在; delivered via off-thread refresh.
	m.applyBusyState(busyStateMsg{gen: m.busyFetchGen, permMode: permission.PermissionModeYolo})
	require.Equal(t, permission.PermissionModeYolo, m.permModeCached(), "the refresh must write the new mode through the cache")
	yoloPrompt := ansi.Strip(m.textarea.View())
	require.NotEqual(t, normalPrompt, yoloPrompt,
		"a remote yolo toggle must change the rendered editor prompt")
	require.Contains(t, yoloPrompt, "!",
		"the yolo prompt icon must render after a remote toggle")

	// Flipping back off must restore normal 提示词.
	m.applyBusyState(busyStateMsg{gen: m.busyFetchGen, permMode: permission.PermissionModeNormal})
	require.Equal(t, permission.PermissionModeNormal, m.permModeCached())
	require.Equal(t, normalPrompt, ansi.Strip(m.textarea.View()),
		"toggling yolo off must restore the normal editor prompt")
}
