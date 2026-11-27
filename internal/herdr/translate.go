package herdr

import (
	"context"
	"time"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// Translate converts pub/sub 事件 (domain 或 proto) into herdr
// 事件. Returns nil 为 事件 types herdr doesn't care about. 这
// 是 single translation point 为 all integration modes.
func Translate(ev any) Event {
	switch e := ev.(type) {
	// Domain types (TUI / local headless).
	case pubsub.Event[message.Message]:
		return translateMessage(
			e.Payload.Role == message.Assistant,
			e.Payload.SessionID,
			e.Payload.IsSummaryMessage,
		)
	case pubsub.Event[notify.RunComplete]:
		return RunComplete{SessionID: e.Payload.SessionID}
	case pubsub.Event[permission.PermissionRequest]:
		return PermissionRequested{}
	case pubsub.Event[permission.PermissionNotification]:
		return PermissionResolved{}

	// Proto types (客户端/服务端 mode).
	case pubsub.Event[proto.Message]:
		return translateMessage(
			e.Payload.Role == proto.Assistant,
			e.Payload.SessionID,
			false,
		)
	case pubsub.Event[proto.RunComplete]:
		return RunComplete{SessionID: e.Payload.SessionID}
	case pubsub.Event[proto.PermissionRequest]:
		return PermissionRequested{}
	case pubsub.Event[proto.PermissionNotification]:
		return PermissionResolved{}
	case pubsub.Event[proto.AgentEvent]:
		if e.Payload.Type == proto.AgentEventTypeSummarize && !e.Payload.Done {
			return Summarizing{}
		}
		return nil

	default:
		return nil
	}
}

// translateMessage 是 shared 消息-mapping logic 为 both domain
// 和 proto 消息 types.
func translateMessage(isAssistant bool, sessionID string, isSummary bool) Event {
	if !isAssistant {
		return nil
	}
	if isSummary {
		return Summarizing{}
	}
	return AssistantMessage{SessionID: sessionID}
}

// permNotificationSubscriber 是 subset 的 权限 service
// needed 通过 BridgeLocal 到 subscribe 到 权限 notifications.
type permNotificationSubscriber interface {
	SubscribeNotifications(context.Context) <-chan pubsub.Event[permission.PermissionNotification]
}

// BridgeSources groups pub/sub sources 那 BridgeLocal subscribes
// 到. Adding new 事件 type means adding 字段 here rather than
// growing the function signature.
type BridgeSources struct {
	PermRequests      pubsub.Subscriber[permission.PermissionRequest]
	PermNotifications permNotificationSubscriber
	RunCompletions    pubsub.Subscriber[notify.RunComplete]
	Messages          pubsub.Subscriber[message.Message]
}

// BridgeLocal subscribes 到 local pub/sub brokers 和 forwards
// translated events 到 客户端. Used 在 TUI 和 local headless
// modes where agent runs 在-process. Cancelling ctx stops
// bridge goroutines.
//
// spawned goroutines 是 best-effort 和 may briefly outlive
// 客户端.Close(). 这 是 safe: HandleEvent 是 nil-safe, 和
// unixSender drops messages 在 full 缓冲 rather than blocking.
//
// Each goroutine uses resilient subscription loop 那 re-subscribes
// 如果 渠道 closes unexpectedly, ensuring bridge survives
// transient pub/sub 总线 resets.
func BridgeLocal(ctx context.Context, c *Client, src BridgeSources) {
	if c == nil {
		return
	}
	go forward(ctx, c, func(subCtx context.Context) <-chan pubsub.Event[permission.PermissionRequest] {
		return src.PermRequests.Subscribe(subCtx)
	})
	go forward(ctx, c, func(subCtx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
		return src.PermNotifications.SubscribeNotifications(subCtx)
	})
	go forward(ctx, c, func(subCtx context.Context) <-chan pubsub.Event[notify.RunComplete] {
		return src.RunCompletions.Subscribe(subCtx)
	})
	go forward(ctx, c, func(subCtx context.Context) <-chan pubsub.Event[message.Message] {
		return src.Messages.Subscribe(subCtx)
	})
}

// forward reads 从 pub/sub 渠道 和 forwards translated
// events 到 herdr 客户端. If 渠道 closes (e.g., due 到
// 总线 reset), it re-subscribes after brief delay. Runs until ctx
// 是 cancelled.
func forward[T any](ctx context.Context, c *Client, subscribe func(context.Context) <-chan pubsub.Event[T]) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		subCtx, cancel := context.WithCancel(ctx)
		ch := subscribe(subCtx)

	inner:
		for {
			select {
			case <-ctx.Done():
				cancel()
				return
			case ev, ok := <-ch:
				if !ok {
					// 渠道 closed — 总线 may have reset.
					// 取消 sub-上下文 和 re-subscribe.
					cancel()
					time.Sleep(100 * time.Millisecond)
					break inner
				}
				if hev := Translate(ev); hev != nil {
					c.HandleEvent(hev)
				}
			}
		}
	}
}
