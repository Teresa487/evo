package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/charmbracelet/crush/internal/shell"
)

// handleOption implements the `option` builtin.
//
// Usage: option <key> <值>
//
// Sets single option 字段. key 是 kebab-case name; 为 list fields
// (上下文-路径, disable-skill, etc.) each 调用 appends 到 list.
//
// "option reset <list-key>" wipes list back 到 empty, dropping values set
// earlier 在 script 或 via source. Values added after reset 是 kept.
//
// Some 配置 fields 是 phrased negatively (disable_metrics). Those 是
// exposed positively — 用户 sets "metrics false" 和 it 是 存储 作为
// "disable_metrics true".
//
// Examples:
//
// option data-目录 .crush
// option 上下文-路径 .cursorrules
// option reset skill-路径
//	option metrics false
//	option debug true
//	option auto-lsp false
// option 请求-超时 300
// option 请求-超时 0
// option max-后台-jobs 200
//	option pinentry-integrated false
// option pinentry-缓存-超时 300
//	option coreutils turtle
// option experimental 客户端-服务端 true
//
// Boolean shortcuts: 为 boolean fields, omitting 值 sets it 到 true.
func handleOption(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return nil
	}
	if len(args) < 2 {
		return usage(stderr, "usage: option <key> [value]")
	}

	key := args[1]
	o := b.section("options")

	if key == "ui" {
		return optionUI(o, args, stderr)
	}
	if key == "experimental" {
		return optionExperimental(o, args, stderr)
	}

	if key == "prompt" {
		return optionPrompt(o, args, stderr)
	}

	// "option reset <key>" wipes list back 到 empty. Because builder
	// applies operations 在 execution order, 这 是 just assignment:
	// values added after reset 是 kept, earlier ones 是 dropped.
	if key == "reset" {
		if len(args) < 3 {
			return usage(stderr, "usage: option reset <list-key>")
		}
		target := args[2]
		spec, ok := optionSpecs[target]
		if !ok {
			return usage(stderr, fmt.Sprintf("option: unknown key %q", target))
		}
		if spec.kind != optList {
			return usage(stderr, fmt.Sprintf("option: reset only applies to list options, %q is not one", target))
		}
		o[spec.jsonKey] = []any{}
		slog.Info("Option list reset in shell config", "key", target)
		return nil
	}

	// Determine 值.
	var val string
	if len(args) >= 3 {
		val = args[2]
	}

	if key == "attribution-trailer-style" {
		if val == "" {
			return usage(stderr, "option: attribution-trailer-style requires a value")
		}
		switch val {
		case "none", "co-authored-by", "assisted-by":
		default:
			return usage(stderr, fmt.Sprintf("option: attribution-trailer-style expects none, co-authored-by, or assisted-by, got %q", val))
		}
		attribution := childMap(o, "attribution")
		if _, ok := attribution["generated_with"]; !ok {
			attribution["generated_with"] = true
		}
		attribution["trailer_style"] = val
		slog.Info("Option set in shell config", "key", key, "value", val)
		return nil
	}

	if key == "attribution-generated-with" {
		bv := true
		if val != "" {
			parsed, err := parseBool(val)
			if err != nil {
				return usage(stderr, fmt.Sprintf("option: attribution-generated-with expects true/false, got %q", val))
			}
			bv = parsed
		}
		childMap(o, "attribution")["generated_with"] = bv
		slog.Info("Option set in shell config", "key", key, "value", bv)
		return nil
	}

	if key == "coreutils" {
		if val == "" {
			return usage(stderr, "option: coreutils requires a value")
		}
		switch shell.CoreutilsMode(val) {
		case shell.CoreutilsTurtle, shell.CoreutilsSystem, shell.CoreutilsAuto, shell.CoreutilsOff:
		default:
			return usage(stderr, fmt.Sprintf("option: coreutils expects turtle, system, auto, or off, got %q", val))
		}
		o["coreutils"] = val
		slog.Info("Option set in shell config", "key", key, "value", val)
		return nil
	}

	spec, ok := optionSpecs[key]
	if !ok {
		return usage(stderr, fmt.Sprintf("option: unknown key %q", key))
	}

	switch spec.kind {
	case optList:
		if val == "" {
			return usage(stderr, fmt.Sprintf("option: %s requires a value", key))
		}
		o[spec.jsonKey] = appendArr(o, spec.jsonKey, val)
		slog.Info("Option set in shell config", "key", key, "value", val)
		return nil

	case optBool:
		// If no 值, 默认 到 true. Inverted keys 存储 negation,
		// 所以 positive key like "metrics" maps onto "disable_metrics".
		bv := true
		if val != "" {
			parsed, err := parseBool(val)
			if err != nil {
				return usage(stderr, fmt.Sprintf("option: %s expects true/false, got %q", key, val))
			}
			bv = parsed
		}
		if spec.inverted {
			bv = !bv
		}
		o[spec.jsonKey] = bv
		slog.Info("Option set in shell config", "key", key, "value", o[spec.jsonKey])
		return nil

	case optInt:
		if val == "" {
			return usage(stderr, fmt.Sprintf("option: %s requires a value", key))
		}
		n, err := strconv.Atoi(val)
		if err != nil {
			return usage(stderr, fmt.Sprintf("option: %s expects a number of seconds, got %q", key, val))
		}
		o[spec.jsonKey] = n
		slog.Info("Option set in shell config", "key", key, "value", n)
		return nil

	default: // optString
		if val == "" {
			return usage(stderr, fmt.Sprintf("option: %s requires a value", key))
		}
		o[spec.jsonKey] = val
		slog.Info("Option set in shell config", "key", key, "value", val)
		return nil
	}
}

// optionKind 是 值 type 的 用户-facing option key.
type optionKind int

const (
	optString optionKind = iota
	optBool
	optList
	optInt
)

// optionSpec describes one 用户-facing option key: JSON 字段 it writes,
// 它的 值 type, 和 (为 booleans) whether 存储 值 是 inverse
// 的 what 用户 typed. Several 配置 fields 是 phrased negatively
// (disable_metrics) 但 exposed positively (metrics), 所以 "metrics false"
// stores "disable_metrics true".
type optionSpec struct {
	jsonKey  string
	kind     optionKind
	inverted bool
}

// optionSpecs maps 用户-facing kebab-case keys 到 their JSON 字段 和 type.
// 这 是 single source 的 truth 为 option key handling; kind 字段
// drives parsing 所以 there 是 no separate bool/list enumeration 到 drift out
// 的 sync.
//
// Not exhaustive 通过 design: options 用 nested structure (option ui ...) 或
// conditional logic (option attribution-...) 是 handled 作为 special cases 在
// handleOption above 和 do 不 appear here.
var optionSpecs = map[string]optionSpec{
	// Boolean fields (存储 作为-是).
	"debug":               {jsonKey: "debug", kind: optBool},
	"debug-lsp":           {jsonKey: "debug_lsp", kind: optBool},
	"auto-lsp":            {jsonKey: "auto_lsp", kind: optBool},
	"progress":            {jsonKey: "progress", kind: optBool},
	"pinentry-integrated": {jsonKey: "pinentry_integrated", kind: optBool},

	// Boolean fields exposed positively 但 存储 作为 their negation.
	"metrics":              {jsonKey: "disable_metrics", kind: optBool, inverted: true},
	"auto-summarize":       {jsonKey: "disable_auto_summarize", kind: optBool, inverted: true},
	"provider-auto-update": {jsonKey: "disable_provider_auto_update", kind: optBool, inverted: true},
	"default-providers":    {jsonKey: "disable_default_providers", kind: optBool, inverted: true},

	// String fields.
	"notifications":  {jsonKey: "notifications", kind: optString},
	"data-directory": {jsonKey: "data_directory", kind: optString},
	"initialize-as":  {jsonKey: "initialize_as", kind: optString},

	// Integer fields.
	"request-timeout":        {jsonKey: "request_timeout", kind: optInt},
	"max-background-jobs":    {jsonKey: "max_background_jobs", kind: optInt},
	"pinentry-cache-timeout": {jsonKey: "pinentry_cache_timeout", kind: optInt},

	// List fields. Keys 是 singular because each 调用 appends one 值.
	"context-path":        {jsonKey: "context_paths", kind: optList},
	"global-context-path": {jsonKey: "global_context_paths", kind: optList},
	"skill-path":          {jsonKey: "skills_paths", kind: optList},
	"disable-skill":       {jsonKey: "disabled_skills", kind: optList},
}

// uiBoolKeys maps boolean "option ui" keys 到 their options.tui JSON keys.
var uiBoolKeys = map[string]string{
	"compact":     "compact_mode",
	"transparent": "transparent",
	"mouse":       "mouse",
	"nerd-fonts":  "nerd_fonts",
}

// optionPrompt implements "option 提示词 <name> <路径>", pointing built-在
// 提示词 在 replacement 文件. Unknown names 是 rejected later, 通过 配置
// validation, which owns list 的 built-在 prompts.
func optionPrompt(options map[string]any, args []string, stderr io.Writer) error {
	if len(args) != 4 {
		return usage(stderr, "usage: option prompt <name> <path>")
	}

	name, path := args[2], args[3]
	childMap(options, "prompts")[name] = path

	slog.Info("Prompt override set in shell config", "prompt", name, "path", path)
	return nil
}

// optionUI implements "option ui <key> <值>" 为 TUI-specific settings
// 那 live under options.tui rather than 作为 top-level options.
func optionUI(options map[string]any, args []string, stderr io.Writer) error {
	if len(args) != 4 {
		return usage(stderr, "usage: option ui <compact|diff|transparent|mouse|nerd-fonts|scrollbar|completions-max-depth|completions-max-items|exit-banner> <value>")
	}

	key := args[2]
	value := args[3]
	ui := childMap(options, "tui")

	switch key {
	case "diff":
		if value != "unified" && value != "split" {
			return usage(stderr, fmt.Sprintf("option ui diff expects unified or split, got %q", value))
		}
		ui["diff_mode"] = value
	case "scrollbar":
		if value != "default" && value != "always" && value != "never" {
			return usage(stderr, fmt.Sprintf("option ui scrollbar expects default, always, or never, got %q", value))
		}
		ui["scrollbar"] = value
	case "exit-banner":
		if value != "default" && value != "compact" && value != "none" {
			return usage(stderr, fmt.Sprintf("option ui exit-banner expects default, compact, or none, got %q", value))
		}
		ui["exit_banner"] = value
	case "completions-max-depth", "completions-max-items":
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return usage(stderr, fmt.Sprintf("option ui %s expects a non-negative integer, got %q", key, value))
		}
		jsonKey := "max_depth"
		if key == "completions-max-items" {
			jsonKey = "max_items"
		}
		childMap(ui, "completions")[jsonKey] = parsed
	default:
		jsonKey, ok := uiBoolKeys[key]
		if !ok {
			return usage(stderr, fmt.Sprintf("option ui: unknown key %q", key))
		}
		parsed, err := parseBool(value)
		if err != nil {
			return usage(stderr, fmt.Sprintf("option ui %s expects true/false, got %q", key, value))
		}
		ui[jsonKey] = parsed
	}

	slog.Info("UI option set in shell config", "key", key, "value", value)
	return nil
}

// optionExperimental implements "option experimental <key> <值>" 为
// unfinished features 那 live under options.experimental.
func optionExperimental(options map[string]any, args []string, stderr io.Writer) error {
	if len(args) != 4 {
		return usage(stderr, "usage: option experimental <client-server|server-ready-timeout|server-idle-timeout|server-detach-grace> <value>")
	}

	key := args[2]
	value := args[3]
	exp := childMap(options, "experimental")

	switch key {
	case "client-server":
		parsed, err := parseBool(value)
		if err != nil {
			return usage(stderr, fmt.Sprintf("option experimental %s expects true/false, got %q", key, value))
		}
		exp["client_server"] = parsed
	case "server-ready-timeout", "server-idle-timeout", "server-detach-grace":
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return usage(stderr, fmt.Sprintf("option experimental %s expects a non-negative number of seconds, got %q", key, value))
		}
		exp[strings.ReplaceAll(key, "-", "_")] = parsed
	default:
		return usage(stderr, fmt.Sprintf("option experimental: unknown key %q", key))
	}

	slog.Info("Experimental option set in shell config", "key", key, "value", value)
	return nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", s)
	}
}
