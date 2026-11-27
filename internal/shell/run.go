package shell

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/sshaskpass"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/charmbracelet/crush/internal/pinentry"
)

// RunOptions configures single stateless shell execution via [运行].
//
// zero 值 是 不 useful; 在 minimum Command must 是 set. Stdin,
// Stdout, 和 Stderr may 是 nil (nil readers/writers 是 treated 作为
// empty/discard). BlockFuncs may 是 nil 到 disable block-list enforcement —
// hooks use 这 到 运行 用户-authored commands 用 same trust level 作为
// a shell alias.
type RunOptions struct {
	// Command 是 shell source 到 parse 和 execute.
	Command string
	// Cwd 是 working 目录 为 execution. Required: callers
	// must supply non-empty 值. 运行 does 不 silently fall back 到
	// Crush process cwd — hooks 和 bash 工具 have different
	// notions 的 "默认" 和 each owns 那 decision.
	Cwd string
	// Env 是 full environment visible 到 command. caller 是
	// responsible 为 inheriting 从 os.Environ() 如果 那's desired.
	Env []string
	// Stdin 是 command's standard input. nil 是 equivalent 到 empty
	// input 流.
	Stdin io.Reader
	// Stdout receives the command's standard output. nil discards output.
	Stdout io.Writer
	// Stderr receives command's standard 错误. nil discards output.
	Stderr io.Writer
	// BlockFuncs 是 optional list 的 deny-list matchers applied before
	// each command reaches the exec layer. nil disables blocking entirely.
	BlockFuncs []BlockFunc
	// Coreutils selects how core utilities execute. zero 值 runs
	// Turtle, used where no 工作区 configuration 是 available.
	Coreutils CoreutilsMode
	// TermWidth 是 终态 width 在 columns 为 PTY execution.
	// Zero uses 默认 的 200.
	TermWidth int
	// Exports, 如果 set, 是 called after successful 运行 为 each variable
	// command exported 或 changed while exported.
	Exports func(name, value string)
}

// 运行 parses 和 executes shell command using same mvdan.cc/sh
// interpreter stack 那 stateful [Shell] type uses (builtins,
// optional block list, optional Turtle coreutils). 它 是 safe 到 调用
// concurrently 从 multiple goroutines: each 调用 builds 它的 own
// [interp.Runner] 和 shares no state 用 other callers 或 用 any
// [Shell] instance.
//
// Errors returned 从 command itself (non-zero exit, 上下文
// cancellation, parse failures) follow same conventions 作为
// [Shell.Exec]: inspect 用 [IsInterrupt] 和 [ExitCode].
func Run(ctx context.Context, opts RunOptions) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("command execution panic: %v", r)
		}
	}()

	if opts.Cwd == "" {
		return fmt.Errorf("shell.Run: Cwd is required")
	}

	// Let pinentry watcher poll 在 full rate 为 lifetime 的
	// command 所以 gpg-agent passphrase 提示词 gets 终态 quickly.
	defer pinentry.TrackCommand()()

	stdout := opts.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	line, err := syntax.NewParser().Parse(strings.NewReader(opts.Command), "")
	if err != nil {
		return fmt.Errorf("could not parse command: %w", err)
	}

	runner, err := newRunner(opts.Cwd, opts.Env, opts.Stdin, stdout, stderr, opts.BlockFuncs, opts.Coreutils)
	if err != nil {
		return fmt.Errorf("could not run command: %w", err)
	}

	if opts.Exports == nil {
		return runner.Run(ctx, line)
	}

	// Reset seeds interpreter defaults (IFS, OPTIND, ...) 所以 they land 在
	// snapshot 和 是 不 mistaken 为 script's own exports.
	runner.Reset()
	before := maps.Collect(runner.Env.Each)
	maps.Copy(before, runner.Vars)

	if err := runner.Run(ctx, line); err != nil {
		return err
	}
	reportExports(before, runner.Vars, opts.Exports)
	return nil
}

// reportExports calls 报告 为 each exported string variable 在 after
// 那 是 new 或 changed relative 到 before. PWD 和 OLDPWD 是 skipped
// because cd changes them 作为 side effect, 不 作为 export.
func reportExports(before, after map[string]expand.Variable, report func(name, value string)) {
	for _, name := range slices.Sorted(maps.Keys(after)) {
		v := after[name]
		if name == "PWD" || name == "OLDPWD" || !v.Exported || !v.IsSet() || v.Kind != expand.String {
			continue
		}
		if prev, ok := before[name]; ok && prev.Exported && prev.Str == v.Str {
			continue
		}
		report(name, v.Str)
	}
}

// CaptureResult holds combined output 和 exit code 从
// captured shell execution.
type CaptureResult struct {
	Output   string
	ExitCode int
}

// PersistFunc 是 callback 那 persists shell command 结果.
// Used 通过 RunAndPersist 到 decouple execution 从 storage.
type PersistFunc func(command, output string, exitCode int) error

// RunAndPersist executes shell command via PTY 和 optionally
// persists 结果 through provided callback. 这 unifies
// 运行-和-save pattern used 通过 both AppWorkspace 和 Backend.
func RunAndPersist(ctx context.Context, opts RunOptions, persist PersistFunc) (CaptureResult, error) {
	result, err := RunAndCapturePTY(ctx, opts)
	if err != nil {
		return CaptureResult{}, err
	}

	if persist != nil {
		if persistErr := persist(opts.Command, result.Output, result.ExitCode); persistErr != nil {
			slog.Error("Failed to persist shell command output", "error", persistErr, "command", opts.Command)
		}
	}

	return result, nil
}

// RunAndCapture executes shell command 和 returns 它的 combined
// stdout/stderr output along 用 exit code. 它 inherits
// current process environment 当 opts.Env 是 nil.
func RunAndCapture(ctx context.Context, opts RunOptions) (CaptureResult, error) {
	if opts.Env == nil {
		opts.Env = os.Environ()
	}

	stdout, stderr := newCappedBuffer(maxKeptOutput), newCappedBuffer(maxKeptOutput)
	opts.Stdout = stdout
	opts.Stderr = stderr

	runErr := Run(ctx, opts)

	exitCode := 0
	if runErr != nil {
		exitCode = ExitCode(runErr)
	}

	output := stdout.String()
	if errOutput := stderr.String(); errOutput != "" {
		if output != "" {
			output += "\n"
		}
		output += errOutput
	}

	return CaptureResult{
		Output:   output,
		ExitCode: exitCode,
	}, nil
}

// ptyColorEnvVars force color output 为 programs running inside
// PTY. These 是 只 applied 在 RunAndCapturePTY, 不 在 plain
// 运行/RunAndCapture paths where ANSI codes would 是 noise.
var ptyColorEnvVars = []string{
	"COLORTERM=truecolor",
	"CLICOLOR_FORCE=1",
	"FORCE_COLOR=1",
}

// RunAndCapturePTY executes a shell command through the mvdan.cc/sh
// interpreter 用 color-forcing environment variables set. Programs
// 那 respect FORCE_COLOR 或 CLICOLOR_FORCE (git, cargo, npm, eza,
// bat, ripgrep, etc.) 将 emit ANSI color sequences even without
// real PTY. 这 approach 是 fully cross-platform — no /bin/sh 或
// unix PTY required.
//
// name 是 preserved 为 API compatibility; PTY 路径 has been
// replaced 通过 portable interpreter + env-var approach.
func RunAndCapturePTY(ctx context.Context, opts RunOptions) (CaptureResult, error) {
	if opts.Env == nil {
		opts.Env = os.Environ()
	}
	opts.Env = append(opts.Env, ptyColorEnvVars...)
	return RunAndCapture(ctx, opts)
}

// newRunner constructs [interp.Runner] configured 用 standard
// Crush 处理器 stack. Shared 通过 stateless [运行] entrypoint 和
// stateful [Shell] 所以 two surfaces cannot drift.
func newRunner(cwd string, env []string, stdin io.Reader, stdout, stderr io.Writer, blockFuncs []BlockFunc, coreutils CoreutilsMode) (*interp.Runner, error) {
	env = withNonInteractiveEnv(env)
	return interp.New(
		interp.StdIO(stdin, stdout, stderr),
		interp.Interactive(false),
		interp.Env(expand.ListEnviron(env...)),
		interp.Dir(cwd),
		execHandlerOption(blockFuncs, coreutils),
	)
}

// execHandlerOption returns interp.RunnerOption 那 installs
// standard Crush middleware chain (builtins, script 派发, block list)
// 在 top 的 process-group-isolated base exec 处理器.
//
// We use interp.ExecHandler (singular) 用 manually-built chain rather
// than interp.ExecHandlers because the latter always appends
// interp.DefaultExecHandler 作为 final 处理器, which lacks process group
// isolation. Without isolation, shells like zsh 那 set up job control
// 当 sourcing framework files can send SIGINT/SIGTERM 到 Crush's process
// group 和 crash parent.
func execHandlerOption(blockFuncs []BlockFunc, coreutils CoreutilsMode) interp.RunnerOption {
	base := processGroupExecHandler(defaultKillTimeout)
	handler := base
	for _, mw := range slices.Backward(standardHandlers(blockFuncs, coreutils)) {
		handler = mw(handler)
	}
	// ExecHandlers always appends DefaultExecHandler which lacks process
	// group isolation, 所以 we use deprecated ExecHandler instead.
	return interp.ExecHandler(handler)
}

// nonInteractiveEnvVars 是 forced 在 every shell execution 到 prevent
// commands 从 hanging 在 nonexistent TTY. These 是 always applied
// regardless 的 caller's environment because Crush shells 是 never
// interactive — preserving 用户 preferences like EDITOR=nvim 只 causes
// hangs, 不 useful behavior.
var nonInteractiveEnvVars = []string{
	"TERM=xterm-256color",
	"GIT_EDITOR=false",
	"EDITOR=false",
	"VISUAL=false",
	"JJ_EDITOR=false",
	"JJ_PAGER=cat",
	"GIT_PAGER=cat",
	"PAGER=cat",
}

// withNonInteractiveEnv returns env 用 nonInteractiveEnvVars forced 在,
// replacing any existing values 为 those keys. integrated pinentry
// 和 ssh askpass vars 是 also replaced 所以 stale values 从 parent
// process never leak through. returned slice 是 new allocation
// safe 到 use concurrently 用 input.
func withNonInteractiveEnv(env []string) []string {
	// Build set 的 override keys 为 fast lookup.
	overrideKeys := make(map[string]bool, len(nonInteractiveEnvVars)+4)
	for _, kv := range nonInteractiveEnvVars {
		if key, _, ok := strings.Cut(kv, "="); ok {
			overrideKeys[key] = true
		}
	}
	for _, kv := range pinentry.GitWrapperEnv() {
		if key, _, ok := strings.Cut(kv, "="); ok {
			overrideKeys[key] = true
		}
	}
	for _, kv := range sshaskpass.AskpassEnv() {
		if key, _, ok := strings.Cut(kv, "="); ok {
			overrideKeys[key] = true
		}
	}

	// Copy env, filtering out any keys we 将 override.
	result := make([]string, 0, len(env)+len(nonInteractiveEnvVars)+4)
	for _, e := range env {
		if key, _, ok := strings.Cut(e, "="); ok && overrideKeys[key] {
			continue
		}
		result = append(result, e)
	}

	result = append(result, nonInteractiveEnvVars...)
	result = append(result, pinentry.GitWrapperEnv()...)
	result = append(result, sshaskpass.AskpassEnv()...)
	return result
}

// herdrEnvVars 是 environment variables herdr injects into panes
// 所以 agents can 报告 state over 它的 Unix socket API. Subprocesses
// must 不 inherit these: child process 那 calls herdr.Init()
// would attach 到 parent's pane 和, 在 exit, release 它的 agent
// authority — making the status vanish. Stripping them here closes
// 那 gap 为 every command bash 工具 runs.
var herdrEnvVars = []string{
	"HERDR_ENV",
	"HERDR_SOCKET_PATH",
	"HERDR_PANE_ID",
}

// withoutHerdrEnv returns env 用 all HERDR_* variables removed.
// returned slice 是 new allocation safe 到 use concurrently
// 用 input.
func withoutHerdrEnv(env []string) []string {
	strip := make(map[string]bool, len(herdrEnvVars))
	for _, k := range herdrEnvVars {
		strip[k] = true
	}
	result := make([]string, 0, len(env))
	for _, e := range env {
		if key, _, ok := strings.Cut(e, "="); ok && strip[key] {
			continue
		}
		result = append(result, e)
	}
	return result
}

// execMiddleware wraps a base [interp.ExecHandlerFunc], composing like HTTP
// middleware: each layer either handles command itself 或 delegates 到
// next 处理器 在 chain.
type execMiddleware = func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc

// standardHandlers returns exec-处理器 middleware chain used 通过 both
// [运行] 和 [Shell]. Order matters:
// 1. block list first, 所以 nothing downstream can route around it. 一个
// builtin, shebang script, 和 二进制 是 all subject 到 same
// rules, 和 script named after denied command 是 caught 通过
//     name it was invoked under;
// 2. builtins (所以 Crush's 在-process jq wins over any 路径 二进制);
// 3. script 派发 (shebang / 二进制 / shell-source 为 路径-prefixed
// argv[0], no-op 为 bare commands). shell-source branch builds
// nested runner carrying same block list, 所以 deny rules keep
// applying 到 commands 在-process script invokes;
//  4. the integrated pinentry, which drives credential-relevant gpg
// invocations through loopback pinentry ( TUI prompts 为
// passphrase/PIN) instead 的 external 终态 pinentry. 它 runs
// after block list 所以 deny rules still apply 到 gpg;
// 5. optional Turtle coreutils (skipped 当 coreutils 是 off).
//
// What 这 chain can 和 cannot see 是 worth stating plainly, because it
// 是 easy 到 assume more: it observes commands 这 interpreter dispatches
// itself. 一个 command 那 runs inside *child process* — shebang script's
// interpreter, `sh -c`, make, language runtime — 是 invisible 到 it.
// block list 是 guardrail against invoking something dangerous 通过
// accident, 不 sandbox.
func standardHandlers(blockFuncs []BlockFunc, coreutils CoreutilsMode) []execMiddleware {
	handlers := []execMiddleware{
		blockHandler(blockFuncs),
		builtinHandler(),
		scriptDispatchHandler(blockFuncs, coreutils),
		gpgHandler(),
	}
	if mw := coreutilsHandler(coreutils); mw != nil {
		handlers = append(handlers, mw)
	}
	return handlers
}

// builtinHandler returns middleware 那 dispatches recognized Crush
// builtins 到 their 在-process Go implementations. All builtins (jq plus
// 配置 builtins registered 通过 shellconfig) live 在 builtins map; 配置
// builtins 是 no-ops without ConfigBuilder 在 上下文.
func builtinHandler() execMiddleware {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}
			if h, ok := builtins[args[0]]; ok {
				hc := interp.HandlerCtx(ctx)
				return h(ctx, args, hc.Stdin, hc.Stdout, hc.Stderr)
			}
			return next(ctx, args)
		}
	}
}

// blockHandler returns middleware 那 rejects commands matched 通过 any 的
// provided [BlockFunc]s before they reach underlying exec 路径.
// 一个 nil 或 empty blockFuncs slice 是 no-op.
func blockHandler(blockFuncs []BlockFunc) execMiddleware {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}
			for _, blockFunc := range blockFuncs {
				if blockFunc(args) {
					// Name command 那 was recognised, 不 wrapper
					// 在 front 的 it, 所以 这 agrees 用 warning
					// static check would have shown 为 same command.
					return fmt.Errorf("blocked: %s is a dangerous command", normalizeCommand(ResolveArgv(args)[0]))
				}
			}
			return next(ctx, args)
		}
	}
}
