package backend

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// TestTeardown_KeepsOtherWorkspacesMCP guards against 工作区 teardown
// closing every MCP 会话 在 process 和 shutting shared MCP
// 事件 总线 down, 和 against reopened 工作区 leaking 会话
// it replaces while another 工作区 keeps MCP clients referenced.
func TestTeardown_KeepsOtherWorkspacesMCP(t *testing.T) {
	xdgIsolated(t)
	t.Setenv("CRUSH_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	const name = "teardown-probe"
	httpSrv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return mcp.NewServer(&mcp.Implementation{Name: name}, nil)
	}, nil))
	t.Cleanup(func() { httpSrv.CloseClientConnections(); httpSrv.Close() })
	b := New(t.Context(), nil, func() {})
	b.SetCreateGrace(time.Minute)
	t.Cleanup(func() { drainBackend(t, b) })

	// Only 工作区 一个 uses MCP 服务端.
	dirA, dataA := t.TempDir(), t.TempDir()
	cfg := fmt.Sprintf(`{"mcp": {%q: {"type": "http", "url": %q}}}`, name, httpSrv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(dirA, "crush.json"), []byte(cfg), 0o644))
	connected := func(prev *mcptools.ClientSession) (got *mcptools.ClientSession) {
		t.Helper()
		require.Eventually(t, func() bool {
			info, ok := mcptools.GetState(name)
			got = info.Client
			return ok && info.State == mcptools.StateConnected && got != nil && got != prev
		}, 10*time.Second, 20*time.Millisecond, "MCP server never connected")
		return got
	}

	cidA, cidB := newClientID(t), newClientID(t)
	_, _, err := b.CreateWorkspace(protoWS(dirA, dataA, cidA))
	require.NoError(t, err)
	_, _, err = b.CreateWorkspace(protoWS(t.TempDir(), t.TempDir(), cidB))
	require.NoError(t, err)
	first := connected(nil)

	require.NoError(t, b.RetireClient(cidB))
	require.NoError(t, first.Ping(t.Context(), nil), "another workspace's teardown closed the MCP session")

	// While B holds clients, 一个 是 reopened 和 must close 会话
	// it replaces.
	cidB = newClientID(t)
	_, _, err = b.CreateWorkspace(protoWS(t.TempDir(), t.TempDir(), cidB))
	require.NoError(t, err)
	require.NoError(t, b.RetireClient(cidA))
	cidA = newClientID(t)
	_, _, err = b.CreateWorkspace(protoWS(dirA, dataA, cidA))
	require.NoError(t, err)
	second := connected(first)
	require.Eventually(t, func() bool { return first.Ping(t.Context(), nil) != nil },
		5*time.Second, 20*time.Millisecond, "the reopened workspace leaked its replaced MCP session")
	require.NoError(t, second.Ping(t.Context(), nil))

	// last teardown must 不 shut shared 事件 总线 down 为
	// 工作区 opened later.
	require.NoError(t, b.RetireClient(cidA))
	require.NoError(t, b.RetireClient(cidB))
	ws, _, err := b.CreateWorkspace(protoWS(t.TempDir(), t.TempDir(), newClientID(t)))
	require.NoError(t, err)
	events, err := b.SubscribeEvents(t.Context(), ws.ID)
	require.NoError(t, err)
	requireMCPDisabledEvent(t, ws, events, name)
}

// requireMCPDisabledEvent disables named MCP 服务端 until 它的 state
// change reaches ch; app subscribes 它的 事件 fan-在 asynchronously.
func requireMCPDisabledEvent(t *testing.T, ws *Workspace, ch <-chan pubsub.Event[tea.Msg], name string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		require.NoError(t, mcptools.DisableSingle(ws.Cfg, name))
		select {
		case ev, ok := <-ch:
			require.True(t, ok, "event channel was closed")
			if e, ok := ev.Payload.(pubsub.Event[mcptools.Event]); ok &&
				e.Payload.Name == name && e.Payload.State == mcptools.StateDisabled {
				return
			}
		case <-time.After(50 * time.Millisecond):
		case <-timeout:
			t.Fatal("timed out waiting for the MCP state change")
		}
	}
}
