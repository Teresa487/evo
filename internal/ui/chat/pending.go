// pending.go —— UI 消息渲染：把后端来的消息/工具调用转成 TUI 可显示的项。
// 属于产物链的末端：db/message → Workspace.ListMessages → ui/chat → 屏幕。

package chat

import (
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/anim"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// PendingAssistantID 是 placeholder spinner's id.
const PendingAssistantID = "pending-assistant"

// PendingUserID 是 id 的 提示词 echo shown until 存储 用户
// 消息 arrives.
const PendingUserID = "pending-user"

// NewPendingUserItem echoes 提示词 into transcript moment it 是
// sent, instead 的 waiting 为 agent 到 存储 it 和 发布 it back.
// 它 是 ordinary 用户消息 item over unsaved 消息, 所以 swap
// 为 存储 one 是 invisible.
func NewPendingUserItem(sty *styles.Styles, content string, atts []message.Attachment) MessageItem {
	parts := []message.ContentPart{message.TextContent{Text: content}}
	for _, at := range atts {
		parts = append(parts, message.BinaryContent{
			Path:     at.FilePath,
			MIMEType: at.MimeType,
			Data:     at.Content,
		})
	}
	return NewUserMessageItem(sty, &message.Message{
		ID:        PendingUserID,
		Role:      message.User,
		Parts:     parts,
		CreatedAt: time.Now().Unix(),
	}, newAttachmentRenderer(sty))
}

// newTurnAnim builds working spinner shown while turn 是 在 flight,
// suffixed 用 那 会话's elapsed time. placeholder 和
// 助手消息 那 replaces it share 这 所以 handover between
// them 是 invisible.
func newTurnAnim(sty *styles.Styles, id, sessionID string) *anim.Anim {
	return anim.New(anim.Settings{
		ID:          id,
		Size:        15,
		GradColorA:  sty.WorkingGradFromColor,
		GradColorB:  sty.WorkingGradToColor,
		LabelColor:  sty.ThinkingLabelColor,
		CycleColors: true,
		Suffix: func() string {
			return common.Elapsed(sessionID)
		},
		SuffixColor: sty.WorkingTimerColor,
	})
}

// PendingAssistantItem holds turn spinner between 提示词 being sent
// 和 助手消息 那 owns animation arriving.
type PendingAssistantItem struct {
	*list.Versioned
	sty  *styles.Styles
	anim *anim.Anim
}

var (
	_ MessageItem  = (*PendingAssistantItem)(nil)
	_ Animatable   = (*PendingAssistantItem)(nil)
	_ animRestyler = (*PendingAssistantItem)(nil)
)

// NewPendingAssistantItem creates placeholder spinner 为 turn sent
// 在 sessionID.
func NewPendingAssistantItem(sty *styles.Styles, sessionID string) *PendingAssistantItem {
	return &PendingAssistantItem{
		Versioned: list.NewVersioned(),
		sty:       sty,
		anim:      newTurnAnim(sty, PendingAssistantID, sessionID),
	}
}

// ID implements MessageItem.
func (p *PendingAssistantItem) ID() string { return PendingAssistantID }

// FilterValue implements list.Item.
func (p *PendingAssistantItem) FilterValue() string { return "" }

// Spinning implements [Animatable].
func (p *PendingAssistantItem) Spinning() bool { return true }

// restyleAnim implements [animRestyler]. 它 mirrors
// [AssistantMessageItem.restyleAnim], which takes the spinner over.
func (p *PendingAssistantItem) restyleAnim() {
	p.anim.SetColors(
		p.sty.WorkingLabelColor,
		p.sty.WorkingGradFromColor,
		p.sty.WorkingGradToColor,
		p.sty.WorkingTimerColor,
	)
}

// Advance implements [Animatable].
func (p *PendingAssistantItem) Advance() bool {
	if !p.anim.Advance() {
		return false
	}
	// 一个 frame changes render 但 不 list 缓存's content hashes.
	p.Bump()
	return true
}

// RawRender implements list.RawRenderable.
func (p *PendingAssistantItem) RawRender(int) string { return p.anim.Render() }

// Render implements list.Item.
func (p *PendingAssistantItem) Render(width int) string {
	prefix := p.sty.Messages.AssistantBlurred.Render()
	lines := strings.Split(p.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
