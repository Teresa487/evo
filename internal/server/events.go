// events.go —— SSE 事件流：把 backend 的事件按客户端订阅过滤后推送给远端。
//
// 从产物反推：远端 TUI 之所以能看到消息更新、session 变化、agent 完成，
// 全靠这里的事件流把 backend/pubsub 的事件推过 HTTP 长连接。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pinentry"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/sshaskpass"
)

// wrapEvent converts raw tea.Msg ( pubsub.事件[T] 从 app
// 事件 fan-在) into pubsub.Payload envelope 用 correct
// PayloadType discriminator 和 proto-typed inner payload 那 has
// proper JSON tags. Returns nil 如果 事件 type 是 unrecognized.
func wrapEvent(ev any) *pubsub.Payload {
	switch e := ev.(type) {
	case pubsub.Event[app.LSPEvent]:
		return envelope(pubsub.PayloadTypeLSPEvent, pubsub.Event[proto.LSPEvent]{
			Type: e.Type,
			Payload: proto.LSPEvent{
				Type:            proto.LSPEventType(e.Payload.Type),
				Name:            e.Payload.Name,
				State:           e.Payload.State,
				Error:           e.Payload.Error,
				DiagnosticCount: e.Payload.DiagnosticCount,
			},
		})
	case pubsub.Event[mcp.Event]:
		pt := mcpEventTypeToProto(e.Payload.Type)
		if pt == "" {
			// Unsupported MCP 事件 type. Drop it instead 的 fabricating
			// state_changed 事件.
			slog.Debug("Dropping unsupported MCP event type for SSE", "type", e.Payload.Type)
			return nil
		}
		return envelope(pubsub.PayloadTypeMCPEvent, pubsub.Event[proto.MCPEvent]{
			Type: e.Type,
			Payload: proto.MCPEvent{
				Type:           pt,
				Name:           e.Payload.Name,
				State:          proto.MCPState(e.Payload.State),
				Error:          e.Payload.Error,
				ToolCount:      e.Payload.Counts.Tools,
				PromptCount:    e.Payload.Counts.Prompts,
				ResourceCount:  e.Payload.Counts.Resources,
				ChannelMessage: e.Payload.ChannelMessage,
			},
		})
	case pubsub.Event[permission.PermissionRequest]:
		return envelope(pubsub.PayloadTypePermissionRequest, pubsub.Event[proto.PermissionRequest]{
			Type: e.Type,
			Payload: proto.PermissionRequest{
				ID:          e.Payload.ID,
				SessionID:   e.Payload.SessionID,
				ToolCallID:  e.Payload.ToolCallID,
				ToolName:    e.Payload.ToolName,
				Description: e.Payload.Description,
				Action:      e.Payload.Action,
				Path:        e.Payload.Path,
				Params:      e.Payload.Params,
				Danger:      e.Payload.Danger,
			},
		})
	case pubsub.Event[permission.PermissionNotification]:
		return envelope(pubsub.PayloadTypePermissionNotification, pubsub.Event[proto.PermissionNotification]{
			Type: e.Type,
			Payload: proto.PermissionNotification{
				SessionID:  e.Payload.SessionID,
				ToolCallID: e.Payload.ToolCallID,
				Granted:    e.Payload.Granted,
				Denied:     e.Payload.Denied,
				Cancelled:  e.Payload.Cancelled,
			},
		})
	case pubsub.Event[permission.ModeChangedEvent]:
		return envelope(pubsub.PayloadTypePermissionModeChanged, pubsub.Event[proto.PermissionModeEvent]{
			Type: e.Type,
			Payload: proto.PermissionModeEvent{
				Mode: proto.PermissionModeToProto(e.Payload.Mode),
			},
		})
	case pubsub.Event[question.Request]:
		slog.Info("Wrapping question batch event for SSE", "id", e.Payload.ID, "questions", len(e.Payload.Questions))
		return envelope(pubsub.PayloadTypeQuestionRequest, pubsub.Event[proto.QuestionRequest]{
			Type: e.Type,
			Payload: proto.QuestionRequest{
				ID:                 e.Payload.ID,
				SessionID:          e.Payload.SessionID,
				ToolCallID:         e.Payload.ToolCallID,
				Questions:          questionsToProto(e.Payload.Questions),
				ConfirmTitle:       e.Payload.ConfirmTitle,
				ConfirmDescription: e.Payload.ConfirmDescription,
			},
		})
	case pubsub.Event[question.Notification]:
		return envelope(pubsub.PayloadTypeQuestionNotification, pubsub.Event[proto.QuestionNotification]{
			Type: e.Type,
			Payload: proto.QuestionNotification{
				BatchID:   e.Payload.BatchID,
				SessionID: e.Payload.SessionID,
			},
		})
	case pubsub.Event[pinentry.PromptRequest]:
		return envelope(pubsub.PayloadTypePinentryPromptRequest, pubsub.Event[proto.PinentryPromptRequest]{
			Type: e.Type,
			Payload: proto.PinentryPromptRequest{
				ID:     e.Payload.ID,
				Prompt: e.Payload.Prompt,
				Key: proto.PinentryKey{
					CardNumber:  e.Payload.Key.CardNumber,
					KeyID:       e.Payload.Key.KeyID,
					Fingerprint: e.Payload.Key.Fingerprint,
					Holder:      e.Payload.Key.Holder,
				},
				Kind:       string(e.Payload.Kind),
				RetryCount: e.Payload.RetryCount,
				Error:      e.Payload.Error,
			},
		})
	case pubsub.Event[pinentry.Notification]:
		return envelope(pubsub.PayloadTypePinentryNotification, pubsub.Event[proto.PinentryNotification]{
			Type: e.Type,
			Payload: proto.PinentryNotification{
				RequestID: e.Payload.RequestID,
			},
		})
	case pubsub.Event[sshaskpass.PromptRequest]:
		return envelope(pubsub.PayloadTypeSSHPromptRequest, pubsub.Event[proto.SSHPromptRequest]{
			Type: e.Type,
			Payload: proto.SSHPromptRequest{
				ID:      e.Payload.ID,
				Prompt:  e.Payload.Prompt,
				KeyInfo: e.Payload.KeyInfo,
				Kind:    string(e.Payload.Kind),
				Title:   e.Payload.Title,
				Message: e.Payload.Message,
			},
		})
	case pubsub.Event[sshaskpass.Notification]:
		return envelope(pubsub.PayloadTypeSSHNotification, pubsub.Event[proto.SSHNotification]{
			Type: e.Type,
			Payload: proto.SSHNotification{
				RequestID: e.Payload.RequestID,
			},
		})
	case pubsub.Event[message.Message]:
		return envelope(pubsub.PayloadTypeMessage, pubsub.Event[proto.Message]{
			Type:    e.Type,
			Payload: messageToProto(e.Payload),
		})
	case pubsub.Event[session.Session]:
		return envelope(pubsub.PayloadTypeSession, pubsub.Event[proto.Session]{
			Type:    e.Type,
			Payload: sessionToProto(e.Payload),
		})
	case pubsub.Event[history.File]:
		return envelope(pubsub.PayloadTypeFile, pubsub.Event[proto.File]{
			Type:    e.Type,
			Payload: fileToProto(e.Payload),
		})
	case pubsub.Event[notify.Notification]:
		payload := proto.AgentEvent{
			SessionID:    e.Payload.SessionID,
			SessionTitle: e.Payload.SessionTitle,
			RunID:        e.Payload.RunID,
			Type:         proto.AgentEventType(e.Payload.Type),
			ProviderID:   e.Payload.ProviderID,
			AWSSOCommand: e.Payload.AWSSOCommand,
			AWSSOURL:     e.Payload.AWSSOURL,
		}
		// Carry any human-readable 消息 across wire; 客户端
		// maps 错误 back into 通知.消息.
		if e.Payload.Message != "" {
			payload.Error = errors.New(e.Payload.Message)
		}
		if e.Payload.Type == notify.TypeAgentError {
			payload.Type = proto.AgentEventTypeError
		}
		return envelope(pubsub.PayloadTypeAgentEvent, pubsub.Event[proto.AgentEvent]{
			Type:    e.Type,
			Payload: payload,
		})
	case pubsub.Event[notify.RunComplete]:
		return envelope(pubsub.PayloadTypeRunComplete, pubsub.Event[proto.RunComplete]{
			Type: e.Type,
			Payload: proto.RunComplete{
				SessionID: e.Payload.SessionID,
				RunID:     e.Payload.RunID,
				MessageID: e.Payload.MessageID,
				Text:      e.Payload.Text,
				Error:     e.Payload.Error,
				Cancelled: e.Payload.Cancelled,
			},
		})
	case pubsub.Event[proto.ConfigChanged]:
		return envelope(pubsub.PayloadTypeConfigChanged, e)
	case pubsub.Event[proto.HostThemeState]:
		return envelope(pubsub.PayloadTypeHostTheme, e)
	case pubsub.Event[proto.ClientSession]:
		return envelope(pubsub.PayloadTypeClientSession, e)
	case app.UpdateAvailableMsg:
		return envelope(pubsub.PayloadTypeUpdateAvailable, pubsub.Event[proto.UpdateAvailable]{
			Type: pubsub.UpdatedEvent,
			Payload: proto.UpdateAvailable{
				CurrentVersion: e.CurrentVersion,
				LatestVersion:  e.LatestVersion,
				IsDevelopment:  e.IsDevelopment,
			},
		})
	case pubsub.Event[skills.Event]:
		return envelope(pubsub.PayloadTypeSkillsEvent, pubsub.Event[proto.SkillsEvent]{
			Type:    e.Type,
			Payload: skillsEventToProto(e.Payload),
		})
	case pubsub.Event[pinentry.Event]:
		// 终态 handover 为 GPG passphrase prompts 是 local concern
		// 的 host running watcher; pinentry dialog would 不
		// draw 在 remote 客户端's 终态.
		return nil
	case pubsub.Event[tools.ToolProgress]:
		return envelope(pubsub.PayloadTypeToolProgress, pubsub.Event[proto.ToolProgress]{
			Type:    e.Type,
			Payload: proto.ToolProgress(e.Payload),
		})
	default:
		slog.Warn("Unrecognized event type for SSE wrapping", "type", fmt.Sprintf("%T", ev))
		return nil
	}
}

// sessionScopedEventID extracts 会话 ID 从 events 那 belong 到
// single 会话 (interactive prompts 和 live 工具 progress). second
// return 值 reports whether 事件 是 会话-scoped 在 all. An 事件
// whose 会话 ID 是 empty 是 treated 作为 unscoped: it cannot match any
// 客户端's current 会话, 所以 scoping it would drop it 为 everyone.
func sessionScopedEventID(ev any) (string, bool) {
	var id string
	switch e := ev.(type) {
	case pubsub.Event[permission.PermissionRequest]:
		id = e.Payload.SessionID
	case pubsub.Event[permission.PermissionNotification]:
		id = e.Payload.SessionID
	case pubsub.Event[question.Request]:
		id = e.Payload.SessionID
	case pubsub.Event[question.Notification]:
		id = e.Payload.SessionID
	case pubsub.Event[tools.ToolProgress]:
		id = e.Payload.SessionID
	default:
		return "", false
	}
	return id, id != ""
}

// deliverToClient reports whether 工作区 事件 should 是 written
// 到 given 客户端's SSE 流. 会话-scoped 提示词 events 是
// 只 delivered 到 clients currently viewing 会话 那 raised
// them: 权限 dialog 或 question form 是 只 actionable there,
// 和 broadcasting it 到 every 客户端 在 工作区 lets sessions
// steal each other's prompts. Prompts raised 通过 子代理 sessions
// (agent 工具, agentic fetch) resolve 到 top-level 会话 所以
// viewer 的 parent 会话 can answer them. Clients 在 landing
// screen ( explicitly cleared 会话) receive no prompts, while
// clients 那 never reported 会话 在 all (e.g. API clients
// predating 会话 reporting) fail open 所以 their prompts 是 不
// stranded. All other events 是 delivered unfiltered.
func (c *controllerV1) deliverToClient(ctx context.Context, workspaceID, clientID string, ev any) bool {
	sessionID, scoped := sessionScopedEventID(ev)
	if !scoped {
		return true
	}
	current, reported := c.backend.ClientCurrentSession(workspaceID, clientID)
	if !reported {
		// 客户端 never reported current 会话, 所以 there 是
		// nothing 到 scope against. Fail open rather than stranding
		// 提示词 nobody can answer.
		return true
	}
	if current == "" {
		return false
	}
	if sessionID == current {
		return true
	}
	root, err := c.backend.RootSessionID(ctx, workspaceID, sessionID)
	if err != nil {
		// Fail open: unresolvable 会话 是 delivered 作为 before
		// rather than stranding 提示词 nobody can answer.
		slog.Debug("Failed to resolve root session for event routing",
			"session_id", sessionID, "error", err)
		return true
	}
	return root == current
}

// envelope marshals inner 事件 和 wraps it 在 pubsub.Payload.
func envelope(payloadType pubsub.PayloadType, inner any) *pubsub.Payload {
	raw, err := json.Marshal(inner)
	if err != nil {
		slog.Error("Failed to marshal event payload", "error", err)
		return nil
	}
	return &pubsub.Payload{
		Type:    payloadType,
		Payload: raw,
	}
}

func mcpEventTypeToProto(t mcp.EventType) proto.MCPEventType {
	switch t {
	case mcp.EventStateChanged:
		return proto.MCPEventStateChanged
	case mcp.EventToolsListChanged:
		return proto.MCPEventToolsListChanged
	case mcp.EventPromptsListChanged:
		return proto.MCPEventPromptsListChanged
	case mcp.EventResourcesListChanged:
		return proto.MCPEventResourcesListChanged
	case mcp.EventChannelMessage:
		return proto.MCPEventChannelMessage
	default:
		// Unsupported type. Return empty 所以 callers can drop it rather
		// than coercing 到 state_changed.
		return ""
	}
}

func sessionToProto(s session.Session) proto.Session {
	return proto.Session{
		ID:               s.ID,
		ParentSessionID:  s.ParentSessionID,
		Title:            s.Title,
		SummaryMessageID: s.SummaryMessageID,
		MessageCount:     s.MessageCount,
		PromptTokens:     s.PromptTokens,
		CompletionTokens: s.CompletionTokens,
		EstimatedUsage:   s.EstimatedUsage,
		Cost:             s.Cost,
		Todos:            todosToProto(s.Todos),
		Channel:          s.Channel,
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        s.UpdatedAt,
	}
}

func protoToSession(s proto.Session) session.Session {
	sess := session.Session{
		ID:               s.ID,
		ParentSessionID:  s.ParentSessionID,
		Title:            s.Title,
		SummaryMessageID: s.SummaryMessageID,
		MessageCount:     s.MessageCount,
		PromptTokens:     s.PromptTokens,
		CompletionTokens: s.CompletionTokens,
		EstimatedUsage:   s.EstimatedUsage,
		Cost:             s.Cost,
		Channel:          s.Channel,
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        s.UpdatedAt,
	}
	for _, t := range s.Todos {
		sess.Todos = append(sess.Todos, session.Todo{
			Content:    t.Content,
			Status:     session.TodoStatus(t.Status),
			ActiveForm: t.ActiveForm,
		})
	}
	return sess
}

// isSessionBusy reports whether given 工作区 has 在-flight
// agent 运行 为 sessionID. 它 tolerates nil 工作区 (treating it 作为
// "不 busy") 所以 REST handlers can pass GetWorkspace's 结果 through
// unconditionally — 工作区 lookup 错误 是 already surfaced 通过
// prior ListSessions/GetSession 调用 当 relevant.
func isSessionBusy(ws *backend.Workspace, sessionID string) bool {
	if ws == nil {
		return false
	}
	coord := ws.Coordinator()
	return coord != nil && coord.IsSessionBusy(sessionID)
}

// attachedClients returns number 的 clients currently viewing
// sessionID 在 ws. Hold-只 clients (streams == 0) do 不 contribute.
// 一个 nil 工作区 是 treated 作为 zero 所以 handlers can pass GetWorkspace's
// 结果 through without extra guard.
func attachedClients(ws *backend.Workspace, sessionID string) int {
	if ws == nil {
		return 0
	}
	return ws.AttachedClientsForSession(sessionID)
}

func todosToProto(todos []session.Todo) []proto.Todo {
	if len(todos) == 0 {
		return nil
	}
	out := make([]proto.Todo, len(todos))
	for i, t := range todos {
		out[i] = proto.Todo{
			Content:    t.Content,
			Status:     string(t.Status),
			ActiveForm: t.ActiveForm,
		}
	}
	return out
}

func fileToProto(f history.File) proto.File {
	return proto.File{
		ID:        f.ID,
		SessionID: f.SessionID,
		Path:      f.Path,
		Content:   f.Content,
		Version:   f.Version,
		CreatedAt: f.CreatedAt,
		UpdatedAt: f.UpdatedAt,
	}
}

func messageToProto(m message.Message) proto.Message {
	msg := proto.Message{
		ID:                      m.ID,
		SessionID:               m.SessionID,
		Role:                    proto.MessageRole(m.Role),
		Model:                   m.Model,
		ModelName:               m.ModelName,
		Provider:                m.Provider,
		PrismModelID:            m.PrismModelID,
		PrismModelName:          m.PrismModelName,
		PrismHypercreditSavings: m.PrismHypercreditSavings,
		PrismDollarSavings:      m.PrismDollarSavings,
		CreatedAt:               m.CreatedAt,
		UpdatedAt:               m.UpdatedAt,
		IsSummaryMessage:        m.IsSummaryMessage,
	}

	for _, p := range m.Parts {
		switch v := p.(type) {
		case message.TextContent:
			msg.Parts = append(msg.Parts, proto.TextContent{Text: v.Text, Hidden: v.Hidden})
		case message.ReasoningContent:
			msg.Parts = append(msg.Parts, proto.ReasoningContent{
				Thinking:   v.Thinking,
				Signature:  v.Signature,
				StartedAt:  v.StartedAt,
				FinishedAt: v.FinishedAt,
			})
		case message.ToolCall:
			msg.Parts = append(msg.Parts, proto.ToolCall{
				ID:          v.ID,
				Name:        v.Name,
				Input:       v.Input,
				Finished:    v.Finished,
				StartedAtMs: v.StartedAtMs,
			})
		case message.ToolResult:
			msg.Parts = append(msg.Parts, proto.ToolResult{
				ToolCallID:   v.ToolCallID,
				Name:         v.Name,
				Content:      v.Content,
				Data:         v.Data,
				MIMEType:     v.MIMEType,
				Metadata:     v.Metadata,
				IsError:      v.IsError,
				FinishedAtMs: v.FinishedAtMs,
			})
		case message.Finish:
			msg.Parts = append(msg.Parts, proto.Finish{
				Reason:  proto.FinishReason(v.Reason),
				Time:    v.Time,
				Message: v.Message,
				Details: v.Details,
			})
		case message.ImageURLContent:
			msg.Parts = append(msg.Parts, proto.ImageURLContent{URL: v.URL, Detail: v.Detail})
		case message.BinaryContent:
			msg.Parts = append(msg.Parts, proto.BinaryContent{Path: v.Path, MIMEType: v.MIMEType, Data: v.Data})
		case message.ShellCommand:
			msg.Parts = append(msg.Parts, proto.ShellCommand{
				Command:  v.Command,
				Output:   v.Output,
				ExitCode: v.ExitCode,
			})
		case message.SubAgentReport:
			msg.Parts = append(msg.Parts, proto.SubAgentReport{
				Label:  v.Label,
				Output: v.Output,
				Failed: v.Failed,
				Model:  v.Model,
			})
		}
	}

	return msg
}

// skillsEventToProto converts skills.事件 into 它的 wire form. Errors
// 是 flattened 到 strings because 错误 does 不 round-trip over JSON.
func skillsEventToProto(e skills.Event) proto.SkillsEvent {
	if len(e.States) == 0 {
		return proto.SkillsEvent{}
	}
	out := proto.SkillsEvent{States: make([]proto.SkillState, len(e.States))}
	for i, s := range e.States {
		entry := proto.SkillState{
			Name:  s.Name,
			Path:  s.Path,
			State: proto.SkillDiscoveryState(s.State),
		}
		if s.Err != nil {
			entry.Error = s.Err.Error()
		}
		out.States[i] = entry
	}
	return out
}

func messagesToProto(msgs []message.Message) []proto.Message {
	out := make([]proto.Message, len(msgs))
	for i, m := range msgs {
		out[i] = messageToProto(m)
	}
	return out
}

func questionsToProto(qs []question.Question) []proto.QuestionItem {
	if len(qs) == 0 {
		return nil
	}
	out := make([]proto.QuestionItem, len(qs))
	for i, q := range qs {
		choices := make([]proto.QuestionChoice, len(q.Choices))
		for j, c := range q.Choices {
			choices[j] = proto.QuestionChoice{
				ID:          c.ID,
				Label:       c.Label,
				Description: c.Description,
			}
		}
		out[i] = proto.QuestionItem{
			ID:          q.ID,
			Type:        string(q.Type),
			Label:       q.Label,
			Question:    q.Text,
			Description: q.Description,
			Choices:     choices,
		}
	}
	return out
}
