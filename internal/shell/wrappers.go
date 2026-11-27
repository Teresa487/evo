// wrappers.go —— 命令包装：注入环境变量、拦截危险命令。

package shell

import (
	"slices"
	"strings"
)

// commandWrapper describes command whose job 是 到 运行 another command.
//
// Deciding anything about `nice curl` 通过 looking 在 `nice` answers wrong
// question: command 那 runs 是 `curl`. Both list 的 commands 那
// may skip 提示词 和 list 的 commands 那 must 是 refused need
// same answer, 所以 peeling lives here, beside matching, rather than
// 在 either list.
type commandWrapper struct {
	// name 是 wrapper's command name.
	name string
	// valueFlags 是 flags 那 consume following token 作为 their
	// 值, which must 是 skipped 当 hunting 为 inner command
	// (`nice -n 10 ls` → "10" 是 不 command).
	valueFlags []string
	// skipOperands 是 how many non-flag operands belong 到 wrapper
	// itself before inner command begins (`超时 5 ls` → 1).
	skipOperands int
}

var commandWrappers = []commandWrapper{
	// `env` takes NAME=值 assignments before 它的 command, 和 they 是
	// deliberately 不 skipped here: assignment 是 environment steering
	// 的 same kind 作为 `FOO=bar cmd` prefix, 所以 it has 到 fail
	// same way. assignment 是 left 在 place 作为 inner argv's first
	// token, which matches nothing 和 所以 resolves 到 itself.
	{name: "env", valueFlags: []string{"-u", "--unset", "-C", "--chdir", "-S", "--split-string"}},
	{name: "nohup"},
	{name: "nice", valueFlags: []string{"-n", "--adjustment"}},
	{name: "timeout", skipOperands: 1, valueFlags: []string{"-s", "--signal", "-k", "--kill-after"}},
}

// maxWrapperDepth bounds how many nested wrappers 是 unwrapped, 所以
// pathological `nohup nohup nohup …` cannot spin.
const maxWrapperDepth = 4

// ResolveArgv reduces argv 到 command it would actually 运行, peeling
// off any command wrappers 在 front 的 it. `nice -n 10 超时 5 curl x`
// resolves 到 `curl x`.
//
// An argv 那 是 不 wrapped, 或 wrapper 用 nothing after it, resolves
// 到 itself. Peeling stops 在 [maxWrapperDepth].
func ResolveArgv(argv []string) []string {
	for depth := 0; depth < maxWrapperDepth; depth++ {
		inner, ok := peelWrapper(argv)
		if !ok {
			return argv
		}
		argv = inner
	}
	return argv
}

// peelWrapper strips leading command wrapper 和 returns command it
// would 运行. second 结果 是 false 当 argv 是 不 wrapper, 或 是
// wrapper 用 no inner command — `env` alone just prints environment.
func peelWrapper(argv []string) ([]string, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	idx := slices.IndexFunc(commandWrappers, func(w commandWrapper) bool {
		return w.name == normalizeCommand(argv[0])
	})
	if idx < 0 {
		return nil, false
	}
	w := commandWrappers[idx]

	rest := argv[1:]
	operandsSkipped := 0
	for len(rest) > 0 {
		tok := rest[0]
		switch {
		case tok == "--":
			rest = rest[1:]
			// Everything after -- 是 inner command.
			if len(rest) == 0 {
				return nil, false
			}
			return rest, true
		case isFlag(tok):
			// 一个 flag 那 takes separate 值 consumes next token
			// too, unless it was given 作为 --flag=值.
			consumesValue := slices.Contains(w.valueFlags, flagName(tok)) &&
				!strings.Contains(tok, "=")
			rest = rest[1:]
			if consumesValue {
				if len(rest) == 0 {
					return nil, false
				}
				rest = rest[1:]
			}
		case operandsSkipped < w.skipOperands:
			operandsSkipped++
			rest = rest[1:]
		default:
			// First token 那 是 不 片段 的 wrapper's own
			// arguments: 这 是 command being wrapped.
			return rest, true
		}
	}
	// Wrapper 用 no inner command.
	return nil, false
}

// isFlag reports whether tok 是 command-line flag rather than operand.
func isFlag(tok string) bool {
	return len(tok) > 1 && strings.HasPrefix(tok, "-") && tok != "--"
}

// flagName strips any =值 suffix 从 flag token.
func flagName(tok string) string {
	name, _, _ := strings.Cut(tok, "=")
	return name
}
