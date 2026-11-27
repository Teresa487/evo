// Package shell provides cross-platform shell execution capabilities.
//
// 这 package provides Shell instances 为 executing commands 用 their own
// working 目录 和 environment. Each shell execution 是 independent.
//
// WINDOWS COMPATIBILITY:
// 这 implementation provides POSIX shell emulation (mvdan.cc/sh/v3) even 在
// Windows. Commands should use forward slashes (/) 作为 路径 separators 到 work
// correctly 在 all platforms.
package shell

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/charmbracelet/crush/internal/pinentry"
	"github.com/charmbracelet/x/exp/slice"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// ShellType represents type 的 shell 到 use
type ShellType int

const (
	ShellTypePOSIX ShellType = iota
	ShellTypeCmd
	ShellTypePowerShell
)

// CrushEnvMarkers returns fresh slice 的 environment variables 那
// Crush unconditionally sets 在 every shell it spawns — both interactive
// bash 工具's [Shell] 和 钩子 runner's [运行] calls. Tools 那 want 到
// detect "am I being invoked 通过 AI agent?" can check any 的 these.
// Keeping them 在 one place guarantees two shell surfaces cannot drift.
// 一个 fresh slice 是 returned 在 every 调用 所以 callers may append freely.
func CrushEnvMarkers() []string {
	return []string{
		"CRUSH=1",
		"AGENT=crush",
		"AI_AGENT=crush",
	}
}

// Logger interface 为 optional logging
type Logger interface {
	InfoPersist(msg string, keysAndValues ...any)
}

// noopLogger 是 logger 那 does nothing
type noopLogger struct{}

func (noopLogger) InfoPersist(msg string, keysAndValues ...any) {}

// BlockFunc 是 function 那 determines 如果 command should 是 blocked
type BlockFunc func(args []string) bool

// Shell provides cross-platform shell execution 用 optional state persistence
type Shell struct {
	env        []string
	cwd        string
	mu         sync.Mutex
	logger     Logger
	blockFuncs []BlockFunc
	coreutils  CoreutilsMode
}

// Options 为 creating new shell
type Options struct {
	WorkingDir string
	Env        []string
	Logger     Logger
	BlockFuncs []BlockFunc
	// Coreutils selects how core utilities execute. zero 值 runs
	// Turtle, used where no 工作区 configuration 是 available.
	Coreutils CoreutilsMode
}

// NewShell creates new shell instance 用 given options
func NewShell(opts *Options) *Shell {
	if opts == nil {
		opts = &Options{}
	}

	cwd := opts.WorkingDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	env := opts.Env
	if env == nil {
		env = os.Environ()
	}

	// Strip herdr pane-ownership vars 所以 subprocesses (including 测试
	// binaries 和 nested crush instances) can't attach 到 或 release
	// the parent pane's agent authority.
	env = withoutHerdrEnv(env)

	// Allow tools 到 detect execution 通过 Crush.
	env = append(env, CrushEnvMarkers()...)

	logger := opts.Logger
	if logger == nil {
		logger = noopLogger{}
	}

	return &Shell{
		cwd:        cwd,
		env:        env,
		logger:     logger,
		blockFuncs: opts.BlockFuncs,
		coreutils:  opts.Coreutils,
	}
}

// Exec executes command 在 shell
func (s *Shell) Exec(ctx context.Context, command string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.exec(ctx, command)
}

// ExecStream executes command 在 shell 用 streaming output 到 provided writers
func (s *Shell) ExecStream(ctx context.Context, command string, stdout, stderr io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.execStream(ctx, command, stdout, stderr)
}

// GetWorkingDir returns current working 目录
func (s *Shell) GetWorkingDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

// SetWorkingDir sets working 目录
func (s *Shell) SetWorkingDir(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify 目录 exists
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("directory does not exist: %w", err)
	}

	s.cwd = dir
	return nil
}

// GetEnv returns copy 的 environment variables
func (s *Shell) GetEnv() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	env := make([]string, len(s.env))
	copy(env, s.env)
	return env
}

// SetEnv sets an environment variable
func (s *Shell) SetEnv(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 更新 或 add environment variable
	keyPrefix := key + "="
	for i, env := range s.env {
		if strings.HasPrefix(env, keyPrefix) {
			s.env[i] = keyPrefix + value
			return
		}
	}
	s.env = append(s.env, keyPrefix+value)
}

// SetBlockFuncs sets command block functions 为 shell
func (s *Shell) SetBlockFuncs(blockFuncs []BlockFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockFuncs = blockFuncs
}

// CommandsBlocker creates BlockFunc 那 blocks command 通过 name,
// regardless 的 any leading 路径 (所以 "/bin/curl" 是 blocked same 作为
// "curl").
func CommandsBlocker(cmds []string) BlockFunc {
	bannedSet := make(map[string]struct{}, len(cmds))
	for _, cmd := range cmds {
		bannedSet[normalizeCommand(cmd)] = struct{}{}
	}

	return func(args []string) bool {
		// Resolve through any wrapper first: `nice curl` 是 invocation
		// 的 curl, 和 rule about curl has 到 see it 作为 one. list 的
		// commands allowed 到 skip 提示词 peels same way, 所以 both
		// answer "what actually runs here" identically.
		args = ResolveArgv(args)
		if len(args) == 0 {
			return false
		}
		_, ok := bannedSet[normalizeCommand(args[0])]
		return ok
	}
}

// Rule blocks single command invocation. 它 matches 当 command name
// (用 any leading 路径 stripped) equals Command, leading positional
// arguments match Args 在 order, 和 every flag 在 Flags 是 present.
type Rule struct {
	// Command 是 command name 到 match, without 路径, e.g. "npm".
	Command string
	// Args 是 required leading positional arguments, e.g. ["install"].
	Args []string
	// Flags 是 flags 那 must all 是 present 为 rule 到 match, e.g.
	// ["--global"]. Clustered short flags 是 matched too, 所以 rule naming
	// "-S" matches "pacman -Syu".
	Flags []string
}

// Match reports whether given expanded argument list 是 blocked 通过
// rule. argv 是 resolved through any command wrapper first, 所以 rule
// about `npm install -g` catches `nice npm install -g` too.
func (r Rule) Match(args []string) bool {
	args = ResolveArgv(args)
	if len(args) == 0 || normalizeCommand(args[0]) != normalizeCommand(r.Command) {
		return false
	}

	pos, flags := splitArgsFlags(args[1:])
	if len(pos) < len(r.Args) || !slices.Equal(pos[:len(r.Args)], r.Args) {
		return false
	}
	return slice.IsSubset(r.Flags, flags)
}

// ArgumentsBlocker creates BlockFunc 那 blocks specific subcommand
// invocation. 它 是 thin adapter over [Rule].
func ArgumentsBlocker(cmd string, args []string, flags []string) BlockFunc {
	return Rule{Command: cmd, Args: args, Flags: flags}.Match
}

// normalizeCommand reduces command word 到 bare command name 为 matching:
// it strips any 目录 prefix 和 Windows executable extension, 所以
// "/usr/bin/rm", "rm.exe" 和 "RM.EXE" all normalize 到 "rm".
//
// Matching folds case everywhere. Windows 和 macOS both resolve command
// names case-insensitively, 所以 在 those platforms `SUDO` runs very same
// 二进制 作为 `sudo` 和 case-sensitive list simply misses it. Linux can tell
// two apart, 但 machine carrying `CURL` 那 是 genuinely
// different program 从 `curl` 是 不 real scenario, 和 folding there too
// costs 在 most one unnecessary 提示词. Not folding costs silent bypass.
func normalizeCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		// filepath.Base("") 是 ".", which would 是 command name nobody
		// meant 和 which matches real 目录 reference.
		return ""
	}
	cmd = strings.ToLower(filepath.Base(filepath.FromSlash(cmd)))
	for _, ext := range []string{".exe", ".bat", ".cmd"} {
		if len(cmd) > len(ext) && cmd[len(cmd)-len(ext):] == ext {
			return cmd[:len(cmd)-len(ext)]
		}
	}
	return cmd
}

// NormalizeCommandName reduces command name 到 form block list
// matches 在. Configuration goes through 这 too, 所以 条目 written
// "/usr/bin/CURL " lines up 用 "curl" list already holds instead 的
// quietly matching nothing.
func NormalizeCommandName(cmd string) string {
	return normalizeCommand(cmd)
}

// splitArgsFlags separates positional arguments 从 flags. 它 understands
// "--" end-的-options marker, "--flag=值" (matched 作为 "--flag"), 和
// clustered short flags ("-Syu" also yields "-S", "-y", "-u") 所以 那 rules
// naming a single short flag still match it inside a cluster.
func splitArgsFlags(parts []string) (args []string, flags []string) {
	args = make([]string, 0, len(parts))
	flags = make([]string, 0, len(parts))
	endOfFlags := false
	for _, part := range parts {
		if endOfFlags || part == "-" || !strings.HasPrefix(part, "-") {
			args = append(args, part)
			continue
		}
		if part == "--" {
			endOfFlags = true
			continue
		}
		name := part
		if before, _, ok := strings.Cut(part, "="); ok {
			name = before
		}
		flags = append(flags, name)
		// Expand clustered short flags (e.g. "-Syu"). Long ("--") flags 和
		// single-character shorts need no expansion.
		if !strings.HasPrefix(name, "--") && len(name) > 2 {
			for _, c := range name[1:] {
				flags = append(flags, "-"+string(c))
			}
		}
	}
	return args, flags
}

// IsCommandBlocked reports whether command string would likely 是 blocked
// 通过 given block functions. See [BlockedCommandReason] 为 details.
func IsCommandBlocked(command string, blockFuncs []BlockFunc) bool {
	return BlockedCommandReason(command, blockFuncs) != ""
}

// CommandCheck 是 verdict 的 static command check.
//
// Reason 和 Matched answer different questions, 和 conflating them 是
// mistake 这 type exists 到 prevent. Reason says "there 是 something here
// worth warning about", which covers both command 在 deny list 和
// command nobody could analyse. Matched says specifically " named command 在
// deny list was recognised", which 是 只 case where 用户 can 是
// shown what they 是 approving 和 meaningfully approve it. Treating
// unanalysable command 作为 though it had been approved lets unrelated
// $(date) anywhere 在 line waive 运行-time checks 为 everything else
// 在 it.
type CommandCheck struct {
	// Matched reports 那 command was recognised 在 deny list 通过
	// name. Reason then names it.
	Matched bool
	// Reason 是 short human-readable explanation, 或 empty 当 nothing
	// looked wrong.
	Reason string
}

// Dangerous reports whether the check found anything worth warning about.
func (c CommandCheck) Dangerous() bool { return c.Reason != "" }

// CheckCommand statically inspects command string 为 anything dangerous.
//
// 它 是 used 到 warn about dangerous commands before they 运行 和 到 gate
// auto-approval. Each command 在 script 是 expanded 到 fields way
// shell would (quotes 是 removed, word parts joined, globbing disabled), 但
// nothing 是 executed: command substitution 或 any other expansion 那
// would require running command 是 treated 作为 dangerous rather than
// resolved. 一个 command name 那 是 itself built 从 expansion
// ("$CMD example.com") 是 dangerous 为 same reason: what it resolves 到
// 是 unknowable until it runs. Unparseable input 是 likewise treated 作为
// dangerous. 这 fails safe, 但 it cannot see results 的 runtime
// expansion, 所以 it 是 conservative approximation 的 authoritative
// blockHandler check.
func CheckCommand(command string, blockFuncs []BlockFunc) CommandCheck {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		// If we 可以't parse it, consider it potentially dangerous.
		// 一个 command shell cannot parse will 不 运行 either way: 运行
		// 和 execCommon refuse it 用 parse 错误 的 their own. Warning
		// about danger here would dress typo up 作为 threat.
		return CommandCheck{}
	}

	// Empty environment, nil CmdSubst, 和 erroring ProcSubst: variables
	// resolve 到 empty 和 command/process substitutions 错误 out instead
	// 的 executing. ProcSubst 处理器 是 required: mvdan.cc/sh calls
	// cfg.ProcSubst directly without nil guard (unlike CmdSubst), 所以 any
	// command containing <(...) 或 >(...) would otherwise panic 用 nil
	// function 调用.
	cfg := &expand.Config{
		Env: expand.FuncEnviron(func(string) string { return "" }),
		ProcSubst: func(*syntax.ProcSubst) (string, error) {
			return "", errors.New("process substitution requires execution")
		},
	}

	var result CommandCheck
	syntax.Walk(file, func(node syntax.Node) bool {
		// Returning false prunes current subtree 但 walk carries
		// 在, 所以 without 这 guard later, vaguer finding would overwrite
		// name 的 command 那 was actually recognised.
		if result.Matched {
			return false
		}
		switch node := node.(type) {
		case *syntax.CallExpr:
			if len(node.Args) == 0 {
				return true
			}
			if isDynamicWord(node.Args[0]) {
				result.Reason = cmp.Or(result.Reason, "expansion in command name")
				return false
			}
			args, err := expand.Fields(cfg, node.Args...)
			if err != nil {
				// 一个 substitution 或 expansion we can't resolve without running
				// something. Be conservative 和 treat it 作为 dangerous.
				result.Reason = cmp.Or(result.Reason, "substitution in arguments")
				return false
			}
			for _, blockFunc := range blockFuncs {
				if blockFunc(args) {
					// 一个 named match outranks anything found earlier: it 是
					// most specific thing 那 can 是 said about command,
					// 和 只 finding 用户 can act 在.
					result = CommandCheck{Matched: true, Reason: normalizeCommand(ResolveArgv(args)[0])}
					return false
				}
			}
		case *syntax.Redirect:
			// Process substitutions 在 redirect position (cmd > >(...),
			// cmd < <(...)) hang off redirect word rather than 调用
			// argument, 所以 expand it too: ProcSubst 处理器 errors 和
			// command fails closed 作为 dangerous. Plain filenames 和
			// variable expansions resolve without 错误 和 是 ignored.
			if node.Word == nil {
				return true
			}
			if _, err := expand.Fields(cfg, node.Word); err != nil {
				result.Reason = cmp.Or(result.Reason, "process substitution in redirect position")
				return false
			}
		}
		return true
	})

	return result
}

// BlockedCommandReason returns a short human-readable reason why a command
// string would likely 是 blocked 通过 given block functions, 或 empty
// string 如果 it looks safe. See [CheckCommand] 当 distinction between
// named deny-list match 和 unanalysable command matters.
func BlockedCommandReason(command string, blockFuncs []BlockFunc) string {
	return CheckCommand(command, blockFuncs).Reason
}

// isDynamicWord reports whether word's 值 depends 在 runtime state, i.e.
// whether it contains parameter expansion, command 或 process
// substitution, 或 arithmetic expansion.
func isDynamicWord(word *syntax.Word) bool {
	dynamic := false
	syntax.Walk(word, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp:
			dynamic = true
			return false
		}
		return true
	})
	return dynamic
}

// newInterp creates new interpreter 用 current shell state. 一个 nil
// stdin 是 equivalent 到 empty input 流.
func (s *Shell) newInterp(stdin io.Reader, stdout, stderr io.Writer) (*interp.Runner, error) {
	return newRunner(s.cwd, s.env, stdin, stdout, stderr, s.blockFuncs, s.coreutils)
}

// updateShellFromRunner updates shell 从 interpreter after execution.
func (s *Shell) updateShellFromRunner(runner *interp.Runner) {
	s.cwd = runner.Dir
	s.env = s.env[:0]
	for name, vr := range runner.Vars {
		if vr.Exported {
			s.env = append(s.env, name+"="+vr.Str)
		}
	}
}

// execCommon 是 shared implementation 为 executing commands
func (s *Shell) execCommon(ctx context.Context, command string, stdout, stderr io.Writer) (err error) {
	// Let pinentry watcher poll 在 full rate 为 lifetime 的
	// command 所以 gpg-agent passphrase 提示词 gets 终态 quickly.
	defer pinentry.TrackCommand()()

	var runner *interp.Runner
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("command execution panic: %v", r)
		}
		if runner != nil {
			s.updateShellFromRunner(runner)
		}
		s.logger.InfoPersist("command finished", "command", command, "err", err)
	}()

	line, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return fmt.Errorf("could not parse command: %w", err)
	}

	runner, err = s.newInterp(nil, stdout, stderr)
	if err != nil {
		return fmt.Errorf("could not run command: %w", err)
	}

	err = runner.Run(ctx, line)
	return err
}

// exec executes commands using a cross-platform shell interpreter.
func (s *Shell) exec(ctx context.Context, command string) (string, string, error) {
	stdout, stderr := newCappedBuffer(maxKeptOutput), newCappedBuffer(maxKeptOutput)
	err := s.execCommon(ctx, command, stdout, stderr)
	return stdout.String(), stderr.String(), err
}

// execStream executes commands using POSIX shell emulation 用 streaming output
func (s *Shell) execStream(ctx context.Context, command string, stdout, stderr io.Writer) error {
	return s.execCommon(ctx, command, stdout, stderr)
}

// IsInterrupt checks 如果 错误 是 due 到 interruption
func IsInterrupt(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// ExitCode extracts exit code 从 错误
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := errors.AsType[interp.ExitStatus](err); ok {
		return int(exitErr)
	}
	return 1
}

// ContainsCommandChaining reports whether command does anything beyond
// running single simple command 用 redirections: pipelines, `&&`/`||`,
// `;`, backgrounding, subshells, 和 command 或 process substitution all
// count.
//
// Callers use it 到 decide whether command 是 simple enough 为 它的 leading
// words 到 是 matched against safe-command list. Matching prefix 是 只
// meaningful 当 prefix 是 whole command, 所以 anything 那 can smuggle
// second command past prefix has 到 报告 true.
//
// Redirection alone (`ls > out`, `ls &> /dev/null`) 是 不 chaining: it changes
// where output goes, 不 which commands 运行.
//
// Unparseable input reports true: 如果 we cannot tell what command does, we
// do 不 get 到 调用 it simple. Note 这 differs 从 [CheckCommand], which
// reports nothing 为 unparseable input, because 那 answers different
// question — such command never runs, 所以 there 是 no danger 到 name.
func ContainsCommandChaining(command string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return true
	}
	if len(file.Stmts) == 0 {
		return false
	}
	if len(file.Stmts) > 1 {
		return true
	}

	stmt := file.Stmts[0]
	// `ls &` backgrounds ls 和 lets whatever follows 运行 在 它的 own;
	// parser reports it 作为 one statement, 所以 check flag directly.
	if stmt.Background || stmt.Negated {
		return true
	}
	// Anything 那 是 不 plain 调用 (pipelines, `&&`, subshells, blocks,
	// loops, conditionals, function definitions) can 运行 more than one thing.
	if _, ok := stmt.Cmd.(*syntax.CallExpr); !ok {
		return true
	}

	// 一个 substitution anywhere 在 arguments 或 redirections runs command
	// whose 文本 we cannot see.
	chained := false
	syntax.Walk(stmt, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			chained = true
			return false
		}
		return !chained
	})
	return chained
}
