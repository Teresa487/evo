package list

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Item represents single item 在 lazy-loaded list.
//
// Items participate 在 list-level render memo (F6). 缓存 key
// 为 each item 是 (指针, width, version), 所以 items must bump their
// version (via embedded *Versioned helper) 在 every mutation 那
// changes the rendered output.
type Item interface {
	// Render returns string representation 的 item 为 given
	// width.
	Render(width int) string

	// Version returns monotonic 计数器 那 list-level 缓存
	// uses 到 detect mutations. Items must increment version
	// (via Versioned.Bump) 在 every state change 那 would alter
	// the rendered output.
	Version() uint64
}

// Versioned 是 tiny embeddable helper 那 satisfies Item.Version()
// 和 provides Bump() method 到 调用 从 every state-mutating
// 方法. Items typically embed *Versioned alongside their other
// helpers; see chat.AssistantMessageItem 为 canonical wiring.
//
// Bump() 是 不 safe 为 concurrent use; callers must hold whatever
// synchronization their item type already requires 为 state
// mutations. list itself never reads Version() 从 goroutine
// other than the UI thread.
type Versioned struct {
	v uint64
}

// NewVersioned returns fresh *Versioned 在 version zero.
func NewVersioned() *Versioned {
	return &Versioned{}
}

// Version returns current version 计数器.
func (vc *Versioned) Version() uint64 {
	return vc.v
}

// Bump advances version 计数器 通过 one. Mutators 在 items 那
// affect rendered output must 调用 Bump exactly once per
// observable state change. Bumping more than once per change 是
// harmless other than single extra 缓存 miss.
func (vc *Versioned) Bump() {
	vc.v++
}

// RawRenderable represents item 那 can provide raw rendering
// without additional styling.
type RawRenderable interface {
	// RawRender returns the raw rendered string without any additional
	// styling.
	RawRender(width int) string
}

// Focusable represents item 那 can 是 aware 的 focus state changes.
type Focusable interface {
	// SetFocused sets focus state 的 item.
	SetFocused(focused bool)
}

// Highlightable represents item 那 can highlight portion 的 它的 content.
type Highlightable interface {
	// SetHighlight highlights content 从 given start 到 end
	// positions. Use -1 为 no highlight.
	SetHighlight(startLine, startCol, endLine, endCol int)
	// Highlight returns the current highlight positions within the item.
	Highlight() (startLine, startCol, endLine, endCol int)
}

// MouseClickable represents item 那 can handle mouse click events.
type MouseClickable interface {
	// HandleMouseClick processes mouse click 事件 在 given coordinates.
	// 它 returns true 如果 事件 was handled, false otherwise.
	HandleMouseClick(btn ansi.MouseButton, x, y int) bool
}

// SpacerItem 是 spacer item 那 adds vertical space 在 list.
type SpacerItem struct {
	*Versioned
	Height int
}

// NewSpacerItem creates new [SpacerItem] 用 specified height.
func NewSpacerItem(height int) *SpacerItem {
	return &SpacerItem{
		Versioned: NewVersioned(),
		Height:    max(0, height-1),
	}
}

// Render implements Item interface 为 [SpacerItem].
func (s *SpacerItem) Render(width int) string {
	return strings.Repeat("\n", s.Height)
}
