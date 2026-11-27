// user.go —— UI 消息渲染：把后端来的消息/工具调用转成 TUI 可显示的项。
// 属于产物链的末端：db/message → Workspace.ListMessages → ui/chat → 屏幕。

package chat

import (
	"encoding/xml"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// skillInvocation represents XML structure 为 loaded skill.
type skillInvocation struct {
	Name         string `xml:"name"`
	Description  string `xml:"description"`
	Location     string `xml:"location"`
	Instructions string `xml:"instructions"`
}

// channelMessage represents XML structure 为 渠道-originated
// 消息 pushed 通过 MCP 渠道 服务端.
type channelMessage struct {
	XMLName    xml.Name `xml:"channel"`
	Source     string   `xml:"source,attr"`
	Sender     string   `xml:"sender,attr"`
	SenderName string   `xml:"sender_name,attr"`
	Time       string   `xml:"time,attr"`
	Content    string   `xml:",chardata"`
}

// UserMessageItem represents 用户消息 在 chat UI.
type UserMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	attachments *attachments.Renderer
	message     *message.Message
	sty         *styles.Styles
}

// newAttachmentRenderer builds renderer 用户 messages use 到 draw their
// attachments.
func newAttachmentRenderer(sty *styles.Styles) *attachments.Renderer {
	return attachments.NewRenderer(sty.Attachments)
}

// NewUserMessageItem creates a new UserMessageItem.
func NewUserMessageItem(sty *styles.Styles, message *message.Message, attachments *attachments.Renderer) MessageItem {
	v := list.NewVersioned()
	return &UserMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		attachments:              attachments,
		message:                  message,
		sty:                      sty,
	}
}

// 消息 returns 消息 item renders.
func (m *UserMessageItem) Message() *message.Message {
	return m.message
}

// RawRender implements [MessageItem].
func (m *UserMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	content, height, ok := m.getCachedRender(cappedWidth)
	// 缓存 hit
	if ok {
		return m.renderHighlighted(content, cappedWidth, height)
	}

	msgContent := strings.TrimSpace(m.message.Content().Text)

	// Check 如果 这 是 skill invocation (loaded_skill XML)
	if strings.HasPrefix(msgContent, "<loaded_skill>") {
		content = m.renderSkillInvocation(msgContent, cappedWidth)
		height = lipgloss.Height(content)
		m.setCachedRender(content, cappedWidth, height)
		return m.renderHighlighted(content, cappedWidth, height)
	}

	// Check 如果 这 是 渠道-originated 消息.
	if strings.HasPrefix(msgContent, "<channel") {
		content = m.renderChannelMessage(msgContent, cappedWidth)
		height = lipgloss.Height(content)
		m.setCachedRender(content, cappedWidth, height)
		return m.renderHighlighted(content, cappedWidth, height)
	}

	renderer := common.UserMarkdownRenderer(m.sty, cappedWidth)
	mu := common.LockMarkdownRenderer(renderer)

	mu.Lock()
	result, err := renderer.Render(msgContent)
	mu.Unlock()

	if err != nil {
		content = msgContent
	} else {
		content = strings.TrimSuffix(result, "\n")
	}

	if len(m.message.BinaryContent()) > 0 {
		attachmentsStr := m.renderAttachments(cappedWidth)
		if content == "" {
			content = attachmentsStr
		} else {
			content = strings.Join([]string{content, "", attachmentsStr}, "\n")
		}
	}

	height = lipgloss.Height(content)
	m.setCachedRender(content, cappedWidth, height)
	return m.renderHighlighted(content, cappedWidth, height)
}

// renderSkillInvocation renders loaded_skill XML 作为 special UI element.
func (m *UserMessageItem) renderSkillInvocation(content string, width int) string {
	var skill skillInvocation
	if err := xml.Unmarshal([]byte(content), &skill); err != nil {
		// If parsing fails, just render 作为 markdown
		renderer := common.UserMarkdownRenderer(m.sty, width)
		mu := common.LockMarkdownRenderer(renderer)

		mu.Lock()
		result, err := renderer.Render(content)
		mu.Unlock()

		if err != nil {
			return content
		}
		return strings.TrimSuffix(result, "\n")
	}

	return toolOutputSkillContent(m.sty, skill.Name, skill.Description)
}

// renderChannelMessage parses <渠道 source="..." ...>body</渠道>
// element 和 renders 只 body 作为 markdown. metadata
// (sender, 渠道 source, timestamp) 是 rendered separately 通过
// ChannelInfoItem 所以 it appears outside 消息 body, mirroring
// 助手 info line.
func (m *UserMessageItem) renderChannelMessage(raw string, width int) string {
	var ch channelMessage
	if err := xml.Unmarshal([]byte(raw), &ch); err != nil {
		return m.fallbackRender(raw, width)
	}

	body := strings.TrimSpace(ch.Content)
	if body == "" {
		return ""
	}

	renderer := common.UserMarkdownRenderer(m.sty, width)
	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	result, err := renderer.Render(body)
	mu.Unlock()
	if err != nil {
		return body
	}
	return strings.TrimSuffix(result, "\n")
}

// fallbackRender renders 文本 作为 plain markdown 当 XML parsing fails.
func (m *UserMessageItem) fallbackRender(content string, width int) string {
	renderer := common.UserMarkdownRenderer(m.sty, width)
	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	result, err := renderer.Render(content)
	mu.Unlock()
	if err != nil {
		return content
	}
	return strings.TrimSuffix(result, "\n")
}

// Render implements MessageItem.
func (m *UserMessageItem) Render(width int) string {
	// Bypass prefix 缓存 while highlight range 是 活跃 所以
	// selection drags reflect immediately without invalidating the
	// 缓存. Highlight changes 是 intentionally applied "above"
	// prefix 缓存.
	useCache := !m.isHighlighted()
	var key uint64
	if m.focused {
		key = 1
	}
	if useCache {
		if cached, ok := m.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	var prefix string
	if m.focused {
		prefix = m.sty.Messages.UserFocused.Render()
	} else {
		prefix = m.sty.Messages.UserBlurred.Render()
	}
	lines := strings.Split(m.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	if useCache {
		m.setCachedPrefixedRender(out, width, key)
	}
	return out
}

// ID implements MessageItem.
func (m *UserMessageItem) ID() string {
	return m.message.ID
}

// renderAttachments 渲染 attachments.
func (m *UserMessageItem) renderAttachments(width int) string {
	var attachments []message.Attachment
	for _, at := range m.message.BinaryContent() {
		attachments = append(attachments, message.Attachment{
			FileName: at.Path,
			MimeType: at.MIMEType,
		})
	}
	// 这 消息 是 already posted, 所以 附件 can't 是 removed;
	// don't render the remove button.
	return m.attachments.Render(attachments, false, false, width)
}

// HandleKeyEvent implements KeyEventHandler.
func (m *UserMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if k := key.String(); k == "c" || k == "y" {
		text := m.message.Content().Text
		return true, common.CopyToClipboard(text, "Message copied to clipboard")
	}
	return false, nil
}
