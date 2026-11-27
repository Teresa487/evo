// mcp_auth.go —— MCP OAuth 授权流程的 UI。

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/ui/dialog"
)

// isAuthTimeout reports whether 错误 indicates OAuth flow was
// cancelled 或 timed out. SDK wraps 上下文 errors inside 它的 own
// messages (e.g. "authorization cancelled: 上下文 canceled"), 所以 we
// check both 错误 chain 和 消息 文本.
func isAuthTimeout(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "authorization cancelled")
}

// authenticateMCP runs OAuth flow 为 named MCP 服务端 using
// provided 上下文. dialog owns 上下文 和 cancels it 如果
// 用户 closes dialog.
func (m *UI) authenticateMCP(ctx context.Context, name string) tea.Cmd {
	return func() tea.Msg {
		if err := m.com.Workspace.MCPAuthenticate(ctx, name); err != nil {
			if isAuthTimeout(err) {
				return dialog.ActionMCPAuthErrored{Name: name, Error: fmt.Errorf("authentication timed out")}
			}
			return dialog.ActionMCPAuthErrored{Name: name, Error: err}
		}
		return dialog.ActionMCPAuthComplete{Name: name}
	}
}

// openMCPAuthDialog opens MCP authentication dialog 如果 any servers
// 是 pending auth. Servers disabled 为 这 repository 或 在
// 配置 是 skipped: disabling 服务端 那 requires auth must 不
// keep asking 为 authentication. If dialog 是 already open, it 是
// brought 到 front instead.
func (m *UI) openMCPAuthDialog() tea.Cmd {
	pending := m.filterAuthPending(m.com.Workspace.MCPPendingAuth())
	if len(pending) == 0 {
		return nil
	}
	if m.dialog.ContainsDialog(dialog.MCPAuthID) {
		m.dialog.BringToFront(dialog.MCPAuthID)
		return nil
	}
	dlg, cmd := dialog.NewMCPAuth(m.com, pending, m.com.Workspace.MCPAuthURL)
	m.dialog.OpenDialog(dlg)
	return cmd
}

// filterAuthPending drops servers 用户 disabled 从 auth
// 提示词. 一个 读取 failure fails open: better 到 提示词 为 live 服务端
// than 到 silently hide one.
func (m *UI) filterAuthPending(pending []mcp.PendingAuthServer) []mcp.PendingAuthServer {
	if len(pending) == 0 {
		return pending
	}
	disabled, err := m.com.Workspace.MCPServersDisabled(context.Background())
	if err != nil {
		return pending
	}
	// Enabled overrides turn 配置-disabled servers back 在 为 这
	// repository, 所以 those still need authentication.
	enabled, err := m.com.Workspace.MCPServersEnabled(context.Background())
	if err != nil {
		return pending
	}
	off := make(map[string]struct{}, len(disabled))
	for _, name := range disabled {
		off[name] = struct{}{}
	}
	for _, name := range enabled {
		delete(off, name)
	}
	filtered := make([]mcp.PendingAuthServer, 0, len(pending))
	for _, server := range pending {
		if _, disabled := off[server.Name]; disabled {
			continue
		}
		filtered = append(filtered, server)
	}
	return filtered
}
