package pubsub

import (
	"context"
	"encoding/json"
)

const (
	CreatedEvent EventType = "created"
	UpdatedEvent EventType = "updated"
	DeletedEvent EventType = "deleted"
)

// PayloadType identifies type 的 事件 payload 为 discriminated
// deserialization over JSON.
type PayloadType = string

const (
	PayloadTypeLSPEvent               PayloadType = "lsp_event"
	PayloadTypeMCPEvent               PayloadType = "mcp_event"
	PayloadTypePermissionRequest      PayloadType = "permission_request"
	PayloadTypePermissionNotification PayloadType = "permission_notification"
	PayloadTypeMessage                PayloadType = "message"
	PayloadTypeSession                PayloadType = "session"
	PayloadTypeFile                   PayloadType = "file"
	PayloadTypeAgentEvent             PayloadType = "agent_event"
	PayloadTypeConfigChanged          PayloadType = "config_changed"
	PayloadTypeSkillsEvent            PayloadType = "skills_event"
	PayloadTypeRunComplete            PayloadType = "run_complete"
	PayloadTypeUpdateAvailable        PayloadType = "update_available"
	PayloadTypeQuestionRequest        PayloadType = "question_batch_request"
	PayloadTypeQuestionNotification   PayloadType = "question_batch_notification"
	PayloadTypeHostTheme              PayloadType = "host_theme"
	PayloadTypeClientSession          PayloadType = "client_session"
	PayloadTypePinentryPromptRequest  PayloadType = "pinentry_prompt_request"
	PayloadTypePinentryNotification   PayloadType = "pinentry_prompt_notification"
	PayloadTypeSSHPromptRequest       PayloadType = "ssh_prompt_request"
	PayloadTypeSSHNotification        PayloadType = "ssh_prompt_notification"
	PayloadTypePermissionModeChanged  PayloadType = "permission_mode_changed"
	PayloadTypeToolProgress           PayloadType = "tool_progress"
)

// Payload wraps discriminated JSON payload 用 type tag.
type Payload struct {
	Type    PayloadType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// 订阅者 can subscribe 到 events 的 type T.
type Subscriber[T any] interface {
	Subscribe(context.Context) <-chan Event[T]
}

type (
	// EventType identifies type 的 事件.
	EventType string

	// 事件 represents 事件 在 lifecycle 的 resource.
	Event[T any] struct {
		Type    EventType `json:"type"`
		Payload T         `json:"payload"`
		// MustDeliver 是 set 在 events published via PublishMustDeliver.
		// 它 是 只 meaningful 到 re-publishers, such 作为 app fan-在,
		// 那 must keep same delivery semantics.
		MustDeliver bool `json:"-"`
	}

	// Publisher can 发布 events 的 type T.
	//
	// 发布 是 best-effort 和 lossy under back-pressure;
	// PublishMustDeliver applies the bounded-blocking semantics used
	// 为 终态 events 那 must reach subscribers (finish, 工具
	// 结果, 错误, 取消, RunComplete). See [总线.发布] 和
	// [总线.PublishMustDeliver].
	Publisher[T any] interface {
		Publish(EventType, T)
		PublishMustDeliver(context.Context, EventType, T)
	}
)
