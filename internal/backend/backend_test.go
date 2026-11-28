package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// newTestBackend returns Backend whose teardown 路径 skips any
// real [app.App] shutdown work. Useful 为 state-machine tests 那
// install synthetic workspaces directly via insertTestWorkspace.
func newTestBackend(t *testing.T) (*Backend, *atomic.Int32) {
	t.Helper()
	var shutdownCount atomic.Int32
	b := &Backend{
		workspaces:   csync.NewMap[string, *Workspace](),
		pathIndex:    make(map[string]string),
		ctx:          context.Background(),
		createGrace:  50 * time.Millisecond,
		idleShutdown: true,
		autoClose:    true,
		shutdownFn:   func() { shutdownCount.Add(1) },
	}
	return b, &shutdownCount
}

// insertTestWorkspace installs synthetic 工作区 into b 在
// given resolved 路径. Its shutdownFn 是 recorded 在 returned
// 计数器 所以 tests can assert it ran exactly once.
func insertTestWorkspace(t *testing.T, b *Backend, key string) (*Workspace, *atomic.Int32) {
	t.Helper()
	var shutdowns atomic.Int32
	ws := &Workspace{
		ID:           uuid.New().String(),
		Path:         key,
		resolvedPath: key,
		clients:      make(map[string]*clientState),
		shutdownFn:   func() { shutdowns.Add(1) },
	}
	b.mu.Lock()
	b.workspaces.Set(ws.ID, ws)
	b.pathIndex[key] = ws.ID
	b.mu.Unlock()
	return ws, &shutdowns
}

func newClientID(t *testing.T) string {
	t.Helper()
	return uuid.New().String()
}

func TestResolveWorkspaceKey_AbsoluteAndSymlink(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	real, err := filepath.EvalSymlinks(tmp)
	require.NoError(t, err)

	got, err := resolveWorkspaceKey(tmp)
	require.NoError(t, err)
	require.Equal(t, real, got)
}

func TestResolveWorkspaceKey_NonExistentFallback(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	got, err := resolveWorkspaceKey(missing)
	require.NoError(t, err)
	abs, err := filepath.Abs(missing)
	require.NoError(t, err)
	require.Equal(t, abs, got)
}

func TestValidateClientID(t *testing.T) {
	t.Parallel()

	_, err := validateClientID("")
	require.ErrorIs(t, err, ErrInvalidClientID)
	_, err = validateClientID("not-a-uuid")
	require.ErrorIs(t, err, ErrInvalidClientID)

	id := uuid.New().String()
	got, err := validateClientID(id)
	require.NoError(t, err)
	require.Equal(t, id, got)
}

func TestRegisterClient_Idempotent(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/a")

	cid := newClientID(t)
	b.registerClient(ws, cid)
	b.registerClient(ws, cid)

	ws.clientsMu.Lock()
	defer ws.clientsMu.Unlock()
	require.Len(t, ws.clients, 1)
	require.NotNil(t, ws.clients[cid].holdTimer)
	require.Equal(t, 0, ws.clients[cid].streams)
}

func TestAttachClient_ConsumesHold(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, shutdowns := insertTestWorkspace(t, b, "/tmp/a")

	cid := newClientID(t)
	b.registerClient(ws, cid)
	require.NoError(t, b.AttachClient(ws.ID, cid))

	ws.clientsMu.Lock()
	require.Len(t, ws.clients, 1)
	require.Nil(t, ws.clients[cid].holdTimer, "attach must stop the grace timer")
	require.Equal(t, 1, ws.clients[cid].streams)
	ws.clientsMu.Unlock()

	// Wait past grace 窗口: stopped timer must 不 fire.
	time.Sleep(150 * time.Millisecond)
	require.Equal(t, int32(0), shutdowns.Load(), "workspace must not be torn down while attached")
}

func TestAttachClient_WithoutPriorCreate(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/a")

	cid := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, cid))

	ws.clientsMu.Lock()
	defer ws.clientsMu.Unlock()
	require.Len(t, ws.clients, 1)
	require.Equal(t, 1, ws.clients[cid].streams)
	require.Nil(t, ws.clients[cid].holdTimer)
}

func TestAttachClient_DuplicateStreams(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, shutdowns := insertTestWorkspace(t, b, "/tmp/a")

	cid := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, cid))
	require.NoError(t, b.AttachClient(ws.ID, cid))

	ws.clientsMu.Lock()
	require.Equal(t, 2, ws.clients[cid].streams)
	ws.clientsMu.Unlock()

	b.DetachClient(ws.ID, cid)
	ws.clientsMu.Lock()
	require.Equal(t, 1, ws.clients[cid].streams)
	ws.clientsMu.Unlock()
	require.Equal(t, int32(0), shutdowns.Load())

	b.DetachClient(ws.ID, cid)
	require.Equal(t, int32(1), shutdowns.Load(), "second detach tears down the workspace")
}

func TestDetachClient_LastStreamTearsDown(t *testing.T) {
	t.Parallel()

	b, srvShutdowns := newTestBackend(t)
	ws, wsShutdowns := insertTestWorkspace(t, b, "/tmp/a")

	cid := newClientID(t)
	b.registerClient(ws, cid)
	require.NoError(t, b.AttachClient(ws.ID, cid))
	b.DetachClient(ws.ID, cid)

	require.Equal(t, int32(1), wsShutdowns.Load())
	require.Equal(t, int32(1), srvShutdowns.Load(), "last workspace shut down must trigger server shutdown")
	_, err := b.GetWorkspace(ws.ID)
	require.ErrorIs(t, err, ErrWorkspaceNotFound)
}

func TestHoldExpiry_TearsDown(t *testing.T) {
	t.Parallel()

	b, srvShutdowns := newTestBackend(t)
	ws, wsShutdowns := insertTestWorkspace(t, b, "/tmp/a")

	cid := newClientID(t)
	b.registerClient(ws, cid)

	require.Eventually(t, func() bool {
		return wsShutdowns.Load() == 1 && srvShutdowns.Load() == 1
	}, 1*time.Second, 5*time.Millisecond)
}

func TestReleaseHold_NoStreams(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, shutdowns := insertTestWorkspace(t, b, "/tmp/a")

	cid := newClientID(t)
	b.registerClient(ws, cid)
	require.NoError(t, b.releaseHold(ws.ID, cid))

	require.Equal(t, int32(1), shutdowns.Load())
	// Idempotent.
	require.NoError(t, b.releaseHold(ws.ID, cid))
	require.Equal(t, int32(1), shutdowns.Load())
}

func TestReleaseHold_WithActiveStream(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, shutdowns := insertTestWorkspace(t, b, "/tmp/a")

	cid := newClientID(t)
	b.registerClient(ws, cid)
	require.NoError(t, b.AttachClient(ws.ID, cid))
	require.NoError(t, b.releaseHold(ws.ID, cid))

	ws.clientsMu.Lock()
	require.Equal(t, 1, ws.clients[cid].streams)
	require.Nil(t, ws.clients[cid].holdTimer)
	ws.clientsMu.Unlock()
	require.Equal(t, int32(0), shutdowns.Load())

	b.DetachClient(ws.ID, cid)
	require.Equal(t, int32(1), shutdowns.Load())
}

func TestReleaseHoldThenAttach(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, shutdowns := insertTestWorkspace(t, b, "/tmp/a")

	cid := newClientID(t)
	require.NoError(t, b.releaseHold(ws.ID, cid)) // no entry yet — no-op.
	require.NoError(t, b.AttachClient(ws.ID, cid))
	ws.clientsMu.Lock()
	require.Equal(t, 1, ws.clients[cid].streams)
	ws.clientsMu.Unlock()
	require.NoError(t, b.releaseHold(ws.ID, cid)) // hold-only no-op (no hold timer).
	require.Equal(t, int32(0), shutdowns.Load())
	b.DetachClient(ws.ID, cid)
	require.Equal(t, int32(1), shutdowns.Load())
}

func TestRefcountWithSecondClient(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, shutdowns := insertTestWorkspace(t, b, "/tmp/a")

	cidA := newClientID(t)
	cidB := newClientID(t)
	b.registerClient(ws, cidA)
	require.NoError(t, b.AttachClient(ws.ID, cidA))
	b.registerClient(ws, cidB)
	require.NoError(t, b.AttachClient(ws.ID, cidB))

	b.DetachClient(ws.ID, cidA)
	ws.clientsMu.Lock()
	require.Contains(t, ws.clients, cidB)
	require.NotContains(t, ws.clients, cidA)
	ws.clientsMu.Unlock()
	require.Equal(t, int32(0), shutdowns.Load(), "workspace survives while second client attached")

	b.DetachClient(ws.ID, cidB)
	require.Equal(t, int32(1), shutdowns.Load())
}

func TestAttachClient_InvalidID(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/a")

	require.ErrorIs(t, b.AttachClient(ws.ID, ""), ErrInvalidClientID)
	require.ErrorIs(t, b.AttachClient(ws.ID, "not-a-uuid"), ErrInvalidClientID)
}

func TestDeleteWorkspace_RejectsBadClientID(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/a")

	require.ErrorIs(t, b.DeleteWorkspace(ws.ID, ""), ErrInvalidClientID)
	require.ErrorIs(t, b.DeleteWorkspace(ws.ID, "not-a-uuid"), ErrInvalidClientID)
}

// TestHoldExpiry_RaceWithAttach checks 那, 当 grace timer fires
// while concurrent AttachClient 调用 是 在 flight, 工作区 ends
// up either fully attached 或 fully torn down — never 在 half-state.
func TestHoldExpiry_RaceWithAttach(t *testing.T) {
	t.Parallel()

	for i := range 50 {
		b, _ := newTestBackend(t)
		// Tighten grace 窗口 further 到 force race.
		b.createGrace = 1 * time.Millisecond
		ws, shutdowns := insertTestWorkspace(t, b, "/tmp/race")

		cid := newClientID(t)
		b.registerClient(ws, cid)
		// Attach concurrently 用 very short grace timer.
		errCh := make(chan error, 1)
		go func() { errCh <- b.AttachClient(ws.ID, cid) }()
		<-errCh

		// Wait 为 any pending timer 到 settle.
		time.Sleep(10 * time.Millisecond)

		ws.clientsMu.Lock()
		gotShutdown := shutdowns.Load() == 1
		cs, present := ws.clients[cid]
		var (
			gotStreams   int
			gotHoldTimer *time.Timer
		)
		if present {
			gotStreams = cs.streams
			gotHoldTimer = cs.holdTimer
		}
		ws.clientsMu.Unlock()
		// Either 工作区 was torn down OR 客户端 是
		// attached 用 streams==1 和 hold timer cleared.
		// state must 是 consistent: 如果 shutdown, 客户端 是
		// gone; 如果 attached, no teardown 和 streams==1.
		if gotShutdown {
			require.False(t, present, "iter %d: shutdown but client still present", i)
		} else {
			require.True(t, present, "iter %d: not shutdown but client missing", i)
			require.Equal(t, 1, gotStreams, "iter %d: attach winner must leave streams=1", i)
			require.Nil(t, gotHoldTimer, "iter %d: attach winner must clear holdTimer", i)
		}
	}
}

// TestConcurrentAttachDetach exercises the state machine under
// parallel attach/detach pressure 用 race detector.
func TestConcurrentAttachDetach(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/a")

	cid := newClientID(t)
	b.registerClient(ws, cid)
	require.NoError(t, b.AttachClient(ws.ID, cid)) // ensure refcount stays > 0.

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			cid2 := newClientID(t)
			_ = b.AttachClient(ws.ID, cid2)
			b.DetachClient(ws.ID, cid2)
		}()
	}
	wg.Wait()

	ws.clientsMu.Lock()
	defer ws.clientsMu.Unlock()
	require.Len(t, ws.clients, 1)
	require.Contains(t, ws.clients, cid)
}

// TestPathDedupe_FullCreate exercises CreateWorkspace end-到-end
// (配置 init, real app.App). Two CreateWorkspace calls 在 same
// 路径 return same 工作区 ID 和 share clients map.
func TestPathDedupe_FullCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cwd := t.TempDir()
	dataDir := t.TempDir()

	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })

	cidA := uuid.New().String()
	cidB := uuid.New().String()

	wsA, protoA, err := b.CreateWorkspace(protoWS(cwd, dataDir, cidA))
	require.NoError(t, err)
	require.NotEmpty(t, protoA.ID)
	require.Equal(t, protoA.DataDir, wsA.Cfg.Config().Options.DataDirectory)

	wsB, protoB, err := b.CreateWorkspace(protoWS(cwd, dataDir, cidB))
	require.NoError(t, err)
	require.Equal(t, wsA.ID, wsB.ID, "second create at same path must return existing workspace")
	require.Equal(t, protoA.ID, protoB.ID)

	wsA.clientsMu.Lock()
	require.Contains(t, wsA.clients, cidA)
	require.Contains(t, wsA.clients, cidB)
	wsA.clientsMu.Unlock()
}

// TestPathDedupe_DifferentPaths_DifferentWorkspaces confirms 那 two
// CreateWorkspace calls 在 distinct paths produce distinct workspaces.
func TestPathDedupe_DifferentPaths_DifferentWorkspaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cwdA := t.TempDir()
	cwdB := t.TempDir()
	dataA := t.TempDir()
	dataB := t.TempDir()

	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })

	wsA, _, err := b.CreateWorkspace(protoWS(cwdA, dataA, uuid.New().String()))
	require.NoError(t, err)
	wsB, _, err := b.CreateWorkspace(protoWS(cwdB, dataB, uuid.New().String()))
	require.NoError(t, err)
	require.NotEqual(t, wsA.ID, wsB.ID)
}

// TestPathDedupe_FirstWinsKeepsOriginalEnv verifies 那 second
// 创建 在 same 路径 returns *originating* 客户端's Env 在
// 它的 proto 和 does 不 mutate existing 工作区's PermissionMode/Debug
// flags.
func TestPathDedupe_FirstWinsKeepsOriginalEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cwd := t.TempDir()
	dataDir := t.TempDir()

	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })

	originalEnv := []string{"FOO=bar"}
	argsA := protoWS(cwd, dataDir, uuid.New().String())
	argsA.PermissionMode = proto.WorkspacePermissionModeYolo
	argsA.Env = originalEnv
	wsA, protoA, err := b.CreateWorkspace(argsA)
	require.NoError(t, err)
	require.Equal(t, proto.WorkspacePermissionModeYolo, protoA.PermissionMode)
	require.Equal(t, originalEnv, wsA.Env)
	require.Empty(t, protoA.Env, "proto responses must not leak workspace Env")

	argsB := protoWS(cwd, dataDir, uuid.New().String())
	argsB.PermissionMode = proto.WorkspacePermissionModeNormal
	argsB.Debug = true
	argsB.Env = []string{"BAZ=qux"}
	wsB, protoB, err := b.CreateWorkspace(argsB)
	require.NoError(t, err)
	require.Equal(t, protoA.ID, protoB.ID)
	require.Equal(t, proto.WorkspacePermissionModeYolo, protoB.PermissionMode, "first wins: PermissionMode must remain yolo")
	require.Equal(t, originalEnv, wsB.Env, "workspace must keep the originating client's Env")
	require.Empty(t, protoB.Env, "proto responses must not leak workspace Env")
	require.Equal(t, permission.PermissionModeYolo, wsA.Cfg.Overrides().PermissionMode)

	for _, listed := range b.ListWorkspaces() {
		require.Empty(t, listed.Env, "ListWorkspaces must not leak workspace Env")
	}
	got, err := b.GetWorkspaceProto(wsA.ID)
	require.NoError(t, err)
	require.Empty(t, got.Env, "GetWorkspaceProto must not leak workspace Env")
}

// TestPathDedupe_Symlink confirms two paths 那 resolve 到 same
// target share 工作区.
func TestPathDedupe_Symlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))
	dataDir := t.TempDir()

	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })

	wsA, _, err := b.CreateWorkspace(protoWS(real, dataDir, uuid.New().String()))
	require.NoError(t, err)
	wsB, _, err := b.CreateWorkspace(protoWS(link, dataDir, uuid.New().String()))
	require.NoError(t, err)
	require.Equal(t, wsA.ID, wsB.ID)
}

// TestPathDedupe_NonExistentPath ensures CreateWorkspace tolerates a
// 路径 那 does 不 yet exist (EvalSymlinks falls back 到 Abs).
func TestPathDedupe_NonExistentPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	parent := t.TempDir()
	missing := filepath.Join(parent, "does-not-exist")
	dataDir := t.TempDir()

	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })

	_, p, err := b.CreateWorkspace(protoWS(missing, dataDir, uuid.New().String()))
	require.NoError(t, err)
	require.NotEmpty(t, p.ID)
}

// TestCreateWorkspace_IdempotentSameClient checks 那 duplicate
// 创建 从 same 客户端 在 same 路径 does 不 produce
// second claim.
func TestCreateWorkspace_IdempotentSameClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cwd := t.TempDir()
	dataDir := t.TempDir()
	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })

	cid := uuid.New().String()
	ws1, _, err := b.CreateWorkspace(protoWS(cwd, dataDir, cid))
	require.NoError(t, err)
	ws2, _, err := b.CreateWorkspace(protoWS(cwd, dataDir, cid))
	require.NoError(t, err)
	require.Equal(t, ws1.ID, ws2.ID)

	ws1.clientsMu.Lock()
	require.Len(t, ws1.clients, 1, "duplicate create from same client must not double the claim")
	ws1.clientsMu.Unlock()
}

// TestPathDedupe_ParallelCreates ensures two simultaneous CreateWorkspace
// calls 在 same 路径 produce same 工作区 和 clients map
// contains both 客户端 IDs.
func TestPathDedupe_ParallelCreates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cwd := t.TempDir()
	dataDir := t.TempDir()

	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })

	cidA := uuid.New().String()
	cidB := uuid.New().String()

	type result struct {
		ws    *Workspace
		proto proto.Workspace
		err   error
	}
	ch := make(chan result, 2)
	start := make(chan struct{})
	go func() {
		<-start
		ws, p, err := b.CreateWorkspace(protoWS(cwd, dataDir, cidA))
		ch <- result{ws, p, err}
	}()
	go func() {
		<-start
		ws, p, err := b.CreateWorkspace(protoWS(cwd, dataDir, cidB))
		ch <- result{ws, p, err}
	}()
	close(start)
	r1 := <-ch
	r2 := <-ch
	require.NoError(t, r1.err)
	require.NoError(t, r2.err)
	require.Equal(t, r1.ws.ID, r2.ws.ID, "both creates must converge on one workspace ID")

	ws := r1.ws
	ws.clientsMu.Lock()
	defer ws.clientsMu.Unlock()
	require.Contains(t, ws.clients, cidA)
	require.Contains(t, ws.clients, cidB)
}

// TestPathDedupe_CaseVariant guards against one 目录 opened under
// two spellings 那 differ 只 在 case becoming two workspaces,
// second failing 在 data-目录 锁 first holds.
func TestPathDedupe_CaseVariant(t *testing.T) {
	xdgIsolated(t)
	t.Setenv("CRUSH_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "Proj"), 0o755))
	if _, err := os.Stat(filepath.Join(root, "proj")); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })

	wsA, _, err := b.CreateWorkspace(protoWS(filepath.Join(root, "Proj"), "", uuid.NewString()))
	require.NoError(t, err)
	wsB, _, err := b.CreateWorkspace(protoWS(filepath.Join(root, "proj"), "", uuid.NewString()))
	require.NoError(t, err)
	require.Equal(t, wsA.ID, wsB.ID)
}

// TestCreateWorkspace_RejectsBadClientID covers 400 路径 从
// backend side.
func TestCreateWorkspace_RejectsBadClientID(t *testing.T) {
	t.Parallel()

	b := New(context.Background(), nil, func() {})

	_, _, err := b.CreateWorkspace(protoWS("/tmp/x", t.TempDir(), ""))
	require.ErrorIs(t, err, ErrInvalidClientID)
	_, _, err = b.CreateWorkspace(protoWS("/tmp/x", t.TempDir(), "not-a-uuid"))
	require.ErrorIs(t, err, ErrInvalidClientID)
}

// drainBackend tears backend down 在 end 的 测试 通过 deleting
// every remaining 工作区. Necessary 所以 测试 process doesn't
// leak goroutines 或 DB handles 从 embedded [app.App] instances.
func drainBackend(t *testing.T, b *Backend) {
	t.Helper()
	for _, ws := range b.workspaces.Seq2() {
		ws.clientsMu.Lock()
		ids := make([]string, 0, len(ws.clients))
		for id := range ws.clients {
			ids = append(ids, id)
		}
		ws.clientsMu.Unlock()
		for _, cid := range ids {
			_ = b.releaseHold(ws.ID, cid)
		}
	}
}

func protoWS(path, dataDir, clientID string) proto.Workspace {
	return proto.Workspace{Path: path, DataDir: dataDir, ClientID: clientID}
}

// syncBuffer 是 thread-safe 缓冲 那 can 是 safely 读取 和 written
// 从 multiple goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *syncBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

// captureDebugLogs installs 缓冲-backed slog 处理器 在 Debug
// level 为 duration 的 测试, returning 缓冲.
// previous 默认 处理器 是 restored via t.清理.
func captureDebugLogs(t *testing.T) *syncBuffer {
	t.Helper()
	var sb syncBuffer
	prev := slog.Default()
	handler := slog.NewTextHandler(&sb, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &sb
}

// xdgIsolated points HOME 和 XDG_* variables 在 fresh tempdirs 所以
// CreateWorkspace's 配置 loading does 不 interfere 用 host
// machine's real 配置.
func xdgIsolated(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

// TestFirstWinsMismatch_LogsOnFlagDifferences verifies 那
// debug mismatch line 是 emitted 当 any 的 PermissionMode, Debug, DataDir,
// 或 Env differs between first 和 second CreateWorkspace 在
// same 路径, 和 那 existing 工作区's Debug flag 是
// 不 overwritten.
func TestFirstWinsMismatch_LogsOnFlagDifferences(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*proto.Workspace)
	}{
		{
			name:   "yolo",
			mutate: func(p *proto.Workspace) { p.PermissionMode = proto.WorkspacePermissionModeYolo },
		},
		{
			name:   "debug",
			mutate: func(p *proto.Workspace) { p.Debug = true },
		},
		{
			name:   "datadir",
			mutate: func(p *proto.Workspace) { p.DataDir = "" },
		},
		{
			name:   "env",
			mutate: func(p *proto.Workspace) { p.Env = []string{"NEW=val"} },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			xdgIsolated(t)
			cwd := t.TempDir()
			dataDir := t.TempDir()

			buf := captureDebugLogs(t)
			b := New(context.Background(), nil, func() {})
			b.SetCreateGrace(2 * time.Second)
			t.Cleanup(func() { drainBackend(t, b) })

			argsA := protoWS(cwd, dataDir, uuid.New().String())
			argsA.Env = []string{"FOO=bar", "SECRET_A=hunter2"}
			wsA, _, err := b.CreateWorkspace(argsA)
			require.NoError(t, err)
			originalDebug := wsA.Cfg.Config().Options.Debug
			originalMode := wsA.Cfg.Overrides().PermissionMode

			argsB := protoWS(cwd, dataDir, uuid.New().String())
			argsB.Env = []string{"FOO=bar", "SECRET_A=hunter2"} // identical by default
			tc.mutate(&argsB)
			_, _, err = b.CreateWorkspace(argsB)
			require.NoError(t, err)

			require.Contains(
				t, buf.String(),
				"Workspace flag mismatch on duplicate create",
				"expected debug log for mismatching %s", tc.name,
			)
			require.NotContains(t, buf.String(), "hunter2", "env values must not be logged")
			require.NotContains(t, buf.String(), "NEW=val", "env values must not be logged")
			// Existing 工作区's PermissionMode 和 Debug must 不 change.
			require.Equal(t, originalMode, wsA.Cfg.Overrides().PermissionMode, "PermissionMode must be immutable on first-wins")
			require.Equal(t, originalDebug, wsA.Cfg.Config().Options.Debug, "Debug must be immutable on first-wins")
		})
	}
}

// TestFirstWinsMismatch_NoLogWhenIdentical confirms identical args
// do 不 emit mismatch log line.
func TestFirstWinsMismatch_NoLogWhenIdentical(t *testing.T) {
	xdgIsolated(t)
	cwd := t.TempDir()
	dataDir := t.TempDir()

	buf := captureDebugLogs(t)
	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })

	argsA := protoWS(cwd, dataDir, uuid.New().String())
	argsA.Env = []string{"FOO=bar"}
	_, _, err := b.CreateWorkspace(argsA)
	require.NoError(t, err)

	argsB := protoWS(cwd, dataDir, uuid.New().String())
	argsB.Env = []string{"FOO=bar"}
	_, _, err = b.CreateWorkspace(argsB)
	require.NoError(t, err)

	require.False(t,
		strings.Contains(buf.String(), "Workspace flag mismatch on duplicate create"),
		"identical args must not log a mismatch: %s", buf.String())
}

// TestChannelOptInBoundary_DuplicateCreate verifies 那 channels 是
// explicit opt-在 那 是 never shared across duplicate 创建 在
// same 路径. 一个 second 客户端 whose requested channels differ 从
// existing 工作区 (including 客户端 那 did 不 opt 在 在
// all) 是 rejected rather than silently inheriting existing
// 工作区's channels.
func TestChannelOptInBoundary_DuplicateCreate(t *testing.T) {
	tests := []struct {
		name            string
		firstChannels   []string
		secondChannels  []string
		wantMismatchErr bool
	}{
		{
			name:            "second omits opt-in",
			firstChannels:   []string{"server:webhook"},
			secondChannels:  nil,
			wantMismatchErr: true,
		},
		{
			name:            "second opts into extra channel",
			firstChannels:   nil,
			secondChannels:  []string{"server:webhook"},
			wantMismatchErr: true,
		},
		{
			name:            "different channels",
			firstChannels:   []string{"server:webhook"},
			secondChannels:  []string{"server:events"},
			wantMismatchErr: true,
		},
		{
			name:            "identical channels shared",
			firstChannels:   []string{"server:webhook"},
			secondChannels:  []string{"server:webhook"},
			wantMismatchErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			xdgIsolated(t)
			cwd := t.TempDir()
			dataDir := t.TempDir()

			b := New(context.Background(), nil, func() {})
			b.SetCreateGrace(2 * time.Second)
			t.Cleanup(func() { drainBackend(t, b) })

			argsA := protoWS(cwd, dataDir, uuid.New().String())
			argsA.Channels = tc.firstChannels
			wsA, _, err := b.CreateWorkspace(argsA)
			require.NoError(t, err)

			argsB := protoWS(cwd, dataDir, uuid.New().String())
			argsB.Channels = tc.secondChannels
			wsB, protoB, err := b.CreateWorkspace(argsB)

			if tc.wantMismatchErr {
				require.ErrorIs(t, err, ErrChannelOptInMismatch)
				require.Nil(t, wsB)
				// existing 工作区 must 不 是 mutated
				// 和 must 不 register rejected 客户端.
				require.Equal(t, tc.firstChannels, wsA.Cfg.Overrides().EnabledChannels,
					"existing channels must be immutable on rejection")
				wsA.clientsMu.Lock()
				require.Len(t, wsA.clients, 1, "rejected client must not be registered")
				wsA.clientsMu.Unlock()
				return
			}

			require.NoError(t, err)
			require.Equal(t, wsA.ID, protoB.ID)
			require.Equal(t, tc.firstChannels, protoB.Channels)
		})
	}
}

// TestRaceTwoClientsAttachOneDetaches exercises the PLAN-required
// race scenario: two clients attach concurrently, then one detaches.
// 工作区 must remain alive 用 refcount==1 和 clients
// map must reflect remaining 客户端 只.
func TestRaceTwoClientsAttachOneDetaches(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, shutdowns := insertTestWorkspace(t, b, "/tmp/race-two")

	cidA := newClientID(t)
	cidB := newClientID(t)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		require.NoError(t, b.AttachClient(ws.ID, cidA))
	}()
	go func() {
		defer wg.Done()
		require.NoError(t, b.AttachClient(ws.ID, cidB))
	}()
	wg.Wait()

	ws.clientsMu.Lock()
	require.Len(t, ws.clients, 2, "both clients must be attached")
	ws.clientsMu.Unlock()

	b.DetachClient(ws.ID, cidA)

	ws.clientsMu.Lock()
	require.Len(t, ws.clients, 1, "refcount must be 1 after one detach")
	require.Contains(t, ws.clients, cidB, "remaining client must be cidB")
	require.NotContains(t, ws.clients, cidA, "detached client must be removed")
	ws.clientsMu.Unlock()
	require.Equal(t, int32(0), shutdowns.Load(), "workspace must remain alive")

	// Drain.
	b.DetachClient(ws.ID, cidB)
	require.Equal(t, int32(1), shutdowns.Load())
}

// TestExplicitDeleteThenAttach reproduces the PLAN scenario: start
// 用 real hold, releaseHold consumes it, AttachClient 从
// same clientID creates fresh 条目 用 streams==1, 和 calling
// releaseHold again 是 no-op. 一个 second 客户端 keeps 工作区
// alive 所以 AttachClient can still resolve 工作区 ID after
// first 客户端's hold 是 released.
func TestExplicitDeleteThenAttach(t *testing.T) {
	t.Parallel()

	// Large grace 窗口 所以 timers cannot fire during 测试
	// — we want 到 exercise explicit releaseHold 路径.
	b, _ := newTestBackend(t)
	b.createGrace = time.Hour
	ws, shutdowns := insertTestWorkspace(t, b, "/tmp/delete-then-attach")

	// Anchor 客户端 keeps 工作区 registered 在
	// b.workspaces across the cid's releaseHold below.
	anchor := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, anchor))

	cid := newClientID(t)
	// Real hold via registerClient (mirrors CreateWorkspace).
	b.registerClient(ws, cid)
	ws.clientsMu.Lock()
	require.Contains(t, ws.clients, cid)
	require.NotNil(t, ws.clients[cid].holdTimer, "hold must be live")
	require.Equal(t, 0, ws.clients[cid].streams)
	ws.clientsMu.Unlock()

	// releaseHold: consumes hold 和 removes 条目
	// (streams == 0). anchor 客户端 keeps 工作区
	// alive.
	require.NoError(t, b.releaseHold(ws.ID, cid))
	require.Equal(t, int32(0), shutdowns.Load(), "anchor must keep workspace alive")
	ws.clientsMu.Lock()
	require.NotContains(t, ws.clients, cid, "entry must be removed by releaseHold")
	ws.clientsMu.Unlock()

	// AttachClient creates fresh 条目 用 streams==1 和 no
	// hold timer.
	require.NoError(t, b.AttachClient(ws.ID, cid))
	ws.clientsMu.Lock()
	require.Contains(t, ws.clients, cid, "fresh entry must be created")
	require.Equal(t, 1, ws.clients[cid].streams, "fresh attach must start at streams=1")
	require.Nil(t, ws.clients[cid].holdTimer, "fresh attach must have no hold timer")
	ws.clientsMu.Unlock()

	// Calling releaseHold again 是 no-op (no hold timer 到
	// stop, streams > 0 所以 条目 stays).
	require.NoError(t, b.releaseHold(ws.ID, cid))
	ws.clientsMu.Lock()
	require.Contains(t, ws.clients, cid, "releaseHold must not touch a stream-only entry")
	require.Equal(t, 1, ws.clients[cid].streams)
	require.Nil(t, ws.clients[cid].holdTimer)
	ws.clientsMu.Unlock()

	// Drain.
	b.DetachClient(ws.ID, cid)
	b.DetachClient(ws.ID, anchor)
	require.Equal(t, int32(1), shutdowns.Load())
}

// TestAttachClient_RacesWithTeardown forces AttachClient 到 compete
// 用 teardown 路径 triggered 通过 DetachClient. Before fix,
// AttachClient could observe 工作区 after teardown had already
// decided 到 remove it (because AttachClient did 不 synchronize 用
// Backend.mu), leaving live 流 claim attached 到 工作区
// 那 was then removed 和 shut down. With fix, outcome must
// 是 deterministic: either AttachClient won 和 工作区 是
// alive 用 客户端 registered, 或 teardown won 和 AttachClient
// returns ErrWorkspaceNotFound — never a half-state where the
// 工作区 是 gone 但 ws.clients still contains new 客户端.
func TestAttachClient_RacesWithTeardown(t *testing.T) {
	t.Parallel()

	for i := range 200 {
		b, _ := newTestBackend(t)
		// Keep grace 窗口 long 所以 it can't fire during
		// 测试 和 confuse bookkeeping.
		b.createGrace = time.Hour
		ws, shutdowns := insertTestWorkspace(t, b, "/tmp/race-teardown")

		// Seed: cidA holds 工作区 open via 流.
		// imminent DetachClient(cidA) will 是 *只* claim
		// drop, 所以 teardown will 运行.
		cidA := newClientID(t)
		require.NoError(t, b.AttachClient(ws.ID, cidA))

		// cidB attempts 到 attach concurrently 用 detach
		// 那 will tear 工作区 down.
		cidB := newClientID(t)
		start := make(chan struct{})
		errCh := make(chan error, 1)
		detachDone := make(chan struct{})
		go func() {
			<-start
			errCh <- b.AttachClient(ws.ID, cidB)
		}()
		go func() {
			<-start
			b.DetachClient(ws.ID, cidA)
			close(detachDone)
		}()
		close(start)

		// Wait 为 both goroutines 所以 teardown (including
		// shutdownFn) has fully 运行 before we 读取 state.
		attachErr := <-errCh
		<-detachDone

		_, wsStillRegistered := b.workspaces.Get(ws.ID)
		ws.clientsMu.Lock()
		_, hasA := ws.clients[cidA]
		_, hasB := ws.clients[cidB]
		clientCount := len(ws.clients)
		ws.clientsMu.Unlock()
		shutdownCount := shutdowns.Load()

		switch {
		case attachErr == nil:
			// AttachClient won. 工作区 must 是 alive
			// (registered) 用 cidB 在 它的 clients map. cidA
			// may 或 may 不 still 是 there depending 在 who
			// took clientsMu first, 但 工作区 must
			// 不 have been torn down.
			require.True(t, wsStillRegistered,
				"iter %d: attach succeeded but workspace was removed", i)
			require.True(t, hasB,
				"iter %d: attach succeeded but cidB missing from clients", i)
			require.Equal(t, int32(0), shutdownCount,
				"iter %d: attach succeeded but workspace was shut down", i)
		case errors.Is(attachErr, ErrWorkspaceNotFound):
			// Teardown won. 工作区 must 是 removed,
			// shut down exactly once, 和 ws.clients must 是
			// empty (no half-state 用 cidB inserted into
			// dead 工作区's clients map).
			require.False(t, wsStillRegistered,
				"iter %d: ErrWorkspaceNotFound but workspace still registered", i)
			require.Equal(t, int32(1), shutdownCount,
				"iter %d: ErrWorkspaceNotFound but shutdown count = %d", i, shutdownCount)
			require.False(t, hasA,
				"iter %d: teardown won but cidA still in clients", i)
			require.False(t, hasB,
				"iter %d: teardown won but cidB still in clients (would be the leaked attach)", i)
			require.Zero(t, clientCount,
				"iter %d: teardown won but clients map is non-empty", i)
		default:
			t.Fatalf("iter %d: unexpected AttachClient error: %v", i, attachErr)
		}
	}
}

// TestSetCurrentSession_BasicAttachAndSwitch verifies happy 路径:
// attached 客户端 can set 它的 current 会话, second attached
// 客户端 can target same 会话, 和 one 的 them can switch 到
// different 会话 without disturbing other's record.
func TestSetCurrentSession_BasicAttachAndSwitch(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/current-session-basic")

	cidA := newClientID(t)
	cidB := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, cidA))
	require.NoError(t, b.AttachClient(ws.ID, cidB))

	require.NoError(t, b.SetCurrentSession(context.Background(), ws.ID, cidA, "S1"))
	ws.clientsMu.Lock()
	require.Equal(t, "S1", ws.clients[cidA].currentSessionID)
	ws.clientsMu.Unlock()

	require.NoError(t, b.SetCurrentSession(context.Background(), ws.ID, cidB, "S1"))
	ws.clientsMu.Lock()
	require.Equal(t, "S1", ws.clients[cidA].currentSessionID)
	require.Equal(t, "S1", ws.clients[cidB].currentSessionID)
	ws.clientsMu.Unlock()

	// B switches 到 S2; counts redistribute.
	require.NoError(t, b.SetCurrentSession(context.Background(), ws.ID, cidB, "S2"))
	ws.clientsMu.Lock()
	require.Equal(t, "S1", ws.clients[cidA].currentSessionID)
	require.Equal(t, "S2", ws.clients[cidB].currentSessionID)
	ws.clientsMu.Unlock()

	// 一个 clears 它的 selection.
	require.NoError(t, b.SetCurrentSession(context.Background(), ws.ID, cidA, ""))
	ws.clientsMu.Lock()
	require.Empty(t, ws.clients[cidA].currentSessionID)
	require.Equal(t, "S2", ws.clients[cidB].currentSessionID)
	ws.clientsMu.Unlock()

	// Drain 到 release 工作区.
	b.DetachClient(ws.ID, cidA)
	b.DetachClient(ws.ID, cidB)
}

// TestSetCurrentSession_DetachClearsEntry verifies the implicit
// 清理: once 客户端's [clientState] 条目 是 removed (last
// 流 closed), 它的 currentSessionID 是 gone 用 it.
func TestSetCurrentSession_DetachClearsEntry(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/current-session-detach")

	// Anchor 客户端 所以 工作区 是 不 torn down 当 cid
	// detaches.
	anchor := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, anchor))

	cid := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, cid))
	require.NoError(t, b.SetCurrentSession(context.Background(), ws.ID, cid, "S2"))

	b.DetachClient(ws.ID, cid)

	ws.clientsMu.Lock()
	_, present := ws.clients[cid]
	ws.clientsMu.Unlock()
	require.False(t, present, "detach must remove the clientState entry along with its currentSessionID")

	// 一个 follow-up SetCurrentSession 在 gone 客户端 must 是
	// rejected 用 ErrClientNotAttached.
	require.ErrorIs(t, b.SetCurrentSession(context.Background(), ws.ID, cid, "S3"), ErrClientNotAttached)

	b.DetachClient(ws.ID, anchor)
}

// TestSetCurrentSession_RejectsHoldOnly verifies 那 registered
// 客户端 whose 只 claim 是 creation hold (streams == 0) cannot
// influence presence: SetCurrentSession returns ErrClientNotAttached
// 和 条目's currentSessionID stays empty.
func TestSetCurrentSession_RejectsHoldOnly(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	// Keep grace 窗口 large 所以 hold survives 测试.
	b.createGrace = time.Hour
	ws, _ := insertTestWorkspace(t, b, "/tmp/current-session-hold")

	cid := newClientID(t)
	b.registerClient(ws, cid)

	require.ErrorIs(t, b.SetCurrentSession(context.Background(), ws.ID, cid, "S1"), ErrClientNotAttached)

	ws.clientsMu.Lock()
	require.Empty(t, ws.clients[cid].currentSessionID, "hold-only client must not write a session id")
	ws.clientsMu.Unlock()

	// Drain.
	require.NoError(t, b.releaseHold(ws.ID, cid))
}

// TestSetCurrentSession_UnknownClient verifies 那 客户端 用 no
// 条目 在 all 是 rejected 用 ErrClientNotAttached.
func TestSetCurrentSession_UnknownClient(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/current-session-unknown")

	require.ErrorIs(t, b.SetCurrentSession(context.Background(), ws.ID, newClientID(t), "S1"), ErrClientNotAttached)
}

// TestSetCurrentSession_RejectsBadInputs covers the validation
// branches: empty/malformed client_id 和 unknown 工作区.
func TestSetCurrentSession_RejectsBadInputs(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/current-session-bad")

	require.ErrorIs(t, b.SetCurrentSession(context.Background(), ws.ID, "", "S1"), ErrInvalidClientID)
	require.ErrorIs(t, b.SetCurrentSession(context.Background(), ws.ID, "not-a-uuid", "S1"), ErrInvalidClientID)

	require.ErrorIs(
		t,
		b.SetCurrentSession(context.Background(), "00000000-0000-0000-0000-000000000000", newClientID(t), "S1"),
		ErrWorkspaceNotFound,
	)
}

// TestSetCurrentSession_RaceWithDetach exercises concurrent
// SetCurrentSession updates 从 one 客户端 racing against detach
// 在 second 客户端. final state must 是 self-consistent: any
// remaining clientState entries reflect a coherent
// (streams, currentSessionID) 配对.
func TestSetCurrentSession_RaceWithDetach(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/current-session-race")

	cidA := newClientID(t)
	cidB := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, cidA))
	require.NoError(t, b.AttachClient(ws.ID, cidB))

	var wg sync.WaitGroup
	const updates = 200
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := range updates {
			// Errors 是 tolerated: once cidA detaches,
			// further updates against cidA 必须 return
			// ErrClientNotAttached 但 never panic.
			_ = b.SetCurrentSession(context.Background(), ws.ID, cidA, "SA")
			_ = i
		}
	}()
	go func() {
		defer wg.Done()
		for i := range updates {
			_ = b.SetCurrentSession(context.Background(), ws.ID, cidB, "SB")
			_ = i
		}
	}()
	go func() {
		defer wg.Done()
		// Single concurrent detach 的 cidA partway through.
		b.DetachClient(ws.ID, cidA)
	}()
	wg.Wait()

	ws.clientsMu.Lock()
	defer ws.clientsMu.Unlock()
	require.NotContains(t, ws.clients, cidA, "detached client must be gone")
	require.Contains(t, ws.clients, cidB, "remaining client must still be present")
	require.Equal(t, "SB", ws.clients[cidB].currentSessionID, "remaining client must keep its last set session")
}

// TestAttachedClients_BasicLifecycle walks one 会话's count through
// attach -> set -> second 客户端 joins -> switch -> detach. 它 also
// confirms hold-只 和 unselected clients do 不 contribute.
func TestAttachedClients_BasicLifecycle(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	// Keep grace 窗口 long 所以 hold-只 客户端 survives.
	b.createGrace = time.Hour
	ws, _ := insertTestWorkspace(t, b, "/tmp/attached-clients-basic")

	// No clients yet.
	n, err := b.AttachedClients(ws.ID, "S1")
	require.NoError(t, err)
	require.Zero(t, n)

	// Attach 一个, set 到 S1. Count 为 S1 是 1; count 为 S2 是 0.
	cidA := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, cidA))
	require.NoError(t, b.SetCurrentSession(context.Background(), ws.ID, cidA, "S1"))

	n, err = b.AttachedClients(ws.ID, "S1")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = b.AttachedClients(ws.ID, "S2")
	require.NoError(t, err)
	require.Zero(t, n)

	// Attach B, set 到 S1. Count 为 S1 是 2.
	cidB := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, cidB))
	require.NoError(t, b.SetCurrentSession(context.Background(), ws.ID, cidB, "S1"))

	n, _ = b.AttachedClients(ws.ID, "S1")
	require.Equal(t, 2, n)

	// B switches 到 S2; counts redistribute.
	require.NoError(t, b.SetCurrentSession(context.Background(), ws.ID, cidB, "S2"))
	n, _ = b.AttachedClients(ws.ID, "S1")
	require.Equal(t, 1, n)
	n, _ = b.AttachedClients(ws.ID, "S2")
	require.Equal(t, 1, n)

	// 一个 hold-只 客户端 must NOT 是 counted, even 如果 we were able 到
	// imagine currentSessionID 在 it. registerClient leaves
	// currentSessionID empty 通过 construction, 和 SetCurrentSession
	// rejects hold-只 writers — 所以 contract holds two ways.
	cidHold := newClientID(t)
	b.registerClient(ws, cidHold)
	t.Cleanup(func() { _ = b.releaseHold(ws.ID, cidHold) })
	n, _ = b.AttachedClients(ws.ID, "S1")
	require.Equal(t, 1, n, "hold-only client must not contribute")
	n, _ = b.AttachedClients(ws.ID, "")
	require.Equal(t, 0, n,
		"empty sessionID must not match the hold-only entry (streams==0)")

	// 一个 客户端 用 streams > 0 但 currentSessionID == "" 是 NOT
	// counted toward any non-empty 会话, 和 是 matched 只
	// against empty 会话 id (which represents landing
	// screen).
	cidC := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, cidC))
	n, _ = b.AttachedClients(ws.ID, "S1")
	require.Equal(t, 1, n, "stream-only client with empty currentSessionID must not be counted toward S1")
	n, _ = b.AttachedClients(ws.ID, "")
	require.Equal(t, 1, n, "stream-only client with empty currentSessionID matches the empty session id")

	// B detaches: count 为 S2 drops 到 0.
	b.DetachClient(ws.ID, cidB)
	n, _ = b.AttachedClients(ws.ID, "S2")
	require.Zero(t, n)
	n, _ = b.AttachedClients(ws.ID, "S1")
	require.Equal(t, 1, n, "A still on S1")

	// Final 清理.
	b.DetachClient(ws.ID, cidA)
	b.DetachClient(ws.ID, cidC)
}

// TestAttachedClients_UnknownWorkspace verifies 错误 surface.
func TestAttachedClients_UnknownWorkspace(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	_, err := b.AttachedClients("00000000-0000-0000-0000-000000000000", "S1")
	require.ErrorIs(t, err, ErrWorkspaceNotFound)
}

// TestTeardown_DefersShutdownWhileCreatePending verifies core 的
// "coder agent offline after more than one 会话" fix: tearing down
// last live 工作区 must NOT shut 服务端 down while another
// CreateWorkspace 是 mid-flight (it has committed 到 slow init 路径
// 但 不 yet registered 它的 工作区). Without pending guard,
// teardown observed empty 工作区 map 和 killed whole 服务端
// out 从 under 工作区 being born.
func TestTeardown_DefersShutdownWhileCreatePending(t *testing.T) {
	t.Parallel()

	b, serverShutdowns := newTestBackend(t)
	ws, wsShutdowns := insertTestWorkspace(t, b, "/tmp/pending-guard")

	// Simulate concurrent 创建 那 has passed pending++ point
	// 但 has 不 yet registered 它的 工作区.
	b.mu.Lock()
	b.pending = 1
	b.mu.Unlock()

	b.teardown(ws)

	require.Equal(t, int32(1), wsShutdowns.Load(),
		"the torn-down workspace's own resources must still be released")
	require.Equal(t, int32(0), serverShutdowns.Load(),
		"server must not shut down while a create is in flight")
}

// TestTeardown_ShutsDownWhenIdleAndNoPending 是 control 为 guard
// above: 用 no 创建 在 flight, tearing down last 工作区 still
// triggers 服务端 shutdown, preserving "shut down 当 empty"
// behavior.
func TestTeardown_ShutsDownWhenIdleAndNoPending(t *testing.T) {
	t.Parallel()

	b, serverShutdowns := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/idle-shutdown")

	b.teardown(ws)

	require.Equal(t, int32(1), serverShutdowns.Load(),
		"server must shut down once the last workspace is gone and nothing is pending")
}

// TestCreateWorkspace_PendingBalancedOnSuccess drives the real
// CreateWorkspace 路径 和 asserts 在-flight `pending` 计数器 it
// uses 到 hold off 服务端 shutdown 是 balanced back 到 zero once
// 工作区 是 registered. 这 guards bookkeeping unit-level
// teardown tests stub out: refactor 那 drops increment 或
// misplaces the deferred decrement would reintroduce the shutdown race.
func TestCreateWorkspace_PendingBalancedOnSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	b, shutdownCount := newTestBackend(t)
	// Keep 创建 hold alive 所以 它的 grace timer can't fire 和 tear
	// 工作区 down mid-assertion.
	b.SetCreateGrace(time.Hour)
	t.Cleanup(func() { drainBackend(t, b) })

	_, _, err := b.CreateWorkspace(protoWS(t.TempDir(), t.TempDir(), uuid.New().String()))
	require.NoError(t, err)

	b.mu.Lock()
	pending := b.pending
	b.mu.Unlock()
	require.Equal(t, 0, pending, "pending must return to zero after a successful create")
	require.Equal(t, int32(0), shutdownCount.Load(), "a live workspace must not trigger shutdown")
}

// TestCreateWorkspace_PendingBalancedAndReapsOnFailure asserts 那
// 创建 which fails during 它的 slow init still decrements `pending`, 和
// 那 deferred reap shuts otherwise-空闲 服务端 down ( safety
// net 那 keeps failed 创建 racing last teardown 从 leaking
// empty 服务端 plain teardown declined 到 close).
func TestCreateWorkspace_PendingBalancedAndReapsOnFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	b, shutdownCount := newTestBackend(t)

	// 一个 data dir whose parent 是 regular 文件 makes 工作区's
	// data-目录 creation fail deterministically, after pending
	// 计数器 has been incremented.
	tmp := t.TempDir()
	fileAsParent := filepath.Join(tmp, "notadir")
	require.NoError(t, os.WriteFile(fileAsParent, []byte("x"), 0o600))
	badDataDir := filepath.Join(fileAsParent, "data")

	_, _, err := b.CreateWorkspace(protoWS(tmp, badDataDir, uuid.New().String()))
	require.Error(t, err, "create must fail when the data dir cannot be created")

	b.mu.Lock()
	pending := b.pending
	remaining := b.workspaces.Len()
	b.mu.Unlock()
	require.Equal(t, 0, pending, "pending must return to zero after a failed create")
	require.Zero(t, remaining, "no workspace should be registered after a failed create")
	require.Equal(t, int32(1), shutdownCount.Load(),
		"a failed create that leaves the server idle must reap it")
}

// TestServer_CreateCancelsPendingIdleShutdown 是 regression 测试 为
// coder-agent-offline handoff: 当 last 客户端 的 会话
// leaves, 服务端 must linger (不 shut down immediately), 和
// 客户端 returning within linger 窗口 — same 目录 或
// different one — must 取消 那 pending shutdown 所以 it 是 never handed
// 工作区 在 服务端 那 是 about 到 die.
func TestServer_CreateCancelsPendingIdleShutdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CRUSH_DISABLE_PROVIDER_AUTO_UPDATE", "1")

	b, shutdownCount := newTestBackend(t)
	b.SetIdleShutdownDelay(10 * time.Second) // long enough not to fire mid-test
	b.SetCreateGrace(time.Hour)
	t.Cleanup(func() { drainBackend(t, b) })

	// 会话 1: 工作区 那 是 attached then leaves, arming
	// 空闲-shutdown timer.
	wsA, _ := insertTestWorkspace(t, b, "/tmp/linger-A")
	cidA := newClientID(t)
	require.NoError(t, b.AttachClient(wsA.ID, cidA))
	b.DetachClient(wsA.ID, cidA)

	b.mu.Lock()
	armed := b.shutdownTimer != nil
	b.mu.Unlock()
	require.True(t, armed, "idle-shutdown timer must be armed after the last client leaves")
	require.Equal(t, int32(0), shutdownCount.Load(), "server must linger, not shut down immediately")

	// 会话 2 arrives within linger 窗口: creating 工作区
	// must 取消 pending shutdown.
	_, _, err := b.CreateWorkspace(protoWS(t.TempDir(), t.TempDir(), uuid.New().String()))
	require.NoError(t, err)

	b.mu.Lock()
	stillArmed := b.shutdownTimer != nil
	b.mu.Unlock()
	require.False(t, stillArmed, "a create within the linger window must cancel the pending shutdown")
	require.Equal(t, int32(0), shutdownCount.Load(), "server must not shut down after a client returns")
}

// TestServer_ShutsDownAfterLingerWhenIdle confirms the linger still ends
// 在 shutdown 当 no 客户端 returns: 服务端 是 不 leaked.
func TestServer_ShutsDownAfterLingerWhenIdle(t *testing.T) {
	t.Parallel()

	b, shutdownCount := newTestBackend(t)
	b.SetIdleShutdownDelay(100 * time.Millisecond)

	wsA, _ := insertTestWorkspace(t, b, "/tmp/linger-idle")
	cidA := newClientID(t)
	require.NoError(t, b.AttachClient(wsA.ID, cidA))
	b.DetachClient(wsA.ID, cidA)

	// Nobody returns: after linger elapses 服务端 shuts down.
	require.Eventually(t, func() bool { return shutdownCount.Load() == 1 },
		2*time.Second, 10*time.Millisecond,
		"idle server must shut down after the linger window")
}

// TestServer_DisabledIdleShutdownKeepsServerUp covers 服务端 用户
// started explicitly 用 `crush 服务端`: neither 空闲 timer armed 在
// startup nor last 客户端 leaving may shut it down. 它 runs until it
// 是 stopped.
func TestServer_DisabledIdleShutdownKeepsServerUp(t *testing.T) {
	t.Parallel()

	b, shutdownCount := newTestBackend(t)
	b.SetIdleShutdownDelay(100 * time.Millisecond)
	b.DisableIdleShutdown()

	b.ArmIdleShutdown()
	require.Zero(t, shutdownCount.Load(),
		"the startup idle timer must not shut down a server with idle shutdown disabled")

	wsA, _ := insertTestWorkspace(t, b, "/tmp/no-linger")
	cidA := newClientID(t)
	require.NoError(t, b.AttachClient(wsA.ID, cidA))
	b.DetachClient(wsA.ID, cidA)

	require.Never(t, func() bool { return shutdownCount.Load() > 0 },
		500*time.Millisecond, 10*time.Millisecond,
		"a server with idle shutdown disabled must stay up when idle")
}

// TestDisableWorkspaceAutoClose_KeepsHeadlessWork covers 服务端 started
// explicitly 用 `crush 服务端`: neither expired creation hold,
// detached 流, nor retired 客户端 may close 工作区. Departed
// clients must still 是 forgotten, 所以 they do 不 linger 作为 viewers 或 keep
// another 客户端's explicit 删除 从 closing 工作区.
func TestDisableWorkspaceAutoClose_KeepsHeadlessWork(t *testing.T) {
	t.Parallel()

	b, serverShutdowns := newTestBackend(t)
	b.DisableIdleShutdown()
	b.DisableWorkspaceAutoClose()
	b.createGrace = time.Millisecond
	b.SetDetachGrace(time.Millisecond)

	ws, wsShutdowns := insertTestWorkspace(t, b, "/tmp/headless-work")

	neverAttached := newClientID(t)
	b.registerClient(ws, neverAttached)

	disconnected := newClientID(t)
	b.registerClient(ws, disconnected)
	require.NoError(t, b.AttachClient(ws.ID, disconnected))
	b.DetachClient(ws.ID, disconnected)

	retired := newClientID(t)
	b.registerClient(ws, retired)
	require.NoError(t, b.AttachClient(ws.ID, retired))
	require.NoError(t, b.RetireClient(retired))

	require.Eventually(t, func() bool {
		ws.clientsMu.Lock()
		defer ws.clientsMu.Unlock()
		return len(ws.clients) == 0
	}, time.Second, 10*time.Millisecond, "departed clients must be forgotten")
	_, err := b.GetWorkspace(ws.ID)
	require.NoError(t, err, "the workspace must remain open for headless work")
	require.Zero(t, wsShutdowns.Load(), "the workspace must not be torn down")
	require.Zero(t, serverShutdowns.Load(), "the server must remain open")

	require.NoError(t, b.DeleteWorkspace(ws.ID, newClientID(t)))
	require.Eventually(t, func() bool {
		_, err := b.GetWorkspace(ws.ID)
		return errors.Is(err, ErrWorkspaceNotFound)
	}, time.Second, 10*time.Millisecond,
		"an explicit delete must still close a workspace no client holds")
	require.Equal(t, int32(1), wsShutdowns.Load())
	require.Zero(t, serverShutdowns.Load(),
		"deleting the last workspace must not shut down an explicitly started server")
}

// -- Detach grace --
//
// SSE 流 是 客户端's refcount claim, 所以 流 那 drops
// without explicit release must 不 destroy 工作区 before
// 客户端's reconnect loop can come back. These cover both outcomes 的
// 那 窗口 plus fast 路径 clean exit still gets.

// TestDetachStream_GraceSurvivesReattach 是 core regression:
// momentary 流 drop followed 通过 reconnect must leave 工作区
// exactly where it was. Before grace, reconnect came back 到
// 工作区 ID 服务端 no longer knew, 和 every later 请求 404'd
// forever because sibling 会话 kept 服务端 itself alive.
func TestDetachStream_GraceSurvivesReattach(t *testing.T) {
	t.Parallel()

	b, srvShutdowns := newTestBackend(t)
	b.SetDetachGrace(time.Hour)
	ws, wsShutdowns := insertTestWorkspace(t, b, "/tmp/blip")

	cid := newClientID(t)
	b.registerClient(ws, cid)
	require.NoError(t, b.AttachClient(ws.ID, cid))

	b.DetachClient(ws.ID, cid)
	require.Equal(t, int32(0), wsShutdowns.Load(),
		"a dropped stream must not tear the workspace down within the grace")
	live, err := b.GetWorkspace(ws.ID)
	require.NoError(t, err, "the workspace must still be addressable by its original ID")
	require.Same(t, ws, live)

	// reconnect lands 和 converts grace back into 流 claim.
	require.NoError(t, b.AttachClient(ws.ID, cid))
	ws.clientsMu.Lock()
	cs := ws.clients[cid]
	require.Equal(t, 1, cs.streams)
	require.Nil(t, cs.holdTimer, "re-attaching must cancel the grace timer")
	ws.clientsMu.Unlock()
	require.Equal(t, int32(0), srvShutdowns.Load())
}

// TestDetachStream_GraceExpiryTearsDown checks grace 是 bounded:
// 客户端 那 never comes back must 不 pin 工作区 (和 用 it
// 服务端) indefinitely.
func TestDetachStream_GraceExpiryTearsDown(t *testing.T) {
	t.Parallel()

	b, srvShutdowns := newTestBackend(t)
	b.SetDetachGrace(50 * time.Millisecond)
	ws, wsShutdowns := insertTestWorkspace(t, b, "/tmp/gone-for-good")

	cid := newClientID(t)
	b.registerClient(ws, cid)
	require.NoError(t, b.AttachClient(ws.ID, cid))
	b.DetachClient(ws.ID, cid)

	require.Eventually(t, func() bool { return srvShutdowns.Load() == 1 },
		2*time.Second, 10*time.Millisecond,
		"an unclaimed grace must expire and tear the workspace down")
	require.Equal(t, int32(1), wsShutdowns.Load())
	_, err := b.GetWorkspace(ws.ID)
	require.ErrorIs(t, err, ErrWorkspaceNotFound)
}

// TestDetachStream_ExplicitReleaseSkipsGrace 保持 clean exits fast:
// 客户端 那 says goodbye before 它的 流 closes 是 不 coming back,
// 所以 grace must 不 delay teardown.
func TestDetachStream_ExplicitReleaseSkipsGrace(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	b.SetDetachGrace(time.Hour)
	ws, wsShutdowns := insertTestWorkspace(t, b, "/tmp/clean-exit")

	cid := newClientID(t)
	b.registerClient(ws, cid)
	require.NoError(t, b.AttachClient(ws.ID, cid))

	// order quitting 客户端 produces: release, then 流 ends.
	require.NoError(t, b.releaseHold(ws.ID, cid))
	require.Equal(t, int32(0), wsShutdowns.Load(), "the live stream still holds it")
	b.DetachClient(ws.ID, cid)

	require.Equal(t, int32(1), wsShutdowns.Load(),
		"an explicitly released client must tear down without waiting out the grace")
}

// TestRegisterClient_RearmsGraceAndCancelsRelease covers 客户端 那
// recovers 通过 re-creating 工作区 rather than re-attaching:
// duplicate 创建 must buy it fresh attach 窗口 和 undo earlier
// release, 和 superseded timer must 不 是 able 到 kill new claim.
func TestRegisterClient_RearmsGraceAndCancelsRelease(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	b.SetDetachGrace(50 * time.Millisecond)
	b.createGrace = time.Hour
	ws, wsShutdowns := insertTestWorkspace(t, b, "/tmp/rearm")

	cid := newClientID(t)
	b.registerClient(ws, cid)
	require.NoError(t, b.AttachClient(ws.ID, cid))
	require.NoError(t, b.releaseHold(ws.ID, cid))
	b.DetachClient(ws.ID, cid)
	require.Equal(t, int32(1), wsShutdowns.Load())

	// 一个 second 工作区, 这 time reclaimed 通过 duplicate 创建 while
	// 它的 short detach grace 是 running.
	ws2, ws2Shutdowns := insertTestWorkspace(t, b, "/tmp/rearm-2")
	b.registerClient(ws2, cid)
	require.NoError(t, b.AttachClient(ws2.ID, cid))
	b.DetachClient(ws2.ID, cid)
	b.registerClient(ws2, cid)

	ws2.clientsMu.Lock()
	require.False(t, ws2.clients[cid].released)
	ws2.clientsMu.Unlock()

	//nolint:forbidigo // superseded 50ms timer has 到 是 given 它的 chance 到 fire.
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, int32(0), ws2Shutdowns.Load(),
		"the re-armed claim must outlive the timer it replaced")
}

// -- 客户端 retirement --

// TestRetireClient_ReleasesEveryClaim checks the primitive a quitting
// 客户端 relies 在: one 调用 drops 它的 claims everywhere, whether they
// 是 held 通过 流, 创建 hold, 或 detach grace.
func TestRetireClient_ReleasesEveryClaim(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	b.SetDetachGrace(time.Hour)
	b.createGrace = time.Hour

	streamed, streamedShutdowns := insertTestWorkspace(t, b, "/tmp/retire-stream")
	held, heldShutdowns := insertTestWorkspace(t, b, "/tmp/retire-hold")
	shared, sharedShutdowns := insertTestWorkspace(t, b, "/tmp/retire-shared")

	cid, other := newClientID(t), newClientID(t)
	b.registerClient(streamed, cid)
	require.NoError(t, b.AttachClient(streamed.ID, cid))
	b.registerClient(held, cid)
	b.registerClient(shared, cid)
	b.registerClient(shared, other)

	require.NoError(t, b.RetireClient(cid))

	require.Equal(t, int32(1), streamedShutdowns.Load())
	require.Equal(t, int32(1), heldShutdowns.Load())
	require.Equal(t, int32(0), sharedShutdowns.Load(),
		"a workspace another client is still using must be left alone")
	shared.clientsMu.Lock()
	require.NotContains(t, shared.clients, cid)
	require.Contains(t, shared.clients, other)
	shared.clientsMu.Unlock()

	// Idempotent: retiring twice 是 不 错误 和 changes nothing.
	require.NoError(t, b.RetireClient(cid))
	require.Equal(t, int32(1), streamedShutdowns.Load())
}

// TestRetireClient_RefusesLaterCreates 是 what makes teardown exact
// without timing guesses. 一个 recovery 创建 whose 响应 客户端
// never saw can still land 在 服务端 afterwards; refusing it 是
// 只 way 到 guarantee it cannot leave 工作区 nobody can name.
func TestRetireClient_RefusesLaterCreates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	b := New(context.Background(), nil, func() {})
	t.Cleanup(func() { drainBackend(t, b) })

	cid := newClientID(t)
	require.NoError(t, b.RetireClient(cid))

	_, _, err := b.CreateWorkspace(protoWS(t.TempDir(), t.TempDir(), cid))
	require.ErrorIs(t, err, ErrClientRetired)
	require.Zero(t, b.workspaces.Len(), "a refused create must register nothing")
}

// TestRetireClient_DuringPendingCreate drives the actual ordering the
// design has 到 survive: retirement lands while 创建 是 already
// inside 它的 slow initialization, holding b.mu released. 创建 must
// notice 在 它的 commit point 和 discard 工作区 it built rather
// than registering claim 为 客户端 那 has already gone.
func TestRetireClient_DuringPendingCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	b := New(context.Background(), nil, func() {})
	t.Cleanup(func() { drainBackend(t, b) })

	cid := newClientID(t)
	cwd, dataDir := t.TempDir(), t.TempDir()

	createErr := make(chan error, 1)
	go func() {
		_, _, err := b.CreateWorkspace(protoWS(cwd, dataDir, cid))
		createErr <- err
	}()

	// pending 是 bumped under b.mu before 创建 releases 锁 为
	// 它的 slow init (配置, db, app), 和 dropped 只 after 工作区
	// 是 registered. Observing pending == 1 therefore means 创建 是
	// genuinely mid-flight 用 b.mu free — exact 窗口 retirement has
	// 到 cover. Nothing here fakes race; 创建 really 是 running.
	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.pending == 1
	}, 30*time.Second, time.Millisecond, "create must reach its slow path")

	require.NoError(t, b.RetireClient(cid))

	require.ErrorIs(t, <-createErr, ErrClientRetired,
		"a create that commits after retirement must be refused")
	require.Zero(t, b.workspaces.Len(), "no workspace may be left behind")
}

// -- Shutdown 作为 atomic decision --

// TestShutdownIfIdle_RefusesWhileWorkspaceLive 是 guard 那 keeps
// second 会话 alive: upgrading 客户端 asks mismatched 服务端 到
// stand down, 和 只 服务端 can answer without racing 会话
// 那 arrives between question 和 answer.
func TestShutdownIfIdle_RefusesWhileWorkspaceLive(t *testing.T) {
	t.Parallel()

	b, shutdowns := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/in-use")
	cid := newClientID(t)
	b.registerClient(ws, cid)

	require.False(t, b.ShutdownIfIdle(), "a server hosting a workspace must refuse")
	require.Equal(t, int32(0), shutdowns.Load())

	// Refusing must 不 latch anything: 服务端 keeps serving.
	b.mu.Lock()
	require.False(t, b.closing)
	b.mu.Unlock()
}

// TestShutdownIfIdle_RefusesWhileCreatePending closes subtler half 的
// same 窗口: 工作区 still working through 它的 slow startup 是
// 不 yet 在 工作区 map, 所以 count-based check would 调用
// 服务端 空闲 和 kill 会话 那 是 being born.
func TestShutdownIfIdle_RefusesWhileCreatePending(t *testing.T) {
	t.Parallel()

	b, shutdowns := newTestBackend(t)
	b.mu.Lock()
	b.pending = 1
	b.mu.Unlock()

	require.False(t, b.ShutdownIfIdle())
	require.Equal(t, int32(0), shutdowns.Load())
}

// TestShutdownIfIdle_GrantedAndFinal preserves upgrades: 空闲 服务端
// does stand down, 和 decision 是 final 所以 创建 arriving
// afterwards 是 refused instead 的 being initialized 在 dying process.
func TestShutdownIfIdle_GrantedAndFinal(t *testing.T) {
	t.Parallel()

	b, shutdowns := newTestBackend(t)
	require.True(t, b.ShutdownIfIdle())
	require.Equal(t, int32(1), shutdowns.Load())

	_, _, err := b.CreateWorkspace(protoWS(t.TempDir(), t.TempDir(), newClientID(t)))
	require.ErrorIs(t, err, ErrServerShuttingDown)
}

// TestShutdownIfIdle_RefusedByExplicitServer keeps 客户端 的 another
// build 从 replacing 服务端 用户 started 用 `crush 服务端`:
// replacement it spawns would shut itself down once 空闲.
func TestShutdownIfIdle_RefusedByExplicitServer(t *testing.T) {
	t.Parallel()

	b, shutdowns := newTestBackend(t)
	b.DisableIdleShutdown()

	require.False(t, b.ShutdownIfIdle(), "an explicitly started server must refuse")
	require.Equal(t, int32(0), shutdowns.Load())
	require.False(t, b.Closing())
}

// TestIdleShutdown_RefusesLaterCreates covers same finality 为
// unprompted 路径: 服务端 那 decided 到 exit because it went 空闲
// must 不 受理 创建 的 客户端 那 arrives moment later.
func TestIdleShutdown_RefusesLaterCreates(t *testing.T) {
	t.Parallel()

	b, shutdowns := newTestBackend(t)
	b.SetIdleShutdownDelay(10 * time.Millisecond)
	ws, _ := insertTestWorkspace(t, b, "/tmp/going-idle")
	cid := newClientID(t)
	require.NoError(t, b.AttachClient(ws.ID, cid))
	b.DetachClient(ws.ID, cid)

	require.Eventually(t, func() bool { return shutdowns.Load() == 1 },
		2*time.Second, 10*time.Millisecond)

	_, _, err := b.CreateWorkspace(protoWS(t.TempDir(), t.TempDir(), cid))
	require.ErrorIs(t, err, ErrServerShuttingDown,
		"the client must be told to retry against a replacement server")
}

// TestShutdownWorkspaces_TearsDownEachOnceInParallel covers 服务端 told
// 到 stop while hosting live workspaces: each 是 torn down exactly once,
// concurrently, 和 backend refuses creates 和 服务端 re-shutdowns.
func TestShutdownWorkspaces_TearsDownEachOnceInParallel(t *testing.T) {
	t.Parallel()

	b, serverShutdowns := newTestBackend(t)
	const n = 3
	var started sync.WaitGroup
	started.Add(n)
	release := make(chan struct{})
	counts := make([]*atomic.Int32, n)
	workspaces := make([]*Workspace, n)
	for i := range n {
		ws, count := insertTestWorkspace(t, b, fmt.Sprintf("/tmp/live-%d", i))
		ws.shutdownFn = func() {
			count.Add(1)
			started.Done()
			<-release
		}
		counts[i], workspaces[i] = count, ws
	}

	done := make(chan error, 1)
	go func() { done <- b.ShutdownWorkspaces(t.Context()) }()
	// Every teardown must 是 在 flight 在 once; serial teardown would
	// block here 在 first 工作区 forever.
	started.Wait()
	close(release)
	require.NoError(t, <-done)

	for _, count := range counts {
		require.Equal(t, int32(1), count.Load())
	}
	require.Zero(t, b.workspaces.Len())
	require.Empty(t, b.pathIndex)

	// 一个 detach racing shutdown must 不 tear 工作区 down again,
	// nor trigger 空闲 服务端 shutdown.
	b.teardown(workspaces[0])
	require.Equal(t, int32(1), counts[0].Load())
	require.Zero(t, serverShutdowns.Load())

	_, _, err := b.CreateWorkspace(protoWS(t.TempDir(), t.TempDir(), newClientID(t)))
	require.ErrorIs(t, err, ErrServerShuttingDown)
}

// TestShutdownWorkspaces_GivesUpAtDeadline covers 工作区 whose
// teardown hangs: caller's 期限 wins, 不 slowest 工作区.
func TestShutdownWorkspaces_GivesUpAtDeadline(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/stuck")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ws.shutdownFn = func() { <-release }

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, b.ShutdownWorkspaces(ctx), context.DeadlineExceeded)
	require.Zero(t, b.workspaces.Len())
}

// TestShutdownWorkspaces_WaitsForTeardownInFlight guards against a stop
// returning while 工作区 那 already left index 是 still being
// torn down 通过 它的 expired detach grace.
func TestShutdownWorkspaces_WaitsForTeardownInFlight(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend(t)
	b.SetDetachGrace(10 * time.Millisecond)
	b.SetIdleShutdownDelay(time.Hour)
	ws, _ := insertTestWorkspace(t, b, "/tmp/tearing-down")
	started, release := make(chan struct{}), make(chan struct{})
	open := sync.OnceFunc(func() { close(release) })
	t.Cleanup(open)
	var finished atomic.Bool
	ws.shutdownFn = func() {
		close(started)
		<-release
		finished.Store(true)
	}

	cid := newClientID(t)
	b.registerClient(ws, cid)
	require.NoError(t, b.AttachClient(ws.ID, cid))
	b.DetachClient(ws.ID, cid)
	<-started

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, b.ShutdownWorkspaces(ctx), context.DeadlineExceeded)
	open()
	require.NoError(t, b.ShutdownWorkspaces(t.Context()))
	require.True(t, finished.Load(), "the stop returned before the teardown finished")
}
