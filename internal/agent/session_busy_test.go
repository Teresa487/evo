package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func newBusyTestCoordinator(main SessionAgent) *coordinator {
	return &coordinator{
		mainAgent:     main,
		mainAgentName: "coder",
		agents:        map[string]SessionAgent{"coder": main},
	}
}

// 一个 提示词 是 accepted before 它的 运行 registers 作为 活跃. Anything asking
// whether 会话 may 是 modified must 是 told it 是 busy 在 那 窗口,
// 或 it acts 在 会话 那 是 about 到 运行.
func TestHasPendingWork_CoversAcceptedButNotYetActiveRuns(t *testing.T) {
	t.Parallel()
	sa, _ := newCancelTestAgent(t)

	require.False(t, sa.HasPendingWork("sid"), "an untouched session is idle")

	accept := sa.BeginAccepted("sid")
	require.True(t, sa.HasPendingWork("sid"),
		"a session with an accepted run is busy before the run starts")

	accept.Close()
	require.False(t, sa.HasPendingWork("sid"), "closing the last accept frees it")
}

// Sibling accepts must 不 free 会话 until last one closes.
func TestHasPendingWork_StaysBusyWhileAnyAcceptRemains(t *testing.T) {
	t.Parallel()
	sa, _ := newCancelTestAgent(t)

	first := sa.BeginAccepted("sid")
	second := sa.BeginAccepted("sid")

	first.Close()
	require.True(t, sa.HasPendingWork("sid"), "one accept still outstanding")

	second.Close()
	require.False(t, sa.HasPendingWork("sid"))
}

// 运行 holds 它的 own 受理 while asking whether 到 队列, 所以 HasActiveTurn
// must 不 count accepts 或 运行 would 队列 behind itself.
func TestHasActiveTurn_IgnoresTheCallersOwnAccept(t *testing.T) {
	t.Parallel()
	sa, _ := newCancelTestAgent(t)

	accept := sa.BeginAccepted("sid")
	t.Cleanup(accept.Close)

	require.False(t, sa.HasActiveTurn("sid"),
		"an accept alone is not an active turn")
	require.True(t, sa.HasPendingWork("sid"),
		"but observers must still see work in flight")
}

// Summarize rewrites transcript, 所以 accepted 运行 about 到 start
// must block it just like 活跃 one.
func TestSummarize_RefusesWhileARunIsAccepted(t *testing.T) {
	t.Parallel()
	sa, _ := newCancelTestAgent(t)

	accept := sa.BeginAccepted("sid")
	t.Cleanup(accept.Close)

	err := sa.Summarize(t.Context(), "sid", sa.Model(), nil, nil)
	require.ErrorIs(t, err, ErrSessionBusy)
}

// Shutdown 和 agent switching key off IsBusy, 所以 accepted 运行 counts.
func TestIsBusy_CoversAcceptedRuns(t *testing.T) {
	t.Parallel()
	sa, _ := newCancelTestAgent(t)

	require.False(t, sa.IsBusy())
	accept := sa.BeginAccepted("sid")
	require.True(t, sa.IsBusy())
	accept.Close()
	require.False(t, sa.IsBusy())
}

// 一个 sub-agent runs 在 它的 own agent instance, 所以 main agent knows
// nothing 的 它的 会话. trackSubSession registers it 作为 live.
func TestCoordinatorIsSessionBusy_LiveSubSession(t *testing.T) {
	t.Parallel()
	main, _ := newCancelTestAgent(t)
	coord := newBusyTestCoordinator(main)

	require.False(t, coord.IsSessionBusy("child"))

	forget := coord.trackSubSession("child")
	require.True(t, coord.IsSessionBusy("child"),
		"a live sub-session is busy even when no parent run is registered")
	require.False(t, coord.IsSessionBusy("other"))

	forget()
	require.False(t, coord.IsSessionBusy("child"))
}

// Top-level sessions fall through 到 agent, accepts included.
func TestCoordinatorIsSessionBusy_TopLevelSession(t *testing.T) {
	t.Parallel()
	main, _ := newCancelTestAgent(t)
	coord := newBusyTestCoordinator(main)

	require.False(t, coord.IsSessionBusy("solo"))
	accept := main.BeginAccepted("solo")
	t.Cleanup(accept.Close)
	require.True(t, coord.IsSessionBusy("solo"))
}

// Resolving 会话 must never 读取 数据库: it runs per 会话
// 在 list endpoints 和 在 keystrokes.
func TestCoordinatorIsSessionBusy_NeedsNoSessionService(t *testing.T) {
	t.Parallel()
	main, _ := newCancelTestAgent(t)
	coord := newBusyTestCoordinator(main)
	require.Nil(t, coord.sessions, "no session service wired")

	forget := coord.trackSubSession("child")
	defer forget()
	require.True(t, coord.IsSessionBusy("child"))
	require.False(t, coord.IsSessionBusy("unrelated"))
}
