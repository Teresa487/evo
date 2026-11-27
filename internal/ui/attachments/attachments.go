package attachments

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

const maxFilename = 15

// minFilename 是 narrowest filename may 是 trimmed 到 在 order 到
// keep "N more…" hint alongside it. Below 这 name 是 mostly
// ellipsis 和 identifies nothing, 所以 hint gives way 到 name.
const minFilename = 5

type Keymap struct {
	DeleteMode,
	DeleteAll,
	Escape key.Binding
}

func New(renderer *Renderer, keyMap Keymap) *Attachments {
	return &Attachments{
		keyMap:   keyMap,
		renderer: renderer,
	}
}

type Attachments struct {
	renderer *Renderer
	keyMap   Keymap
	list     []message.Attachment
	deleting bool
}

func (m *Attachments) List() []message.Attachment { return m.list }
func (m *Attachments) Reset()                     { m.list = nil }

func (m *Attachments) Update(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case message.Attachment:
		m.list = append(m.list, msg)
		return true
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keyMap.DeleteMode):
			if len(m.list) > 0 {
				m.deleting = true
			}
			return true
		case m.deleting && key.Matches(msg, m.keyMap.Escape):
			m.deleting = false
			return true
		case m.deleting && key.Matches(msg, m.keyMap.DeleteAll):
			m.deleting = false
			m.list = nil
			return true
		case m.deleting:
			// Handle digit keys 为 individual 附件 deletion.
			r := msg.Code
			if r >= '0' && r <= '9' {
				num := int(r - '0')
				if num < len(m.list) {
					m.list = slices.Delete(m.list, num, num+1)
				}
				m.deleting = false
			}
			return true
		}
	}
	return false
}

// HandleClick processes mouse click 在 given x offset within
// 附件 行. If click lands 在 remove button,
// corresponding 附件 是 removed. 它 returns true 如果 click was
// handled.
func (m *Attachments) HandleClick(x int) bool {
	if m.deleting || len(m.list) == 0 {
		return false
	}
	idx := m.renderer.HitTestRemove(m.list, x)
	if idx >= 0 && idx < len(m.list) {
		m.list = slices.Delete(m.list, idx, idx+1)
		return true
	}
	return false
}

func (m *Attachments) Render(width int) string {
	// editor 是 interactive, 所以 remove button 是 shown.
	return m.renderer.Render(m.list, m.deleting, true, width)
}

// SetStyles updates chip styles used 当 rendering.
func (m *Attachments) SetStyles(s styles.AttachmentStyles) { m.renderer.SetStyles(s) }

func NewRenderer(s styles.AttachmentStyles) *Renderer {
	return &Renderer{styles: s}
}

// SetStyles updates renderer styles 在 place.
func (r *Renderer) SetStyles(s styles.AttachmentStyles) { r.styles = s }

type Renderer struct {
	styles styles.AttachmentStyles
	// bounds stores X-coordinate ranges 的 each chip's remove
	// button 从 most recent Render 调用, 为 mouse hit-testing.
	bounds []chipBounds
}

// chipBounds holds rendered strings 和 X-coordinate range 的
// each chip's remove button 为 hit-testing.
type chipBounds struct {
	startX    int
	removeEnd int // exclusive end X of the remove button (0 if none)
}

// Render renders 附件 chips. Each chip shows icon 和
// filename; 当 showRemove 是 true remove button (✕) follows 在
// right, 和 在 deleting mode 那 slot shows numeral 到 press
// instead, 所以 toggling 删除-mode doesn't shift chips. showRemove
// should 是 false 为 attachments 在 already-posted messages, where
// removal 是 不 possible.
func (r *Renderer) Render(attachments []message.Attachment, deleting, showRemove bool, width int) string {
	var chips []string
	r.bounds = r.bounds[:0]
	if width <= 0 {
		return ""
	}

	removeStr := ""
	removeW := 0
	if showRemove && !deleting {
		removeStr = r.styles.Remove.String()
		removeW = lipgloss.Width(removeStr)
	}

	var offset int
	for i, att := range attachments {
		filename := filepath.Base(att.FileName)
		if ansi.StringWidth(filename) > maxFilename {
			filename = ansi.Truncate(filename, maxFilename, "…")
		}

		iconStr := r.icon(att).String()
		nameStyle := r.styles.Normal
		if !showRemove {
			nameStyle = nameStyle.MarginRight(1)
		}

		trailingStr := removeStr
		if deleting {
			trailingStr = r.styles.Deleting.Render(strconv.Itoa(i))
		}

		hidden := len(attachments) - i
		hintW := 0
		if hidden > 1 {
			hintW = lipgloss.Width(r.more(hidden - 1))
		}

		nameStr := nameStyle.Render(filename)
		chipW := lipgloss.Width(iconStr) + lipgloss.Width(nameStr) + lipgloss.Width(trailingStr)

		if offset+chipW+hintW > width {
			if i > 0 {
				chips = append(chips, r.more(hidden))
				break
			}

			tail := ""
			room := width - lipgloss.Width(iconStr) - lipgloss.Width(trailingStr) - hintW - nameStyle.GetHorizontalFrameSize()
			if hidden > 1 && room >= minFilename {
				nameStr = nameStyle.Render(ansi.Truncate(filename, room, "…"))
				chipW = lipgloss.Width(iconStr) + lipgloss.Width(nameStr) + lipgloss.Width(trailingStr)
				tail = r.more(hidden - 1)
			}

			chips = append(chips, iconStr, nameStr, trailingStr)
			r.track(chipW, removeW, width)
			if tail != "" {
				chips = append(chips, tail)
			}
			break
		}

		chips = append(chips, iconStr, nameStr, trailingStr)
		r.track(offset+chipW, removeW, width)
		offset += chipW
	}

	return ansi.Truncate(lipgloss.JoinHorizontal(lipgloss.Left, chips...), max(width, 0), "")
}

func (r *Renderer) track(chipEnd, removeW, width int) {
	if removeW == 0 || chipEnd > width {
		return
	}
	startX := chipEnd - removeW
	r.bounds = append(r.bounds, chipBounds{
		startX:    startX,
		removeEnd: startX + removeW - r.styles.Remove.GetHorizontalMargins(),
	})
}

func (r *Renderer) more(n int) string {
	return r.styles.More.Render(fmt.Sprintf("%d more…", n))
}

// HitTestRemove returns index 的 附件 whose remove button
// contains given x coordinate, 或 -1 如果 none.
func (r *Renderer) HitTestRemove(_ []message.Attachment, x int) int {
	for i, b := range r.bounds {
		if x >= b.startX && x < b.removeEnd {
			return i
		}
	}
	return -1
}

func (r *Renderer) icon(a message.Attachment) lipgloss.Style {
	if a.IsImage() {
		return r.styles.Image
	}
	if a.IsMarkdown() {
		return r.styles.Skill
	}
	return r.styles.Text
}
