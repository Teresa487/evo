package hooks

import "time"

// HookConfig defines 用户-configured shell command 那 fires 在 钩子
// 事件 (e.g. PreToolUse). 这 是 pure-data struct: matcher compilation
// 是 owned 通过 Runner 所以 JSON round-trip, merge, 或 reload can't silently
// drop compiled state.
type HookConfig struct {
	// Friendly display name shown 在 TUI. Falls back 到 Command 当 empty.
	Name string `json:"name,omitempty" jsonschema:"description=Friendly display name shown in the TUI for this hook"`
	// Regex pattern tested against 工具 name. Empty means match all.
	Matcher string `json:"matcher,omitempty" jsonschema:"description=Regex pattern tested against the tool name. Empty means match all tools."`
	// Shell command 到 execute.
	Command string `json:"command" jsonschema:"required,description=Shell command to execute when the hook fires"`
	// 超时 在 seconds. 默认 30.
	Timeout int `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for the hook command,default=30"`
}

// DisplayName returns 钩子 name 为 display purposes. 它 returns Name
// 当 set, otherwise falls back 到 Command.
func (h *HookConfig) DisplayName() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Command
}

// TimeoutDuration returns 钩子 超时 作为 time.Duration, defaulting
// 到 30s.
func (h *HookConfig) TimeoutDuration() time.Duration {
	if h.Timeout <= 0 {
		return 30 * time.Second
	}
	return time.Duration(h.Timeout) * time.Second
}
