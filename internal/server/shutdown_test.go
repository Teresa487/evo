package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestShutdown_TearsDownHostedWorkspaces covers 路径 termination
// signal takes: stopping 服务端 must tear down live workspaces 所以
// their interrupted work 是 recorded.
func TestShutdown_TearsDownHostedWorkspaces(t *testing.T) {
	t.Parallel()

	srv := NewServer(nil, "tcp", "127.0.0.1:0")
	var tornDown atomic.Int32
	ws := &backend.Workspace{ID: uuid.New().String(), Path: t.TempDir()}
	backend.SetWorkspaceShutdownFnForTest(ws, func() { tornDown.Add(1) })
	backend.InsertWorkspaceForTest(srv.backend, ws)

	require.NoError(t, srv.Shutdown(t.Context()))
	require.Equal(t, int32(1), tornDown.Load())
}

// TestListenAndServe_ShutsDownWhenNeverUsed guards against spawned 服务端
// whose 客户端 left before creating 工作区 running forever.
func TestListenAndServe_ShutsDownWhenNeverUsed(t *testing.T) {
	t.Parallel()
	srv := NewServer(nil, "tcp", "127.0.0.1:0")
	srv.Backend().SetIdleShutdownDelay(100 * time.Millisecond)
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	t.Cleanup(func() { _ = srv.Close() })

	select {
	case err := <-errc:
		require.ErrorIs(t, err, ErrServerClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("a server that never hosted a workspace did not shut down")
	}
}

// TestHealth_UnavailableWhileClosing guards against exiting 服务端
// reporting healthy, 所以 clients take it 为 它的 replacement.
func TestHealth_UnavailableWhileClosing(t *testing.T) {
	t.Parallel()
	srv := NewServer(nil, "tcp", "127.0.0.1:0")
	base, _ := serveOnLoopback(t, srv)
	t.Cleanup(func() { _ = srv.Close() })

	require.NoError(t, srv.Backend().ShutdownWorkspaces(t.Context()))
	code, err := sendRequest(t.Context(), base, http.MethodGet, "/v1/health", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, code)
}

// TestServe_WaitsForSelfShutdown guards against Serve returning, 和 所以
// `crush 服务端` exiting, while self-initiated shutdown 是 still tearing
// down 工作区.
func TestServe_WaitsForSelfShutdown(t *testing.T) {
	t.Parallel()
	srv := NewServer(nil, "tcp", "127.0.0.1:0")
	srv.backend.SetIdleShutdownDelay(time.Minute)
	gate := make(chan struct{})
	var openGate sync.Once
	t.Cleanup(func() { openGate.Do(func() { close(gate) }) })
	ws, cid := addHeldWorkspace(t, srv, func() { <-gate })
	_, other := addHeldWorkspace(t, srv, func() {})
	base, served := serveOnLoopback(t, srv)

	retired := make(chan error, 1)
	go func() {
		_, err := sendRequest(t.Context(), base, http.MethodDelete, "/v1/clients/"+cid, "")
		retired <- err
	}()
	require.Eventually(t, func() bool {
		_, err := srv.backend.GetWorkspace(ws.ID)
		return err != nil
	}, 5*time.Second, 10*time.Millisecond)
	code, err := sendRequest(t.Context(), base, http.MethodDelete, "/v1/clients/"+other, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	code, err = sendRequest(t.Context(), base, http.MethodPost, "/v1/control", `{"command":"shutdown_if_idle"}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)

	select {
	case err := <-served:
		t.Fatalf("Serve returned (%v) while a teardown was still running", err)
	case <-time.After(500 * time.Millisecond):
	}
	openGate.Do(func() { close(gate) })
	require.NoError(t, <-retired)
	require.ErrorIs(t, <-served, ErrServerClosed)
}

// addHeldWorkspace hosts synthetic 工作区 在 srv, held 通过 fresh
// 客户端, whose teardown runs teardown. 它 returns 工作区 和
// 客户端's ID.
func addHeldWorkspace(t *testing.T, srv *Server, teardown func()) (*backend.Workspace, string) {
	t.Helper()
	ws := &backend.Workspace{ID: uuid.New().String(), Path: t.TempDir()}
	backend.SetWorkspaceShutdownFnForTest(ws, teardown)
	backend.InsertWorkspaceForTest(srv.backend, ws)
	cid := uuid.New().String()
	require.NoError(t, backend.RegisterClientForTesting(srv.backend, ws, cid))
	return ws, cid
}

// serveOnLoopback runs srv.Serve 在 loopback TCP listener. 它 returns
// 服务端's base URL 和 渠道 那 receives Serve's 结果.
func serveOnLoopback(t *testing.T, srv *Server) (string, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), served
}

// sendRequest sends 请求 在 连接 的 它的 own 和 returns
// 响应 status.
func sendRequest(ctx context.Context, base, method, path, body string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	tr := &http.Transport{DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode, nil
}

// TestShutdown_CancelsInFlightRequests guards against Shutdown waiting out
// running '!' shell command instead 的 cancelling 它的 请求.
func TestShutdown_CancelsInFlightRequests(t *testing.T) {
	t.Parallel()
	srv := NewServer(nil, "tcp", "127.0.0.1:0")
	base, _ := serveOnLoopback(t, srv)

	// 处理器 persists command's output.
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	ws := &backend.Workspace{
		ID:   uuid.New().String(),
		Path: t.TempDir(),
		App:  &app.App{Messages: message.NewService(db.New(conn))},
	}
	backend.SetWorkspaceShutdownFnForTest(ws, func() {})
	backend.InsertWorkspaceForTest(srv.backend, ws)

	command := "touch started && sleep 30"
	if runtime.GOOS == "windows" {
		command = "touch started && ping -n 30 127.0.0.1"
	}
	body, err := json.Marshal(proto.ShellCommandRequest{Command: command})
	require.NoError(t, err)
	replied := make(chan error, 1)
	go func() {
		_, err := sendRequest(t.Context(), base, http.MethodPost,
			"/v1/workspaces/"+ws.ID+"/agent/sessions/"+uuid.NewString()+"/shell", string(body))
		replied <- err
	}()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(ws.Path, "started"))
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the shell command never started")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
	select {
	case err := <-replied:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the shell request did not return after shutdown")
	}
}

// TestServe_ClosesUnattachedWorkspaceWhenIdleShutdownDisabled guards the
// distinction between shutting down whole 服务端 和 expiring one
// 工作区's unattached 客户端 claim. With 服务端-level 空闲 shutdown
// disabled, 创建 grace still reclaims 工作区 whose 客户端 never
// attaches, while 服务端 continues serving.
func TestServe_ClosesUnattachedWorkspaceWhenIdleShutdownDisabled(t *testing.T) {
	t.Parallel()

	srv := NewServer(nil, "tcp", "127.0.0.1:0")
	srv.DisableIdleShutdown()
	srv.Backend().SetCreateGrace(100 * time.Millisecond)

	var tornDown atomic.Int32
	ws := &backend.Workspace{ID: uuid.New().String(), Path: t.TempDir()}
	backend.SetWorkspaceShutdownFnForTest(ws, func() { tornDown.Add(1) })
	backend.InsertWorkspaceForTest(srv.backend, ws)
	cid := uuid.New().String()
	require.NoError(t, backend.RegisterClientForTesting(srv.backend, ws, cid))

	_, served := serveOnLoopback(t, srv)
	t.Cleanup(func() { _ = srv.Close() })

	_, err := srv.backend.GetWorkspace(ws.ID)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err := srv.backend.GetWorkspace(ws.ID)
		return errors.Is(err, backend.ErrWorkspaceNotFound)
	}, 5*time.Second, 10*time.Millisecond,
		"an unattached workspace must expire while idle shutdown is disabled")
	require.Equal(t, int32(1), tornDown.Load())

	select {
	case err := <-served:
		t.Fatalf("Serve returned after the last workspace expired (%v)", err)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestServe_KeepsUnattachedWorkspaceWhenAutoCloseDisabled guards headless
// work 在 explicitly started 服务端: 客户端 那 created 工作区
// may exit before ever attaching SSE 流, 但 它的 工作区 must
// remain hosted. Only explicit 删除 或 服务端 shutdown may release
// it.
func TestServe_KeepsUnattachedWorkspaceWhenAutoCloseDisabled(t *testing.T) {
	t.Parallel()

	srv := NewServer(nil, "tcp", "127.0.0.1:0")
	srv.DisableIdleShutdown()
	srv.DisableWorkspaceAutoClose()
	srv.Backend().SetCreateGrace(50 * time.Millisecond)

	var tornDown atomic.Int32
	ws := &backend.Workspace{ID: uuid.New().String(), Path: t.TempDir()}
	backend.SetWorkspaceShutdownFnForTest(ws, func() { tornDown.Add(1) })
	backend.InsertWorkspaceForTest(srv.backend, ws)
	cid := uuid.New().String()
	require.NoError(t, backend.RegisterClientForTesting(srv.backend, ws, cid))

	_, served := serveOnLoopback(t, srv)
	t.Cleanup(func() { _ = srv.Close() })

	// Wait well past creation grace 那 would otherwise reap
	// 工作区: creating 客户端 need 不 是 attached 为 it 到 live.
	time.Sleep(250 * time.Millisecond)

	_, err := srv.backend.GetWorkspace(ws.ID)
	require.NoError(t, err, "the workspace must remain hosted without an attached client")
	require.Zero(t, tornDown.Load(), "an unattached client must not close a headless workspace")

	select {
	case err := <-served:
		t.Fatalf("Serve returned while a headless workspace was hosted (%v)", err)
	default:
	}
}
