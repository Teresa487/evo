package list

// FocusedRenderCallback 是 helper function 那 returns render callback
// 那 marks items 作为 focused during rendering.
func FocusedRenderCallback(list *List) RenderCallback {
	return func(idx, selectedIdx int, item Item) Item {
		if focusable, ok := item.(Focusable); ok {
			focusable.SetFocused(list.Focused() && idx == selectedIdx)
			return focusable.(Item)
		}
		return item
	}
}
