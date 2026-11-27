package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// swapInitGate replaces package-global initDone 渠道 那 WaitForInit
// waits 在 用 fresh, open one 为 duration 的 测试, restoring
// original 在 清理. 这 lets each 测试 drive WaitForInit deterministically
// (通过 closing returned 渠道 到 signal "init 完成") instead 的
// branching 在 whether earlier 测试 already closed process-wide
// one-shot. tests 那 use it 是 不 parallel 和 nothing else touches
// gate during unit-测试 运行, 所以 swap 是 race-free.
func swapInitGate(t *testing.T) chan struct{} {
	t.Helper()
	orig := initDone
	initDone = make(chan struct{})

	initMu.Lock()
	origStarted := initStarted
	origArmedAt := initArmedAt
	initStarted = true
	initArmedAt = time.Now()
	initMu.Unlock()

	t.Cleanup(func() {
		initDone = orig
		initMu.Lock()
		initStarted = origStarted
		initArmedAt = origArmedAt
		initMu.Unlock()
	})
	return initDone
}

// TestWaitForInit_BlocksUntilInitCompletes pins the contract the
// non-interactive 路径 relies 在: WaitForInit blocks while MCP initialization
// 是 still 在 flight 和 returns once it completes. Non-interactive runs
// (`crush 运行`) wait 在 it before reading 工具 registry 所以 slow-到-start
// servers (e.g. stdio Python via uv) have registered their tools first.
// Interactive runs deliberately do 不 gate 在 it ( slow 服务端 froze
// TUI's first 提示词); they build 工具 palette 从 whatever 是 registered
// 在 send time 和 pick up late servers 在 later runs. See coordinator.运行.
func TestWaitForInit_BlocksUntilInitCompletes(t *testing.T) {
	gate := swapInitGate(t)

	// Init 不 done yet: WaitForInit must block until 上下文 expires.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, WaitForInit(ctx), context.DeadlineExceeded,
		"WaitForInit must block while initialization is in flight")

	// Once initialization completes (the gate closes), WaitForInit returns nil.
	close(gate)
	require.NoError(t, WaitForInit(context.Background()),
		"WaitForInit must return once initialization has completed")
}

// TestWaitForInit_ReturnsWhenNotArmed 是 regression 测试 为 callers
// outside app startup. Those paths never 调用 mcp.Initialize (which 是
// what arms gate), 所以 WaitForInit must return immediately instead 的
// blocking 在 渠道 那 will never close. Before fix it blocked
// until ctx was cancelled, hanging RunNonInteractive's gate forever.
func TestWaitForInit_ReturnsWhenNotArmed(t *testing.T) {
	// Ensure gate looks unarmed regardless 的 测试 ordering.
	initMu.Lock()
	orig := initStarted
	initStarted = false
	initMu.Unlock()
	t.Cleanup(func() {
		initMu.Lock()
		initStarted = orig
		initMu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, WaitForInit(ctx),
		"WaitForInit must return immediately when initialization was never armed")
}

// TestWaitForInitBudget_ProceedsWhenInitWedged 是 regression 测试 为
// "messages go into void" hang: MCP 服务端 那 never answers 它的
// handshake (e.g. Python 服务端 那 chokes 在 SEP-2575 服务端/discover
// probe without responding) keeps Initialize — 和 所以 init gate — open 为
// 它的 full connect 超时, up 到 minutes. Every turn gated 在 open-ended
// WaitForInit, 所以 typing 消息 produced nothing: no persisted 用户
// 消息, no spinner. bounded wait must give up after 它的 budget 和 let
// turn proceed without wedged 服务端.
func TestWaitForInitBudget_ProceedsWhenInitWedged(t *testing.T) {
	swapInitGate(t) // armed, never closed: initialization is wedged

	start := time.Now()
	require.NoError(t, WaitForInitBudget(context.Background(), 50*time.Millisecond),
		"a wedged MCP initialization must not fail the turn once the budget elapses")
	require.Less(t, time.Since(start), 5*time.Second,
		"WaitForInitBudget must return promptly after its budget, not block until initialization finishes")
}

// TestWaitForInitBudget_ReturnsOnceInitCompletes pins 那 budget 是
// ceiling, 不 delay: 当 initialization finishes within budget
// wait ends immediately, preserving #132 guarantee 那 healthy
// slow-到-start 服务端's tools 是 registered before buildTools reads
// registry.
func TestWaitForInitBudget_ReturnsOnceInitCompletes(t *testing.T) {
	gate := swapInitGate(t)

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(gate)
	}()

	start := time.Now()
	require.NoError(t, WaitForInitBudget(context.Background(), 30*time.Second))
	require.Less(t, time.Since(start), 5*time.Second,
		"WaitForInitBudget must return as soon as initialization completes, not sit out its budget")
}

// TestWaitForInitBudget_CallerCancellationStillAborts pins 那 budget
// 只 absorbs 它的 own 期限: caller's 上下文 being cancelled (
// 用户 hit esc, 请求 ended) still aborts turn 用 错误 rather
// than being mistaken 为 elapsed budget 和 silently proceeding.
func TestWaitForInitBudget_CallerCancellationStillAborts(t *testing.T) {
	swapInitGate(t) // armed, never closed

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	require.ErrorIs(t, WaitForInitBudget(ctx, 30*time.Second), context.Canceled,
		"caller cancellation must surface as an error, not be swallowed like an elapsed budget")
}

// TestWaitForInitBudget_DeadlineIsAbsolute pins 那 budget anchors 在
// ArmInit, 不 在 each 调用: turn's 路径 holds several sequential waiters
// (readyWg's 工具 build, then turn's own gate), 和 per-调用 budgets would
// stack into multiples 的 budget while 服务端 是 wedged. 一个 调用 arriving
// after armed-在 期限 has already passed must return 在 once.
func TestWaitForInitBudget_DeadlineIsAbsolute(t *testing.T) {
	swapInitGate(t) // armed, never closed

	// Rewind arming time 所以 30s budget 是 already spent.
	initMu.Lock()
	initArmedAt = time.Now().Add(-time.Minute)
	initMu.Unlock()

	start := time.Now()
	require.NoError(t, WaitForInitBudget(context.Background(), 30*time.Second),
		"a call after the armed-at deadline must proceed without waiting")
	require.Less(t, time.Since(start), 5*time.Second,
		"the budget must not restart per call once the armed-at deadline has passed")
}

// TestWaitForInitBudget_ReturnsWhenNotArmed mirrors WaitForInit's contract 为
// coordinators built outside app startup: nothing armed means nothing 到 wait
// 为.
func TestWaitForInitBudget_ReturnsWhenNotArmed(t *testing.T) {
	initMu.Lock()
	orig := initStarted
	initStarted = false
	initMu.Unlock()
	t.Cleanup(func() {
		initMu.Lock()
		initStarted = orig
		initMu.Unlock()
	})

	require.NoError(t, WaitForInitBudget(context.Background(), 30*time.Second),
		"WaitForInitBudget must return immediately when initialization was never armed")
}

// TestWaitForInit_ToolsVisibleAfterInit pins the visibility guarantee
// WaitForInit gives non-interactive 路径: any 工具 registered before
// initialization completes must 是 visible once WaitForInit returns.
// interactive coordinator deliberately no longer relies 在 这 (it reads
// registry ungated 和 picks up late tools 在 subsequent runs); 这 测试
// keeps guarantee 为 non-interactive runs, which still wait.
func TestWaitForInit_ToolsVisibleAfterInit(t *testing.T) {
	const name = "test-waitforinit-tools"
	t.Cleanup(func() {
		if s, ok := sessions.Take(name); ok {
			_ = s.Close()
		}
		allTools.Del(name)
		states.Del(name)
	})

	sess, _ := liveSession(t, "slow_tool")
	gate := swapInitGate(t)

	// 一个 slow MCP 服务端 registers 它的 tools, then initialization completes
	// ( gate closes). close(gate) happens-after registration, 和
	// WaitForInit returning happens-after observing close, 所以 tools 是
	// guaranteed visible once WaitForInit returns.
	go func() {
		sessions.Set(name, sess)
		allTools.Set(name, []*Tool{{Name: "slow_tool"}})
		updateState(name, StateConnected, nil, sess, Counts{Tools: 1})
		close(gate)
	}()

	require.NoError(t, WaitForInit(context.Background()))

	tools, ok := allTools.Get(name)
	require.True(t, ok, "a slow server's tools must be visible after WaitForInit returns")
	require.Len(t, tools, 1)
	require.Equal(t, "slow_tool", tools[0].Name)
}
