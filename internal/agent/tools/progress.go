// progress.go —— 工具执行进度的上报。

package tools

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/shell"
)

const (
	// progressPollInterval 是 how often running command's output 是
	// checked 和, 如果 it changed, published. 它 also caps 更新 rate,
	// 所以 chatty command cannot flood UI.
	progressPollInterval = 200 * time.Millisecond

	// progressMaxBytes bounds trailing output carried 通过 one 事件.
	progressMaxBytes = 64 * 1024
)

// ToolProgress carries live output 为 running 工具调用. 它 是 display
// 只: it 是 never persisted 或 sent 到 模型, 和 工具结果
// replaces it.
type ToolProgress struct {
	SessionID  string `json:"session_id"`
	ToolCallID string `json:"tool_call_id"`
	ShellID    string `json:"shell_id,omitempty"`
	Output     string `json:"output"`
}

// progressReporter publishes size-limited live output 为 one 工具调用.
type progressReporter struct {
	sessionID  string
	toolCallID string
	publish    func(ToolProgress)

	lastSeq    uint64
	lastOutput string
}

func newProgressReporter(pub pubsub.Publisher[ToolProgress], sessionID, toolCallID string) *progressReporter {
	if pub == nil || toolCallID == "" {
		return nil
	}
	return &progressReporter{
		sessionID:  sessionID,
		toolCallID: toolCallID,
		publish: func(p ToolProgress) {
			pub.Publish(pubsub.UpdatedEvent, p)
		},
	}
}

// 报告 publishes shell's current output 如果 it changed since last
// 事件.
func (r *progressReporter) report(bgShell *shell.BackgroundShell) {
	if r == nil || r.publish == nil || r.toolCallID == "" {
		return
	}
	seq := bgShell.OutputSeq()
	if seq == r.lastSeq {
		return
	}
	r.lastSeq = seq
	stdout, stderr := bgShell.GetOutputTail(progressMaxBytes)
	r.publishOutput(bgShell.ID, stdout, stderr)
}

func (r *progressReporter) publishOutput(shellID, stdout, stderr string) {
	if r == nil || r.publish == nil || r.toolCallID == "" {
		return
	}
	tail := tailProgressOutput(combineProgressOutput(stdout, stderr))
	if tail == r.lastOutput {
		return
	}
	r.lastOutput = tail
	r.publish(ToolProgress{
		SessionID:  r.sessionID,
		ToolCallID: r.toolCallID,
		ShellID:    shellID,
		Output:     tail,
	})
}

// combineProgressOutput joins stdout 和 stderr way final 工具
// 结果 shows them.
func combineProgressOutput(stdout, stderr string) string {
	stdout = strings.TrimRight(stdout, "\r\n")
	stderr = strings.TrimRight(stderr, "\r\n")
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}

// tailProgressOutput keeps last progressMaxBytes 的 output, starting 在
// line boundary 当 one 是 available, 和 preserving UTF-8 boundaries.
func tailProgressOutput(output string) string {
	if len(output) <= progressMaxBytes {
		return output
	}
	tail := output[len(output)-progressMaxBytes:]
	if idx := strings.IndexByte(tail, '\n'); idx >= 0 && idx < len(tail)-1 {
		tail = tail[idx+1:]
	} else {
		skip := 0
		for skip < len(tail) && skip < utf8.UTFMax-1 && !utf8.RuneStart(tail[skip]) {
			skip++
		}
		tail = tail[skip:]
	}
	return strings.Clone(tail)
}
