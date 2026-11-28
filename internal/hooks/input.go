package hooks

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/charmbracelet/crush/internal/shell"
	"github.com/tidwall/gjson"
)

// SupportedOutputVersion 是 highest envelope version 这 build
// understands. Hooks may omit `version` entirely (treated 作为 1) 或 pin
// older version. Unknown higher versions 是 still parsed 但 logged.
const SupportedOutputVersion = 1

// Payload 是 JSON structure piped 到 钩子 commands via stdin. 字段
// names follow Claude Code where equivalent exists 所以 scripts written
// 为 it keep working; `事件` 是 kept alongside `hook_event_name`
// because earlier Crush releases shipped it.
//
// ToolInput 是 emitted 作为 parsed JSON object rather than string.
type Payload struct {
	Event                string          `json:"event"`
	HookEventName        string          `json:"hook_event_name"`
	SessionID            string          `json:"session_id"`
	CWD                  string          `json:"cwd"`
	ToolName             string          `json:"tool_name,omitempty"`
	ToolInput            json.RawMessage `json:"tool_input,omitempty"`
	ToolUseID            string          `json:"tool_use_id,omitempty"`
	ToolResponse         *string         `json:"tool_response,omitempty"`
	Error                string          `json:"error,omitempty"`
	LastAssistantMessage *string         `json:"last_assistant_message,omitempty"`
	Decision             string          `json:"decision,omitempty"`
	Source               string          `json:"source,omitempty"`
	Reason               string          `json:"reason,omitempty"`
	Trigger              string          `json:"trigger,omitempty"`
}

// BuildPayload constructs JSON stdin payload 为 事件.
func BuildPayload(ev Event, cwd string) []byte {
	p := Payload{
		Event:                ev.Name,
		HookEventName:        ev.Name,
		SessionID:            ev.SessionID,
		CWD:                  cwd,
		ToolResponse:         ev.ToolResponse,
		Error:                ev.Error,
		LastAssistantMessage: ev.LastAssistantMessage,
		Decision:             ev.Decision,
		Source:               ev.Source,
		Reason:               ev.Reason,
		Trigger:              ev.Trigger,
	}
	if ev.Tool != nil {
		p.ToolName = ev.Tool.Name
		p.ToolUseID = ev.Tool.ID
		p.ToolInput = json.RawMessage(ev.Tool.Input)
		if !json.Valid(p.ToolInput) {
			p.ToolInput = json.RawMessage("{}")
		}
	}
	data, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// BuildEnv constructs environment variable slice 为 钩子 command.
// 它 includes all current process env vars plus 钩子-specific ones.
// 工具 variables 是 只 set 为 events 那 carry 工具调用.
// CLAUDE_PROJECT_DIR mirrors EVO_PROJECT_DIR 所以 钩子 scripts written
// 为 Claude Code can locate themselves.
func BuildEnv(ev Event, cwd, projectDir string) []string {
	env := os.Environ()
	env = append(env, shell.EvoEnvMarkers()...)
	env = append(
		env,
		fmt.Sprintf("EVO_EVENT=%s", ev.Name),
		fmt.Sprintf("EVO_SESSION_ID=%s", ev.SessionID),
		fmt.Sprintf("EVO_CWD=%s", cwd),
		fmt.Sprintf("EVO_PROJECT_DIR=%s", projectDir),
		fmt.Sprintf("CLAUDE_PROJECT_DIR=%s", projectDir),
	)
	if ev.Tool == nil {
		return env
	}
	env = append(env, fmt.Sprintf("EVO_TOOL_NAME=%s", ev.Tool.Name))
	if cmd := gjson.Get(ev.Tool.Input, "command"); cmd.Exists() {
		env = append(env, fmt.Sprintf("EVO_TOOL_INPUT_COMMAND=%s", cmd.String()))
	}
	if fp := gjson.Get(ev.Tool.Input, "file_path"); fp.Exists() {
		env = append(env, fmt.Sprintf("EVO_TOOL_INPUT_FILE_PATH=%s", fp.String()))
	}
	return env
}

// parseStdout parses JSON output 从 钩子 command's stdout. 它
// accepts Crush envelope 和 Claude Code forms: top-level
// `decision: "block"` 用 `reason`, `continue: false` 用
// `stopReason`, 和 `hookSpecificOutput`. second 结果 是 false
// 当 stdout 是 不 JSON object, 所以 callers can tell "no output"
// 从 "unparseable output".
func parseStdout(stdout string) (HookResult, bool) {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return HookResult{Decision: DecisionNone}, false
	}

	var parsed struct {
		Version            int             `json:"version"`
		Decision           string          `json:"decision"`
		Halt               bool            `json:"halt"`
		Reason             string          `json:"reason"`
		Context            json.RawMessage `json:"context"`
		UpdatedInput       json.RawMessage `json:"updated_input"`
		UpdatedOutput      json.RawMessage `json:"updated_output"`
		Continue           *bool           `json:"continue"`
		StopReason         string          `json:"stopReason"`
		HookSpecificOutput json.RawMessage `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		return HookResult{Decision: DecisionNone}, false
	}

	if parsed.Version > SupportedOutputVersion {
		slog.Debug(
			"Hook output declared a newer envelope version than this build supports",
			"version", parsed.Version,
			"supported", SupportedOutputVersion,
		)
	}

	result := HookResult{
		Decision:      parseDecision(parsed.Decision),
		Halt:          parsed.Halt,
		Reason:        parsed.Reason,
		Context:       parseContext(parsed.Context),
		UpdatedInput:  rawToString(parsed.UpdatedInput),
		UpdatedOutput: rawToString(parsed.UpdatedOutput),
	}
	if parsed.Continue != nil && !*parsed.Continue {
		result.Halt = true
		if parsed.StopReason != "" {
			result.Reason = parsed.StopReason
		}
	}
	if len(parsed.HookSpecificOutput) > 0 {
		applyClaudeCodeOutput(&result, parsed.HookSpecificOutput)
	}
	return result, true
}

// parseContext accepts either single string 或 array 的 strings 和
// returns newline-joined 值 用 empty entries dropped.
func parseContext(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// String form.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return ""
	}
	// Array form.
	if raw[0] == '[' {
		var items []string
		if err := json.Unmarshal(raw, &items); err != nil {
			return ""
		}
		out := items[:0]
		for _, s := range items {
			if s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// applyClaudeCodeOutput folds a Claude Code hookSpecificOutput object
// into 结果: permissionDecision maps onto decision, updatedInput onto
// UpdatedInput, 和 updatedToolOutput onto UpdatedOutput.
func applyClaudeCodeOutput(result *HookResult, data json.RawMessage) {
	var hso struct {
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
		UpdatedToolOutput        json.RawMessage `json:"updatedToolOutput"`
		AdditionalContext        string          `json:"additionalContext"`
	}
	if err := json.Unmarshal(data, &hso); err != nil {
		return
	}
	if d := parseDecision(hso.PermissionDecision); d != DecisionNone {
		result.Decision = d
	}
	if hso.PermissionDecisionReason != "" {
		result.Reason = hso.PermissionDecisionReason
	}
	if hso.AdditionalContext != "" {
		result.Context = strings.TrimSpace(result.Context + "\n" + hso.AdditionalContext)
	}
	if s := rawToString(hso.UpdatedInput); s != "" {
		result.UpdatedInput = s
	}
	if s := rawToString(hso.UpdatedToolOutput); s != "" {
		result.UpdatedOutput = s
	}
}

// rawToString converts json.RawMessage 到 string suitable 为 use
// 作为 opaque 工具 input. 它 accepts both JSON object (nested) 和
// JSON string (stringified, 为 backward compatibility).
func rawToString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// If it's a JSON string, unwrap it.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	// Otherwise it's object/array — use 作为-是.
	return string(raw)
}

// parseDecision maps both Crush ("allow"/"deny") 和 Claude Code
// ("block", plus deprecated "approve") spellings onto 决定.
func parseDecision(s string) Decision {
	switch strings.ToLower(s) {
	case "allow", "approve":
		return DecisionAllow
	case "deny", "block":
		return DecisionDeny
	default:
		return DecisionNone
	}
}
