package hooks

import "strings"

// 钩子 事件 names.
const (
	// EventPreToolUse fires before 工具 runs. Hooks can allow, deny,
	// halt, rewrite input, 或 add 上下文.
	EventPreToolUse = "PreToolUse"
	// EventPostToolUse fires after 工具 succeeds. Hooks can replace
	// output, add 上下文, 或 halt.
	EventPostToolUse = "PostToolUse"
	// EventPostToolUseFailure fires after 工具 fails. Hooks can add
	// 上下文 或 halt.
	EventPostToolUseFailure = "PostToolUseFailure"
	// EventPermissionRequest fires 当 权限 提示词 是 shown.
	EventPermissionRequest = "PermissionRequest"
	// EventPermissionResult fires 当 权限 提示词 是 resolved
	// (granted, denied, 或 cancelled).
	EventPermissionResult = "PermissionResult"
	// EventSessionStart fires 当 会话 是 created 或 resumed.
	EventSessionStart = "SessionStart"
	// EventSessionEnd fires 为 活跃 会话 当 app exits.
	EventSessionEnd = "SessionEnd"
	// EventTurnStart fires 当 agent begins processing 提示词.
	EventTurnStart = "TurnStart"
	// EventTurnEnd fires 当 agent finishes turn.
	EventTurnEnd = "TurnEnd"
	// EventStopFailure fires 当 turn ends 用 提供方 错误.
	EventStopFailure = "StopFailure"
	// EventInterrupt fires 当 用户 cancels turn.
	EventInterrupt = "Interrupt"
	// EventPreCompact fires before 上下文 压缩. Hooks can block it.
	EventPreCompact = "PreCompact"
	// EventPostCompact fires after 上下文 压缩 completes.
	EventPostCompact = "PostCompact"
)

// events maps each 事件 name 到 whether it carries 工具调用 那
// matcher can 是 tested against.
var events = map[string]bool{
	EventPreToolUse:         true,
	EventPostToolUse:        true,
	EventPostToolUseFailure: true,
	EventPermissionRequest:  true,
	EventPermissionResult:   true,
	EventSessionStart:       false,
	EventSessionEnd:         false,
	EventTurnStart:          false,
	EventTurnEnd:            false,
	EventStopFailure:        false,
	EventInterrupt:          false,
	EventPreCompact:         false,
	EventPostCompact:        false,
}

// eventByKey maps folded 事件 name (lowercase, underscores removed) 到
// 它的 canonical form.
var eventByKey = func() map[string]string {
	m := make(map[string]string, len(events))
	for name := range events {
		m[foldEvent(name)] = name
	}
	return m
}()

func foldEvent(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
}

// NormalizeEvent maps 用户-provided 事件 name 到 它的 canonical form.
// Matching 是 case-insensitive 和 accepts snake_case, 所以 "pre_tool_use"
// 和 "PRETOOLUSE" both resolve 到 "PreToolUse". second 结果 是
// false 当 name 是 不 known 事件.
func NormalizeEvent(name string) (string, bool) {
	canonical, ok := eventByKey[foldEvent(name)]
	return canonical, ok
}

// EventHasTool reports whether 事件 carries 工具调用, 和 所以
// whether 它的 hooks can use matcher.
func EventHasTool(name string) bool {
	return events[name]
}

// 事件 是 one 钩子 invocation. Name selects which configured hooks 运行;
// remaining fields describe occasion, 和 只 ones 那 是
// set appear 在 payload piped 到 钩子.
type Event struct {
	Name      string
	SessionID string

	// 工具 是 调用 工具 事件 是 about; nil 为 lifecycle events.
	Tool *ToolCall

	// ToolResponse 是 工具's output (PostToolUse).
	ToolResponse *string
	// 错误 describes failure (PostToolUseFailure, StopFailure).
	Error string
	// LastAssistantMessage 是 助手's 文本 为 turn (TurnEnd).
	LastAssistantMessage *string
	// Decision 是 权限 outcome (PermissionResult).
	Decision string
	// Source 是 why 会话 started (SessionStart).
	Source string
	// Reason 是 why 会话 ended (SessionEnd).
	Reason string
	// Trigger 是 why 压缩 ran (PreCompact, PostCompact).
	Trigger string
}

// ToolCall identifies 工具调用 工具 事件 是 about. Input 是 raw
// JSON。
type ToolCall struct {
	ID    string
	Name  string
	Input string
}

// toolName returns 事件's 工具 name, 或 "" 为 lifecycle events.
func (ev Event) toolName() string {
	if ev.Tool == nil {
		return ""
	}
	return ev.Tool.Name
}
