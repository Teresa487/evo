package model

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/stretchr/testify/require"
)

// viewChecked renders through 缓存, then renders same state again
// 用 memoization disabled 和 requires two 到 agree. Probing 用
// frames == nil neither resets nor stores, 所以 it does 不 perturb 缓存.
func viewChecked(t *testing.T, u *UI, what string) tea.View {
	t.Helper()
	got := u.View()
	saved := u.frames
	u.frames = nil
	want := u.View()
	u.frames = saved
	require.Equal(t, want.Content, got.Content, "stale memoized frame after %s", what)
	return got
}

// 一个 diagonal trackpad scroll over selected shell block changes block's
// horizontal offset. Vertical scroll clamped 在 bottom leaves scroll
// position unchanged, 所以 frame must 是 invalidated 通过 item's version
// rather than 通过 position.
func TestDiagonalWheelOverShellItem(t *testing.T) {
	u := newFrameTestUI(t)
	wide := strings.Repeat("wide output column ", 30)
	u.chat.SetMessages(chat.NewShellItem(u.com.Styles, "ls", wide+"\n"+wide, 0))
	u.updateLayoutAndSize()
	u.chat.ScrollToBottom()
	u.chat.SetSelected(0)
	require.True(t, u.chat.IsSelectedShellItem())
	viewChecked(t, u, "initial render")

	u.Update(common.CoalescedWheelMsg{
		Mouse:  tea.Mouse{X: u.layout.main.Min.X + 1, Y: u.layout.main.Min.Y + 1},
		DeltaX: 8, // pan the shell block right
		DeltaY: 1, // clamped: already at the bottom
	})
	viewChecked(t, u, "diagonal wheel over a shell block")
}

// scrollbar's visibility flag 在 ScrollbarDefault mode 是 不 片段 的
// frameKey, 所以 clamped scroll 那 reveals scrollbar hits 缓存.
func TestScrollbarDefault_ClampedScrollRevealsScrollbar(t *testing.T) {
	u := newFrameTestUI(t)
	u.chat.scrollbarMode = config.ScrollbarDefault
	u.chat.scrollbarVisible = false
	u.chat.ScrollToBottom()
	viewChecked(t, u, "initial render")

	// Wheel down 在 bottom: clamped, 所以 offsets do 不 move, 但
	// ScrollBy still calls showScrollbar 和 sets scrollbarVisible.
	u.beginFrameUpdate()
	u.markScrollOnly()
	u.chat.ScrollBy(5)
	u.endFrameUpdate()
	require.True(t, u.chat.scrollbarVisible, "scroll must reveal the scrollbar")
	viewChecked(t, u, "clamped scroll that reveals the scrollbar")
}

// op 是 one 用户 action 那 PR treats 作为 (或 routes through)
// scroll-只 更新.
type op struct {
	name string
	run  func(u *UI) tea.Cmd
}

func wheel(u *UI, dx, dy float64) tea.Cmd {
	_, cmd := u.Update(common.CoalescedWheelMsg{
		Mouse:  tea.Mouse{X: u.layout.main.Min.X + 1, Y: u.layout.main.Min.Y + 1},
		DeltaX: dx, DeltaY: dy,
	})
	return cmd
}

func keyOp(k rune) func(*UI) tea.Cmd {
	return func(u *UI) tea.Cmd {
		_, cmd := u.Update(tea.KeyPressMsg{Code: k})
		return cmd
	}
}

var frameOps = []op{
	{"wheel down", func(u *UI) tea.Cmd { return wheel(u, 0, 1) }},
	{"wheel up", func(u *UI) tea.Cmd { return wheel(u, 0, -1) }},
	{"wheel down x5", func(u *UI) tea.Cmd { return wheel(u, 0, 5) }},
	{"wheel up x5", func(u *UI) tea.Cmd { return wheel(u, 0, -5) }},
	{"wheel diagonal", func(u *UI) tea.Cmd { return wheel(u, 4, 1) }},
	{"wheel diagonal back", func(u *UI) tea.Cmd { return wheel(u, -4, -1) }},
	{"key up", keyOp(tea.KeyUp)},
	{"key down", keyOp(tea.KeyDown)},
	{"pgup", keyOp(tea.KeyPgUp)},
	{"pgdown", keyOp(tea.KeyPgDown)},
	{"frame gc", func(u *UI) tea.Cmd { _, cmd := u.Update(frameGCMsg{}); return cmd }},
	{"scrollbar hide timer", func(u *UI) tea.Cmd {
		_, cmd := u.Update(scrollbarHideMsg{seq: u.chat.scrollbarHideSeq})
		return cmd
	}},
}

func runFrameSweep(t *testing.T, u *UI, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	u.chat.ScrollToBottom()
	viewChecked(t, u, "initial render")
	before := u.frames.putSkips
	for i := range 200 {
		o := frameOps[rng.Intn(len(frameOps))]
		o.run(u)
		viewChecked(t, u, fmt.Sprintf("step %d: %s", i, o.name))
	}
	t.Logf("frames dropped because state moved during Draw: %d", u.frames.putSkips-before)
}

func TestFrameSweep_ScrollbarAlways(t *testing.T) {
	u := newFrameTestUI(t)
	u.chat.Focus()
	runFrameSweep(t, u, 1)
}

func TestFrameSweep_ScrollbarDefault(t *testing.T) {
	u := newFrameTestUI(t)
	u.chat.Focus()
	u.chat.scrollbarMode = config.ScrollbarDefault
	runFrameSweep(t, u, 2)
}

func TestFrameSweep_WithShellItems(t *testing.T) {
	u := newFrameTestUI(t)
	u.chat.Focus()
	wide := strings.Repeat("wide output column ", 20)
	items := frameTestItems("line")
	items[3] = chat.NewShellItem(u.com.Styles, "ls -la", wide+"\n"+wide+"\n"+wide, 0)
	items[40] = chat.NewShellItem(u.com.Styles, "cat big.txt", wide+"\n"+wide, 0)
	u.chat.SetMessages(items...)
	u.updateLayoutAndSize()
	u.chat.ScrollToTop()
	u.chat.SetSelected(3)
	runFrameSweep(t, u, 3)
}
