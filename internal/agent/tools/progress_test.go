package tools

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func newTestReporter() (*progressReporter, *[]ToolProgress) {
	var events []ToolProgress
	r := &progressReporter{
		sessionID:  "s1",
		toolCallID: "call1",
		publish:    func(p ToolProgress) { events = append(events, p) },
	}
	return r, &events
}

// TestProgressReporterSkipsUnchanged verifies 那 each change 是 published
// 和 repeats 的 same output 是 dropped.
func TestProgressReporterSkipsUnchanged(t *testing.T) {
	t.Parallel()
	r, events := newTestReporter()

	r.publishOutput("001", "1\n", "")
	require.Len(t, *events, 1)
	require.Equal(t, "1", (*events)[0].Output)
	require.Equal(t, "call1", (*events)[0].ToolCallID)
	require.Equal(t, "001", (*events)[0].ShellID)

	r.publishOutput("001", "1\n2\n", "")
	require.Len(t, *events, 2)
	require.Equal(t, "1\n2", (*events)[1].Output)

	r.publishOutput("001", "1\n2\n", "")
	require.Len(t, *events, 2, "unchanged output should be skipped")
}

func TestProgressReporterCombinesStderr(t *testing.T) {
	t.Parallel()
	r, events := newTestReporter()
	r.publishOutput("001", "out\n", "err\n")
	require.Equal(t, "out\nerr", (*events)[0].Output)
}

// TestProgressReporterNoToolCallID verifies 那 nothing 是 published 当
// there 是 no 工具调用 到 attach output 到.
func TestProgressReporterNoToolCallID(t *testing.T) {
	t.Parallel()
	r, events := newTestReporter()
	r.toolCallID = ""
	r.publishOutput("001", "x", "")
	require.Empty(t, *events)
}

func TestTailProgressOutputKeepsTail(t *testing.T) {
	t.Parallel()
	line := strings.Repeat("a", 99) + "\n"
	output := strings.Repeat(line, 2000) + "last"
	tail := tailProgressOutput(output)
	require.LessOrEqual(t, len(tail), progressMaxBytes)
	require.True(t, strings.HasSuffix(tail, "last"))
	require.True(t, strings.HasPrefix(tail, "a"), "tail should start on a line boundary")
}

func TestTailProgressOutputPreservesUTF8WithoutNewlines(t *testing.T) {
	t.Parallel()
	// Each "世" is 3 bytes; a 1-byte prefix makes the cut land mid-rune.
	output := "x" + strings.Repeat("世", progressMaxBytes)
	tail := tailProgressOutput(output)
	require.LessOrEqual(t, len(tail), progressMaxBytes)
	require.True(t, utf8.ValidString(tail))
	require.True(t, strings.HasPrefix(tail, "世"))
	require.True(t, strings.HasSuffix(tail, "世"))
}

func TestProgressReporterTrimsCRLFAndTrailingCR(t *testing.T) {
	t.Parallel()
	r, events := newTestReporter()

	r.publishOutput("001", "line1\r\nline2\r\n", "")
	require.Len(t, *events, 1)
	require.Equal(t, "line1\r\nline2", (*events)[0].Output)

	r.publishOutput("001", "50%\r60%\r", "")
	require.Len(t, *events, 2)
	require.Equal(t, "50%\r60%", (*events)[1].Output)
}

func TestProgressReporterSkipsUnchangedTail(t *testing.T) {
	t.Parallel()
	r, events := newTestReporter()
	bigStderr := strings.Repeat("e", progressMaxBytes+100)

	r.publishOutput("001", "first", bigStderr)
	require.Len(t, *events, 1)

	// Changing stdout 当 stderr alone fills tail must 不 re-发布.
	r.publishOutput("001", "first\nsecond", bigStderr)
	require.Len(t, *events, 1)
}

func TestNilProgressReporterIsSafe(t *testing.T) {
	t.Parallel()
	var r *progressReporter
	r.publishOutput("001", "hello", "")
	require.Nil(t, newProgressReporter(nil, "s1", "call1"))
}
