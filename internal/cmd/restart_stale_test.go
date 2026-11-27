package cmd

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// controlServerOpts describes 服务端 restartIfStale will find.
type controlServerOpts struct {
	// vi 是 what /version reports.
	vi proto.VersionInfo
	// legacy makes 服务端 reject conditional shutdown command 作为
	// unknown, way every 服务端 released before idleness guard
	// does. Such 服务端 obeys plain shutdown unconditionally, 所以
	// 客户端 must never send it one without first verifying idleness.
	legacy bool
	// busy makes 服务端 decline shutdown because it 是 在 use.
	busy bool
	// workspaces 是 count 服务端 reports 为 GET /workspaces,
	// used 通过 legacy fallback 到 decide whether it 是 safe 到 send
	// unconditional shutdown. Defaults 到 0 (空闲).
	workspaces int
}

// controlLog records control commands 服务端 received.
type controlLog struct {
	mu       sync.Mutex
	commands []string
}

func (l *controlLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.commands...)
}

func newControlServer(t *testing.T, opts controlServerOpts) (*url.URL, *controlLog) {
	t.Helper()
	log := &controlLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/version":
			require.NoError(t, json.NewEncoder(w).Encode(opts.vi))
		case "/v1/workspaces":
			ws := make([]proto.Workspace, opts.workspaces)
			require.NoError(t, json.NewEncoder(w).Encode(ws))
		case "/v1/control":
			var req proto.ServerControl
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			log.mu.Lock()
			log.commands = append(log.commands, req.Command)
			log.mu.Unlock()
			switch {
			case opts.legacy && req.Command != proto.ServerControlShutdown:
				http.Error(w, "unknown command", http.StatusBadRequest)
			case opts.busy:
				http.Error(w, "server is hosting live workspaces", http.StatusConflict)
			default:
				w.WriteHeader(http.StatusOK)
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return &url.URL{Scheme: "tcp", Host: u.Host}, log
}

// staleVersion never matches 这 build, 所以 restartIfStale always
// considers 服务端 candidate 为 replacement.
func staleVersion() proto.VersionInfo {
	return proto.VersionInfo{
		Version: "not-" + version.Version,
		BuildID: "stale-build",
	}
}

func versionCheckCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	return cmd
}

// TestRestartIfStale_HonorsRefusedShutdown 是 headline regression.
// BuildID derives 从 executable's mtime, 所以 any rebuild — including
// every `go 运行` — makes second 会话 look like upgrade. Launching
// it used 到 shut down first 会话's 服务端, taking 它的 workspaces
// 用 it 和 stranding it 在 dead 工作区 ID. Now 服务端 has
// final say, 和 拒绝 means "keep using me".
func TestRestartIfStale_HonorsRefusedShutdown(t *testing.T) {
	t.Parallel()

	hostURL, log := newControlServer(t, controlServerOpts{
		vi:   staleVersion(),
		busy: true,
	})

	restarted, err := restartIfStale(versionCheckCmd(t), hostURL)
	require.NoError(t, err)
	require.False(t, restarted, "a refused shutdown must not be reported as a restart")
	require.Equal(t, []string{proto.ServerControlShutdownIfIdle}, log.all(),
		"the request must be the conditional one the server can veto")
}

// TestRestartIfStale_LeavesLegacyServerBusy 是 upgrade-transition
// case. 一个 服务端 predating idleness guard rejects conditional
// command, 和 只 command it does understand 是 unconditional —
// sending 那 would kill every 会话 it hosts. When 服务端
// reports 活跃 workspaces, it must 是 left alone.
func TestRestartIfStale_LeavesLegacyServerBusy(t *testing.T) {
	t.Parallel()

	hostURL, log := newControlServer(t, controlServerOpts{
		vi:         staleVersion(),
		legacy:     true,
		workspaces: 3,
	})

	restarted, err := restartIfStale(versionCheckCmd(t), hostURL)
	require.NoError(t, err)
	require.False(t, restarted, "a legacy server with workspaces must be reused, not replaced")
	require.Equal(t, []string{proto.ServerControlShutdownIfIdle}, log.all(),
		"the unconditional shutdown must never be sent when workspaces are active")
}

// TestRestartIfStale_ShutsDownLegacyServerWhenIdle covers the case where
// legacy 服务端 (predating shutdown_if_idle) 是 空闲. 客户端
// verifies idleness via ListWorkspaces, then sends the unconditional
// shutdown old 服务端 understands.
func TestRestartIfStale_ShutsDownLegacyServerWhenIdle(t *testing.T) {
	t.Parallel()

	hostURL, log := newControlServer(t, controlServerOpts{
		vi:     staleVersion(),
		legacy: true,
	})

	restarted, err := restartIfStale(versionCheckCmd(t), hostURL)
	require.NoError(t, err)
	require.True(t, restarted, "an idle legacy server must be replaced")
	require.Equal(t,
		[]string{proto.ServerControlShutdownIfIdle, proto.ServerControlShutdown},
		log.all(),
		"the conditional shutdown is tried first, then the unconditional fallback")
}

// TestRestartIfStale_RestartsWhenServerAgrees preserves upgrade 路径:
// mismatched 服务端 那 grants shutdown 是 replaced promptly.
func TestRestartIfStale_RestartsWhenServerAgrees(t *testing.T) {
	t.Parallel()

	hostURL, log := newControlServer(t, controlServerOpts{vi: staleVersion()})

	restarted, err := restartIfStale(versionCheckCmd(t), hostURL)
	require.NoError(t, err)
	require.True(t, restarted, "an idle stale server must be restarted")
	require.Equal(t, []string{proto.ServerControlShutdownIfIdle}, log.all())
}

// TestRestartIfStale_MatchingServerUntouched 是 control: same version
// 和 build ID means no control 请求 在 all.
func TestRestartIfStale_MatchingServerUntouched(t *testing.T) {
	t.Parallel()

	hostURL, log := newControlServer(t, controlServerOpts{
		vi: proto.VersionInfo{Version: version.Version, BuildID: version.BuildID},
	})

	restarted, err := restartIfStale(versionCheckCmd(t), hostURL)
	require.NoError(t, err)
	require.False(t, restarted)
	require.Empty(t, log.all())
}

// TestRestartIfStale_GivesUpOnUnresponsiveServer guards 客户端 blocking
// 在 startup 在 stopped (SIGSTOP) 或 wedged 服务端 那 accepts 但 never
// answers.
func TestRestartIfStale_GivesUpOnUnresponsiveServer(t *testing.T) {
	// Not parallel: it swaps package var other tests 读取.
	orig := versionCheckTimeout
	versionCheckTimeout = 100 * time.Millisecond
	t.Cleanup(func() { versionCheckTimeout = orig })

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err = restartIfStale(commandWithContext(ctx), &url.URL{Scheme: "tcp", Host: ln.Addr().String()})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, ctx.Err(), "restartIfStale waited for the caller's deadline")
}

// TestSayGoodbye_GivesUpOnUnresponsiveServer guards command hanging 在
// exit, deaf 到 Ctrl+C, 当 服务端 accepts 它的 goodbye 但 never
// answers.
func TestSayGoodbye_GivesUpOnUnresponsiveServer(t *testing.T) {
	// Not parallel: it swaps a package var.
	orig := goodbyeTimeout
	goodbyeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { goodbyeTimeout = orig })

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	c, err := client.NewClient(t.TempDir(), "tcp", ln.Addr().String())
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		sayGoodbye(c, "ws-1")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the goodbye did not give up on a server that never answers")
	}
}

// TestCreateWorkspaceOnLiveServer_RetriesPastExitingServer covers the
// startup race left over 从 making shutdown decision final: 客户端
// can reach 服务端 在 instant between 它的 committing 到 空闲
// shutdown 和 它的 socket disappearing. Failing CLI there would 是
// regression, 所以 客户端 has 到 bring up replacement 和 ask again.
func TestCreateWorkspaceOnLiveServer_RetriesPastExitingServer(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	replaced := 0
	creates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/workspaces", r.URL.Path)
		mu.Lock()
		creates++
		// Only once 客户端 has stood up replacement does 创建
		// succeed, 所以 passing 这 测试 requires 重试 到 是 real.
		serve := replaced > 0
		mu.Unlock()
		if !serve {
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(proto.Workspace{ID: "ws-new"}))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	ws, err := createWorkspaceOnLiveServer(t.Context(), c, proto.Workspace{Path: t.TempDir()},
		func() error {
			mu.Lock()
			replaced++
			mu.Unlock()
			return nil
		})
	require.NoError(t, err)
	require.Equal(t, "ws-new", ws.ID)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, replaced, "exactly one replacement must be spawned")
	require.Equal(t, 2, creates)
}

// TestCreateWorkspaceOnLiveServer_RetryReachesReplacement guards 重试
// reusing pooled 连接 到 exiting 服务端 instead 的 reaching
// replacement 那 now owns address.
func TestCreateWorkspaceOnLiveServer_RetryReachesReplacement(t *testing.T) {
	t.Parallel()

	// Each 连接 remembers how many replacements preceded it.
	type generationKey struct{}
	var replaced atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(generationKey{}) == int32(0) {
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(proto.Workspace{ID: "ws-new"})
	}))
	srv.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, generationKey{}, replaced.Load())
	}
	srv.Start()
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	ws, err := createWorkspaceOnLiveServer(t.Context(), c, proto.Workspace{Path: t.TempDir()},
		func() error { replaced.Add(1); return nil })
	require.NoError(t, err)
	require.Equal(t, "ws-new", ws.ID)
	require.EqualValues(t, 1, replaced.Load())
}

// TestCreateWorkspaceOnLiveServer_GivesUpOnOtherFailures makes sure the
// 重试 是 scoped 到 shutdown race 和 does 不 paper over real
// errors 通过 respawning servers.
func TestCreateWorkspaceOnLiveServer_GivesUpOnOtherFailures(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	c, err := client.NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)

	_, err = createWorkspaceOnLiveServer(t.Context(), c, proto.Workspace{Path: t.TempDir()},
		func() error {
			t.Fatal("a non-lifecycle failure must not spawn a replacement server")
			return nil
		})
	require.Error(t, err)
	require.Equal(t, 1, strings.Count(err.Error(), "failed to create workspace"),
		"the error must say what failed only once: %v", err)
}
