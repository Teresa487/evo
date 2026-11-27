package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestChannelContextIsScopedToTurn(t *testing.T) {
	t.Parallel()
	channelContext := WithChannel(context.Background(), "signal")
	require.Equal(t, "signal", ChannelFromContext(channelContext))
	require.Empty(t, ChannelFromContext(context.Background()))
}

func TestFilterToolsForChannel(t *testing.T) {
	t.Parallel()
	channelTool := &channelTestTool{name: "send", mcpName: "signal"}
	plainTool := &fakeTool{name: "plain"}
	states := map[string]mcp.ClientInfo{"signal": {Channel: true}}

	// 一个 local turn (no 渠道) keeps all tools, including 渠道 服务端
	// tools — TUI 用户 can still ask agent 到 send via Signal.
	local := filterToolsForChannel([]fantasy.AgentTool{channelTool, plainTool}, "", states)
	require.Len(t, local, 2)

	// 一个 渠道-originated turn 只 sees originating 渠道's tools
	// plus non-渠道 tools.
	matching := filterToolsForChannel([]fantasy.AgentTool{channelTool, plainTool}, "signal", states)
	require.Len(t, matching, 2)

	// 一个 different 渠道's turn does 不 see signal's tools.
	other := filterToolsForChannel([]fantasy.AgentTool{channelTool, plainTool}, "switchboard", states)
	require.Len(t, other, 1)
	require.Equal(t, "plain", other[0].Info().Name)
}

type channelTestTool struct {
	name    string
	mcpName string
}

func (t *channelTestTool) MCP() string {
	return t.mcpName
}

func (t *channelTestTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: t.name}
}

func (*channelTestTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.NewTextResponse("ok"), nil
}

func (*channelTestTool) ProviderOptions() fantasy.ProviderOptions {
	return nil
}

func (*channelTestTool) SetProviderOptions(fantasy.ProviderOptions) {}

// TestSyncSessionChannelLifecycle covers full lifecycle 的 persisted
// 会话-渠道 binding: 渠道 turn binds 会话, push 从
// different 渠道 rebinds it (newest wins), 和 local turn clears it —
// reaping 路径 为 sessions 用户 takes over.
func TestSyncSessionChannelLifecycle(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	// Release 只 这 测试's pooled 连接; global db.ResetPool()
	// here would close databases 的 tests still running 在 parallel.
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	sessions := session.NewService(db.New(conn), conn)
	c := &coordinator{sessions: sessions}

	created, err := sessions.Create(t.Context(), "channel session")
	require.NoError(t, err)

	// 一个 渠道-originated turn binds 会话 到 它的 渠道.
	c.syncSessionChannel(t.Context(), created.ID, "signal")
	got, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "signal", got.Channel)

	// 一个 push 从 another 渠道 rebinds: newest push wins.
	c.syncSessionChannel(t.Context(), created.ID, "switchboard")
	got, err = sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "switchboard", got.Channel)

	// 一个 local turn (no originating 渠道) clears binding.
	c.syncSessionChannel(t.Context(), created.ID, "")
	got, err = sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Empty(t, got.Channel)

	// 一个 missing 会话 是 no-op, 不 错误.
	c.syncSessionChannel(t.Context(), "does-not-exist", "signal")
}
