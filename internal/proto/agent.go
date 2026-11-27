package proto

import (
	"encoding/json"
	"errors"
)

// AgentEventType represents type 的 agent 事件.
type AgentEventType string

const (
	AgentEventTypeError     AgentEventType = "error"
	AgentEventTypeResponse  AgentEventType = "response"
	AgentEventTypeSummarize AgentEventType = "summarize"
)

// MarshalText implements the [encoding.TextMarshaler] interface.
func (t AgentEventType) MarshalText() ([]byte, error) {
	return []byte(t), nil
}

// UnmarshalText implements the [encoding.TextUnmarshaler] interface.
func (t *AgentEventType) UnmarshalText(text []byte) error {
	*t = AgentEventType(text)
	return nil
}

// AgentEvent represents 事件 emitted 通过 agent.
type AgentEvent struct {
	Type    AgentEventType `json:"type"`
	Message Message        `json:"message"`
	Error   error          `json:"error,omitempty"`

	// RunID echoes caller-supplied AgentMessage.RunID 为 运行
	// 那 produced 这 事件. 它 lets observers (notably
	// `crush 运行`) attribute 错误 事件 到 specific 请求
	// instead 的 到 any 在-flight 运行 在 会话. Empty 当 no
	// caller set one.
	RunID string `json:"run_id,omitempty"`

	// ProviderID identifies 提供方 那 needs reauthentication.
	ProviderID string `json:"provider_id,omitempty"`

	// When summarizing.
	SessionID    string `json:"session_id,omitempty"`
	SessionTitle string `json:"session_title,omitempty"`
	Progress     string `json:"progress,omitempty"`
	Done         bool   `json:"done,omitempty"`

	// AWS SSO progress fields, carried 为 TypeAWSSSOAuth 和
	// TypeAWSSSOAuthResult 所以 refresh dialog works 在 客户端/服务端
	// mode. command runs 在 服务端; these ferry 它的 progress 到
	// 客户端. AWSSOCommand 是 refresh command being 运行; AWSSOURL
	// 是 verification URL once it appears 在 command output.
	// 结果's failure 文本 travels through 错误, like TypeAgentError.
	AWSSOCommand string `json:"aws_sso_command,omitempty"`
	AWSSOURL     string `json:"aws_sso_url,omitempty"`
}

// MarshalJSON implements the [json.Marshaler] interface.
func (e AgentEvent) MarshalJSON() ([]byte, error) {
	type Alias AgentEvent
	return json.Marshal(&struct {
		Error string `json:"error,omitempty"`
		Alias
	}{
		Error: func() string {
			if e.Error != nil {
				return e.Error.Error()
			}
			return ""
		}(),
		Alias: Alias(e),
	})
}

// UnmarshalJSON implements the [json.Unmarshaler] interface.
func (e *AgentEvent) UnmarshalJSON(data []byte) error {
	type Alias AgentEvent
	aux := &struct {
		Error string `json:"error,omitempty"`
		Alias
	}{
		Alias: Alias(*e),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*e = AgentEvent(aux.Alias)
	if aux.Error != "" {
		e.Error = errors.New(aux.Error)
	}
	return nil
}
