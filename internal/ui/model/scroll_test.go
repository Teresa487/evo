package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyChatScroll_LargeDeltaIsNotRewoundBySelection(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.chat.ScrollToBottom()
	u.chat.SelectLast()
	before := u.chat.Offset()

	u.applyChatScroll(-60)
	require.Equal(t, before-60, u.chat.Offset(), "selection follow must not rewind the viewport")
	require.True(t, u.chat.SelectedItemInView())

	u.applyChatScroll(60)
	require.True(t, u.chat.AtBottom())
	require.Equal(t, u.chat.Len()-1, u.chat.Selected(), "reaching the bottom must select the last item")
}

func TestApplyChatScroll_OutOfViewSelectionGoesToNearestEdge(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.chat.ScrollToBottom()

	// Selection far above viewport, 用户 scrolls up: it should land 在
	// top 行 (nearest edge), 不 jump 到 bottom 行.
	u.chat.SetSelected(0)
	u.applyChatScroll(-5)
	got := u.chat.Selected()
	u.chat.SelectFirstInView()
	require.Equal(t, u.chat.Selected(), got, "selection above viewport must snap to the top edge")

	// Selection below viewport, 用户 scrolls down: bottom 行.
	u.chat.ScrollToTop()
	u.chat.SetSelected(u.chat.Len() - 1)
	u.applyChatScroll(5)
	got = u.chat.Selected()
	u.chat.SelectLastInView()
	require.Equal(t, u.chat.Selected(), got, "selection below viewport must snap to the bottom edge")
}

// Swapping 提示词 echo 为 存储 提示词 removes item 从
// followed view. view must stay pinned 到 end rather than leaving
// the transcript lifted off the bottom until something else scrolls it.
func TestChatFollow_RemovingItemKeepsViewPinned(t *testing.T) {
	t.Parallel()
	u := newFrameTestUI(t)
	u.chat.ScrollToBottom()
	_ = u.View()

	u.chat.RemoveMessage("198")
	_ = u.View()
	gotIdx, gotLine := u.chat.ScrollPosition()

	u.chat.ScrollToBottom()
	wantIdx, wantLine := u.chat.ScrollPosition()
	require.Equal(t, wantIdx, gotIdx)
	require.Equal(t, wantLine, gotLine)
}
