package message

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"github.com/charmbracelet/crush/internal/stringext"
	"github.com/charmbracelet/x/ansi"
)

type MessageRole string

const (
	Assistant MessageRole = "assistant"
	User      MessageRole = "user"
	System    MessageRole = "system"
	Tool      MessageRole = "tool"
)

// mediaLoadFailedPlaceholder 是 文本 substituted 为 图片 data 那
// cannot 是 decoded during 会话 重放.
const mediaLoadFailedPlaceholder = "[Image data could not be loaded]"

type FinishReason string

const (
	FinishReasonEndTurn   FinishReason = "end_turn"
	FinishReasonMaxTokens FinishReason = "max_tokens"
	FinishReasonToolUse   FinishReason = "tool_use"
	FinishReasonCanceled  FinishReason = "canceled"
	FinishReasonError     FinishReason = "error"
	// FinishReasonContentFilter 是 提供方 safety/拒绝 stop
	// (Anthropic stop_reason=拒绝, OpenAI content_filter, etc.).
	// TUI renders 这 作为 REFUSED banner rather than silent
	// 空轮次。
	FinishReasonContentFilter FinishReason = "content_filter"

	// 正常情况下不应发生
	FinishReasonUnknown FinishReason = "unknown"
)

type ContentPart interface {
	isPart()
}

type ReasoningContent struct {
	Thinking         string                             `json:"thinking"`
	Signature        string                             `json:"signature"`
	ThoughtSignature string                             `json:"thought_signature"` // Used for google
	ToolID           string                             `json:"tool_id"`           // Used for openrouter google models
	ResponsesData    *openai.ResponsesReasoningMetadata `json:"responses_data"`
	StartedAt        int64                              `json:"started_at,omitempty"`
	FinishedAt       int64                              `json:"finished_at,omitempty"`
}

func (tc ReasoningContent) String() string {
	return tc.Thinking
}

// HasReplayData reports whether reasoning must 是 sent back 到
// 提供方. Providers may return signed reasoning 用 no visible 文本,
// 和 dropping it changes prefix their signatures 是 bound 到.
func (tc ReasoningContent) HasReplayData() bool {
	return tc.Thinking != "" || tc.Signature != "" || tc.ThoughtSignature != "" || tc.ResponsesData != nil
}
func (ReasoningContent) isPart() {}

type TextContent struct {
	Text string `json:"text"`
	// Hidden marks generated 用户 continuations 那 remain 在 模型 history.
	Hidden bool `json:"hidden,omitempty"`
	// 上下文 是 文本 sent 到 模型 ahead 的 提示词, such 作为 host
	// 上下文 和 用户 备注. 它 是 存储 所以 replayed history matches
	// what 模型 saw, which signed thinking blocks depend 在.
	Context string `json:"context,omitempty"`
}

func (tc TextContent) String() string {
	return tc.Text
}

func (TextContent) isPart() {}

type ImageURLContent struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func (iuc ImageURLContent) String() string {
	return iuc.URL
}

func (ImageURLContent) isPart() {}

type BinaryContent struct {
	Path     string
	MIMEType string
	Data     []byte
}

func (bc BinaryContent) String(p catwalk.InferenceProvider) string {
	base64Encoded := base64.StdEncoding.EncodeToString(bc.Data)
	if p == catwalk.InferenceProviderOpenAI {
		return "data:" + bc.MIMEType + ";base64," + base64Encoded
	}
	return base64Encoded
}

func (BinaryContent) isPart() {}

type ToolCall struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Input            string `json:"input"`
	ProviderExecuted bool   `json:"provider_executed"`
	Finished         bool   `json:"finished"`
	// StartedAtMs 是 当 调用 first appeared, 在 Unix milliseconds.
	StartedAtMs int64 `json:"started_at_ms,omitempty"`
}

func (ToolCall) isPart() {}

type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	Data       string `json:"data"`
	MIMEType   string `json:"mime_type"`
	Metadata   string `json:"metadata"`
	IsError    bool   `json:"is_error"`
	// FinishedAtMs 是 当 这 reply was recorded, 在 Unix milliseconds.
	FinishedAtMs int64 `json:"finished_at_ms,omitempty"`
}

func (ToolResult) isPart() {}

type Finish struct {
	Reason  FinishReason `json:"reason"`
	Time    int64        `json:"time"`
	Message string       `json:"message,omitempty"`
	Details string       `json:"details,omitempty"`
}

func (Finish) isPart() {}

// ShellCommand stores bang-mode shell command 和 它的 output 作为
// distinct content 片段 所以 it can 是 reconstructed 在 会话 restore.
type ShellCommand struct {
	Command  string `json:"command"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

func (ShellCommand) isPart() {}

// HasShellCommand reports whether 消息 contains any ShellCommand parts.
func (m *Message) HasShellCommand() bool {
	for _, part := range m.Parts {
		if _, ok := part.(ShellCommand); ok {
			return true
		}
	}
	return false
}

// IsPrompt reports whether 消息 是 something 用户 typed.
func (m *Message) IsPrompt() bool {
	return m.Role == User && !m.HasShellCommand() && len(m.SubAgentReports()) == 0
}

// ShellCommands returns all ShellCommand parts 从 消息.
func (m *Message) ShellCommands() []ShellCommand {
	var cmds []ShellCommand
	for _, part := range m.Parts {
		if sc, ok := part.(ShellCommand); ok {
			cmds = append(cmds, sc)
		}
	}
	return cmds
}

// SubAgentReport stores 结果 detached 子代理 reported back, 作为
// distinct content 片段 所以 UI can render it 作为 子代理 结果 rather
// than 作为 something 用户 typed.
type SubAgentReport struct {
	Label  string `json:"label"`
	Output string `json:"output"`
	Failed bool   `json:"failed,omitempty"`
	// 模型 是 提供方/模型, 为 display.
	Model string `json:"model,omitempty"`
}

func (SubAgentReport) isPart() {}

// PromptText 是 报告 作为 模型 reads it.
func (r SubAgentReport) PromptText() string {
	return fmt.Sprintf("<subagent-report label=%q>\n%s\n</subagent-report>", r.Label, r.Output)
}

// ParseSubAgentReportPrompt recovers 报告 从 它的 PromptText, 为
// places such 作为 提示词 队列 那 只 carry 文本.
func ParseSubAgentReportPrompt(prompt string) (SubAgentReport, bool) {
	const prefix, suffix = "<subagent-report label=", "\n</subagent-report>"
	rest, ok := strings.CutPrefix(prompt, prefix)
	if !ok {
		return SubAgentReport{}, false
	}
	quoted, output, ok := strings.Cut(rest, ">\n")
	if !ok {
		return SubAgentReport{}, false
	}
	label, err := strconv.Unquote(quoted)
	if err != nil {
		return SubAgentReport{}, false
	}
	return SubAgentReport{Label: label, Output: strings.TrimSuffix(output, suffix)}, true
}

// HasSubAgentReport reports whether 消息 contains any SubAgentReport parts.
func (m *Message) HasSubAgentReport() bool {
	for _, part := range m.Parts {
		if _, ok := part.(SubAgentReport); ok {
			return true
		}
	}
	return false
}

// SubAgentReports returns all SubAgentReport parts 从 消息.
func (m *Message) SubAgentReports() []SubAgentReport {
	var reports []SubAgentReport
	for _, part := range m.Parts {
		if r, ok := part.(SubAgentReport); ok {
			reports = append(reports, r)
		}
	}
	return reports
}

type Message struct {
	ID                      string
	Role                    MessageRole
	SessionID               string
	Parts                   []ContentPart
	Model                   string
	ModelName               string
	Provider                string
	CreatedAt               int64
	UpdatedAt               int64
	IsSummaryMessage        bool
	PrismModelID            string
	PrismModelName          string
	PrismHypercreditSavings *float64
	PrismDollarSavings      *float64
}

func (m *Message) Content() TextContent {
	for _, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			return c
		}
	}
	return TextContent{}
}

func (m *Message) ReasoningContent() ReasoningContent {
	for _, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			return c
		}
	}
	return ReasoningContent{}
}

func (m *Message) ImageURLContent() []ImageURLContent {
	imageURLContents := make([]ImageURLContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ImageURLContent); ok {
			imageURLContents = append(imageURLContents, c)
		}
	}
	return imageURLContents
}

func (m *Message) BinaryContent() []BinaryContent {
	binaryContents := make([]BinaryContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(BinaryContent); ok {
			binaryContents = append(binaryContents, c)
		}
	}
	return binaryContents
}

func (m *Message) ToolCalls() []ToolCall {
	toolCalls := make([]ToolCall, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			toolCalls = append(toolCalls, c)
		}
	}
	return toolCalls
}

func (m *Message) ToolResults() []ToolResult {
	toolResults := make([]ToolResult, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolResult); ok {
			toolResults = append(toolResults, c)
		}
	}
	return toolResults
}

func (m *Message) IsFinished() bool {
	for _, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			return true
		}
	}
	return false
}

func (m *Message) FinishPart() *Finish {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return &c
		}
	}
	return nil
}

func (m *Message) FinishReason() FinishReason {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return c.Reason
		}
	}
	return ""
}

// IsErrorLike reports whether 消息 finished 用 错误-style
// banner ( real 错误 或 提供方 safety 拒绝). TUI renders
// both through same banner 路径.
func (m *Message) IsErrorLike() bool {
	switch m.FinishReason() {
	case FinishReasonError, FinishReasonContentFilter:
		return true
	}
	return false
}

// IsTruncated reports whether 模型 stopped because it ran out 的
// output tokens instead 的 completing 响应 在 它的 own terms.
// TUI renders 这 作为 TRUNCATED banner, alongside
// 错误-style banners, 所以 响应 那 stops mid-sentence 是 never
// mistaken 为 完成 one.
func (m *Message) IsTruncated() bool {
	return m.FinishReason() == FinishReasonMaxTokens
}

func (m *Message) IsThinking() bool {
	if m.ReasoningContent().Thinking != "" && m.Content().Text == "" && !m.IsFinished() {
		return true
	}
	return false
}

func (m *Message) AppendContent(delta string) {
	found := false
	for i, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			c.Text += delta
			m.Parts[i] = c
			found = true
		}
	}
	if !found {
		m.Parts = append(m.Parts, TextContent{Text: delta})
	}
}

func (m *Message) AppendReasoningContent(delta string) {
	found := false
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			m.Parts[i] = ReasoningContent{
				Thinking:   c.Thinking + delta,
				Signature:  c.Signature,
				StartedAt:  c.StartedAt,
				FinishedAt: c.FinishedAt,
			}
			found = true
		}
	}
	if !found {
		m.Parts = append(m.Parts, ReasoningContent{
			Thinking:  delta,
			StartedAt: time.Now().Unix(),
		})
	}
}

func (m *Message) AppendThoughtSignature(signature string, toolCallID string) {
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			m.Parts[i] = ReasoningContent{
				Thinking:         c.Thinking,
				ThoughtSignature: c.ThoughtSignature + signature,
				ToolID:           toolCallID,
				Signature:        c.Signature,
				StartedAt:        c.StartedAt,
				FinishedAt:       c.FinishedAt,
			}
			return
		}
	}
	m.Parts = append(m.Parts, ReasoningContent{ThoughtSignature: signature})
}

func (m *Message) AppendReasoningSignature(signature string) {
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			m.Parts[i] = ReasoningContent{
				Thinking:   c.Thinking,
				Signature:  c.Signature + signature,
				StartedAt:  c.StartedAt,
				FinishedAt: c.FinishedAt,
			}
			return
		}
	}
	m.Parts = append(m.Parts, ReasoningContent{Signature: signature})
}

func (m *Message) SetReasoningResponsesData(data *openai.ResponsesReasoningMetadata) {
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			m.Parts[i] = ReasoningContent{
				Thinking:      c.Thinking,
				ResponsesData: data,
				StartedAt:     c.StartedAt,
				FinishedAt:    c.FinishedAt,
			}
			return
		}
	}
}

func (m *Message) FinishThinking() {
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			if c.FinishedAt == 0 {
				m.Parts[i] = ReasoningContent{
					Thinking:   c.Thinking,
					Signature:  c.Signature,
					StartedAt:  c.StartedAt,
					FinishedAt: time.Now().Unix(),
				}
			}
			return
		}
	}
}

func (m *Message) ThinkingDuration() time.Duration {
	reasoning := m.ReasoningContent()
	if reasoning.StartedAt == 0 {
		return 0
	}

	endTime := reasoning.FinishedAt
	if endTime == 0 {
		endTime = time.Now().Unix()
	}

	return time.Duration(endTime-reasoning.StartedAt) * time.Second
}

func (m *Message) FinishToolCall(toolCallID string) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == toolCallID {
				m.Parts[i] = ToolCall{
					ID:          c.ID,
					Name:        c.Name,
					Input:       c.Input,
					Finished:    true,
					StartedAtMs: c.StartedAtMs,
				}
				return
			}
		}
	}
}

// toolCallInput returns arguments safe 到 hand 提供方. Empty 或 partial
// 输入变成 {}，因为不同提供方对「空」的含义理解不一致。
func toolCallInput(input string) string {
	if !json.Valid([]byte(input)) {
		return "{}"
	}
	return input
}

// AddToolCall adds 或 replaces 工具调用 通过 ID, keeping time
// 调用 first appeared.
func (m *Message) AddToolCall(tc ToolCall) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok && c.ID == tc.ID {
			tc.StartedAtMs = cmp.Or(tc.StartedAtMs, c.StartedAtMs)
			m.Parts[i] = tc
			return
		}
	}
	tc.StartedAtMs = cmp.Or(tc.StartedAtMs, time.Now().UnixMilli())
	m.Parts = append(m.Parts, tc)
}

func (m *Message) SetToolCalls(tc []ToolCall) {
	// remove any existing 工具调用 片段 it could have multiple
	parts := make([]ContentPart, 0)
	for _, part := range m.Parts {
		if _, ok := part.(ToolCall); ok {
			continue
		}
		parts = append(parts, part)
	}
	m.Parts = parts
	for _, toolCall := range tc {
		m.Parts = append(m.Parts, toolCall)
	}
}

func (m *Message) AddToolResult(tr ToolResult) {
	m.Parts = append(m.Parts, tr)
}

func (m *Message) SetToolResults(tr []ToolResult) {
	for _, toolResult := range tr {
		m.Parts = append(m.Parts, toolResult)
	}
}

// Clone returns deep copy 的 消息 用 independent Parts slice.
// 这 prevents race conditions 当 消息 是 modified concurrently.
func (m *Message) Clone() Message {
	clone := *m
	clone.Parts = make([]ContentPart, len(m.Parts))
	copy(clone.Parts, m.Parts)
	return clone
}

// ResetStreamedContent removes all parts 那 were added during streaming
// (文本, reasoning, 工具 calls, finish) 所以 消息 是 ready 为
// 重试. Non-streamed parts (images, 二进制 attachments, 工具 results,
// shell commands) 是 preserved.
func (m *Message) ResetStreamedContent() {
	kept := m.Parts[:0]
	for _, part := range m.Parts {
		switch part.(type) {
		case TextContent, ReasoningContent, ToolCall, Finish:
			// 丢弃流式片段。
		default:
			kept = append(kept, part)
		}
	}
	m.Parts = kept
}

func (m *Message) AddFinish(reason FinishReason, message, details string) {
	m.AddFinishAt(reason, message, details, time.Now().Unix())
}

// AddFinishAt 是 AddFinish 用 explicit timestamp, 为 backdating
// repaired turn 所以 响应-time statistics stay honest.
func (m *Message) AddFinishAt(reason FinishReason, message, details string, at int64) {
	// remove any existing finish 片段
	for i, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			m.Parts = slices.Delete(m.Parts, i, i+1)
			break
		}
	}
	m.Parts = append(m.Parts, Finish{Reason: reason, Time: at, Message: message, Details: details})
}

func (m *Message) AddImageURL(url, detail string) {
	m.Parts = append(m.Parts, ImageURLContent{URL: url, Detail: detail})
}

func (m *Message) AddBinary(mimeType string, data []byte) {
	m.Parts = append(m.Parts, BinaryContent{MIMEType: mimeType, Data: data})
}

func PromptWithTextAttachments(prompt string, attachments []Attachment) string {
	var sb strings.Builder
	sb.WriteString(prompt)
	addedAttachments := false
	for _, content := range attachments {
		if !content.IsText() {
			continue
		}
		if !addedAttachments {
			sb.WriteString("\n<system_info>The files below have been attached by the user, consider them in your response</system_info>\n")
			addedAttachments = true
		}
		if content.FilePath != "" {
			fmt.Fprintf(&sb, "<file path='%s'>\n", content.FilePath)
		} else {
			sb.WriteString("<file>\n")
		}
		sb.WriteString("\n")
		sb.Write(content.Content)
		sb.WriteString("\n</file>\n")
	}
	return sb.String()
}

func (m *Message) ToAIMessage() []fantasy.Message {
	var messages []fantasy.Message
	switch m.Role {
	case User:
		var parts []fantasy.MessagePart
		text := m.Content().Text
		var textAttachments []Attachment
		for _, content := range m.BinaryContent() {
			if !strings.HasPrefix(content.MIMEType, "text/") {
				continue
			}
			textAttachments = append(textAttachments, Attachment{
				FilePath: content.Path,
				MimeType: content.MIMEType,
				Content:  content.Data,
			})
		}
		text = m.Content().Context + PromptWithTextAttachments(text, textAttachments)
		// Include 子代理 reports 作为 上下文 为 agent. 片段
		// carries label 和 body separately 所以 UI can render
		// them; 模型 sees them 作为 one tagged block.
		for _, r := range m.SubAgentReports() {
			reportText := r.PromptText()
			if text != "" {
				text += "\n\n" + reportText
			} else {
				text = reportText
			}
		}
		// Include bang-mode shell commands 作为 上下文 为 agent.
		for _, sc := range m.ShellCommands() {
			shellText := fmt.Sprintf("$ %s\n%s\n(exit code %d)", sc.Command, ansi.Strip(sc.Output), sc.ExitCode)
			if text != "" {
				text += "\n\n" + shellText
			} else {
				text = shellText
			}
		}
		if strings.TrimSpace(text) != "" {
			parts = append(parts, fantasy.TextPart{Text: text})
		}
		for _, content := range m.BinaryContent() {
			// skip 文本 attachements
			if strings.HasPrefix(content.MIMEType, "text/") {
				continue
			}
			parts = append(parts, fantasy.FilePart{
				Filename:  content.Path,
				Data:      content.Data,
				MediaType: content.MIMEType,
			})
		}
		messages = append(messages, fantasy.Message{
			Role:    fantasy.MessageRoleUser,
			Content: parts,
		})
	case Assistant:
		var parts []fantasy.MessagePart
		text := m.Content().Text
		reasoning := m.ReasoningContent()
		if reasoning.HasReplayData() {
			reasoningPart := fantasy.ReasoningPart{Text: reasoning.Thinking, ProviderOptions: fantasy.ProviderOptions{}}
			if reasoning.Signature != "" {
				reasoningPart.ProviderOptions[anthropic.Name] = &anthropic.ReasoningOptionMetadata{
					Signature: reasoning.Signature,
				}
			}
			if reasoning.ResponsesData != nil {
				reasoningPart.ProviderOptions[openai.Name] = reasoning.ResponsesData
			}
			if reasoning.ThoughtSignature != "" {
				reasoningPart.ProviderOptions[google.Name] = &google.ReasoningMetadata{
					Signature: reasoning.ThoughtSignature,
					ToolID:    reasoning.ToolID,
				}
			}
			parts = append(parts, reasoningPart)
		}
		if strings.TrimSpace(text) != "" {
			parts = append(parts, fantasy.TextPart{Text: text})
		}
		for _, call := range m.ToolCalls() {
			parts = append(parts, fantasy.ToolCallPart{
				ToolCallID:       call.ID,
				ToolName:         call.Name,
				Input:            toolCallInput(call.Input),
				ProviderExecuted: call.ProviderExecuted,
			})
		}
		messages = append(messages, fantasy.Message{
			Role:    fantasy.MessageRoleAssistant,
			Content: parts,
		})
	case Tool:
		var parts []fantasy.MessagePart
		for _, result := range m.ToolResults() {
			var content fantasy.ToolResultOutputContent
			if result.IsError {
				content = fantasy.ToolResultOutputContentError{
					Error: errors.New(result.Content),
				}
			} else if result.Data != "" {
				if stringext.IsValidBase64(result.Data) {
					content = fantasy.ToolResultOutputContentMedia{
						Data:      result.Data,
						MediaType: result.MIMEType,
					}
				} else {
					content = fantasy.ToolResultOutputContentText{
						Text: mediaLoadFailedPlaceholder,
					}
				}
			} else {
				content = fantasy.ToolResultOutputContentText{
					Text: result.Content,
				}
			}
			parts = append(parts, fantasy.ToolResultPart{
				ToolCallID: result.ToolCallID,
				Output:     content,
			})
		}
		messages = append(messages, fantasy.Message{
			Role:    fantasy.MessageRoleTool,
			Content: parts,
		})
	}
	return messages
}
