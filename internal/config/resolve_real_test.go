package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/env"
	"github.com/stretchr/testify/require"
)

// These tests exercise full shell-expansion 路径 (no mocks,
// no injected Expander) 到 catch regressions 那 只 surface 当
// internal/shell actually runs 值. 表-level unit tests 用
// fake expanders live 在 resolve_test.go.

// realShellResolver builds resolver backed 通过 shell env 那
// contains 路径 + caller-supplied overrides. Production callers
// get 路径 为 free via env.New(); these tests need it 所以 $(cat ...)
// 和 similar inner commands can resolve.
func realShellResolver(vars map[string]string) VariableResolver {
	m := map[string]string{"PATH": os.Getenv("PATH")}
	maps.Copy(m, vars)
	return NewShellVariableResolver(env.NewFromMap(m))
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

// TestResolvedEnv_RealShell_Success covers the shell constructs the
// PLAN calls out: $(cat tempfile) 用 和 without trailing newline,
// ${VAR:-默认} 为 unset vars, literal-space preservation around
// $(...), nested parens, quoted args inside $(echo ...), 和
// glob-like literal round-tripping unchanged.
func TestResolvedEnv_RealShell_Success(t *testing.T) {
	t.Parallel()

	// filepath.ToSlash 所以 Windows temp paths (C:\Users\...) survive
	// being injected into a shell command string — the embedded shell
	// treats backslashes 作为 escapes, forward slashes work 在 every OS.
	withNL := filepath.ToSlash(writeTempFile(t, "token-with-nl\n"))
	noNL := filepath.ToSlash(writeTempFile(t, "token-no-nl"))

	m := MCPConfig{
		Env: map[string]string{
			// POSIX strips exactly one trailing newline 从 $(...)
			// output, 所以 both forms land 在 same 值.
			"TOK_NL": fmt.Sprintf("$(cat %s)", withNL),
			"TOK_NO": fmt.Sprintf("$(cat %s)", noNL),

			// ${VAR:-默认} must 不 错误 在 unset: 这 是
			// opt-在 escape hatch 为 "empty 是 fine".
			"FALLBACK": "${MCP_MISSING:-fallback}",

			// Leading/trailing literal spaces around $(...) must 是
			// preserved — single-值 contract, no 字段 splitting.
			"PADDED": "  $(echo v)  ",

			// ")" inside quoted arg 到 echo 是 regression guard
			// 为 old hand-rolled paren matcher.
			"PAREN": `$(echo ")")`,

			// Embedded space inside quoted arg 必须 survive
			// verbatim; no word-splitting side effect.
			"SPACEY": `$(echo "a b")`,

			// Glob-like literals must 不 expand.
			"GLOB": "*.go",
		},
	}

	got, err := m.ResolvedEnv(realShellResolver(nil))
	require.NoError(t, err)

	// ResolvedEnv returns "KEY=值" sorted 通过 key.
	want := []string{
		"FALLBACK=fallback",
		"GLOB=*.go",
		"PADDED=  v  ",
		"PAREN=)",
		"SPACEY=a b",
		"TOK_NL=token-with-nl",
		"TOK_NO=token-no-nl",
	}
	require.Equal(t, want, got)
}

// TestResolvedEnv_RealShell_DoesNotMutate pins 那 both success 和
// failure paths leave m.Env 原样. Prior behaviour rewrote
// 值 在 place 在 错误; 那 was exact mechanism 那 shipped
// empty credentials 到 MCP servers.
func TestResolvedEnv_RealShell_DoesNotMutate(t *testing.T) {
	t.Parallel()

	t.Run("success path leaves Env untouched", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Env: map[string]string{"TOKEN": "$(echo shh)"}}
		orig := maps.Clone(m.Env)

		_, err := m.ResolvedEnv(realShellResolver(nil))
		require.NoError(t, err)
		require.Equal(t, orig, m.Env)
	})

	t.Run("failure path leaves Env untouched", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Env: map[string]string{"BROKEN": "$(false)"}}
		orig := maps.Clone(m.Env)

		_, err := m.ResolvedEnv(realShellResolver(nil))
		require.Error(t, err)
		require.Equal(t, orig, m.Env, "map must be preserved on error")
	})
}

// TestResolvedEnv_RealShell_Idempotent pins the pure-function contract:
// two calls 在 same 配置 return deeply-equal slices.
func TestResolvedEnv_RealShell_Idempotent(t *testing.T) {
	t.Parallel()

	m := MCPConfig{
		Env: map[string]string{
			"A": "$(echo one)",
			"B": "$(echo two)",
			"C": "literal",
		},
	}
	r := realShellResolver(nil)

	first, err := m.ResolvedEnv(r)
	require.NoError(t, err)
	second, err := m.ResolvedEnv(r)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

// TestResolvedEnv_RealShell_Deterministic guards against Go's
// randomized map iteration leaking into the returned slice order.
func TestResolvedEnv_RealShell_Deterministic(t *testing.T) {
	t.Parallel()

	m := MCPConfig{Env: map[string]string{
		"Z": "z",
		"A": "a",
		"M": "m",
	}}

	got, err := m.ResolvedEnv(realShellResolver(nil))
	require.NoError(t, err)
	require.True(t, slices.IsSorted(got), "env slice must be sorted; got %v", got)
}

// TestResolvedEnv_RealShell_UnsetExpandsEmpty pins the lenient
// 默认: unset bare $VAR expands 到 empty string, matching
// bash. silent-empty-credential class 的 bug 是 prevented 通过
// pure-function 错误-returning contract 的 ResolvedEnv, 所以 we don't
// rely 在 nounset 到 catch typo'd variable names. Users who want
// strict behaviour 为 required credential opt 在 per-reference
// 用 ${VAR:?msg}; see TestResolvedEnv_RealShell_ColonQuestionIsStrict.
func TestResolvedEnv_RealShell_UnsetExpandsEmpty(t *testing.T) {
	t.Parallel()

	m := MCPConfig{Env: map[string]string{
		// Intentional typo: 用户 meant $FORGEJO_TOKEN. Under
		// lenient 默认 这 expands 到 "" rather than erroring,
		// matching bash's behaviour 在 bare $VAR.
		"FORGEJO_ACCESS_TOKEN": "$FORGJO_TOKEN",
	}}
	got, err := m.ResolvedEnv(realShellResolver(nil))
	require.NoError(t, err, "unset var must expand to empty, not error")
	require.Equal(t, []string{"FORGEJO_ACCESS_TOKEN="}, got)
}

// TestResolvedEnv_RealShell_ColonQuestionIsStrict pins opt-在
// strictness contract: ${VAR:?msg} must hard-错误 当 VAR 是 unset,
// regardless 的 global NoUnset toggle. 这 是 recommended
// mechanism 为 required credentials under lenient 默认, 所以
// future refactor 那 accidentally swallows ${VAR:?...} errors would
// silently ship empty tokens 到 MCP servers again.
func TestResolvedEnv_RealShell_ColonQuestionIsStrict(t *testing.T) {
	t.Parallel()

	m := MCPConfig{Env: map[string]string{
		"FORGEJO_ACCESS_TOKEN": "${FORGJO_TOKEN:?set FORGJO_TOKEN}",
	}}
	got, err := m.ResolvedEnv(realShellResolver(nil))
	require.Error(t, err, "${VAR:?msg} must error when VAR is unset")
	require.Nil(t, got)
	// resolver wraps 用 env key 和 用户-written
	// template; inner shell 错误 carries :? 消息 所以
	// users learn which credential 是 missing 和 why.
	msg := err.Error()
	require.Contains(t, msg, "FORGEJO_ACCESS_TOKEN")
	require.Contains(t, msg, "${FORGJO_TOKEN:?set FORGJO_TOKEN}")
	require.Contains(t, msg, "set FORGJO_TOKEN")
}

// TestResolvedEnv_RealShell_FailureDetail pins 那 failing inner
// command surfaces enough detail (exit code + stderr 在 POSIX,
// underlying OS 错误 在 Windows where coreutils runs 在-process) 到
// diagnose without forcing 用户 到 re-运行 command 通过 hand.
// Also verifies template 是 included 所以 they know which Env
// 条目 blew up.
func TestResolvedEnv_RealShell_FailureDetail(t *testing.T) {
	t.Parallel()

	// Forward slashes 所以 路径 survives shell-string injection 在
	// Windows; see TestResolvedEnv_RealShell_Success 为 same note.
	missing := filepath.ToSlash(filepath.Join(t.TempDir(), "definitely-not-here"))
	m := MCPConfig{Env: map[string]string{
		"FORGEJO_ACCESS_TOKEN": fmt.Sprintf("$(cat %s)", missing),
	}}

	_, err := m.ResolvedEnv(realShellResolver(nil))
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "FORGEJO_ACCESS_TOKEN", "must identify the failing env var")
	require.Contains(t, msg, missing, "must include the template so users see what failed")

	// Inner diagnostic detail 必须 survive. POSIX surfaces "exit
	// status N" + stderr; Windows' 在-process coreutils surfaces
	// Go OS 错误 instead. 受理 either shape 所以 测试 是
	// portable without weakening the intent.
	lower := strings.ToLower(msg)
	hasDetail := strings.Contains(lower, "exit status") ||
		strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "cannot find")
	require.True(t, hasDetail, "must surface inner error detail: %s", msg)
}

// TestResolvedHeaders_RealShell_FailurePreservesOriginal pins two
// invariants simultaneously: 在 failure returned map 是 nil (不
// partially-populated map) 和 receiver's Headers map 是
// unchanged. 一个 测试 那 只 asserted 在 returned 值 could
// hide 在-place mutation regression.
func TestResolvedHeaders_RealShell_FailurePreservesOriginal(t *testing.T) {
	t.Parallel()

	m := MCPConfig{Headers: map[string]string{
		"Authorization": "Bearer $(false)",
		"X-Static":      "kept",
	}}
	orig := maps.Clone(m.Headers)

	got, err := m.ResolvedHeaders(realShellResolver(nil))
	require.Error(t, err)
	require.Nil(t, got, "headers map must be nil on failure")
	require.Contains(t, err.Error(), "header Authorization")
	require.Equal(t, orig, m.Headers, "receiver Headers must be preserved")
}

// TestResolvedHeaders_RealShell_DropEmpty verifies 那 header
// whose 值 resolves 到 empty string 是 omitted 从
// returned map. Covers three ways 值 can legitimately land
// 在 empty — unset bare $VAR under lenient nounset, literal "",
// 和 non-failing command whose stdout 是 empty — 和 also pins
// 那 failing $(cmd) still errors rather than silently dropping.
func TestResolvedHeaders_RealShell_DropEmpty(t *testing.T) {
	t.Parallel()

	t.Run("unset $VAR is absent", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Headers: map[string]string{
			"X-Missing": "$MCP_HEADER_NEVER_SET",
			"X-Kept":    "present",
		}}
		got, err := m.ResolvedHeaders(realShellResolver(nil))
		require.NoError(t, err)
		_, present := got["X-Missing"]
		require.False(t, present, "unset bare $VAR → empty → header dropped")
		require.Equal(t, "present", got["X-Kept"])
	})

	t.Run("literal empty string is absent", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Headers: map[string]string{
			"X-Custom": "",
			"X-Kept":   "present",
		}}
		got, err := m.ResolvedHeaders(realShellResolver(nil))
		require.NoError(t, err)
		_, present := got["X-Custom"]
		require.False(t, present, "literal empty-string header must be dropped")
		require.Equal(t, "present", got["X-Kept"])
	})

	t.Run("$(echo) is absent", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Headers: map[string]string{
			"X-Empty": "$(echo)",
			"X-Kept":  "present",
		}}
		got, err := m.ResolvedHeaders(realShellResolver(nil))
		require.NoError(t, err)
		_, present := got["X-Empty"]
		require.False(t, present, "$(echo) → empty → header dropped")
		require.Equal(t, "present", got["X-Kept"])
	})

	t.Run("$(false) errors and does not mutate", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Headers: map[string]string{
			"X-Broken": "$(false)",
			"X-Kept":   "present",
		}}
		orig := maps.Clone(m.Headers)

		got, err := m.ResolvedHeaders(realShellResolver(nil))
		require.Error(t, err)
		require.Empty(t, got, "map must be nil/empty on failure, not a partial")
		require.Contains(t, err.Error(), "header X-Broken")
		require.Equal(t, orig, m.Headers, "receiver Headers must be preserved")
	})
}

// TestResolvedArgs_RealShell exercises both success 和 failure 为
// m.Args symmetrically 用 Env. Args 是 ordered 所以 错误 messages
// must identify positional index, 不 key.
func TestResolvedArgs_RealShell(t *testing.T) {
	t.Parallel()

	t.Run("success expands each element", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Args: []string{"--token", "$(echo shh)", "--host", "example.com"}}
		got, err := m.ResolvedArgs(realShellResolver(nil))
		require.NoError(t, err)
		require.Equal(t, []string{"--token", "shh", "--host", "example.com"}, got)
	})

	t.Run("failure identifies offending index", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Args: []string{"--token", "$(false)"}}
		orig := slices.Clone(m.Args)

		got, err := m.ResolvedArgs(realShellResolver(nil))
		require.Error(t, err)
		require.Nil(t, got)
		require.Contains(t, err.Error(), "arg 1")
		require.Equal(t, orig, m.Args, "receiver Args must be preserved")
	})

	t.Run("nil args returns nil, no error", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{}
		got, err := m.ResolvedArgs(realShellResolver(nil))
		require.NoError(t, err)
		require.Nil(t, got)
	})
}

// TestLSPConfig_ResolvedArgs_RealShell exercises both success 和
// failure 为 l.Args symmetrically 用 MCP args. Args 是 ordered 所以
// 错误 messages must identify positional index, 不 key.
func TestLSPConfig_ResolvedArgs_RealShell(t *testing.T) {
	t.Parallel()

	t.Run("success expands $VAR in each element", func(t *testing.T) {
		t.Parallel()
		l := LSPConfig{Args: []string{"--root", "$HOME", "--flag", "literal"}}
		r := realShellResolver(map[string]string{"HOME": "/home/tester"})
		got, err := l.ResolvedArgs(r)
		require.NoError(t, err)
		require.Equal(t, []string{"--root", "/home/tester", "--flag", "literal"}, got)
	})

	t.Run("failure identifies offending index", func(t *testing.T) {
		t.Parallel()
		l := LSPConfig{Args: []string{"--root", "$(false)"}}
		orig := slices.Clone(l.Args)

		got, err := l.ResolvedArgs(realShellResolver(nil))
		require.Error(t, err)
		require.Nil(t, got)
		require.Contains(t, err.Error(), "arg 1")
		require.Equal(t, orig, l.Args, "receiver Args must be preserved")
	})

	t.Run("nil args returns nil, no error", func(t *testing.T) {
		t.Parallel()
		l := LSPConfig{}
		got, err := l.ResolvedArgs(realShellResolver(nil))
		require.NoError(t, err)
		require.Nil(t, got)
	})
}

// TestLSPConfig_ResolvedEnv_RealShell pins the LSP env contract:
// success expands $VAR, failure wraps 用 key name, 和
// receiver map 是 never mutated. shape 是 map[string]string
// (不 MCP []string form) because powernap.ClientConfig.Environment
// takes a map directly.
func TestLSPConfig_ResolvedEnv_RealShell(t *testing.T) {
	t.Parallel()

	t.Run("success expands $VAR", func(t *testing.T) {
		t.Parallel()
		l := LSPConfig{Env: map[string]string{"GOPATH": "$HOME/go"}}
		r := realShellResolver(map[string]string{"HOME": "/home/tester"})
		got, err := l.ResolvedEnv(r)
		require.NoError(t, err)
		require.Equal(t, map[string]string{"GOPATH": "/home/tester/go"}, got)
	})

	t.Run("failure identifies offending key", func(t *testing.T) {
		t.Parallel()
		l := LSPConfig{Env: map[string]string{
			"GOPATH": "$(false)",
			"OTHER":  "literal",
		}}
		orig := maps.Clone(l.Env)

		got, err := l.ResolvedEnv(realShellResolver(nil))
		require.Error(t, err)
		require.Nil(t, got)
		require.Contains(t, err.Error(), `env "GOPATH"`)
		require.Equal(t, orig, l.Env, "receiver Env must be preserved")
	})

	t.Run("idempotent and non-mutating", func(t *testing.T) {
		t.Parallel()
		l := LSPConfig{Env: map[string]string{
			"A": "$(echo one)",
			"B": "literal",
		}}
		orig := maps.Clone(l.Env)
		r := realShellResolver(nil)

		first, err := l.ResolvedEnv(r)
		require.NoError(t, err)
		second, err := l.ResolvedEnv(r)
		require.NoError(t, err)
		require.Equal(t, first, second)
		require.Equal(t, orig, l.Env, "receiver Env must be preserved")
	})
}

// TestLSPConfig_IdentityResolver pins 客户端-mode contract: both
// ResolvedArgs 和 ResolvedEnv round-trip template verbatim under
// IdentityResolver 和 never 错误 在 unset variables. Local
// expansion would double-expand 当 服务端 does 它的 own — 这
// has 到 stay pure pass-through.
func TestLSPConfig_IdentityResolver(t *testing.T) {
	t.Parallel()

	l := LSPConfig{
		Args: []string{"--root", "$LSP_ROOT", "$(vault read -f lsp)"},
		Env: map[string]string{
			"GOPATH": "$HOME/go",
			"TOKEN":  "$(cat /run/secrets/x)",
		},
	}
	r := IdentityResolver()

	args, err := l.ResolvedArgs(r)
	require.NoError(t, err)
	require.Equal(t, l.Args, args)

	envs, err := l.ResolvedEnv(r)
	require.NoError(t, err)
	require.Equal(t, l.Env, envs)
}

// TestMCPConfig_IdentityResolver pins 客户端-mode contract: every
// Resolved* method round-trips template verbatim 和 never errors
// 在 unset variables. Local expansion would double-expand 当
// 服务端 does 它的 own — 这 has 到 stay pure pass-through.
func TestMCPConfig_IdentityResolver(t *testing.T) {
	t.Parallel()

	m := MCPConfig{
		Command: "$CMD",
		Args:    []string{"--token", "$MCP_MISSING_TOKEN", "$(vault read -f secret)"},
		Env: map[string]string{
			"TOKEN": "$(cat /run/secrets/x)",
			"HOST":  "$MCP_MISSING_HOST",
		},
		Headers: map[string]string{
			"Authorization": "Bearer $(vault read -f token)",
		},
		URL: "https://$MCP_HOST/$(vault read -f path)",
	}
	r := IdentityResolver()

	args, err := m.ResolvedArgs(r)
	require.NoError(t, err)
	require.Equal(t, m.Args, args)

	envs, err := m.ResolvedEnv(r)
	require.NoError(t, err)
	// Sorted "KEY=值".
	require.Equal(t, []string{
		"HOST=$MCP_MISSING_HOST",
		"TOKEN=$(cat /run/secrets/x)",
	}, envs)

	headers, err := m.ResolvedHeaders(r)
	require.NoError(t, err)
	require.Equal(t, m.Headers, headers)

	u, err := m.ResolvedURL(r)
	require.NoError(t, err)
	require.Equal(t, m.URL, u)
}

// TestLoad_CommandSubstitutionRunsInWorkingDir pins 那 $(...) 在 配置
// values runs 在 存储's working 目录 rather than process's.
// 服务端 hosts every 工作区 在 one process whose working 目录
// belongs 到 none 的 them, 所以 project's "$(cat key.txt)" must still 读取
// 那 project's key.txt.
func TestLoad_CommandSubstitutionRunsInWorkingDir(t *testing.T) {
	// Not parallel: sets environment variables, resets package-level
	// 提供方 state 和 changes process working 目录.
	workDir, dataDir := isolateLoadEnv(t)
	resetProviderState()
	t.Cleanup(resetProviderState)

	elsewhere := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "key.txt"), []byte("workspace-key\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(elsewhere, "key.txt"), []byte("process-cwd-key\n"), 0o600))
	t.Chdir(elsewhere)

	require.NoError(t, os.WriteFile(filepath.Join(workDir, "crush.json"), []byte(`{
		"options": {"disable_default_providers": true},
		"providers": {
			"local": {
				"base_url": "http://127.0.0.1:1/v1",
				"api_key": "$(cat key.txt)",
				"models": [{"id": "m", "name": "M"}]
			}
		},
		"models": {
			"large": {"provider": "local", "model": "m"},
			"small": {"provider": "local", "model": "m"}
		}
	}`), 0o600))

	store, err := Load(t.Context(), workDir, dataDir, false)
	require.NoError(t, err)

	requireWorkspaceKey := func() {
		t.Helper()
		pc, ok := store.Config().Providers.Get("local")
		require.True(t, ok)
		key, err := store.Resolve(pc.APIKey)
		require.NoError(t, err)
		require.Equal(t, "workspace-key", key)
	}
	requireWorkspaceKey()

	// 一个 reload builds new resolver, which must keep working
	// 目录 too.
	require.NoError(t, store.ReloadFromDisk(t.Context()))
	requireWorkspaceKey()
}

// TestShellVariableResolver_MissingWorkingDir pins 那 working 目录
// 那 does 不 exist leaves $(...) 在 process working 目录 rather
// than failing it, since 服务端 resolves 它的 own 配置 在
// GlobalWorkspaceDir, which may 不 have been created yet.
func TestShellVariableResolver_MissingWorkingDir(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	require.NoError(t, err)
	missing := filepath.Join(t.TempDir(), "missing")

	r := NewShellVariableResolver(env.NewFromMap(nil), WithWorkingDir(missing))
	got, err := r.ResolveValue("$(pwd)")
	require.NoError(t, err)
	require.Equal(t, wd, got)
}
