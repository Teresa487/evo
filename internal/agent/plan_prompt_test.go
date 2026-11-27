package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// TestPlanPromptListsConfiguredTools verifies plan 系统提示词
// advertises tools plan agent 是 actually given. list 是
// generated 从 effective 工具 names rather than hard-coded, 所以
// 提示词 can never promise 工具 用户 disabled 或 运行 mode
// withheld.
func TestPlanPromptListsConfiguredTools(t *testing.T) {
	env := testEnv(t)

	// Minimal hermetic 配置 所以 配置.Init 和 提示词 build both
	// succeed without touching 用户's real 提供方 配置.
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

	// buildPrompt renders plan 提示词 作为 coordinator would 为
	// given 运行 mode, 所以 advertised list goes through same gate
	// 那 decides which tools 是 attached.
	buildPrompt := func(t *testing.T, interactive bool) string {
		t.Helper()
		c := &coordinator{interactive: interactive}
		planCfg := cfg.Config().Agents[config.AgentPlan]
		p, err := loadPrompt(cfg, "plan",
			prompt.WithWorkingDir(env.workingDir),
			prompt.WithToolNames(c.effectiveToolNames(planCfg, false)),
		)
		require.NoError(t, err)
		systemPrompt, err := p.Build(context.Background(), "mock", "mock-model", cfg)
		require.NoError(t, err)
		return systemPrompt
	}

	// Interactive: subagents 报告 back 在 their own, 所以 detached
	// pair 是 advertised 和 blocking 工具 是 不.
	require.Contains(t, buildPrompt(t, true),
		"Your available tools are: agent_dispatch, agent_send, agent_status, agent_stop, lsp_symbols, lsp_definition, lsp_call_hierarchy, glob, grep, ls, question, sourcegraph, view.")

	// Headless: nothing 是 left 到 receive later 报告, 所以 delegation
	// blocks instead, 和 there 是 nobody 到 answer question.
	require.Contains(t, buildPrompt(t, false),
		"Your available tools are: agent, lsp_symbols, lsp_definition, lsp_call_hierarchy, glob, grep, ls, sourcegraph, view.")

	// 一个 工具 用户 disabled disappears 从 advertised list.
	// ( word "question" still appears elsewhere 在 提示词's rules,
	// 所以 assert 在 exact generated tools line instead.)
	cfg.Config().Options.DisabledTools = []string{"question"}
	cfg.SetupAgents()
	systemPrompt := buildPrompt(t, true)
	require.Contains(t, systemPrompt,
		"Your available tools are: agent_dispatch, agent_send, agent_status, agent_stop, lsp_symbols, lsp_definition, lsp_call_hierarchy, glob, grep, ls, sourcegraph, view.")
	require.NotContains(t, systemPrompt, "ls, question,")
}
