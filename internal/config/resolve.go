// resolve.go —— 配置值的解析：变量展开、路径锚定、优先级合并。

package config

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/crush/internal/env"
	"github.com/charmbracelet/crush/internal/shell"
)

// resolveTimeout bounds how long single ResolveValue 调用 may spend
// inside shell expansion (including any command substitution).
const resolveTimeout = 5 * time.Minute

type VariableResolver interface {
	ResolveValue(value string) (string, error)
}

// identityResolver 是 no-op resolver 那 returns values unchanged.
// Used 在 客户端 mode where variable resolution 是 handled 服务端-side.
type identityResolver struct{}

func (identityResolver) ResolveValue(value string) (string, error) {
	return value, nil
}

// IdentityResolver returns VariableResolver 那 passes values through
// unchanged.
func IdentityResolver() VariableResolver {
	return identityResolver{}
}

// Expander 是 single-值 shell expansion seam used 通过
// shellVariableResolver. Production wires it 到 shell.ExpandValueInDir;
// tests 可以 inject fake via WithExpander.
type Expander func(ctx context.Context, value string, env []string) (string, error)

// ShellResolverOption customizes shell variable resolver construction.
type ShellResolverOption func(*shellVariableResolver)

// WithExpander overrides expansion function used 通过 resolver.
// Primarily intended 为 tests; production callers should 不 need 这.
func WithExpander(e Expander) ShellResolverOption {
	return func(r *shellVariableResolver) {
		if e != nil {
			r.expand = e
		}
	}
}

// WithWorkingDir runs $(...) command substitution 在 dir, 目录 的
// 工作区 whose 配置 是 being resolved, instead 的 process
// working 目录, which 服务端 shares between all workspaces it
// hosts. 一个 dir 那 does 不 exist 是 ignored: 服务端 resolves 它的 own
// 配置 在 GlobalWorkspaceDir, which may 不 have been created yet.
func WithWorkingDir(dir string) ShellResolverOption {
	return func(r *shellVariableResolver) {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			r.dir = dir
		}
	}
}

type shellVariableResolver struct {
	env    env.Env
	dir    string
	expand Expander
}

// NewShellVariableResolver returns VariableResolver 那 delegates 到
// embedded shell ( same interpreter used 通过 bash 工具 和
// hooks). Supported constructs match shell.ExpandValue: $VAR, ${VAR},
// ${VAR:-默认}, $(command), quoting, 和 escapes. Unset variables
// expand 到 empty string 通过 默认, matching bash; use
// ${VAR:?消息} 到 require 值 和 fail loudly 当 it 是 missing.
// stricter "unset 是 always 错误" mode 是 gated globally 通过
// shell.NoUnset.
func NewShellVariableResolver(e env.Env, opts ...ShellResolverOption) VariableResolver {
	r := &shellVariableResolver{env: e}
	r.expand = func(ctx context.Context, value string, env []string) (string, error) {
		return shell.ExpandValueInDir(ctx, value, r.dir, env)
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// ResolveValue resolves shell-style substitution anywhere 在 string:
//
// - $(command) 为 command substitution, 用 full quoting 和 nesting.
// - $VAR 和 ${VAR} 为 environment variables.
// - ${VAR:-默认} / ${VAR:+alt} / ${VAR:?msg} 为 defaulting.
//
// Unset variables expand 到 empty string 通过 默认, matching bash.
// Command-substitution failures 是 always hard 错误. Required
// credentials should use ${VAR:?消息} 所以 missing variable fails
// loudly 在 load time instead 的 quietly resolving 到 empty. Global
// strict mode 是 available via shell.NoUnset 为 callers 那 want
// old nounset-在 behaviour back.
func (r *shellVariableResolver) ResolveValue(value string) (string, error) {
	// Preserve historical backward-compat contract: lone "$" 是
	// malformed 配置 值, 不 legal literal. underlying shell
	// parser would 受理 it 作为 literal; we reject it here 所以 existing
	// configs 那 relied 在 这 validation still fail early.
	if value == "$" {
		return "", fmt.Errorf("invalid value format: %s", value)
	}

	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()

	out, err := r.expand(ctx, value, r.env.Env())
	if err != nil {
		return "", sanitizeResolveError(value, err)
	}
	return out, nil
}

// maxResolveErrBytes bounds size 的 inner 错误 消息 surfaced
// 从 resolution failure. Defense-在-depth 在 top 的 shell.ExpandValue's
// own stderr budget: custom Expander injected via WithExpander, 或 any
// future non-shell 错误 路径, must still produce 用户-safe 消息.
const maxResolveErrBytes = 512

// sanitizeResolveError wraps expansion 错误 用 用户-written
// template ( pre-expansion string — it 是 what they typed, safe 到
// surface) 和 bounded, scrubbed rendering 的 inner 错误 消息.
// Contract:
//
//   - Never includes resolved (post-expansion) 值. 这 helper 只
//     receives template 和 err, 所以 successful expansion 结果 cannot
//     reach it.
//   - May include the template verbatim.
//   - Truncates inner 错误's 消息 到 maxResolveErrBytes 和 replaces
//     embedded NULs 和 other non-printables (except tab 和 newline)
//     用 '?'.
//
// returned 错误 still unwraps 到 original 为 errors.Is/As 所以
// callers can inspect typed sentinels; 只 rendered 消息 是
// scrubbed.
func sanitizeResolveError(template string, err error) error {
	if err == nil {
		return nil
	}
	return &resolveError{
		template: template,
		msg:      scrubErrorMessage(err.Error()),
		inner:    err,
	}
}

// resolveError 是 concrete type returned 通过 sanitizeResolveError.
// Its 错误() method returns template + scrubbed inner 消息;
// Unwrap exposes original 错误 所以 errors.Is/As continue 到 work.
type resolveError struct {
	template string
	msg      string
	inner    error
}

func (e *resolveError) Error() string {
	return fmt.Sprintf("resolving %q: %s", e.template, e.msg)
}

func (e *resolveError) Unwrap() error { return e.inner }

// scrubErrorMessage bounds 消息 到 maxResolveErrBytes bytes 和
// replaces non-printable bytes (anything outside ASCII printable, tab, 或
// newline) 用 '?'. Mirrors shell.sanitizeStderr 但 operates 在
// string rather than raw command stderr 和 runs 在 配置 layer,
// 所以 arbitrary Expander 错误 文本 是 also sanitized.
func scrubErrorMessage(s string) string {
	if len(s) > maxResolveErrBytes {
		s = s[:maxResolveErrBytes]
	}
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\t' || c == '\n' || (c >= 0x20 && c < 0x7f) {
			out[i] = c
			continue
		}
		out[i] = '?'
	}
	return string(out)
}
