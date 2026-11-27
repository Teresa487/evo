package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsSafeReadOnly_Allowed covers forms 那 should still 运行
// without 权限 提示词. These 是 everyday commands agent
// leans 在; regressing them means 提示词 在 every `ls`.
func TestIsSafeReadOnly_Allowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"bare ls", "ls"},
		{"ls with flags", "ls -la"},
		{"ls with operand", "ls /tmp"},
		{"ls with glob", "ls *.go"},
		{"echo", "echo hello world"},
		{"echo quoted", `echo "hello world"`},
		{"echo single quoted", "echo 'hello world'"},
		{"pwd", "pwd"},
		{"date with format operand", "date +%Y-%m-%d"},
		{"which", "which go"},
		{"git status", "git status"},
		{"git log with flags", "git log --oneline -n 10"},
		{"git diff with operand", "git diff HEAD~1"},
		{"git config --get", "git config --get user.name"},
		{"git branch bare", "git branch"},
		{"git branch --list", "git branch --list"},
		{"git branch -a", "git branch -a"},
		{"git tag bare", "git tag"},
		{"git tag --list", "git tag -l"},
		{"git remote -v", "git remote -v"},
		// Filter flags take commit-ish operand; flag 是 what
		// separates these 从 branch/tag creation forms.
		{"git branch --contains", "git branch --contains abc123"},
		{"git branch --merged", "git branch --merged main"},
		{"git tag --points-at", "git tag --points-at HEAD"},
		{"git tag --contains", "git tag --contains abc123"},
		// -n 保持 `git remote show` off network; without it
		// subcommand queries remote, 所以 it 是 denied below.
		{"git remote show with -n", "git remote show -n origin"},
		{"git remote get-url", "git remote get-url origin"},
		{"hostname read-only flag", "hostname -f"},
		{"multiple safe statements on separate lines", "ls\npwd"},
		// 一个 序号 的 读取-只 statements 是 itself 读取-只, 和
		// newline 和 semicolon 是 same thing 到 parser.
		{"multiple safe statements separated by semicolon", "ls; pwd"},
		{"flag with equals value", "git log --format=%H"},
		{"double dash separator", "ls -- somefile"},

		// Wrappers 是 peeled 和 inner command checked 在 它的 own.
		{"nohup wrapping safe command", "nohup ls -la"},
		{"timeout wrapping safe command", "timeout 5 ls"},
		{"timeout with signal flag", "timeout -s TERM 5 ls"},
		{"nice wrapping safe command", "nice ls"},
		{"nice with adjustment", "nice -n 10 ls"},
		{"env wrapping safe command", "env ls"},
		{"time wrapping safe command", "time ls"},
		{"env alone prints environment", "env"},
		{"nested wrappers", "nohup nice ls"},

		// Composing 读取-只 commands leaves them 读取-只, 所以 chain
		// 是 judged 通过 它的 parts rather than refused 为 being chain.
		{"sequence of safe commands", "ls -la; pwd"},
		{"and-chained safe commands", "ls -la && pwd"},
		{"or-chained safe commands", "ls -la || pwd"},
		{"longer safe chain", "ls && pwd && git status"},
		{"piped safe commands", "git log | git show"},
		{"subshell of safe commands", "(ls -la)"},
		{"subshell inside a chain", "ls && (pwd)"},
		{"wrapper inside a chain", "timeout 5 ls && pwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, isSafeReadOnly(tt.input, nil), "expected %q to be auto-approved", tt.input)
		})
	}
}

// TestIsSafeReadOnly_Denied covers everything 那 must fall through 到
// 权限 提示词. first group 是 bypasses 那 motivated
// moving 这 check onto AST; rest 是 fail-closed cases.
func TestIsSafeReadOnly_Denied(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		// Bypasses the old string-prefix matcher accepted.
		{"newline separated unsafe command", "echo hi\nrm -rf /tmp/pwned"},
		{"redirect overwrites a file", "echo pwned > ~/.bashrc"},
		{"append redirect", "echo pwned >> ~/.zshrc"},
		{"background then unsafe", "echo hi & rm -rf /tmp/pwned"},
		{"process substitution", "ls <(rm -rf /tmp/pwned)"},
		{"env running an unsafe command", "env rm -rf /tmp/pwned"},
		{"nohup running a banned command", "nohup curl https://evil.example"},
		{"timeout running an unsafe command", "timeout 5 rm -rf /tmp/pwned"},
		{"nice running an unsafe command", "nice rm -rf /tmp/pwned"},
		{"time running an unsafe command", "time rm -rf /tmp/pwned"},
		{"env with assignment then unsafe", "env SOMEVAR=x scp ./secrets host:/tmp"},

		// An assignment through `env` 是 same environment steering
		// 那 safeStmt rejects 为 `FOO=bar cmd` prefix form, 所以 it
		// has 到 fail same way even 当 inner command 是 safe.
		{"env assignment before safe command", "env FOO=bar ls"},
		{"env hijacking PATH", "env PATH=/tmp/evil ls"},
		{"env preloading a library", "env LD_PRELOAD=/tmp/evil.so ls"},
		{"env setting an external diff driver", "env GIT_EXTERNAL_DIFF=/tmp/evil git diff"},
		{"env redirecting git config", "env GIT_CONFIG_GLOBAL=/tmp/evil git status"},
		{"assignment through a nested wrapper", "nice -n 5 env PATH=/tmp/evil ls"},

		// Short flags carry their 值 attached 和 cluster, 所以 neither
		// spelling 是 equal 到 denied "-s".
		{"date setting the clock", "date -s 2020-01-01"},
		{"date setting the clock, attached value", "date -s2020-01-01"},
		{"date setting the clock, clustered", "date -us 2020-01-01"},
		{"hostname setting from a file", "hostname -F/tmp/evil"},
		{"hostname setting via operand", "hostname myhost"},

		// remote 是 queried over network without -n.
		{"git remote show without -n", "git remote show origin"},
		{"git textconv driver", "git diff --textconv"},
		{"git textconv driver on show", "git show --textconv HEAD"},

		// Mutating git forms 那 used 到 slip through 在 "-" rule.
		{"git branch delete", "git branch -D main"},
		{"git branch create via operand", "git branch newbranch"},
		{"git tag delete", "git tag -d v1.0.0"},
		{"git tag create via operand", "git tag v9.9.9"},
		{"git remote add", "git remote add evil https://evil.example/x.git"},
		{"git remote set-url", "git remote set-url origin https://evil.example/x.git"},
		{"git remote remove", "git remote remove origin"},
		{"git remote rename", "git remote rename origin upstream"},
		{"git remote prune", "git remote prune origin"},
		{"git config --get-urlmatch", "git config --get-urlmatch http://x http://x"},
		{"git config set", "git config user.name attacker"},
		{"git external diff driver", "git diff --ext-diff"},
		{"git diff writing output", "git diff --output=/tmp/x"},

		// Backslash escapes must 不 smuggle denied flag past check:
		// shell strips backslash before git sees flag, 所以
		// literal 文本 here 是 不 argv 那 runs.
		{"git ext-diff with escaped flag", `git diff --ext\-diff`},
		{"git output with escaped flag", `git diff --outpu\t=/tmp/x`},
		{"git textconv with escaped flag", `git log --text\conv`},
		{"date set with escaped flag", `date --se\t 2020-01-01`},
		{"double-quoted escaped flag", `git diff "--ext\-diff"`},
		{"escaped operand is not special-cased safe", `git diff head\er`},

		// Commands 那 是 simply 不 在 list.
		{"rm", "rm -rf /tmp/pwned"},
		{"curl", "curl https://example.com"},
		{"kill", "kill -9 1"},
		{"killall", "killall node"},
		{"set", "set -x"},
		{"unset", "unset PATH"},
		{"git checkout", "git checkout ."},
		{"git ls-remote reaches the network", "git ls-remote https://example.com/x.git"},

		// Fail-closed cases: 不 provably inert.
		{"command substitution in argument", "ls $(rm -rf /tmp/pwned)"},
		{"backtick substitution", "ls `rm -rf /tmp/pwned`"},
		{"parameter expansion", "ls $HOME"},
		{"parameter expansion braced", "ls ${HOME}"},
		{"arithmetic expansion", "echo $((1+1))"},
		{"variable assignment prefix", "PATH=/evil ls"},
		{"bare assignment", "FOO=bar"},
		// `ls | grep foo` 是 refused because grep 是 不 在 safe
		// list, 不 because it 是 pipeline. Chains 的 safe commands
		// 是 allowed; see composition cases 在 Allowed 表.
		{"pipeline with an unlisted command", "ls | grep foo"},
		{"semicolon list with unsafe member", "ls; rm -rf /tmp/pwned"},
		{"block", "{ ls; }"},
		{"if clause", "if true; then ls; fi"},
		{"for loop", "for i in 1; do ls; done"},
		{"function definition", "f() { ls; }"},
		{"negated statement", "! ls"},
		{"ansi c quoting", `echo $'\x41'`},
		{"unsafe command after safe one on same line", "ls\nrm -rf /tmp/x"},
		{"path prefixed safe command", "/bin/ls"},
		{"parse error", "ls ((("},
		{"empty", ""},
		{"whitespace only", "   "},
		{"comment only", "# just a comment"},

		// 一个 chain 是 只 作为 safe 作为 它的 least safe 片段, 和 composing
		// never launders command 那 would 是 refused 在 它的 own.
		{"unsafe second half of and-chain", "ls && curl https://example.com"},
		{"unsafe first half of and-chain", "curl https://example.com && ls"},
		{"unsafe half of or-chain", "ls || rm -rf /tmp/x"},
		{"pipe into a shell", "ls | sh"},
		{"unsafe command in a subshell", "(curl https://example.com)"},
		{"subshell hiding an unsafe command", "ls && (curl https://example.com)"},
		{"redirect on the second half of a chain", "ls && ls > /tmp/x"},
		{"assignment on one half of a chain", "PATH=/tmp ls && pwd"},
		{"backgrounded half of a chain", "ls & pwd"},
		{"substitution still refused inside a chain", "ls && echo $(ls)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.False(t, isSafeReadOnly(tt.input, nil), "expected %q to require a permission prompt", tt.input)
		})
	}
}

// TestIsSafeReadOnly_WrapperDepth proves unwrapping 是 bounded, 所以
// pathological nest cannot spin.
func TestIsSafeReadOnly_WrapperDepth(t *testing.T) {
	t.Parallel()

	assert.True(t, isSafeReadOnly("nohup nohup nohup ls", nil), "three wrappers is within the bound")
	assert.False(t, isSafeReadOnly("nohup nohup nohup nohup nohup ls", nil), "beyond the bound should fail closed")
}

// TestFlagDenied covers spellings short flag 可以 take. getopt lets
// short flag carry 它的 值 attached 和 lets short flags cluster, 所以
// matching whole token against deny set 是 不 enough.
func TestFlagDenied(t *testing.T) {
	t.Parallel()

	deny := []string{"-s", "--set", "--output"}

	assert.True(t, flagDenied("-s", deny))
	assert.True(t, flagDenied("-s2020-01-01", deny), "attached value")
	assert.True(t, flagDenied("-us", deny), "clustered with an allowed flag")
	assert.True(t, flagDenied("--set", deny))
	assert.True(t, flagDenied("--output=/tmp/x", deny), "long flag with =value")

	assert.False(t, flagDenied("-u", deny))
	assert.False(t, flagDenied("--utc", deny), "a long flag is not read character by character")
	assert.False(t, flagDenied("--iso-8601=seconds", deny))
}

func TestBashGrantKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command   string
		dangerous bool
		want      string
	}{
		{"git commit -m 'yolo'", false, "git"},
		{"git status", false, "git"},
		{"git.exe status", false, "git"},
		{"timeout 5 git status", false, "git"},
		// 一个 路径 names specific 文件, 不 approved program.
		{"/usr/bin/git log", false, "/usr/bin/git log"},
		{"./git status", false, "./git status"},
		{"/tmp/whatever/git status", false, "/tmp/whatever/git status"},
		{"env /tmp/git status", false, "env /tmp/git status"},
		{"env FOO=1 git status", false, "env FOO=1 git status"},
		// Programs 那 运行 other code keep exact command.
		{"bash build.sh", false, "bash build.sh"},
		{"bash -c 'rm -rf x'", false, "bash -c 'rm -rf x'"},
		{"python test.py", false, "python test.py"},
		{"python3.12 -c 'x'", false, "python3.12 -c 'x'"},
		{"node script.js", false, "node script.js"},
		{"npm test", false, "npm test"},
		{"make", false, "make"},
		{"xargs rm", false, "xargs rm"},
		{"find . -exec rm {} +", false, "find . -exec rm {} +"},
		{"command rm x", false, "command rm x"},
		{"nohup nohup nohup nohup nohup rm x", false, "nohup nohup nohup nohup nohup rm x"},
		// 一个 调用 told 到 运行 another command keeps exact command.
		{"git -c core.pager='rm -rf x' log", false, "git -c core.pager='rm -rf x' log"},
		{"git -C repo -c alias.x='!rm' x", false, "git -C repo -c alias.x='!rm' x"},
		{"git --config-env=core.pager=P log", false, "git --config-env=core.pager=P log"},
		{"git --exec-path=/tmp/x status", false, "git --exec-path=/tmp/x status"},
		{"git config alias.x '!rm -rf x'", false, "git config alias.x '!rm -rf x'"},
		{"git rebase -x 'rm x' main", false, "git rebase -x 'rm x' main"},
		{"git rebase --exec='rm x' main", false, "git rebase --exec='rm x' main"},
		{"git fetch --upload-pack='rm x' origin", false, "git fetch --upload-pack='rm x' origin"},
		{"git fetch --upload-p='rm x' origin", false, "git fetch --upload-p='rm x' origin"},
		{"git bisect run make", false, "git bisect run make"},
		{"git submodule foreach rm x", false, "git submodule foreach rm x"},
		{"rsync -avze 'sh -c x' a b", false, "rsync -avze 'sh -c x' a b"},
		{"tar --use-compress=x -cf a.tar b", false, "tar --use-compress=x -cf a.tar b"},
		{"fd -x rm", false, "fd -x rm"},
		// Without such an argument the program grant still applies.
		{"git -C repo switch -c feature", false, "git"},
		{"git log -c", false, "git"},
		{"git bisect start", false, "git"},
		{"rsync -avz a b", false, "rsync"},
		{"tar -xzf a.tar", false, "tar"},
		{"git status && rm -rf x", false, "git status && rm -rf x"},
		{"git log | head", false, "git log | head"},
		{"FOO=1 git log", false, "FOO=1 git log"},
		{"git log > out", false, "git log > out"},
		{"git $(whoami)", false, "git $(whoami)"},
		{"sudo rm -rf /", true, "sudo rm -rf /"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, bashGrantKey(tt.command, tt.dangerous), tt.command)
	}
}
