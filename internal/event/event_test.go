package event

// These tests verify 那 错误 function correctly handles various
// scenarios. These tests will 不 log anything.

import (
	"reflect"
	"testing"

	"github.com/posthog/posthog-go"
)

func TestSetNonInteractive(t *testing.T) {
	originalNonInteractive := baseProps[nonInteractiveAttrName]
	originalNonInteractiveNested := baseProps[nonInteractiveNestedAttrName]
	t.Cleanup(func() {
		baseProps = baseProps.
			Set(nonInteractiveAttrName, originalNonInteractive).
			Set(nonInteractiveNestedAttrName, originalNonInteractiveNested)
	})

	tests := []struct {
		name                     string
		nonInteractive           bool
		evo                      string
		wantNonInteractiveNested bool
	}{
		{
			name: "interactive direct invocation",
		},
		{
			name:           "non-interactive direct invocation",
			nonInteractive: true,
		},
		{
			name: "interactive nested invocation",
			evo:  "1",
		},
		{
			name:                     "non-interactive nested invocation",
			nonInteractive:           true,
			evo:                      "1",
			wantNonInteractiveNested: true,
		},
		{
			name:           "non-interactive invocation with unrecognized marker",
			nonInteractive: true,
			evo:            "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EVO", tt.evo)
			SetNonInteractive(tt.nonInteractive)

			if got := baseProps[nonInteractiveAttrName]; got != tt.nonInteractive {
				t.Errorf("%s = %v, want %v", nonInteractiveAttrName, got, tt.nonInteractive)
			}
			if got := baseProps[nonInteractiveNestedAttrName]; got != tt.wantNonInteractiveNested {
				t.Errorf("%s = %v, want %v", nonInteractiveNestedAttrName, got, tt.wantNonInteractiveNested)
			}
		})
	}
}

func TestError(t *testing.T) {
	t.Run("returns early when client is nil", func(t *testing.T) {
		// 这 测试 verifies 那 当 PostHog 客户端 是 不 initialized
		// 错误 function safely returns early without attempting 到
		// enqueue any events. 这 是 important during initialization 或 当
		// metrics 是 disabled, 作为 we don't want 错误 reporting mechanism
		// itself 到 cause panics.
		originalClient := client
		defer func() {
			client = originalClient
		}()

		client = nil
		Error("test error", "key", "value")
	})

	t.Run("handles nil client without panicking", func(t *testing.T) {
		// 这 测试 covers various edge cases where 错误 值 might 是
		// nil, string, 或 错误 type.
		originalClient := client
		defer func() {
			client = originalClient
		}()

		client = nil
		Error(nil)
		Error("some error")
		Error(newDefaultTestError("runtime error"), "key", "value")
	})

	t.Run("handles error with properties", func(t *testing.T) {
		// 这 测试 verifies 那 错误 function can handle additional
		// key-值 properties 那 provide 上下文 about 错误. These
		// properties 是 typically passed 当 recovering 从 panics (i.e.,
		// panic name, function name).
		//
		// Even 用 these additional properties, function should handle
		// them gracefully without panicking.
		originalClient := client
		defer func() {
			client = originalClient
		}()

		client = nil
		Error(
			"test error",
			"type", "test",
			"severity", "high",
			"source", "unit-test",
		)
	})
}

func TestPairsToProps(t *testing.T) {
	t.Run("sets valid key value pairs", func(t *testing.T) {
		got := pairsToProps("foo", "bar", "count", 3)
		want := posthog.NewProperties().
			Set("foo", "bar").
			Set("count", 3)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pairsToProps() = %#v, want %#v", got, want)
		}
	})

	t.Run("returns empty properties for odd pairs", func(t *testing.T) {
		got := pairsToProps("foo", "bar", "count")
		if len(got) != 0 {
			t.Fatalf("pairsToProps() should return empty properties, got %#v", got)
		}
	})

	t.Run("ignores non-string key and continues", func(t *testing.T) {
		got := pairsToProps(123, "bad", "ok", true)
		want := posthog.NewProperties().Set("ok", true)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pairsToProps() = %#v, want %#v", got, want)
		}
	})
}

// newDefaultTestError creates 测试 错误 那 mimics runtime panic
// errors. 这 helps us testing 那 错误 function can handle various
// 错误 types, including those 那 might 是 passed 从 panic recovery
// scenario.
func newDefaultTestError(s string) error {
	return testError(s)
}

type testError string

func (e testError) Error() string {
	return string(e)
}
