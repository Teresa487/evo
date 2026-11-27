// loop_detection.go —— 检测重复工具调用，防止 agent 卡在循环里。
//
// 对最近若干步的「工具名+输入+输出」做哈希，重复超阈值就提前停止本轮。

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"io"

	"charm.land/fantasy"
)

const (
	loopDetectionWindowSize = 10
	loopDetectionMaxRepeats = 5
)

// hasRepeatedToolCalls checks whether agent 是 stuck 在 loop 通过 looking
// 在 recent steps. 它 examines last windowSize steps 和 returns true 如果
// any 工具-调用 signature appears more than maxRepeats times.
func hasRepeatedToolCalls(steps []fantasy.StepResult, windowSize, maxRepeats int) bool {
	if len(steps) < windowSize {
		return false
	}

	window := steps[len(steps)-windowSize:]
	counts := make(map[string]int)

	for _, step := range window {
		sig := getToolInteractionSignature(step.Content)
		if sig == "" {
			continue
		}
		counts[sig]++
		if counts[sig] > maxRepeats {
			return true
		}
	}

	return false
}

// getToolInteractionSignature computes hash signature 为 工具
// interactions 在 single step's content. 它 pairs 工具 calls 用 their
// results (matched 通过 ToolCallID) 和 returns hex-encoded SHA-256 hash.
// If step contains no 工具 calls, it returns "".
func getToolInteractionSignature(content fantasy.ResponseContent) string {
	toolCalls := content.ToolCalls()
	if len(toolCalls) == 0 {
		return ""
	}

	// Index 工具 results 通过 their ToolCallID 为 fast lookup.
	resultsByID := make(map[string]fantasy.ToolResultContent)
	for _, tr := range content.ToolResults() {
		resultsByID[tr.ToolCallID] = tr
	}

	h := sha256.New()
	for _, tc := range toolCalls {
		output := ""
		if tr, ok := resultsByID[tc.ToolCallID]; ok {
			output = toolResultOutputString(tr.Result)
		}
		io.WriteString(h, tc.ToolName)
		io.WriteString(h, "\x00")
		io.WriteString(h, tc.Input)
		io.WriteString(h, "\x00")
		io.WriteString(h, output)
		io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// toolResultOutputString converts ToolResultOutputContent 到 stable string
// representation 为 signature comparison.
func toolResultOutputString(result fantasy.ToolResultOutputContent) string {
	if result == nil {
		return ""
	}
	if text, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](result); ok {
		return text.Text
	}
	if errResult, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result); ok {
		if errResult.Error != nil {
			return errResult.Error.Error()
		}
		return ""
	}
	if media, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](result); ok {
		return media.Data
	}
	return ""
}
