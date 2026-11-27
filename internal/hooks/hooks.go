// Package hooks 在钩子事件（如 PreToolUse）触发用户定义的 shell 命令，
// 返回决策来控制 agent 行为。
//
// 从产物反推：工具调用的前后、轮次开始/结束、压缩前后都会经过这里；
// hook 可以放行、拦截、改写工具输入/输出、注入上下文。
package hooks

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/tidwall/sjson"
)

// HaltExitCode 是 exit code 那 halts whole turn. 2 blocks
// current 工具调用; 49 sits 在 no-man's-land between
// generic-错误 range (1-30), sysexits range (64-78), 和
// killed-通过-signal range (128+) 所以 it can't 是 hit 通过 accident.
const HaltExitCode = 49

// HookMetadata 是 embedded 在 工具 响应 metadata 所以 UI can
// display 钩子 indicator.
type HookMetadata struct {
	HookCount     int        `json:"hook_count"`
	Decision      string     `json:"decision"`
	Halt          bool       `json:"halt,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	InputRewrite  bool       `json:"input_rewrite,omitempty"`
	OutputRewrite bool       `json:"output_rewrite,omitempty"`
	Hooks         []HookInfo `json:"hooks,omitempty"`
}

// HookInfo identifies single 钩子 那 ran 和 它的 individual 结果.
type HookInfo struct {
	Name          string `json:"name"`
	Event         string `json:"event,omitempty"`
	Matcher       string `json:"matcher,omitempty"`
	Decision      string `json:"decision"`
	Halt          bool   `json:"halt,omitempty"`
	Reason        string `json:"reason,omitempty"`
	InputRewrite  bool   `json:"input_rewrite,omitempty"`
	OutputRewrite bool   `json:"output_rewrite,omitempty"`
}

// Decision represents outcome 的 single 钩子 execution.
type Decision int

const (
	// DecisionNone means 钩子 expressed no opinion.
	DecisionNone Decision = iota
	// DecisionAllow means 钩子 explicitly allowed action.
	DecisionAllow
	// DecisionDeny means 钩子 blocked action.
	DecisionDeny
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	default:
		return "none"
	}
}

// HookResult holds parsed output 的 single 钩子 execution.
type HookResult struct {
	Decision      Decision
	Halt          bool   // If true, halt the whole turn.
	Reason        string // Deny or halt reason (same field, different audience).
	Context       string
	UpdatedInput  string // Shallow-merge patch against tool_input (opaque JSON).
	UpdatedOutput string // Replacement for the tool's output (PostToolUse).
}

// AggregateResult holds combined outcome 的 all hooks 为 事件.
type AggregateResult struct {
	Decision      Decision
	Halt          bool       // Any hook requested halt.
	HookCount     int        // Number of hooks that ran.
	Hooks         []HookInfo // Info about each hook that ran (config order).
	Reason        string     // Concatenated deny/halt reasons (newline-separated).
	Context       string     // Concatenated context from all hooks.
	UpdatedInput  string     // Merged tool_input JSON (empty if no patches).
	UpdatedOutput string     // Replacement tool output; the last hook to set it wins.
}

// Blocked reports whether any 钩子 denied action 或 halted turn.
func (a AggregateResult) Blocked() bool {
	return a.Decision == DecisionDeny || a.Halt
}

// aggregate 把多个 HookResult 合并成一个 AggregateResult。
// Results 是 processed 在 配置 order ( order 的 slice). Deny
// wins over allow, allow wins over none. Halt 是 sticky. Reasons 和
// 上下文 concatenate 在 order. updated_input patches shallow-merge 在
// order against original 工具 input; updated_output 是 whole
// replacement, 所以 last 钩子 到 set it wins.
func aggregate(results []HookResult, origToolInput string) AggregateResult {
	var (
		decision Decision
		halt     bool
		reasons  []string
		contexts []string
		merged   = origToolInput
		anyPatch = false
		output   string
	)
	for _, r := range results {
		switch r.Decision {
		case DecisionDeny:
			decision = DecisionDeny
			if r.Reason != "" {
				reasons = append(reasons, r.Reason)
			}
		case DecisionAllow:
			if decision != DecisionDeny {
				decision = DecisionAllow
			}
		case DecisionNone:
			// 无变化。
		}
		if r.Halt {
			halt = true
			if r.Reason != "" && r.Decision != DecisionDeny {
				// 一个 halting 钩子 那 didn't also deny still contributes
				// 它的 reason 所以 用户 sees it.
				reasons = append(reasons, r.Reason)
			}
		}
		if r.Context != "" {
			contexts = append(contexts, r.Context)
		}
		if r.UpdatedOutput != "" {
			output = r.UpdatedOutput
		}
		if r.UpdatedInput != "" {
			next, err := shallowMerge(merged, r.UpdatedInput)
			if err != nil {
				slog.Warn(
					"Hook updated_input patch rejected; ignoring",
					"error", err,
					"patch", r.UpdatedInput,
				)
				continue
			}
			merged = next
			anyPatch = true
		}
	}

	agg := AggregateResult{
		Decision:      decision,
		Halt:          halt,
		HookCount:     len(results),
		UpdatedOutput: output,
	}
	if anyPatch {
		agg.UpdatedInput = merged
	}
	if len(reasons) > 0 {
		agg.Reason = strings.Join(reasons, "\n")
	}
	if len(contexts) > 0 {
		agg.Context = strings.Join(contexts, "\n")
	}
	return agg
}

// shallowMerge applies top-level-keys patch 到 base (both JSON
// objects). Keys 在 patch overwrite keys 在 base; keys absent 从
// patch 是 preserved. Returns 错误 如果 either 值 是 不 valid
// JSON 对象。
func shallowMerge(base, patch string) (string, error) {
	if base == "" {
		base = "{}"
	}
	// Ensure base 是 object 所以 sjson has somewhere 到 写入.
	var baseAny any
	if err := json.Unmarshal([]byte(base), &baseAny); err != nil {
		return "", err
	}
	if _, ok := baseAny.(map[string]any); !ok {
		return "", errNotObject("tool_input")
	}
	var patchMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patch), &patchMap); err != nil {
		return "", errNotObject("updated_input")
	}
	out := base
	for k, v := range patchMap {
		next, err := sjson.SetRawBytes([]byte(out), k, v)
		if err != nil {
			return "", err
		}
		out = string(next)
	}
	return out, nil
}

type errNotObject string

func (e errNotObject) Error() string { return string(e) + " is not a JSON object" }
