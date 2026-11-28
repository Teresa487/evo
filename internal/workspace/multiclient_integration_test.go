package workspace_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/server"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// xdgIsolate redirects HOME 和 XDG_* 到 fresh temp dirs 所以 配置
// loading does 不 touch host's real 配置.
func xdgIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

// runtimeServer wires production 服务端 处理器 around
// httptest.NewServer 为 integration testing.
type runtimeServer struct {
	srv     *server.Server
	httpSrv *httptest.Server
	host    string
}

func newRuntimeServer(t *testing.T) *runtimeServer {
	t.Helper()
	s := server.NewServer(nil, "tcp", "127.0.0.1:0")
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)

	u, err := url.Parse(hs.URL)
	require.NoError(t, err)
	return &runtimeServer{srv: s, httpSrv: hs, host: u.Host}
}

func (r *runtimeServer) newClient(t *testing.T, path string) *client.Client {
	t.Helper()
	c, err := client.NewClient(path, "tcp", r.host)
	require.NoError(t, err)
	// Retire 客户端 during 清理 所以 服务端 releases every
	// claim it holds 和 tears 工作区 down 在 once, closing
	// pooled DB 连接. Without 这 测试 那 leaves clients
	// attached ( SSE 缓存 tests never shut theirs down) keeps
	// 工作区, 和 它的 open evo.db, alive past t.TempDir 清理,
	// which Windows cannot remove while 文件 是 locked.
	t.Cleanup(func() { _ = c.RetireClient(context.Background()) })
	return c
}

// TestClientWorkspace_ConfigChangedRefreshesSiblingCache 是
// cross-客户端 refresh end-到-end 测试 required 通过 PLAN item 4. Two
// ClientWorkspace instances pointed 在 same backend 工作区
// subscribe 到 events; 当 one mutates configuration via 服务端,
// other's cached 配置 snapshot reflects new 值 without
// manual 刷新.
func TestClientWorkspace_ConfigChangedRefreshesSiblingCache(t *testing.T) {
	xdgIsolate(t)
	rt := newRuntimeServer(t)

	cwd := t.TempDir()
	dataDir := t.TempDir()

	cA := rt.newClient(t, cwd)
	cB := rt.newClient(t, cwd)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	wsProto, err := cA.CreateWorkspace(ctx, proto.Workspace{Path: cwd, DataDir: dataDir})
	require.NoError(t, err)
	// 客户端 B joins same 工作区 通过 路径; 服务端
	// deduplicates 和 returns existing 工作区.
	wsProtoB, err := cB.CreateWorkspace(ctx, proto.Workspace{Path: cwd, DataDir: dataDir})
	require.NoError(t, err)
	require.Equal(t, wsProto.ID, wsProtoB.ID)

	wsA := workspace.NewClientWorkspace(cA, *wsProto)
	wsB := workspace.NewClientWorkspace(cB, *wsProtoB)

	// Both clients attach 事件 streams. They 运行 为
	// lifetime 的 测试; cancelling via 上下文 tears them
	// down. consumeEvents 是 exercised 通过 Subscribe 在 production;
	// here we 运行 it inline 所以 we don't need real *tea.Program.
	evcA, err := cA.SubscribeEvents(ctx, wsProto.ID)
	require.NoError(t, err)
	evcB, err := cB.SubscribeEvents(ctx, wsProto.ID)
	require.NoError(t, err)

	go wsA.ConsumeEventsForTest(evcA, func(tea.Msg) {})
	go wsB.ConsumeEventsForTest(evcB, func(tea.Msg) {})

	// Pre-condition: neither 缓存 has compact mode enabled yet.
	require.NotNil(t, wsA.Config())
	require.NotNil(t, wsB.Config())
	require.False(t, compactMode(wsA.Config()), "compact mode must start disabled on client A")
	require.False(t, compactMode(wsB.Config()), "compact mode must start disabled on client B")

	// 客户端 一个 flips real 配置-mutating 工作区 operation
	// (SetCompactMode) via 服务端. PLAN item 4 acceptance:
	// B's cached ws.配置 must reflect 这 change without restart.
	// SetCompactMode 是 used over UpdatePreferredModel because
	// latter's autoReload reverts unknown-提供方 models back 到
	// defaults during configureSelectedModels, which would make the
	// assertion 测试 infrastructure rather than 缓存 wiring.
	require.NoError(t, wsA.SetCompactMode(config.ScopeGlobal, true))

	// 客户端 一个 writes 和 refreshes synchronously inside
	// SetCompactMode, 所以 它的 缓存 must already reflect change.
	// Eventually here absorbs any background work 但 should pass
	// immediately.
	require.Eventually(t, func() bool { return compactMode(wsA.Config()) },
		3*time.Second, 25*time.Millisecond,
		"client A cache must reflect its own compact-mode mutation")

	// 客户端 B must see same change via ConfigChanged SSE
	// 事件 triggering 它的 own cached refresh.
	require.Eventually(t, func() bool { return compactMode(wsB.Config()) },
		3*time.Second, 25*time.Millisecond,
		"client B cache must reflect A's compact-mode mutation via SSE")
}

// compactMode 是 tiny accessor 那 survives nil intermediates 所以
// Eventually polling loop can 调用 it 在 transient 缓存 state.
func compactMode(cfg *config.Config) bool {
	if cfg == nil || cfg.Options == nil {
		return false
	}
	return cfg.Options.TUI.CompactMode
}

// TestClientWorkspace_ConfigChangedSignalArrives 是 smaller 测试
// 那 asserts SSE wiring delivers ConfigChanged 事件 到
// raw 客户端 subscription. 它 catches breakage 在
// wrapEvent/decoder bridge independent 的 工作区 缓存.
func TestClientWorkspace_ConfigChangedSignalArrives(t *testing.T) {
	xdgIsolate(t)
	rt := newRuntimeServer(t)

	cwd := t.TempDir()
	dataDir := t.TempDir()

	c := rt.newClient(t, cwd)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	wsProto, err := c.CreateWorkspace(ctx, proto.Workspace{Path: cwd, DataDir: dataDir})
	require.NoError(t, err)

	evc, err := c.SubscribeEvents(ctx, wsProto.ID)
	require.NoError(t, err)

	require.NoError(t, c.SetConfigField(ctx, wsProto.ID, config.ScopeGlobal, "options.debug", true))

	gotConfigChanged := false
	deadline := time.After(3 * time.Second)
loop:
	for !gotConfigChanged {
		select {
		case ev, ok := <-evc:
			if !ok {
				break loop
			}
			if cc, isCC := ev.(pubsub.Event[proto.ConfigChanged]); isCC {
				require.Equal(t, wsProto.ID, cc.Payload.WorkspaceID)
				gotConfigChanged = true
			}
		case <-deadline:
			break loop
		}
	}
	require.True(t, gotConfigChanged, "expected ConfigChanged event over SSE")
}

// TestClientWorkspace_GlobalTogglesRefreshCaches covers the Global scope
// 的 skill 和 MCP toggle dialogs. toggling 客户端 must 读取 它的
// change back 作为 soon 作为 调用 returns, 或 dialog re-reads
// stale 配置 和 flips 行 back, 和 sibling 客户端 must pick it
// up 从 ConfigChanged 事件.
func TestClientWorkspace_GlobalTogglesRefreshCaches(t *testing.T) {
	const name = "toggle-probe"
	tests := []struct {
		name     string
		toggle   func(context.Context, *workspace.ClientWorkspace) error
		disabled func(*config.Config) bool
	}{
		{
			name: "skill",
			toggle: func(ctx context.Context, w *workspace.ClientWorkspace) error {
				return w.SkillSetConfigDisabled(ctx, name, true)
			},
			disabled: func(cfg *config.Config) bool {
				return slices.Contains(cfg.Options.DisabledSkills, name)
			},
		},
		{
			name: "mcp",
			toggle: func(ctx context.Context, w *workspace.ClientWorkspace) error {
				return w.MCPSetServerConfigDisabled(ctx, name, true)
			},
			disabled: func(cfg *config.Config) bool {
				return cfg.MCP[name].Disabled
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xdgIsolate(t)
			t.Setenv("EVO_DISABLE_PROVIDER_AUTO_UPDATE", "1")
			// command does 不 exist, 所以 startup 只 records
			// 错误 和 no MCP 服务端 ever runs.
			globalCfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "crush", "evo.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(globalCfg), 0o755))
			require.NoError(t, os.WriteFile(globalCfg, fmt.Appendf(nil,
				`{"mcp": {%q: {"type": "stdio", "command": "crush-toggle-probe-missing"}}}`, name), 0o644))

			rt := newRuntimeServer(t)
			cwd, dataDir := t.TempDir(), t.TempDir()
			cA, cB := rt.newClient(t, cwd), rt.newClient(t, cwd)
			ctx := t.Context()

			wsProtoA, err := cA.CreateWorkspace(ctx, proto.Workspace{Path: cwd, DataDir: dataDir})
			require.NoError(t, err)
			wsProtoB, err := cB.CreateWorkspace(ctx, proto.Workspace{Path: cwd, DataDir: dataDir})
			require.NoError(t, err)
			require.Equal(t, wsProtoA.ID, wsProtoB.ID)
			wsA := workspace.NewClientWorkspace(cA, *wsProtoA)
			wsB := workspace.NewClientWorkspace(cB, *wsProtoB)

			// Only B listens, 所以 一个 can see 它的 own toggle 只 through
			// refresh 那 follows 调用.
			evc, err := cB.SubscribeEvents(ctx, wsProtoB.ID)
			require.NoError(t, err)
			go wsB.ConsumeEventsForTest(evc, func(tea.Msg) {})

			require.False(t, tt.disabled(wsA.Config()))
			require.False(t, tt.disabled(wsB.Config()))

			require.NoError(t, tt.toggle(ctx, wsA))
			require.True(t, tt.disabled(wsA.Config()),
				"the toggling client must see its global toggle at once")
			require.Eventually(t, func() bool { return tt.disabled(wsB.Config()) },
				3*time.Second, 25*time.Millisecond,
				"a sibling client must see the global toggle via ConfigChanged")
		})
	}
}

// -- Concurrent 会话 lifecycle --
//
// These drive real HTTP surface, because bug they cover 只
// appears 当 two sessions share 服务端: first 客户端's 工作区
// died, 和 sibling kept 服务端 (和 它的 404s) alive 所以 first
// 客户端 never got fresh start.

// TestServer_ConcurrentSessionsSameDirShareOneWorkspace checks the
// baseline 为 two sessions 在 one 目录: they share 工作区, 和
// one 的 them leaving must 不 disturb other.
func TestServer_ConcurrentSessionsSameDirShareOneWorkspace(t *testing.T) {
	xdgIsolate(t)
	rt := newRuntimeServer(t)
	rt.srv.Backend().SetDetachGrace(0)

	cwd, dataDir := t.TempDir(), t.TempDir()
	cA, cB := rt.newClient(t, cwd), rt.newClient(t, cwd)
	ctx := t.Context()

	wsA, err := cA.CreateWorkspace(ctx, proto.Workspace{Path: cwd, DataDir: dataDir})
	require.NoError(t, err)
	wsB, err := cB.CreateWorkspace(ctx, proto.Workspace{Path: cwd, DataDir: dataDir})
	require.NoError(t, err)
	require.Equal(t, wsA.ID, wsB.ID, "same directory must share one workspace")

	// 一个 leaves. B's claim must keep 工作区 addressable.
	require.NoError(t, cA.RetireClient(ctx))
	got, err := cB.GetWorkspace(ctx, wsB.ID)
	require.NoError(t, err, "one client leaving must not destroy the shared workspace")
	require.Equal(t, wsB.ID, got.ID)
}

// TestServer_ConcurrentSessionsDifferentDirsAreIndependent 是 other
// half: two directories get two workspaces 在 one 服务端, 和 retiring
// one 客户端 must leave other's 工作区 completely alone.
func TestServer_ConcurrentSessionsDifferentDirsAreIndependent(t *testing.T) {
	xdgIsolate(t)
	rt := newRuntimeServer(t)
	rt.srv.Backend().SetDetachGrace(0)

	cwdA, cwdB := t.TempDir(), t.TempDir()
	cA, cB := rt.newClient(t, cwdA), rt.newClient(t, cwdB)
	ctx := t.Context()

	wsA, err := cA.CreateWorkspace(ctx, proto.Workspace{Path: cwdA, DataDir: t.TempDir()})
	require.NoError(t, err)
	wsB, err := cB.CreateWorkspace(ctx, proto.Workspace{Path: cwdB, DataDir: t.TempDir()})
	require.NoError(t, err)
	require.NotEqual(t, wsA.ID, wsB.ID)

	require.NoError(t, cA.RetireClient(ctx))

	_, err = cB.GetWorkspace(ctx, wsB.ID)
	require.NoError(t, err, "a sibling session's workspace must survive")
	_, err = cB.GetWorkspace(ctx, wsA.ID)
	require.ErrorIs(t, err, client.ErrNotFound, "the retired client's workspace must be gone")
}

// TestServer_RefusesShutdownWhileWorkspaceLive 是 invariant 那 keeps
// upgrading 客户端 从 killing running 会话. 拒绝 has 到
// come 从 服务端: 客户端 checking idleness itself needs second
// round trip 那 new 会话 can slip into.
func TestServer_RefusesShutdownWhileWorkspaceLive(t *testing.T) {
	xdgIsolate(t)
	rt := newRuntimeServer(t)

	cwd := t.TempDir()
	cLive := rt.newClient(t, cwd)
	ctx := t.Context()

	ws, err := cLive.CreateWorkspace(ctx, proto.Workspace{Path: cwd, DataDir: t.TempDir()})
	require.NoError(t, err)

	// 一个 second 客户端, standing 在 为 one 那 found version mismatch.
	err = rt.newClient(t, t.TempDir()).ShutdownServerIfIdle(ctx)
	require.ErrorIs(t, err, client.ErrServerBusy,
		"a server hosting a workspace must refuse to stand down")

	_, err = cLive.GetWorkspace(ctx, ws.ID)
	require.NoError(t, err, "the live session's workspace must be untouched")
}

// TestServer_DetachGraceSurvivesStreamBlip 是 服务端-side half 的
// SSE regression. Cutting 流 used 到 destroy 工作区
// instantly, 所以 客户端's reconnect — 250ms later — came back 到 ID
// 服务端 no longer knew, 和 404'd 从 then 在.
func TestServer_DetachGraceSurvivesStreamBlip(t *testing.T) {
	xdgIsolate(t)
	rt := newRuntimeServer(t)
	rt.srv.Backend().SetDetachGrace(30 * time.Second)

	cwd := t.TempDir()
	c := rt.newClient(t, cwd)

	ws, err := c.CreateWorkspace(t.Context(), proto.Workspace{Path: cwd, DataDir: t.TempDir()})
	require.NoError(t, err)

	streamCtx, killStream := context.WithCancel(t.Context())
	evc, err := c.SubscribeEvents(streamCtx, ws.ID)
	require.NoError(t, err)

	// Cut 流 way network blip does: no release first.
	killStream()
	for range evc { //nolint:revive // Drain until the server closes it.
	}

	require.Eventually(t, func() bool {
		_, err := c.GetWorkspace(t.Context(), ws.ID)
		return err == nil
	}, 3*time.Second, 25*time.Millisecond,
		"the workspace must survive the drop so the reconnect can re-attach")

	// reconnect lands 和 工作区 是 still same one.
	evc2, err := c.SubscribeEvents(t.Context(), ws.ID)
	require.NoError(t, err, "the client must be able to re-attach to its workspace")
	require.NotNil(t, evc2)
}

// TestClientWorkspace_RecoversAfterServerSideTeardown 是 whole bug,
// end 到 end: 服务端 drops 客户端's 工作区 while 服务端
// itself stays up ( sibling 会话 keeps it alive). 客户端 must
// notice 404, re-register, 和 end up 在 工作区 it can use again
// rather than 404ing forever.
func TestClientWorkspace_RecoversAfterServerSideTeardown(t *testing.T) {
	xdgIsolate(t)
	t.Cleanup(workspace.SetSSEBackoffForTest(5*time.Millisecond, 25*time.Millisecond))

	rt := newRuntimeServer(t)
	rt.srv.Backend().SetDetachGrace(0)

	cwd, dataDir := t.TempDir(), t.TempDir()
	// 一个 sibling 会话 在 another 目录 keeps 服务端 alive, which
	// 是 what made 404s permanent instead 的 self-healing.
	sibling := rt.newClient(t, t.TempDir())
	siblingCwd := t.TempDir()
	_, err := sibling.CreateWorkspace(t.Context(), proto.Workspace{
		Path: siblingCwd, DataDir: t.TempDir(),
	})
	require.NoError(t, err)

	c := rt.newClient(t, cwd)
	wsProto, err := c.CreateWorkspace(t.Context(), proto.Workspace{Path: cwd, DataDir: dataDir})
	require.NoError(t, err)
	originalID := wsProto.ID

	// 服务端 drops 工作区 while 客户端 still holds
	// snapshot naming it. That 是 what upgrade, 或 teardown racing
	// 流 drop, leaves behind: live 服务端 那 answers 404 为
	// 只 工作区 ID 这 客户端 knows.
	require.NoError(t, c.DeleteWorkspace(t.Context(), originalID))
	_, err = c.GetWorkspace(t.Context(), originalID)
	require.ErrorIs(t, err, client.ErrNotFound, "the workspace must really be gone")

	ws := workspace.NewClientWorkspace(c, *wsProto)
	done := make(chan struct{})
	go func() {
		ws.RunSubscriptionForTest(func(tea.Msg) {})
		close(done)
	}()

	require.Eventually(t, func() bool {
		id := ws.WorkspaceIDForTest()
		return id != "" && id != originalID
	}, 10*time.Second, 25*time.Millisecond,
		"the client must re-register instead of retrying a workspace the server forgot")

	// recovered 工作区 是 usable, which 是 what 404s prevented.
	_, err = ws.ListSessions(t.Context())
	require.NoError(t, err, "the recovered workspace must serve requests again")

	ws.Shutdown()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the subscription loop did not stop after Shutdown")
	}
}

// TestClientWorkspace_HostThemeReachesTUI drives 主机 theme through
// real 服务端: TUI 那 attaches after host set 它的 theme receives it
// 作为 HostThemeEvent 在 once ( attach 重放), 和 clear arrives 作为
// nil theme.
func TestClientWorkspace_HostThemeReachesTUI(t *testing.T) {
	xdgIsolate(t)
	rt := newRuntimeServer(t)

	cwd := t.TempDir()
	tui := rt.newClient(t, cwd)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	wsProto, err := tui.CreateWorkspace(ctx, proto.Workspace{Path: cwd, DataDir: t.TempDir()})
	require.NoError(t, err)

	// host 是 separate 客户端 那 never creates 工作区.
	host := rt.newClient(t, cwd)
	theme := &proto.HostTheme{
		Name:    "charmtone-dark",
		Source:  "superjoy",
		Palette: map[string]string{"primary": "#6B50FF", "bg_base": "#201F26"},
		Syntax:  map[string]string{"keyword": "#00A4FF"},
	}
	st, err := host.SetHostTheme(ctx, wsProto.ID, theme)
	require.NoError(t, err)
	require.True(t, theme.Equal(st.Theme))

	evc, err := tui.SubscribeEvents(ctx, wsProto.ID)
	require.NoError(t, err)
	ws := workspace.NewClientWorkspace(tui, *wsProto)
	msgs := make(chan tea.Msg, 16)
	go ws.ConsumeEventsForTest(evc, func(msg tea.Msg) { msgs <- msg })

	next := func() workspace.HostThemeEvent {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case msg := <-msgs:
				if ev, ok := msg.(workspace.HostThemeEvent); ok {
					return ev
				}
			case <-deadline:
				require.FailNow(t, "timed out waiting for a HostThemeEvent")
			}
		}
	}
	require.True(t, theme.Equal(next().Theme), "the attach must replay the current host theme")

	_, err = host.SetHostTheme(ctx, wsProto.ID, nil)
	require.NoError(t, err)
	require.Nil(t, next().Theme)

	got, err := host.GetHostTheme(ctx, wsProto.ID)
	require.NoError(t, err)
	require.Nil(t, got.Theme)
}
