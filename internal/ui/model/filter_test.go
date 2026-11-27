package model

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/stretchr/testify/require"
)

func newTestFilter(t *testing.T) (*Filter, *time.Time) {
	t.Helper()
	now := time.Now()
	f := &Filter{now: func() time.Time { return now }}
	return f, &now
}

func wheelUp() tea.MouseWheelMsg {
	return tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelUp})
}

func wheelDown() tea.MouseWheelMsg {
	return tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelDown})
}

func TestFilter_SingleEventPassthrough(t *testing.T) {
	t.Parallel()
	f, _ := newTestFilter(t)

	result := f.Filter(nil, wheelDown())
	msg, ok := result.(common.CoalescedWheelMsg)
	require.True(t, ok, "expected CoalescedWheelMsg")
	require.Equal(t, float64(0), msg.DeltaX)
	require.Equal(t, float64(1), msg.DeltaY)
}

func TestFilter_CoalescesSameDirection(t *testing.T) {
	t.Parallel()
	f, now := newTestFilter(t)

	// First 事件 passes through.
	result := f.Filter(nil, wheelDown())
	require.IsType(t, common.CoalescedWheelMsg{}, result)

	// Second 事件 within 窗口 gets dropped.
	*now = now.Add(5 * time.Millisecond)
	result = f.Filter(nil, wheelDown())
	require.Nil(t, result)

	// Third 事件 within 窗口 gets dropped.
	*now = now.Add(5 * time.Millisecond)
	result = f.Filter(nil, wheelDown())
	require.Nil(t, result)

	// After 窗口 expires, accumulated delta 是 emitted.
	*now = now.Add(20 * time.Millisecond)
	result = f.Filter(nil, wheelDown())
	msg, ok := result.(common.CoalescedWheelMsg)
	require.True(t, ok, "expected CoalescedWheelMsg after window")
	require.Equal(t, float64(3), msg.DeltaY, "should accumulate 3 down events")
}

func TestFilter_DirectionChangeWithinWindowAccumulatesNewDirection(t *testing.T) {
	t.Parallel()
	f, now := newTestFilter(t)

	// Scroll down (passes through immediately).
	f.Filter(nil, wheelDown())

	// Immediately scroll up (within 窗口). Accumulates 但 suppressed.
	*now = now.Add(5 * time.Millisecond)
	result := f.Filter(nil, wheelUp())
	require.Nil(t, result, "within time window, should be suppressed")

	// After 窗口, up delta includes suppressed 事件 plus 这 one.
	*now = now.Add(20 * time.Millisecond)
	result = f.Filter(nil, wheelUp())
	msg, ok := result.(common.CoalescedWheelMsg)
	require.True(t, ok)
	require.Equal(t, float64(-2), msg.DeltaY, "should have 2 accumulated up events")
}

func TestFilter_DirectionChangeDropsStaleAccumulator(t *testing.T) {
	t.Parallel()
	f, now := newTestFilter(t)

	// Scroll up
	f.Filter(nil, wheelUp())

	// Scroll up again
	*now = now.Add(5 * time.Millisecond)
	result := f.Filter(nil, wheelUp())
	require.Nil(t, result)

	// Scroll down (accumulation 应当 reset)
	*now = now.Add(5 * time.Millisecond)
	result = f.Filter(nil, wheelDown())
	require.Nil(t, result)

	// And scroll down again
	*now = now.Add(20 * time.Millisecond)
	result = f.Filter(nil, wheelDown())
	msg, ok := result.(common.CoalescedWheelMsg)
	require.True(t, ok)
	require.Equal(t, float64(2), msg.DeltaY, "should drop stale opposite-direction events")
}

func TestFilter_WindowExpiryEmitsAccumulated(t *testing.T) {
	t.Parallel()
	f, now := newTestFilter(t)

	// First 事件 passes through immediately.
	result := f.Filter(nil, wheelUp())
	require.IsType(t, common.CoalescedWheelMsg{}, result)

	// Two more events within 窗口 get accumulated.
	*now = now.Add(5 * time.Millisecond)
	f.Filter(nil, wheelUp())
	*now = now.Add(5 * time.Millisecond)
	f.Filter(nil, wheelUp())

	// Wait 为 窗口 到 expire.
	*now = now.Add(20 * time.Millisecond)

	// Next 事件 triggers emission 的 accumulated delta (2 suppressed + 1 new).
	result = f.Filter(nil, wheelUp())
	msg, ok := result.(common.CoalescedWheelMsg)
	require.True(t, ok)
	require.Equal(t, float64(-3), msg.DeltaY, "should include accumulated events plus triggering event")
}

func TestFilter_MotionThrottledIndependently(t *testing.T) {
	t.Parallel()
	f, now := newTestFilter(t)

	motion := tea.MouseMotionMsg(tea.Mouse{X: 1, Y: 1})

	// First motion passes.
	result := f.Filter(nil, motion)
	require.NotNil(t, result)

	// Motion within 窗口 是 dropped.
	*now = now.Add(5 * time.Millisecond)
	result = f.Filter(nil, motion)
	require.Nil(t, result)

	// Wheel 事件 是 不 affected 通过 motion throttle.
	*now = now.Add(5 * time.Millisecond)
	result = f.Filter(nil, wheelDown())
	require.NotNil(t, result, "wheel should not be throttled by motion")
}

func TestFilter_NonMouseMessagesPassThrough(t *testing.T) {
	t.Parallel()
	f, _ := newTestFilter(t)

	type customMsg struct{}
	msg := customMsg{}
	result := f.Filter(nil, msg)
	require.Equal(t, msg, result, "non-mouse messages should pass through unchanged")
}
