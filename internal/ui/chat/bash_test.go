package chat

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestBashToolMessageItem_CommandAlwaysExpanded guards the contract
// 那 bash command 是 always rendered expanded (newlines
// preserved) regardless 的 expansion state, while output body
// remains expandable 和 defaults 到 collapsed.
func TestBashToolMessageItem_CommandAlwaysExpanded(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{
		ID:       "bash1",
		Name:     "bash",
		Input:    `{"command":"echo one\necho two\necho three"}`,
		Finished: true,
	}

	render := func(expanded bool) string {
		ctx := &BashToolRenderContext{}
		return ctx.RenderTool(&sty, 120, &ToolRenderOpts{
			ToolCall:        tc,
			Result:          &message.ToolResult{ToolCallID: "bash1", Content: "out"},
			Status:          ToolStatusSuccess,
			ExpandedContent: expanded,
		})
	}

	for _, expanded := range []bool{false, true} {
		out := ansi.Strip(render(expanded))
		require.Contains(t, out, "echo one")
		require.Contains(t, out, "echo two")
		require.Contains(t, out, "echo three")
	}
}

// Sub-second runs 是 noise, 所以 bash header 只 shows longer ones.
func TestBashToolMessageItem_TookHidesSubSecond(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{ID: "bash-took", Name: "bash", Input: `{"command":"ls"}`, Finished: true}
	render := func(elapsed time.Duration) string {
		ctx := &BashToolRenderContext{}
		return ansi.Strip(ctx.RenderTool(&sty, 120, &ToolRenderOpts{
			ToolCall: tc,
			Result:   &message.ToolResult{ToolCallID: "bash-took", Content: "out"},
			Status:   ToolStatusSuccess,
			Elapsed:  elapsed,
		}))
	}

	require.NotContains(t, render(900*time.Millisecond), "took")
	require.Contains(t, render(3*time.Second), "took=3s")
}

// TestBashToolMessageItem_LongCommandNotTruncated verifies 那
// long bash command wraps 在 header instead 的 being truncated
// 用 ellipsis, even 当 output 是 collapsed.
func TestBashToolMessageItem_LongCommandNotTruncated(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	longCmd := "echo " + strings.Repeat("a", 200)
	tc := message.ToolCall{
		ID:       "bash3",
		Name:     "bash",
		Input:    `{"command":"` + longCmd + `"}`,
		Finished: true,
	}
	ctx := &BashToolRenderContext{}
	out := ansi.Strip(ctx.RenderTool(&sty, 120, &ToolRenderOpts{
		ToolCall:        tc,
		Result:          &message.ToolResult{ToolCallID: "bash3", Content: "out"},
		Status:          ToolStatusSuccess,
		ExpandedContent: false,
	}))

	require.NotContains(t, out, "…", "collapsed bash command must not be truncated")
	require.Contains(t, out, longCmd[strings.LastIndex(longCmd, "a")-10:], "end of command must be visible")
}

// TestBashToolMessageItem_OutputDefaultsCollapsed verifies 那 bash
// output 是 truncated 当 collapsed 和 fully shown 当 expanded.
func TestBashToolMessageItem_OutputDefaultsCollapsed(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{
		ID:       "bash2",
		Name:     "bash",
		Input:    `{"command":"seq 20"}`,
		Finished: true,
	}
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	result := &message.ToolResult{ToolCallID: "bash2", Content: strings.Join(lines, "\n")}

	item := NewBashToolMessageItem(&sty, tc, result, false, "")
	bash, ok := item.(*BashToolMessageItem)
	require.True(t, ok, "NewBashToolMessageItem must return a *BashToolMessageItem")
	require.False(t, bash.expandedContent, "bash items must default to collapsed")

	renderCollapsed := ansi.Strip(bash.RawRender(120))
	require.Contains(t, renderCollapsed, "line10")
	require.NotContains(t, renderCollapsed, "line20")
	require.Contains(t, renderCollapsed, "… (10 lines hidden)")

	require.True(t, bash.ToggleExpanded(), "first toggle should report expanded")
	require.Contains(t, ansi.Strip(bash.RawRender(120)), "line20")
}

// TestToolOutputPlainContent_SingleHiddenLineShown verifies 那
// collapsing never hides just one line: content one line over the
// collapsed limit 是 shown 在 full, while more over 那 truncates.
func TestToolOutputPlainContent_SingleHiddenLineShown(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	var lines []string
	for i := 1; i <= 11; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	out := ansi.Strip(toolOutputPlainContent(&sty, strings.Join(lines, "\n"), 80, false))
	require.Contains(t, out, "line11")
	require.NotContains(t, out, "hidden")

	lines = append(lines, "line12")
	out = ansi.Strip(toolOutputPlainContent(&sty, strings.Join(lines, "\n"), 80, false))
	require.NotContains(t, out, "line12")
	require.Contains(t, out, "2 lines hidden")
}

// renderTool renders finished 工具调用 用 given input 和 结果
// metadata, both marshalled 到 JSON 所以 native paths survive 在 Windows.
func renderTool(t *testing.T, r ToolRenderer, name string, input, metadata map[string]any, expanded, compact bool) string {
	t.Helper()
	sty := styles.CharmtonePantera()
	in, err := json.Marshal(input)
	require.NoError(t, err)
	var meta []byte
	if metadata != nil {
		meta, err = json.Marshal(metadata)
		require.NoError(t, err)
	}
	return ansi.Strip(r.RenderTool(&sty, 120, &ToolRenderOpts{
		ToolCall:        message.ToolCall{ID: "tc", Name: name, Input: string(in), Finished: true},
		Result:          &message.ToolResult{ToolCallID: "tc", Content: "ok", Metadata: string(meta)},
		Status:          ToolStatusSuccess,
		ExpandedContent: expanded,
		Compact:         compact,
	}))
}

// TestBashToolMessageItem_CwdHint verifies 那 command 运行 outside
// 工作区 shows "cwd=" option, 和 那 it stays quiet otherwise.
func TestBashToolMessageItem_CwdHint(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	ctx := &BashToolRenderContext{workingDir: workspace}
	render := func(input, metadata map[string]any) string {
		return renderTool(t, ctx, "bash", input, metadata, false, false)
	}

	out := render(map[string]any{"command": "go test ./...", "working_dir": filepath.Join(workspace, "internal", "ui")}, nil)
	require.Contains(t, out, "go test ./... (cwd=internal/ui)")

	out = render(map[string]any{"command": "ls", "working_dir": filepath.Join("internal", "ui")}, nil)
	require.Contains(t, out, "(cwd=internal/ui)", "relative dirs resolve against the workspace")

	out = render(
		map[string]any{"command": "ls", "working_dir": filepath.Join(workspace, "internal")},
		map[string]any{"working_directory": filepath.Join(workspace, "other")},
	)
	require.Contains(t, out, "(cwd=other)", "result metadata wins over the input")

	out = render(map[string]any{"command": "ls", "working_dir": workspace}, nil)
	require.NotContains(t, out, "cwd=")

	out = render(map[string]any{"command": "cd sub && ls", "working_dir": filepath.Join(workspace, "sub")}, nil)
	require.NotContains(t, out, "cwd=", "a leading cd already says where it runs")

	out = render(
		map[string]any{"command": "make build", "run_in_background": true},
		map[string]any{"background": true, "shell_id": "sh-1", "working_directory": filepath.Join(workspace, "sub"), "description": "build it"},
	)
	require.Contains(t, out, "PID sh-1 build it (cwd=sub)")
}

// TestCwdHint covers 路径 handling 在 它的 own.
func TestCwdHint(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.Empty(t, cwdHint(workspace, "", "ls"))
	require.Empty(t, cwdHint("", filepath.Join(workspace, "sub"), "ls"))
	require.Empty(t, cwdHint(workspace, workspace, "ls"))
	require.Equal(t, "sub", cwdHint(workspace, "sub", "ls"))
	require.Equal(t, "../elsewhere", cwdHint(workspace, filepath.Join(filepath.Dir(workspace), "elsewhere"), "ls"))
}

// TestJobOutputToolMessageItem_CwdHint verifies 那 job output carries
// cwd hint 的 background shell it 读取 从.
func TestJobOutputToolMessageItem_CwdHint(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	ctx := &JobOutputToolRenderContext{workingDir: workspace}
	out := renderTool(t, ctx, "job_output",
		map[string]any{"shell_id": "sh-1"},
		map[string]any{"shell_id": "sh-1", "description": "serve", "working_directory": filepath.Join(workspace, "sub")},
		false, false)
	require.Contains(t, out, "serve (cwd=sub)")
}

// TestBashToolMessageItem_DescriptionHeader verifies 那 模型's
// description leads header, 用 command blank line below it,
// 在 column 工具 name 和 output start 在.
func TestBashToolMessageItem_DescriptionHeader(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	ctx := &BashToolRenderContext{workingDir: workspace}
	render := func(input map[string]any, expanded, compact bool) []string {
		return strings.Split(renderTool(t, ctx, "bash", input, nil, expanded, compact), "\n")
	}
	commandLine := regexp.MustCompile(`^\s+echo hi\s*$`)

	longDesc := map[string]any{"command": "echo hi", "description": strings.Repeat("d", 200)}

	collapsed := render(longDesc, false, false)
	require.Contains(t, collapsed[0], "…")
	require.LessOrEqual(t, lipgloss.Width(collapsed[0]), cappedMessageWidth(120))
	require.Empty(t, strings.TrimSpace(collapsed[1]), "a blank line separates description and command")
	require.Regexp(t, commandLine, collapsed[2], "command lines up with the tool name")

	expanded := render(longDesc, true, false)
	joined := strings.Join(expanded, "\n")
	require.NotContains(t, joined, "…", "expanded description wraps instead of truncating")
	require.Regexp(t, `^\s+d+\s*$`, expanded[1])
	require.Regexp(t, commandLine, expanded[len(expanded)-3])

	multi := map[string]any{"command": "echo hi", "description": "run the echo\nthen the suite"}
	require.NotContains(t, strings.Join(render(multi, false, false), "\n"), "then the suite")
	require.Contains(t, strings.Join(render(multi, true, false), "\n"), "then the suite")

	out := render(map[string]any{"command": "sleep 5", "description": "wait", "run_in_background": true}, false, false)
	require.Contains(t, out[0], "wait (background=true)")

	out = render(map[string]any{"command": "echo hi", "working_dir": filepath.Join(workspace, "sub"), "description": "do things"}, false, false)
	require.Contains(t, out[0], "Bash do things (cwd=sub)")

	out = render(map[string]any{"command": "echo hi", "description": "do things"}, false, true)
	require.Contains(t, out[0], "echo hi", "nested calls keep the plain command header")
	require.NotContains(t, strings.Join(out, "\n"), "do things")

	out = render(map[string]any{"command": "echo hi", "description": ""}, false, false)
	require.Contains(t, out[0], "echo hi")
}

// 一个 子代理's 工具 calls render one line each, 所以 multi-line command
// collapses onto 它的 first 行 instead 的 spilling few lines 的 inline
// script into the tree.
func TestBashToolMessageItem_CompactCommandIsOneLine(t *testing.T) {
	t.Parallel()

	ctx := &BashToolRenderContext{workingDir: t.TempDir()}
	cmd := "cd ~/.local/share/crush && python3 -c \"\nimport json\n    d=json.load(open('hyper.json'))\n\""
	out := renderTool(t, ctx, "bash", map[string]any{"command": cmd}, nil, false, true)
	require.NotContains(t, out, "\n")
	require.Contains(t, out, `python3 -c " import json d=json.load(`)

	out = renderTool(t, ctx, "bash", map[string]any{"command": cmd}, nil, true, false)
	require.Contains(t, out, "\n", "a top-level call still shows the command as written")
}
