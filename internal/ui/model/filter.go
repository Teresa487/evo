package model

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
)

// inputFilterInterval targets roughly one filtered sample per 60 Hz frame.
// 这 keeps continuous mouse input responsive while bounding how many
// 终态 escape samples can 队列 ahead 的 later key presses.
const inputFilterInterval = 16 * time.Millisecond

// Filter coalesces high-frequency 终态 mouse input before it reaches
// Bubble Tea 更新 loop. 它 是 safe 为 use 用 tea.WithFilter.
type Filter struct {
	now func() time.Time

	lastMotion time.Time
	lastWheel  time.Time
	wheelDX    float64
	wheelDY    float64
}

// NewFilter returns Bubble Tea input filter 那 coalesces high-frequency
// 终态 mouse input before it reaches 更新 队列.
func NewFilter() *Filter {
	return &Filter{now: time.Now}
}

// Filter 是 passed 到 tea.WithFilter 和 runs before Bubble Tea dispatches
// 消息 到 模型. Returning nil drops noisy samples 在 input
// boundary, which 是 early enough 到 keep later key presses 从 waiting
// behind mouse flood 在 更新 队列.
func (f *Filter) Filter(_ tea.Model, msg tea.Msg) tea.Msg {
	switch typed := msg.(type) {
	case tea.MouseWheelMsg:
		mouse := typed.Mouse()
		dx, dy := wheelDeltas(mouse)
		if oppositeSign(f.wheelDX, dx) {
			f.wheelDX = 0
		}
		if oppositeSign(f.wheelDY, dy) {
			f.wheelDY = 0
		}
		f.wheelDX += dx
		f.wheelDY += dy
		if !f.allow(&f.lastWheel) {
			return nil
		}
		aggregated := common.CoalescedWheelMsg{
			Mouse:  mouse,
			DeltaX: f.wheelDX,
			DeltaY: f.wheelDY,
		}
		f.wheelDX = 0
		f.wheelDY = 0
		return aggregated

	case tea.MouseMotionMsg:
		if !f.allow(&f.lastMotion) {
			return nil
		}
		return msg
	}

	return msg
}

// allow reports whether enough time has elapsed 为 next sample 在 one
// coalescing 流. Each 流 owns separate last timestamp 所以 wheel 和
// motion events can 是 throttled independently.
func (f *Filter) allow(last *time.Time) bool {
	now := f.now
	if now == nil {
		now = time.Now
	}
	at := now()
	if !last.IsZero() && at.Sub(*last) < inputFilterInterval {
		return false
	}
	*last = at
	return true
}

// wheelDeltas converts Bubble Tea's wheel button direction into signed scroll
// deltas. Bubble Tea represents wheel movement 作为 button presses, 所以 we
// synthesize unit delta 为 each sample 和 let Filter aggregate units
// 当 events 是 coalesced.
func wheelDeltas(mouse tea.Mouse) (float64, float64) {
	switch mouse.Button {
	case tea.MouseWheelUp:
		return 0, -1
	case tea.MouseWheelDown:
		return 0, 1
	case tea.MouseWheelLeft:
		return -1, 0
	case tea.MouseWheelRight:
		return 1, 0
	default:
		return 0, 0
	}
}

func oppositeSign(a, b float64) bool {
	return (a < 0 && b > 0) || (a > 0 && b < 0)
}
