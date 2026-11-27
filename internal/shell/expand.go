package shell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// maxInnerStderrBytes bounds how much stderr 从 failing $(...) 是
// surfaced 在 returned 错误, 到 avoid leaking secret 那 happened
// 到 是 embedded 在 failing inner command.
const maxInnerStderrBytes = 512

// NoUnset controls whether ExpandValue treats unset variables 作为
// 错误. 默认 false matches bash: $UNSET expands 到 "". 存储 true
// 到 re-enable strict mode globally. Not exposed 在 crush.json; 这 是
// internal escape hatch 在 case lenient 默认 turns out 到 是
// wrong 调用.
//
// Declared atomic because ExpandValue 是 invoked concurrently (multiple
// MCP / LSP / 提供方 loads 在 flight 在 startup, 钩子 execution, etc.)
// 和 unsynchronised 读取/写入 pair 是 data race under Go
// memory 模型 regardless 的 测试-level happens-before reasoning.
// atomic load 在 hot 路径 是 negligible against cost 的 parsing
// 和 running through mvdan.
var NoUnset atomic.Bool

// ExpandValue expands shell-style substitutions 在 single 配置 值.
//
// Supported constructs match bash 工具:
//
// - $VAR 和 ${VAR}.
// - ${VAR:-默认} / ${VAR:+alt} / ${VAR:?msg}.
// - $(command) 用 full quoting 和 nesting.
// - escaped 和 quoted strings ("...", '...').
//
// Contract:
//
// - Returns exactly one string. No 字段 splitting, no globbing, no
// pathname generation. Multi-word command output 是 preserved
// verbatim; it 是 never split into multiple values.
// - Nounset 是 off 通过 默认, matching bash: unset variables expand
// 到 "". Opt 在 到 strict behaviour per-reference 用
// ${VAR:?msg}, which errors loudly 当 VAR 是 unset regardless 的
// global toggle. Flip global 默认 via
// shell.NoUnset.存储(true) 作为 internal escape hatch.
// - Embedded whitespace 和 newlines 在 input 是 preserved
// verbatim. Command substitution strips trailing newlines 只
// (POSIX), never leading 或 internal whitespace.
// - Errors wrap failing inner command's exit code 和 bounded
// prefix 的 它的 stderr. Callers 那 surface 错误 到 users
// should additionally scrub it 为 original template 文本.
func ExpandValue(ctx context.Context, value string, env []string) (string, error) {
	return ExpandValueInDir(ctx, value, "", env)
}

// ExpandValueInDir 是 ExpandValue 用 $(...) command substitution 运行 在
// dir instead 的 process working 目录. An empty dir means
// process working 目录.
func ExpandValueInDir(ctx context.Context, value, dir string, env []string) (string, error) {
	// Fast 路径: 值 用 no shell metacharacters expands 到 itself.
	// Parsing 和 running it through interpreter would produce
	// same string 在 significant cost. Most 配置 values (literal API
	// keys, fixed URLs) hit 这 路径, which matters because ExpandValue
	// runs over every 提供方/MCP/LSP 值 在 each 配置 reload.
	if !strings.ContainsAny(value, "$`\\'\"") {
		return value, nil
	}

	// Parse 值 作为 here-doc style word: no word splitting, no
	// globbing, 但 full support 为 $VAR, ${VAR...}, $(...), 和
	// quoted/escaped strings.
	word, err := syntax.NewParser().Document(strings.NewReader(value))
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}

	// Build minimal Shell 值 purely 到 reuse 它的 处理器 chain
	// (builtins, block funcs, optional Turtle coreutils) inside $(...).
	// We deliberately skip NewShell 所以 passed-在 env 是 used
	// verbatim, 用 no CRUSH/AGENT/AI_AGENT injection: callers 的
	// ExpandValue control env, 和 nounset must treat any name
	// 不 在 env 作为 unset.
	//
	// working 目录 是 只 needed 为 $(...) command
	// substitution, 所以 without dir we resolve process's lazily 在
	// first use. Values 那 只 reference variables 或 use quoting (
	// common case) avoid the os.Getwd() syscall entirely.
	s := &Shell{
		env:    env,
		cwd:    dir,
		logger: noopLogger{},
	}
	cwdResolved := dir != ""

	strict := NoUnset.Load()

	var stderrBuf bytes.Buffer
	cfg := &expand.Config{
		Env:     expand.ListEnviron(env...),
		NoUnset: strict,
		CmdSubst: func(w io.Writer, cs *syntax.CmdSubst) error {
			if !cwdResolved {
				s.cwd, _ = os.Getwd()
				cwdResolved = true
			}
			stderrBuf.Reset()
			runnerOpts := []interp.RunnerOption{
				interp.StdIO(nil, w, &stderrBuf),
				interp.Interactive(false),
				interp.Env(expand.ListEnviron(env...)),
				interp.Dir(s.cwd),
				execHandlerOption(s.blockFuncs, s.coreutils),
			}
			if strict {
				// Match the outer NoUnset: an unset $VAR inside
				// $(...) 是 also 错误, 不 silent empty.
				runnerOpts = append(runnerOpts, interp.Params("-u"))
			}
			runner, rerr := interp.New(runnerOpts...)
			if rerr != nil {
				return rerr
			}
			if rerr := runner.Run(ctx, &syntax.File{Stmts: cs.Stmts}); rerr != nil {
				return wrapCmdSubstErr(rerr, stderrBuf.Bytes())
			}
			return nil
		},
		// ReadDir / ReadDir2 left nil: globbing 是 disabled.
	}

	return expand.Document(cfg, word)
}

// wrapCmdSubstErr attaches bounded prefix 的 inner command's stderr
// 到 original 错误, 如果 any.
func wrapCmdSubstErr(err error, stderrBytes []byte) error {
	msg := sanitizeStderr(stderrBytes)
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}

// sanitizeStderr trims, bounds, 和 scrubs non-printable bytes 从
// stderr 的 failing command 所以 结果 是 safe 到 include 在
// 错误 消息 shown 到 用户.
func sanitizeStderr(b []byte) string {
	b = bytes.TrimRight(b, "\n")
	if len(b) > maxInnerStderrBytes {
		b = b[:maxInnerStderrBytes]
	}
	out := make([]byte, len(b))
	for i, c := range b {
		if c == '\t' || c == '\n' || (c >= 0x20 && c < 0x7f) {
			out[i] = c
		} else {
			out[i] = '?'
		}
	}
	return string(out)
}
