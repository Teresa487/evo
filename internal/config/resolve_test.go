package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/env"
	"github.com/stretchr/testify/require"
)

// fakeExpander returns canned 值/错误 为 last passed 值 和
// records 上下文, raw 值, 和 env slice it was called 用. 它
// lets 配置-layer tests assert 在 delegation behaviour without
// spinning up real interpreter — real-shell coverage lives 在
// internal/shell/expand_test.go 和 resolve_real_test.go.
type fakeExpander struct {
	expand    func(ctx context.Context, value string, env []string) (string, error)
	lastValue string
	lastEnv   []string
	calls     int
}

func (f *fakeExpander) Expand(ctx context.Context, value string, env []string) (string, error) {
	f.calls++
	f.lastValue = value
	f.lastEnv = env
	if f.expand == nil {
		return value, nil
	}
	return f.expand(ctx, value, env)
}

func TestShellVariableResolver_DelegatesToExpander(t *testing.T) {
	t.Parallel()

	fe := &fakeExpander{
		expand: func(_ context.Context, value string, _ []string) (string, error) {
			if value == "hello $FOO" {
				return "hello bar", nil
			}
			return value, nil
		},
	}

	e := env.NewFromMap(map[string]string{"FOO": "bar"})
	r := NewShellVariableResolver(e, WithExpander(fe.Expand))

	got, err := r.ResolveValue("hello $FOO")
	require.NoError(t, err)
	require.Equal(t, "hello bar", got)
	require.Equal(t, 1, fe.calls)
	require.Equal(t, "hello $FOO", fe.lastValue)
	require.Contains(t, fe.lastEnv, "FOO=bar")
}

func TestShellVariableResolver_LoneDollarIsError(t *testing.T) {
	t.Parallel()

	// Lone "$" 必须 short-circuit before reaching expander:
	// underlying shell parser would 受理 it 作为 literal, 但 这
	// resolver has historically rejected it 和 callers depend 在
	// 那 early-fail behaviour.
	fe := &fakeExpander{}
	r := NewShellVariableResolver(env.NewFromMap(nil), WithExpander(fe.Expand))

	_, err := r.ResolveValue("$")
	require.Error(t, err)
	require.Equal(t, 0, fe.calls, "expander must not be called for lone $")
}

func TestShellVariableResolver_PassesThroughLiterals(t *testing.T) {
	t.Parallel()

	fe := &fakeExpander{
		expand: func(_ context.Context, value string, _ []string) (string, error) {
			return value, nil
		},
	}
	r := NewShellVariableResolver(env.NewFromMap(nil), WithExpander(fe.Expand))

	got, err := r.ResolveValue("plain-string")
	require.NoError(t, err)
	require.Equal(t, "plain-string", got)
}

func TestShellVariableResolver_WrapsErrorsWithTemplate(t *testing.T) {
	t.Parallel()

	inner := errors.New("cat: /run/secrets/x: permission denied")
	fe := &fakeExpander{
		expand: func(_ context.Context, _ string, _ []string) (string, error) {
			return "", inner
		},
	}
	r := NewShellVariableResolver(env.NewFromMap(nil), WithExpander(fe.Expand))

	_, err := r.ResolveValue("$(cat /run/secrets/x)")
	require.Error(t, err)
	require.ErrorIs(t, err, inner)
	require.Contains(t, err.Error(), "$(cat /run/secrets/x)")
	require.Contains(t, err.Error(), "permission denied")
}

func TestSanitizeResolveError(t *testing.T) {
	t.Parallel()

	t.Run("nil passes through", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, sanitizeResolveError("anything", nil))
	})

	t.Run("includes template and wraps inner", func(t *testing.T) {
		t.Parallel()
		inner := errors.New("cat: /run/secrets/x: permission denied")
		got := sanitizeResolveError("$(cat /run/secrets/x)", inner)
		require.Error(t, got)
		require.ErrorIs(t, got, inner)
		require.Contains(t, got.Error(), "$(cat /run/secrets/x)")
		require.Contains(t, got.Error(), "permission denied")
	})

	t.Run("unwrap preserves original for errors.Is", func(t *testing.T) {
		t.Parallel()
		inner := errors.New("sentinel")
		got := sanitizeResolveError("$FOO", inner)
		require.ErrorIs(t, got, inner)
	})

	t.Run("truncates over-budget inner message", func(t *testing.T) {
		t.Parallel()
		// Inner 消息 holds far more than budget. After
		// sanitization rendered inner portion must 不 exceed
		// maxResolveErrBytes, 和 characters beyond budget
		// (marked 通过 distinct tail sentinel) must 是 gone.
		const tailSentinel = "TAIL_SENTINEL_BEYOND_BUDGET"
		body := strings.Repeat("x", maxResolveErrBytes)
		inner := errors.New(body + tailSentinel)

		got := sanitizeResolveError("$TEMPLATE", inner)
		require.Error(t, got)

		prefix := `resolving "$TEMPLATE": `
		rendered := got.Error()
		require.True(
			t,
			strings.HasPrefix(rendered, prefix),
			"rendered error must start with template prefix",
		)
		innerRendered := strings.TrimPrefix(rendered, prefix)
		require.LessOrEqual(
			t,
			len(innerRendered),
			maxResolveErrBytes,
			"inner message must be bounded to maxResolveErrBytes",
		)
		require.NotContains(
			t,
			rendered,
			tailSentinel,
			"content past the budget must not leak",
		)
	})

	t.Run("replaces non-printable bytes", func(t *testing.T) {
		t.Parallel()
		// NUL, BEL, ESC, DEL, 和 UTF-8 high byte should all 是
		// scrubbed 到 '?'. Tab 和 newline 是 preserved because
		// they show up legitimately 在 command stderr.
		inner := errors.New("ok\x00bad\x07\x1b\x7f\xffend\ttab\nline")
		got := sanitizeResolveError("$T", inner)
		rendered := got.Error()

		require.NotContains(t, rendered, "\x00")
		require.NotContains(t, rendered, "\x07")
		require.NotContains(t, rendered, "\x1b")
		require.NotContains(t, rendered, "\x7f")
		require.NotContains(t, rendered, "\xff")
		require.Contains(t, rendered, "ok?bad????end\ttab\nline")
	})

	t.Run("scrubbing does not depend on shell.ExpandValue upstream", func(t *testing.T) {
		t.Parallel()
		// 一个 custom Expander can inject arbitrary 错误 文本.
		// 配置-layer helper 是 single chokepoint; it must
		// bound + scrub regardless 的 错误 source.
		nasty := strings.Repeat("A", maxResolveErrBytes+64) + "\x00BEYOND"
		fe := &fakeExpander{
			expand: func(_ context.Context, _ string, _ []string) (string, error) {
				return "", errors.New(nasty)
			},
		}
		r := NewShellVariableResolver(env.NewFromMap(nil), WithExpander(fe.Expand))

		_, err := r.ResolveValue("$T")
		require.Error(t, err)
		require.NotContains(t, err.Error(), "BEYOND", "over-budget tail must not leak")
		require.NotContains(t, err.Error(), "\x00", "non-printables must be scrubbed")
	})
}

func TestScrubErrorMessage(t *testing.T) {
	t.Parallel()

	t.Run("bounds output to maxResolveErrBytes", func(t *testing.T) {
		t.Parallel()
		got := scrubErrorMessage(strings.Repeat("a", maxResolveErrBytes*3))
		require.Len(t, got, maxResolveErrBytes)
	})

	t.Run("preserves printable ASCII tab and newline", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "a\tb\nc d!", scrubErrorMessage("a\tb\nc d!"))
	})

	t.Run("replaces control and non-ASCII bytes", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "a?b??c", scrubErrorMessage("a\x01b\x1b\xe2c"))
	})
}

func TestNewShellVariableResolver(t *testing.T) {
	testEnv := env.NewFromMap(map[string]string{"TEST": "value"})
	resolver := NewShellVariableResolver(testEnv)

	require.NotNil(t, resolver)
	require.Implements(t, (*VariableResolver)(nil), resolver)
}
