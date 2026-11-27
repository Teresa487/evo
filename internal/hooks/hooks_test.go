package hooks

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

// newTestRunner creates Runner 从 slice 的 HookConfigs 为
// PreToolUse 事件. Convenience wrapper 为 tests 那 don't care
// about 事件 routing.
func newTestRunner(t *testing.T, cfgs []HookConfig) *Runner {
	t.Helper()
	return NewRunner(Static(map[string][]HookConfig{EventPreToolUse: cfgs}), t.TempDir(), t.TempDir())
}

// preToolEvent builds PreToolUse 事件 为 given 工具.
func preToolEvent(tool, input string) Event {
	return Event{Name: EventPreToolUse, SessionID: "sess", Tool: &ToolCall{Name: tool, Input: input}}
}

func TestAggregation(t *testing.T) {
	t.Parallel()

	t.Run("empty results", func(t *testing.T) {
		t.Parallel()
		agg := aggregate(nil, "{}")
		require.Equal(t, DecisionNone, agg.Decision)
		require.Empty(t, agg.Reason)
		require.Empty(t, agg.Context)
		require.False(t, agg.Halt)
	})

	t.Run("single allow", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow},
		}, "{}")
		require.Equal(t, DecisionAllow, agg.Decision)
	})

	t.Run("deny wins over allow", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, Context: "ctx1"},
			{Decision: DecisionDeny, Reason: "blocked"},
		}, "{}")
		require.Equal(t, DecisionDeny, agg.Decision)
		require.Equal(t, "blocked", agg.Reason)
		require.Equal(t, "ctx1", agg.Context)
	})

	t.Run("multiple deny reasons concatenated", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionDeny, Reason: "reason1"},
			{Decision: DecisionDeny, Reason: "reason2"},
		}, "{}")
		require.Equal(t, DecisionDeny, agg.Decision)
		require.Equal(t, "reason1\nreason2", agg.Reason)
	})

	t.Run("context concatenated from all hooks", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, Context: "ctx-a"},
			{Decision: DecisionNone, Context: "ctx-b"},
		}, "{}")
		require.Equal(t, DecisionAllow, agg.Decision)
		require.Equal(t, "ctx-a\nctx-b", agg.Context)
	})

	t.Run("allow wins over none", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionNone},
			{Decision: DecisionAllow},
		}, "{}")
		require.Equal(t, DecisionAllow, agg.Decision)
	})

	t.Run("halt is sticky across results", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow},
			{Halt: true, Reason: "stop now"},
		}, "{}")
		require.True(t, agg.Halt)
		require.Contains(t, agg.Reason, "stop now")
	})

	t.Run("halt with deny only records reason once", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionDeny, Halt: true, Reason: "stop"},
		}, "{}")
		require.True(t, agg.Halt)
		require.Equal(t, DecisionDeny, agg.Decision)
		require.Equal(t, "stop", agg.Reason)
	})
}

func TestParseStdout(t *testing.T) {
	t.Parallel()

	t.Run("empty stdout", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout("")
		require.Equal(t, DecisionNone, r.Decision)
	})

	t.Run("valid allow", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"allow","context":"some context"}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, "some context", r.Context)
	})

	t.Run("valid deny", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"deny","reason":"not allowed"}`)
		require.Equal(t, DecisionDeny, r.Decision)
		require.Equal(t, "not allowed", r.Reason)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{bad json}`)
		require.Equal(t, DecisionNone, r.Decision)
	})

	t.Run("unknown decision", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"maybe"}`)
		require.Equal(t, DecisionNone, r.Decision)
	})

	t.Run("version 1 accepted", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"version":1,"decision":"allow"}`)
		require.Equal(t, DecisionAllow, r.Decision)
	})

	t.Run("unknown higher version still parses", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"version":99,"decision":"deny","reason":"future"}`)
		require.Equal(t, DecisionDeny, r.Decision)
		require.Equal(t, "future", r.Reason)
	})

	t.Run("halt true without decision", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"halt":true,"reason":"turn over"}`)
		require.True(t, r.Halt)
		require.Equal(t, "turn over", r.Reason)
		require.Equal(t, DecisionNone, r.Decision)
	})

	t.Run("context string form", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"allow","context":"one note"}`)
		require.Equal(t, "one note", r.Context)
	})

	t.Run("context array form", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"allow","context":["first","second"]}`)
		require.Equal(t, "first\nsecond", r.Context)
	})

	t.Run("context array drops empty entries", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"allow","context":["","keep",""]}`)
		require.Equal(t, "keep", r.Context)
	})

	t.Run("context null becomes empty", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"allow","context":null}`)
		require.Empty(t, r.Context)
	})
}

func TestBuildEnv(t *testing.T) {
	t.Parallel()

	env := BuildEnv(Event{
		Name:      EventPreToolUse,
		SessionID: "sess-1",
		Tool:      &ToolCall{Name: "bash", Input: `{"command":"ls","file_path":"/tmp/f.txt"}`},
	}, "/work", "/project")

	envMap := make(map[string]string)
	for _, e := range env {
		parts := splitFirst(e, "=")
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	require.Equal(t, EventPreToolUse, envMap["CRUSH_EVENT"])
	require.Equal(t, "bash", envMap["CRUSH_TOOL_NAME"])
	require.Equal(t, "sess-1", envMap["CRUSH_SESSION_ID"])
	require.Equal(t, "/work", envMap["CRUSH_CWD"])
	require.Equal(t, "/project", envMap["CRUSH_PROJECT_DIR"])
	require.Equal(t, "ls", envMap["CRUSH_TOOL_INPUT_COMMAND"])
	require.Equal(t, "/tmp/f.txt", envMap["CRUSH_TOOL_INPUT_FILE_PATH"])

	// Shared Crush markers must 是 present 所以 钩子-authored scripts can
	// detect they're running under Crush same way bash-工具-invoked
	// scripts 可以.
	require.Equal(t, "1", envMap["CRUSH"])
	require.Equal(t, "crush", envMap["AGENT"])
	require.Equal(t, "crush", envMap["AI_AGENT"])
}

func splitFirst(s, sep string) []string {
	before, after, found := strings.Cut(s, sep)
	if !found {
		return []string{s}
	}
	return []string{before, after}
}

func TestBuildPayload(t *testing.T) {
	t.Parallel()

	t.Run("tool event", func(t *testing.T) {
		t.Parallel()
		payload := BuildPayload(Event{Name: EventPreToolUse, SessionID: "sess-1", Tool: &ToolCall{ID: "call-1", Name: "bash", Input: `{"command":"ls"}`}}, "/work")
		s := string(payload)
		require.Contains(t, s, `"event":"`+EventPreToolUse+`"`)
		require.Contains(t, s, `"hook_event_name":"`+EventPreToolUse+`"`)
		require.Contains(t, s, `"tool_name":"bash"`)
		require.Contains(t, s, `"tool_use_id":"call-1"`)
		// tool_input should 是 object, 不 string.
		require.Contains(t, s, `"tool_input":{"command":"ls"}`)
		require.NotContains(t, s, `"tool_response"`)
	})

	t.Run("lifecycle event omits tool fields", func(t *testing.T) {
		t.Parallel()
		s := string(BuildPayload(Event{Name: EventTurnStart, SessionID: "sess-1"}, "/work"))
		require.NotContains(t, s, `"tool_name"`)
		require.NotContains(t, s, `"tool_input"`)
		require.Contains(t, s, `"session_id":"sess-1"`)
	})

	t.Run("empty tool_response is still sent", func(t *testing.T) {
		t.Parallel()
		s := string(BuildPayload(Event{Name: EventPostToolUse, Tool: &ToolCall{Name: "bash", Input: `{}`}, ToolResponse: new("")}, "/work"))
		require.Contains(t, s, `"tool_response":""`)
	})

	t.Run("turn end carries last_assistant_message", func(t *testing.T) {
		t.Parallel()
		s := string(BuildPayload(Event{Name: EventTurnEnd, LastAssistantMessage: new("done")}, "/work"))
		require.Contains(t, s, `"last_assistant_message":"done"`)
	})

	t.Run("occasion fields", func(t *testing.T) {
		t.Parallel()
		s := string(BuildPayload(Event{Name: EventPermissionResult, Tool: &ToolCall{Name: "bash"}, Decision: "granted"}, "/work"))
		require.Contains(t, s, `"decision":"granted"`)
		s = string(BuildPayload(Event{Name: EventPreCompact, Trigger: "auto"}, "/work"))
		require.Contains(t, s, `"trigger":"auto"`)
		s = string(BuildPayload(Event{Name: EventSessionStart, Source: "resume"}, "/work"))
		require.Contains(t, s, `"source":"resume"`)
	})
}

func TestRunnerExitCode0Allow(t *testing.T) {
	t.Parallel()
	hookCfg := HookConfig{
		Command: `echo '{"decision":"allow","context":"ok"}'`,
	}
	r := newTestRunner(t, []HookConfig{hookCfg})
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.Equal(t, DecisionAllow, result.Decision)
	require.Equal(t, "ok", result.Context)
}

func TestRunnerExitCode2Deny(t *testing.T) {
	t.Parallel()
	hookCfg := HookConfig{
		Command: `echo "forbidden" >&2; exit 2`,
	}
	r := newTestRunner(t, []HookConfig{hookCfg})
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.Equal(t, DecisionDeny, result.Decision)
	require.False(t, result.Halt)
	require.Equal(t, "forbidden", result.Reason)
}

func TestRunnerExitCode49Halt(t *testing.T) {
	t.Parallel()
	hookCfg := HookConfig{
		Command: `echo "stop the turn" >&2; exit 49`,
	}
	r := newTestRunner(t, []HookConfig{hookCfg})
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.True(t, result.Halt)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "stop the turn", result.Reason)
}

func TestRunnerHaltViaJSON(t *testing.T) {
	t.Parallel()
	hookCfg := HookConfig{
		Command: `echo '{"halt":true,"reason":"via json"}'`,
	}
	r := newTestRunner(t, []HookConfig{hookCfg})
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.True(t, result.Halt)
	require.Equal(t, "via json", result.Reason)
}

func TestRunnerExitCodeOtherNonBlocking(t *testing.T) {
	t.Parallel()
	hookCfg := HookConfig{
		Command: `exit 1`,
	}
	r := newTestRunner(t, []HookConfig{hookCfg})
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.Equal(t, DecisionNone, result.Decision)
}

func TestRunnerTimeout(t *testing.T) {
	t.Parallel()
	hookCfg := HookConfig{
		Command: `sleep 10`,
		Timeout: 1,
	}
	r := newTestRunner(t, []HookConfig{hookCfg})
	start := time.Now()
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	elapsed := time.Since(start)
	require.Equal(t, DecisionNone, result.Decision)
	require.Less(t, elapsed, 5*time.Second)
}

func TestRunnerDeduplication(t *testing.T) {
	t.Parallel()
	// Two hooks 用 same command should 只 运行 once.
	hookCfg := HookConfig{
		Command: `echo '{"decision":"allow"}'`,
	}
	r := newTestRunner(t, []HookConfig{hookCfg, hookCfg})
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.Equal(t, DecisionAllow, result.Decision)
}

func TestRunnerNoMatchingHooks(t *testing.T) {
	t.Parallel()
	// Hooks 是 empty.
	r := newTestRunner(t, nil)
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.Equal(t, DecisionNone, result.Decision)
}

func TestRunnerMatcherFiltering(t *testing.T) {
	t.Parallel()

	t.Run("compiled regex matches", func(t *testing.T) {
		t.Parallel()
		hooks := []HookConfig{
			{Command: `echo '{"decision":"deny","reason":"blocked"}'`, Matcher: "^bash$"},
		}
		r := newTestRunner(t, hooks)
		result := r.Run(context.Background(), preToolEvent("bash", `{}`))
		require.Equal(t, DecisionDeny, result.Decision)
	})

	t.Run("compiled regex does not match", func(t *testing.T) {
		t.Parallel()
		hooks := []HookConfig{
			{Command: `echo '{"decision":"deny","reason":"blocked"}'`, Matcher: "^edit$"},
		}
		r := newTestRunner(t, hooks)
		result := r.Run(context.Background(), preToolEvent("bash", `{}`))
		require.Equal(t, DecisionNone, result.Decision)
	})

	t.Run("no matcher matches everything", func(t *testing.T) {
		t.Parallel()
		hooks := []HookConfig{
			{Command: `echo '{"decision":"allow"}'`},
		}
		r := newTestRunner(t, hooks)
		result := r.Run(context.Background(), preToolEvent("bash", `{}`))
		require.Equal(t, DecisionAllow, result.Decision)
	})

	t.Run("partial regex match", func(t *testing.T) {
		t.Parallel()
		hooks := []HookConfig{
			{Command: `echo '{"decision":"deny","reason":"mcp blocked"}'`, Matcher: "^mcp_"},
		}
		r := newTestRunner(t, hooks)

		result := r.Run(context.Background(), preToolEvent("mcp_github_get_me", `{}`))
		require.Equal(t, DecisionDeny, result.Decision)

		result = r.Run(context.Background(), preToolEvent("bash", `{}`))
		require.Equal(t, DecisionNone, result.Decision)
	})

	// Runner must compile matchers itself; it cannot rely 在
	// ValidateHooks having 运行 first. 这 是 guarantee 那 prevents
	// reload-drops-matcher class 的 bug.
	t.Run("runner compiles matcher without ValidateHooks", func(t *testing.T) {
		t.Parallel()
		raw := []HookConfig{
			{Command: `echo '{"decision":"deny","reason":"blocked"}'`, Matcher: "^bash$"},
		}
		r := newTestRunner(t, raw)

		deny := r.Run(context.Background(), preToolEvent("bash", `{}`))
		require.Equal(t, DecisionDeny, deny.Decision)

		noop := r.Run(context.Background(), preToolEvent("view", `{}`))
		require.Equal(t, DecisionNone, noop.Decision)
	})

	// 一个 matcher 那 fails 到 compile 在 Runner construction must 不
	// degrade 到 match-everything; 钩子 是 dropped instead.
	t.Run("runner skips hooks with invalid matcher", func(t *testing.T) {
		t.Parallel()
		raw := []HookConfig{
			{Command: `echo '{"decision":"deny","reason":"should not fire"}'`, Matcher: "[invalid"},
		}
		r := newTestRunner(t, raw)

		result := r.Run(context.Background(), preToolEvent("bash", `{}`))
		require.Equal(t, DecisionNone, result.Decision)
		require.Zero(t, result.HookCount)
	})
}

func TestRunnerHookNameUsesDisplayName(t *testing.T) {
	t.Parallel()

	t.Run("name field is used when set", func(t *testing.T) {
		t.Parallel()
		hookCfg := HookConfig{
			Name:    "my-hook",
			Command: `echo '{"decision":"allow"}'`,
		}
		r := newTestRunner(t, []HookConfig{hookCfg})
		result := r.Run(context.Background(), preToolEvent("bash", `{}`))
		require.Equal(t, DecisionAllow, result.Decision)
		require.Len(t, result.Hooks, 1)
		require.Equal(t, "my-hook", result.Hooks[0].Name)
	})

	t.Run("command is used when name is empty", func(t *testing.T) {
		t.Parallel()
		hookCfg := HookConfig{
			Command: `echo '{"decision":"allow"}'`,
		}
		r := newTestRunner(t, []HookConfig{hookCfg})
		result := r.Run(context.Background(), preToolEvent("bash", `{}`))
		require.Equal(t, DecisionAllow, result.Decision)
		require.Len(t, result.Hooks, 1)
		require.Equal(t, `echo '{"decision":"allow"}'`, result.Hooks[0].Name)
	})
}

func TestRunnerParallelExecution(t *testing.T) {
	t.Parallel()
	// Two hooks: one allows, one denies. Deny 应当 win.
	hooks := []HookConfig{
		{Command: `echo '{"decision":"allow","context":"hook1"}'`},
		{Command: `echo '{"decision":"deny","reason":"nope"}' ; exit 0`},
	}
	r := newTestRunner(t, hooks)
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "nope", result.Reason)
}

func TestRunnerEnvVarsPropagated(t *testing.T) {
	t.Parallel()
	hookCfg := HookConfig{
		Command: `printf '{"decision":"allow","context":"%s"}' "$CRUSH_TOOL_NAME"`,
	}
	r := newTestRunner(t, []HookConfig{hookCfg})
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.Equal(t, DecisionAllow, result.Decision)
	require.Equal(t, "bash", result.Context)
}

func TestParseStdoutUpdatedInput(t *testing.T) {
	t.Parallel()

	t.Run("nested object", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"allow","updated_input":{"command":"rtk cat foo.go"}}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, `{"command":"rtk cat foo.go"}`, r.UpdatedInput)
	})

	t.Run("stringified backward compat", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"allow","updated_input":"{\"command\":\"rtk cat foo.go\"}"}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, `{"command":"rtk cat foo.go"}`, r.UpdatedInput)
	})

	t.Run("no updated_input", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"allow"}`)
		require.Empty(t, r.UpdatedInput)
	})
}

func TestAggregationUpdatedInput(t *testing.T) {
	t.Parallel()

	t.Run("patches merge in config order with later overriding", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `{"command":"first","keep":"me"}`},
			{Decision: DecisionAllow, UpdatedInput: `{"command":"second"}`},
		}, `{"command":"orig","timeout":60}`)
		require.Equal(t, DecisionAllow, agg.Decision)
		// command overridden 通过 second patch; keep preserved 从 first
		// patch; 超时 preserved 从 original input.
		require.JSONEq(
			t,
			`{"command":"second","keep":"me","timeout":60}`,
			agg.UpdatedInput,
		)
	})

	t.Run("shallow: nested objects are replaced wholesale", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `{"env":{"FOO":"bar"}}`},
		}, `{"env":{"BAZ":"qux"},"command":"ls"}`)
		// "env" 是 replaced entirely; "command" preserved.
		require.JSONEq(
			t,
			`{"env":{"FOO":"bar"},"command":"ls"}`,
			agg.UpdatedInput,
		)
	})

	t.Run("deny still reports merged input (caller ignores it)", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `{"command":"rewritten"}`},
			{Decision: DecisionDeny, Reason: "blocked"},
		}, `{"command":"orig"}`)
		require.Equal(t, DecisionDeny, agg.Decision)
	})

	t.Run("no patches leaves updated_input empty", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow},
			{Decision: DecisionNone},
		}, `{"command":"orig"}`)
		require.Empty(t, agg.UpdatedInput)
	})

	t.Run("invalid patch is ignored", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `"not-an-object"`},
			{Decision: DecisionAllow, UpdatedInput: `{"command":"good"}`},
		}, `{"command":"orig"}`)
		require.JSONEq(t, `{"command":"good"}`, agg.UpdatedInput)
	})

	t.Run("malformed patch JSON is ignored and merge continues", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `{broken json`},
			{Decision: DecisionAllow, UpdatedInput: `{"command":"good"}`},
		}, `{"command":"orig"}`)
		require.JSONEq(t, `{"command":"good"}`, agg.UpdatedInput)
	})

	t.Run("non-object tool_input rejects all patches", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `{"command":"rewrite"}`},
		}, `"just-a-string"`)
		require.Empty(t, agg.UpdatedInput)
	})

	t.Run("null updated_input is a no-op", func(t *testing.T) {
		t.Parallel()
		// parseStdout converts null updated_input 到 "", 所以 aggregate
		// never sees patch — merged input 是 empty 和
		// original tool_input 是 used unchanged.
		r, _ := parseStdout(`{"decision":"allow","updated_input":null}`)
		require.Empty(t, r.UpdatedInput)
		agg := aggregate([]HookResult{r}, `{"command":"orig"}`)
		require.Empty(t, agg.UpdatedInput)
	})
}

// TestRunnerAbandonRaceSafety verifies 那 如果 钩子's shell execution
// does 不 yield 到 ctx cancellation within abandonGrace, runOne returns
// promptly 和 never touches shared stdout/stderr buffers again —
// even while abandoned goroutine continues 到 写入 到 them.
//
// substitute shell executor ignores ctx entirely, writes 到 Stdout
// both before 和 after abandon 期限, 和 只 then returns.
// Under -race 这 catches any code 路径 在 runOne 那 reads those
// buffers after returning DecisionNone abandon 结果.
//
// abandoned goroutine outlives r.运行, 所以 测试 also gates 它的
// shutdown 在 stub having been entered (started): wg.Add runs inside
// stub, 和 under loaded scheduler goroutine may 不 是
// scheduled until after 测试 body returned. Waiting 在 started
// orders wg.Add (和 goroutine's 读取 的 runShell variable)
// before 清理's wg.Wait 和 清理 restoring runShell;
// without it both pairs 是 Add-after-Wait misuse 那 -race flags.
func TestRunnerAbandonRaceSafety(t *testing.T) {
	origRunShell := runShell
	t.Cleanup(func() { runShell = origRunShell })

	// Synchronize shutdown 用 abandoned goroutine 所以 测试
	// exits cleanly even under -race.
	var wg sync.WaitGroup
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		wg.Wait()
	})

	runShell = func(_ context.Context, opts shell.RunOptions) error {
		wg.Add(1)
		defer wg.Done()
		close(started)
		// 写入 before caller observes ctx.Done(); caller will
		// 不 读取 缓冲 while we still own it.
		_, _ = io.WriteString(opts.Stdout, "before\n")
		// Hold past ctx 期限 + abandonGrace 所以 caller takes
		// the abandon branch, then continue writing. If the caller
		// reads these buffers after abandoning, -race 将 flag it.
		select {
		case <-time.After(5 * time.Second):
		case <-release:
		}
		_, _ = io.WriteString(opts.Stdout, "after\n")
		return nil
	}

	hookCfg := HookConfig{
		Command: "# irrelevant; runShell is stubbed",
		Timeout: 1,
	}
	r := newTestRunner(t, []HookConfig{hookCfg})

	start := time.Now()
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	elapsed := time.Since(start)

	require.Equal(t, DecisionNone, result.Decision)
	// Abandon must happen 在 ~超时 + abandonGrace. Allow generous
	// slack 所以 CI noise doesn't flake 测试.
	require.Less(t, elapsed, 3500*time.Millisecond,
		"runOne should return within timeout+abandonGrace+slack")

	// Block until abandoned goroutine has entered 桩. In
	// common case it was entered long before abandon 期限 和
	// 这 returns immediately; 在 pathological case goroutine
	// was starved 为 whole abandon 窗口, 和 这 waits 为 it 到
	// 运行 所以 cleanups below stay ordered after 它的 wg.Add 和 它的
	// 读取 的 runShell variable.
	<-started
}

func TestRunnerUpdatedInput(t *testing.T) {
	t.Parallel()
	hookCfg := HookConfig{
		Command: `echo '{"decision":"allow","updated_input":{"command":"echo rewritten"}}'`,
	}
	r := newTestRunner(t, []HookConfig{hookCfg})
	result := r.Run(context.Background(), preToolEvent("bash", `{"command":"echo original","timeout":60}`))
	require.Equal(t, DecisionAllow, result.Decision)
	require.JSONEq(
		t,
		`{"command":"echo rewritten","timeout":60}`,
		result.UpdatedInput,
	)
}

func TestParseStdoutClaudeCodeFormat(t *testing.T) {
	t.Parallel()

	t.Run("allow with reason", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"RTK auto-rewrite"}}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, "RTK auto-rewrite", r.Reason)
	})

	t.Run("allow with updatedInput", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"command":"rtk cat foo.go"}}}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, `{"command":"rtk cat foo.go"}`, r.UpdatedInput)
	})

	t.Run("deny", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"not allowed"}}`)
		require.Equal(t, DecisionDeny, r.Decision)
		require.Equal(t, "not allowed", r.Reason)
	})

	t.Run("no permissionDecision", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"hookSpecificOutput":{}}`)
		require.Equal(t, DecisionNone, r.Decision)
	})

	t.Run("crush format still works", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"allow","context":"hello"}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, "hello", r.Context)
	})
}

func TestParseStdoutClaudeCodeTopLevel(t *testing.T) {
	t.Parallel()

	t.Run("decision block maps to deny with reason", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"decision":"block","reason":"run the tests first"}`)
		require.Equal(t, DecisionDeny, r.Decision)
		require.Equal(t, "run the tests first", r.Reason)
	})

	t.Run("continue false halts with stopReason", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"continue":false,"stopReason":"budget exhausted"}`)
		require.True(t, r.Halt)
		require.Equal(t, "budget exhausted", r.Reason)
	})

	t.Run("updatedToolOutput replaces output", func(t *testing.T) {
		t.Parallel()
		r, _ := parseStdout(`{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedToolOutput":"[REDACTED]","additionalContext":"scrubbed"}}`)
		require.Equal(t, "[REDACTED]", r.UpdatedOutput)
		require.Empty(t, r.UpdatedInput)
		require.Equal(t, "scrubbed", r.Context)
	})

	t.Run("non-object stdout is not JSON", func(t *testing.T) {
		t.Parallel()
		_, ok := parseStdout(`plain text`)
		require.False(t, ok)
		_, ok = parseStdout(`{"decision":"allow"}`)
		require.True(t, ok)
	})
}

func TestRunnerNonZeroExitWithJSON(t *testing.T) {
	t.Parallel()
	// As 在 Claude Code, valid JSON object 在 stdout counts even 当
	// exit code 是 non-blocking 错误.
	r := newTestRunner(t, []HookConfig{
		{Command: `echo '{"decision":"deny","reason":"from json"}'; exit 1`},
	})
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "from json", result.Reason)
}

func TestRunnerNilIsNoop(t *testing.T) {
	t.Parallel()
	var r *Runner
	result := r.Run(context.Background(), preToolEvent("bash", `{}`))
	require.Equal(t, DecisionNone, result.Decision)
	require.Zero(t, result.HookCount)
}

func TestRunnerLifecycleEvent(t *testing.T) {
	t.Parallel()
	r := NewRunner(Static(map[string][]HookConfig{
		EventTurnEnd: {
			{Command: `test "$CRUSH_EVENT" = TurnEnd || exit 1; jq -r .last_assistant_message | grep -q hello || exit 1; test -z "$CRUSH_TOOL_NAME" || exit 1; echo '{"decision":"allow"}'`},
		},
	}), t.TempDir(), t.TempDir())
	result := r.Run(context.Background(), Event{Name: EventTurnEnd, SessionID: "s", LastAssistantMessage: new("hello there")})
	require.Equal(t, 1, result.HookCount)
	require.Equal(t, DecisionAllow, result.Decision)
	require.Equal(t, EventTurnEnd, result.Hooks[0].Event)

	// An 事件 用 no hooks configured runs nothing.
	none := r.Run(context.Background(), Event{Name: EventTurnStart, SessionID: "s"})
	require.Zero(t, none.HookCount)
}

func TestRunnerPostToolUseReplacesOutput(t *testing.T) {
	t.Parallel()
	r := NewRunner(Static(map[string][]HookConfig{
		EventPostToolUse: {
			{Command: `echo '{"updated_output":"first"}'`},
			{Command: `echo '{"updated_output":"second"}'`},
		},
	}), t.TempDir(), t.TempDir())
	result := r.Run(context.Background(), Event{Name: EventPostToolUse, Tool: &ToolCall{Name: "bash", Input: `{}`}, ToolResponse: new("secret")})
	// Last writer wins, 和 input 是 left alone.
	require.Equal(t, "second", result.UpdatedOutput)
	require.Empty(t, result.UpdatedInput)
	require.True(t, result.Hooks[1].OutputRewrite)
}

func TestRunnerPermissionMatcher(t *testing.T) {
	t.Parallel()
	r := NewRunner(Static(map[string][]HookConfig{
		EventPermissionRequest: {
			{Command: `echo '{"decision":"allow"}'`, Matcher: "^bash$"},
		},
	}), t.TempDir(), t.TempDir())
	require.Equal(t, 1, r.Run(context.Background(), Event{Name: EventPermissionRequest, Tool: &ToolCall{Name: "bash"}}).HookCount)
	require.Zero(t, r.Run(context.Background(), Event{Name: EventPermissionRequest, Tool: &ToolCall{Name: "view"}}).HookCount)
}

func TestRunnerPicksUpSourceChanges(t *testing.T) {
	t.Parallel()
	hooks := map[string][]HookConfig{}
	r := NewRunner(func() map[string][]HookConfig { return hooks }, t.TempDir(), t.TempDir())
	require.Zero(t, r.Run(context.Background(), preToolEvent("bash", `{}`)).HookCount)

	hooks = map[string][]HookConfig{EventPreToolUse: {{Command: "exit 0"}}}
	require.Equal(t, 1, r.Run(context.Background(), preToolEvent("bash", `{}`)).HookCount)
}

func TestRunnerCoreutilsMode(t *testing.T) {
	hookCfg := HookConfig{Command: "# irrelevant; runShell is stubbed"}

	var got shell.CoreutilsMode
	origRunShell := runShell
	runShell = func(_ context.Context, opts shell.RunOptions) error {
		got = opts.Coreutils
		return nil
	}
	t.Cleanup(func() { runShell = origRunShell })

	r := NewRunner(
		Static(map[string][]HookConfig{EventPreToolUse: {hookCfg}}),
		t.TempDir(), t.TempDir(),
		WithCoreutils(func() shell.CoreutilsMode { return shell.CoreutilsSystem }),
	)
	r.Run(t.Context(), preToolEvent("bash", `{}`))
	require.Equal(t, shell.CoreutilsSystem, got)

	// Without option, hooks 运行 用 shell's 默认 mode.
	r = NewRunner(
		Static(map[string][]HookConfig{EventPreToolUse: {hookCfg}}),
		t.TempDir(), t.TempDir(),
	)
	r.Run(t.Context(), preToolEvent("bash", `{}`))
	require.Equal(t, shell.CoreutilsTurtle, got)
}
