package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// postCurrentSession drives real current-会话 endpoint 为 clientID.
func (h *e2eHarness) postCurrentSession(t *testing.T, ctx context.Context, workspaceID, clientID, sessionID string) {
	t.Helper()
	body, err := json.Marshal(proto.CurrentSession{SessionID: sessionID})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.httpSrv.URL+"/v1/workspaces/"+workspaceID+"/current-session?client_id="+clientID,
		bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// nextClientSession returns next client_session 事件 在 evc 那
// 是 about clientID.
func nextClientSession(t *testing.T, ctx context.Context, evc <-chan any, clientID string) pubsub.Event[proto.ClientSession] {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ev, ok := drainUntil(ctx, evc, func(ev pubsub.Event[proto.ClientSession]) bool {
		return ev.Payload.ClientID == clientID
	})
	require.True(t, ok, "expected a client_session event")
	return ev
}

// nextClientSessionChange returns next 事件 那 moves clientID off
// prev. 一个 流 那 attached just before prev was chosen can 是 sent prev
// twice, live 和 again 通过 重放 在 attach, 所以 repeats 是 skipped.
func nextClientSessionChange(t *testing.T, ctx context.Context, evc <-chan any, clientID, prev string) pubsub.Event[proto.ClientSession] {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ev, ok := drainUntil(ctx, evc, func(ev pubsub.Event[proto.ClientSession]) bool {
		return ev.Payload.ClientID == clientID && ev.Payload.SessionID != prev
	})
	require.True(t, ok, "expected a client_session event moving off %q", prev)
	return ev
}

// TestE2E_ClientSessionBroadcast verifies 那 客户端's 会话 change
// reaches every other 客户端's 流, 和 那 cleared 会话 是 sent
// 作为 empty 会话.
func TestE2E_ClientSessionBroadcast(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	ctx := t.Context()

	tui, watcher := uuid.New().String(), uuid.New().String()
	_, cancelTUI := h.subscribeSSE(t, ctx, h.workspace.ID, tui)
	t.Cleanup(cancelTUI)
	watchEvents, cancelWatch := h.subscribeSSE(t, ctx, h.workspace.ID, watcher)
	t.Cleanup(cancelWatch)
	h.waitForAttached(t, 2)

	h.postCurrentSession(t, ctx, h.workspace.ID, tui, "S1")
	ev := nextClientSession(t, ctx, watchEvents, tui)
	require.Equal(t, pubsub.UpdatedEvent, ev.Type)
	require.Equal(t, "S1", ev.Payload.SessionID)

	h.postCurrentSession(t, ctx, h.workspace.ID, tui, "S2")
	require.Equal(t, "S2", nextClientSessionChange(t, ctx, watchEvents, tui, "S1").Payload.SessionID)

	h.postCurrentSession(t, ctx, h.workspace.ID, tui, "")
	require.Empty(t, nextClientSessionChange(t, ctx, watchEvents, tui, "S2").Payload.SessionID)
}

// TestE2E_ClientSessionReplayedOnAttach verifies 那 客户端 attaching
// after another one chose 会话 是 told which 会话 it 是.
func TestE2E_ClientSessionReplayedOnAttach(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	ctx := t.Context()

	tui := uuid.New().String()
	_, cancelTUI := h.subscribeSSE(t, ctx, h.workspace.ID, tui)
	t.Cleanup(cancelTUI)
	h.waitForAttached(t, 1)
	h.postCurrentSession(t, ctx, h.workspace.ID, tui, "S1")

	late, cancelLate := h.subscribeSSE(t, ctx, h.workspace.ID, uuid.New().String())
	t.Cleanup(cancelLate)
	ev := nextClientSession(t, ctx, late, tui)
	require.Equal(t, "S1", ev.Payload.SessionID)
}

// TestE2E_ClientSessionClearedWhenTheClientLeaves verifies 那 other
// clients 是 told 当 客户端 那 views 会话 goes away.
func TestE2E_ClientSessionClearedWhenTheClientLeaves(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	ctx := t.Context()

	tui := uuid.New().String()
	_, cancelTUI := h.subscribeSSE(t, ctx, h.workspace.ID, tui)
	t.Cleanup(cancelTUI)
	watch, cancelWatch := h.subscribeSSE(t, ctx, h.workspace.ID, uuid.New().String())
	t.Cleanup(cancelWatch)
	h.waitForAttached(t, 2)
	h.postCurrentSession(t, ctx, h.workspace.ID, tui, "S1")
	require.Equal(t, "S1", nextClientSession(t, ctx, watch, tui).Payload.SessionID)

	require.NoError(t, h.backend.RetireClient(tui)) // a clean exit gives up the claim at once
	cancelTUI()
	require.Empty(t, nextClientSessionChange(t, ctx, watch, tui, "S1").Payload.SessionID)
}
