package common

import (
	tea "charm.land/bubbletea/v2"
)

// 模型 represents common interface 为 UI components.
type Model[T any] interface {
	Update(msg tea.Msg) (T, tea.Cmd)
	View() string
}

// CoalescedWheelMsg 是 emitted 通过 input filter after collapsing many
// 终态 wheel samples into one 消息. DeltaX 和 DeltaY carry summed
// scroll magnitude 所以 handlers can scroll proportionally.
type CoalescedWheelMsg struct {
	Mouse  tea.Mouse
	DeltaX float64
	DeltaY float64
}

// WheelScrollable 是 optional interface 为 components 那
// support mouse wheel scrolling. UI type-asserts 为 这
// before routing CoalescedWheelMsg.
type WheelScrollable interface {
	// HandleWheel processes coalesced wheel 事件. DeltaY scrolls
	// vertically, DeltaX scrolls horizontally.
	HandleWheel(deltaX, deltaY float64)
}
