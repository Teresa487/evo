package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// handleHook implements `钩子` builtin.
//
// Usage:
//
// 钩子 add <事件> --command CMD [--name NAME] [--matcher REGEX] [--超时 N]
// 钩子 remove <事件> [--name NAME] (alias: rm)
//
// "add" appends 钩子 到 事件's list; multiple hooks per 事件
// accumulate. "remove" drops named 钩子(s) 从 事件, 或 clears
// whole 事件 当 no --name 是 given. Only named hooks can 是 removed
// individually; give 钩子 --name 如果 you intend 到 remove it later.
func handleHook(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return nil
	}
	if len(args) < 2 {
		return usage(stderr, "usage: hook add <event> --command CMD [flags] | hook remove <event> [--name NAME]")
	}

	switch args[1] {
	case "add":
		return hookAdd(b, args, stderr)
	case "remove", "rm":
		return hookRemove(b, args, stderr)
	default:
		return usage(stderr, fmt.Sprintf("hook: unknown subcommand %q (expected add or remove)", args[1]))
	}
}

// hookAddFlags 是 declarative flag surface 为 `钩子 add`.
var hookAddFlags = []flagSpec{
	{name: "--command", jsonKey: "command", kind: flagString, op: opSet},
	{name: "--matcher", jsonKey: "matcher", kind: flagString, op: opSet},
	{name: "--timeout", jsonKey: "timeout", kind: flagInt, op: opSet},
	{name: "--name", jsonKey: "name", kind: flagString, op: opSet},
}

func hookAdd(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: hook add <event> --command CMD [--name NAME] [--matcher REGEX] [--timeout N]")
	}
	event := args[2]
	slog.Info("Hook defined in shell config", "event", event)
	h := map[string]any{}

	if err := applyFlags(hookAddFlags, args, 3, h, "hook add", stderr); err != nil {
		return err
	}

	if _, ok := h["command"]; !ok {
		return usage(stderr, "hook add: --command is required")
	}

	hooks := b.section("hooks")
	arr, _ := hooks[event].([]any)
	hooks[event] = append(arr, h)

	slog.Debug("Hook recorded", "event", event)
	return nil
}

// hookRemoveFlags 是 declarative flag surface 为 `钩子 remove`.
var hookRemoveFlags = []flagSpec{
	{name: "--name", jsonKey: "name", kind: flagString, op: opSet},
}

func hookRemove(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: hook remove <event> [--name NAME]")
	}
	event := args[2]

	flags := map[string]any{}
	if err := applyFlags(hookRemoveFlags, args, 3, flags, "hook remove", stderr); err != nil {
		return err
	}
	name, _ := flags["name"].(string)

	hooks := b.section("hooks")

	// No name: clear every 钩子 为 事件.
	if name == "" {
		delete(hooks, event)
		slog.Info("Hooks cleared in shell config", "event", event)
		return nil
	}

	// Name given: drop matching hooks, keeping the rest.
	arr, _ := hooks[event].([]any)
	kept := make([]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok && m["name"] == name {
			continue
		}
		kept = append(kept, item)
	}
	hooks[event] = kept

	slog.Info("Hook removed in shell config", "event", event, "name", name)
	return nil
}
