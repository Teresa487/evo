package model

// Memoized 工作区 state.
//
// In 客户端/服务端 mode every 工作区 probe (busy checks, 权限 mode,
// queued prompts, agent readiness/模型, LSP state) 是 synchronous HTTP
// round-trip, 和 更新 goroutine 是 render loop — blocking it
// freezes typing. UI therefore never probes 工作区 synchronously
// 从 更新 或 View. ( constructor 是 one carve-out: New seeds
// yolo 和 ready/模型 caches synchronously 所以 first frame has values 到
// render; Init then refreshes them off-thread.)
//
//   - Reads (isAgentBusy, permModeCached, promptQueue, selectedLargeModel,
// lspInfo) always return memoized 值, stale 或 不.
// - State edges (消息 created, agent finished/errored, 提示词
// submitted, 取消, 会话 switch, yolo toggle, 模型 change, LSP
// events) invalidate 或 写入 through caches 和 派发
// off-thread 刷新 cmd.
// - 一个 TTL backstop 在 end 的 更新 re-dispatches refresh whenever
// memoized state has gone stale, 所以 unrelated churn (typing,
// resize storms, spinner ticks) 只 ever schedules async work.
//
// Fresh values arrive 作为 busyStateMsg / promptQueueMsg / lspStatesMsg 和
// 是 applied 在 更新 goroutine, per UI guidelines (no IO 在
// 更新, no 模型 mutation inside commands).

import (
	"slices"
	"time"

	"github.com/charmbracelet/crush/internal/message"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/workspace"
)

// busyCacheTTL bounds how long memoized busy/权限 state may go
// without re-probe being scheduled. Package var 所以 tests can pin it.
var busyCacheTTL = 500 * time.Millisecond

// promptQueueTTL 是 backstop refresh interval 为 queued-提示词
// state; 队列 是 otherwise refreshed 在 事件 edges.
var promptQueueTTL = 2 * time.Second

// ttlCache memoizes one boolean 工作区 probe 结果.
type ttlCache struct {
	val bool
	at  time.Time
}

// fresh reports whether cached 值 是 within 它的 TTL.
func (c *ttlCache) fresh(ttl time.Duration) bool {
	return !c.at.IsZero() && time.Since(c.at) < ttl
}

// set writes known-good 值 through 缓存.
func (c *ttlCache) set(val bool) {
	c.val = val
	c.at = time.Now()
}

// invalidate marks 值 stale 所以 next 更新-tail backstop
// re-probes; last 值 keeps being served 在 meantime.
func (c *ttlCache) invalidate() {
	c.at = time.Time{}
}

// modeTTLCache memoizes one 权限-mode 工作区 probe 结果.
type modeTTLCache struct {
	val permission.PermissionMode
	at  time.Time
}

// fresh reports whether cached 值 是 within 它的 TTL.
func (c *modeTTLCache) fresh(ttl time.Duration) bool {
	return !c.at.IsZero() && time.Since(c.at) < ttl
}

// set writes known-good 值 through 缓存.
func (c *modeTTLCache) set(val permission.PermissionMode) {
	c.val = val
	c.at = time.Now()
}

// invalidate marks 值 stale 所以 next 更新-tail backstop
// re-probes; last 值 keeps being served 在 meantime.
func (c *modeTTLCache) invalidate() {
	c.at = time.Time{}
}

// busyStateMsg delivers 结果 的 off-thread busy/权限 probe.
type busyStateMsg struct {
	// gen 是 busy generation captured 当 probe was dispatched.
	// 一个 结果 whose generation no longer matches m.busyFetchGen started
	// before a newer state transition (optimistic send, invalidation,
	// 会话 switch, ...) 和 是 discarded, then re-fetched, 所以
	// authoritative refresh 是 never lost 到 older 在-flight 请求.
	gen       uint64
	ready     bool
	agentBusy bool
	permMode  permission.PermissionMode
	// forSession 是 会话 sessionBusy was probed 为.
	forSession string
	// sessionBusy reports whether forSession itself has 运行 在 flight,
	// unlike agentBusy, which any 会话 或 shell command sets.
	sessionBusy bool
	// 模型 是 coordinator's 活跃 agent 和 selected 模型, fetched 通过
	// same probe 所以 sidebar/landing 模型 info renders 从
	// memoized state 和 input mode follows shared agent. Zero (和
	// ignored) 当 ready 是 false.
	model workspace.AgentModel
}

// promptQueueMsg delivers the queued prompts fetched off-thread.
type promptQueueMsg struct {
	// forSession 是 会话 fetch was scoped 到; 结果 那
	// raced 会话 switch 是 discarded 和 re-fetched.
	forSession string
	// gen 是 队列 generation captured 在 派发; like
	// busyStateMsg.gen it guards against stale 在-flight 结果
	// overwriting newer optimistic 或 invalidated 队列 state.
	gen     uint64
	prompts []string
}

// agentRunSubmittedMsg reports whether AgentRun accepted 提示词. 一个
// rejected 提示词 gets no RunComplete, 所以 err must end 它的 turn.
type agentRunSubmittedMsg struct {
	sessionID string
	// pendingID 是 optimistic 队列 条目 为 提示词, 或 zero.
	pendingID uint64
	err       error
}

// agentModelChangedMsg reports 那 coordinator's 模型 was updated
// (模型 selection, thinking toggle, reasoning effort), 所以 memoized
// ready/模型 state should 是 re-fetched without waiting 为 TTL.
type agentModelChangedMsg struct{}

// agentModelChangedCmd 是 sequenced after cmds 那 调用 UpdateAgentModel 所以
// refresh probes coordinator 只 once 更新 has completed.
// Callers should reach 为 updateAgentModelCmd rather than sequencing 这
// 通过 hand.
func agentModelChangedCmd() tea.Msg { return agentModelChangedMsg{} }

// currentSessionID returns 活跃 会话's ID, 或 "" 当 none.
func (m *UI) currentSessionID() string {
	if m.session == nil {
		return ""
	}
	return m.session.ID
}

// invalidateBusyCaches marks all memoized 工作区 probe state stale 和
// bumps busy generation 所以 any 在-flight probe 结果 是 discarded 当
// it lands. Called 通过 handlers 为 events 那 change agent 或 权限
// state.
func (m *UI) invalidateBusyCaches() {
	m.agentBusyCache.invalidate()
	m.permModeCache.invalidate()
	m.busyFetchGen++
}

// invalidatePromptQueue bumps 提示词-队列 generation 所以 any 在-flight
// 队列 fetch 结果 是 discarded 当 it lands (和 re-fetched) instead 的
// overwriting newer optimistic 或 cleared 队列 state.
func (m *UI) invalidatePromptQueue() {
	m.promptQueueGen++
}

// dispatchBusyRefresh returns command 那 probes 工作区 busy 和
// 权限 state off 更新 goroutine, delivering busyStateMsg. 它
// returns nil while probe 是 already 在 flight. closure captures 只
// locals (never m) 所以 it 是 safe off-thread; state 是 applied 通过
// applyBusyState 在 更新 goroutine.
func (m *UI) dispatchBusyRefresh() tea.Cmd {
	if m.busyFetchInFlight || m.com == nil || m.com.Workspace == nil {
		return nil
	}
	m.busyFetchInFlight = true
	ws := m.com.Workspace
	gen := m.busyFetchGen
	sessionID := m.currentSessionID()
	return func() tea.Msg {
		st := busyStateMsg{gen: gen, forSession: sessionID}
		if ws.AgentIsReady() {
			st.ready = true
			st.agentBusy = ws.AgentIsBusy()
			st.model = ws.AgentModel()
			if sessionID != "" {
				st.sessionBusy = ws.AgentIsSessionBusy(sessionID)
			}
		}
		st.permMode = ws.PermissionMode()
		return st
	}
}

// updateAgentModelCmd sequences coordinator 模型 rebuild
// (UpdateAgentModel) 用 invalidation 的 memoized ready/模型
// state. Callers wrap their pre-work 在 pre; memoized 模型 must 只
// 是 re-probed after rebuild lands ( synchronous HTTP round-trip 在
// 客户端/服务端 mode), 所以 消息 drives refresh instead 的 each
// 调用 site remembering 到.
func (m *UI) updateAgentModelCmd(pre tea.Cmd) tea.Cmd {
	return tea.Sequence(pre, agentModelChangedCmd)
}

// applyBusyState stores off-thread probe 结果 和 reacts 到 busy
// edges (todo spinner, pills). Runs 在 更新 goroutine.
func (m *UI) applyBusyState(msg busyStateMsg) []tea.Cmd {
	m.busyFetchInFlight = false
	if msg.gen != m.busyFetchGen {
		// 这 probe started before newer state transition (optimistic
		// send, invalidation, 会话 switch, ...). Discard 它的 结果 和
		// re-派发 所以 required authoritative refresh 是 不 lost
		// merely because 这 older 请求 was 在 flight.
		if cmd := m.dispatchBusyRefresh(); cmd != nil {
			return []tea.Cmd{cmd}
		}
		return nil
	}
	prevBusy := m.isAgentBusy()
	prevMode := m.permModeCached()
	m.agentBusyCache.set(msg.agentBusy)
	m.permModeCache.set(msg.permMode)
	m.agentReady = msg.ready
	m.agentModel = msg.model
	modeChanged := m.followMainAgent(msg.model.Agent)
	if prevMode != msg.permMode || modeChanged {
		// 一个 remote/async toggle changed 权限 或 input mode:
		// 更新 editor 提示词 function 所以 提示词 icon/style tracks
		// new mode. 缓存 是 written above 和 placeholder 是
		// refreshed 通过 更新 tail.
		m.setEditorPrompt(msg.permMode)
	}

	var cmds []tea.Cmd
	busy := m.isAgentBusy()
	if busy {
		// 一个 会话 reload 那 raced unpopulated busy 缓存 (
		// zero-值 读取 在 boot) froze animation clock even though
		// agent 是 working. authoritative probe re-enables it 所以
		// freshly rendered spinners tick. When probe reports 空闲
		// reload gate stands, keeping ghost spinners still.
		m.animClock.frozen = false
	}
	if m.hasSession() && hasInProgressTodo(m.session.Todos) && busy && !m.todoIsSpinning {
		m.todoIsSpinning = true
		cmds = append(cmds, m.todoSpinner.Tick)
	}
	if m.todoIsSpinning && !busy {
		m.todoIsSpinning = false
	}
	m.probedSession = msg.forSession
	m.probedSessionBusy = msg.sessionBusy
	if prevBusy != busy {
		m.renderPills()
	}
	return cmds
}

// dispatchPromptQueueRefresh returns command 那 fetches queued
// prompts off 更新 goroutine, delivering promptQueueMsg. 它 returns
// nil while fetch 是 already 在 flight. With no 活跃 会话 队列
// 是 simply cleared.
func (m *UI) dispatchPromptQueueRefresh() tea.Cmd {
	if m.promptQueueInFlight || m.com == nil || m.com.Workspace == nil {
		return nil
	}
	if !m.hasSession() {
		m.promptQueueCheckedAt = time.Now()
		// Bump generation 所以 any 在-flight fetch scoped 到
		// now-departed 会话 是 discarded rather than repopulating
		// 队列.
		m.invalidatePromptQueue()
		prev := m.promptQueue.Shown()
		m.promptQueue.Reset()
		if prev != 0 {
			m.updateLayoutAndSize()
		}
		return nil
	}
	m.promptQueueInFlight = true
	ws := m.com.Workspace
	sessionID := m.session.ID
	gen := m.promptQueueGen
	return func() tea.Msg {
		msg := promptQueueMsg{forSession: sessionID, gen: gen}
		if ws.AgentIsReady() {
			msg.prompts = ws.AgentQueuedPromptsList(sessionID)
		}
		return msg
	}
}

// applyPromptQueue stores off-thread 队列 fetch 和 re-layouts 当
// count changed. Runs 在 更新 goroutine.
func (m *UI) applyPromptQueue(msg promptQueueMsg) []tea.Cmd {
	m.promptQueueInFlight = false
	if msg.forSession != m.currentSessionID() || msg.gen != m.promptQueueGen {
		// fetch raced 会话 switch 或 newer 队列 transition
		// (submit, clear, invalidation). Discard stale 结果 和
		// re-fetch 所以 newer state 是 不 clobbered 和 authoritative
		// refresh 是 不 lost 到 这 older 在-flight 请求.
		if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
			return []tea.Cmd{cmd}
		}
		return nil
	}
	m.promptQueueCheckedAt = time.Now()
	prevItems := m.promptQueue.Items()
	m.promptQueue.SetFetched(msg.prompts)
	itemsChanged := !slices.Equal(prevItems, m.promptQueue.Items())
	countChanged := len(prevItems) != m.promptQueue.Shown()
	// 一个 报告 waits 在 队列 until 模型 reads it, 但 它的
	// 子代理 是 already done.
	for _, prompt := range msg.prompts {
		if report, ok := message.ParseSubAgentReportPrompt(prompt); ok {
			m.markDispatchReported(report.Label)
		}
	}
	if countChanged {
		m.updateLayoutAndSize()
	} else if itemsChanged {
		m.renderPills()
	}
	return nil
}

// staleWorkspaceRefreshCmds 是 TTL backstop, called 在 tail 的
// 更新: 当 any memoized 工作区 state has outlived 它的 TTL (和 no
// 事件 edge refreshed it), schedule off-thread re-probe. 它 never does
// IO itself — couple 的 time comparisons per 消息 在 most.
func (m *UI) staleWorkspaceRefreshCmds() []tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	var cmds []tea.Cmd
	if !m.agentBusyCache.fresh(busyCacheTTL) || !m.permModeCache.fresh(busyCacheTTL) {
		if cmd := m.dispatchBusyRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if m.hasSession() && time.Since(m.promptQueueCheckedAt) >= promptQueueTTL {
		if cmd := m.dispatchPromptQueueRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if time.Since(m.lspCheckedAt) >= lspStatesTTL {
		if cmd := m.dispatchLSPRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if time.Since(m.mcpCheckedAt) >= mcpStatesTTL {
		if cmd := m.dispatchMCPRefresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// permModeCached reports memoized 权限 mode. Toggles 写入
// through 缓存; 更新-tail backstop keeps it bounded-stale
// otherwise.
func (m *UI) permModeCached() permission.PermissionMode {
	return m.permModeCache.val
}

// yoloActive reports whether memoized 权限 mode skips 权限
// prompts. Sysadmin skips them too, differing 从 yolo 只 在 dropping
// exec-time block list, which input-mode cycle has no stake 在.
func (m *UI) yoloActive() bool {
	return m.permModeCached() != permission.PermissionModeNormal
}
