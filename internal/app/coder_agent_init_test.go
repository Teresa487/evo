package app

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// initFakeCoordinator 是 minimal agent.Coordinator stand-在 为
// exercising initCoderAgent's 创建-只 semantics without full
// coordinator machinery.
type initFakeCoordinator struct {
	busy        bool
	cancelCalls int
}

func (c *initFakeCoordinator) SetMainAgent(string) error { return nil }
func (c *initFakeCoordinator) MainAgent() string         { return "" }
func (c *initFakeCoordinator) Run(context.Context, string, string, ...message.Attachment) (*fantasy.AgentResult, error) {
	return nil, nil
}

func (c *initFakeCoordinator) RunAccepted(context.Context, *agent.AcceptedRun, string, string, ...message.Attachment) (*fantasy.AgentResult, error) {
	return nil, nil
}
func (c *initFakeCoordinator) BeginAccepted(string) *agent.AcceptedRun { return nil }
func (c *initFakeCoordinator) Cancel(string)                           {}
func (c *initFakeCoordinator) CancelAll()                              { c.cancelCalls++ }
func (c *initFakeCoordinator) IsSessionBusy(string) bool               { return false }
func (c *initFakeCoordinator) IsBusy() bool                            { return c.busy }
func (c *initFakeCoordinator) QueuedPrompts(string) int                { return 0 }
func (c *initFakeCoordinator) QueuedPromptsList(string) []string       { return nil }
func (c *initFakeCoordinator) ClearQueue(string)                       {}
func (c *initFakeCoordinator) Summarize(context.Context, string) error { return nil }
func (c *initFakeCoordinator) Model() agent.Model                      { return agent.Model{} }
func (c *initFakeCoordinator) UpdateModels(context.Context) error      { return nil }
func (c *initFakeCoordinator) GenerateTitle(context.Context, string, string) {
}

func (c *initFakeCoordinator) ListSettledMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}
func (c *initFakeCoordinator) AddNote(string, string)           {}
func (c *initFakeCoordinator) SetHostContext(proto.HostContext) {}
func (c *initFakeCoordinator) HostContext() proto.HostContext   { return proto.HostContext{} }

// TestInitCoderAgentCreateOnly: once a coordinator exists, a second
// initialization (headless 或 interactive) 是 no-op, 所以 渠道 push
// initializing headless 工作区 cannot race 客户端 attach into
// building a second coordinator.
func TestInitCoderAgentCreateOnly(t *testing.T) {
	t.Parallel()

	existing := &initFakeCoordinator{}
	a := &App{AgentCoordinator: existing, agentInteractive: true}

	require.NoError(t, a.initCoderAgent(context.Background(), false))
	require.NoError(t, a.initCoderAgent(context.Background(), true))
	require.Same(t, existing, a.AgentCoordinator)
	require.Zero(t, existing.cancelCalls)
}

// TestInitCoderAgentInteractiveUpgradeKeepsBusyCoordinator: an
// interactive initializer wants 到 upgrade headless coordinator, 但
// 不 while 运行 是 在 flight 在 it — swapping then would strand
// 运行, 所以 existing coordinator 是 kept.
func TestInitCoderAgentInteractiveUpgradeKeepsBusyCoordinator(t *testing.T) {
	t.Parallel()

	existing := &initFakeCoordinator{busy: true}
	a := &App{AgentCoordinator: existing, agentInteractive: false}

	require.NoError(t, a.initCoderAgent(context.Background(), true))
	require.Same(t, existing, a.AgentCoordinator)
	require.False(t, a.agentInteractive)
	require.Zero(t, existing.cancelCalls)
}

// TestInitCoderAgentUpgradeCancelsSupersededCoordinator: 交互式
// upgrade 的 空闲 headless coordinator cancels old one before
// swap, 所以 nothing it still has 在 flight keeps running through
// coordinator app no longer references. 配置 here has no
// coder agent, 所以 initCoderAgent stops 用 错误 right after
// 取消 — assertion 是 那 superseded coordinator was shut
// down before any 的 那.
func TestInitCoderAgentUpgradeCancelsSupersededCoordinator(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfgStore, err := config.Init(t.Context(), t.TempDir(), "", false)
	require.NoError(t, err)
	delete(cfgStore.Config().Agents, config.AgentCoder)

	existing := &initFakeCoordinator{}
	a := &App{AgentCoordinator: existing, agentInteractive: false, config: cfgStore}

	require.Error(t, a.initCoderAgent(context.Background(), true))
	require.Equal(t, 1, existing.cancelCalls)
	require.Same(t, existing, a.AgentCoordinator)
}
