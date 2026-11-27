package hooks

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/shell"
)

// abandonGrace 是 how long runOne waits after ctx cancellation 为
// shell goroutine 到 yield before returning control 到 caller 和
// letting goroutine finish 在 它的 own. Mirrors historical
// cmd.WaitDelay = time.Second behavior 的 previous os/exec 路径.
const abandonGrace = time.Second

// runShell 是 shell executor used 通过 runOne. 它 是 package-level
// variable 所以 tests can substitute blocking 或 non-yielding
// implementation 到 exercise abandon-在-超时 路径 without
// depending 在 scheduling behavior 的 real interpreter.
var runShell = shell.Run

// Source returns current 钩子 configuration keyed 通过 事件 name.
// runner calls it 在 every 事件, 所以 source backed 通过 配置
// 存储 picks up reloads 用 no extra wiring.
type Source func() map[string][]HookConfig

// Static returns Source 那 always yields same hooks.
func Static(hooks map[string][]HookConfig) Source {
	return func() map[string][]HookConfig { return hooks }
}

// Runner executes 钩子 commands 和 aggregates their results. One
// Runner serves every 事件 为 application. 一个 nil Runner 是 valid
// 和 runs nothing, 所以 callers never have 到 guard against it.
type Runner struct {
	source     Source
	cwd        string
	projectDir string

	// coreutils returns mode 钩子 commands 运行 用, 所以 工作区's
	// configuration (和 它的 reloads) apply without extra wiring. nil
	// falls back 到 shell 默认.
	coreutils func() shell.CoreutilsMode

	matchersMu sync.Mutex
	matchers   map[string]*regexp.Regexp // nil value: pattern did not compile
}

// RunnerOption configures Runner created 通过 [NewRunner].
type RunnerOption func(*Runner)

// WithCoreutils returns RunnerOption 那 resolves coreutils mode
// through src 在 every 钩子 运行.
func WithCoreutils(src func() shell.CoreutilsMode) RunnerOption {
	return func(r *Runner) {
		r.coreutils = src
	}
}

// NewRunner creates Runner 那 reads 钩子 configs 从 source.
// Matchers 是 compiled lazily 和 cached 通过 pattern; one 那 fails 到
// compile 是 skipped 用 warning rather than treated 作为
// match-everything. ValidateHooks 是 expected 到 have caught syntax
// errors earlier, 所以 这 是 defense 在 depth.
func NewRunner(source Source, cwd, projectDir string, opts ...RunnerOption) *Runner {
	r := &Runner{
		source:     source,
		cwd:        cwd,
		projectDir: projectDir,
		matchers:   make(map[string]*regexp.Regexp),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// 运行 executes every configured 钩子 为 事件 在 parallel 和
// returns their aggregated 结果. Hooks 用 matcher 只 运行 当
// it matches 事件's 工具 name. Execution failures 是 logged 和
// treated 作为 "no opinion", 所以 结果 是 always usable.
func (r *Runner) Run(ctx context.Context, ev Event) AggregateResult {
	if r == nil {
		return AggregateResult{}
	}
	hooks := r.matchingHooks(ev)
	if len(hooks) == 0 {
		return AggregateResult{}
	}

	envVars := BuildEnv(ev, r.cwd, r.projectDir)
	payload := BuildPayload(ev, r.cwd)

	results := make([]HookResult, len(hooks))
	var wg sync.WaitGroup
	for i, h := range hooks {
		wg.Go(func() {
			results[i] = r.runOne(ctx, h, envVars, payload)
		})
	}
	wg.Wait()

	var toolInput string
	if ev.Tool != nil {
		toolInput = ev.Tool.Input
	}
	agg := aggregate(results, toolInput)
	agg.Hooks = make([]HookInfo, len(hooks))
	for i, h := range hooks {
		agg.Hooks[i] = HookInfo{
			Name:          h.DisplayName(),
			Event:         ev.Name,
			Matcher:       h.Matcher,
			Decision:      results[i].Decision.String(),
			Halt:          results[i].Halt,
			Reason:        results[i].Reason,
			InputRewrite:  results[i].UpdatedInput != "",
			OutputRewrite: results[i].UpdatedOutput != "",
		}
	}
	slog.Info(
		"Hook completed",
		"event", ev.Name,
		"tool", ev.toolName(),
		"hooks", agg.HookCount,
		"decision", agg.Decision.String(),
	)
	return agg
}

// matchingHooks returns hooks configured 为 事件, deduplicated
// 通过 command, keeping 只 those whose matcher accepts 工具 name. 一个
// matcher 在 事件 without 工具 name 是 ignored.
func (r *Runner) matchingHooks(ev Event) []HookConfig {
	seen := make(map[string]bool)
	var matched []HookConfig
	for _, h := range r.source()[ev.Name] {
		if seen[h.Command] {
			continue
		}
		if h.Matcher != "" && ev.Tool != nil {
			re := r.matcher(h)
			if re == nil || !re.MatchString(ev.Tool.Name) {
				continue
			}
		}
		seen[h.Command] = true
		matched = append(matched, h)
	}
	return matched
}

// matcher returns compiled regex 为 钩子's matcher, 或 nil 当
// it does 不 compile.
func (r *Runner) matcher(h HookConfig) *regexp.Regexp {
	r.matchersMu.Lock()
	defer r.matchersMu.Unlock()
	if re, ok := r.matchers[h.Matcher]; ok {
		return re
	}
	re, err := regexp.Compile(h.Matcher)
	if err != nil {
		slog.Warn(
			"Hook matcher failed to compile; skipping hook",
			"matcher", h.Matcher,
			"command", h.Command,
			"error", err,
		)
		re = nil
	}
	r.matchers[h.Matcher] = re
	return re
}

// runOne executes single 钩子 command 和 returns 它的 结果.
//
// Execution goes through Crush's embedded POSIX shell (shell.运行) 所以
// same interpreter, builtins, 和 coreutils 是 visible 到 hooks 作为 到
// bash 工具. BlockFuncs 是 intentionally omitted: hooks 是
// 用户-authored 配置 那 carry same trust 作为 shell alias.
//
// 一个 钩子 那 fails 到 yield after 它的 期限 has passed 是 abandoned
// after abandonGrace 所以 caller never blocks longer than
// 超时 + abandonGrace. Ownership 的 stdout 和 stderr buffers 是
// strictly single-goroutine:
// - before receiving 从 `done`, 只 goroutine writes 到 them;
// - after `done` delivers 值, goroutine 是 finished 和
//     outer frame reads them;
// - 在 abandon 路径, goroutine may still 是 writing 和
// outer frame must 不 touch them again.
func (r *Runner) runOne(parentCtx context.Context, hook HookConfig, envVars []string, payload []byte) HookResult {
	timeout := hook.TimeoutDuration()
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	coreutils := shell.CoreutilsTurtle
	if r.coreutils != nil {
		coreutils = r.coreutils()
	}

	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runShell(ctx, shell.RunOptions{
			Command:   hook.Command,
			Cwd:       r.cwd,
			Env:       envVars,
			Stdin:     bytes.NewReader(payload),
			Stdout:    &stdout,
			Stderr:    &stderr,
			Coreutils: coreutils,
		})
	}()

	var err error
	select {
	case err = <-done:
		// Normal 路径: goroutine has finished, buffers 是 safe 到 读取.
	case <-ctx.Done():
		select {
		case err = <-done:
			// Interpreter yielded within grace period; safe 到 读取.
		case <-time.After(abandonGrace):
			slog.Warn(
				"Hook did not yield after cancel; abandoning goroutine",
				"command", hook.Command,
				"timeout", timeout,
			)
			// goroutine may still 是 writing 到 stdout/stderr; do
			// 不 读取 either 缓冲 below 这 point.
			return HookResult{Decision: DecisionNone}
		}
	}

	if shell.IsInterrupt(err) {
		// Distinguish 超时 从 parent cancellation.
		if parentCtx.Err() != nil {
			slog.Debug("Hook cancelled by parent context", "command", hook.Command)
		} else {
			slog.Warn("Hook timed out", "command", hook.Command, "timeout", timeout)
		}
		return HookResult{Decision: DecisionNone}
	}

	result, hasJSON := parseStdout(stdout.String())
	if err == nil {
		slog.Debug("Hook executed", "command", hook.Command, "decision", result.Decision.String())
		return result
	}

	// Exit 2 blocks action 和 49 halts turn; stderr 是
	// reason, falling back 到 any reason JSON carried. Any other
	// non-zero exit 是 non-blocking 错误, except 那, 作为 在 Claude
	// Code, valid JSON object 在 stdout still counts 和 exit code
	// 是 ignored.
	exitCode := shell.ExitCode(err)
	reason := strings.TrimSpace(stderr.String())
	if reason == "" {
		reason = result.Reason
	}
	switch exitCode {
	case 2:
		if reason == "" {
			reason = "blocked by hook"
		}
		return HookResult{Decision: DecisionDeny, Reason: reason}
	case HaltExitCode:
		if reason == "" {
			reason = "turn halted by hook"
		}
		return HookResult{Decision: DecisionDeny, Halt: true, Reason: reason}
	}
	if hasJSON {
		return result
	}
	slog.Warn(
		"Hook failed with non-blocking error",
		"command", hook.Command,
		"exit_code", exitCode,
		"stderr", strings.TrimSpace(stderr.String()),
		"error", err,
	)
	return HookResult{Decision: DecisionNone}
}
