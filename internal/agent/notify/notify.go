// Package notify defines domain 通知 types 为 agent events.
// These types 是 decoupled 从 UI concerns 所以 agent can 发布
// events without importing UI packages.
package notify

// Type identifies kind 的 agent 通知.
type Type string

const (
	// TypeAgentFinished indicates agent has completed 它的 turn.
	TypeAgentFinished Type = "agent_finished"
	// TypeReAuthenticate indicates the agent encountered an
	// authentication 错误 和 用户 needs 到 re-authenticate.
	TypeReAuthenticate Type = "re_authenticate"
	// TypeAgentError indicates agent's turn terminated 用
	// 错误. 错误 文本 是 carried 在 通知.消息.
	TypeAgentError Type = "error"
	// TypeAWSSSOAuth indicates AWS SSO credentials have expired 和
	// coordinator 是 running configured refresh command. 它 opens
	// AWS SSO dialog; follow-up 用 same type carries SSO URL
	// once it appears 在 command output. AWSSOCommand carries
	// command being 运行; AWSSOURL carries verification URL 当 known.
	TypeAWSSSOAuth Type = "aws_sso_auth"
	// TypeAWSSSOAuthResult indicates AWS SSO 刷新 command has
	// finished. 消息 carries 错误 文本 当 it failed, empty 在
	// success.
	TypeAWSSSOAuthResult Type = "aws_sso_auth_result"
)

// 通知 represents domain 事件 published 通过 agent.
type Notification struct {
	SessionID    string
	SessionTitle string
	Type         Type
	ProviderID   string
	// RunID, 当 non-empty, 是 caller-supplied correlator
	// (proto.AgentMessage.RunID) 为 运行 那 produced 这
	// 通知. 它 lets observers attribute TypeAgentError 到
	// specific 请求 rather than 到 any 在-flight 运行 在
	// 会话. Empty 当 no caller set one.
	RunID string
	// 消息 carries 错误 文本 为 TypeAgentError. Other
	// 通知 types ignore it.
	Message string
	// AWSSOCommand carries shell command 为 TypeAWSSSOAuth.
	AWSSOCommand string
	// AWSSOURL carries SSO verification URL 为 TypeAWSSSOAuth once it
	// appears 在 refresh command's output.
	AWSSOURL string
}

// RunComplete 是 authoritative end-的-运行 signal 为 会话.
// 它 是 published exactly once per top-level agent 运行 (per
// [sessionAgent.运行] invocation 那 actually executed) after all
// 消息 updates 为 turn have been flushed via
// 消息.Service.FlushAll. Carries final 助手 文本 和
// 消息 ID 所以 non-interactive clients can reconcile stdout even 如果
// SSE events arrive out 的 order 或 是 dropped 通过 总线. 错误
// 是 non-empty 当 运行 terminated 用 错误; Cancelled 是
// true 当 运行 terminated due 到 上下文 cancellation. two
// 是 mutually exclusive 在 success case 但 may overlap 当
// 取消 triggers downstream 错误.
//
// RunID identifies specific 请求 那 produced 这 事件.
// 它 是 值 caller set 在 `proto.AgentMessage.RunID` (或
// equivalently propagated via agent.WithRunID 在 上下文 那
// reaches coordinator); empty 当 no caller set one. Filtering
// 通过 RunID lets 客户端 correlate SendMessage 调用 用 它的
// 终态 事件 even 当 会话 是 busy 和 other turns 是
// finishing 在 same 会话.
type RunComplete struct {
	SessionID string
	RunID     string
	MessageID string
	Text      string
	Error     string
	Cancelled bool
}
