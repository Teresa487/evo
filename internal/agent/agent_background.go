package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/tools"
)

// subAgentBackgroundAfter 是 how long blocking 子代理 工具调用 runs 在
// foreground before it 是 moved 到 background, mirroring bash
// 工具's auto-background threshold.
const subAgentBackgroundAfter = 60 * time.Second

// AgentBackgroundMetadata marks blocking 子代理 工具结果 作为 handed
// off 到 background. parent transcript carries it 所以 子代理
// can 是 replayed 作为 running 派发: shown 通过 agent_status, reconciled
// after restart, 和 reached 通过 agent_send 和 agent_stop.
type AgentBackgroundMetadata struct {
	Label        string `json:"label"`
	Backgrounded bool   `json:"backgrounded"`
	Access       string `json:"access,omitempty"`
	Task         string `json:"task,omitempty"`
}

// backgroundLabel labels auto-backgrounded 子代理 after 它的 工具调用.
func backgroundLabel(toolName, callID string) string {
	prefix := "agent"
	if toolName == tools.AgenticFetchToolName {
		prefix = "search"
	}
	id := callID
	if i := strings.LastIndexAny(id, "_-"); i >= 0 && i+1 < len(id) {
		id = id[i+1:]
	}
	if len(id) > 6 {
		id = id[len(id)-6:]
	}
	if id == "" {
		id = fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	}
	return prefix + "-" + id
}

// runSubAgentMaybeBackground runs blocking 子代理 工具调用, 但 moves it
// 到 background 如果 it outruns threshold. 一个 headless 运行 has no later
// turn 到 投递 报告 into, 所以 there 调用 always blocks, exactly 作为
// before. In interactive 运行 long 调用 是 handed 到 派发
// registry: 工具 returns notice, 结果 arrives later 作为 报告,
// 和 agent_status, agent_send, 和 agent_stop can reach it meanwhile.
func (c *coordinator) runSubAgentMaybeBackground(
	ctx context.Context,
	toolName, access string,
	params subAgentParams,
) (fantasy.ToolResponse, error) {
	if !c.interactive {
		return c.runSubAgent(ctx, params)
	}
	access = accessOrRead(access)

	label := c.names.name(backgroundLabel(toolName, params.ToolCallID))
	childSessionID := c.sessions.CreateAgentToolSessionID(params.AgentMessageID, params.ToolCallID)
	entry := newDispatchedAgent(label, access, childSessionID, params.SessionID, params.Agent)

	// background goroutine rolls child's cost up itself, 作为
	// dispatched 子代理 does, 所以 synchronous rollup 是 turned off.
	params.SkipCostRollup = true

	key := dispatchKey(params.SessionID, label)
	c.dispatchMu.Lock()
	c.dispatched.Set(key, entry)
	runCtx, gen, _ := entry.claim()
	done := entry.runInline(gen)
	c.dispatchMu.Unlock()

	c.runDispatched(runCtx, entry, gen, func(ctx context.Context) (fantasy.ToolResponse, error) {
		return c.runSubAgent(ctx, params)
	})

	// 一个 调用 那 finishes 在 foreground leaves nothing 在 registry.
	takeInline := func() (fantasy.ToolResponse, error) {
		c.dispatchMu.Lock()
		if current, ok := c.dispatched.Get(key); ok && current == entry {
			c.dispatched.Del(key)
		}
		c.dispatchMu.Unlock()
		return entry.result()
	}

	timer := time.NewTimer(c.backgroundAfter)
	defer timer.Stop()

	select {
	case <-done:
		return takeInline()
	case <-timer.C:
		if !entry.detach(gen) {
			return takeInline()
		}
		return backgroundNotice(label, access, params.Prompt, c.backgroundAfter), nil
	case <-ctx.Done():
		// Cancelled before handoff: stop it 和 surface
		// cancellation, 作为 blocking 调用 would; no 报告 是 written.
		entry.stop("the parent turn was cancelled")
		<-done
		takeInline()
		return fantasy.ToolResponse{}, ctx.Err()
	}
}

// backgroundNotice 是 what 模型 gets 当 子代理 是 moved 到
// background. 它 names label 所以 模型 can follow up 在 it.
func backgroundNotice(label, access, prompt string, after time.Duration) fantasy.ToolResponse {
	notice := fmt.Sprintf(
		"Still working after %s, so this subagent moved to the background as %q. "+
			"Its report arrives under that label when it finishes and wakes you, so carry on "+
			"with anything that does not need it, then end your turn. Do not sleep or check "+
			"%s to wait for it. Steer it with %s, or stop it with %s.",
		after, label, AgentStatusToolName, AgentSendToolName, AgentStopToolName,
	)
	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(notice),
		AgentBackgroundMetadata{
			Label:        label,
			Backgrounded: true,
			Access:       access,
			Task:         prompt,
		},
	)
}
