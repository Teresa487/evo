// proto.go —— 客户端/服务端之间传输的协议类型。
//
// ClientWorkspace 和 server 都依赖这里的 Workspace、事件、权限、请求等结构；
// 它定义「两端说什么语言」，不含业务逻辑。
package proto

import (
	"encoding/json"
	"errors"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/lsp"
)

// WorkspacePermissionMode represents 权限 enforcement level 为
// 工作区.
type WorkspacePermissionMode string

const (
	// WorkspacePermissionModeNormal prompts 为 all non-safe commands.
	WorkspacePermissionModeNormal WorkspacePermissionMode = "normal"
	// WorkspacePermissionModeYolo never prompts: it approves ordinary
	// requests 和 blocks dangerous commands without asking.
	WorkspacePermissionModeYolo WorkspacePermissionMode = "yolo"
	// WorkspacePermissionModeSysadmin never prompts 和 blocks nothing.
	WorkspacePermissionModeSysadmin WorkspacePermissionMode = "sysadmin"
)

// 工作区 represents running app.App 工作区 用 它的 associated
// resources 和 state.
type Workspace struct {
	ID             string                  `json:"id"`
	Path           string                  `json:"path"`
	PermissionMode WorkspacePermissionMode `json:"permission_mode,omitempty"`
	Debug          bool                    `json:"debug,omitempty"`
	DataDir        string                  `json:"data_dir,omitempty"`
	Version        string                  `json:"version,omitempty"`
	ClientID       string                  `json:"client_id,omitempty"`
	Config         *config.Config          `json:"config,omitempty"`
	// Env carries 客户端's environment 在 工作区 creation requests
	// 只. 服务端 responses omit it 所以 secrets 在 environment 是 不
	// exposed 到 other clients.
	Env []string `json:"env,omitempty"`
	// Channels lists MCP servers opted 在 作为 channels 为 这 工作区
	// (从 --channels flag).
	Channels []string `json:"channels,omitempty"`
	// Prompts replaces built-在 prompts 为 这 工作区 (从
	// --提示词 flag), keyed 通过 提示词 name. Paths 是 absolute.
	Prompts config.PromptPaths `json:"prompts,omitempty"`
	// Skills carries snapshot 的 skill discovery state 在 工作区
	// creation time. Subsequent updates flow through SSE 事件
	// 流.
	Skills []SkillState `json:"skills,omitempty"`
}

// 错误 represents 错误 响应.
type Error struct {
	Message string `json:"message"`
}

// ConfigChanged 是 published whenever 工作区's configuration 是
// mutated 通过 backend operation. Clients react 通过 re-fetching
// 工作区 snapshot 所以 cached 配置 stays 在 sync across subscribers.
type ConfigChanged struct {
	WorkspaceID string `json:"workspace_id"`
}

// UpdateAvailable 是 published 当 newer Crush release 是 detected
// 在 服务端 side. 它 mirrors app.UpdateAvailableMsg across SSE
// boundary 所以 客户端/服务端 mode TUI clients see same 通知
// 作为 local-mode clients.
type UpdateAvailable struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	IsDevelopment  bool   `json:"is_development"`
}

// ClientSession 是 payload 的 client_session 事件: 会话
// 客户端 的 工作区 是 viewing has changed. An empty SessionID means
// 客户端 no longer views one, such 作为 landing screen 或 after it
// went away.
type ClientSession struct {
	ClientID  string `json:"client_id"`
	SessionID string `json:"session_id"`
}

// CurrentSession 是 请求 body 为 per-客户端
// current-会话 endpoint. An empty SessionID clears 条目.
type CurrentSession struct {
	SessionID string `json:"session_id"`
}

// HostContext 是 what application hosting Crush, such 作为 editor, has
// 在 view: files it has open, 和 文件 和 line 用户 是 在.
// agent gets it 用 each 提示词, 所以 那 "here" 或 "这 文件" means what
// 用户 sees. zero HostContext 是 none.
type HostContext struct {
	OpenFiles  []string `json:"open_files,omitempty"`
	ActiveFile string   `json:"active_file,omitempty"`
	Line       int      `json:"line,omitempty"` // 1-based line of the cursor in ActiveFile
}

// RunComplete 是 authoritative end-的-运行 signal 为 会话,
// emitted exactly once per top-level agent turn after all 消息
// updates 为 turn have flushed. Clients 那 need reliable
// completion contract (notably `crush 运行` 在 客户端/服务端 mode)
// should listen 为 这 事件 filtered 通过 RunID (preferred) — 或
// 通过 SessionID 当 no RunID was supplied — 和 use 文本 和
// MessageID 到 reconcile any output they have already streamed 从
// earlier 消息 events. 错误 是 non-empty 当 运行 terminated
// 用 错误; Cancelled 是 true 当 terminated due 到 上下文
// cancellation.
//
// RunID echoes 值 caller set 在 AgentMessage.RunID. 它 是
// 只 safe correlator 当 caller's 提示词 was queued
// behind busy 会话: another turn's RunComplete 为 same
// SessionID may arrive first, 和 filtering 通过 SessionID alone
// would terminate caller before 它的 own turn ran.
type RunComplete struct {
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id,omitempty"`
	MessageID string `json:"message_id"`
	Text      string `json:"text,omitempty"`
	Error     string `json:"error,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// SkillInfo describes visible skill exposed 到 frontend.
type SkillInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Label         string `json:"label"`
	Source        string `json:"source"`
	UserInvocable bool   `json:"user_invocable"`
}

// ReadSkillRequest 是 请求 body 为 reading skill's content.
type ReadSkillRequest struct {
	SkillID string `json:"skill_id"`
}

// ReadSkillResponse 是 响应 为 reading skill's content.
type ReadSkillResponse struct {
	Content []byte          `json:"content"`
	Result  SkillReadResult `json:"result"`
}

// SkillReadResult holds metadata about skill returned alongside 它的
// content.
type SkillReadResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Builtin     bool   `json:"builtin"`
}

// AgentInfo represents information about the agent.
type AgentInfo struct {
	IsBusy   bool                 `json:"is_busy"`
	IsReady  bool                 `json:"is_ready"`
	Model    catwalk.Model        `json:"model"`
	ModelCfg config.SelectedModel `json:"model_cfg"`
	// MainAgent names 活跃 agent ("coder" 或 "plan"), which every
	// 客户端 的 工作区 shares. Empty 当 agent 是 不 ready.
	MainAgent string `json:"main_agent,omitempty"`
}

// IsZero checks 如果 AgentInfo 是 zero-valued.
func (a AgentInfo) IsZero() bool {
	return !a.IsBusy && !a.IsReady && a.Model.ID == ""
}

// AgentMessage represents 消息 sent 到 agent.
//
// RunID, 当 non-empty, 是 echoed back 在 [RunComplete] 事件
// emitted 为 resulting turn. Callers 那 need 到 correlate
// specific SendMessage 用 它的 终态 事件 (notably
// `crush 运行`, which may attach 到 busy 会话 whose currently
// running turn finishes first) should set it 到 fresh unique
// 值 before 请求. 服务端-side propagation flows through
// agent.WithRunID 在 请求 上下文 into
// SessionAgentCall; it 是 preserved across busy-会话 队列.
// When empty resulting RunComplete carries empty RunID 和
// callers must fall back 到 SessionID-只 filtering, which
// remains correct 只 当 no other turns 是 在 flight 为
// same 会话.
//
// 渠道 origin 是 deliberately absent 从 这 wire type: 客户端
// must never 是 able 到 标记 它的 own turn 作为 渠道-originated (那
// would route turn's reply back through 渠道's messaging
// 工具 用 no 权限 提示词). 渠道 binding 为 运行 是
// set 服务端-side, 在 backend's 渠道 injection 路径, 和
// travels 用 运行 internally rather than 在 请求.
type AgentMessage struct {
	HiddenUserMessage bool         `json:"hidden_user_message,omitempty"`
	SessionID         string       `json:"session_id"`
	RunID             string       `json:"run_id,omitempty"`
	Prompt            string       `json:"prompt"`
	Attachments       []Attachment `json:"attachments,omitempty"`
}

// SessionNote 是 something host application tells 会话's agent,
// 到 是 读取 用 会话's next 提示词. 它 starts no 运行 和 是 不
// chat 消息.
type SessionNote struct {
	Text string `json:"text"`
}

// ShellCommandRequest represents 请求 到 运行 shell command directly.
type ShellCommandRequest struct {
	SessionID string `json:"session_id"`
	Command   string `json:"command"`
	TermWidth int    `json:"term_width,omitempty"`
	// IsFirstMessage marks command 那 started 会话, 所以
	// 会话 是 titled after it.
	IsFirstMessage bool `json:"is_first_message,omitempty"`
}

// ShellCommandResponse represents 结果 的 direct shell command.
type ShellCommandResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// AgentSession represents 会话 用 它的 busy status.
type AgentSession struct {
	Session
	IsBusy bool `json:"is_busy"`
}

// IsZero checks 如果 AgentSession 是 zero-valued.
func (a AgentSession) IsZero() bool {
	return a.ID == "" && !a.IsBusy
}

// PermissionAction represents action taken 在 权限 请求.
type PermissionAction string

const (
	PermissionAllow           PermissionAction = "allow"
	PermissionAllowForSession PermissionAction = "allow_session"
	PermissionDeny            PermissionAction = "deny"
)

// MarshalText implements the [encoding.TextMarshaler] interface.
func (p PermissionAction) MarshalText() ([]byte, error) {
	return []byte(p), nil
}

// UnmarshalText implements the [encoding.TextUnmarshaler] interface.
func (p *PermissionAction) UnmarshalText(text []byte) error {
	*p = PermissionAction(text)
	return nil
}

// PermissionGrant represents 权限 grant 请求.
type PermissionGrant struct {
	Permission PermissionRequest `json:"permission"`
	Action     PermissionAction  `json:"action"`
}

// PermissionGrantResponse 是 服务端's 响应 到 权限
// grant 调用. Resolved 是 true 当 这 调用 resolved pending
// 请求, 和 false 当 请求 had already been resolved 通过
// previous caller (e.g., another 客户端 在 multi-订阅者 UI). 一个
// false 值 是 不 错误.
type PermissionGrantResponse struct {
	Resolved bool `json:"resolved"`
}

// QuestionRequest 是 wire format 为 batch question
// sent 从 服务端 到 客户端 over SSE.
type QuestionRequest struct {
	ID                 string         `json:"id"`
	SessionID          string         `json:"session_id"`
	ToolCallID         string         `json:"tool_call_id"`
	Questions          []QuestionItem `json:"questions"`
	ConfirmTitle       string         `json:"confirm_title,omitempty"`
	ConfirmDescription string         `json:"confirm_description,omitempty"`
}

// QuestionItem 是 single question within batch.
type QuestionItem struct {
	ID          string           `json:"id"`
	Type        string           `json:"type"`
	Label       string           `json:"label,omitempty"`
	Question    string           `json:"question"`
	Description string           `json:"description,omitempty"`
	Choices     []QuestionChoice `json:"choices,omitempty"`
}

// QuestionChoice 是 selectable option.
type QuestionChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionAnswer 是 wire format 为 answering batch
// question, sent 从 客户端 到 服务端 via REST.
type QuestionAnswer struct {
	BatchRequestID string             `json:"batch_request_id"`
	Responses      []QuestionResponse `json:"responses"`
}

// QuestionResponse 是 single answer within batch 响应.
type QuestionResponse struct {
	QuestionID  string            `json:"request_id"`
	SelectedIDs []string          `json:"selected_ids,omitempty"`
	FillInText  string            `json:"fill_in_text,omitempty"`
	Yes         *bool             `json:"yes,omitempty"`
	Notes       map[string]string `json:"notes,omitempty"`
}

// QuestionCancel 是 wire format 为 cancelling batch question,
// sent 从 客户端 到 服务端 via REST.
type QuestionCancel struct {
	BatchRequestID string `json:"batch_request_id"`
}

// QuestionAnswerResponse 是 服务端's 响应 到
// question batch answer 调用.
type QuestionAnswerResponse struct {
	Resolved bool `json:"resolved"`
}

// QuestionNotification 是 published 当 question batch 是
// resolved 所以 non-answering clients can dismiss their forms.
type QuestionNotification struct {
	BatchID   string `json:"batch_id"`
	SessionID string `json:"session_id"`
}

// PinentryKey 是 wire format 为 key pinentry 提示词
// concerns, 作为 far 作为 GPG reported it.
type PinentryKey struct {
	CardNumber  string `json:"card_number,omitempty"`
	KeyID       string `json:"key_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Holder      string `json:"holder,omitempty"`
}

// PinentryPromptRequest 是 SSE wire format 为 integrated
// pinentry credential 提示词 ( GPG passphrase 或 security key PIN).
// resolved secret 是 never carried 在 这 type.
type PinentryPromptRequest struct {
	ID         string      `json:"id"`
	Prompt     string      `json:"prompt"`
	Key        PinentryKey `json:"key,omitempty"`
	Kind       string      `json:"kind"`
	RetryCount int         `json:"retry_count,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// PinentryAnswer 是 wire format 为 answering pinentry 提示词,
// sent 从 客户端 到 服务端 via REST.
type PinentryAnswer struct {
	RequestID string `json:"request_id"`
	Secret    string `json:"secret"`
}

// PinentryCancel 是 wire format 为 dismissing pinentry 提示词.
type PinentryCancel struct {
	RequestID string `json:"request_id"`
}

// PinentryResponse 是 服务端's 响应 到 pinentry answer 和
// 取消 calls.
type PinentryResponse struct {
	Resolved bool `json:"resolved"`
}

// PinentryNotification 是 published 当 pinentry 提示词 是 resolved
// 所以 non-answering clients can dismiss their dialogs.
type PinentryNotification struct {
	RequestID string `json:"request_id"`
}

// SSHPromptRequest 是 SSE wire format 为 integrated SSH askpass
// 提示词: authentication password 或 key passphrase, 或
// confirmation question (security key touch, 主机 key acceptance).
// resolved secret 是 never carried 在 这 type.
type SSHPromptRequest struct {
	ID      string `json:"id"`
	Prompt  string `json:"prompt"`
	KeyInfo string `json:"key_info,omitempty"`
	Kind    string `json:"kind"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`
}

// SSHAnswer 是 wire format 为 answering SSH 提示词, sent 从
// 客户端 到 服务端 via REST.
type SSHAnswer struct {
	RequestID string `json:"request_id"`
	Secret    string `json:"secret"`
}

// SSHCancel 是 wire format 为 dismissing SSH 提示词.
type SSHCancel struct {
	RequestID string `json:"request_id"`
}

// SSHResponse 是 服务端's 响应 到 SSH answer 和 取消 calls.
type SSHResponse struct {
	Resolved bool `json:"resolved"`
}

// SSHNotification 是 published 当 SSH 提示词 是 resolved 所以
// non-answering clients 可以 dismiss their dialogs.
type SSHNotification struct {
	RequestID string `json:"request_id"`
}

// PermissionSetModeRequest represents 请求 到 set 权限 mode.
type PermissionSetModeRequest struct {
	Mode WorkspacePermissionMode `json:"mode"`
}

// PermissionModeEvent 是 SSE payload published whenever 服务端-side
// 权限 mode changes.
type PermissionModeEvent struct {
	Mode WorkspacePermissionMode `json:"mode"`
}

// LSPEventType represents type 的 LSP 事件.
type LSPEventType string

const (
	LSPEventStateChanged       LSPEventType = "state_changed"
	LSPEventDiagnosticsChanged LSPEventType = "diagnostics_changed"
)

// MarshalText implements the [encoding.TextMarshaler] interface.
func (e LSPEventType) MarshalText() ([]byte, error) {
	return []byte(e), nil
}

// UnmarshalText implements the [encoding.TextUnmarshaler] interface.
func (e *LSPEventType) UnmarshalText(data []byte) error {
	*e = LSPEventType(data)
	return nil
}

// ToolProgress 是 wire form 的 live output 为 running 工具调用.
type ToolProgress struct {
	SessionID  string `json:"session_id"`
	ToolCallID string `json:"tool_call_id"`
	ShellID    string `json:"shell_id,omitempty"`
	Output     string `json:"output"`
}

// LSPEvent represents 事件 在 LSP system.
type LSPEvent struct {
	Type            LSPEventType    `json:"type"`
	Name            string          `json:"name"`
	State           lsp.ServerState `json:"state"`
	Error           error           `json:"error,omitempty"`
	DiagnosticCount int             `json:"diagnostic_count,omitempty"`
}

// MarshalJSON implements the [json.Marshaler] interface.
func (e LSPEvent) MarshalJSON() ([]byte, error) {
	type Alias LSPEvent
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
func (e *LSPEvent) UnmarshalJSON(data []byte) error {
	type Alias LSPEvent
	aux := &struct {
		Error string `json:"error,omitempty"`
		Alias
	}{
		Alias: Alias(*e),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*e = LSPEvent(aux.Alias)
	if aux.Error != "" {
		e.Error = errors.New(aux.Error)
	}
	return nil
}

// LSPClientInfo holds information about LSP 客户端's state.
type LSPClientInfo struct {
	Name            string          `json:"name"`
	State           lsp.ServerState `json:"state"`
	Error           error           `json:"error,omitempty"`
	DiagnosticCount int             `json:"diagnostic_count,omitempty"`
	ConnectedAt     time.Time       `json:"connected_at"`
}

// MarshalJSON implements the [json.Marshaler] interface.
func (i LSPClientInfo) MarshalJSON() ([]byte, error) {
	type Alias LSPClientInfo
	return json.Marshal(&struct {
		Error string `json:"error,omitempty"`
		Alias
	}{
		Error: func() string {
			if i.Error != nil {
				return i.Error.Error()
			}
			return ""
		}(),
		Alias: Alias(i),
	})
}

// UnmarshalJSON implements the [json.Unmarshaler] interface.
func (i *LSPClientInfo) UnmarshalJSON(data []byte) error {
	type Alias LSPClientInfo
	aux := &struct {
		Error string `json:"error,omitempty"`
		Alias
	}{
		Alias: Alias(*i),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*i = LSPClientInfo(aux.Alias)
	if aux.Error != "" {
		i.Error = errors.New(aux.Error)
	}
	return nil
}
