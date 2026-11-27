package lsp

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"
)

func TestClient(t *testing.T) {
	// 创建 simple 配置 为 testing
	cfg := config.LSPConfig{
		Command:   "$THE_CMD", // Use echo as a dummy command that won't fail
		Args:      []string{"hello"},
		FileTypes: []string{"go"},
		Env:       map[string]string{},
	}

	// 测试 creating powernap 客户端 - 这 will likely fail 用 echo
	// 但 we can still 测试 basic structure
	client, err := New("test", cfg, config.NewShellVariableResolver(env.NewFromMap(map[string]string{
		"THE_CMD": "echo",
	})), ".", false)
	if err != nil {
		// Expected 到 fail 用 echo command, skip rest
		t.Skipf("Powernap client creation failed as expected with dummy command: %v", err)
		return
	}

	// If we get here, 测试 basic interface methods
	if client.GetName() != "test" {
		t.Errorf("Expected name 'test', got '%s'", client.GetName())
	}

	if !client.HandlesFile("test.go") {
		t.Error("Expected client to handle .go files")
	}

	if client.HandlesFile("test.py") {
		t.Error("Expected client to not handle .py files")
	}

	// 测试 服务端 state
	client.SetServerState(StateReady)
	if client.GetServerState() != StateReady {
		t.Error("Expected server state to be StateReady")
	}

	// Clean up - expect 这 到 fail 用 echo command
	if err := client.Close(t.Context()); err != nil {
		// Expected 到 fail 用 echo command
		t.Logf("Close failed as expected with dummy command: %v", err)
	}
}

// TestNew_ExpansionFailure_Args pins 那 failing $(cmd) 在 LSP
// args surfaces 作为 load 错误 prefixed "invalid lsp args:" 和 那
// no 客户端 是 returned. Mirrors MCP contract where expansion
// failure hard-stops 传输 creation rather than silently running
// 用 empty 或 literal 值.
func TestNew_ExpansionFailure_Args(t *testing.T) {
	t.Parallel()

	cfg := config.LSPConfig{
		Command: "echo",
		Args:    []string{"--root", "$(false)"},
	}
	resolver := config.NewShellVariableResolver(env.NewFromMap(map[string]string{}))

	client, err := New("test-args-fail", cfg, resolver, ".", false)
	require.Error(t, err)
	require.Nil(t, client, "client must not start when args expansion fails")
	require.Contains(t, err.Error(), "invalid lsp args")
}

// TestNew_ExpansionFailure_Env pins same contract 为 env values.
func TestNew_ExpansionFailure_Env(t *testing.T) {
	t.Parallel()

	cfg := config.LSPConfig{
		Command: "echo",
		Env:     map[string]string{"BAD": "$(false)"},
	}
	resolver := config.NewShellVariableResolver(env.NewFromMap(map[string]string{}))

	client, err := New("test-env-fail", cfg, resolver, ".", false)
	require.Error(t, err)
	require.Nil(t, client, "client must not start when env expansion fails")
	require.Contains(t, err.Error(), "invalid lsp env")
}

func TestNilClient(t *testing.T) {
	t.Parallel()

	var c *Client

	require.False(t, c.HandlesFile("/some/file.go"))
	require.Equal(t, DiagnosticCounts{}, c.GetDiagnosticCounts())
	require.Nil(t, c.GetDiagnostics())
	require.Nil(t, c.OpenFileOnDemand(context.Background(), "/some/file.go"))
	require.Nil(t, c.NotifyChange(context.Background(), "/some/file.go"))
	c.WaitForDiagnostics(context.Background(), time.Second)
}

// newTestClient builds stopped 客户端. Tests 那 wait 在 it 运行 under
// synctest, since real timers stall 在 CI.
func newTestClient() *Client {
	c := &Client{
		name:        "test",
		diagnostics: csync.NewVersionedMap[protocol.DocumentURI, []protocol.Diagnostic](),
		openFiles:   csync.NewMap[string, *OpenFileInfo](),
	}
	c.serverState.Store(StateStopped)
	return c
}

func TestWaitForDiagnostics_NoChange(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c := newTestClient()
		start := time.Now()
		c.WaitForDiagnostics(t.Context(), 5*time.Second)
		elapsed := time.Since(start)

		// Should return early via firstChangeDeadline (1s), 不 full 超时.
		require.Equal(t, time.Second, elapsed, "should return early when no diagnostics change")
	})
}

func TestWaitForDiagnostics_ImmediateChange(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c := newTestClient()

		go func() {
			time.Sleep(100 * time.Millisecond)
			c.diagnostics.Set(protocol.DocumentURI("file:///test.go"), nil)
		}()

		start := time.Now()
		c.WaitForDiagnostics(t.Context(), 5*time.Second)
		elapsed := time.Since(start)

		// Should detect change 和 then settle 为 300ms.
		require.GreaterOrEqual(t, elapsed, 400*time.Millisecond, "should wait for settle duration")
		require.Less(t, elapsed, time.Second, "should return after settling, not full timeout")
	})
}

func TestWaitForDiagnostics_RepeatedChanges(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c := newTestClient()

		// Simulate LSP 服务端 那 publishes diagnostics 在 bursts.
		go func() {
			for i := range 5 {
				time.Sleep(50 * time.Millisecond)
				c.diagnostics.Set(protocol.DocumentURI("file:///test.go"), []protocol.Diagnostic{
					{Message: fmt.Sprintf("diag-%d", i)},
				})
			}
		}()

		start := time.Now()
		c.WaitForDiagnostics(t.Context(), 5*time.Second)
		elapsed := time.Since(start)

		// Burst lasts 250ms, then 300ms settle 窗口.
		require.GreaterOrEqual(t, elapsed, 550*time.Millisecond, "should wait for all changes to settle")
		require.Less(t, elapsed, time.Second, "should return after settling, not full timeout")
	})
}

func TestWaitForDiagnostics_ContextCancellation(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c := newTestClient()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		go func() {
			time.Sleep(200 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		c.WaitForDiagnostics(ctx, 5*time.Second)
		elapsed := time.Since(start)

		require.Equal(t, 200*time.Millisecond, elapsed, "should return as soon as the context is cancelled")
	})
}

func TestWaitForDiagnostics_NilClient(t *testing.T) {
	t.Parallel()

	var c *Client
	// Should 不 panic.
	c.WaitForDiagnostics(context.Background(), time.Second)
}
