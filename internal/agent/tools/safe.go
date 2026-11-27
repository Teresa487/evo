package tools

import (
	"runtime"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
	"mvdan.cc/sh/v3/syntax"
)

// safeCommand describes one command form 那 是 读取-只 enough 到 运行
// without 权限 提示词.
//
// argv 是 matched 作为 exact leading token 序号, never 作为 string
// prefix: 条目 为 {"git", "status"} matches command `git status`
// 和 不 `git status-something`, 和 — because tokens come 从
// parsed AST — 不 `git -c core.pager=... status` either, since 那 argv
// begins 用 different tokens.
type safeCommand struct {
	// argv 是 exact leading token 序号 这 条目 matches.
	argv []string
	// restrictFlags limits accepted flags 到 allowFlags. When false,
	// any flag 不 在 denyFlags 是 accepted. 它 exists 所以 那 command
	// whose every flag 是 harmless (`ls`, `df`) does 不 have 到 enumerate
	// them, while one 那 can mutate under flag (`git branch`) does.
	restrictFlags bool
	// allowFlags 是 set 的 accepted flag names 当 restrictFlags 是
	// set. Compared against flag name 只, 所以 `--format=x` 是 matched
	// 通过 条目 的 "--format".
	allowFlags []string
	// denyFlags 是 set 的 rejected flag names 当 restrictFlags 是
	// 不 set.
	denyFlags []string
	// requireFlag demands 那 在 least one flag 是 present. 它 exists
	// 为 `git 配置`, where 读取-只 forms 是 distinguished 从
	// 写入 form 通过 flag rather than 通过 subcommand token:
	// `git 配置 --get x` reads, `git 配置 x y` writes.
	requireFlag bool
	// allowOperands reports whether non-flag arguments may follow argv.
	// 它 是 false 为 commands 那 mutate 当 handed operand, such
	// 作为 `git branch <name>` (creates) 或 `git remote add` (writes).
	allowOperands bool
}

// gitCodeExecFlags 是 flags 那 make otherwise 读取-只 git
// subcommand 运行 program 或 写入 文件: external diff drivers,
// pager handoff, 和 explicit output redirection. They 是 rejected
// everywhere they 是 accepted 作为 flags.
//
// --textconv sits here 为 same reason 作为 --ext-diff: both 运行
// program named 通过 repository's own 配置, 所以 repository 用户
// cloned 但 does 不 control chooses what executes.
var gitCodeExecFlags = []string{
	"--ext-diff",
	"--textconv",
	"--open-files-in-pager",
	"-O",
	"--output",
	"--output-indicator-new",
	"--upload-pack",
	"--receive-pack",
	"--exec",
}

// safeCommands 是 command forms 那 skip 权限 提示词.
//
// bar 为 membership 是 那 command cannot 写入 到
// filesystem, mutate repository 或 system state, execute another program,
// 或 reach network. Anything 那 fails 那 bar — including
// 读取-只 command 那 becomes destructive under flag — either gets
// restricted flag set here 或 stays off list entirely.
//
// Deliberately absent:
//
// - env, nice, nohup, 超时: these execute arbitrary command given
// 到 them. They 是 resolved 通过 shell.ResolveArgv instead, which peels
// them 和 re-checks whatever 是 inside. (`time` needs no 条目: it
// 是 shell keyword, handled 在 safeStmt.)
//   - kill, killall: signal arbitrary processes.
//   - set, unset: mutate shell state.
// - git branch/tag/remote 用 operands, 和 any git subcommand 那
//     writes: see the restricted entries below.
// - git ls-remote, 和 nslookup/ping 在 Windows: these reach
// network, which sits badly beside block list 那 bans curl/wget.
var safeCommands = []safeCommand{
	// Bash builtins 和 core utils. Every flag these 受理 是 读取-只,
	// 所以 they carry no flag restrictions.
	{argv: []string{"cal"}, allowOperands: true},
	// `date -s` sets the system clock.
	{argv: []string{"date"}, denyFlags: []string{"-s", "--set"}, allowOperands: true},
	{argv: []string{"df"}, allowOperands: true},
	{argv: []string{"du"}, allowOperands: true},
	// echo 是 safe 只 because redirections 是 rejected outright; 用
	// redirect it becomes arbitrary 文件 写入. Do 不 add redirect
	// support without revisiting 这 条目.
	{argv: []string{"echo"}, allowOperands: true},
	{argv: []string{"free"}, allowOperands: true},
	{argv: []string{"groups"}, allowOperands: true},
	// hostname's flags 是 enumerated rather than denied, because ones
	// 那 写入 take their 值 attached 到 flag: `hostname -F/etc/x`
	// sets hostname 从 文件 在 single token, 所以 there 是 no
	// operand 为 allowOperands 到 reject. An allow list fails closed 在
	// the whole shape, including clustered forms.
	{
		argv:          []string{"hostname"},
		restrictFlags: true,
		allowFlags: []string{
			"-a", "--alias", "-A", "--all-fqdns", "-d", "--domain",
			"-f", "--fqdn", "--long", "-i", "--ip-address",
			"-I", "--all-ip-addresses", "-s", "--short",
			"-y", "--yp", "--nis",
		},
		allowOperands: false,
	},
	{argv: []string{"id"}, allowOperands: true},
	{argv: []string{"ls"}, allowOperands: true},
	{argv: []string{"printenv"}, allowOperands: true},
	{argv: []string{"ps"}, allowOperands: true},
	{argv: []string{"pwd"}, allowOperands: false},
	{argv: []string{"top"}, allowOperands: true},
	{argv: []string{"type"}, allowOperands: true},
	{argv: []string{"uname"}, allowOperands: true},
	{argv: []string{"uptime"}, allowOperands: true},
	{argv: []string{"whatis"}, allowOperands: true},
	{argv: []string{"whereis"}, allowOperands: true},
	{argv: []string{"which"}, allowOperands: true},
	{argv: []string{"whoami"}, allowOperands: false},

	// Git — 读取-只 porcelain.
	{argv: []string{"git", "blame"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "describe"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "diff"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "grep"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "log"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "ls-files"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "rev-parse"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "shortlog"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "show"}, denyFlags: gitCodeExecFlags, allowOperands: true},
	{argv: []string{"git", "status"}, denyFlags: gitCodeExecFlags, allowOperands: true},

	// Git — 读取-只 forms 的 otherwise-mutating subcommands. These take
	// no operands: `git branch <name>` creates, `git tag <name>` creates,
	// 和 `git remote add|set-url|remove` all 写入 到 配置.
	{
		argv:          []string{"git", "branch"},
		restrictFlags: true,
		allowFlags: []string{
			"-l", "--list", "-a", "--all", "-r", "--remotes",
			"-v", "-vv", "--verbose", "--show-current",
			"--format", "--sort",
		},
		allowOperands: false,
	},
	{
		argv:          []string{"git", "tag"},
		restrictFlags: true,
		allowFlags: []string{
			"-l", "--list", "-n", "--contains", "--no-contains",
			"--merged", "--no-merged", "--points-at",
			"--format", "--sort",
		},
		allowOperands: false,
	},
	// filter flags above take commit-ish operand. Requiring one 的
	// them 到 是 present 是 what keeps `git branch <name>` (creates) 和
	// `git tag <name>` (creates) out, since those carry operand 和 no
	// flag.
	{
		argv:          []string{"git", "branch"},
		restrictFlags: true,
		allowFlags:    []string{"--contains", "--no-contains", "--merged", "--no-merged", "--points-at"},
		requireFlag:   true,
		allowOperands: true,
	},
	{
		argv:          []string{"git", "tag"},
		restrictFlags: true,
		allowFlags:    []string{"--contains", "--no-contains", "--merged", "--no-merged", "--points-at"},
		requireFlag:   true,
		allowOperands: true,
	},
	{
		argv:          []string{"git", "remote"},
		restrictFlags: true,
		allowFlags:    []string{"-v", "--verbose"},
		allowOperands: false,
	},
	// 读取-只 remote subcommands, spelled out 所以 mutating siblings
	// (add, remove, rename, set-url, prune) stay off the list.
	//
	// `git remote show` queries remote over network unless -n 是
	// given, 所以 -n 是 required here — otherwise 这 条目 would admit
	// exactly network access 那 keeps `git ls-remote` off list.
	{
		argv:          []string{"git", "remote", "show"},
		restrictFlags: true,
		allowFlags:    []string{"-n"},
		requireFlag:   true,
		allowOperands: true,
	},
	{argv: []string{"git", "remote", "get-url"}, allowOperands: true},
	// `git 配置 --get <key>` reads one key; bare `--get` prefix used
	// 到 also admit --get-urlmatch 和 friends, 所以 flag 是 matched
	// exactly here.
	{
		argv:          []string{"git", "config"},
		restrictFlags: true,
		allowFlags:    []string{"--get", "--get-all", "--list", "-l"},
		requireFlag:   true,
		allowOperands: true,
	},

	// `env` 用 no command 到 运行 just prints environment.
	// wrapper peel below 只 fires 当 there 是 inner command, 所以
	// 这 条目 是 reached exactly 当 there 是 不.
	{argv: []string{"env"}, restrictFlags: true, allowOperands: false},
}

func init() {
	if runtime.GOOS == "windows" {
		safeCommands = append(
			safeCommands,
			// Windows-specific 读取-只 commands. nslookup 和 ping 是
			// deliberately excluded: they reach the network.
			safeCommand{argv: []string{"ipconfig"}, allowOperands: true},
			safeCommand{argv: []string{"systeminfo"}, allowOperands: true},
			safeCommand{argv: []string{"tasklist"}, allowOperands: true},
			safeCommand{argv: []string{"where"}, allowOperands: true},
		)
	}
}

// isSafeReadOnly reports whether command consists entirely 的 读取-只
// commands 那 may 运行 without 权限 提示词.
//
// 它 parses command rather than matching against 它的 文本. Matching
// 在 文本 cannot see difference between `echo hi` 和
// `echo pwned > ~/.bashrc`, treats newline 作为 ordinary whitespace, 和
// has no way 到 tell `git branch` 从 `git branch -D main`.
//
// analysis fails closed 在 every step. 一个 parse 错误, unrecognized
// node type, redirection, background 或 negated statement, variable
// assignment, word 那 是 不 fully literal, 或 argument 那 does
// 不 match [safeCommand] 条目 all 结果 在 false — which costs
// 用户 权限 提示词 和 nothing more.
func isSafeReadOnly(command string, extra []safeCommand) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return false
	}
	if len(file.Stmts) == 0 {
		return false
	}
	return safeStmts(file.Stmts, extra)
}

// bashGrantKey returns what "allow 为 会话" covers 为 command. 一个
// single plain command 运行 通过 bare name 是 keyed 通过 它的 program name, 所以
// approving `git commit -m x` approves every later `git` 调用. Anything else
// (lists, pipes, redirections, env assignments, non-literal words, 路径
// such 作为 ./git, program 那 runs other code such 作为 bash 或 npm, 调用
// told 到 运行 another command such 作为 `git -c core.pager=...`, 或 command
// flagged dangerous) 是 keyed 通过 exact command 文本, 所以 one approval
// cannot widen into something 用户 never saw.
func bashGrantKey(command string, dangerous bool) string {
	if dangerous {
		return command
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return command
	}
	stmt := file.Stmts[0]
	if len(stmt.Redirs) > 0 || stmt.Background || stmt.Coprocess || stmt.Negated || stmt.Disown {
		return command
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(call.Args) == 0 {
		return command
	}
	argv, ok := literalArgs(call.Args)
	if !ok {
		return command
	}
	argv = shell.ResolveArgv(argv)
	if len(argv) == 0 {
		return command
	}
	// Only bare program name collapses. 一个 路径 such 作为 ./git 或
	// /tmp/x/git names specific 文件, which may 是 script 在 repo
	// rather than program 用户 approved.
	name := argv[0]
	if name == "" || strings.ContainsAny(name, `/\=`) || strings.HasPrefix(name, "-") {
		return command
	}
	name = shell.NormalizeCommandName(name)
	if runsArbitraryCode(name) || runsCodeViaArgs(name, argv[1:]) {
		return command
	}
	return name
}

// codeRunnerFlags 是 flags 那 make otherwise ordinary program 运行
// command it was handed, such 作为 `rsync -e 'sh -c ...'`.
var codeRunnerFlags = map[string][]string{
	"rsync": {"-e", "--rsh", "--rsync-path"},
	"tar":   {"-I", "-F", "--use-compress-program", "--to-command", "--checkpoint-action", "--info-script", "--new-volume-script"},
	"fd":    {"-x", "-X", "--exec", "--exec-batch"},
	"rg":    {"--pre"},
	"sort":  {"--compress-program"},
	"scp":   {"-S"},
}

// gitRunFlags 是 git subcommand flags 那 运行 program: ones already
// kept off safe list, plus rebase's 和 difftool's command options.
var gitRunFlags = append(slices.Clone(gitCodeExecFlags), "-x", "--extcmd")

// gitGlobalRunFlags 是 git options, given before subcommand, 那 set
// 配置 或 helper 目录, either 的 which can name program 到 运行.
var gitGlobalRunFlags = []string{"-c", "--config-env", "--exec-path"}

// gitGlobalValueFlags 是 git options 那 take next word 作为 值.
var gitGlobalValueFlags = []string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--super-prefix", "--attr-source"}

// runsCodeViaArgs reports whether program 那 是 ordinarily fine 到
// approve 为 会话 has been told 通过 它的 arguments 到 运行 another
// command, 所以 approving program must 不 approve 这 调用.
func runsCodeViaArgs(name string, args []string) bool {
	if name != "git" {
		return hasRunFlag(args, codeRunnerFlags[name])
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if isFlag(arg) {
			if slices.Contains(gitGlobalRunFlags, flagName(arg)) {
				return true
			}
			if slices.Contains(gitGlobalValueFlags, arg) {
				i++
			}
			continue
		}
		rest := args[i+1:]
		switch arg {
		// 配置 can define aliases, pagers 和 hooks 那 运行 commands,
		// 和 tools below 运行 commands 通过 design.
		case "config", "filter-branch", "difftool", "mergetool":
			return true
		case "bisect":
			return slices.Contains(rest, "run")
		case "submodule":
			return slices.Contains(rest, "foreach")
		}
		return hasRunFlag(rest, gitRunFlags)
	}
	return false
}

// hasRunFlag reports whether any flag 在 args 是, 或 abbreviates, one 的
// 运行. getopt 和 git both 受理 unambiguous prefix 的 long option, 所以
// `--use-compress=x` means `--use-compress-program=x`.
func hasRunFlag(args, run []string) bool {
	for _, arg := range args {
		if !isFlag(arg) {
			continue
		}
		if flagDenied(arg, run) {
			return true
		}
		name := flagName(arg)
		if strings.HasPrefix(name, "--") && slices.ContainsFunc(run, func(r string) bool { return strings.HasPrefix(r, name) }) {
			return true
		}
	}
	return false
}

// codeRunners 是 programs whose arguments 是 themselves code: approving
// `bash build.sh` 或 `npm 测试` must 不 approve `bash -c '<anything>'`, 所以
// these keep the exact-command grant key.
var codeRunners = []string{
	// Shells 和 shell builtins 那 运行 their arguments.
	"sh", "bash", "zsh", "dash", "ksh", "mksh", "csh", "tcsh", "fish",
	"pwsh", "powershell", "cmd", "busybox",
	"eval", "exec", "source", ".", "command", "builtin", "trap",
	// Interpreters 和 runtimes.
	"python", "python2", "python3", "pypy", "pypy3", "node", "nodejs",
	"deno", "bun", "ruby", "irb", "perl", "php", "lua", "luajit", "tclsh",
	"osascript", "java", "jshell", "groovy", "scala", "kotlin", "R",
	"rscript", "julia", "awk", "gawk", "mawk", "nawk", "sed", "jq",
	"go", "cargo", "dotnet", "swift",
	// Package managers 和 task runners 那 运行 project scripts.
	"npm", "npx", "pnpm", "pnpx", "yarn", "uv", "uvx", "pip", "pip3",
	"pipx", "poetry", "bundle", "rake", "gradle", "gradlew", "mvn",
	"make", "gmake", "just", "task", "ninja", "cmake",
	// Programs 那 运行 another command given 作为 argument.
	"xargs", "find", "parallel", "watch", "time", "sudo", "doas", "su",
	"ssh", "env", "nohup", "nice", "timeout", "strace", "ltrace", "gdb",
	"script", "flock", "chroot", "nsenter", "unshare", "setsid",
	"stdbuf", "systemd-run", "docker", "podman", "kubectl",
}

// runsArbitraryCode reports whether name 是 [codeRunners] 条目, allowing
// 为 version suffix such 作为 python3.12 或 node18.
func runsArbitraryCode(name string) bool {
	if slices.ContainsFunc(codeRunners, func(r string) bool { return strings.EqualFold(r, name) }) {
		return true
	}
	base := strings.TrimRight(name, "0123456789.-")
	return base != name && base != "" &&
		slices.ContainsFunc(codeRunners, func(r string) bool { return strings.EqualFold(r, base) })
}

// safeStmts reports whether every statement 在 list 是 safe. An empty list
// 是 不: command 那 resolves 到 nothing 是 不 something 到 wave
// through, 和 it 是 shape parse surprise tends 到 take.
func safeStmts(stmts []*syntax.Stmt, extra []safeCommand) bool {
	if len(stmts) == 0 {
		return false
	}
	for _, stmt := range stmts {
		if !safeStmt(stmt, extra) {
			return false
		}
	}
	return true
}

// safeStmt reports whether single statement 是 读取-只.
//
// Only bare 调用 expression qualifies. Pipelines, lists, subshells,
// conditionals, loops 和 function definitions 是 all rejected: each
// either composes commands 在 ways 这 analysis does 不 模型, 或 (在
// case 的 pipeline into non-listed command) has no benefit worth
// the added surface.
func safeStmt(stmt *syntax.Stmt, extra []safeCommand) bool {
	if stmt == nil || stmt.Cmd == nil {
		return false
	}
	// 一个 redirection turns 读取-只 command into 文件 写入, 和
	// backgrounding detaches it 从 运行 we 是 about 到 observe.
	if len(stmt.Redirs) > 0 || stmt.Background || stmt.Coprocess || stmt.Negated || stmt.Disown {
		return false
	}
	// `time` 是 shell keyword rather than command, 所以 it arrives 作为
	// 它的 own node. 它 只 measures what it wraps, 所以 defer 到 那.
	if clause, ok := stmt.Cmd.(*syntax.TimeClause); ok {
		return clause.Stmt != nil && safeStmt(clause.Stmt, extra)
	}
	// Composing 读取-只 commands leaves them 读取-只, 所以 chain 是
	// judged 通过 它的 parts: `&&`, `||`, 和 `|` 是 safe exactly 当 both
	// sides 是. Each 片段 keeps 它的 own fixed argv, which 是 what makes
	// 这 sound — unlike command substitution, where one command's output
	// becomes another's arguments 和 could supply flag 那 turns
	// 读取-只 command into writing one. Substitution 是 still refused
	// 通过 literalArgs below.
	if bin, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
		switch bin.Op {
		case syntax.AndStmt, syntax.OrStmt, syntax.Pipe, syntax.PipeAll:
			return safeStmt(bin.X, extra) && safeStmt(bin.Y, extra)
		default:
			return false
		}
	}
	// 一个 subshell changes where commands 运行, 不 what they may do.
	if sub, ok := stmt.Cmd.(*syntax.Subshell); ok {
		return safeStmts(sub.Stmts, extra)
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok {
		return false
	}
	// `FOO=bar cmd` can steer command through 它的 environment
	// (路径, LD_PRELOAD, GIT_*), 所以 it never auto-approves.
	if len(call.Assigns) > 0 || len(call.Args) == 0 {
		return false
	}
	argv, ok := literalArgs(call.Args)
	if !ok {
		return false
	}
	return safeArgv(argv, extra)
}

// safeArgv reports whether fully-literal argv 是 safe command. argv
// 是 first resolved through any command wrapper, 用 same peeling
// deny list applies, 所以 two lists cannot disagree about which command 是
// being 运行.
func safeArgv(argv []string, extra []safeCommand) bool {
	argv = shell.ResolveArgv(argv)
	if len(argv) == 0 {
		return false
	}
	match := func(sc safeCommand) bool { return sc.matches(argv) }
	return slices.ContainsFunc(safeCommands, match) || slices.ContainsFunc(extra, match)
}

// userSafeCommands turns configured safe command lines into entries.
//
// Each 条目 matches 只 exact tokens it was given: no operands 和 no
// flags beyond them, 所以 "go build" covers `go build` 和 不 `go build -o
// /usr/local/bin/thing`. That 是 deliberately stricter than built-在
// entries, which carry hand-checked flag policy per command. 一个 setting 那
// widened itself 到 cover flags nobody reviewed would 是 way 到 hand over
// far more than the reader thought they were granting.
func userSafeCommands(perms *config.Permissions) []safeCommand {
	if perms == nil {
		return nil
	}
	out := make([]safeCommand, 0, len(perms.SafeCommands))
	for _, entry := range perms.SafeCommands {
		argv := strings.Fields(entry)
		if len(argv) == 0 {
			continue
		}
		// 一个 wrapper 在 front would let 条目 stand 在 为 whatever
		// wrapper was handed, 所以 resolve it way every other list does
		// 和 record what actually runs.
		argv = shell.ResolveArgv(argv)
		argv[0] = shell.NormalizeCommandName(argv[0])
		out = append(out, safeCommand{
			argv:          argv,
			restrictFlags: true, // allowFlags is empty, so any flag fails.
		})
	}
	return out
}

// matches reports whether argv 是 instance 的 这 safe command form.
func (sc safeCommand) matches(argv []string) bool {
	if len(argv) < len(sc.argv) || !slices.Equal(argv[:len(sc.argv)], sc.argv) {
		return false
	}
	rest := argv[len(sc.argv):]
	operandsOnly := false
	sawFlag := false
	for _, arg := range rest {
		if arg == "--" {
			operandsOnly = true
			continue
		}
		if !operandsOnly && isFlag(arg) {
			if sc.restrictFlags {
				// Matched exactly: an unrecognized spelling, including a
				// cluster 的 otherwise-allowed short flags, fails closed.
				if !slices.Contains(sc.allowFlags, flagName(arg)) {
					return false
				}
			} else if flagDenied(arg, sc.denyFlags) {
				return false
			}
			sawFlag = true
			continue
		}
		if !sc.allowOperands {
			return false
		}
	}
	return sawFlag || !sc.requireFlag
}

// isFlag reports whether token 是 flag rather than operand. 一个 lone
// "-" 是 conventionally stdin, 和 lone "--" 是 separator; neither 是
// a flag.
func isFlag(tok string) bool {
	return len(tok) > 1 && strings.HasPrefix(tok, "-") && tok != "--"
}

// flagName strips any =值 suffix 从 flag token.
func flagName(tok string) string {
	name, _, _ := strings.Cut(tok, "=")
	return name
}

// flagDenied reports whether tok 是 denied flag, 或 carries one.
//
// 一个 long flag matches 通过 name, 所以 `--output=x` 是 caught 通过 条目 的
// "--output". 一个 short flag needs more than 那: getopt accepts 它的 值
// attached (`date -s2020-01-01`) 和 accepts clusters (`date -us`), 所以
// neither spelling 是 equal 到 "-s". Every character 的 single-dash
// token 是 therefore checked against deny set.
func flagDenied(tok string, deny []string) bool {
	if slices.Contains(deny, flagName(tok)) {
		return true
	}
	if strings.HasPrefix(tok, "--") {
		return false
	}
	for _, r := range tok[1:] {
		if slices.Contains(deny, "-"+string(r)) {
			return true
		}
	}
	return false
}

// literalArgs converts parsed words 到 plain strings, reporting false 如果
// any word 是 不 entirely literal.
func literalArgs(words []*syntax.Word) ([]string, bool) {
	out := make([]string, 0, len(words))
	for _, word := range words {
		lit, ok := literalWord(word)
		if !ok {
			return nil, false
		}
		out = append(out, lit)
	}
	return out, true
}

// literalWord flattens word 到 它的 literal 文本, reporting false 如果 any
// 片段 的 it 是 resolved 在 runtime.
//
// Command substitution, parameter expansion, arithmetic expansion 和
// process substitution 是 all rejected rather than evaluated: their
// 值 是 不 knowable here, 和 `ls $(rm -rf /)` must never 是 treated
// 作为 `ls`. Glob characters 是 left alone — they 是 expanded 通过
// shell against filesystem 和 cannot introduce new command.
//
// 一个 literal containing backslash 是 also rejected. syntax.Lit.值 keeps
// unquoted escape sequences verbatim, 所以 文本 here 是 不 argv
// shell will pass: `--ext\-diff` reads 作为 denied flag `--ext-diff` 只
// after the shell strips the backslash, which would let a protected flag
// slip past denyFlags. Failing closed 在 any backslash 是 cheaper than
// re-implementing the shell's escape rules.
func literalWord(word *syntax.Word) (string, bool) {
	var sb strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if strings.Contains(p.Value, `\`) {
				return "", false
			}
			sb.WriteString(p.Value)
		case *syntax.SglQuoted:
			// $'…' applies escape sequences, 所以 值 是 不 final
			// 文本; 只 plain '…' 是 taken 在 face 值.
			if p.Dollar {
				return "", false
			}
			sb.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return "", false
			}
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				// Double-quoted literals keep backslash escapes verbatim
				// too; reject them 为 same reason 作为 bare literals.
				if strings.Contains(lit.Value, `\`) {
					return "", false
				}
				sb.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return sb.String(), true
}
