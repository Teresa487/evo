// Package agenttest provides 测试-只 constructors 为 wiring real
// production agent.Coordinator without booting full app.App. 它 是
// imported 只 从 _test.go files (e.g. internal/backend integration
// tests) 和 是 never referenced 通过 production code, 所以 it 是 compiled
// 只 under tests 和 never ships 在 production 二进制 或 API.
package agenttest

import (
	"context"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
)

// NewCoordinator builds a real agent.Coordinator through the production
// agent.NewCoordinator constructor 所以 RunAccepted / BeginAccepted /
// 运行 路径 (including UpdateModels) 是 actual code under 测试.
//
// 它 installs minimal 配置 用 single openai-compatible 提供方
// whose 模型 resolves offline. 运行 rebuilds 模型 在 every 调用, 所以
// 提供方 must construct without network I/O; 取消-在-条目
// 路径 这 helper 是 built 到 exercise returns before any 模型 调用,
// 所以 no 请求 是 ever issued. coder agent's allowed-tools list 是
// cleared 到 keep 工具 construction cheap 和 free 的 子代理 wiring.
//
// The optional coordinator dependencies (history, filetracker, LSP,
// notify, runComplete, skills) 是 nil: 运行 guards publisher fields
// 和 取消-在-条目 路径 never touches others.
func NewCoordinator(
	ctx context.Context,
	workingDir string,
	sessions session.Service,
	messages message.Service,
) (agent.Coordinator, error) {
	cfg, err := config.Init(ctx, workingDir, "", false)
	if err != nil {
		return nil, err
	}

	const (
		providerID = "test-openai-compat"
		modelID    = "test-model"
	)
	cfg.Config().Providers.Set(providerID, config.ProviderConfig{
		ID:      providerID,
		Name:    "Test",
		Type:    catwalk.TypeCompletions,
		BaseURL: "http://127.0.0.1:0/v1",
		APIKey:  "test",
		Models:  []catwalk.Model{{ID: modelID, DefaultMaxTokens: 4096}},
	})
	selected := config.SelectedModel{Provider: providerID, Model: modelID}
	cfg.OverridePreferredModel(config.SelectedModelTypeLarge, selected)
	cfg.OverridePreferredModel(config.SelectedModelTypeSmall, selected)
	cfg.SetupAgents()

	// Keep buildTools light: no 子代理 或 agentic-fetch construction.
	coderCfg := cfg.Config().Agents[config.AgentCoder]
	coderCfg.AllowedTools = nil
	cfg.Config().Agents[config.AgentCoder] = coderCfg

	return agent.NewCoordinator(ctx, agent.CoordinatorOptions{
		Config:      cfg,
		Sessions:    sessions,
		Messages:    messages,
		Permissions: permission.NewPermissionService(workingDir, nil, nil),
	})
}
