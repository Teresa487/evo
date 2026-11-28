package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// handlePermissions implements the `permissions` builtin.
//
// Usage:
//
//	permissions allow <工具> [<工具> ...]
//	permissions deny <工具> [<工具> ...]
//	permissions safe <command line> [<command line> ...]
//	permissions block <command> [<command> ...]
//	permissions unblock <command> [<command> ...]
//
// first two act 在 tools. "allow" adds tools 到 allow-list (tools
// 那 skip 权限 prompts). "deny" hides tools 从 agent entirely
// (options.disabled_tools) — inverse 的 allow. Adding same 工具 twice
// 是 no-op.
//
// Precedence: deny wins. If 工具 appears 在 both allow 和 deny, it 是
// still removed 从 agent's effective 工具 set via disabled_tools.
//
// last three act 在 shell commands rather than tools, which 是
// different question: 不 "may agent 运行 commands 在 all" 但 "which
// commands 是 quiet, 和 which 是 worth warning".
//
// safe runs without 提示词, matched 作为 exact command line given
// block treated 作为 dangerous, 所以 it 是 warned about 和 prompted 为
// unblock drops command 从 dangerous list, built-在 ones included
func handlePermissions(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return nil
	}
	if len(args) < 2 {
		return usage(stderr, "usage: permissions allow|deny|safe|block|unblock <value> [<value> ...]")
	}

	switch args[1] {
	case "allow":
		return permissionsAllow(b, args, stderr)
	case "deny":
		return permissionsDeny(b, args, stderr)
	case "safe":
		return permissionsCommandList(b, args, stderr, "safe_commands")
	case "block":
		return permissionsCommandList(b, args, stderr, "blocked_commands")
	case "unblock":
		return permissionsCommandList(b, args, stderr, "allowed_commands")
	default:
		return usage(stderr, fmt.Sprintf("permissions: unknown subcommand %q (expected allow, deny, safe, block or unblock)", args[1]))
	}
}

// permissionsCommandList appends command entries 到 one 的 command lists
// under permissions. Each argument 是 one 条目, 所以 command line 用
// arguments 是 given 作为 single quoted word:
//
//	permissions safe "go build" "cargo check"
func permissionsCommandList(b *ConfigBuilder, args []string, stderr io.Writer, field string) error {
	if len(args) < 3 {
		return usage(stderr, fmt.Sprintf("usage: permissions %s <command> [<command> ...]", args[1]))
	}
	perms := b.section("permissions")
	list, _ := perms[field].([]any)

	for _, cmd := range args[2:] {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		if !containsAny(list, cmd) {
			list = append(list, cmd)
		}
	}
	perms[field] = list

	slog.Info("Permission commands set in shell config", "field", field, "commands", args[2:])
	return nil
}

func permissionsAllow(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: permissions allow <tool> [<tool> ...]")
	}
	perms := b.section("permissions")
	allowed, _ := perms["allowed_tools"].([]any)

	for _, tool := range args[2:] {
		if !containsAny(allowed, tool) {
			allowed = append(allowed, tool)
		}
	}
	perms["allowed_tools"] = allowed

	slog.Info("Permissions allowed in shell config", "tools", args[2:])
	return nil
}

// permissionsDeny hides tools 从 agent 通过 adding them 到
// options.disabled_tools. 它 是 inverse 的 allow.
func permissionsDeny(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: permissions deny <tool> [<tool> ...]")
	}
	opts := b.section("options")
	disabled, _ := opts["disabled_tools"].([]any)

	for _, tool := range args[2:] {
		if !containsAny(disabled, tool) {
			disabled = append(disabled, tool)
		}
	}
	opts["disabled_tools"] = disabled

	slog.Info("Permissions denied in shell config", "tools", args[2:])
	return nil
}

// containsAny reports whether slice already holds given string 值.
func containsAny(s []any, v string) bool {
	for _, item := range s {
		if str, ok := item.(string); ok && str == v {
			return true
		}
	}
	return false
}
