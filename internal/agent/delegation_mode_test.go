package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEffectiveToolNamesByRunMode pins how 运行 mode picks delegation
// style. 一个 TUI can receive 子代理's 报告 作为 later turn, 所以
// delegation detaches; headless 运行 exits before one could arrive, 所以
// delegation blocks instead.
func TestEffectiveToolNamesByRunMode(t *testing.T) {
	t.Parallel()

	agentCfg := config.Agent{
		AllowedTools: []string{
			AgentToolName,
			AgentDispatchToolName,
			AgentSendToolName,
			"question",
			"view",
		},
	}

	tests := []struct {
		name        string
		interactive bool
		isSubAgent  bool
		want        []string
	}{
		{
			name:        "interactive delegates detached",
			interactive: true,
			want:        []string{AgentDispatchToolName, AgentSendToolName, "question", "view"},
		},
		{
			name:        "headless delegates blocking",
			interactive: false,
			want:        []string{AgentToolName, "view"},
		},
		{
			name:        "subagents neither fan out nor ask",
			interactive: true,
			isSubAgent:  true,
			want:        []string{"view"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &coordinator{interactive: tt.interactive}
			assert.Equal(t, tt.want, c.effectiveToolNames(agentCfg, tt.isSubAgent))
		})
	}
}

// TestEffectiveToolNamesKeepsConfigAuthoritative checks gate 只 ever
// narrows: 工具 用户 disabled cannot reappear because 的 运行 mode.
func TestEffectiveToolNamesKeepsConfigAuthoritative(t *testing.T) {
	t.Parallel()

	agentCfg := config.Agent{AllowedTools: []string{"view"}}
	for _, interactive := range []bool{true, false} {
		c := &coordinator{interactive: interactive}
		assert.Equal(t, []string{"view"}, c.effectiveToolNames(agentCfg, false))
	}
}

// TestResolveSubAgentAccess covers how a requested access level maps onto a
// 子代理 variant, including plan mode, which may 只 delegate work it
// could have done itself.
func TestResolveSubAgentAccess(t *testing.T) {
	t.Parallel()

	read := &mockSessionAgent{}
	write := &mockSessionAgent{}
	both := map[string]SessionAgent{accessRead: read, accessWrite: write}
	readOnly := map[string]SessionAgent{accessRead: read}

	t.Run("empty access defaults to read", func(t *testing.T) {
		t.Parallel()
		agent, refusal := resolveSubAgent(both, "", false)
		require.Empty(t, refusal)
		assert.Same(t, read, agent)
	})

	t.Run("write is available to a writing parent", func(t *testing.T) {
		t.Parallel()
		agent, refusal := resolveSubAgent(both, accessWrite, false)
		require.Empty(t, refusal)
		assert.Same(t, write, agent)
	})

	t.Run("plan mode refuses write with a reason", func(t *testing.T) {
		t.Parallel()
		agent, refusal := resolveSubAgent(readOnly, accessWrite, true)
		assert.Nil(t, agent)
		assert.Contains(t, refusal, "read-only")
	})

	t.Run("plan mode still delegates reads", func(t *testing.T) {
		t.Parallel()
		agent, refusal := resolveSubAgent(readOnly, accessRead, true)
		require.Empty(t, refusal)
		assert.Same(t, read, agent)
	})

	t.Run("nonsense access is rejected", func(t *testing.T) {
		t.Parallel()
		agent, refusal := resolveSubAgent(both, "admin", false)
		assert.Nil(t, agent)
		assert.Contains(t, refusal, `"read" or "write"`)
	})
}

// TestReadOnlyParent documents which parents may 只 launch 读取-只
// subagents.
func TestReadOnlyParent(t *testing.T) {
	t.Parallel()

	assert.True(t, readOnlyParent(config.AgentPlan))
	assert.False(t, readOnlyParent(config.AgentCoder))
	assert.False(t, readOnlyParent(""))
}
