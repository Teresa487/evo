package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/tidwall/sjson"
)

// hookedTool wraps fantasy.AgentTool 到 运行 PreToolUse hooks before
// inner 工具 和 PostToolUse 或 PostToolUseFailure hooks after it.
type hookedTool struct {
	inner  fantasy.AgentTool
	runner *hooks.Runner
}

func newHookedTool(inner fantasy.AgentTool, runner *hooks.Runner) *hookedTool {
	return &hookedTool{inner: inner, runner: runner}
}

// wrapToolsWithHooks returns 工具 slice 用 each 条目 wrapped 在
// hookedTool. Returns original slice unchanged 当 runner 是 nil 或
// 当 isSubAgent 是 true — subagents never fire hooks, top-level
// invocation 的 子代理 工具 itself 是 wrapped 在 caller's side.
func wrapToolsWithHooks(tools []fantasy.AgentTool, runner *hooks.Runner, isSubAgent bool) []fantasy.AgentTool {
	if runner == nil || isSubAgent {
		return tools
	}
	out := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		out[i] = newHookedTool(tool, runner)
	}
	return out
}

func (h *hookedTool) Info() fantasy.ToolInfo {
	return h.inner.Info()
}

func (h *hookedTool) ProviderOptions() fantasy.ProviderOptions {
	return h.inner.ProviderOptions()
}

func (h *hookedTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	h.inner.SetProviderOptions(opts)
}

func (h *hookedTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	ev := hooks.Event{
		Name:      hooks.EventPreToolUse,
		SessionID: tools.GetSessionFromContext(ctx),
		Tool:      &hooks.ToolCall{ID: call.ID, Name: call.Name, Input: call.Input},
	}

	pre := h.runner.Run(ctx, ev)
	if pre.Blocked() {
		reason := fmt.Sprintf("Tool call blocked by hook. Reason: %s", pre.Reason)
		if pre.Halt {
			reason = fmt.Sprintf("Turn halted by hook. Reason: %s", pre.Reason)
		}
		resp := fantasy.NewTextErrorResponse(reason)
		// Halt ends whole turn; plain deny 只 blocks 这 工具
		// 调用 所以 模型 can see 错误 和 try something else.
		resp.StopTurn = pre.Halt
		resp.Metadata = mergeHookMetadata("", pre)
		return resp, nil
	}
	if pre.UpdatedInput != "" {
		call.Input = pre.UpdatedInput
		ev.Tool.Input = pre.UpdatedInput
	}
	// An explicit allow 从 钩子 pre-approves 权限 提示词 为
	// 这 工具调用. Silence falls through 到 normal 权限 flow.
	if pre.Decision == hooks.DecisionAllow {
		ctx = permission.WithHookApproval(ctx, call.ID)
	}

	resp, err := h.inner.Run(ctx, call)

	ev = postToolEvent(ev, resp, err)
	post := h.runner.Run(ctx, ev)
	if post.UpdatedOutput != "" && ev.Name == hooks.EventPostToolUse {
		resp.Content = post.UpdatedOutput
	}
	resp.StopTurn = resp.StopTurn || post.Halt
	resp.Metadata = mergeHookMetadata(resp.Metadata, pre, post)
	if err != nil {
		// 一个 Go 错误 aborts 运行, 所以 there 是 no 结果 到 carry
		// feedback 到 模型.
		return resp, err
	}

	// 一个 post 钩子 那 blocks cannot undo 调用, 所以 它的 reason 是
	// handed 到 模型 作为 feedback instead, 作为 在 Claude Code.
	feedback := []string{pre.Context, post.Context}
	if post.Blocked() && post.Reason != "" {
		feedback = append(feedback, "Hook feedback: "+post.Reason)
	}
	resp.Content = joinNonEmpty(resp.Content, feedback...)
	return resp, nil
}

// postToolEvent turns PreToolUse 事件 into one describing how
// 调用 ended. 一个 Go 错误 和 错误 响应 both count 作为
// failure, 作为 在 Claude Code, 所以 PostToolUse 只 ever sees output
// 模型 will treat 作为 success.
func postToolEvent(ev hooks.Event, resp fantasy.ToolResponse, err error) hooks.Event {
	switch {
	case err != nil:
		ev.Name, ev.Error = hooks.EventPostToolUseFailure, err.Error()
	case resp.IsError:
		ev.Name, ev.Error = hooks.EventPostToolUseFailure, resp.Content
	default:
		ev.Name, ev.ToolResponse = hooks.EventPostToolUse, new(resp.Content)
	}
	return ev
}

// joinNonEmpty appends non-empty parts 到 content, one per line.
func joinNonEmpty(content string, parts ...string) string {
	for _, p := range parts {
		if p == "" {
			continue
		}
		if content != "" {
			content += "\n"
		}
		content += p
	}
	return content
}

// mergeHookMetadata injects combined 钩子 metadata 为 given
// results into existing 工具 metadata under "钩子" key. Results
// 用 no hooks 是 skipped; 当 nothing ran, existing 是 returned
// unchanged.
func mergeHookMetadata(existing string, results ...hooks.AggregateResult) string {
	var meta hooks.HookMetadata
	var decision hooks.Decision
	for _, r := range results {
		if r.HookCount == 0 {
			continue
		}
		meta.HookCount += r.HookCount
		meta.Hooks = append(meta.Hooks, r.Hooks...)
		meta.Halt = meta.Halt || r.Halt
		meta.InputRewrite = meta.InputRewrite || r.UpdatedInput != ""
		meta.OutputRewrite = meta.OutputRewrite || r.UpdatedOutput != ""
		// Decisions 是 ordered none < allow < deny, 所以 strongest wins.
		decision = max(decision, r.Decision)
		meta.Reason = strings.TrimSpace(meta.Reason + "\n" + r.Reason)
	}
	if meta.HookCount == 0 {
		return existing
	}
	meta.Decision = decision.String()
	data, err := json.Marshal(meta)
	if err != nil {
		return existing
	}
	if existing == "" {
		existing = "{}"
	}
	merged, err := sjson.SetRaw(existing, "hook", string(data))
	if err != nil {
		return existing
	}
	return merged
}
