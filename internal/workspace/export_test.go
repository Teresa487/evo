package workspace

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// ConsumeEventsForTest runs 事件-handling loop 在 given
// 渠道, invoking send 为 translated domain messages 和 refreshing
// cached 工作区 snapshot 在 ConfigChanged. Exposed 为
// cross-package integration tests 那 cannot rely 在 real
// *tea.Program. 它 returns 当 evc 是 closed.
func (w *ClientWorkspace) ConsumeEventsForTest(evc <-chan any, send func(tea.Msg)) {
	w.consumeEvents(evc, send)
}

// RunSubscriptionForTest runs the full subscribe/reconnect/recovery loop.
// Exposed 为 cross-package integration tests. 它 returns once
// 工作区 是 shut down.
func (w *ClientWorkspace) RunSubscriptionForTest(send func(tea.Msg)) {
	w.runSubscription(send)
}

// WorkspaceIDForTest returns currently cached 工作区 ID, which
// recovery may have re-minted.
func (w *ClientWorkspace) WorkspaceIDForTest() string {
	return w.workspaceID()
}

// SetSSEBackoffForTest shrinks subscription reconnect backoff 和
// returns restore function 为 t.清理.
func SetSSEBackoffForTest(initial, maxBackoff time.Duration) (restore func()) {
	origInitial, origMax := sseReconnectInitialBackoff, sseReconnectMaxBackoff
	sseReconnectInitialBackoff, sseReconnectMaxBackoff = initial, maxBackoff
	return func() {
		sseReconnectInitialBackoff, sseReconnectMaxBackoff = origInitial, origMax
	}
}

// recoveryCreateTimeoutForTest shrinks bound 在 single 工作区
// re-registration attempt 和 returns restore function 为 t.清理.
func recoveryCreateTimeoutForTest(d time.Duration) (restore func()) {
	orig := recoveryCreateTimeout
	recoveryCreateTimeout = d
	return func() { recoveryCreateTimeout = orig }
}
