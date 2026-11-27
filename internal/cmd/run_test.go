package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// runServer 是 fake 服务端 为 runNonInteractive 在 工作区 "w" 和
// 会话 "s". 它 records other POSTs 客户端 sends.
type runServer struct {
	sent chan struct{} // Closed once POST /agent has the prompt.
	mu   sync.Mutex
	reqs []string
}

func (s *runServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reqs...)
}

// newRunServer starts a runServer. With holdSend, POST /agent never
// answers. Once 提示词 是 accepted, 流 (如果 set) writes 事件
// 流; it 是 otherwise held open.
func newRunServer(t *testing.T, holdSend bool, stream func(http.ResponseWriter, *http.Request, string)) (*client.Client, *runServer) {
	t.Helper()
	rs := &runServer{sent: make(chan struct{})}
	accepted := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/workspaces/w/agent", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(proto.AgentInfo{IsReady: true})
	})
	mux.HandleFunc("POST /v1/workspaces/w/agent/update", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("POST /v1/workspaces/w/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(proto.Session{ID: "s"})
	})
	mux.HandleFunc("POST /", func(_ http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		rs.reqs = append(rs.reqs, strings.TrimPrefix(r.URL.Path, "/v1/workspaces/w/"))
	})
	mux.HandleFunc("POST /v1/workspaces/w/agent", func(w http.ResponseWriter, r *http.Request) {
		var msg proto.AgentMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if holdSend {
			close(rs.sent)
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusAccepted)
		w.(http.Flusher).Flush()
		accepted <- msg.RunID
		close(rs.sent)
	})
	mux.HandleFunc("GET /v1/workspaces/w/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case runID := <-accepted:
			if stream != nil {
				stream(w, r, runID)
				return
			}
		case <-r.Context().Done():
		}
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)
	return c, rs
}

func runAgainst(ctx context.Context, c *client.Client) error {
	ws := &proto.Workspace{ID: "w", Config: &config.Config{Options: &config.Options{}}}
	return runNonInteractive(ctx, c, ws, "hi", "", "", "", true, "", false)
}

// runComplete writes rc 作为 运行's RunComplete 事件.
func runComplete(rc proto.RunComplete) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, runID string) {
		rc.SessionID, rc.RunID = "s", runID
		inner, _ := json.Marshal(pubsub.Event[proto.RunComplete]{Type: pubsub.UpdatedEvent, Payload: rc})
		data, _ := json.Marshal(pubsub.Payload{Type: pubsub.PayloadTypeRunComplete, Payload: inner})
		fmt.Fprintf(w, "data: %s\n\n", data)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
}

// TestRunNonInteractive_RunOutcome guards `crush 运行` exiting 0 当
// 服务端 dropped 或 cancelled 运行, 和 checks 那 every 运行 asks
// 服务端 到 auto-approve 它的 会话 (it hung 在 权限 prompts) 和
// reports it 作为 会话 it views, 所以 运行 是 不 cancelled 当
// another 客户端 viewing it leaves.
func TestRunNonInteractive_RunOutcome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		stream  func(http.ResponseWriter, *http.Request, string)
		wantErr string
	}{
		{name: "completed", stream: runComplete(proto.RunComplete{Text: "answer"})},
		{name: "stream ends", stream: func(http.ResponseWriter, *http.Request, string) {}, wantErr: "lost connection"},
		{name: "cancelled on server", stream: runComplete(proto.RunComplete{
			Text: "partial", Error: "context canceled", Cancelled: true,
		}), wantErr: "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, srv := newRunServer(t, false, tc.stream)
			err := runAgainst(t.Context(), c)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
			require.Equal(t, []string{"sessions/s/auto-approve", "current-session"}, srv.requests())
		})
	}
}

// TestRunNonInteractive_InterruptCancelsServerRun guards Ctrl+C 在 `crush
// 运行` leaving 它的 agent 运行 going 在 服务端.
func TestRunNonInteractive_InterruptCancelsServerRun(t *testing.T) {
	t.Parallel()

	for _, holdSend := range []bool{false, true} {
		t.Run(fmt.Sprintf("holdSend=%v", holdSend), func(t *testing.T) {
			t.Parallel()
			c, srv := newRunServer(t, holdSend, nil)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			errc := make(chan error, 1)
			go func() { errc <- runAgainst(ctx, c) }()

			<-srv.sent
			cancel()
			select {
			case <-errc:
			case <-time.After(10 * time.Second):
				t.Fatal("runNonInteractive did not return after the interrupt")
			}
			require.Contains(t, srv.requests(), "agent/sessions/s/cancel")
		})
	}
}
