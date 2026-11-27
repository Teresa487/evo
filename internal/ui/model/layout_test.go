package model

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
)

// testMessageItem 是 minimal chat item used 到 populate chat list
// without pulling 在 full 消息 rendering machinery.
type testMessageItem struct {
	id   string
	text string
}

func (m testMessageItem) ID() string           { return m.id }
func (m testMessageItem) Render(int) string    { return m.text }
func (m testMessageItem) RawRender(int) string { return m.text }
func (m testMessageItem) Version() uint64      { return 0 }

var _ chat.MessageItem = testMessageItem{}

// mutableMessageItem 是 测试 消息 item whose rendered height can grow
// over time, simulating streaming content 那 increases item's height.
type mutableMessageItem struct {
	id      string
	lines   int
	version uint64
}

func (m *mutableMessageItem) ID() string { return m.id }
func (m *mutableMessageItem) Render(width int) string {
	lines := make([]string, m.lines)
	for i := range lines {
		lines[i] = "line"
	}
	return strings.Join(lines, "\n")
}
func (m *mutableMessageItem) RawRender(width int) string { return m.Render(width) }
func (m *mutableMessageItem) Version() uint64            { return m.version }

var _ chat.MessageItem = (*mutableMessageItem)(nil)

// newTestUI builds focused uiChat 模型 用 dynamic textarea sizing enabled.
// 它 intentionally keeps dependencies minimal 所以 layout behavior can 是 tested
// 在 isolation.
func newTestUI() *UI {
	com := common.DefaultCommon(nil)

	ta := textarea.New()
	ta.SetStyles(com.Styles.Editor.Textarea)
	ta.ShowLineNumbers = false
	ta.CharLimit = -1
	ta.SetVirtualCursor(false)
	ta.DynamicHeight = true
	ta.MinHeight = TextareaMinHeight
	ta.MaxHeight = TextareaMaxHeight
	ta.Focus()

	u := &UI{
		com:      com,
		status:   NewStatus(com, nil),
		chat:     NewChat(com, config.ScrollbarDefault),
		textarea: ta,
		state:    uiChat,
		focus:    uiFocusEditor,
		width:    140,
		height:   45,
	}

	return u
}

func TestGenerateLayout_HelpAndSidebarDoNotOverlap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fullHelp bool
	}{
		{name: "short help", fullHelp: false},
		{name: "full help", fullHelp: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u := newTestUI()
			u.com.Workspace = &testWorkspace{}
			u.attachments = attachments.New(nil, attachments.Keymap{})
			if tt.fullHelp {
				u.status.ToggleHelp()
			}
			l := u.generateLayout(120, 40)
			if l.status.Max.X > l.sidebar.Min.X {
				t.Fatalf("help ends at column %d, past the sidebar at %d", l.status.Max.X, l.sidebar.Min.X)
			}
			if l.sidebar.Max.Y != l.status.Max.Y-1 {
				t.Fatalf("sidebar ends at row %d, want it to reach the last help row %d", l.sidebar.Max.Y, l.status.Max.Y-2)
			}
		})
	}
}

func TestUpdateLayoutAndSize_EditorGrowthShrinksChat(t *testing.T) {
	t.Parallel()

	// Baseline layout 在 min textarea height.
	u := newTestUI()
	u.updateLayoutAndSize()

	initialEditorHeight := u.layout.editor.Dy()
	initialChatHeight := u.layout.main.Dy()

	// Increase textarea content enough 到 trigger growth, then 运行
	// same resize 钩子 used 在 real 更新 路径.
	prevHeight := u.textarea.Height()
	u.textarea.SetValue(strings.Repeat("line\n", 8))
	u.textarea.MoveToEnd()
	_ = u.handleTextareaHeightChange(prevHeight)

	if got := u.layout.editor.Dy(); got <= initialEditorHeight {
		t.Fatalf("expected editor to grow: got %d, want > %d", got, initialEditorHeight)
	}

	if got := u.layout.main.Dy(); got >= initialChatHeight {
		t.Fatalf("expected chat to shrink: got %d, want < %d", got, initialChatHeight)
	}
}

func TestHandleTextareaHeightChange_FollowModeStaysAtBottom(t *testing.T) {
	t.Parallel()

	// Use enough messages 到 make chat scrollable 所以 AtBottom/Follow
	// assertions 是 meaningful.
	u := newTestUI()

	msgs := make([]chat.MessageItem, 0, 60)
	for i := range 60 {
		msgs = append(msgs, testMessageItem{
			id:   "m-" + strconv.Itoa(i),
			text: "message " + strconv.Itoa(i),
		})
	}
	u.chat.SetMessages(msgs...)
	u.updateLayoutAndSize()

	// Enter follow mode 和 verify we're anchored 在 bottom first.
	u.chat.ScrollToBottom()
	if !u.chat.AtBottom() {
		t.Fatal("expected chat to start at bottom")
	}

	// Grow editor; follow mode should keep chat pinned 到 end
	// even 作为 chat viewport shrinks.
	prevHeight := u.textarea.Height()
	u.textarea.SetValue(strings.Repeat("line\n", 10))
	u.textarea.MoveToEnd()
	_ = u.handleTextareaHeightChange(prevHeight)

	if !u.chat.Follow() {
		t.Fatal("expected follow mode to remain enabled")
	}
	if !u.chat.AtBottom() {
		t.Fatal("expected chat to remain at bottom after editor resize in follow mode")
	}
}

func TestScrollByDown_EnablesFollowAtBottom(t *testing.T) {
	t.Parallel()

	// Use enough messages 到 make chat scrollable 所以 AtBottom/Follow
	// assertions 是 meaningful.
	u := newTestUI()

	msgs := make([]chat.MessageItem, 0, 60)
	for i := range 60 {
		msgs = append(msgs, testMessageItem{
			id:   "m-" + strconv.Itoa(i),
			text: "message " + strconv.Itoa(i),
		})
	}
	u.chat.SetMessages(msgs...)
	u.updateLayoutAndSize()

	// Start 在 top 用 follow disabled, simulating 用户 那
	// scrolled up 到 读取 earlier messages.
	u.chat.ScrollToTop()
	if u.chat.Follow() {
		t.Fatal("expected follow mode to be disabled after scrolling to top")
	}

	// Scroll down 在 small increments (like mouse wheel ticks) until we
	// reach bottom. Follow mode should re-enable once bottom 是
	// reached 所以 view sticks 到 new content.
	for range 200 {
		if u.chat.AtBottom() {
			break
		}
		u.chat.ScrollBy(3)
	}

	if !u.chat.AtBottom() {
		t.Fatal("expected chat to be at bottom after scrolling down")
	}
	if !u.chat.Follow() {
		t.Fatal("expected follow mode to be enabled after scrolling to bottom")
	}
}

func TestFollowStaysAtBottomWhenContentGrows(t *testing.T) {
	t.Parallel()

	u := newTestUI()

	// 创建 enough static messages 到 make chat scrollable, plus one
	// mutable item 在 end 那 will grow (simulating streaming).
	msgs := make([]chat.MessageItem, 0, 60)
	for i := range 59 {
		msgs = append(msgs, testMessageItem{
			id:   "m-" + strconv.Itoa(i),
			text: "message " + strconv.Itoa(i),
		})
	}
	streaming := &mutableMessageItem{id: "streaming", lines: 1, version: 1}
	msgs = append(msgs, streaming)
	u.chat.SetMessages(msgs...)
	u.updateLayoutAndSize()

	// Start 在 bottom 在 follow mode.
	u.chat.ScrollToBottom()
	if !u.chat.AtBottom() {
		t.Fatal("expected chat to start at bottom")
	}

	// Simulate streaming: grow the last item's height.
	streaming.lines = 20
	streaming.version++

	// Trigger draw (which renders list 和 updates cached heights).
	// follow re-anchor 在 Draw should keep us pinned 到 bottom.
	scr := uv.NewScreenBuffer(u.width, u.height)
	u.chat.Draw(scr, u.layout.main)

	if !u.chat.AtBottom() {
		t.Fatal("expected chat to remain at bottom after streaming content grew while following")
	}
}

func TestAutoExpandPillsIfReasonable(t *testing.T) {
	t.Parallel()

	t.Run("expands when terminal is tall enough and todos exist", func(t *testing.T) {
		t.Parallel()

		u := newTestUI()
		u.height = 50
		u.session = &session.Session{ID: "s1", Todos: []session.Todo{
			{Status: session.TodoStatusInProgress, Content: "do work"},
			{Status: session.TodoStatusPending, Content: "do more"},
		}}

		u.autoExpandPillsIfReasonable()

		if !u.pillsExpanded {
			t.Fatal("expected pillsExpanded to be true")
		}
		if u.focusedPillSection != pillSectionTodos {
			t.Fatalf("expected focusedPillSection to be pillSectionTodos, got %d", u.focusedPillSection)
		}
	})

	t.Run("does not expand when terminal is too short", func(t *testing.T) {
		t.Parallel()

		u := newTestUI()
		u.height = 30
		u.session = &session.Session{ID: "s1", Todos: []session.Todo{
			{Status: session.TodoStatusInProgress, Content: "do work"},
		}}

		u.autoExpandPillsIfReasonable()

		if u.pillsExpanded {
			t.Fatal("expected pillsExpanded to be false when terminal height is below threshold")
		}
	})

	t.Run("does not expand when all todos are completed", func(t *testing.T) {
		t.Parallel()

		u := newTestUI()
		u.height = 50
		u.session = &session.Session{ID: "s1", Todos: []session.Todo{
			{Status: session.TodoStatusCompleted, Content: "done"},
		}}

		u.autoExpandPillsIfReasonable()

		if u.pillsExpanded {
			t.Fatal("expected pillsExpanded to be false when all todos are completed")
		}
	})

	t.Run("does not expand when already expanded", func(t *testing.T) {
		t.Parallel()

		u := newTestUI()
		u.height = 50
		u.pillsExpanded = true
		u.session = &session.Session{ID: "s1", Todos: []session.Todo{
			{Status: session.TodoStatusInProgress, Content: "do work"},
		}}
		u.updateLayoutAndSize()

		u.autoExpandPillsIfReasonable()

		if !u.pillsExpanded {
			t.Fatal("expected pillsExpanded to stay true")
		}
	})

	t.Run("expands for prompt queue when no todos", func(t *testing.T) {
		t.Parallel()

		u := newTestUI()
		u.height = 50
		u.session = &session.Session{ID: "s1", Todos: []session.Todo{}}
		u.promptQueue.SetFetched([]string{"a", "b"})

		u.autoExpandPillsIfReasonable()

		if !u.pillsExpanded {
			t.Fatal("expected pillsExpanded to be true for prompt queue")
		}
		if u.focusedPillSection != pillSectionQueue {
			t.Fatalf("expected focusedPillSection to be pillSectionQueue, got %d", u.focusedPillSection)
		}
	})

	t.Run("does not expand when no session", func(t *testing.T) {
		t.Parallel()

		u := newTestUI()
		u.height = 50
		u.session = nil

		u.autoExpandPillsIfReasonable()

		if u.pillsExpanded {
			t.Fatal("expected pillsExpanded to be false when there is no session")
		}
	})
}
