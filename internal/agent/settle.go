// settle.go —— 轮次收尾：给未回答的工具调用补结果，修复中断的工具状态。
//
// Run 结束后调用 settleTurn，保证消息历史里工具调用与结果成对。

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/message"
)

// Replies recorded 为 工具调用 那 will never 是 answered. They 是
// 存储 在 transcript, 所以 they 是 also what 模型 reads next turn.
const (
	canceledToolCallResult    = "Error: user cancelled assistant tool calling"
	failedToolCallResult      = "There was an error while executing the tool"
	truncatedToolCallResult   = "the model ran out of output tokens before it finished writing this tool call, so the call was never run; retry with smaller arguments, splitting the work across several calls if that helps"
	refusedToolCallResult     = "the provider's safety classifier stopped this response before the tool call was finished, so the call was never run; rephrase the request or try a different model"
	abandonedToolCallResult   = "the turn ended before the model finished writing this tool call, so the call was never run; retry the call if the result is still needed"
	interruptedToolCallResult = "tool call was interrupted and did not produce a result, you may retry this call if the result is still needed"
	interruptedTurnMessage    = "Interrupted"
)

// toolResultsForCalls builds 工具 消息 那 must immediately follow
// 助手消息 用 工具 calls. toolResultsByCall holds one 结果
// per 调用 和 是 consumed, 所以 结果 存储 alongside results 为
// other assistants 是 emitted once, next 到 调用 那 asked 为 it. 一个
// 调用 用 no 存储 结果 gets synthetic one; settling should have made
// 那 impossible, 所以 it 是 logged 作为 warning.
func toolResultsForCalls(m message.Message, toolResultsByCall map[string]fantasy.ToolResultPart) fantasy.Message {
	content := make([]fantasy.MessagePart, 0, len(m.ToolCalls()))
	for _, tc := range m.ToolCalls() {
		result, ok := toolResultsByCall[tc.ID]
		delete(toolResultsByCall, tc.ID)
		if ok {
			content = append(content, result)
			continue
		}
		slog.Warn(
			"Injecting synthetic tool result for orphaned tool call",
			"tool_call_id", tc.ID,
			"tool_name", tc.Name,
		)
		content = append(content, fantasy.ToolResultPart{
			ToolCallID: tc.ID,
			Output: fantasy.ToolResultOutputContentError{
				Error: errors.New(interruptedToolCallResult),
			},
		})
	}
	return fantasy.Message{
		Role:    fantasy.MessageRoleTool,
		Content: content,
	}
}

// turnEndReason reports what 到 tell 模型 about 调用 它的 turn left
// unanswered. Why turn stopped decides what 重试 should change.
func turnEndReason(assistant *message.Message, runErr error) string {
	switch {
	case errors.Is(runErr, context.Canceled):
		return canceledToolCallResult
	case errors.Is(runErr, errRefused):
		return refusedToolCallResult
	case runErr != nil:
		return failedToolCallResult
	}
	finish := assistant.FinishPart()
	if finish == nil {
		return abandonedToolCallResult
	}
	switch finish.Reason {
	case message.FinishReasonMaxTokens:
		return truncatedToolCallResult
	case message.FinishReasonContentFilter:
		return refusedToolCallResult
	default:
		return abandonedToolCallResult
	}
}

// settleTurn answers every 调用 在 turn's last 助手消息 那
// has no reply yet. Every way turn ends reaches 这, 所以 调用 can never
// outlive turn 那 made it.
func (a *sessionAgent) settleTurn(ctx context.Context, assistant *message.Message, answered func(toolCallID string) bool, runErr error) error {
	var open []message.ToolCall
	for _, tc := range assistant.ToolCalls() {
		if !answered(tc.ID) {
			open = append(open, tc)
		}
	}
	_, err := settleToolCalls(ctx, a.messages, assistant, open, turnEndReason(assistant, runErr))
	return err
}

// settleToolCalls marks calls 完成 和 records reason 作为 each one's
// reply. Partial arguments 是 kept 作为 written; send 路径 makes them
// valid JSON. created replies 是 returned 在 order written.
func settleToolCalls(ctx context.Context, messages message.Service, assistant *message.Message, calls []message.ToolCall, reason string) ([]message.Message, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	for _, tc := range calls {
		slog.Warn("Settling a tool call that will never be answered",
			"session_id", assistant.SessionID,
			"tool_call_id", tc.ID,
			"tool_name", tc.Name,
			"reason", reason)
		if !tc.Finished {
			tc.Finished = true
			assistant.AddToolCall(tc)
		}
	}
	if err := messages.Update(ctx, *assistant); err != nil {
		return nil, fmt.Errorf("failed to settle tool calls: %w", err)
	}

	settled := make([]message.Message, 0, len(calls))
	for _, tc := range calls {
		created, err := messages.Create(ctx, assistant.SessionID, message.CreateMessageParams{
			Role: message.Tool,
			Parts: []message.ContentPart{message.ToolResult{
				ToolCallID:   tc.ID,
				Name:         tc.Name,
				Content:      reason,
				IsError:      true,
				FinishedAtMs: time.Now().UnixMilli(),
			}},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to record settled tool result: %w", err)
		}
		settled = append(settled, created)
	}
	return settled, nil
}

// repairInterruptedToolCalls settles calls a dead process left without a
// reply 和 returns msgs 用 repair applied. caller must have
// checked 那 nothing 是 running 会话.
func repairInterruptedToolCalls(ctx context.Context, messages message.Service, msgs []message.Message) ([]message.Message, error) {
	resolved := make(map[string]struct{})
	for _, msg := range msgs {
		for _, tr := range msg.ToolResults() {
			resolved[tr.ToolCallID] = struct{}{}
		}
	}

	var repaired []message.Message
	for i := range msgs {
		msg := &msgs[i]
		if msg.Role != message.Assistant {
			continue
		}
		var orphans []message.ToolCall
		for _, tc := range msg.ToolCalls() {
			if _, ok := resolved[tc.ID]; !ok {
				orphans = append(orphans, tc)
			}
		}
		if len(orphans) == 0 {
			continue
		}
		// Backdated, since 响应-time statistics average over it.
		if !msg.IsFinished() {
			msg.AddFinishAt(message.FinishReasonError, interruptedTurnMessage, "", msg.UpdatedAt)
		}
		settled, err := settleToolCalls(ctx, messages, msg, orphans, interruptedToolCallResult)
		if err != nil {
			return nil, err
		}
		repaired = append(repaired, settled...)
	}
	// Replies pair 用 calls 通过 ID, 所以 they can simply follow transcript.
	return append(msgs, repaired...), nil
}

// ListSettledMessages returns 会话's transcript 用 calls dead
// process left dangling already settled. Readers 是 serialized 所以 two
// cannot settle same 调用 twice. Busy 是 sampled 在 both sides 的
// 读取, since 运行 那 ends mid-读取 left snapshot missing 它的 results.
func (c *coordinator) ListSettledMessages(ctx context.Context, sessionID string) ([]message.Message, error) {
	c.settleMu.Lock()
	defer c.settleMu.Unlock()

	if err := c.messages.FlushAll(ctx); err != nil {
		return nil, err
	}
	busy := c.IsSessionBusy(sessionID)
	msgs, err := c.messages.List(ctx, sessionID)
	if err != nil || busy || c.IsSessionBusy(sessionID) {
		return msgs, err
	}
	settled, err := repairInterruptedToolCalls(ctx, c.messages, msgs)
	if err != nil {
		// repair may have stopped halfway, 所以 reread what was 存储.
		slog.Warn("Failed to settle interrupted tool calls",
			"session_id", sessionID, "error", err)
		return c.messages.List(ctx, sessionID)
	}
	return settled, nil
}
