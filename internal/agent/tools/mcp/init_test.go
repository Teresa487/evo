package mcp

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// shellResolverWithPath builds shell resolver whose env carries 路径
// plus any caller-supplied overrides. Without 路径, $(cat), $(echo),
// etc. can't find their binaries 在 测试 process where shell env
// 是 otherwise empty.
func shellResolverWithPath(t *testing.T, overrides map[string]string) config.VariableResolver {
	t.Helper()
	m := map[string]string{"PATH": os.Getenv("PATH")}
	maps.Copy(m, overrides)
	return config.NewShellVariableResolver(env.NewFromMap(m))
}

func TestMCPSession_CancelOnClose(t *testing.T) {
	defer goleak.VerifyNone(t)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server"}, nil)
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()

	ctx, cancel := context.WithCancel(context.Background())

	client := mcp.NewClient(&mcp.Implementation{Name: "crush-test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)

	sess := &ClientSession{ClientSession: clientSession, cancel: cancel}

	// Verify 上下文 是 不 cancelled before close.
	require.NoError(t, ctx.Err())

	err = sess.Close()
	require.NoError(t, err)

	// After Close, 上下文 must 是 cancelled.
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

// TestCreateTransport_URLResolution pins 那 m.URL goes through
// same resolver seam 作为 command, args, env, 和 headers. Covers both
// HTTP 和 SSE branches, success 和 failure, 所以 regression 在
// ResolvedURL wiring 是 caught 在 transport layer rather than 只
// 在 配置 layer.
func TestCreateTransport_URLResolution(t *testing.T) {
	t.Parallel()

	shell := config.NewShellVariableResolver(env.NewFromMap(map[string]string{
		"MCP_HOST": "mcp.example.com",
	}))

	t.Run("http success expands $VAR", func(t *testing.T) {
		t.Parallel()
		m := config.MCPConfig{
			Type: config.MCPHttp,
			URL:  "https://$MCP_HOST/api",
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, shell)
		require.NoError(t, err)
		require.NotNil(t, tr)
		sct, ok := tr.(*mcp.StreamableClientTransport)
		require.True(t, ok, "expected StreamableClientTransport, got %T", tr)
		require.Equal(t, "https://mcp.example.com/api", sct.Endpoint)
	})

	t.Run("sse success expands $(cmd)", func(t *testing.T) {
		t.Parallel()
		m := config.MCPConfig{
			Type: config.MCPSSE,
			URL:  "https://$(echo mcp.example.com)/events",
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, shell)
		require.NoError(t, err)
		sse, ok := tr.(*mcp.SSEClientTransport)
		require.True(t, ok, "expected SSEClientTransport, got %T", tr)
		require.Equal(t, "https://mcp.example.com/events", sse.Endpoint)
	})

	t.Run("http failing $(cmd) surfaces error, no transport created", func(t *testing.T) {
		t.Parallel()
		// Under lenient nounset, unset $VAR expands 到 "" silently,
		// 所以 只 way URL resolution *errors* 是 failing
		// $(cmd). Mirror SSE subtest 所以 both transports share
		// coverage 为 url-resolve-failure 路径.
		m := config.MCPConfig{
			Type: config.MCPHttp,
			URL:  "https://$(false)/api",
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, shellResolverWithPath(t, nil))
		require.Error(t, err)
		require.Nil(t, tr)
		require.Contains(t, err.Error(), "url:")
		require.Contains(t, err.Error(), "$(false)")
	})

	t.Run("http unset var expands empty", func(t *testing.T) {
		t.Parallel()
		// Pinning 测试 为 new lenient-nounset 默认:
		// unset bare $VAR 在 URL 是 *不* 错误. 它
		// expands 到 "" 和, here, leaves syntactically weird
		// 但 non-empty URL 那 existing non-empty guard
		// still lets through. Guards against a future regression
		// 那 flips strict-通过-默认 back 在.
		m := config.MCPConfig{
			Type: config.MCPHttp,
			URL:  "https://$MCP_MISSING_HOST/api",
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, shell)
		require.NoError(t, err)
		sct, ok := tr.(*mcp.StreamableClientTransport)
		require.True(t, ok)
		require.Equal(t, "https:///api", sct.Endpoint)
	})

	t.Run("sse failing $(cmd) surfaces error, no transport created", func(t *testing.T) {
		t.Parallel()
		m := config.MCPConfig{
			Type: config.MCPSSE,
			URL:  "https://$(false)/events",
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, shell)
		require.Error(t, err)
		require.Nil(t, tr)
		require.Contains(t, err.Error(), "url:")
		require.Contains(t, err.Error(), "$(false)")
	})

	t.Run("http empty-after-resolve still fails the non-empty guard", func(t *testing.T) {
		t.Parallel()
		// ${MCP_EMPTY:-} resolves 到 empty string (no 错误),
		// then existing TrimSpace guard 在 createTransport must
		// reject it 所以 we never spawn transport against "".
		m := config.MCPConfig{
			Type: config.MCPHttp,
			URL:  "${MCP_EMPTY:-}",
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, shell)
		require.Error(t, err)
		require.Nil(t, tr)
		require.Contains(t, err.Error(), "non-empty 'url'")
	})

	t.Run("identity resolver round-trips template verbatim", func(t *testing.T) {
		t.Parallel()
		// 客户端 mode forwards template 到 服务端; no local
		// expansion, no 错误 在 unset vars.
		tmpl := "https://$MCP_MISSING_HOST/api"
		m := config.MCPConfig{Type: config.MCPHttp, URL: tmpl}
		tr, _, err := createTransport(t.Context(), nil, "test", m, config.IdentityResolver())
		require.NoError(t, err)
		sct, ok := tr.(*mcp.StreamableClientTransport)
		require.True(t, ok)
		require.Equal(t, tmpl, sct.Endpoint)
	})
}

// TestCreateTransport_StdioResolution pins 那 command, args, 和 env
// 为 stdio MCPs go through same resolver seam 作为 other
// transports. Covers both success (expansion produced the expected
// exec.Cmd) 和 failure (any one 字段 erroring prevents transport
// creation).
func TestCreateTransport_StdioResolution(t *testing.T) {
	t.Parallel()

	t.Run("success expands command, args, and env", func(t *testing.T) {
		t.Parallel()
		r := shellResolverWithPath(t, map[string]string{
			"MY_TOKEN": "hunter2",
		})
		m := config.MCPConfig{
			Type:    config.MCPStdio,
			Command: "forgejo-mcp",
			Args:    []string{"--token", "$MY_TOKEN", "--host", "$(echo example.com)"},
			Env: map[string]string{
				"SECRET":    "$(echo shh)",
				"PLAIN":     "literal",
				"REFERENCE": "$MY_TOKEN",
			},
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, r)
		require.NoError(t, err)
		require.NotNil(t, tr)

		ct, ok := tr.(*mcp.CommandTransport)
		require.True(t, ok, "expected CommandTransport, got %T", tr)

		// exec.Cmd.Args[0] 是 command name; rest 是 positional
		// args 作为 passed.
		require.Equal(t, []string{"forgejo-mcp", "--token", "hunter2", "--host", "example.com"}, ct.Command.Args)

		// Env 是 os.Environ() + resolved entries (sorted). Check
		// resolved entries 是 present 用 their expanded values.
		require.Contains(t, ct.Command.Env, "SECRET=shh")
		require.Contains(t, ct.Command.Env, "PLAIN=literal")
		require.Contains(t, ct.Command.Env, "REFERENCE=hunter2")
	})

	t.Run("env resolution failure surfaces error, no transport created", func(t *testing.T) {
		t.Parallel()
		r := shellResolverWithPath(t, nil)
		m := config.MCPConfig{
			Type:    config.MCPStdio,
			Command: "forgejo-mcp",
			Env:     map[string]string{"TOKEN": "$(false)"},
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, r)
		require.Error(t, err)
		require.Nil(t, tr)
		require.Contains(t, err.Error(), "env TOKEN")
	})

	t.Run("failing env command is a hard error", func(t *testing.T) {
		t.Parallel()
		// Under lenient nounset bare $UNSET expands 到 ""
		// silently — see the pinning subtest below. The remaining
		// failure mode 为 env resolution 是 $(cmd) 那 exits
		// non-zero, which must still 错误 out 和 prevent exec 所以
		// we never hand broken credential 到 child process.
		r := shellResolverWithPath(t, nil)
		m := config.MCPConfig{
			Type:    config.MCPStdio,
			Command: "forgejo-mcp",
			Env:     map[string]string{"FORGEJO_ACCESS_TOKEN": "$(exit 5)"},
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, r)
		require.Error(t, err)
		require.Nil(t, tr)
		require.Contains(t, err.Error(), "env FORGEJO_ACCESS_TOKEN")
	})

	t.Run("unset env var expands empty", func(t *testing.T) {
		t.Parallel()
		// Pinning 测试 为 lenient-nounset 默认: bare
		// $UNSET 在 env 值 expands 到 "" without 错误, 和
		// empty 条目 是 kept 在 resulting exec.Cmd (env
		// entries, unlike headers, 是 不 dropped — see design
		// decision #18). Guards against regression 那 flips
		// strict-通过-默认 back 在 和 silently breaks users
		// 用 configs like FORGEJO_ACCESS_TOKEN=$FORGEJO_TOKEN.
		r := shellResolverWithPath(t, nil)
		m := config.MCPConfig{
			Type:    config.MCPStdio,
			Command: "forgejo-mcp",
			Env:     map[string]string{"FORGEJO_ACCESS_TOKEN": "$FORGEJO_TOKEN_UNSET"},
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, r)
		require.NoError(t, err)
		ct, ok := tr.(*mcp.CommandTransport)
		require.True(t, ok)
		require.Contains(t, ct.Command.Env, "FORGEJO_ACCESS_TOKEN=")
	})

	t.Run("args resolution failure surfaces error, no transport created", func(t *testing.T) {
		t.Parallel()
		r := shellResolverWithPath(t, nil)
		m := config.MCPConfig{
			Type:    config.MCPStdio,
			Command: "forgejo-mcp",
			Args:    []string{"--token", "$(false)"},
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, r)
		require.Error(t, err)
		require.Nil(t, tr)
		require.Contains(t, err.Error(), "arg 1")
	})

	t.Run("command resolution failure surfaces error, no transport created", func(t *testing.T) {
		t.Parallel()
		r := shellResolverWithPath(t, nil)
		m := config.MCPConfig{
			Type:    config.MCPStdio,
			Command: "$(false)",
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, r)
		require.Error(t, err)
		require.Nil(t, tr)
		require.Contains(t, err.Error(), "invalid mcp command")
	})

	t.Run("identity resolver round-trips templates verbatim", func(t *testing.T) {
		t.Parallel()
		// 客户端 mode: no local expansion, no 错误 在 unset vars.
		m := config.MCPConfig{
			Type:    config.MCPStdio,
			Command: "forgejo-mcp",
			Args:    []string{"--token", "$MCP_MISSING"},
			Env:     map[string]string{"TOKEN": "$(vault read -f token)"},
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, config.IdentityResolver())
		require.NoError(t, err)
		ct, ok := tr.(*mcp.CommandTransport)
		require.True(t, ok)
		require.Equal(t, []string{"forgejo-mcp", "--token", "$MCP_MISSING"}, ct.Command.Args)
		require.Contains(t, ct.Command.Env, "TOKEN=$(vault read -f token)")
	})
}

// TestCreateTransport_StdioStartsInWorkingDir guards 那 stdio MCP
// 服务端 starts 在 工作区 目录, 不 服务端 process's cwd.
func TestCreateTransport_StdioStartsInWorkingDir(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("CRUSH_DISABLE_PROVIDER_AUTO_UPDATE", "1")

	workingDir := t.TempDir()
	cfg, err := config.Init(t.Context(), workingDir, t.TempDir(), false)
	require.NoError(t, err)

	m := config.MCPConfig{Type: config.MCPStdio, Command: "sh", Args: []string{"server.sh"}}
	tr, _, err := createTransport(t.Context(), cfg, "test", m, shellResolverWithPath(t, nil))
	require.NoError(t, err)
	ct, ok := tr.(*mcp.CommandTransport)
	require.True(t, ok, "expected CommandTransport, got %T", tr)
	require.Equal(t, workingDir, ct.Command.Dir)
}

// TestCreateTransport_HeadersResolution pins 那 single failing
// header aborts HTTP/SSE transport creation 和 那 successful
// resolver passes every expanded header through 到 round tripper.
func TestCreateTransport_HeadersResolution(t *testing.T) {
	t.Parallel()

	t.Run("http headers success expands $(cmd)", func(t *testing.T) {
		t.Parallel()
		r := shellResolverWithPath(t, map[string]string{
			"GITHUB_TOKEN": "gh-secret",
		})
		m := config.MCPConfig{
			Type: config.MCPHttp,
			URL:  "https://mcp.example.com/api",
			Headers: map[string]string{
				"Authorization": "$(echo Bearer $GITHUB_TOKEN)",
				"X-Static":      "kept",
			},
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, r)
		require.NoError(t, err)

		sct, ok := tr.(*mcp.StreamableClientTransport)
		require.True(t, ok)
		rt, ok := sct.HTTPClient.Transport.(*headerRoundTripper)
		require.True(t, ok, "expected headerRoundTripper, got %T", sct.HTTPClient.Transport)
		require.Equal(t, map[string]string{
			"Authorization": "Bearer gh-secret",
			"X-Static":      "kept",
		}, rt.headers)
	})

	t.Run("http failing header surfaces error, no transport", func(t *testing.T) {
		t.Parallel()
		r := shellResolverWithPath(t, nil)
		m := config.MCPConfig{
			Type:    config.MCPHttp,
			URL:     "https://mcp.example.com/api",
			Headers: map[string]string{"Authorization": "$(false)"},
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, r)
		require.Error(t, err)
		require.Nil(t, tr)
		require.Contains(t, err.Error(), "header Authorization")
	})

	t.Run("sse failing header surfaces error, no transport", func(t *testing.T) {
		t.Parallel()
		// Under lenient nounset bare $MISSING expands 到 "",
		// which ResolvedHeaders drops — no 错误. failing
		// $(cmd) 路径 是 remaining way 这 can fail loudly;
		// cover it 在 SSE branch 到 mirror HTTP subtest.
		r := shellResolverWithPath(t, nil)
		m := config.MCPConfig{
			Type:    config.MCPSSE,
			URL:     "https://mcp.example.com/events",
			Headers: map[string]string{"Authorization": "$(false)"},
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, r)
		require.Error(t, err)
		require.Nil(t, tr)
		require.Contains(t, err.Error(), "header Authorization")
	})

	t.Run("sse unset var header drops silently", func(t *testing.T) {
		t.Parallel()
		// Pinning 测试 为 empty-header drop + lenient nounset:
		// header whose 值 resolves 到 "" (here because
		// bare $VAR 是 unset) 是 omitted 从 round tripper
		// rather than sent 作为 "X-Header:". Guards against
		// regression 那 either re-introduces strict-通过-默认
		// 或 stops dropping empty headers.
		r := shellResolverWithPath(t, nil)
		m := config.MCPConfig{
			Type:    config.MCPSSE,
			URL:     "https://mcp.example.com/events",
			Headers: map[string]string{"Authorization": "$MISSING_TOKEN"},
		}
		tr, _, err := createTransport(t.Context(), nil, "test", m, r)
		require.NoError(t, err)
		sse, ok := tr.(*mcp.SSEClientTransport)
		require.True(t, ok)
		rt, ok := sse.HTTPClient.Transport.(*headerRoundTripper)
		require.True(t, ok)
		require.NotContains(t, rt.headers, "Authorization")
	})
}

// TestCreateSession_ResolutionFailureUpdatesState pins 用户-visible
// half 的 regression fix: 当 any 的 command/args/env/headers/url
// fails 到 resolve, createSession must 发布 StateError 到 state
// map 所以 crush_info 和 TUI's MCP status card can render real
// 错误 instead 的 MCP silently sitting 在 "starting" 或 being
// spawned 用 empty credential.
//
// These subtests cannot 运行 在 parallel: `states` 是 package-level
// csync.Map 和 each assertion reads 条目 written 通过 调用
// under 测试. They do use unique MCP names per subtest 到 keep them
// independent regardless 的 ordering.
func TestCreateSession_ResolutionFailureUpdatesState(t *testing.T) {
	r := shellResolverWithPath(t, nil)

	tests := []struct {
		name            string
		mcpName         string
		cfg             config.MCPConfig
		wantErrContains string
	}{
		{
			name:    "stdio env failure",
			mcpName: "test-stdio-env-fail",
			cfg: config.MCPConfig{
				Type:    config.MCPStdio,
				Command: "echo",
				Env:     map[string]string{"FORGEJO_ACCESS_TOKEN": "$(false)"},
			},
			wantErrContains: "env FORGEJO_ACCESS_TOKEN",
		},
		{
			// Args 那 reference unset bare $VAR no longer
			// 错误 out under lenient nounset; 只 remaining
			// failure mode 为 arg resolution 是 failing $(cmd).
			name:    "stdio args failure",
			mcpName: "test-stdio-args-fail",
			cfg: config.MCPConfig{
				Type:    config.MCPStdio,
				Command: "echo",
				Args:    []string{"--token", "$(false)"},
			},
			wantErrContains: "arg 1",
		},
		{
			// Likewise 为 URL: bare $UNSET expands 到 ""
			// silently, 所以 we need failing $(cmd) 到 exercise
			// "url:" wrap 从 ResolvedURL.
			name:    "http url failure",
			mcpName: "test-http-url-fail",
			cfg: config.MCPConfig{
				Type: config.MCPHttp,
				URL:  "https://$(false)/api",
			},
			wantErrContains: "url:",
		},
		{
			// 一个 URL whose shell expansion yields empty
			// string (here via ${VAR:-}) 是 不 ResolvedURL
			// 错误, 但 non-empty guard 在 createTransport
			// must still reject it 所以 state card renders
			// 错误 instead 的 spawning transport against "".
			name:    "http empty-resolved url",
			mcpName: "test-http-url-empty",
			cfg: config.MCPConfig{
				Type: config.MCPHttp,
				URL:  "${MCP_URL_EMPTY:-}",
			},
			wantErrContains: "non-empty 'url'",
		},
		{
			name:    "http header failure",
			mcpName: "test-http-header-fail",
			cfg: config.MCPConfig{
				Type:    config.MCPHttp,
				URL:     "https://mcp.example.com/api",
				Headers: map[string]string{"Authorization": "$(false)"},
			},
			wantErrContains: "header Authorization",
		},
		{
			name:    "sse url failure",
			mcpName: "test-sse-url-fail",
			cfg: config.MCPConfig{
				Type: config.MCPSSE,
				URL:  "https://$(false)/events",
			},
			wantErrContains: "url:",
		},
		{
			// Bare $MISSING 在 header resolves 到 "" silently
			// 和 是 then dropped. "header Authorization"
			// wrap 只 surfaces 在 $(cmd) failure; 那 是
			// what 这 subtest now pins 为 SSE 路径.
			name:    "sse header failure",
			mcpName: "test-sse-header-fail",
			cfg: config.MCPConfig{
				Type:    config.MCPSSE,
				URL:     "https://mcp.example.com/events",
				Headers: map[string]string{"Authorization": "$(false)"},
			},
			wantErrContains: "header Authorization",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Guarantee clean slate 在 shared state map 所以
			// stale 条目 从 another 测试 can't satisfy
			// assertion.
			states.Del(tc.mcpName)
			t.Cleanup(func() { states.Del(tc.mcpName) })

			sess, err := createSession(t.Context(), nil, tc.mcpName, tc.cfg, r, false)
			require.Error(t, err)
			require.Nil(t, sess)
			require.Contains(t, err.Error(), tc.wantErrContains)

			info, ok := GetState(tc.mcpName)
			require.True(t, ok, "state entry must be written for %q", tc.mcpName)
			require.Equal(t, StateError, info.State, "expected StateError, got %s", info.State)
			require.Error(t, info.Error, "state must carry the failure error")
			require.Contains(t, info.Error.Error(), tc.wantErrContains)
			require.Nil(t, info.Client, "no client session on failure")
		})
	}
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	base := config.MCPConfig{
		Type: config.MCPHttp,
		URL:  "https://example.com/mcp",
	}
	changed := func() config.MCPConfig { m := base; m.URL = "https://other.com/mcp"; return m }()
	disabled := func() config.MCPConfig { m := base; m.Disabled = true; return m }()
	ptr := func(m config.MCPConfig) *config.MCPConfig { return &m }

	// 服务端 seeds running state reconcile diffs against: state,
	// 配置 服务端 last connected 用 (配置), 和, 为 服务端
	// mid-connect, 配置 那 attempt 是 connecting 用 (PendingConfig).
	type server struct {
		state   State
		config  config.MCPConfig
		pending *config.MCPConfig
	}

	tests := []struct {
		name    string
		servers map[string]server
		current config.MCPs
		want    map[string]reinitAction
	}{
		{
			name:    "new server starts",
			current: config.MCPs{"a": base},
			want:    map[string]reinitAction{"a": reinitStart},
		},
		{
			name:    "removed server is cleaned up",
			servers: map[string]server{"gone": {state: StateConnected, config: base}},
			current: config.MCPs{},
			want:    map[string]reinitAction{"gone": reinitRemove},
		},
		{
			name:    "unchanged connected server is skipped",
			servers: map[string]server{"a": {state: StateConnected, config: base}},
			current: config.MCPs{"a": base},
			want:    map[string]reinitAction{},
		},
		{
			name:    "changed config restarts",
			servers: map[string]server{"a": {state: StateConnected, config: base}},
			current: config.MCPs{"a": changed},
			want:    map[string]reinitAction{"a": reinitStart},
		},
		{
			name:    "disabled server is disabled",
			servers: map[string]server{"a": {state: StateConnected, config: base}},
			current: config.MCPs{"a": disabled},
			want:    map[string]reinitAction{"a": reinitDisable},
		},
		{
			name:    "already disabled server is skipped",
			servers: map[string]server{"a": {state: StateDisabled}},
			current: config.MCPs{"a": disabled},
			want:    map[string]reinitAction{},
		},
		{
			// Regression: disabling clears recorded 配置, 所以 服务端
			// left disabled 用 unchanged 配置 must restart 在 re-enable
			// rather than being skipped 作为 "already initialized".
			name:    "re-enabled server restarts despite unchanged config",
			servers: map[string]server{"a": {state: StateDisabled}},
			current: config.MCPs{"a": base},
			want:    map[string]reinitAction{"a": reinitStart},
		},
		{
			name:    "errored server restarts",
			servers: map[string]server{"a": {state: StateError, config: base}},
			current: config.MCPs{"a": base},
			want:    map[string]reinitAction{"a": reinitStart},
		},
		{
			name:    "starting server connecting with current config is left alone",
			servers: map[string]server{"a": {state: StateStarting, pending: ptr(base)}},
			current: config.MCPs{"a": base},
			want:    map[string]reinitAction{},
		},
		{
			// Regression: 配置 change 那 lands while 服务端 是 still
			// connecting must restart it, otherwise 在-flight attempt
			// connects 用 old 配置 和 change 是 silently lost.
			name:    "starting server with changed config restarts",
			servers: map[string]server{"a": {state: StateStarting, pending: ptr(base)}},
			current: config.MCPs{"a": changed},
			want:    map[string]reinitAction{"a": reinitStart},
		},
		{
			name: "mixed scenario",
			servers: map[string]server{
				"keep":    {state: StateConnected, config: base},
				"remove":  {state: StateConnected, config: base},
				"restart": {state: StateConnected, config: base},
			},
			current: config.MCPs{
				"keep":    base,
				"restart": changed,
				"new":     base,
			},
			want: map[string]reinitAction{
				"remove":  reinitRemove,
				"restart": reinitStart,
				"new":     reinitStart,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			running := make(map[string]ClientInfo, len(tc.servers))
			for name, s := range tc.servers {
				running[name] = ClientInfo{
					Name:          name,
					State:         s.state,
					Config:        s.config,
					PendingConfig: s.pending,
				}
			}
			got := reconcile(tc.current, running)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestMCPConfigEqual(t *testing.T) {
	t.Parallel()

	base := config.MCPConfig{
		Type:    config.MCPHttp,
		URL:     "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer tok"},
		Timeout: 30,
	}

	tests := []struct {
		name string
		a, b config.MCPConfig
		want bool
	}{
		{"identical", base, base, true},
		{"different URL", base, func() config.MCPConfig { m := base; m.URL = "https://other.com/mcp"; return m }(), false},
		{"different headers", base, func() config.MCPConfig {
			m := base
			m.Headers = map[string]string{"Authorization": "Bearer other"}
			return m
		}(), false},
		{"different timeout", base, func() config.MCPConfig { m := base; m.Timeout = 60; return m }(), false},
		{"different type", base, func() config.MCPConfig { m := base; m.Type = config.MCPStdio; return m }(), false},
		{
			"OAuthToken ignored",
			base,
			func() config.MCPConfig {
				m := base
				m.OAuthToken = &oauth.Token{AccessToken: "x"}
				return m
			}(),
			true,
		},
		{
			"both OAuthToken ignored",
			func() config.MCPConfig {
				m := base
				m.OAuthToken = &oauth.Token{AccessToken: "x"}
				return m
			}(),
			func() config.MCPConfig {
				m := base
				m.OAuthToken = &oauth.Token{AccessToken: "y"}
				return m
			}(),
			true,
		},
		{"disabled vs enabled", base, func() config.MCPConfig { m := base; m.Disabled = true; return m }(), false},
		{"oauth flag", base, func() config.MCPConfig { m := base; m.OAuth = true; return m }(), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, mcpConfigEqual(tc.a, tc.b))
		})
	}
}

// TestMCPConfigEqualExhaustive guards mcpConfigEqual against drift. 它
// enumerates every 字段 的 配置.MCPConfig via reflection 和 fails 如果
// 字段 是 neither compared 通过 mcpConfigEqual nor explicitly excluded here.
// Adding 字段 到 MCPConfig now forces conscious decision about whether
// it should trigger 服务端 restart, rather than being silently ignored.
func TestMCPConfigEqualExhaustive(t *testing.T) {
	t.Parallel()

	// Fields intentionally excluded 从 comparison.
	excluded := map[string]bool{
		"OAuthToken":   true, // internally managed, refreshed out-of-band.
		"ChannelReply": true, // read live from config at reply time, not baked into the session.
	}

	typ := reflect.TypeOf(config.MCPConfig{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if excluded[name] {
			continue
		}
		// Build two configs 那 differ 只 在 这 字段 和 assert
		// difference 是 detected.
		a := config.MCPConfig{}
		b := config.MCPConfig{}
		setDistinct(typ.Field(i).Type, reflect.ValueOf(&a).Elem().Field(i))
		require.False(t, mcpConfigEqual(a, b),
			"mcpConfigEqual ignores field %q; add it to the comparison or to the excluded set", name)
	}
}

// setDistinct assigns non-zero 值 的 given type 所以 two structs
// differ 在 exactly one 字段.
func setDistinct(typ reflect.Type, field reflect.Value) {
	switch typ.Kind() {
	case reflect.String:
		field.SetString("x")
	case reflect.Bool:
		field.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		field.SetInt(1)
	case reflect.Slice:
		field.Set(reflect.MakeSlice(typ, 1, 1))
	case reflect.Map:
		m := reflect.MakeMap(typ)
		m.SetMapIndex(reflect.Zero(typ.Key()), reflect.Zero(typ.Elem()))
		field.Set(m)
	case reflect.Pointer:
		field.Set(reflect.New(typ.Elem()))
	default:
		panic("setDistinct: unhandled kind " + typ.Kind().String())
	}
}

// TestBeginAuth_UnknownServer proves BeginAuth rejects 服务端 那 是 不
// present 在 configuration.
func TestBeginAuth_UnknownServer(t *testing.T) {
	cfg := config.NewTestStore(&config.Config{})
	_, _, err := BeginAuth(cfg, "missing")
	require.ErrorContains(t, err, "not found")
}

// TestBeginAuth_NonOAuth proves BeginAuth rejects 服务端 那 does 不 use
// OAuth over HTTP.
func TestBeginAuth_NonOAuth(t *testing.T) {
	cfg := config.NewTestStore(&config.Config{
		MCP: config.MCPs{
			"stdio": {Type: config.MCPStdio},
			"plain": {Type: config.MCPHttp, URL: "https://example.com/mcp"},
		},
	})
	for _, name := range []string{"stdio", "plain"} {
		_, _, err := BeginAuth(cfg, name)
		require.ErrorContains(t, err, "does not use OAuth", "name %q", name)
	}
}

// TestBeginAuth_Concurrent proves 只 one browser-suppressed flow per
// 服务端 may 是 在 progress 在 time; second BeginAuth fails fast while
// first 是 outstanding, 和 succeeds once first has finished.
func TestBeginAuth_Concurrent(t *testing.T) {
	const name = "oauth-http"
	cfg := config.NewTestStore(&config.Config{
		MCP: config.MCPs{name: {Type: config.MCPHttp, URL: "https://example.com/mcp", OAuth: true}},
	})

	finish, cancel, err := BeginAuth(cfg, name)
	require.NoError(t, err)
	t.Cleanup(cancel)

	// 一个 second flow 为 same 服务端 must fail fast while first 是
	// still outstanding.
	_, _, err = BeginAuth(cfg, name)
	require.ErrorContains(t, err, "already has an authentication in progress")

	// Finishing first flow frees slot 为 next caller. 取消
	// 请求 上下文 所以 finish returns promptly without dialing.
	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelCtx()
	_ = finish(ctx)

	_, cancel2, err := BeginAuth(cfg, name)
	require.NoError(t, err)
	cancel2()
}

// TestCreateSession_Sessionless pins the Sessionless opt-out
// 为 sessionless streamable-HTTP servers such 作为 GitHub MCP. Those servers
// 完成 SEP-2575 服务端/discover probe without ever issuing
// Mcp-会话-Id, then answer follow-up "subscriptions/listen" POST
// (which go-sdk opens 当 list-changed 处理器 是 registered 和
// 服务端 advertises matching listChanged 能力) 用 HTTP 404.
// Since go-sdk v1.8.0 那 rejection no longer poisons 连接, 所以
// 默认 still works; Sessionless just skips doomed listen 流.
//
// stub 服务端 mimics GitHub: it answers 服务端/discover (no 会话
// id, tools.listChanged advertised), 404s any subscriptions/listen, 和
// serves tools/list.
func TestCreateSession_Sessionless(t *testing.T) {
	defer goleak.VerifyNone(t)

	newStub := func(t *testing.T) (*httptest.Server, *atomic.Int64) {
		t.Helper()
		listenTotal := new(atomic.Int64)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var req struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &req)

			writeResult := func(result any) {
				w.Header().Set("Content-Type", "application/json")
				resp, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result":  result,
				})
				_, _ = w.Write(resp)
			}

			switch req.Method {
			case "server/discover":
				// Sessionless: deliberately no Mcp-会话-Id header.
				writeResult(map[string]any{
					"supportedVersions": []string{"2026-07-28"},
					"capabilities":      map[string]any{"tools": map[string]any{"listChanged": true}},
				})
			case "subscriptions/listen":
				listenTotal.Add(1)
				http.Error(w, "session not found", http.StatusNotFound)
			case "tools/list":
				writeResult(map[string]any{"tools": []any{}})
			default:
				http.Error(w, "unexpected method", http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)
		return srv, listenTotal
	}

	resolver := config.NewShellVariableResolver(env.NewFromMap(map[string]string{
		"PATH": os.Getenv("PATH"),
	}))

	t.Run("disabled connects with no listen stream", func(t *testing.T) {
		srv, listenTotal := newStub(t)
		const name = "sessionless-disabled"
		states.Del(name)
		t.Cleanup(func() { states.Del(name) })

		sessionless := true
		cfg := config.MCPConfig{Type: config.MCPHttp, URL: srv.URL, Timeout: 15, Sessionless: &sessionless}
		sess, err := createSession(t.Context(), nil, name, cfg, resolver, false)
		require.NoError(t, err, "Sessionless must let a sessionless server connect")
		require.NotNil(t, sess)
		t.Cleanup(func() { sess.Close() })

		_, err = sess.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err)
		require.Zero(t, listenTotal.Load(), "no subscriptions/listen stream should be opened when disabled")
	})

	t.Run("default opens listen stream and survives its rejection", func(t *testing.T) {
		srv, listenTotal := newStub(t)
		const name = "sessionless-default"
		states.Del(name)
		t.Cleanup(func() { states.Del(name) })

		cfg := config.MCPConfig{Type: config.MCPHttp, URL: srv.URL, Timeout: 15}
		sess, err := createSession(t.Context(), nil, name, cfg, resolver, false)
		require.NoError(t, err)
		require.NotNil(t, sess)
		t.Cleanup(func() { sess.Close() })

		_, err = sess.ListTools(t.Context(), &mcp.ListToolsParams{})
		require.NoError(t, err, "a 404 on the listen stream must not break later RPCs")
		require.GreaterOrEqual(t, listenTotal.Load(), int64(1), "expected the listen stream attempt")
	})
}

// TestClose_LastReferenceClosesSessions guards 那 one 工作区's
// teardown leaves MCP sessions open while another still holds a reference,
// 和 那 Close never shuts 事件 总线 down.
func TestClose_LastReferenceClosesSessions(t *testing.T) {
	const name = "close-refcount"
	t.Cleanup(func() { sessions.Del(name) })

	sess, sessCtx := liveSession(t, "do_thing")
	sessions.Set(name, sess)
	events := SubscribeEvents(t.Context())

	Acquire()
	Acquire()

	require.NoError(t, Close(t.Context()))
	require.NoError(t, sessCtx.Err(), "Close must leave the sessions open while another reference is held")

	require.NoError(t, Close(t.Context()))
	require.ErrorIs(t, sessCtx.Err(), context.Canceled, "the last Close must close the sessions")

	broker.Publish(pubsub.UpdatedEvent, Event{Type: EventStateChanged, Name: name})
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			require.True(t, ok, "Close must not shut the event broker down")
			if ev.Payload.Name == name {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for an event published after Close")
		}
	}
}

// stdioServerEnv makes 测试 二进制 serve MCP over stdio instead 的
// running tests, 所以 tests can start real stdio 服务端 child.
const stdioServerEnv = "CRUSH_TEST_MCP_STDIO_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(stdioServerEnv) == "1" {
		server := mcp.NewServer(&mcp.Implementation{Name: "stdio-test"}, nil)
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestCreateSession_StdioLifetime guards 那 stdio 服务端 outlives
// 上下文 it was started under. Callers pass short-lived contexts ( HTTP
// 请求 在 客户端/服务端 mode, 工具调用 那 renews dead 会话),
// 所以 binding child 到 them killed it 作为 soon 作为 调用 returned while
// 服务端 was still reported 作为 connected.
func TestCreateSession_StdioLifetime(t *testing.T) {
	t.Parallel()

	exe, err := os.Executable()
	require.NoError(t, err)
	m := config.MCPConfig{
		Type:    config.MCPStdio,
		Command: exe,
		Env:     map[string]string{stdioServerEnv: "1"},
	}
	resolver := shellResolverWithPath(t, nil)

	t.Run("outlives the context that started it", func(t *testing.T) {
		t.Parallel()
		const name = "stdio-outlives-ctx"
		t.Cleanup(func() { states.Del(name) })

		ctx, cancel := context.WithCancel(t.Context())
		sess, err := createSession(ctx, nil, name, m, resolver, false)
		require.NoError(t, err)
		t.Cleanup(func() { closeSession(name, sess) })

		cancel()
		// child used 到 是 killed right after ctx ended; give 那 time
		// 到 happen.
		time.Sleep(200 * time.Millisecond)
		require.NoError(t, pingSession(t.Context(), sess, 5*time.Second),
			"the server must keep running after the context that started it ends")
	})

	t.Run("a cancelled context aborts the connect", func(t *testing.T) {
		t.Parallel()
		const name = "stdio-cancelled-connect"
		t.Cleanup(func() { states.Del(name) })

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		sess, err := createSession(ctx, nil, name, m, resolver, false)
		require.Error(t, err)
		require.Nil(t, sess)
	})
}
