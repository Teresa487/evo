package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// newGateTestCoordinator builds a minimal coordinator against a hermetic
// 配置: one openai-typed 提供方 pointed 在 closed port, 用 large 和
// small models selected 所以 模型 resolution 和 system-提示词 build both
// succeed without any network access.
func newGateTestCoordinator(t *testing.T, interactive bool) *coordinator {
	t.Helper()

	env := testEnv(t)

	crushJSON := `{
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true},
  "providers": {"mock": {"id": "mock", "name": "Mock", "type": "openai",
    "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
    "models": [{"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128}]}},
  "models": {"large": {"provider": "mock", "model": "mock-model"},
             "small": {"provider": "mock", "model": "mock-model"}}
}`
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "crush.json"), []byte(crushJSON), 0o644))

	cfg, err := config.Init(t.Context(), env.workingDir, "", false)
	require.NoError(t, err)
	cfg.SetupAgents()

	coord := &coordinator{
		cfg:         cfg,
		sessions:    env.sessions,
		messages:    env.messages,
		permissions: env.permissions,
		history:     env.history,
		filetracker: *env.filetracker,
		agents:      make(map[string]SessionAgent),
		interactive: interactive,
	}

	p, err := loadPrompt(cfg, "coder", prompt.WithWorkingDir(env.workingDir))
	require.NoError(t, err)
	agentCfg := cfg.Config().Agents[config.AgentCoder]

	agent, err := coord.buildAgent(context.Background(), &coord.readyWg, p, agentCfg, false)
	require.NoError(t, err)
	coord.mainAgent = agent
	coord.mainAgentName = config.AgentCoder
	coord.agents[config.AgentCoder] = agent

	return coord
}

// TestRunWaitsForMCPOnlyWhenNonInteractive pins split behavior 为
// 在-flight MCP initialization.
//
// MCP servers connect asynchronously. Interactive runs must 不 wait 为 them:
// blocking send 路径 meant slow stdio 服务端 (e.g. Python via uv) froze
// TUI 为 length 的 它的 connect 超时, most visibly 在 first
// 消息 的 会话. Tools 从 late servers simply miss 那 运行's palette
// 和 show up 在 next one.
//
// Non-interactive runs (`crush 运行`, both local 和 客户端/服务端) get single
// shot 在 palette, 所以 they still wait 为 initialization 到 settle.
func TestRunWaitsForMCPOnlyWhenNonInteractive(t *testing.T) {
	t.Run("non-interactive waits", func(t *testing.T) {
		coord := newGateTestCoordinator(t, false)

		// Arm gate 和 never 完成 initialization, standing 在 为
		// MCP 服务端 那 是 still connecting.
		mcp.ArmInit()
		t.Cleanup(mcp.DisarmInit)

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		_, err := coord.run(ctx, nil, nil, "test-session", "hello")
		require.ErrorContains(t, err, "MCP initialization",
			"non-interactive run must block on MCP initialization")
	})

	t.Run("interactive does not wait", func(t *testing.T) {
		coord := newGateTestCoordinator(t, true)

		mcp.ArmInit()
		t.Cleanup(mcp.DisarmInit)

		done := make(chan error, 1)
		go func() {
			_, err := coord.run(context.Background(), nil, nil, "test-session", "hello")
			done <- err
		}()

		select {
		case err := <-done:
			// 运行 fails 为 unrelated reasons (no such 会话, closed
			// 提供方 port); all 那 matters 是 那 it got past gate
			// instead 的 parking 在 MCP initialization.
			if err != nil {
				require.NotContains(t, err.Error(), "MCP initialization",
					"interactive run must not block on MCP initialization")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("interactive run blocked; it must not wait for MCP initialization")
		}
	})
}

func TestRefreshSkillPromptRebuildsOnlyAfterToggle(t *testing.T) {
	t.Parallel()

	coord := newGateTestCoordinator(t, true)
	require.NoError(t, coord.readyWg.Wait())

	p, err := loadPrompt(coord.cfg, "coder", coord.skillOverrideOptions()...)
	require.NoError(t, err)
	coord.prompts = map[string]*prompt.Prompt{config.AgentCoder: p}
	coord.skillKeys = map[string]string{config.AgentCoder: coord.skillToggleKey(t.Context())}

	agent := coord.mainAgent.(*sessionAgent)
	agent.SetSystemPrompt("stale")

	require.NoError(t, coord.refreshSkillPrompt(t.Context(), agent, config.AgentCoder))
	require.Equal(t, "stale", agent.systemPrompt.Get(), "unchanged toggles must keep the prompt")

	require.NoError(t, coord.sessions.SetSkillDisabled(t.Context(), "crush-config", true, false))
	require.NoError(t, coord.refreshSkillPrompt(t.Context(), agent, config.AgentCoder))
	require.NotEqual(t, "stale", agent.systemPrompt.Get(), "a toggle must rebuild the prompt")
	require.NotContains(t, agent.systemPrompt.Get(), "<name>crush-config</name>")
}
