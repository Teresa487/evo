package model

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/anim"
)

// animTickMsg 是 one frame 的 shared animation clock. gen 是 clock
// generation tick was armed 为.
type animTickMsg struct{ gen uint64 }

// animClockLostAfter 是 how long armed clock may go without 它的 tick
// arriving before ensure assumes command was lost 和 arms new one.
// Generous enough 那 slow frame never trips it.
const animClockLostAfter = 2 * time.Second

// animClock drives every spinner 在 UI, 在 transcript 或 chrome.
// There 是 在 most one tick outstanding: tick carrying older generation
// belongs 到 superseded clock 和 是 ignored, 所以 delayed tick can never
// 运行 alongside 它的 replacement. zero 值 是 stopped, unfrozen clock.
type animClock struct {
	// frozen keeps ghost spinners still: reloaded 会话 whose agent 是
	// 空闲 may hold 助手消息 那 never got Finish 片段.
	frozen bool

	running bool
	gen     uint64
	armedAt time.Time
	// now 是 injectable clock 为 tests (nil == real time).
	now func() time.Time
}

// ensure arms clock 如果 no tick 是 outstanding 和 wanted reports
// something 到 animate. wanted 是 只 consulted 当 tick could 是 armed,
// 所以 per-更新 cost 的 already running clock stays constant. 它 是
// 只 place tick 是 armed.
func (c *animClock) ensure(wanted func() bool) tea.Cmd {
	if c.frozen {
		c.running = false
		return nil
	}
	if c.running && c.clock().Sub(c.armedAt) < animClockLostAfter {
		return nil
	}
	if !wanted() {
		c.running = false
		return nil
	}
	c.running = true
	c.armedAt = c.clock()
	c.gen++
	gen := c.gen
	return tea.Tick(anim.FrameInterval(), func(time.Time) tea.Msg {
		return animTickMsg{gen: gen}
	})
}

// 受理 consumes tick, reporting whether it belongs 到 live clock.
// Either way tick 是 spent, 所以 clock stays stopped until ensure.
func (c *animClock) accept(msg animTickMsg) bool {
	if msg.gen != c.gen {
		return false
	}
	c.running = false
	return true
}

func (c *animClock) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
