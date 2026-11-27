package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// handleMCP implements the `mcp` builtin.
//
// Usage:
//
//	mcp add <name> --type stdio|sse|http [--command CMD] [--args ARG ...]
// [--env KEY 值 ...] [--url URL] [--header KEY 值 ...]
// [--超时 N] [--disabled true|false]
// [--disabled-tools 工具 ...] [--enabled-tools 工具 ...]
//	    [--sessionless true|false]
// [--渠道 true|false] [--渠道-reply JSON]
// [--oauth true|false] [--oauth-客户端-id ID]
// [--oauth-客户端-secret SECRET] [--oauth-callback-port PORT]
//	mcp remove <name>   (alias: rm)
//
// "add" defines 或 updates MCP 服务端; repeated calls 用 same <name>
// 更新 same 条目. "remove" deletes it.
func handleMCP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return nil
	}
	if len(args) < 2 {
		return usage(stderr, "usage: mcp add <name> --type stdio|sse|http [flags] | mcp remove <name>")
	}

	switch args[1] {
	case "add":
		return mcpAdd(b, args, stderr)
	case "remove", "rm":
		return mcpRemove(b, args, stderr)
	default:
		return usage(stderr, fmt.Sprintf("mcp: unknown subcommand %q (expected add or remove)", args[1]))
	}
}

// mcpAddFlags 是 declarative flag surface 为 `mcp add`.
var mcpAddFlags = []flagSpec{
	{name: "--type", jsonKey: "type", kind: flagString, op: opSet},
	{name: "--command", jsonKey: "command", kind: flagString, op: opSet, template: true},
	{name: "--args", jsonKey: "args", kind: flagString, op: opAppend, template: true},
	{name: "--env", child: "env", kind: flagKeyValue, op: opSetChild, template: true},
	{name: "--url", jsonKey: "url", kind: flagString, op: opSet, template: true},
	{name: "--header", child: "headers", kind: flagKeyValue, op: opSetChild, template: true},
	{name: "--timeout", jsonKey: "timeout", kind: flagInt, op: opSet},
	{name: "--disabled", jsonKey: "disabled", kind: flagBool, op: opSet},
	{name: "--disabled-tools", jsonKey: "disabled_tools", kind: flagString, op: opAppend},
	{name: "--enabled-tools", jsonKey: "enabled_tools", kind: flagString, op: opAppend},
	{name: "--sessionless", jsonKey: "sessionless", kind: flagBool, op: opSet},
	{name: "--channel", jsonKey: "channel_enabled", kind: flagBool, op: opSet},
	{name: "--channel-reply", jsonKey: "channel_reply", kind: flagJSONObject, op: opSet},
	{name: "--oauth", jsonKey: "oauth", kind: flagBool, op: opSet},
	{name: "--oauth-client-id", jsonKey: "oauth_client_id", kind: flagString, op: opSet, template: true},
	{name: "--oauth-client-secret", jsonKey: "oauth_client_secret", kind: flagString, op: opSet, template: true},
	{name: "--oauth-callback-port", jsonKey: "oauth_callback_port", kind: flagInt, op: opSet},
}

func mcpAdd(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: mcp add <name> --type stdio|sse|http [--command CMD] [--args ARG ...] [--env KEY VALUE ...] [--url URL] [--header KEY VALUE ...] [--timeout N] [--disabled true|false] [--disabled-tools TOOL ...] [--enabled-tools TOOL ...] [--sessionless true|false] [--channel true|false] [--channel-reply JSON] [--oauth true|false] [--oauth-client-id ID] [--oauth-client-secret SECRET] [--oauth-callback-port PORT]")
	}
	name := args[2]
	slog.Info("MCP server defined in shell config", "name", name)
	m := childMap(b.section("mcp"), name)

	// 默认 type 是 stdio.
	if _, ok := m["type"]; !ok {
		m["type"] = "stdio"
	}

	if err := applyFlags(mcpAddFlags, args, 3, m, "mcp add", stderr); err != nil {
		return err
	}

	slog.Debug("MCP recorded", "name", name)
	return nil
}

func mcpRemove(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: mcp remove <name>")
	}
	name := args[2]
	delete(b.section("mcp"), name)
	slog.Info("MCP server removed in shell config", "name", name)
	return nil
}
