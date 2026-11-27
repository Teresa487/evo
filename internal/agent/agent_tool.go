// agent_tool.go —— 传统的 agent 工具实现（非并行派发版本）。
//
// 负责把 agent 调用的配置转换为子 agent 的 agent.Config，并判断只读访问。

package agent

import (
	"context"
	_ "embed"
	"errors"
	"slices"

	"charm.land/fantasy"
	"golang.org/x/sync/errgroup"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed templates/agent_tool.md
var agentToolDescription string

type AgentParams struct {
	Prompt string `json:"prompt" description:"The task for the agent to perform"`
	Access string `json:"access,omitempty" description:"\"read\" (default) for a subagent that can only inspect the codebase, or \"write\" for one that can also edit files"`
}

const (
	AgentToolName = "agent"

	accessRead  = "read"
	accessWrite = "write"
)

// writeAgentConfig derives 读取-写入 variant 的 task agent: same
// agent, given the coder's tools. Subagents never get the delegation tools;
// effectiveToolNames strips them.
func writeAgentConfig(cfg *config.Config) config.Agent {
	task := cfg.Agents[config.AgentTask]
	if coder, ok := cfg.Agents[config.AgentCoder]; ok {
		task.AllowedTools = slices.Clone(coder.AllowedTools)
	}
	return task
}

// accessOrRead defaults unset access level 到 读取-只.
func accessOrRead(access string) string {
	if access == "" {
		return accessRead
	}
	return access
}

// readOnlyParent reports whether parent agent may 只 delegate 到
// 读取-只 subagents. Plan mode promises it cannot change files, 和 那
// promise has 到 cover work it hands off 作为 well 作为 work it does
// itself.
func readOnlyParent(parentAgentID string) bool {
	return parentAgentID == config.AgentPlan
}

// resolveSubAgent picks 子代理 variant 为 requested access level,
// returning 消息 为 模型 当 there 是 no such variant.
func resolveSubAgent(agents map[string]SessionAgent, access string, readOnly bool) (SessionAgent, string) {
	access = accessOrRead(access)
	if agent, ok := agents[access]; ok {
		return agent, ""
	}
	if readOnly && access == accessWrite {
		return nil, `plan mode subagents are read-only; omit access or pass "read"`
	}
	return nil, `access must be "read" or "write"`
}

// taskAgents builds variants 的 task 子代理. Both synchronous
// agent 工具 和 detached 派发 工具 draw 从 these. 一个 读取-只
// parent gets 只 读取 variant, 所以 写入 one 是 never built rather
// than built 和 withheld.
func (c *coordinator) taskAgents(ctx context.Context, readOnly bool) (map[string]SessionAgent, error) {
	if _, ok := c.cfg.Config().Agents[config.AgentTask]; !ok {
		return nil, errors.New("task agent not configured")
	}
	accesses := []string{accessRead, accessWrite}
	if readOnly {
		accesses = accesses[:1]
	}

	// variants share readiness group 的 their own 和 是 fully set
	// up before 这 returns. 这 runs 在 every turn, concurrently 用
	// other runs waiting 在 coordinator's group, 所以 joining 那 group
	// would race 它的 Wait.
	var ready errgroup.Group
	agents := map[string]SessionAgent{}
	for _, access := range accesses {
		agentCfg, canWrite := c.taskAgentConfig(access)
		agent, err := c.buildTaskAgent(ctx, &ready, agentCfg, canWrite, nil)
		if err != nil {
			return nil, err
		}
		agents[access] = agent
	}
	if err := ready.Wait(); err != nil {
		return nil, err
	}
	return agents, nil
}

// taskAgentConfig returns task agent's 配置 为 access level, 和
// whether it may 写入.
func (c *coordinator) taskAgentConfig(access string) (config.Agent, bool) {
	if access == accessWrite {
		return writeAgentConfig(c.cfg.Config()), true
	}
	return c.cfg.Config().Agents[config.AgentTask], false
}

// buildTaskAgent schedules one variant 的 task 子代理 into ready;
// caller waits 在 group, 所以 several variants can build concurrently.
// modelsFn, 当 set, replaces shared 子代理 模型 set; 派发
// 用 模型 override uses it 到 运行 variant 在 overridden 模型.
func (c *coordinator) buildTaskAgent(ctx context.Context, ready *errgroup.Group, agentCfg config.Agent, canWrite bool, modelsFn func() ModelSet) (SessionAgent, error) {
	pr, err := c.newPrompt("task",
		prompt.WithWorkingDir(c.cfg.WorkingDir()),
		prompt.WithCanWrite(canWrite),
	)
	if err != nil {
		return nil, err
	}
	return c.buildAgentWithModels(ctx, ready, pr, agentCfg, true, modelsFn)
}

func (c *coordinator) agentTool(ctx context.Context, parentAgentID string) (fantasy.AgentTool, error) {
	readOnly := readOnlyParent(parentAgentID)
	agents, err := c.taskAgents(ctx, readOnly)
	if err != nil {
		return nil, err
	}
	return fantasy.NewParallelAgentTool(
		AgentToolName,
		agentToolDescription,
		func(ctx context.Context, params AgentParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}

			agent, refusal := resolveSubAgent(agents, params.Access, readOnly)
			if refusal != "" {
				return fantasy.NewTextErrorResponse(refusal), nil
			}

			sessionID := tools.GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}

			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.ToolResponse{}, errors.New("agent message id missing from context")
			}

			return c.runSubAgentMaybeBackground(ctx, AgentToolName, params.Access, subAgentParams{
				Agent:          agent,
				SessionID:      sessionID,
				AgentMessageID: agentMessageID,
				ToolCallID:     call.ID,
				Prompt:         params.Prompt,
				SessionTitle:   "New Agent Session",
			})
		},
	), nil
}
