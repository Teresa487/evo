package config

import (
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/env"
	"github.com/stretchr/testify/require"
)

func TestMCPConfig_ResolvedURL(t *testing.T) {
	t.Parallel()

	t.Run("empty url short-circuits without calling resolver", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Type: MCPHttp}
		got, err := m.ResolvedURL(stubResolver{err: errors.New("should not be called")})
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("literal url passes through unchanged", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Type: MCPHttp, URL: "https://mcp.example.com/api"}
		got, err := m.ResolvedURL(NewShellVariableResolver(env.NewFromMap(nil)))
		require.NoError(t, err)
		require.Equal(t, "https://mcp.example.com/api", got)
	})

	t.Run("expands $VAR with shell resolver", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Type: MCPHttp, URL: "https://$MCP_HOST/api"}
		r := NewShellVariableResolver(env.NewFromMap(map[string]string{"MCP_HOST": "mcp.example.com"}))
		got, err := m.ResolvedURL(r)
		require.NoError(t, err)
		require.Equal(t, "https://mcp.example.com/api", got)
	})

	t.Run("expands $(cmd) with shell resolver", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Type: MCPSSE, URL: "https://$(echo mcp.example.com)/events"}
		got, err := m.ResolvedURL(NewShellVariableResolver(env.NewFromMap(nil)))
		require.NoError(t, err)
		require.Equal(t, "https://mcp.example.com/events", got)
	})

	t.Run("unset var expands to empty under lenient default", func(t *testing.T) {
		t.Parallel()
		// 默认 是 nounset-off: bare $VAR 在 unset
		// variable expands 到 "" rather than erroring. Here
		// host collapses 到 empty, 所以 caller sees malformed
		// URL rather than resolver 错误; 那's expected
		// trade-off 为 making $OPTIONAL-style patterns work, 和
		// required-credential callers 应当 use ${VAR:?msg}.
		m := MCPConfig{Type: MCPHttp, URL: "https://$MCP_MISSING_HOST/api"}
		got, err := m.ResolvedURL(NewShellVariableResolver(env.NewFromMap(nil)))
		require.NoError(t, err, "unset var must not error under lenient default")
		require.Equal(t, "https:///api", got)
	})

	t.Run("colon-question on unset var errors regardless of toggle", func(t *testing.T) {
		t.Parallel()
		// ${VAR:?msg} 是 opt-在 strictness mechanism; it must
		// hard-错误 even 用 NoUnset off 所以 required credentials
		// surface 在 load time instead 的 shipping empty-host URLs
		// 到 transport layer.
		m := MCPConfig{Type: MCPHttp, URL: "https://${MCP_MISSING_HOST:?set MCP_MISSING_HOST}/api"}
		_, err := m.ResolvedURL(NewShellVariableResolver(env.NewFromMap(nil)))
		require.Error(t, err)
		require.Contains(t, err.Error(), "url:")
		require.Contains(t, err.Error(), "set MCP_MISSING_HOST")
	})

	t.Run("failing command substitution is an error", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Type: MCPHttp, URL: "https://$(false)/api"}
		_, err := m.ResolvedURL(NewShellVariableResolver(env.NewFromMap(nil)))
		require.Error(t, err)
		require.Contains(t, err.Error(), "url:")
		require.Contains(t, err.Error(), "$(false)")
	})

	t.Run("identity resolver round-trips template verbatim", func(t *testing.T) {
		t.Parallel()
		// In 客户端 mode expansion happens 服务端-side; 客户端 must
		// forward template without touching it 和 without erroring
		// 在 unset vars.
		tmpl := "https://$MCP_HOST/$(vault read -f url)"
		m := MCPConfig{Type: MCPHttp, URL: tmpl}
		got, err := m.ResolvedURL(IdentityResolver())
		require.NoError(t, err)
		require.Equal(t, tmpl, got)
	})
}

// stubResolver returns ("", err) 为 every 调用. Paired 用 non-nil
// err empty-URL 测试 asserts ResolvedURL short-circuits before
// reaching ResolveValue: 如果 it didn't, 测试 would fail 用 err.
type stubResolver struct {
	err error
}

func (s stubResolver) ResolveValue(v string) (string, error) {
	return "", s.err
}
