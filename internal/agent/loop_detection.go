// loop_detection.go —— 限制本轮的工具调用规模，并检测重复调用，防止 agent 卡住。
//
// 两条独立的停止条件：
//   - 本轮累计工具调用数达到上限就停，避免停不下来时无止境地跑；
//   - 对最近若干步的「工具名+输入+输出」做哈希，重复超阈值也提前停止。

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

// maxToolCallsPerTurn 是一轮对话允许发起的工具调用数量上限。
// 正常的长任务远用不到，它只在模型停不下来时收尾。
const maxToolCallsPerTurn = 50

// toolCallsReachedLimit 报告本轮累计发起的工具调用是否已达到上限。
// steps 中每一步对应一次模型响应，一步可能包含多个并行调用。
func toolCallsReachedLimit(steps []fantasy.StepResult, limit int) bool {
	calls := 0
	for _, step := range steps {
		calls += len(step.Content.ToolCalls())
		if calls >= limit {
			return true
		}
	}
	return false
}

// hasRepeatedToolCalls 判断 agent 是否卡在循环里：检查最近 windowSize 步，
// 只要有任一「工具调用签名」出现次数超过 maxRepeats 就返回 true。
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

// getToolInteractionSignature 为单步内容里的工具交互计算哈希签名：
// 把工具调用与它的结果（按 ToolCallID 配对）一起哈希，返回十六进制 SHA-256。
// 该步没有工具调用时返回空字符串。
func getToolInteractionSignature(content fantasy.ResponseContent) string {
	toolCalls := content.ToolCalls()
	if len(toolCalls) == 0 {
		return ""
	}

	// 按 ToolCallID 建立结果索引，便于快速配对。
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

// toolResultOutputString 把工具结果转换成稳定字符串，用于比较签名。
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
