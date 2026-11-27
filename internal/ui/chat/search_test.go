package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/anim"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestGrepToolMessageItem_PendingShowsSearchParameters guards 那 running
// grep renders 它的 search parameters next 到 spinner instead 的 hiding
// them behind a bare "Grep" label.
func TestGrepToolMessageItem_PendingShowsSearchParameters(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	ctx := &GrepToolRenderContext{}
	out := ansi.Strip(ctx.RenderTool(&sty, 120, &ToolRenderOpts{
		ToolCall: message.ToolCall{
			ID:       "grep1",
			Name:     "grep",
			Input:    `{"pattern":"A9041","path":".","include":"*.go","literal_text":true}`,
			Finished: false,
		},
		Status: ToolStatusRunning,
	}))

	require.Contains(t, out, "Grep")
	require.Contains(t, out, "A9041")
	require.Contains(t, out, "path=.")
	require.Contains(t, out, "include=*.go")
	require.Contains(t, out, "literal=true")
	require.NotContains(t, out, "Invalid parameters")
}

// TestGrepToolMessageItem_PendingPartialInputFallsBackToSpinner guards 那
// while 工具调用 input 是 still streaming 和 no 字段 usable 为
// header exists yet (nothing streamed, key 不 closed, 或 值 不
// started), pending state falls back 到 plain spinner instead 的
// flashing "Invalid parameters" 错误.
func TestGrepToolMessageItem_PendingPartialInputFallsBackToSpinner(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	ctx := &GrepToolRenderContext{}
	streamingPrefixes := []string{"", "{", `{"pattern"`, `{"pattern":`}

	for _, compact := range []bool{false, true} {
		for _, input := range streamingPrefixes {
			a := anim.New(anim.Settings{ID: "grep-anim", Label: "Working"})
			out := ctx.RenderTool(&sty, 120, &ToolRenderOpts{
				ToolCall: message.ToolCall{
					ID:       "grep2",
					Name:     "grep",
					Input:    input,
					Finished: false,
				},
				Status:  ToolStatusRunning,
				Anim:    a,
				Compact: compact,
			})
			require.Equal(t, pendingTool(&sty, "Grep", a, compact), out,
				"pending grep with partial input %q must render the plain spinner (compact=%v)", input, compact)
		}
	}
}

// TestGrepToolMessageItem_PendingPartialInputShowsCompleteFields guards 那
// fields whose JSON 值 already closed 是 rendered while rest 的
// input 是 still streaming, 所以 search 上下文 appears progressively.
func TestGrepToolMessageItem_PendingPartialInputShowsCompleteFields(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	ctx := &GrepToolRenderContext{}

	tests := []struct {
		name     string
		input    string
		contains []string
		missing  []string
	}{
		{
			name:     "pattern closed, object not",
			input:    `{"pattern":"fo"`,
			contains: []string{"fo"},
			missing:  []string{"path=", "include=", "literal="},
		},
		{
			name:     "path closed, object not",
			input:    `{"pattern":"foo","path":"."`,
			contains: []string{"foo", "path=."},
			missing:  []string{"include=", "literal="},
		},
		{
			name:     "all fields closed, closing brace missing",
			input:    `{"pattern":"foo","path":".","include":"*.go","literal_text":true`,
			contains: []string{"foo", "path=.", "include=*.go", "literal=true"},
			missing:  []string{},
		},
		{
			name:     "wrongly typed pattern shows no header at all",
			input:    `{"pattern":123,"path":"."`,
			contains: []string{"Grep"},
			missing:  []string{"123", "path="},
		},
		{
			name:     "pattern grows while its value streams",
			input:    `{"pattern":"fo`,
			contains: []string{"fo"},
			missing:  []string{"path=", "Invalid parameters"},
		},
		{
			name:     "cut escape at end of streaming value",
			input:    `{"pattern":"a\u00`,
			contains: []string{"Grep a"},
			missing:  []string{"u00"},
		},
		{
			name:     "escaped quote inside streaming value",
			input:    `{"pattern":"a\"b`,
			contains: []string{`a"b`},
			missing:  []string{},
		},
		{
			name:     "partial next key keeps complete pattern",
			input:    `{"pattern":"foo","pa`,
			contains: []string{"foo"},
			missing:  []string{"pa"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := ansi.Strip(ctx.RenderTool(&sty, 120, &ToolRenderOpts{
				ToolCall: message.ToolCall{
					ID:       "grep5",
					Name:     "grep",
					Input:    tt.input,
					Finished: false,
				},
				Status: ToolStatusRunning,
			}))

			for _, want := range tt.contains {
				require.Contains(t, out, want)
			}
			for _, notWant := range tt.missing {
				require.NotContains(t, out, notWant)
			}
			require.NotContains(t, out, "Invalid parameters")
		})
	}
}

// TestGrepToolMessageItem_PendingHeaderMatchesCompletedHeader guards 那
// pending state renders very same header 作为 completed state, 所以
// nested (compact) grep calls keep their styling while loading.
func TestGrepToolMessageItem_PendingHeaderMatchesCompletedHeader(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	ctx := &GrepToolRenderContext{}
	tc := message.ToolCall{
		ID:    "grep3",
		Name:  "grep",
		Input: `{"pattern":"foo","path":"."}`,
	}
	opts := func(finished bool) *ToolRenderOpts {
		tc := tc
		tc.Finished = finished
		return &ToolRenderOpts{
			ToolCall: tc,
			Status:   ToolStatusRunning,
			Compact:  true,
		}
	}

	a := anim.New(anim.Settings{ID: "grep-anim", Label: "Working"})
	pendingOpts := opts(false)
	pendingOpts.Anim = a
	pending := ctx.RenderTool(&sty, 120, pendingOpts)
	completed := ctx.RenderTool(&sty, 120, opts(true))

	require.Equal(t, completed+" "+a.Render(), pending,
		"pending compact grep must reuse the completed (nested-styled) header plus the spinner")
}

// TestGrepToolMessageItem_InvalidInputStillErrorsWhenFinished guards 那
// fallback 只 applies 到 pending calls: once 调用 是 finished, invalid
// JSON still renders "Invalid parameters" 错误.
func TestGrepToolMessageItem_InvalidInputStillErrorsWhenFinished(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	ctx := &GrepToolRenderContext{}
	out := ansi.Strip(ctx.RenderTool(&sty, 120, &ToolRenderOpts{
		ToolCall: message.ToolCall{
			ID:       "grep4",
			Name:     "grep",
			Input:    `{"pattern":"fo`,
			Finished: true,
		},
		Status: ToolStatusError,
	}))

	require.Contains(t, out, "Invalid parameters")
}
