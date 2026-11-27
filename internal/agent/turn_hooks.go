package agent

import (
	"context"
	"errors"

	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
)

// ErrCompactionBlocked 是 returned 通过 Summarize 当 PreCompact 钩子
// blocks 压缩. conversation 是 left 作为 it was.
var ErrCompactionBlocked = errors.New("compaction blocked by hook")

// 压缩 triggers reported 到 PreCompact 和 PostCompact hooks.
const (
	compactTriggerAuto   = "auto"
	compactTriggerManual = "manual"
)

// fireTurnHook runs one 钩子 为 top-level turn. 子代理 turns (
// task 工具, agentic_fetch, etc.) 是 skipped 所以 delegated turn does
// 不 fire 用户's 钩子 N times, matching 子代理 exclusion
// applied 到 工具 hooks.
func (a *sessionAgent) fireTurnHook(ctx context.Context, call SessionAgentCall, ev hooks.Event) {
	if call.NonInteractive {
		return
	}
	ev.SessionID = call.SessionID
	a.hooks.Run(ctx, ev)
}

// turnEndEvent names how turn ended 所以 exactly one 钩子 fires 为 it:
// Interrupt 当 用户 cancelled, StopFailure 当 提供方
// failed, 和 TurnEnd otherwise.
func turnEndEvent(err error, assistant *message.Message) hooks.Event {
	switch {
	case errors.Is(err, context.Canceled):
		return hooks.Event{Name: hooks.EventInterrupt}
	case err != nil:
		return hooks.Event{Name: hooks.EventStopFailure, Error: err.Error()}
	}
	ev := hooks.Event{Name: hooks.EventTurnEnd}
	if assistant != nil {
		ev.LastAssistantMessage = new(assistant.Content().String())
	}
	return ev
}
