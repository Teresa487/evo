package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/stretchr/testify/require"
)

// spinTestItem 是 chat item 用 controllable spinner.
type spinTestItem struct {
	testMessageItem
	spinning bool
	advances int
	version  uint64
}

func (s *spinTestItem) Spinning() bool  { return s.spinning }
func (s *spinTestItem) Version() uint64 { return s.version }
func (s *spinTestItem) Advance() bool {
	if !s.spinning {
		return false
	}
	s.advances++
	s.version++
	return true
}

var _ chat.Animatable = (*spinTestItem)(nil)

// neutralMsg 是 更新 那 changes nothing; it exercises 更新 tail.
type neutralMsg struct{}

// newAnimTestUI builds chat UI whose last item spins 和 whose first item
// spins 但 是 far above viewport.
func newAnimTestUI(t *testing.T) (u *UI, top, bottom *spinTestItem) {
	t.Helper()
	u = newFrameTestUI(t)
	items := make([]chat.MessageItem, 0, 200)
	top = &spinTestItem{testMessageItem: testMessageItem{id: "top", text: "top"}, spinning: true}
	items = append(items, top)
	for i := 1; i < 199; i++ {
		items = append(items, &focusableTestItem{testMessageItem: testMessageItem{id: strconv.Itoa(i), text: "line"}})
	}
	bottom = &spinTestItem{testMessageItem: testMessageItem{id: "bottom", text: "bottom\n1\n2\n3\n4\n5"}, spinning: true}
	items = append(items, bottom)
	u.chat.SetMessages(items...)
	u.chat.ScrollToBottom()
	return u, top, bottom
}

func TestAnimClock_StartsFromUpdateTailWhenSpinnerVisible(t *testing.T) {
	t.Parallel()
	u, _, _ := newAnimTestUI(t)
	require.False(t, u.animClock.running)

	_, cmd := u.Update(neutralMsg{})
	require.NotNil(t, cmd, "an update with a visible spinner must arm the clock")
	require.True(t, u.animClock.running)

	_, _ = u.Update(neutralMsg{})
	require.True(t, u.animClock.running, "clock must not be double-armed")
}

func TestAnimClock_TickAdvancesOnlyVisibleSpinners(t *testing.T) {
	t.Parallel()
	u, top, bottom := newAnimTestUI(t)
	_, _ = u.Update(neutralMsg{})

	_, cmd := u.Update(animTickMsg{gen: u.animClock.gen})
	require.NotNil(t, cmd, "tick with a visible spinner must re-arm")
	require.Equal(t, 1, bottom.advances)
	require.Equal(t, 0, top.advances, "off-screen spinner must not advance")
	require.True(t, u.animClock.running)

	_, _ = u.Update(animTickMsg{gen: u.animClock.gen})
	require.Equal(t, 2, bottom.advances)
}

func TestAnimClock_StopsWhenNothingVisibleSpins(t *testing.T) {
	t.Parallel()
	u, _, bottom := newAnimTestUI(t)
	_, _ = u.Update(neutralMsg{})
	require.True(t, u.animClock.running)

	bottom.spinning = false
	_, _ = u.Update(animTickMsg{gen: u.animClock.gen})
	require.False(t, u.animClock.running, "clock must stop when nothing visible is spinning")
	_, _ = u.Update(neutralMsg{})
	require.False(t, u.animClock.running, "clock must stay off while nothing visible is spinning")
}

func TestAnimClock_IdleTickIsScrollOnly(t *testing.T) {
	t.Parallel()
	u, _, bottom := newAnimTestUI(t)
	_, _ = u.Update(neutralMsg{})
	u.View()
	require.Equal(t, 1, u.frames.Len())

	bottom.spinning = false
	_, _ = u.Update(animTickMsg{gen: u.animClock.gen})
	u.View()
	require.Equal(t, 1, u.frames.hits, "a tick that advanced nothing must serve the memoized frame")
}

func TestAnimClock_RestartsWhenScrollRevealsSpinner(t *testing.T) {
	t.Parallel()
	u, top, bottom := newAnimTestUI(t)
	bottom.spinning = false
	_, _ = u.Update(neutralMsg{})
	require.False(t, u.animClock.running, "no visible spinner: clock stays off")

	u.chat.ScrollToTop()
	_, _ = u.Update(neutralMsg{})
	require.True(t, u.animClock.running, "revealing a spinner must arm the clock")
	_, _ = u.Update(animTickMsg{gen: u.animClock.gen})
	require.Equal(t, 1, top.advances)
}

func TestAnimClock_TickOutsideChatStops(t *testing.T) {
	t.Parallel()
	u, _, _ := newAnimTestUI(t)
	_, _ = u.Update(neutralMsg{})
	require.True(t, u.animClock.running)

	u.state = uiLanding
	_, _ = u.Update(animTickMsg{gen: u.animClock.gen})
	require.False(t, u.animClock.running)
}

func TestAnimClock_TickKeepsFollowPinned(t *testing.T) {
	t.Parallel()
	u, _, _ := newAnimTestUI(t)
	_, _ = u.Update(neutralMsg{})
	u.chat.ScrollToBottom()
	require.True(t, u.chat.Follow())
	// Drift up little while (tall) bottom spinner 是 still 在 view,
	// 作为 happens 当 animated item changes height.
	u.chat.ScrollBy(-3)
	u.chat.follow = true
	require.False(t, u.chat.AtBottom())

	_, _ = u.Update(animTickMsg{gen: u.animClock.gen})
	require.True(t, u.chat.AtBottom(), "a frame while following must re-pin to the bottom")
}

func TestAnimClock_ReArmsIfTickWasLost(t *testing.T) {
	t.Parallel()
	u, _, _ := newAnimTestUI(t)
	now := time.Now()
	u.animClock.now = func() time.Time { return now }

	_, cmd := u.Update(neutralMsg{})
	require.NotNil(t, cmd)
	require.True(t, u.animClock.running)

	// Still within grace 窗口: no second clock.
	now = now.Add(animClockLostAfter / 2)
	require.Nil(t, u.ensureAnimating())

	// tick never came back; new clock must 是 armed.
	now = now.Add(animClockLostAfter)
	require.NotNil(t, u.ensureAnimating(), "a lost tick must not freeze spinners forever")
	require.True(t, u.animClock.running)
}

func TestAnimClock_StaleGenerationTickIsIgnored(t *testing.T) {
	t.Parallel()
	u, _, bottom := newAnimTestUI(t)
	now := time.Now()
	u.animClock.now = func() time.Time { return now }

	_, _ = u.Update(neutralMsg{})
	stale := animTickMsg{gen: u.animClock.gen}

	// 事件 loop stalls; watchdog arms replacement clock.
	now = now.Add(2 * animClockLostAfter)
	_, _ = u.Update(neutralMsg{})
	current := u.animClock.gen
	require.NotEqual(t, stale.gen, current)

	// delayed tick 从 old clock arrives: it must neither
	// advance nor re-arm, leaving exactly one live clock.
	_, _ = u.Update(stale)
	require.Equal(t, 0, bottom.advances, "stale tick must not advance")
	require.True(t, u.animClock.running)
	require.Equal(t, current, u.animClock.gen, "stale tick must not arm another clock")

	_, _ = u.Update(animTickMsg{gen: current})
	require.Equal(t, 1, bottom.advances)
	require.Equal(t, current+1, u.animClock.gen, "current tick re-arms exactly once")
}

func TestAnimClock_TickWithoutFollowLeavesScrollAlone(t *testing.T) {
	t.Parallel()
	u, _, _ := newAnimTestUI(t)
	_, _ = u.Update(neutralMsg{})
	u.chat.ScrollBy(-2)
	require.False(t, u.chat.Follow())
	before := u.chat.Offset()

	_, _ = u.Update(animTickMsg{gen: u.animClock.gen})
	require.Equal(t, before, u.chat.Offset())
}

// throttledTestItem changes 它的 output 只 every other frame, like
// spinner whose rendered form 是 stable across some frames.
type throttledTestItem struct {
	spinTestItem
	frames int
}

func (l *throttledTestItem) Advance() bool {
	l.frames++
	if l.frames%2 != 0 {
		return false
	}
	l.version++
	return true
}

func TestAnimClock_UnchangedFrameIsScrollOnlyButKeepsClock(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	item := &throttledTestItem{spinTestItem: spinTestItem{testMessageItem: testMessageItem{id: "th", text: "th"}, spinning: true}}
	u.chat.SetMessages(item)
	_, _ = u.Update(neutralMsg{})
	u.View()
	require.True(t, u.animClock.running)

	_, _ = u.Update(animTickMsg{gen: u.animClock.gen})
	require.True(t, u.animClock.running, "a spinning item that did not change this frame must keep the clock running")
	u.View()
	require.Equal(t, 1, u.frames.hits, "an unchanged frame must serve the memoized frame")

	_, _ = u.Update(animTickMsg{gen: u.animClock.gen})
	u.View()
	require.Equal(t, 1, u.frames.hits, "a changed frame must redraw")
}
