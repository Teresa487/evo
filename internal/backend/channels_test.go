package backend

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fakeChannelSessions 是 channelSessionStore backed 通过 在-memory
// fixtures. created records titles passed 到 创建.
type fakeChannelSessions struct {
	sessions map[string]session.Session
	listed   []session.Session
	created  []string
	getErr   error
}

func (f *fakeChannelSessions) Create(_ context.Context, title string) (session.Session, error) {
	f.created = append(f.created, title)
	return session.Session{ID: "created-" + title}, nil
}

func (f *fakeChannelSessions) Get(_ context.Context, id string) (session.Session, error) {
	if f.getErr != nil {
		return session.Session{}, f.getErr
	}
	s, ok := f.sessions[id]
	if !ok {
		return session.Session{}, errors.New("not found")
	}
	return s, nil
}

func (f *fakeChannelSessions) List(context.Context) ([]session.Session, error) {
	return f.listed, nil
}

func TestChannelTargetSession(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("single viewed session wins", func(t *testing.T) {
		t.Parallel()
		got, err := channelTargetSession(ctx, "signal", []string{"s1"}, &fakeChannelSessions{})
		require.NoError(t, err)
		require.Equal(t, "s1", got)
	})

	t.Run("multiple viewed picks most recently updated", func(t *testing.T) {
		t.Parallel()
		store := &fakeChannelSessions{sessions: map[string]session.Session{
			"old": {ID: "old", UpdatedAt: 100},
			"new": {ID: "new", UpdatedAt: 200},
		}}
		got, err := channelTargetSession(ctx, "signal", []string{"new", "old"}, store)
		require.NoError(t, err)
		require.Equal(t, "new", got)
	})

	t.Run("viewed session ties break on smallest ID", func(t *testing.T) {
		t.Parallel()
		store := &fakeChannelSessions{sessions: map[string]session.Session{
			"b": {ID: "b", UpdatedAt: 100},
			"a": {ID: "a", UpdatedAt: 100},
		}}
		got, err := channelTargetSession(ctx, "signal", []string{"b", "a"}, store)
		require.NoError(t, err)
		require.Equal(t, "a", got)
	})

	t.Run("none viewed reuses most recent top-level session", func(t *testing.T) {
		t.Parallel()
		store := &fakeChannelSessions{listed: []session.Session{
			{ID: "recent"}, {ID: "older"},
		}}
		got, err := channelTargetSession(ctx, "signal", nil, store)
		require.NoError(t, err)
		require.Equal(t, "recent", got)
		require.Empty(t, store.created)
	})

	t.Run("none viewed prefers session bound to the channel", func(t *testing.T) {
		t.Parallel()
		store := &fakeChannelSessions{listed: []session.Session{
			{ID: "unrelated"}, {ID: "channel-chat", Channel: "signal"}, {ID: "older-channel", Channel: "signal"},
		}}
		got, err := channelTargetSession(ctx, "signal", nil, store)
		require.NoError(t, err)
		require.Equal(t, "channel-chat", got)
		require.Empty(t, store.created)
	})

	t.Run("none viewed and no sessions creates one", func(t *testing.T) {
		t.Parallel()
		store := &fakeChannelSessions{}
		got, err := channelTargetSession(ctx, "signal", nil, store)
		require.NoError(t, err)
		require.Equal(t, "created-New Session", got)
		require.Equal(t, []string{"New Session"}, store.created)
	})

	t.Run("all viewed sessions unloadable falls back", func(t *testing.T) {
		t.Parallel()
		store := &fakeChannelSessions{getErr: errors.New("boom")}
		got, err := channelTargetSession(ctx, "signal", []string{"x", "y"}, store)
		require.NoError(t, err)
		require.Equal(t, "created-New Session", got)
	})
}

func TestWorkspaceViewedSessions(t *testing.T) {
	t.Parallel()
	ws := &Workspace{clients: map[string]*clientState{
		"attached-a":   {streams: 1, currentSessionID: "s2"},
		"attached-b":   {streams: 2, currentSessionID: "s1"},
		"attached-c":   {streams: 1, currentSessionID: "s1"}, // duplicate of b
		"landing":      {streams: 1, currentSessionID: ""},   // landing screen
		"hold-only":    {streams: 0, currentSessionID: "s3"}, // no live stream
		"hold-landing": {streams: 0},
	}}
	require.Equal(t, []string{"s1", "s2"}, ws.viewedSessions())
}

// recordingCoordinator 是 minimal agent.Coordinator 那 records
// 会话/提示词 的 each RunAccepted 调用, plus per-turn 渠道
// provenance carried 在 上下文.
type recordingCoordinator struct {
	runs     chan [2]string // {sessionID, prompt}
	channels chan string    // per-turn channel provenance
}

func newRecordingCoordinator() *recordingCoordinator {
	return &recordingCoordinator{runs: make(chan [2]string, 8), channels: make(chan string, 8)}
}

func (c *recordingCoordinator) Run(context.Context, string, string, ...message.Attachment) (*fantasy.AgentResult, error) {
	return nil, nil
}

func (c *recordingCoordinator) RunAccepted(ctx context.Context, _ *agent.AcceptedRun, sessionID, prompt string, _ ...message.Attachment) (*fantasy.AgentResult, error) {
	c.channels <- agent.ChannelFromContext(ctx)
	c.runs <- [2]string{sessionID, prompt}
	return nil, nil
}

func (c *recordingCoordinator) BeginAccepted(string) *agent.AcceptedRun       { return nil }
func (c *recordingCoordinator) Cancel(string)                                 {}
func (c *recordingCoordinator) CancelAll()                                    {}
func (c *recordingCoordinator) IsBusy() bool                                  { return false }
func (c *recordingCoordinator) IsSessionBusy(string) bool                     { return false }
func (c *recordingCoordinator) QueuedPrompts(string) int                      { return 0 }
func (c *recordingCoordinator) QueuedPromptsList(string) []string             { return nil }
func (c *recordingCoordinator) ClearQueue(string)                             {}
func (c *recordingCoordinator) AddNote(string, string)                        {}
func (c *recordingCoordinator) Summarize(context.Context, string) error       { return nil }
func (c *recordingCoordinator) Model() agent.Model                            { return agent.Model{} }
func (c *recordingCoordinator) UpdateModels(context.Context) error            { return nil }
func (c *recordingCoordinator) SetMainAgent(string) error                     { return nil }
func (c *recordingCoordinator) MainAgent() string                             { return "" }
func (c *recordingCoordinator) GenerateTitle(context.Context, string, string) {}
func (c *recordingCoordinator) SetHostContext(proto.HostContext)              {}
func (c *recordingCoordinator) HostContext() proto.HostContext                { return proto.HostContext{} }

// fullFakeSessions adapts fakeChannelSessions 到 full 会话.Service
// interface 通过 embedding it; 只 channelSessionStore subset 是
// implemented, which 是 all 渠道 router touches.
type fullFakeSessions struct {
	session.Service
	*fakeChannelSessions
}

func (f *fullFakeSessions) Create(ctx context.Context, title string) (session.Session, error) {
	return f.fakeChannelSessions.Create(ctx, title)
}

func (f *fullFakeSessions) Get(ctx context.Context, id string) (session.Session, error) {
	return f.fakeChannelSessions.Get(ctx, id)
}

func (f *fullFakeSessions) List(ctx context.Context) ([]session.Session, error) {
	return f.fakeChannelSessions.List(ctx)
}

// insertChannelWorkspace installs synthetic 工作区 whose 配置
// declares MCP 服务端 named srvName, opted 在 作为 渠道 iff
// enabled 是 true, mirroring fields 渠道 router reads.
func insertChannelWorkspace(t *testing.T, b *Backend, srvName string, enabled bool, coord agent.Coordinator, sessions session.Service) *Workspace {
	return insertChannelWorkspaceCfg(t, b, srvName, enabled, false, coord, sessions)
}

// insertChannelWorkspaceCfg 是 insertChannelWorkspace 用 control over both
// enablement sources: `enabled` opts 服务端 在 via --channels
// override, `configEnabled` writes channel_enabled: true into 工作区's
// crush.json.
func insertChannelWorkspaceCfg(t *testing.T, b *Backend, srvName string, enabled, configEnabled bool, coord agent.Coordinator, sessions session.Service) *Workspace {
	t.Helper()

	wd := t.TempDir()
	mcpEntry := map[string]any{"type": "http", "url": "http://127.0.0.1:0/mcp"}
	if configEnabled {
		mcpEntry["channel_enabled"] = true
	}
	cfgJSON, err := json.Marshal(map[string]any{
		"mcp": map[string]any{srvName: mcpEntry},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(wd, "evo.json"), cfgJSON, 0o644))

	cfg, err := config.Init(t.Context(), wd, "", false)
	require.NoError(t, err)
	if enabled {
		cfg.Overrides().EnabledChannels = []string{srvName}
	}

	ws := &Workspace{
		ID:           uuid.New().String(),
		Path:         wd,
		Cfg:          cfg,
		resolvedPath: wd,
		clients:      make(map[string]*clientState),
		shutdownFn:   func() {},
	}
	ws.App = &app.App{AgentCoordinator: coord, Sessions: sessions}
	ws.ctx, ws.cancel = context.WithCancel(b.ctx)
	b.mu.Lock()
	b.workspaces.Set(ws.ID, ws)
	b.pathIndex[ws.resolvedPath] = ws.ID
	b.mu.Unlock()
	return ws
}

// TestRouteChannelMessage_InjectsOncePerOptedInWorkspace 是 core
// exactly-once contract: 渠道 push 是 injected into 工作区
// 那 declares AND opted 在 服务端 (even 用 zero attached
// clients), 和 skipped 为 workspaces 那 只 declare it 或 那
// don't know 服务端 在 all.
func TestRouteChannelMessage_InjectsOncePerOptedInWorkspace(t *testing.T) {
	xdgIsolated(t)
	b, _ := newTestBackend(t)

	optedIn := newRecordingCoordinator()
	declaredOnly := newRecordingCoordinator()
	unrelated := newRecordingCoordinator()

	wsIn := insertChannelWorkspace(t, b, "webhook", true, optedIn,
		&fullFakeSessions{fakeChannelSessions: &fakeChannelSessions{listed: []session.Session{{ID: "recent"}}}})
	insertChannelWorkspace(t, b, "webhook", false, declaredOnly,
		&fullFakeSessions{fakeChannelSessions: &fakeChannelSessions{}})
	insertChannelWorkspace(t, b, "other", true, unrelated,
		&fullFakeSessions{fakeChannelSessions: &fakeChannelSessions{}})

	const content = `<channel source="webhook">build failed</channel>`
	b.routeChannelMessage(mcptools.Event{
		Type:           mcptools.EventChannelMessage,
		Name:           "webhook",
		ChannelMessage: content,
	})

	select {
	case run := <-optedIn.runs:
		require.Equal(t, "recent", run[0], "should land in the most recent session")
		require.Equal(t, content, run[1])
		require.Equal(t, "webhook", <-optedIn.channels, "the server-routed turn must carry its originating channel")
	case <-time.After(5 * time.Second):
		t.Fatal("expected the opted-in workspace to receive the channel push")
	}
	wsIn.runWG.Wait()

	require.Empty(t, optedIn.runs, "exactly one injection for the opted-in workspace")
	require.Empty(t, declaredOnly.runs, "declared-but-not-opted-in workspace must not receive the push")
	require.Empty(t, unrelated.runs, "workspace without the server must not receive the push")
}

// TestRouteChannelMessage_PrefersViewedSession verifies the presence map
// drives targeting: 用 客户端 viewing 会话, push lands
// there instead 的 most recently updated 会话.
func TestRouteChannelMessage_PrefersViewedSession(t *testing.T) {
	xdgIsolated(t)
	b, _ := newTestBackend(t)

	coord := newRecordingCoordinator()
	ws := insertChannelWorkspace(t, b, "webhook", true, coord,
		&fullFakeSessions{fakeChannelSessions: &fakeChannelSessions{listed: []session.Session{{ID: "recent"}}}})
	ws.clientsMu.Lock()
	ws.clients["client-1"] = &clientState{streams: 1, currentSessionID: "viewed"}
	ws.clientsMu.Unlock()

	b.routeChannelMessage(mcptools.Event{
		Type:           mcptools.EventChannelMessage,
		Name:           "webhook",
		ChannelMessage: `<channel source="webhook">hi</channel>`,
	})

	select {
	case run := <-coord.runs:
		require.Equal(t, "viewed", run[0])
	case <-time.After(5 * time.Second):
		t.Fatal("expected an injection into the viewed session")
	}
	ws.runWG.Wait()
}

// TestRouteChannelMessage_ConfigEnabled verifies 那 服务端 用
// channel_enabled: true 在 crush.json receives 渠道 pushes without
// needing --channels 在 CLI.
func TestRouteChannelMessage_ConfigEnabled(t *testing.T) {
	xdgIsolated(t)
	b, _ := newTestBackend(t)

	coord := newRecordingCoordinator()
	sessions := &fullFakeSessions{fakeChannelSessions: &fakeChannelSessions{listed: []session.Session{{ID: "recent"}}}}
	// channel_enabled 在 配置, deliberately no --channels override.
	ws := insertChannelWorkspaceCfg(t, b, "webhook", false, true, coord, sessions)

	const content = `<channel source="webhook">config-enabled push</channel>`
	b.routeChannelMessage(mcptools.Event{
		Type:           mcptools.EventChannelMessage,
		Name:           "webhook",
		ChannelMessage: content,
	})

	select {
	case run := <-coord.runs:
		require.Equal(t, "recent", run[0])
		require.Equal(t, content, run[1])
	case <-time.After(5 * time.Second):
		t.Fatal("expected the config-enabled workspace to receive the channel push")
	}
	ws.runWG.Wait()
}

func (*recordingCoordinator) ListSettledMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}
