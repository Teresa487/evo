package mcp

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// liveSession spins up real 在-memory MCP 服务端 exposing single 工具 和
// returns connected 客户端 会话 wrapped 作为 *ClientSession, mirroring
// what createSession produces 在 production. returned 上下文 是 one
// bound 到 会话's 取消 func, 所以 测试 can assert 会话 was
// actually closed (ctx cancelled) rather than merely dropped. Both sides 是
// torn down via t.清理.
func liveSession(t *testing.T, toolName string) (*ClientSession, context.Context) {
	t.Helper()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "srv"}, nil)
	mcp.AddTool(
		server,
		&mcp.Tool{Name: toolName, Description: "test tool"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
		},
	)
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	client := mcp.NewClient(&mcp.Implementation{Name: "evo-test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)

	return &ClientSession{ClientSession: clientSession, cancel: cancel}, ctx
}

// liveSessionWithCapabilities 是 like liveSession 但 服务端 also exposes
// 提示词 和 resource, 所以 tests can assert those registries 是 populated 在
// (re)connect.
func liveSessionWithCapabilities(t *testing.T, toolName, promptName, resourceURI string) *ClientSession {
	t.Helper()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "srv"}, nil)
	mcp.AddTool(
		server,
		&mcp.Tool{Name: toolName, Description: "test tool"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
		},
	)
	server.AddPrompt(
		&mcp.Prompt{Name: promptName},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		},
	)
	server.AddResource(
		&mcp.Resource{Name: "res", URI: resourceURI},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		},
	)
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	client := mcp.NewClient(&mcp.Implementation{Name: "evo-test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)

	return &ClientSession{ClientSession: clientSession, cancel: cancel}
}

// TestUpdateState_ErrorClosesSessionAndClearsTools pins the primary fix: a
// StateError transition must (1) remove 会话 从 map, (2) actually
// close it 所以 它的 child process/pipes 是 released, 和 (3) clear 它的 tools
// 从 registry. Before fix updateState 只 did bare
// sessions.Del(name): 会话 was leaked 和 它的 tools lingered, 所以
// evo_info kept reading "connected, N tools" while LLM's 工具 list 和
// live 会话 had diverged.
func TestUpdateState_ErrorClosesSessionAndClearsTools(t *testing.T) {
	const name = "test-error-cleanup"
	t.Cleanup(func() {
		sessions.Del(name)
		allTools.Del(name)
		states.Del(name)
	})

	sess, sessCtx := liveSession(t, "do_thing")
	sessions.Set(name, sess)
	allTools.Set(name, []*Tool{{Name: "do_thing"}})

	// Preconditions: 工具 registered 和 会话 live.
	_, ok := allTools.Get(name)
	require.True(t, ok)
	require.NoError(t, sessCtx.Err(), "session context must be live before the error")

	updateState(name, StateError, errors.New("stdio pipe broke"), nil, Counts{Tools: 1})

	// dead 会话 是 removed 从 map...
	_, ok = sessions.Get(name)
	require.False(t, ok, "errored session must be removed from the sessions map")

	// ...actually closed (它的 上下文 是 cancelled, 不 merely dropped)...
	require.ErrorIs(t, sessCtx.Err(), context.Canceled, "errored session must be closed, not just dropped from the map")

	// ...和 它的 tools cleared 从 registry agent sends 到 LLM.
	_, ok = allTools.Get(name)
	require.False(t, ok, "errored session's tools must be cleared from the registry")

	info, ok := GetState(name)
	require.True(t, ok)
	require.Equal(t, StateError, info.State)
}

// TestUpdateState_ErrorFromStaleSessionPreservesHealthyReplacement pins the
// teardown scoping: StateError reported against 会话 那 是 NO LONGER
// registered one ( renewal already replaced it) must 不 tear down
// healthy replacement 或 它的 registrations. Before fix updateState closed
// whatever 会话 was 在 map, 所以 stale 错误 transition — e.g.
// refresh whose list 调用 timed out after another 路径 had already renewed —
// killed fresh 会话 和 wiped 它的 tools.
func TestUpdateState_ErrorFromStaleSessionPreservesHealthyReplacement(t *testing.T) {
	const name = "test-stale-error"
	t.Cleanup(func() {
		sessions.Del(name)
		allTools.Del(name)
		allPrompts.Del(name)
		allResources.Del(name)
		states.Del(name)
	})

	stale, staleCtx := liveSession(t, "old_tool")
	fresh, freshCtx := liveSession(t, "new_tool")

	// registry holds fresh 会话 和 它的 registrations.
	sessions.Set(name, fresh)
	allTools.Set(name, []*Tool{{Name: "new_tool"}})
	allPrompts.Set(name, []*Prompt{{Name: "new_prompt"}})

	// 一个 stale 错误 arrives 为 OLD 会话.
	updateState(name, StateError, errors.New("ping timeout"), stale, Counts{})

	// fresh 会话 must still 是 registered 和 open.
	got, ok := sessions.Get(name)
	require.True(t, ok, "healthy replacement session was removed")
	require.Same(t, fresh, got)
	require.NoError(t, freshCtx.Err(), "healthy replacement session was closed")
	_, ok = allTools.Get(name)
	require.True(t, ok, "healthy replacement's tools were cleared")
	_, ok = allPrompts.Get(name)
	require.True(t, ok, "healthy replacement's prompts were cleared")

	// stale 会话 must have been closed.
	require.ErrorIs(t, staleCtx.Err(), context.Canceled, "stale session must still be closed")
}

// TestUpdateState_ErrorFromCurrentSessionClearsEverything pins the complement:
// 当 erroring 会话 IS registered one, teardown must behave
// exactly 作为 before scoping — 会话 removed 和 closed, every registry
// 条目 cleared, 和 published state must 不 carry dead 会话.
func TestUpdateState_ErrorFromCurrentSessionClearsEverything(t *testing.T) {
	const name = "test-current-error"
	t.Cleanup(func() {
		sessions.Del(name)
		allTools.Del(name)
		allPrompts.Del(name)
		allResources.Del(name)
		states.Del(name)
	})

	sess, sessCtx := liveSession(t, "do_thing")
	sessions.Set(name, sess)
	allTools.Set(name, []*Tool{{Name: "do_thing"}})
	allPrompts.Set(name, []*Prompt{{Name: "a_prompt"}})

	updateState(name, StateError, errors.New("pipe broke"), sess, Counts{})

	_, ok := sessions.Get(name)
	require.False(t, ok, "errored current session must be removed")
	require.ErrorIs(t, sessCtx.Err(), context.Canceled, "errored current session must be closed")
	_, ok = allTools.Get(name)
	require.False(t, ok, "errored current session's tools must be cleared")
	_, ok = allPrompts.Get(name)
	require.False(t, ok, "errored current session's prompts must be cleared")

	info, ok := GetState(name)
	require.True(t, ok)
	require.Equal(t, StateError, info.State)
	require.Nil(t, info.Client, "a dead session must never be published on the state")
}

// TestUpdateState_ConfigBookkeeping pins 配置 snapshot reconcile relies
// 在: StateConnected records 配置 now 在 effect 和 clears any pending
// attempt, StateStarting records 配置 在-flight attempt 是 using,
// StateDisabled clears recorded 配置 所以 re-enable restarts, 和 every
// other transition preserves what was there.
func TestUpdateState_ConfigBookkeeping(t *testing.T) {
	const name = "test-config-bookkeeping"
	t.Cleanup(func() {
		states.Del(name)
	})

	base := config.MCPConfig{Type: config.MCPHttp, URL: "https://example.com/mcp"}
	changed := base
	changed.URL = "https://other.com/mcp"

	// Connecting records 配置 和 clears any pending attempt.
	updateState(name, StateStarting, nil, nil, Counts{}, withPending(base))
	updateState(name, StateConnected, nil, nil, Counts{}, withConfig(base))
	info, _ := GetState(name)
	require.Equal(t, base, info.Config, "connected state must record its config")
	require.Nil(t, info.PendingConfig, "connected state must clear the pending config")

	// Starting records 配置 attempt 是 connecting 用.
	updateState(name, StateStarting, nil, nil, Counts{}, withPending(changed))
	info, _ = GetState(name)
	require.NotNil(t, info.PendingConfig, "starting state must record the pending config")
	require.Equal(t, changed, *info.PendingConfig)
	require.Equal(t, base, info.Config, "starting must not disturb the last connected config")

	// An 错误 preserves both 所以 reconcile can still reason about 服务端.
	updateState(name, StateError, errors.New("boom"), nil, Counts{})
	info, _ = GetState(name)
	require.Equal(t, base, info.Config, "error must preserve the connected config")
	require.NotNil(t, info.PendingConfig, "error must preserve the pending config")

	// Disabling clears both 所以 re-enable 用 unchanged 配置 restarts.
	updateState(name, StateDisabled, nil, nil, Counts{})
	info, _ = GetState(name)
	require.Equal(t, config.MCPConfig{}, info.Config, "disabled must clear the connected config")
	require.Nil(t, info.PendingConfig, "disabled must clear the pending config")
}

// TestUpdateState_ErrorClearsPromptsAndResources pins 那 StateError
// transition also drops dead 服务端's prompts 和 resources, 不 just 它的
// tools. Leaving them registered lets disconnected 服务端 keep advertising
// capabilities agent 可以 no longer fulfil — same state/registry
// divergence 工具 clear exists 到 prevent.
func TestUpdateState_ErrorClearsPromptsAndResources(t *testing.T) {
	const name = "test-error-clears-all"
	t.Cleanup(func() {
		sessions.Del(name)
		allTools.Del(name)
		allPrompts.Del(name)
		allResources.Del(name)
		states.Del(name)
	})

	allTools.Set(name, []*Tool{{Name: "do_thing"}})
	allPrompts.Set(name, []*Prompt{{Name: "a_prompt"}})
	allResources.Set(name, []*Resource{{Name: "a_resource"}})

	updateState(name, StateError, errors.New("pipe broke"), nil, Counts{})

	_, ok := allTools.Get(name)
	require.False(t, ok, "errored session's tools must be cleared")
	_, ok = allPrompts.Get(name)
	require.False(t, ok, "errored session's prompts must be cleared")
	_, ok = allResources.Get(name)
	require.False(t, ok, "errored session's resources must be cleared")
}

// TestGetOrRenewClient_SerializesConcurrentRenewals 是 concurrency
// regression production renew 路径 needs: 当 several 工具 calls observe
// same dead 会话 在 once they must 不 each rebuild it. Without
// serialization, concurrent renewals close 会话 another goroutine just
// registered 或 overwrite 和 leak live replacement. With per-服务端
// 锁 只 first arrival rebuilds; rest re-check 和 reuse
// healthy 会话, 所以 exactly one new 会话 是 created.
func TestGetOrRenewClient_SerializesConcurrentRenewals(t *testing.T) {
	const name = "test-renew-concurrency"
	const workers = 8

	t.Cleanup(func() {
		if s, ok := sessions.Take(name); ok {
			_ = s.Close()
		}
		allTools.Del(name)
		states.Del(name)
	})

	cfg := config.NewTestStore(&config.Config{MCP: config.MCPs{name: {Type: config.MCPStdio}}})

	// Seed dead 会话 所以 first ping fails 和 every worker attempts
	// renewal.
	dead, _ := liveSession(t, "send_message")
	require.NoError(t, dead.Close())
	sessions.Set(name, dead)

	// Pre-build enough live replacements 那 buggy (unserialized) 路径
	// could consume more than one; fix 必须 consume exactly one.
	replacements := make(chan *ClientSession, workers)
	for range workers {
		s, _ := liveSession(t, "send_message")
		replacements <- s
	}
	close(replacements)
	t.Cleanup(func() {
		for s := range replacements {
			_ = s.Close()
		}
	})

	var created atomic.Int32
	origNewSession := newSession
	newSession = func(context.Context, *config.ConfigStore, string, config.MCPConfig, config.VariableResolver, bool) (*ClientSession, error) {
		created.Add(1)
		return <-replacements, nil
	}
	t.Cleanup(func() { newSession = origNewSession })

	var wg sync.WaitGroup
	results := make([]*ClientSession, workers)
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = getOrRenewClient(context.Background(), cfg, name)
		}(i)
	}
	wg.Wait()

	require.Equal(t, int32(1), created.Load(),
		"exactly one renewal must occur; concurrent callers must reuse the renewed session")

	final, ok := sessions.Get(name)
	require.True(t, ok, "a live session must remain registered after concurrent renewals")
	for i := range workers {
		require.NoError(t, errs[i])
		require.Same(t, final, results[i], "every caller must observe the same renewed session")
	}
}

// TestRegisterSessionTools_PopulatesRegistry pins 那 registerSessionTools —
// single seam through which (re)connected 会话's tools enter
// registry — lists live 会话's tools 和 writes them 到 allTools.
func TestRegisterSessionTools_PopulatesRegistry(t *testing.T) {
	const name = "test-register-tools"
	t.Cleanup(func() { allTools.Del(name) })

	sess, _ := liveSession(t, "send_message")
	t.Cleanup(func() { _ = sess.Close() })

	cfg := config.NewTestStore(&config.Config{MCP: config.MCPs{name: {Type: config.MCPStdio}}})

	count, err := registerSessionTools(context.Background(), cfg, name, sess)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	got, ok := allTools.Get(name)
	require.True(t, ok, "a live session's tools must be registered")
	require.Len(t, got, 1)
	require.Equal(t, "send_message", got[0].Name)
}

// TestSessionErrorThenRenew_RestoresTools 是 end-到-end regression 为
// reported bug: MCP 工具 works, stdio 会话 drops mid-conversation,
// 和 afterwards every 调用 returned "工具 不 found" forever. 它 walks
// exact registry transitions the production code performs — initial connect
// registers tools, StateError clears them (和 closes 会话), 和
// lazy renew re-registers them — 所以 regression 在 any leg (tools left stale
// 在 错误, 或 tools never restored 在 renew) fails here.
func TestSessionErrorThenRenew_RestoresTools(t *testing.T) {
	const name = "test-error-then-renew"
	t.Cleanup(func() {
		if s, ok := sessions.Take(name); ok {
			_ = s.Close()
		}
		allTools.Del(name)
		states.Del(name)
	})

	cfg := config.NewTestStore(&config.Config{MCP: config.MCPs{name: {Type: config.MCPStdio}}})

	// 1. Initial connect registers 工具 (mirrors initClient).
	sess1, _ := liveSession(t, "send_message")
	sessions.Set(name, sess1)
	_, err := registerSessionTools(context.Background(), cfg, name, sess1)
	require.NoError(t, err)
	_, ok := allTools.Get(name)
	require.True(t, ok, "tool should be registered after the initial connect")

	// 2. 会话 drops mid-conversation -> StateError. Post-fix 这 clears
	// tools 和 closes dead 会话.
	updateState(name, StateError, errors.New("pipe broke"), nil, Counts{Tools: 1})
	_, ok = allTools.Get(name)
	require.False(t, ok, "tools must be cleared when the session errors")
	_, ok = sessions.Get(name)
	require.False(t, ok, "errored session must be removed from the map")

	// 3. lazy renew 路径 creates fresh 会话 和 MUST re-register
	// tools. bug was 那 it never did: LLM's 工具 list stayed empty
	// 和 every subsequent 调用 returned "工具 不 found".
	sess2, _ := liveSession(t, "send_message")
	count, err := registerSessionTools(context.Background(), cfg, name, sess2)
	require.NoError(t, err)
	sessions.Set(name, sess2)
	require.Equal(t, 1, count)

	got, ok := allTools.Get(name)
	require.True(t, ok, "tools must be restored after the session is renewed")
	require.Len(t, got, 1)
	require.Equal(t, "send_message", got[0].Name)
}

// TestGetOrRenewClient_RestoresPromptsAndResources pins 那 renewal
// repopulates every registry 和 reports counts 那 match. StateError clears
// tools, prompts, 和 resources; 如果 renewal restored 只 tools while keeping
// old 提示词/resource counts, GetState would again advertise capabilities
// absent 从 registries.
func TestGetOrRenewClient_RestoresPromptsAndResources(t *testing.T) {
	const name = "test-renew-prompts-resources"
	t.Cleanup(func() {
		if s, ok := sessions.Take(name); ok {
			_ = s.Close()
		}
		allTools.Del(name)
		allPrompts.Del(name)
		allResources.Del(name)
		states.Del(name)
	})

	cfg := config.NewTestStore(&config.Config{MCP: config.MCPs{name: {Type: config.MCPStdio}}})

	// Seed dead 会话 所以 renewal 路径 runs.
	dead, _ := liveSession(t, "send_message")
	require.NoError(t, dead.Close())
	sessions.Set(name, dead)
	// Stale counts 那 must 是 recomputed, 不 preserved.
	updateState(name, StateConnected, nil, dead, Counts{Tools: 1, Prompts: 1, Resources: 1})

	replacement := liveSessionWithCapabilities(t, "send_message", "a_prompt", "res://thing")
	origNewSession := newSession
	newSession = func(context.Context, *config.ConfigStore, string, config.MCPConfig, config.VariableResolver, bool) (*ClientSession, error) {
		return replacement, nil
	}
	t.Cleanup(func() { newSession = origNewSession })

	sess, err := getOrRenewClient(context.Background(), cfg, name)
	require.NoError(t, err)
	require.Same(t, replacement, sess)

	tools, ok := allTools.Get(name)
	require.True(t, ok, "tools must be restored on renewal")
	require.Len(t, tools, 1)

	prompts, ok := allPrompts.Get(name)
	require.True(t, ok, "prompts must be restored on renewal")
	require.Len(t, prompts, 1)

	resources, ok := allResources.Get(name)
	require.True(t, ok, "resources must be restored on renewal")
	require.Len(t, resources, 1)

	info, ok := GetState(name)
	require.True(t, ok)
	require.Equal(t, StateConnected, info.State)
	require.Equal(t, Counts{Tools: 1, Prompts: 1, Resources: 1}, info.Counts,
		"reported counts must match the restored registries")
}

// testTransportWrapper 是 second, 测试-local decorator. maybeStdioErr must
// see through arbitrary stack 的 them, 不 just one wrapper 那
// happens 到 exist 在 createSession today.
type testTransportWrapper struct {
	mcp.Transport
	inner mcp.Transport
}

func (t *testTransportWrapper) unwrapTransport() mcp.Transport { return t.inner }

// withLongStdioCheckTimeout raises stdioCheck's 超时 为 测试.
func withLongStdioCheckTimeout(t *testing.T) {
	t.Helper()
	orig := stdioCheckTimeout
	stdioCheckTimeout = time.Minute
	t.Cleanup(func() { stdioCheckTimeout = orig })
}

// TestMaybeStdioErr_UnwrapsChannelTransport pins 那 maybeStdioErr sees
// through channelTransport wrapper 到 inner CommandTransport.
//
// Every transport 是 wrapped 在 channelTransport before Connect, 所以
// *mcp.CommandTransport assertion never matched 和 failed stdio 服务端 (
// missing npx, node 不 在 路径) reported bare EOF 用 child's stderr
// thrown away — exact diagnostic stdioCheck exists 到 provide. We assert
// both 那 unwrap reaches command ( 错误 是 no longer bare EOF)
// 和 那 re-executed child's output surfaces 在 joined 错误.
func TestMaybeStdioErr_UnwrapsChannelTransport(t *testing.T) {
	withLongStdioCheckTimeout(t)
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "echo 'startup failed: bad config'; exit 3")
	inner := &mcp.CommandTransport{Command: cmd}
	wrapped := &channelTransport{inner: inner, name: "t", gate: newChannelGate()}

	got := maybeStdioErr(io.EOF, wrapped)
	require.Error(t, got)
	require.NotEqual(t, io.EOF, got, "the unwrap must reach the command transport")
	require.ErrorContains(t, got, "startup failed: bad config",
		"the re-executed child's output must surface in the error")
}

// TestMaybeStdioErr_UnwrapsEveryWrapper pins the unwrap against future
// decorators: it must peel whole stack, 不 fixed number 的 layers.
func TestMaybeStdioErr_UnwrapsEveryWrapper(t *testing.T) {
	withLongStdioCheckTimeout(t)
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "echo boom-diagnostic >&2; exit 3")
	var transport mcp.Transport = &mcp.CommandTransport{Command: cmd}
	transport = &channelTransport{inner: transport, name: "t", gate: newChannelGate()}
	transport = &testTransportWrapper{inner: transport}

	got := maybeStdioErr(io.EOF, transport)
	require.ErrorContains(t, got, "boom-diagnostic",
		"stdio diagnostics must survive every transport decorator")
}

// TestStdioCheck_DoesNotDuplicateArgv0 pins argv0 handling 在
// diagnostic re-运行. exec.Cmd.Args carries argv0 作为 它的 first element 和
// exec.CommandContext prepends 路径 作为 argv0 itself, 所以 passing Args through
// whole re-ran "sh sh -c ..." — 和 错误 reported 那 malformed
// command's failure instead 的 child's real startup output.
func TestStdioCheck_DoesNotDuplicateArgv0(t *testing.T) {
	withLongStdioCheckTimeout(t)
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "echo 'real startup error'; exit 3")

	err := stdioCheck(cmd)
	require.Error(t, err)
	require.ErrorContains(t, err, "real startup error",
		"the re-run must execute the original command, not a duplicated argv0")
	require.NotContains(t, err.Error(), "cannot execute binary file",
		"a duplicated argv0 makes the shell try to exec itself as a script")
}
