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

// TestBuildAgentReadinessSurvivesCallerCancellation 是 regression 测试 为
// EVO_CLIENT_SERVER=1 "new 会话 hangs" bug.
//
// buildAgent starts readiness goroutines 那 build 系统提示词 和
// initial 工具 list. Several 服务端 条目 points build agent 从
// short-lived HTTP 请求 上下文 — InitAgent/UpdateAgent handlers, 和
// 子代理 build reached through UpdateModels -> buildTools -> agentTool.
// When 那 请求 上下文 was canceled moment 处理器 returned,
// readyWg errgroup recorded 上下文.Canceled 和 every later coordinator.运行
// failed 在 readyWg.Wait() before emitting anything — 会话 hung 用
// no visible LLM 响应. (这 was made worse while 工具-list goroutine
// also blocked 在 mcp.WaitForInit, which kept it parked long enough 到
// observe cancellation; readiness work no longer waits 在 MCP init —
// see coordinator.运行 — 但 cancellation detachment still matters.)
//
// fix detaches readiness work 从 caller 上下文 via
// 上下文.WithoutCancel, 所以 canceling 上下文 那 triggered build no
// longer poisons readyWg. Here we build agent 用 cancelable 上下文,
// 取消 it, 和 require 那 readyWg still completes cleanly.
func TestBuildAgentReadinessSurvivesCallerCancellation(t *testing.T) {
	env := testEnv(t)

	// Minimal hermetic 配置: one openai-typed 提供方 用 selected large
	// 和 small models 所以 buildAgentModels 和 system-提示词 build both
	// succeed. No MCP servers 是 configured, 所以 initialization would 完成
	// instantly 如果 we let it — we arm gate anyway 到 prove readiness
	// goroutines no longer block 在 it.
	evoJSON := `{
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true},
  "providers": {"mock": {"id": "mock", "name": "Mock", "type": "openai",
    "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
    "models": [{"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128}]}},
  "models": {"large": {"provider": "mock", "model": "mock-model"},
             "small": {"provider": "mock", "model": "mock-model"}}
}`
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "evo.json"), []byte(evoJSON), 0o644))

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
	}

	// Arm MCP init gate. We never 完成 init; readiness goroutines
	// must 不 care, since they build 工具 list 从 registry 作为 it
	// stands rather than waiting 为 initialization 到 finish.
	mcp.ArmInit()
	t.Cleanup(mcp.DisarmInit)

	p, err := loadPrompt(cfg, "coder", prompt.WithWorkingDir(env.workingDir))
	require.NoError(t, err)
	agentCfg := cfg.Config().Agents[config.AgentCoder]

	ctx, cancel := context.WithCancel(context.Background())
	_, err = coord.buildAgent(ctx, &coord.readyWg, p, agentCfg, false)
	require.NoError(t, err)

	// caller goes away, mirroring HTTP 处理器 returning 和 canceling
	// 它的 请求 上下文.
	cancel()

	done := make(chan error, 1)
	go func() { done <- coord.readyWg.Wait() }()

	select {
	case err := <-done:
		// 上下文.Canceled 是 regression: caller's cancellation
		// leaked into readiness work 和 poisoned errgroup.
		require.NotErrorIs(t, err, context.Canceled,
			"readyWg was poisoned by caller cancellation (client/server new-session hang regression)")
		require.NoError(t, err, "unexpected buildAgent readiness error")
	case <-time.After(2 * time.Second):
		t.Fatal("readyWg did not complete; the readiness goroutines must not block on MCP init")
	}
}

// TestTaskAgentsDoNotJoinCoordinatorReadiness 是 regression 测试 为
// "sync: WaitGroup 是 reused before previous Wait has returned" panic.
//
// Every turn rebuilds task subagents (运行 -> updateAgentModels ->
// buildTools -> taskAgents). Those builds used 到 join coordinator's
// readyWg while another 运行, such 作为 子代理 报告 being delivered,
// was blocked 在 readyWg.Wait. Adding work 到 WaitGroup 用 Wait 在
// flight panics. subagents now use group 的 their own, 和 是 fully
// set up 通过 time taskAgents returns.
func TestTaskAgentsDoNotJoinCoordinatorReadiness(t *testing.T) {
	env := testEnv(t)

	evoJSON := `{
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true},
  "providers": {"mock": {"id": "mock", "name": "Mock", "type": "openai",
    "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
    "models": [{"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128}]}},
  "models": {"large": {"provider": "mock", "model": "mock-model"},
             "small": {"provider": "mock", "model": "mock-model"}}
}`
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "evo.json"), []byte(evoJSON), 0o644))

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
	}

	stop := make(chan struct{})
	waiters := make(chan error, 4)
	for range 4 {
		go func() {
			for {
				select {
				case <-stop:
					waiters <- nil
					return
				default:
				}
				if err := coord.readyWg.Wait(); err != nil {
					waiters <- err
					return
				}
			}
		}()
	}

	for range 20 {
		agents, err := coord.taskAgents(context.Background(), false)
		require.NoError(t, err)
		for access, agent := range agents {
			sa, ok := agent.(*sessionAgent)
			require.True(t, ok)
			require.NotEmpty(t, sa.systemPrompt.Get(), "%s subagent returned before its system prompt was set", access)
			require.NotZero(t, sa.tools.Len(), "%s subagent returned before its tools were set", access)
		}
	}

	close(stop)
	for range 4 {
		require.NoError(t, <-waiters)
	}
}
