package dialog

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// InlineEditor 是 interface 为 components 那 replace
// textarea 在 editor area. UI 模型 holds single
// InlineEditor 字段 和 routes keys, rendering, layout, 和 help
// through it without knowing the concrete type.
type InlineEditor interface {
	// HandleKey processes key 事件. Returns true 当 用户
	// has finished interacting (answer submitted 或 dismissed),
	// plus an optional tea.Cmd.
	HandleKey(msg tea.KeyPressMsg) (done bool, cmd tea.Cmd)

	// ShortHelp returns key bindings 为 status bar.
	ShortHelp() []key.Binding

	// Height returns number 的 content lines 为 layout 在
	// given content width. 它 must 是 pure function 的 width 所以
	// layout stays 在 sync 用 Draw during resize.
	Height(width int) int

	// Draw 渲染 component onto screen within given
	// area. Returns cursor position relative 到 area's
	// top-left, 或 nil 如果 no cursor should 是 shown.
	Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor

	// HeightChanged reports whether the height changed since the
	// last 调用, indicating UI should recalculate layout.
	HeightChanged() bool

	// SetFocused tells component whether editor area 是
	// focused.
	SetFocused(focused bool)
}

// CollapseInlineMsg asks UI 到 move focus away 从 活跃 inline
// editor without dismissing it. editor remains available 为 restoration.
type CollapseInlineMsg struct{}

// CollapsibleInlineEditor 是 implemented 通过 inline editors 那 provide
// compact representation while the chat has focus.
type CollapsibleInlineEditor interface {
	InlineEditor

	// ShouldCollapse reports whether compact representation should 是 used
	// 为 given editor width 和 终态 height while editor 是 blurred.
	ShouldCollapse(width, terminalHeight int) bool

	// CollapsedHeight returns the compact representation's content height.
	CollapsedHeight() int

	// DrawCollapsed 渲染 compact representation.
	DrawCollapsed(scr uv.Screen, area uv.Rectangle)

	// CollapsedHelp returns help description 为 returning focus 到
	// editor, such 作为 "review plan" 或 "answer questions".
	CollapsedHelp() string
}

// ResizableInlineEditor 是 implemented 通过 inline editors whose internal
// layout depends 在 width allocated 通过 UI.
type ResizableInlineEditor interface {
	InlineEditor

	// SetWidth updates the editor's available content width.
	SetWidth(width int)
}

// CmdOnDone 是 optional interface 为 inline editors 那 need 到
// 运行 tea.Cmd 当 dismissed via mouse. UI checks 为 这 after
// HandleMouseClick returns done=true 和 queues returned cmd.
type CmdOnDone interface {
	PendingCmd() tea.Cmd
}

// MouseClickableEditor 是 optional interface 为 inline editors
// 那 handle mouse clicks 和 hover highlighting. UI
// type-asserts 为 这 before routing click 和 motion events.
type MouseClickableEditor interface {
	InlineEditor
	// HandleMouseClick processes mouse click 在 given screen
	// coordinates. Returns done=true 当 editor has completed
	// (answer submitted 或 dismissed), 和 handled=true 如果 click
	// was consumed (even 如果 不 done).
	HandleMouseClick(x, y int) (done bool, handled bool)
	// SetHover updates current mouse position 为 hover
	// highlighting. Called 在 every MouseMotionMsg while
	// editor 是 活跃.
	SetHover(x, y int)
}

// MouseSelectableEditor 是 implemented 通过 inline editors 那 support mouse
// drag selection within their own content.
type MouseSelectableEditor interface {
	InlineEditor
	// HandleMouseDown begins selection 当 press lands 在 selectable
	// content.
	HandleMouseDown(x, y int) bool
	// HandleMouseDrag updates 活跃 selection.
	HandleMouseDrag(x, y int) bool
	// HandleMouseRelease finishes 活跃 selection 和 optionally returns
	// command 那 copies it.
	HandleMouseRelease(x, y int) (handled bool, cmd tea.Cmd)
}

// PasteableEditor 是 optional interface 为 inline editors
// 那 contain 文本 areas 和 can receive paste events. UI
// type-asserts 为 这 before routing tea.PasteMsg.
type PasteableEditor interface {
	// HandlePaste processes paste 消息. Returns optional
	// tea.Cmd 为 side effects (e.g., focus commands).
	HandlePaste(msg tea.PasteMsg) tea.Cmd
}
