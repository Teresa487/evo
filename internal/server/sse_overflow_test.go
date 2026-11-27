package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

// stalledWriter 是 ResponseWriter whose writes block until resume 是
// closed, like 客户端 那 stopped reading 它的 流.
type stalledWriter struct {
	header http.Header
	resume <-chan struct{}
}

func (w *stalledWriter) Header() http.Header { return w.header }
func (w *stalledWriter) WriteHeader(int)     {}
func (w *stalledWriter) Write(p []byte) (int, error) {
	<-w.resume
	return len(p), nil
}

// TestE2E_StalledStreamClosedOnOverflow guards against keeping 客户端's
// 事件 流 open 用 silent gap after events were dropped 为 it.
func TestE2E_StalledStreamClosedOnOverflow(t *testing.T) {
	t.Parallel()
	h := newE2EHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	resume := make(chan struct{})
	served := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-resume:
		default:
			close(resume)
		}
		<-served
	})

	cid := uuid.New().String()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet,
		"/v1/workspaces/"+h.workspace.ID+"/events?client_id="+cid, nil)
	req.Host = "localhost"
	go func() {
		defer close(served)
		h.srv.Handler().ServeHTTP(&stalledWriter{header: http.Header{}, resume: resume}, req)
	}()
	h.waitForAttached(t, 1)
	h.setCurrentSession(t, ctx, h.workspace.ID, cid, "s-overflow")

	// Overflow pubsub's 4096-事件 缓冲 为 stalled 订阅者. Each
	// 事件 updates different 消息, 所以 总线 cannot coalesce them.
	for i := range 4096 + 64 {
		h.app.SendEvent(pubsub.Event[message.Message]{
			Type:    pubsub.UpdatedEvent,
			Payload: message.Message{ID: fmt.Sprintf("m-%d", i), SessionID: "s-overflow", Role: message.Assistant},
		})
	}
	close(resume)
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream of a client that fell behind was left open")
	}
}
