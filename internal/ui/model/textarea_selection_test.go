package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

func newSelectionTestUI() *UI {
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	sty := u.com.Styles.Attachments
	u.attachments = attachments.New(
		attachments.NewRenderer(sty),
		attachments.Keymap{},
	)
	u.updateLayoutAndSize()
	return u
}

func TestTextareaSelectionKeys(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()

	for _, r := range "hello" {
		u.textarea.InsertRune(r)
	}
	u.textarea.CursorStart()

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})

	require.True(t, u.textarea.HasSelection())
	require.Equal(t, "he", u.textarea.SelectedText())
}

func TestTextareaCutSelection(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()
	u.keyMap = DefaultKeyMap()

	for _, r := range "hello" {
		u.textarea.InsertRune(r)
	}
	u.textarea.CursorStart()

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	require.True(t, u.textarea.HasSelection())

	_, _ = u.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl | tea.ModShift})

	require.False(t, u.textarea.HasSelection())
	require.Equal(t, "llo", u.textarea.Value())
}

func TestTextareaMouseSelection(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()

	for _, r := range "hello world" {
		u.textarea.InsertRune(r)
	}
	u.textarea.CursorStart()

	// textarea renders one 行 below editor top ( attachments
	// 行 是 always reserved, even 当 empty). 默认 提示词
	// ("┃ ") 是 2 cells wide.
	startX := u.layout.editor.Min.X + 2
	y := u.layout.editor.Min.Y + 1

	_, _ = u.Update(tea.MouseClickMsg(tea.Mouse{X: startX, Y: y, Button: uv.MouseLeft}))
	require.True(t, u.textareaMouseSelecting)

	// Drag few cells 到 right.
	_, _ = u.Update(tea.MouseMotionMsg(tea.Mouse{X: startX + 5, Y: y, Button: uv.MouseLeft}))

	// Release 到 end gesture.
	_, _ = u.Update(tea.MouseReleaseMsg(tea.Mouse{X: startX + 5, Y: y, Button: uv.MouseLeft}))
	require.False(t, u.textareaMouseSelecting)

	require.True(t, u.textarea.HasSelection())
	require.Equal(t, "hello", u.textarea.SelectedText())
}

func TestTextareaMouseClickOutsideDoesNotSelect(t *testing.T) {
	t.Parallel()

	u := newSelectionTestUI()

	for _, r := range "hello" {
		u.textarea.InsertRune(r)
	}

	// Click 在 chat area, far 从 editor.
	_, _ = u.Update(tea.MouseClickMsg(tea.Mouse{
		X:      u.layout.main.Min.X + 1,
		Y:      u.layout.main.Min.Y + 1,
		Button: uv.MouseLeft,
	}))

	require.False(t, u.textareaMouseSelecting)
	require.False(t, u.textarea.HasSelection())
}
