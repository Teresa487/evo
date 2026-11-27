package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/stretchr/testify/require"
)

// newPendingUI builds UI 在 会话 s1 用 caches warmed 所以 只
// paths under 测试 can move pending spinner.
func newPendingUI(t *testing.T) *UI {
	t.Helper()
	pinTTLs(t)
	m := newBusyUI(&countingWorkspace{ready: true})
	warmCaches(m, false)
	m.session = &session.Session{ID: "s1"}
	t.Cleanup(func() { common.StopTurn("s1") })
	return m
}

func hasPending(m *UI) bool {
	return m.chat.MessageItem(chat.PendingAssistantID) != nil
}

// transcriptIDs lists ids 的 items 在 view, top 到 bottom.
func transcriptIDs(m *UI) []string {
	ids := make([]string, 0, m.chat.list.Len())
	for i := range m.chat.list.Len() {
		if item, ok := m.chat.list.ItemAt(i).(chat.MessageItem); ok {
			ids = append(ids, item.ID())
		}
	}
	return ids
}

// userMessage builds 存储 提示词 agent publishes back.
func userMessage(id, sessionID, text string) message.Message {
	return message.Message{
		ID:        id,
		Role:      message.User,
		SessionID: sessionID,
		Parts:     []message.ContentPart{message.TextContent{Text: text}},
	}
}

// 提示词 shows up moment it 是 sent, above 它的 spinner, rather than
// waiting 为 agent 到 存储 it 和 发布 it back.
func TestSendEchoesThePromptAboveTheSpinner(t *testing.T) {
	m := newPendingUI(t)

	m.sendMessage("hello")

	require.Equal(t, []string{chat.PendingUserID, chat.PendingAssistantID}, transcriptIDs(m))
}

// 存储 提示词 takes echo's place, 和 spinner stays 在
// bottom instead 的 hopping over arriving 消息.
func TestStoredPromptReplacesTheEcho(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("hello")

	m.appendSessionMessage(userMessage("u1", "s1", "hello"))

	require.Equal(t, []string{"u1", chat.PendingAssistantID}, transcriptIDs(m))
}

// load 和 事件 can arrive 在 either order; 提示词 shows once.
func TestLoadRacingTheStoredPromptShowsItOnce(t *testing.T) {
	stored := userMessage("u1", "s1", "hello")

	t.Run("load first", func(t *testing.T) {
		m := newPendingUI(t)
		m.sendMessage("hello")

		m.setSessionMessages([]message.Message{stored})
		require.Equal(t, []string{"u1", chat.PendingAssistantID}, transcriptIDs(m))

		m.appendSessionMessage(stored)
		require.Equal(t, []string{"u1", chat.PendingAssistantID}, transcriptIDs(m))
	})

	t.Run("event first", func(t *testing.T) {
		m := newPendingUI(t)
		m.sendMessage("hello")

		m.appendSessionMessage(stored)
		m.setSessionMessages([]message.Message{stored})
		require.Equal(t, []string{"u1", chat.PendingAssistantID}, transcriptIDs(m))
	})
}

// 一个 load 那 lands before new 提示词 是 存储, showing 只
// history 那 was already 在 screen, must leave echo 在 place.
func TestLoadOfOlderHistoryKeepsTheEcho(t *testing.T) {
	m := newPendingUI(t)
	older := []message.Message{userMessage("u0", "s1", "earlier")}
	m.setSessionMessages(older)
	m.sendMessage("hello")

	m.setSessionMessages(older)

	require.Equal(t, []string{"u0", chat.PendingUserID, chat.PendingAssistantID}, transcriptIDs(m))
}

// 一个 提示词 queued behind running turn belongs 在 队列 pill, 不 在
// transcript, 所以 it 是 不 echoed.
func TestQueuedPromptIsNotEchoed(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("first")
	m.appendSessionMessage(userMessage("u1", "s1", "first"))
	m.appendSessionMessage(message.Message{ID: "a1", Role: message.Assistant, SessionID: "s1"})

	m.sendMessage("second")

	require.Nil(t, m.chat.MessageItem(chat.PendingUserID))
}

// transcript falls silent while 工具 runs: 助手消息
// stops spinning once it carries 工具 calls, 和 工具调用 stops 作为
// soon 作为 模型 has finished writing it, well before 工具 returns.
// 一个 提示词 sent 在 那 窗口 是 still queued behind running turn, 所以
// it belongs 在 队列 pill rather than echoed into transcript 用
// a second spinner stacked under it.
func TestPromptSentDuringAToolRunIsNotEchoed(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("first")
	m.appendSessionMessage(userMessage("u1", "s1", "first"))
	m.applyBusyState(busyStateMsg{
		gen: m.busyFetchGen, ready: true, forSession: "s1", sessionBusy: true,
	})
	m.appendSessionMessage(message.Message{
		ID: "a1", Role: message.Assistant, SessionID: "s1",
		Parts: []message.ContentPart{message.ToolCall{
			ID: "tc1", Name: "bash", Input: "{}", Finished: true,
		}},
	})

	m.sendMessage("second")

	require.Nil(t, m.chat.MessageItem(chat.PendingUserID), "the queue owns it")
	require.False(t, hasPending(m), "no second spinner underneath the turn")
}

// 一个 运行 这 客户端 never sent ( 子代理 报告, 渠道 事件,
// 会话 opened mid-运行) has no pending turn 到 vouch 为 it. While 它的
// 工具 runs transcript 是 silent too, 所以 busy probe has 到 是 what
// keeps 提示词 sent then 在 队列 pill instead 的 echoed 用
// spinner 作为 though it had started turn 的 它的 own.
func TestPromptSentDuringAnUnsentRunIsNotEchoed(t *testing.T) {
	m := newPendingUI(t)
	m.appendSessionMessage(userMessage("u1", "s1", "report"))
	m.appendSessionMessage(message.Message{
		ID: "a1", Role: message.Assistant, SessionID: "s1",
		Parts: []message.ContentPart{message.ToolCall{
			ID: "tc1", Name: "bash", Input: "{}", Finished: true,
		}},
	})
	m.applyBusyState(busyStateMsg{
		gen: m.busyFetchGen, ready: true, forSession: "s1", sessionBusy: true,
	})

	m.sendMessage("keep going")

	require.Nil(t, m.chat.MessageItem(chat.PendingUserID), "the queue owns it")
	require.False(t, hasPending(m), "no spinner for a queued prompt")
}

// 一个 generated continuation 是 hidden 从 transcript, 所以 it gets
// spinner 但 no echo.
func TestHiddenPromptIsNotEchoed(t *testing.T) {
	m := newPendingUI(t)

	m.sendMessageInternal("Implement the plan.", true)

	require.True(t, hasPending(m))
	require.Nil(t, m.chat.MessageItem(chat.PendingUserID))
}

// 一个 turn 那 ends before 它的 提示词 was 存储 takes echo 用 it:
// nothing was persisted, 所以 nothing should 是 left behind.
func TestEndedTurnDropsTheEcho(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("hello")

	m.endPendingTurn("s1")

	require.Empty(t, transcriptIDs(m))
}

// Sending shows spinner straight away rather than waiting 为
// 助手消息, 和 clock runs 从 提示词.
func TestPendingSpinnerAppearsOnSend(t *testing.T) {
	m := newPendingUI(t)

	m.sendMessage("hello")

	require.True(t, hasPending(m))
	require.NotEmpty(t, common.Elapsed("s1"), "the turn clock runs from the prompt")
}

// 助手消息 owns spinner once it exists, 所以
// placeholder steps aside rather than doubling up.
func TestAssistantMessageRetiresPendingSpinner(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("hello")
	require.True(t, hasPending(m))

	m.appendSessionMessage(message.Message{
		ID: "a1", Role: message.Assistant, SessionID: "s1",
	})

	require.False(t, hasPending(m))
}

// An 助手消息 belonging 到 another 会话 must 不 retire 这
// 会话's spinner.
func TestOtherSessionAssistantLeavesPendingSpinner(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("hello")

	m.appendSessionMessage(message.Message{
		ID: "a1", Role: message.Assistant, SessionID: "other",
	})

	require.True(t, hasPending(m))
}

// 一个 提示词 sent while turn 是 already spinning 是 queued behind it.
// running turn owns spinner, 所以 no second one 是 stacked underneath 和
// elapsed time keeps counting 从 turn 在 flight.
func TestQueuedPromptDoesNotStackASecondSpinner(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("first")
	started := common.Elapsed("s1")
	m.appendSessionMessage(message.Message{
		ID: "a1", Role: message.Assistant, SessionID: "s1",
	})

	m.sendMessage("second")

	require.False(t, hasPending(m), "the running turn keeps the only spinner")
	require.NotEmpty(t, started)
	require.NotEmpty(t, common.Elapsed("s1"), "the clock was not restarted")
}

// Sending twice must never leave two placeholders: second would 是
// unreachable through id map 和 would spin until 会话 reload.
func TestPendingSpinnerIsNotDuplicated(t *testing.T) {
	m := newPendingUI(t)

	m.sendMessage("hello")
	m.syncPendingItem()
	m.syncPendingItem()

	var count int
	for i := range m.chat.list.Len() {
		if item, ok := m.chat.list.ItemAt(i).(chat.MessageItem); ok &&
			item.ID() == chat.PendingAssistantID {
			count++
		}
	}
	require.Equal(t, 1, count)
}

// runComplete delivers agent's 终态 事件 为 sessionID.
func runComplete(m *UI, sessionID string, cancelled bool) {
	m.Update(pubsub.Event[notify.RunComplete]{
		Payload: notify.RunComplete{SessionID: sessionID, Cancelled: cancelled},
	})
}

// clock stops 用 运行, even after 助手消息 has taken
// the spinner over.
func TestRunCompleteStopsTheClockAfterHandoff(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("hello")
	m.appendSessionMessage(message.Message{ID: "a1", Role: message.Assistant, SessionID: "s1"})
	require.False(t, hasPending(m), "the assistant message carries the spinner")

	runComplete(m, "s1", false)

	require.Empty(t, common.Elapsed("s1"))
}

// 一个 turn 那 ends before producing 助手消息 retires 它的
// spinner 和 它的 echo.
func TestRunCompleteRetiresPendingSpinner(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("hello")

	runComplete(m, "s1", false)

	require.Empty(t, transcriptIDs(m))
	require.Empty(t, common.Elapsed("s1"))
}

// Busy probes say nothing about 当 turn ends, 所以 空闲 one landing
// before 运行 registers must 不 retire spinner send placed.
func TestIdleProbeKeepsSpinner(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("hello")

	m.applyBusyState(busyStateMsg{
		gen: m.busyFetchGen, ready: true, forSession: "s1", sessionBusy: false,
	})

	require.True(t, hasPending(m))
}

// Another 会话's 运行 ending must 不 retire 这 会话's spinner.
func TestOtherSessionRunCompleteKeepsSpinner(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("hello")

	runComplete(m, "other", false)

	require.True(t, hasPending(m))
	require.NotEmpty(t, common.Elapsed("s1"))
}

// 一个 取消 before any busy probe has seen 运行 still ends turn,
// because agent reports cancelled 运行 like any other.
func TestEarlyCancelRetiresPendingSpinner(t *testing.T) {
	pinTTLs(t)
	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false)
	m.session = &session.Session{ID: "s1"}
	t.Cleanup(func() { common.StopTurn("s1") })

	m.sendMessage("hello")
	m.cancelAgent()
	m.cancelAgent()
	require.Equal(t, 1, ws.cancelCalls, "the second escape cancels")

	runComplete(m, "s1", true)

	require.False(t, hasPending(m), "a cancelled turn must not leave a spinner")
	require.Empty(t, common.Elapsed("s1"))
}

// An 助手消息 left unfinished 通过 killed 运行 still reports
// spinning, 但 frozen. 它 是 不 turn 在 flight, 所以 提示词 sent into
// 那 会话 starts 它的 own.
func TestGhostSpinnerDoesNotSwallowTheTurn(t *testing.T) {
	m := newPendingUI(t)
	m.setSessionMessages([]message.Message{
		userMessage("u1", "s1", "earlier"),
		{
			ID: "a1", Role: message.Assistant, SessionID: "s1",
			Parts: []message.ContentPart{message.ReasoningContent{Thinking: "hmm"}},
		},
	})

	m.sendMessage("hello")

	require.True(t, hasPending(m))
	require.NotNil(t, m.chat.MessageItem(chat.PendingUserID))
}

// Loading 会话 rebuilds transcript 从 存储 messages, which can
// never contain placeholder. 一个 会话 still waiting 在 turn gets it
// back, 所以 tabbing away 和 returning does 不 lose spinner.
func TestSessionLoadRestoresPendingSpinner(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("hello")
	require.True(t, hasPending(m))

	m.setSessionMessages(nil)

	require.True(t, hasPending(m), "same session: spinner comes back")

	m.session = &session.Session{ID: "other"}
	m.setSessionMessages(nil)

	require.False(t, hasPending(m), "a different session has no pending turn")
}

// 一个 ghost spinner left 在 earlier turn 通过 killed 运行 是 不 turn 在
// flight. Counting it would treat every new 提示词 在 long 会话 作为
// queued, holding back 它的 echo 和 spinner until agent publishes them.
func TestGhostSpinnerInAnEarlierTurnDoesNotQueueThePrompt(t *testing.T) {
	m := newPendingUI(t)
	m.setSessionMessages([]message.Message{
		userMessage("u0", "s1", "earlier"),
		{ID: "a0", Role: message.Assistant, SessionID: "s1"},
		userMessage("u1", "s1", "later"),
	})
	m.animClock.frozen = false

	m.sendMessage("hello")

	require.NotNil(t, m.chat.MessageItem(chat.PendingUserID), "the prompt is echoed")
	require.True(t, hasPending(m), "the spinner shows straight away")
}

// busy probe taken mid-turn goes stale moment 运行 ends. 一个 提示词
// sent before the next probe lands starts a turn rather than queueing.
func TestEndedRunClearsTheBusyProbe(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("first")
	m.appendSessionMessage(userMessage("u1", "s1", "first"))
	m.applyBusyState(busyStateMsg{
		gen: m.busyFetchGen, ready: true, forSession: "s1", sessionBusy: true,
	})
	runComplete(m, "s1", false)

	m.sendMessage("second")

	require.NotNil(t, m.chat.MessageItem(chat.PendingUserID), "the prompt is echoed")
	require.True(t, hasPending(m), "the spinner shows straight away")
}

// 一个 probe sent mid-turn can land after 运行 ends. Its busy answer 是
// stale, 所以 it must 不 put next 提示词 back 在 队列.
func TestProbeInFlightAtRunEndIsDiscarded(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("first")
	m.appendSessionMessage(userMessage("u1", "s1", "first"))
	inFlight := m.busyFetchGen
	runComplete(m, "s1", false)
	m.applyBusyState(busyStateMsg{
		gen: inFlight, ready: true, forSession: "s1", sessionBusy: true,
	})

	m.sendMessage("second")

	require.NotNil(t, m.chat.MessageItem(chat.PendingUserID), "the prompt is echoed")
	require.True(t, hasPending(m), "the spinner shows straight away")
}
