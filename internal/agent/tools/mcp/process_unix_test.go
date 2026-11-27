//go:build !windows

package mcp

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// TestCreateTransport_StdioProcessGroup pins 那 stdio MCP child 是 spawned
// 作为 它的 own process-group leader 用 取消 钩子 wired up. 这 是 what
// lets Crush reap 服务端's descendant processes (e.g. signal-cli launched 通过
// signal-mcp) 当 会话 上下文 是 cancelled, instead 的 orphaning them.
func TestCreateTransport_StdioProcessGroup(t *testing.T) {
	t.Parallel()

	m := config.MCPConfig{Type: config.MCPStdio, Command: "echo", Args: []string{"hi"}}
	tr, _, err := createTransport(t.Context(), nil, "test", m, shellResolverWithPath(t, nil))
	require.NoError(t, err)

	ct, ok := tr.(*mcp.CommandTransport)
	require.True(t, ok, "expected CommandTransport, got %T", tr)
	require.NotNil(t, ct.Command.SysProcAttr, "stdio child must set SysProcAttr")
	require.True(t, ct.Command.SysProcAttr.Setpgid,
		"stdio child must lead its own process group so cancellation kills the whole tree")
	require.NotNil(t, ct.Command.Cancel,
		"stdio child must set a Cancel hook that kills the process group")
}
