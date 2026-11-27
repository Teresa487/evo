package proto

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/message"
)

// CreateMessageParams represents parameters 为 creating 消息.
type CreateMessageParams struct {
	Role      MessageRole   `json:"role"`
	Parts     []ContentPart `json:"parts"`
	Model     string        `json:"model"`
	ModelName string        `json:"model_name,omitempty"`
	Provider  string        `json:"provider,omitempty"`
}

// 消息 represents 消息 在 proto layer.
type Message struct {
	ID                      string        `json:"id"`
	Role                    MessageRole   `json:"role"`
	SessionID               string        `json:"session_id"`
	Parts                   []ContentPart `json:"parts"`
	Model                   string        `json:"model"`
	ModelName               string        `json:"model_name,omitempty"`
	Provider                string        `json:"provider"`
	PrismModelID            string        `json:"prism_model_id,omitempty"`
	PrismModelName          string        `json:"prism_model_name,omitempty"`
	PrismHypercreditSavings *float64      `json:"prism_hypercredit_savings,omitempty"`
	PrismDollarSavings      *float64      `json:"prism_dollar_savings,omitempty"`
	CreatedAt               int64         `json:"created_at"`
	UpdatedAt               int64         `json:"updated_at"`
	IsSummaryMessage        bool          `json:"is_summary_message,omitempty"`
}

// MessageRole represents 角色 的 消息 sender.
type MessageRole string

const (
	Assistant MessageRole = "assistant"
	User      MessageRole = "user"
	System    MessageRole = "system"
	Tool      MessageRole = "tool"
)

// MarshalText implements the [encoding.TextMarshaler] interface.
func (r MessageRole) MarshalText() ([]byte, error) {
	return []byte(r), nil
}

// UnmarshalText implements the [encoding.TextUnmarshaler] interface.
func (r *MessageRole) UnmarshalText(data []byte) error {
	*r = MessageRole(data)
	return nil
}

// FinishReason represents why 消息 generation finished.
type FinishReason string

const (
	FinishReasonEndTurn       FinishReason = "end_turn"
	FinishReasonMaxTokens     FinishReason = "max_tokens"
	FinishReasonToolUse       FinishReason = "tool_use"
	FinishReasonCanceled      FinishReason = "canceled"
	FinishReasonError         FinishReason = "error"
	FinishReasonContentFilter FinishReason = "content_filter"
	FinishReasonUnknown       FinishReason = "unknown"
)

// MarshalText implements the [encoding.TextMarshaler] interface.
func (fr FinishReason) MarshalText() ([]byte, error) {
	return []byte(fr), nil
}

// UnmarshalText implements the [encoding.TextUnmarshaler] interface.
func (fr *FinishReason) UnmarshalText(data []byte) error {
	*fr = FinishReason(data)
	return nil
}

// ContentPart 是 片段 的 消息's content.
type ContentPart interface {
	isPart()
}

// ReasoningContent represents reasoning/thinking 片段 的 消息.
type ReasoningContent struct {
	Thinking   string `json:"thinking"`
	Signature  string `json:"signature"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

// String returns thinking content 作为 string.
func (tc ReasoningContent) String() string {
	return tc.Thinking
}

func (ReasoningContent) isPart() {}

// TextContent represents 文本 片段 的 消息.
type TextContent struct {
	Text   string `json:"text"`
	Hidden bool   `json:"hidden,omitempty"`
}

// String returns 文本 content 作为 string.
func (tc TextContent) String() string {
	return tc.Text
}

func (TextContent) isPart() {}

// ImageURLContent represents 图片 URL 片段 的 消息.
type ImageURLContent struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// String returns 图片 URL 作为 string.
func (iuc ImageURLContent) String() string {
	return iuc.URL
}

func (ImageURLContent) isPart() {}

// BinaryContent represents 二进制 data 在 消息.
type BinaryContent struct {
	Path     string
	MIMEType string
	Data     []byte
}

// String returns base64-encoded string 的 二进制 data.
func (bc BinaryContent) String(p catwalk.InferenceProvider) string {
	base64Encoded := base64.StdEncoding.EncodeToString(bc.Data)
	if p == catwalk.InferenceProviderOpenAI {
		return "data:" + bc.MIMEType + ";base64," + base64Encoded
	}
	return base64Encoded
}

func (BinaryContent) isPart() {}

// ToolCall represents 工具调用 在 消息.
type ToolCall struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Input    string `json:"input"`
	Type     string `json:"type,omitempty"`
	Finished bool   `json:"finished,omitempty"`
	// StartedAtMs 是 当 调用 first appeared, 在 Unix milliseconds.
	StartedAtMs int64 `json:"started_at_ms,omitempty"`
}

func (ToolCall) isPart() {}

// ToolResult represents 结果 的 工具调用.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	Data       string `json:"data,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	Metadata   string `json:"metadata"`
	IsError    bool   `json:"is_error"`
	// FinishedAtMs 是 当 这 reply was recorded, 在 Unix milliseconds.
	FinishedAtMs int64 `json:"finished_at_ms,omitempty"`
}

func (ToolResult) isPart() {}

// Finish represents end 的 消息 generation.
type Finish struct {
	Reason  FinishReason `json:"reason"`
	Time    int64        `json:"time"`
	Message string       `json:"message,omitempty"`
	Details string       `json:"details,omitempty"`
}

func (Finish) isPart() {}

// ShellCommand stores bang-mode shell command 和 它的 output.
type ShellCommand struct {
	Command  string `json:"command"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

func (ShellCommand) isPart() {}

// SubAgentReport stores 结果 detached 子代理 reported back.
type SubAgentReport struct {
	Label  string `json:"label"`
	Output string `json:"output"`
	Failed bool   `json:"failed,omitempty"`
	// 模型 是 提供方/模型, 为 display.
	Model string `json:"model,omitempty"`
}

func (SubAgentReport) isPart() {}

// MarshalJSON implements the [json.Marshaler] interface.
func (m Message) MarshalJSON() ([]byte, error) {
	parts, err := MarshalParts(m.Parts)
	if err != nil {
		return nil, err
	}

	type Alias Message
	return json.Marshal(&struct {
		Parts json.RawMessage `json:"parts"`
		*Alias
	}{
		Parts: json.RawMessage(parts),
		Alias: (*Alias)(&m),
	})
}

// UnmarshalJSON implements the [json.Unmarshaler] interface.
func (m *Message) UnmarshalJSON(data []byte) error {
	type Alias Message
	aux := &struct {
		Parts json.RawMessage `json:"parts"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	parts, err := UnmarshalParts([]byte(aux.Parts))
	if err != nil {
		return err
	}

	m.Parts = parts
	return nil
}

// Content returns first 文本 content 片段.
func (m *Message) Content() TextContent {
	for _, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			return c
		}
	}
	return TextContent{}
}

// ReasoningContent returns first reasoning content 片段.
func (m *Message) ReasoningContent() ReasoningContent {
	for _, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			return c
		}
	}
	return ReasoningContent{}
}

// ImageURLContent returns all 图片 URL content parts.
func (m *Message) ImageURLContent() []ImageURLContent {
	imageURLContents := make([]ImageURLContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ImageURLContent); ok {
			imageURLContents = append(imageURLContents, c)
		}
	}
	return imageURLContents
}

// BinaryContent returns all 二进制 content parts.
func (m *Message) BinaryContent() []BinaryContent {
	binaryContents := make([]BinaryContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(BinaryContent); ok {
			binaryContents = append(binaryContents, c)
		}
	}
	return binaryContents
}

// ToolCalls returns all 工具调用 parts.
func (m *Message) ToolCalls() []ToolCall {
	toolCalls := make([]ToolCall, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			toolCalls = append(toolCalls, c)
		}
	}
	return toolCalls
}

// ToolResults returns all 工具结果 parts.
func (m *Message) ToolResults() []ToolResult {
	toolResults := make([]ToolResult, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolResult); ok {
			toolResults = append(toolResults, c)
		}
	}
	return toolResults
}

// IsFinished returns true 如果 消息 has finish 片段.
func (m *Message) IsFinished() bool {
	for _, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			return true
		}
	}
	return false
}

// FinishPart returns finish 片段 如果 present.
func (m *Message) FinishPart() *Finish {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return &c
		}
	}
	return nil
}

// FinishReason returns finish reason 如果 present.
func (m *Message) FinishReason() FinishReason {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return c.Reason
		}
	}
	return ""
}

// IsThinking returns true 如果 消息 是 currently 在 thinking state.
func (m *Message) IsThinking() bool {
	return m.ReasoningContent().Thinking != "" && m.Content().Text == "" && !m.IsFinished()
}

// AppendContent appends 文本 到 文本 content 片段.
func (m *Message) AppendContent(delta string) {
	found := false
	for i, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			m.Parts[i] = TextContent{Text: c.Text + delta, Hidden: c.Hidden}
			found = true
		}
	}
	if !found {
		m.Parts = append(m.Parts, TextContent{Text: delta})
	}
}

// AppendReasoningContent appends 文本 到 reasoning content 片段.
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

// AppendReasoningSignature appends signature 到 reasoning content 片段.
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

// FinishThinking marks reasoning content 作为 finished.
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

// ThinkingDuration returns duration 的 thinking phase.
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

// FinishToolCall marks 工具调用 作为 finished.
func (m *Message) FinishToolCall(toolCallID string) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == toolCallID {
				m.Parts[i] = ToolCall{
					ID:          c.ID,
					Name:        c.Name,
					Input:       c.Input,
					Type:        c.Type,
					Finished:    true,
					StartedAtMs: c.StartedAtMs,
				}
				return
			}
		}
	}
}

// AddToolCall adds 或 updates 工具调用, keeping 它的 recorded start.
func (m *Message) AddToolCall(tc ToolCall) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok && c.ID == tc.ID {
			tc.StartedAtMs = cmp.Or(tc.StartedAtMs, c.StartedAtMs)
			m.Parts[i] = tc
			return
		}
	}
	m.Parts = append(m.Parts, tc)
}

// SetToolCalls replaces all 工具调用 parts.
func (m *Message) SetToolCalls(tc []ToolCall) {
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

// AddToolResult adds 工具结果.
func (m *Message) AddToolResult(tr ToolResult) {
	m.Parts = append(m.Parts, tr)
}

// SetToolResults adds multiple 工具 results.
func (m *Message) SetToolResults(tr []ToolResult) {
	for _, toolResult := range tr {
		m.Parts = append(m.Parts, toolResult)
	}
}

// AddFinish adds finish 片段 到 消息.
func (m *Message) AddFinish(reason FinishReason, message, details string) {
	for i, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			m.Parts = slices.Delete(m.Parts, i, i+1)
			break
		}
	}
	m.Parts = append(m.Parts, Finish{Reason: reason, Time: time.Now().Unix(), Message: message, Details: details})
}

// AddImageURL adds 图片 URL 片段 到 消息.
func (m *Message) AddImageURL(url, detail string) {
	m.Parts = append(m.Parts, ImageURLContent{URL: url, Detail: detail})
}

// AddBinary adds 二进制 content 片段 到 消息.
func (m *Message) AddBinary(mimeType string, data []byte) {
	m.Parts = append(m.Parts, BinaryContent{MIMEType: mimeType, Data: data})
}

type partType string

const (
	reasoningType      partType = "reasoning"
	textType           partType = "text"
	imageURLType       partType = "image_url"
	binaryType         partType = "binary"
	toolCallType       partType = "tool_call"
	toolResultType     partType = "tool_result"
	finishType         partType = "finish"
	shellCommandType   partType = "shell_command"
	subAgentReportType partType = "sub_agent_report"
)

type partWrapper struct {
	Type partType    `json:"type"`
	Data ContentPart `json:"data"`
}

// MarshalParts marshals content parts 到 JSON.
func MarshalParts(parts []ContentPart) ([]byte, error) {
	wrappedParts := make([]partWrapper, len(parts))

	for i, part := range parts {
		var typ partType

		switch part.(type) {
		case ReasoningContent:
			typ = reasoningType
		case TextContent:
			typ = textType
		case ImageURLContent:
			typ = imageURLType
		case BinaryContent:
			typ = binaryType
		case ToolCall:
			typ = toolCallType
		case ToolResult:
			typ = toolResultType
		case Finish:
			typ = finishType
		case ShellCommand:
			typ = shellCommandType
		case SubAgentReport:
			typ = subAgentReportType
		default:
			return nil, fmt.Errorf("unknown part type: %T", part)
		}

		wrappedParts[i] = partWrapper{
			Type: typ,
			Data: part,
		}
	}
	return json.Marshal(wrappedParts)
}

// UnmarshalParts unmarshals content parts 从 JSON. 一个 片段 的 type 这
// build does 不 know, such 作为 one added 通过 newer 服务端, 是 skipped 所以
// 那 rest 的 消息 still decodes.
func UnmarshalParts(data []byte) ([]ContentPart, error) {
	temp := []json.RawMessage{}

	if err := json.Unmarshal(data, &temp); err != nil {
		return nil, err
	}

	parts := make([]ContentPart, 0)

	for _, rawPart := range temp {
		var wrapper struct {
			Type partType        `json:"type"`
			Data json.RawMessage `json:"data"`
		}

		if err := json.Unmarshal(rawPart, &wrapper); err != nil {
			return nil, err
		}

		switch wrapper.Type {
		case reasoningType:
			part := ReasoningContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case textType:
			part := TextContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case imageURLType:
			part := ImageURLContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case binaryType:
			part := BinaryContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case toolCallType:
			part := ToolCall{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case toolResultType:
			part := ToolResult{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case finishType:
			part := Finish{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case shellCommandType:
			part := ShellCommand{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case subAgentReportType:
			part := SubAgentReport{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		default:
			slog.Debug("Skipping unknown message part type", "type", wrapper.Type)
		}
	}

	return parts, nil
}

// 附件 represents 文件 附件.
type Attachment struct {
	FilePath string `json:"file_path"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	Content  []byte `json:"content"`
}

// ToMessage converts proto 附件 到 [消息.附件].
func (a Attachment) ToMessage() message.Attachment {
	return message.Attachment{
		FilePath: a.FilePath,
		FileName: a.FileName,
		MimeType: a.MimeType,
		Content:  a.Content,
	}
}

// AttachmentFromMessage converts [消息.附件] 到 proto
// 附件.
func AttachmentFromMessage(a message.Attachment) Attachment {
	return Attachment{
		FilePath: a.FilePath,
		FileName: a.FileName,
		MimeType: a.MimeType,
		Content:  a.Content,
	}
}

// AttachmentsToMessage converts slice 的 proto Attachments 到 slice
// 的 [消息.附件].
func AttachmentsToMessage(as []Attachment) []message.Attachment {
	out := make([]message.Attachment, len(as))
	for i, a := range as {
		out[i] = a.ToMessage()
	}
	return out
}

// AttachmentsFromMessage converts slice 的 [消息.附件] 到
// slice 的 proto Attachments.
func AttachmentsFromMessage(as []message.Attachment) []Attachment {
	out := make([]Attachment, len(as))
	for i, a := range as {
		out[i] = AttachmentFromMessage(a)
	}
	return out
}

// MarshalJSON implements the [json.Marshaler] interface.
func (a Attachment) MarshalJSON() ([]byte, error) {
	type Alias Attachment
	return json.Marshal(&struct {
		Content string `json:"content"`
		*Alias
	}{
		Content: base64.StdEncoding.EncodeToString(a.Content),
		Alias:   (*Alias)(&a),
	})
}

// UnmarshalJSON implements the [json.Unmarshaler] interface.
func (a *Attachment) UnmarshalJSON(data []byte) error {
	type Alias Attachment
	aux := &struct {
		Content string `json:"content"`
		*Alias
	}{
		Alias: (*Alias)(a),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	content, err := base64.StdEncoding.DecodeString(aux.Content)
	if err != nil {
		return err
	}
	a.Content = content
	return nil
}
