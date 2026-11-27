package workspace

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/stretchr/testify/require"
)

// TestProtoToMessageToolResult ensures 那 ToolResult metadata,
// data, 和 MIME type survive conversion 从 proto 在
// 客户端. Without these fields TUI cannot render rich 工具
// output (e.g. syntax-highlighted code 从 view, diffs 从 edit,
// images, etc.) 和 falls back 到 raw LLM-facing string.
func TestProtoToMessageToolResult(t *testing.T) {
	t.Parallel()

	src := proto.Message{
		ID:   "m1",
		Role: proto.Tool,
		Parts: []proto.ContentPart{
			proto.ToolResult{
				ToolCallID: "call-1",
				Name:       "view",
				Content:    "<file>\n  1| hi\n</file>",
				Data:       "base64data",
				MIMEType:   "image/png",
				Metadata:   `{"file_path":"/tmp/x","content":"hi"}`,
				IsError:    false,
			},
		},
	}

	got := protoToMessage(src)
	require.Len(t, got.Parts, 1)
	tr, ok := got.Parts[0].(message.ToolResult)
	require.True(t, ok, "expected message.ToolResult, got %T", got.Parts[0])
	require.Equal(t, "call-1", tr.ToolCallID)
	require.Equal(t, "view", tr.Name)
	require.Equal(t, "<file>\n  1| hi\n</file>", tr.Content)
	require.Equal(t, "base64data", tr.Data)
	require.Equal(t, "image/png", tr.MIMEType)
	require.Equal(t, `{"file_path":"/tmp/x","content":"hi"}`, tr.Metadata)
	require.False(t, tr.IsError)
}

// TestProtoToMessagePrismModel ensures Prism-routed 模型 fields survive
// conversion 从 proto. Without them 客户端 TUI cannot show which
// 模型 actually served each turn 在 Hyper's 模型 router.
func TestProtoToMessagePrismModel(t *testing.T) {
	t.Parallel()

	src := proto.Message{
		ID:             "m1",
		Role:           proto.Assistant,
		Model:          "prism-model",
		Provider:       "hyper",
		PrismModelID:   "prism-42",
		PrismModelName: "GLM 5.3",

		PrismHypercreditSavings: protoPtrFloat(1.5),
		PrismDollarSavings:      protoPtrFloat(0.002),
	}

	got := protoToMessage(src)
	require.Equal(t, "prism-42", got.PrismModelID)
	require.Equal(t, "GLM 5.3", got.PrismModelName)
	require.NotNil(t, got.PrismHypercreditSavings)
	require.Equal(t, 1.5, *got.PrismHypercreditSavings)
	require.NotNil(t, got.PrismDollarSavings)
	require.Equal(t, 0.002, *got.PrismDollarSavings)
}

func protoPtrFloat(v float64) *float64 { return &v }

// TestClientWorkspace_PermissionGrantMapping verifies 那
// PermissionGrant 在 ClientWorkspace serializes one-time grant
// (proto.PermissionAllow) 和 PermissionGrantPersistent serializes
// persistent grant (proto.PermissionAllowForSession). 一个 swap between
// these two would silently flip "allow once" into "remember 为
// 会话", 和 vice versa, 所以 we pin wire mapping here.
func TestClientWorkspace_PermissionGrantMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		call func(*ClientWorkspace, permission.PermissionRequest)
		want proto.PermissionAction
	}{
		{
			name: "Grant -> PermissionAllow",
			call: func(w *ClientWorkspace, p permission.PermissionRequest) {
				w.PermissionGrant(p)
			},
			want: proto.PermissionAllow,
		},
		{
			name: "GrantPersistent -> PermissionAllowForSession",
			call: func(w *ClientWorkspace, p permission.PermissionRequest) {
				w.PermissionGrantPersistent(p)
			},
			want: proto.PermissionAllowForSession,
		},
		{
			name: "Deny -> PermissionDeny",
			call: func(w *ClientWorkspace, p permission.PermissionRequest) {
				w.PermissionDeny(p)
			},
			want: proto.PermissionDeny,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got proto.PermissionGrant
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "/v1/workspaces/ws-1/permissions/grant", r.URL.Path)
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &got))
				require.NoError(t, json.NewEncoder(w).Encode(proto.PermissionGrantResponse{Resolved: true}))
			}))
			defer srv.Close()

			u, err := url.Parse(srv.URL)
			require.NoError(t, err)
			c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
			require.NoError(t, err)

			ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})

			perm := permission.PermissionRequest{
				ID:          "req-1",
				SessionID:   "sess-1",
				ToolCallID:  "tc-1",
				ToolName:    "tool",
				Description: "do thing",
				Action:      "act",
				Path:        "/tmp/p",
				Params:      `{"repo":"charmbracelet/crush"}`,
			}
			tc.call(ws, perm)

			require.Equal(t, tc.want, got.Action)
			require.Equal(t, "req-1", got.Permission.ID)
			require.Equal(t, "sess-1", got.Permission.SessionID)
			require.Equal(t, "tc-1", got.Permission.ToolCallID)
			require.Equal(t, "tool", got.Permission.ToolName)
			require.Equal(t, "act", got.Permission.Action)
			require.Equal(t, "/tmp/p", got.Permission.Path)
			// Older servers reject MCP 工具's string params.
			require.Nil(t, got.Permission.Params)
		})
	}
}

// TestProtoToSkillStates verifies 那 wire representation 的 skill
// discovery states reconstructs identical values 在 客户端,
// including synthetic errors derived 从 错误 strings.
func TestProtoToSkillStates(t *testing.T) {
	t.Parallel()

	in := []proto.SkillState{
		{Name: "ok", Path: "/p/ok", State: proto.SkillStateNormal},
		{Name: "broken", Path: "/p/broken", State: proto.SkillStateError, Error: "bad frontmatter"},
	}

	got := protoToSkillStates(in)
	require.Len(t, got, 2)
	require.Equal(t, "ok", got[0].Name)
	require.Equal(t, skills.StateNormal, got[0].State)
	require.NoError(t, got[0].Err)
	require.Equal(t, "broken", got[1].Name)
	require.Equal(t, skills.StateError, got[1].State)
	require.EqualError(t, got[1].Err, "bad frontmatter")
}

// TestTranslateEvent_Skills verifies 那 incoming proto.SkillsEvent
// 是 converted into pubsub.事件[skills.事件] 和 那
// 客户端-process skill 缓存 是 updated 作为 side effect, 所以 callers
// reading skills.GetLatestStates see fresh data after each delta.
func TestTranslateEvent_Skills(t *testing.T) {
	// Not parallel - touches package-level skills 缓存 via
	// manager constructed 用 WithGlobalMirror.
	prev := skills.GetLatestStates()
	t.Cleanup(func() { skills.SetLatestStates(prev) })

	skills.SetLatestStates(nil)

	w := NewClientWorkspace(nil, proto.Workspace{})
	ev := pubsub.Event[proto.SkillsEvent]{
		Type: pubsub.UpdatedEvent,
		Payload: proto.SkillsEvent{
			States: []proto.SkillState{
				{Name: "from-server", Path: "/p", State: proto.SkillStateNormal},
			},
		},
	}

	out := w.translateEvent(ev)
	got, ok := out.(pubsub.Event[skills.Event])
	require.True(t, ok, "expected pubsub.Event[skills.Event], got %T", out)
	require.Len(t, got.Payload.States, 1)
	require.Equal(t, "from-server", got.Payload.States[0].Name)

	// Manager (用 WithGlobalMirror) propagated 到 package 缓存.
	cached := skills.GetLatestStates()
	require.Len(t, cached, 1)
	require.Equal(t, "from-server", cached[0].Name)
}

// TestTranslateEvent_MCPChannel verifies 客户端 reconstructs 渠道
// push 从 wire 用 它的 type 和 rendered <渠道> body intact, 所以
// TUI's 渠道 处理器 fires 在 客户端/服务端 mode.
func TestTranslateEvent_MCPChannel(t *testing.T) {
	t.Parallel()

	w := NewClientWorkspace(nil, proto.Workspace{})
	ev := pubsub.Event[proto.MCPEvent]{
		Type: pubsub.CreatedEvent,
		Payload: proto.MCPEvent{
			Type:           proto.MCPEventChannelMessage,
			Name:           "webhook",
			ChannelMessage: `<channel source="webhook">build failed</channel>`,
		},
	}

	out := w.translateEvent(ev)
	got, ok := out.(pubsub.Event[mcp.Event])
	require.True(t, ok, "expected pubsub.Event[mcp.Event], got %T", out)
	require.Equal(t, mcp.EventChannelMessage, got.Payload.Type)
	require.Equal(t, "webhook", got.Payload.Name)
	require.Equal(t, `<channel source="webhook">build failed</channel>`, got.Payload.ChannelMessage)
}

// TestNewClientWorkspace_SeedsSkillsCache verifies 那 snapshot 在
// proto.工作区.Skills populates package-level 缓存 TUI
// reads 在 construction time, eliminating race between TUI startup
// 和 first SSE 事件.
func TestNewClientWorkspace_SeedsSkillsCache(t *testing.T) {
	// Not parallel - touches package-level skills 缓存.
	prev := skills.GetLatestStates()
	t.Cleanup(func() { skills.SetLatestStates(prev) })

	skills.SetLatestStates(nil)

	_ = NewClientWorkspace(nil, proto.Workspace{
		Skills: []proto.SkillState{
			{Name: "seeded", Path: "/p", State: proto.SkillStateNormal},
		},
	})

	got := skills.GetLatestStates()
	require.Len(t, got, 1)
	require.Equal(t, "seeded", got[0].Name)
}

// TestSessionConversionKeepsEstimatedUsage verifies 那 flag marking
// 会话's usage 作为 estimated survives trip 到 和 从 服务端, 所以
// TUI can 标记 numbers 作为 estimates.
func TestSessionConversionKeepsEstimatedUsage(t *testing.T) {
	t.Parallel()

	in := proto.Session{ID: "s1", PromptTokens: 10, EstimatedUsage: true}
	got := protoToSession(in)
	require.True(t, got.EstimatedUsage)
	require.True(t, sessionToProto(got).EstimatedUsage)
}

// TestTranslateEvent_HostTheme verifies 那 host theme events reach
// TUI 作为 HostThemeEvent: theme 为 更新, nil 为 clear.
func TestTranslateEvent_HostTheme(t *testing.T) {
	t.Parallel()

	w := NewClientWorkspace(nil, proto.Workspace{})
	theme := &proto.HostTheme{Name: "charmtone-dark", Palette: map[string]string{"primary": "#6B50FF"}}

	out := w.translateEvent(pubsub.Event[proto.HostThemeState]{
		Type:    pubsub.UpdatedEvent,
		Payload: proto.HostThemeState{WorkspaceID: "ws1", Theme: theme},
	})
	got, ok := out.(HostThemeEvent)
	require.True(t, ok, "expected HostThemeEvent, got %T", out)
	require.Same(t, theme, got.Theme)

	out = w.translateEvent(pubsub.Event[proto.HostThemeState]{
		Type:    pubsub.DeletedEvent,
		Payload: proto.HostThemeState{WorkspaceID: "ws1"},
	})
	got, ok = out.(HostThemeEvent)
	require.True(t, ok, "expected HostThemeEvent, got %T", out)
	require.Nil(t, got.Theme)
}

// TestTranslateEvent_ClientSessionIsQuiet verifies 那 events telling
// which 会话 each 客户端 views 是 dropped without warning:
// 服务端 sends them 到 every 客户端 whenever any 客户端 switches sessions.
func TestTranslateEvent_ClientSessionIsQuiet(t *testing.T) {
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	w := NewClientWorkspace(nil, proto.Workspace{})
	out := w.translateEvent(pubsub.Event[proto.ClientSession]{
		Type:    pubsub.UpdatedEvent,
		Payload: proto.ClientSession{ClientID: "c1", SessionID: "s1"},
	})
	require.Nil(t, out)
	require.Empty(t, logs.String())
}

func TestTranslateReauthenticateEvent(t *testing.T) {
	t.Parallel()

	event := pubsub.Event[proto.AgentEvent]{
		Type:    pubsub.CreatedEvent,
		Payload: proto.AgentEvent{Type: proto.AgentEventType(notify.TypeReAuthenticate), ProviderID: "example"},
	}
	w := NewClientWorkspace(nil, proto.Workspace{})
	out := w.translateEvent(event).(pubsub.Event[notify.Notification])
	require.Equal(t, notify.TypeReAuthenticate, out.Payload.Type)
	require.Equal(t, "example", out.Payload.ProviderID)
}

// TestTranslateEvent_UpdateAvailable verifies 那 incoming
// proto.UpdateAvailable 事件 是 converted back into
// app.UpdateAvailableMsg 那 TUI expects, 所以 客户端/服务端 mode
// shows same 更新 通知 作为 local mode.
func TestTranslateEvent_UpdateAvailable(t *testing.T) {
	t.Parallel()

	w := NewClientWorkspace(nil, proto.Workspace{})
	ev := pubsub.Event[proto.UpdateAvailable]{
		Type: pubsub.UpdatedEvent,
		Payload: proto.UpdateAvailable{
			CurrentVersion: "1.0.0",
			LatestVersion:  "1.1.0",
			IsDevelopment:  true,
		},
	}

	out := w.translateEvent(ev)
	got, ok := out.(app.UpdateAvailableMsg)
	require.True(t, ok, "expected app.UpdateAvailableMsg, got %T", out)
	require.Equal(t, "1.0.0", got.CurrentVersion)
	require.Equal(t, "1.1.0", got.LatestVersion)
	require.True(t, got.IsDevelopment)
}

func TestClientWorkspaceListMCPPrompts(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/workspaces/ws-1/mcp/prompts", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode([]proto.MCPPrompt{
			{
				ID:       "server:review",
				PromptID: "review",
				ClientID: "server",
				Arguments: []proto.MCPPromptArgument{
					{ID: "focus", Title: "Focus", Required: true},
				},
			},
		}))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)
	workspace := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})

	got, err := workspace.ListMCPPrompts(t.Context())
	require.NoError(t, err)
	require.Equal(t, []commands.MCPPrompt{
		{
			ID:       "server:review",
			PromptID: "review",
			ClientID: "server",
			Arguments: []commands.Argument{
				{ID: "focus", Title: "Focus", Required: true},
			},
		},
	}, got)
}

// 一个 '!' command 那 starts 会话 must tell 服务端 所以, 或
// 会话 是 never titled.
func TestClientWorkspaceRunShellCommandSendsFirstMessage(t *testing.T) {
	t.Parallel()

	got := make(chan proto.ShellCommandRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req proto.ShellCommandRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		got <- req
		_ = json.NewEncoder(w).Encode(proto.ShellCommandResponse{})
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)
	workspace := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})

	_, err = workspace.AgentRunShellCommand(t.Context(), "s1", "ls", 80, nil, true)
	require.NoError(t, err)
	require.Equal(t, proto.ShellCommandRequest{Command: "ls", TermWidth: 80, IsFirstMessage: true}, <-got)
}

func TestClientWorkspaceListMCPPromptsServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)
	workspace := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})

	_, err = workspace.ListMCPPrompts(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "status code 500")
}

// TestClientWorkspace_MCPGetStatesTimeout pins probe 期限: hung
// 服务端 must 不 wedge MCPGetStates (和 用 it UI's 在-flight
// 刷新 flag, freezing every later MCP 刷新) forever. fetch gives
// up 在 mcpStatesTimeout 和 reports nil states.
func TestClientWorkspace_MCPGetStatesTimeout(t *testing.T) {
	t.Parallel()

	// 处理器 never answers; it unblocks 当 客户端's 期限
	// cancels 请求.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	orig := mcpStatesTimeout
	mcpStatesTimeout = 50 * time.Millisecond
	t.Cleanup(func() { mcpStatesTimeout = orig })

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)
	workspace := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})

	start := time.Now()
	require.Nil(t, workspace.MCPGetStates())
	require.Less(t, time.Since(start), 5*time.Second,
		"the probe must give up at its deadline instead of hanging")
}

// TestClientWorkspace_ReconnectsOnStreamDrop verifies 那 事件
// subscription loop reconnects after SSE 流 drops instead 的
// leaving TUI permanently orphaned (which surfaced 作为 stuck
// "coder agent 是 offline"), 和 那 Shutdown stops loop.
func TestClientWorkspace_ReconnectsOnStreamDrop(t *testing.T) {
	// Shrink backoff 所以 several reconnects happen quickly.
	origInitial, origMax := sseReconnectInitialBackoff, sseReconnectMaxBackoff
	sseReconnectInitialBackoff = 5 * time.Millisecond
	sseReconnectMaxBackoff = 20 * time.Millisecond
	t.Cleanup(func() {
		sseReconnectInitialBackoff = origInitial
		sseReconnectMaxBackoff = origMax
	})

	var subscribes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/events") {
			// Any other bookkeeping 调用 (e.g. GetWorkspace) just
			// gets empty OK; 测试 只 cares about 流.
			w.WriteHeader(http.StatusOK)
			return
		}
		subscribes.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Drop 流 immediately 所以 客户端 must reconnect.
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})

	done := make(chan struct{})
	go func() {
		ws.runSubscription(func(tea.Msg) {})
		close(done)
	}()

	// loop must reconnect several times 作为 服务端 keeps
	// dropping 流.
	require.Eventually(t, func() bool { return subscribes.Load() >= 3 },
		2*time.Second, 5*time.Millisecond,
		"subscription loop should reconnect after the stream drops")

	// Shutdown cancels subscription 上下文; loop must return.
	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown")
	}
}

// TestClientWorkspace_SubscriptionStopsWhenServerDown verifies the
// reconnect loop does 不 spin forever after Shutdown even 当 it can
// never connect (服务端 unreachable).
func TestClientWorkspace_SubscriptionStopsWhenServerDown(t *testing.T) {
	origInitial, origMax := sseReconnectInitialBackoff, sseReconnectMaxBackoff
	sseReconnectInitialBackoff = 5 * time.Millisecond
	sseReconnectMaxBackoff = 20 * time.Millisecond
	t.Cleanup(func() {
		sseReconnectInitialBackoff = origInitial
		sseReconnectMaxBackoff = origMax
	})

	// Port 1 是 不 listening: SubscribeEvents fails 在 every attempt.
	c, err := client.NewClient(t.TempDir(), "tcp", "127.0.0.1:1")
	require.NoError(t, err)
	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})

	done := make(chan struct{})
	go func() {
		ws.runSubscription(func(tea.Msg) {})
		close(done)
	}()

	// Let it 重试 few times, then shut down.
	time.Sleep(30 * time.Millisecond)
	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown while server was down")
	}
}

// TestClientWorkspace_AgentReadyErr distinguishes 服务端 那 reports
// uninitialized agent 从 服务端 那 cannot 是 reached, 所以 UI
// can show actionable 消息 instead 的 blanket "agent offline".
func TestClientWorkspace_AgentReadyErr(t *testing.T) {
	t.Parallel()

	t.Run("ready", func(t *testing.T) {
		t.Parallel()
		ws := agentInfoWorkspace(t, proto.AgentInfo{IsReady: true})
		require.NoError(t, ws.AgentReadyErr())
		require.True(t, ws.AgentIsReady())
	})

	t.Run("not initialized", func(t *testing.T) {
		t.Parallel()
		ws := agentInfoWorkspace(t, proto.AgentInfo{IsReady: false})
		err := ws.AgentReadyErr()
		require.ErrorIs(t, err, ErrAgentNotInitialized)
		require.NotErrorIs(t, err, ErrServerUnreachable)
		require.False(t, ws.AgentIsReady())
	})

	t.Run("server unreachable", func(t *testing.T) {
		t.Parallel()
		c, err := client.NewClient(t.TempDir(), "tcp", "127.0.0.1:1")
		require.NoError(t, err)
		ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})
		readyErr := ws.AgentReadyErr()
		require.ErrorIs(t, readyErr, ErrServerUnreachable)
		require.NotErrorIs(t, readyErr, ErrAgentNotInitialized)
		require.False(t, ws.AgentIsReady())
	})
}

// TUI's busy probe reads shared main agent 从 agent info it
// already fetches 为 模型, 所以 following another 客户端's switch 到
// plan mode costs no extra 请求.
func TestClientWorkspace_AgentModelReportsMainAgent(t *testing.T) {
	t.Parallel()
	ws := agentInfoWorkspace(t, proto.AgentInfo{IsReady: true, MainAgent: config.AgentPlan})
	require.Equal(t, config.AgentPlan, ws.AgentModel().Agent)
}

// agentInfoWorkspace returns ClientWorkspace whose 服务端 answers
// agent-info endpoint 用 given info.
func agentInfoWorkspace(t *testing.T, info proto.AgentInfo) *ClientWorkspace {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/workspaces/ws-1/agent", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(info))
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)
	return NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})
}

// -- 工作区 recovery --
//
// bug these cover: reconnect loop retried SubscribeEvents 用
// 工作区 ID captured 在 startup, forever. Once 服务端 no
// longer knew 那 ID — because 工作区 was torn down under
// 客户端, 或 服务端 was replaced — every 重试 和 every other
// 请求 answered 404 permanently, 和 sibling 会话 kept
// 服务端 (和 它的 404s) alive.

// recoveryServer 是 scripted 服务端 为 recovery tests. 它 404s
// 事件 subscriptions 为 stale 工作区 IDs, mints new ID 在 创建,
// 和 records what 客户端 did.
type recoveryServer struct {
	mu sync.Mutex
	// liveID 是 只 工作区 ID whose 事件 流 是 served.
	liveID string
	// nextID names 工作区 next 创建 hands back.
	nextID string
	// createErr, 当 set, fails creates instead 的 serving them.
	createErr func() int
	// hangUpOnCreate drops 连接 mid-响应 所以 客户端
	// never learns ID 的 工作区 服务端 did register.
	hangUpOnCreate bool

	// hostTheme 是 host theme theme endpoint reports.
	hostTheme *proto.HostTheme
	// abortStreams kills each 事件 流 mid-响应, 作为 服务端
	// 那 dies does, instead 的 ending it cleanly.
	abortStreams bool
	// 配置 是 配置 creates 报告; configured one makes
	// recovery initialize the agent.
	config *config.Config

	creates      int
	createReqs   []proto.Workspace
	streams      int
	sessionPosts []string
	retired      []string
	deleted      []string
	// agentCalls logs agent calls 作为 "<调用> <id> [<agent>]".
	agentCalls []string
}

func (s *recoveryServer) start(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(s.handler(t))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)
	return c
}

// 处理器 serves scripted API, 所以 测试 can put it 在 any listener.
func (s *recoveryServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces":
			s.creates++
			var req proto.Workspace
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			s.createReqs = append(s.createReqs, req)
			if s.createErr != nil {
				http.Error(w, "no", s.createErr())
				return
			}
			if s.hangUpOnCreate {
				// Register 工作区, then hang up before 客户端
				// can 读取 ID. 这 是 case no amount 的 客户端
				// bookkeeping 可以 name.
				s.liveID = s.nextID
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, err := hj.Hijack()
					require.NoError(t, err)
					_ = conn.Close()
				}
				return
			}
			s.liveID = s.nextID
			require.NoError(t, json.NewEncoder(w).Encode(proto.Workspace{
				ID: s.liveID, Path: "/tmp/recover", Config: s.config,
			}))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/clients/"):
			s.retired = append(s.retired, strings.TrimPrefix(r.URL.Path, "/v1/clients/"))
			s.liveID = ""
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/workspaces/"):
			s.deleted = append(s.deleted, strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"))
		case strings.HasSuffix(r.URL.Path, "/current-session"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"), "/current-session")
			if id != s.liveID {
				http.Error(w, "workspace not found", http.StatusNotFound)
				return
			}
			var req proto.CurrentSession
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			s.sessionPosts = append(s.sessionPosts, req.SessionID)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/theme"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"), "/theme")
			if id != s.liveID {
				http.Error(w, "workspace not found", http.StatusNotFound)
				return
			}
			require.NoError(t, json.NewEncoder(w).Encode(proto.HostThemeState{
				WorkspaceID: id, Theme: s.hostTheme,
			}))
		case strings.Contains(r.URL.Path, "/agent/"):
			id, call, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"), "/")
			if id != s.liveID {
				http.Error(w, "workspace not found", http.StatusNotFound)
				return
			}
			var req proto.AgentSetMainRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			s.agentCalls = append(s.agentCalls, strings.TrimSpace(call+" "+id+" "+req.AgentID))
		case strings.HasSuffix(r.URL.Path, "/events"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"), "/events")
			if id != s.liveID {
				http.Error(w, "workspace not found", http.StatusNotFound)
				return
			}
			s.streams++
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if s.abortStreams {
				panic(http.ErrAbortHandler)
			}
			// Drop 流 所以 loop keeps cycling.
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
}

func (s *recoveryServer) snapshot(f func(*recoveryServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// connectionRecorder collects ConnectionEvents 订阅 loop
// reports 到 UI.
type connectionRecorder struct {
	mu     sync.Mutex
	events []ConnectionEvent
}

func (r *connectionRecorder) send(msg tea.Msg) {
	ev, ok := msg.(ConnectionEvent)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *connectionRecorder) states() []ConnectionState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ConnectionState, len(r.events))
	for i, ev := range r.events {
		out[i] = ev.State
	}
	return out
}

func (r *connectionRecorder) sawStuck() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.Stuck {
			return true
		}
	}
	return false
}

// TestRecreateArgs_KeepsPromptOverrides verifies 那 re-registering
// 工作区 服务端 lost asks 为 --提示词 overrides 客户端
// started 用, 所以 agent does 不 silently fall back 到 built-在
// prompts after 服务端 restart.
func TestRecreateArgs_KeepsPromptOverrides(t *testing.T) {
	t.Parallel()

	prompts := config.PromptPaths{"coder": "/tmp/coder.md.tpl"}
	ws := NewClientWorkspace(nil, proto.Workspace{ID: "ws-1", Path: "/tmp/p", Prompts: prompts})
	require.Equal(t, prompts, ws.recreateArgs().Prompts)
}

// TestClientWorkspace_RecoversFromWorkspaceGone 是 headline 客户端
// regression: 404 为 cached 工作区 ID must make 客户端
// re-register, adopt new ID, re-assert 会话 it was viewing,
// 和 tell UI 到 resync — 不 重试 dead ID forever.
func TestClientWorkspace_RecoversFromWorkspaceGone(t *testing.T) {
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 5*time.Millisecond))

	srv := &recoveryServer{liveID: "", nextID: "ws-2"}
	c := srv.start(t)

	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1", Path: "/tmp/recover"})
	// 客户端 was viewing 会话, 所以 recovery has 到 restore 那
	// selection: 服务端's presence 条目 died 用 工作区.
	ws.mu.Lock()
	ws.lastSession = "sess-1"
	ws.mu.Unlock()

	rec := &connectionRecorder{}
	done := make(chan struct{})
	go func() {
		ws.runSubscription(rec.send)
		close(done)
	}()

	require.Eventually(t, func() bool { return ws.workspaceID() == "ws-2" },
		3*time.Second, 5*time.Millisecond,
		"a 404 must make the client re-register instead of retrying a dead ID")
	require.Eventually(t, func() bool {
		var posts []string
		srv.snapshot(func(s *recoveryServer) { posts = append(posts, s.sessionPosts...) })
		return len(posts) > 0 && posts[0] == "sess-1"
	}, 3*time.Second, 5*time.Millisecond,
		"the recovered workspace must be told which session the client is on")

	require.Eventually(t, func() bool {
		states := rec.states()
		var degraded, recovered bool
		for _, s := range states {
			degraded = degraded || s == ConnectionDegraded
			recovered = recovered || s == ConnectionRecovered
		}
		return degraded && recovered
	}, 3*time.Second, 5*time.Millisecond,
		"the UI must be told to resync after the workspace was re-created")

	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown")
	}
}

// TestClientWorkspace_ReportsLandingScreenOnAttach covers 窗口 那
// opens 在 landing screen: it must tell 服务端 it views no 会话
// 作为 soon 作为 它的 事件 流 attaches. 服务端 sends 客户端 那 never
// reported 会话 权限 和 question prompts 的 every 会话.
func TestClientWorkspace_ReportsLandingScreenOnAttach(t *testing.T) {
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 5*time.Millisecond))

	srv := &recoveryServer{liveID: "ws-1"}
	c := srv.start(t)
	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1", Path: "/tmp/landing"})

	done := make(chan struct{})
	go func() {
		ws.runSubscription(func(tea.Msg) {})
		close(done)
	}()

	require.Eventually(t, func() bool {
		var posts []string
		srv.snapshot(func(s *recoveryServer) { posts = append(posts, s.sessionPosts...) })
		return len(posts) > 0 && posts[0] == ""
	}, 3*time.Second, 5*time.Millisecond,
		"the first attach must report the landing screen")

	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown")
	}
}

// TestClientWorkspace_ResyncsAfterPlainStreamDrop covers the quieter
// failure: 流 closes 和 very next subscribe succeeds. That
// used 到 是 treated 作为 non-事件, 所以 TUI silently showed state
// frozen 在 moment 的 drop — everything published while
// 客户端 was away 是 gone, 和 服务端 had dropped 它的 presence 条目.
func TestClientWorkspace_ResyncsAfterPlainStreamDrop(t *testing.T) {
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 5*time.Millisecond))

	srv := &recoveryServer{liveID: "ws-1"}
	c := srv.start(t)

	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1", Path: "/tmp/blip"})
	ws.mu.Lock()
	ws.lastSession = "sess-9"
	ws.mu.Unlock()

	rec := &connectionRecorder{}
	done := make(chan struct{})
	go func() {
		ws.runSubscription(rec.send)
		close(done)
	}()

	require.Eventually(t, func() bool {
		states := rec.states()
		var degraded, recovered bool
		for _, s := range states {
			degraded = degraded || s == ConnectionDegraded
			recovered = recovered || s == ConnectionRecovered
		}
		return degraded && recovered
	}, 3*time.Second, 5*time.Millisecond,
		"a reconnect that succeeds first try must still resync")

	var posts int
	var creates int
	srv.snapshot(func(s *recoveryServer) { posts, creates = len(s.sessionPosts), s.creates })
	require.Positive(t, posts, "the session selection must be re-asserted after the drop")
	require.Zero(t, creates, "a live workspace must not be re-created")
	require.Equal(t, "ws-1", ws.workspaceID())

	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown")
	}
}

// TestClientWorkspace_ResyncsHostThemeAfterReconnect covers 主机 theme
// cleared while 流 was down: attach 重放 只 carries
// theme 那 是 set, 所以 客户端 must re-读取 it after reconnect 和
// hand UI clear. Once 主机 sets theme again, next
// reconnect hands 那 over too.
func TestClientWorkspace_ResyncsHostThemeAfterReconnect(t *testing.T) {
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 5*time.Millisecond))

	srv := &recoveryServer{liveID: "ws-1"}
	c := srv.start(t)
	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1", Path: "/tmp/theme"})

	var (
		mu     sync.Mutex
		themes []*proto.HostTheme
	)
	send := func(msg tea.Msg) {
		if ev, ok := msg.(HostThemeEvent); ok {
			mu.Lock()
			themes = append(themes, ev.Theme)
			mu.Unlock()
		}
	}
	last := func() (*proto.HostTheme, int) {
		mu.Lock()
		defer mu.Unlock()
		if len(themes) == 0 {
			return nil, 0
		}
		return themes[len(themes)-1], len(themes)
	}

	done := make(chan struct{})
	go func() {
		ws.runSubscription(send)
		close(done)
	}()

	require.Eventually(t, func() bool {
		theme, n := last()
		return n > 0 && theme == nil
	}, 3*time.Second, 5*time.Millisecond,
		"a reconnect must hand the UI the host theme the server holds, even none")

	theme := &proto.HostTheme{Name: "host", Palette: map[string]string{"primary": "#112233"}}
	srv.snapshot(func(s *recoveryServer) { s.hostTheme = theme })
	require.Eventually(t, func() bool {
		got, _ := last()
		return theme.Equal(got)
	}, 3*time.Second, 5*time.Millisecond)

	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown")
	}
}

// TestClientWorkspace_ResyncsHostThemeAfterAbortedStream covers 流
// 那 dies mid-响应 ( 服务端 was killed) instead 的 ending
// cleanly: loop must still resubscribe 和 re-读取 host theme, 所以
// clear published meanwhile reaches the UI.
func TestClientWorkspace_ResyncsHostThemeAfterAbortedStream(t *testing.T) {
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 5*time.Millisecond))

	srv := &recoveryServer{liveID: "ws-1", abortStreams: true}
	c := srv.start(t)
	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1", Path: "/tmp/abort"})

	var resynced atomic.Bool
	send := func(msg tea.Msg) {
		if ev, ok := msg.(HostThemeEvent); ok && ev.Theme == nil {
			resynced.Store(true)
		}
	}
	done := make(chan struct{})
	go func() {
		ws.runSubscription(send)
		close(done)
	}()

	require.Eventually(t, func() bool {
		var streams int
		srv.snapshot(func(s *recoveryServer) { streams = s.streams })
		return streams >= 2 && resynced.Load()
	}, 3*time.Second, 5*time.Millisecond,
		"an aborted stream must be resubscribed and the host theme re-read")

	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown")
	}
}

// TestClientWorkspace_EscalatesUnrecoverableConnection checks the loop
// keeps trying 当 re-registration itself keeps failing, 和 escalates
// UI notice rather than stopping: hard stop would strand 用户
// whose 服务端 comes back minute later.
func TestClientWorkspace_EscalatesUnrecoverableConnection(t *testing.T) {
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 2*time.Millisecond))

	srv := &recoveryServer{createErr: func() int { return http.StatusInternalServerError }}
	c := srv.start(t)

	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1", Path: "/tmp/hopeless"})
	rec := &connectionRecorder{}
	done := make(chan struct{})
	go func() {
		ws.runSubscription(rec.send)
		close(done)
	}()

	require.Eventually(t, rec.sawStuck, 5*time.Second, 5*time.Millisecond,
		"repeated recovery failures must escalate to a persistent notice")
	var creates int
	srv.snapshot(func(s *recoveryServer) { creates = s.creates })
	require.GreaterOrEqual(t, creates, maxRecoveryEscalate,
		"the loop must keep retrying rather than giving up")

	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown")
	}
}

// runSubscriptionUntilStreams runs 订阅 loop until srv has
// served n 事件 streams, then shuts 工作区 down.
func runSubscriptionUntilStreams(t *testing.T, ws *ClientWorkspace, srv *recoveryServer, n int, send func(tea.Msg)) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		ws.runSubscription(send)
		close(done)
	}()
	require.Eventually(t, func() bool {
		var streams int
		srv.snapshot(func(s *recoveryServer) { streams = s.streams })
		return streams >= n
	}, 3*time.Second, 5*time.Millisecond)
	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown")
	}
}

// TestClientWorkspace_RespawnsDeadServer guards against the reconnect loop
// retrying dead 服务端's address forever instead 的 respawning it.
func TestClientWorkspace_RespawnsDeadServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows servers listen on named pipes; see the npipe twin")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	testRespawnsDeadServer(t, "tcp", addr, func() (net.Listener, error) {
		return net.Listen("tcp", addr) //nolint:noctx
	})
}

// testRespawnsDeadServer points 客户端 在 address nothing listens 在
// 和 expects respawn 钩子 到 bring up 服务端 there, 在 which
// 工作区 是 re-registered.
func testRespawnsDeadServer(t *testing.T, network, addr string, listen func() (net.Listener, error)) {
	t.Helper()
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 5*time.Millisecond))
	c, err := client.NewClient(t.TempDir(), network, addr)
	require.NoError(t, err)

	next := &recoveryServer{nextID: "ws-2"}
	var respawns atomic.Int32
	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1", Path: "/tmp/respawn"},
		WithServerRespawn(func(context.Context) error {
			ln, err := listen()
			if err != nil {
				return err
			}
			hs := &http.Server{Handler: next.handler(t)}
			go func() { _ = hs.Serve(ln) }()
			t.Cleanup(func() { _ = hs.Close() })
			respawns.Add(1)
			return nil
		}))
	runSubscriptionUntilStreams(t, ws, next, 1, func(tea.Msg) {})
	require.Equal(t, "ws-2", ws.workspaceID())
	require.EqualValues(t, 1, respawns.Load())
}

// TestClientWorkspace_ShutdownRetiresAfterLostCreateResponse 是
// quit-during-recovery case, 在 它的 nastiest form: 客户端 asks 为
// replacement 工作区 和 连接 dies before it can 读取
// ID. 服务端 registered 工作区 anyway, 所以 客户端 是
// holding claim it cannot name. Retiring 客户端 是 what makes
// teardown exact here — there 是 nothing 到 guess 和 nothing 到 scan.
func TestClientWorkspace_ShutdownRetiresAfterLostCreateResponse(t *testing.T) {
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 5*time.Millisecond))

	srv := &recoveryServer{nextID: "ws-orphan", hangUpOnCreate: true}
	c := srv.start(t)

	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1", Path: "/tmp/recover"})
	done := make(chan struct{})
	go func() {
		ws.runSubscription(func(tea.Msg) {})
		close(done)
	}()

	// Wait until 服务端 has registered 工作区 客户端 never
	// learned about: 那 是 state Shutdown has 到 clean up.
	require.Eventually(t, func() bool {
		var live string
		srv.snapshot(func(s *recoveryServer) { live = s.liveID })
		return live == "ws-orphan"
	}, 3*time.Second, 5*time.Millisecond)
	require.NotEqual(t, "ws-orphan", ws.workspaceID(),
		"the client must genuinely not know the workspace's ID")

	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown")
	}

	srv.snapshot(func(s *recoveryServer) {
		require.Equal(t, []string{c.ClientID()}, s.retired,
			"Shutdown must retire the client, which releases claims it cannot name")
		require.Empty(t, s.liveID, "no workspace may be left holding the server open")
	})
}

// TestClientWorkspace_ShutdownFallsBackForLegacyServer 保持 quits working
// against 服务端 那 predates 客户端 retirement: 404 为 retire
// endpoint must fall back 到 releasing 工作区 通过 ID, 不 是
// reported 作为 failed teardown.
func TestClientWorkspace_ShutdownFallsBackForLegacyServer(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/clients/"):
			http.NotFound(w, r)
		case r.Method == http.MethodDelete:
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	NewClientWorkspace(c, proto.Workspace{ID: "ws-1"}).Shutdown()

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"ws-1"}, deleted)
}

// TestClientWorkspace_ShutdownIsBounded checks quitting cannot hang 在
// 服务端 那 accepts goodbye 和 never answers it, such 作为 one stopped
// 在 debugger 或 suspended 用 Ctrl+Z. 客户端 SDK sets no 请求
// 超时 的 它的 own, 和 通过 time Shutdown runs TUI 是 gone 和
// root signal 处理器 swallows Ctrl+C, 所以 nothing else can end wait.
func TestClientWorkspace_ShutdownIsBounded(t *testing.T) {
	// Shrink bound 所以 测试 does 不 wait out production 窗口,
	// keeping enough 的 it 为 goodbye 到 reach 服务端 在 loaded
	// machine. Not parallel: other tests 读取 bound through Shutdown.
	orig := shutdownRetireTimeout
	shutdownRetireTimeout = 200 * time.Millisecond
	t.Cleanup(func() { shutdownRetireTimeout = orig })

	for _, tc := range []struct {
		name string
		// wedged 是 路径 的 goodbye 请求 那 never answers.
		// Every other 请求 gets 404, which 是 how 服务端 那
		// predates 客户端 retirement answers retire.
		wedged string
	}{
		{name: "retire", wedged: "/v1/clients/"},
		{name: "legacy server", wedged: "/v1/workspaces/ws-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := make(chan struct{})
			var once sync.Once
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || !strings.HasPrefix(r.URL.Path, tc.wedged) {
					http.NotFound(w, r)
					return
				}
				once.Do(func() { close(reached) })
				// 受理 请求 和 never answer it.
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			// Unwedge 处理器 first: closing 服务端 waits 在 it.
			t.Cleanup(func() {
				close(release)
				srv.Close()
			})

			u, err := url.Parse(srv.URL)
			require.NoError(t, err)
			c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
			require.NoError(t, err)
			ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})

			done := make(chan struct{})
			go func() {
				ws.Shutdown()
				close(done)
			}()

			select {
			case <-reached:
			case <-time.After(5 * time.Second):
				t.Fatal("Shutdown never said goodbye to the server")
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Shutdown blocked on an unresponsive server")
			}
		})
	}
}

// TestClientWorkspace_AgentReadyErr_WorkspaceGone checks the status the
// UI 是 given while recovery runs. 一个 404 从 live 服务端 used 到 print
// "lost 连接 到 crush 服务端: ... status code 404", which 是
// both wrong 和 unactionable.
func TestClientWorkspace_AgentReadyErr_WorkspaceGone(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})
	readyErr := ws.AgentReadyErr()
	require.ErrorIs(t, readyErr, ErrWorkspaceGone)
	require.NotErrorIs(t, readyErr, ErrServerUnreachable)
	require.NotErrorIs(t, readyErr, ErrAgentNotInitialized)
}

// TestClientWorkspace_ShutdownWaitsForInFlightRecovery pins the ordering
// requirement: quitting must stop recovery before saying goodbye 到
// 服务端. Cancelling alone does 不 unwind 创建 那 是 already 在
// flight, 所以 goodbye sent first would 是 followed 通过 工作区
// 创建 went 在 到 register — orphan keeping 服务端 alive 和
// blocking the next upgrade.
func TestClientWorkspace_ShutdownWaitsForInFlightRecovery(t *testing.T) {
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 5*time.Millisecond))

	var mu sync.Mutex
	var ops []string
	creating := make(chan struct{})
	var once sync.Once
	live := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces":
			once.Do(func() { close(creating) })
			// Slow enough 那 Shutdown lands mid-创建.
			// nolint:forbidigo // overlap 是 point 的 测试.
			time.Sleep(300 * time.Millisecond)
			mu.Lock()
			ops = append(ops, "create")
			live = "ws-2"
			mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(proto.Workspace{ID: "ws-2"}))
		case strings.HasPrefix(r.URL.Path, "/v1/clients/"):
			mu.Lock()
			ops = append(ops, "retire")
			live = ""
			mu.Unlock()
		case strings.HasSuffix(r.URL.Path, "/events"):
			mu.Lock()
			known := live == strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/workspaces/"), "/events")
			mu.Unlock()
			if !known {
				http.Error(w, "workspace not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1", Path: "/tmp/quit-mid-recovery"})
	done := make(chan struct{})
	go func() {
		ws.runSubscription(func(tea.Msg) {})
		close(done)
	}()

	<-creating
	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runSubscription did not return after Shutdown")
	}

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"create", "retire"}, ops,
		"the goodbye must follow the create it was racing, not precede it")
	require.Empty(t, live, "the recovered workspace must not outlive the client")
	require.Equal(t, "ws-2", ws.workspaceID(),
		"the client must have adopted the workspace it created before releasing it")
}

// TestClientWorkspace_RecoveryCreateIsBounded checks wedged 服务端 cannot
// pin recovery attempt forever. 客户端 SDK sets no 请求 超时,
// 所以 without bound here subscription goroutine would block 为 good
// 在 服务端 那 accepts 创建 和 never answers.
func TestClientWorkspace_RecoveryCreateIsBounded(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces" {
			<-release // Accept the create and never answer it.
			return
		}
		http.NotFound(w, r)
	}))
	// Unwedge 处理器 before closing 服务端, which waits 在 it.
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1", Path: "/tmp/wedged"})
	// Shrink bound 所以 测试 does 不 wait out production 窗口.
	// recovery must give up 在 它的 own, without anything cancelling it:
	// 创建 是 deliberately detached 从 subscription 上下文.
	orig := recoveryCreateTimeoutForTest(50 * time.Millisecond)
	t.Cleanup(orig)

	done := make(chan error, 1)
	go func() { done <- ws.recoverWorkspace() }()

	select {
	case err := <-done:
		require.Error(t, err, "a create that never answers must not report success")
	case <-time.After(5 * time.Second):
		t.Fatal("recoverWorkspace blocked on an unresponsive server")
	}
}

// TestClientWorkspace_RecoveryRestoresMainAgent guards against a
// re-created 工作区 dropping plan mode.
func TestClientWorkspace_RecoveryRestoresMainAgent(t *testing.T) {
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 5*time.Millisecond))

	srv := &recoveryServer{
		liveID: "ws-1",
		nextID: "ws-2",
		config: &config.Config{
			Options: &config.Options{},
			Providers: csync.NewMapFrom(map[string]config.ProviderConfig{
				"fake": {ID: "fake"},
			}),
		},
	}
	ws := NewClientWorkspace(srv.start(t), proto.Workspace{ID: "ws-1", Path: "/tmp/recover"})
	require.NoError(t, ws.AgentSetMain(config.AgentPlan))

	srv.snapshot(func(s *recoveryServer) { s.liveID = "" })
	runSubscriptionUntilStreams(t, ws, srv, 1, func(tea.Msg) {})

	srv.snapshot(func(s *recoveryServer) {
		require.Len(t, s.createReqs, 1)
		require.Equal(t, []string{"agent/main ws-1 plan", "agent/init ws-2", "agent/main ws-2 plan"}, s.agentCalls)
	})
}

// TestClientWorkspace_WarnsAboutServerVersionMismatch guards 那 用户
// 是 warned, once, 当 工作区's 服务端 runs another Crush version.
func TestClientWorkspace_WarnsAboutServerVersionMismatch(t *testing.T) {
	t.Cleanup(SetSSEBackoffForTest(time.Millisecond, 5*time.Millisecond))

	otherVersion := "not-" + version.Version
	srv := &recoveryServer{liveID: "ws-1"}
	ws := NewClientWorkspace(srv.start(t), proto.Workspace{
		ID: "ws-1", Path: "/tmp/version", Version: otherVersion,
	})

	var mu sync.Mutex
	var msgs []util.InfoMsg
	runSubscriptionUntilStreams(t, ws, srv, 3, func(msg tea.Msg) {
		if info, ok := msg.(util.InfoMsg); ok {
			mu.Lock()
			msgs = append(msgs, info)
			mu.Unlock()
		}
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, msgs, 1)
	require.Equal(t, util.InfoTypeWarn, msgs[0].Type)
	require.Contains(t, msgs[0].Msg, strconv.Quote(otherVersion))
}

// TestTranslateEvent_PermissionNotification verifies 那
// 通知's 会话 ID survives proto-到-domain translation,
// keeping it available 为 会话-scoped consumers.
func TestTranslateEvent_PermissionNotification(t *testing.T) {
	t.Parallel()

	w := NewClientWorkspace(nil, proto.Workspace{})
	ev := pubsub.Event[proto.PermissionNotification]{
		Type: pubsub.CreatedEvent,
		Payload: proto.PermissionNotification{
			SessionID:  "s1",
			ToolCallID: "tc-1",
			Granted:    true,
		},
	}

	out := w.translateEvent(ev)
	got, ok := out.(pubsub.Event[permission.PermissionNotification])
	require.True(t, ok, "expected pubsub.Event[permission.PermissionNotification], got %T", out)
	require.Equal(t, "s1", got.Payload.SessionID)
	require.Equal(t, "tc-1", got.Payload.ToolCallID)
	require.True(t, got.Payload.Granted)
}

// TestClientWorkspace_GitBranch verifies 那 GitBranch asks 服务端 和
// returns what it reports. Caching 和 scheduling 是 TUI's business, 所以
// nothing here pretends otherwise.
func TestClientWorkspace_GitBranch(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/workspaces/ws-1/git/branch", r.URL.Path)
		hits.Add(1)
		require.NoError(t, json.NewEncoder(w).Encode(proto.GitBranchResponse{Branch: "feature/x"}))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})
	t.Cleanup(ws.Shutdown)

	branch, err := ws.GitBranch(t.Context())
	require.NoError(t, err)
	require.Equal(t, "feature/x", branch)
	require.Equal(t, int64(1), hits.Load(), "one call must make exactly one request")
}

// TestClientWorkspace_GitBranchServerError verifies 那 failing 服务端
// surfaces 作为 错误 rather than silently empty branch, 所以 caller can
// decide whether 到 keep what it already had.
func TestClientWorkspace_GitBranchServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	ws := NewClientWorkspace(c, proto.Workspace{ID: "ws-1"})
	t.Cleanup(ws.Shutdown)
	_, err = ws.GitBranch(t.Context())
	require.Error(t, err)
}
