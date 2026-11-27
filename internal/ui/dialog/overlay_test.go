package dialog

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// stubDialog 是 minimal Dialog 为 testing Overlay behavior.
type stubDialog struct {
	id       string
	received []tea.Msg
}

func (s *stubDialog) ID() string { return s.id }
func (s *stubDialog) HandleMsg(msg tea.Msg) Action {
	s.received = append(s.received, msg)
	return nil
}
func (s *stubDialog) Draw(_ uv.Screen, _ uv.Rectangle) *tea.Cursor { return nil }

func keyMsg(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// TestOverlay_GracePeriodSwallowsKeys verifies 那 all keystrokes
// arriving within grace period after OpenDialogWithGrace 是 absorbed
// 和 never forwarded 到 dialog.
func TestOverlay_GracePeriodSwallowsKeys(t *testing.T) {
	t.Parallel()

	d := &stubDialog{id: "test"}
	o := NewOverlay()
	o.OpenDialogWithGrace(d)

	for _, r := range []rune{'a', 's', 'd', 'x', 'z'} {
		o.Update(keyMsg(r))
	}
	require.Empty(t, d.received, "no keys should reach the dialog during grace period")
}

// TestOverlay_GracePeriodArmsAfterQuiet verifies 那 once input has been
// quiet 为 graceQuietPeriod, subsequent keys 是 forwarded normally.
func TestOverlay_GracePeriodArmsAfterQuiet(t *testing.T) {
	t.Parallel()

	d := &stubDialog{id: "test"}
	o := NewOverlay()
	o.OpenDialogWithGrace(d)

	// Backdate 所以 both deadlines have elapsed.
	o.graceOpenedAt = time.Now().Add(-graceMaxDelay - time.Millisecond)
	o.graceLastInputAt = time.Now().Add(-graceQuietPeriod - time.Millisecond)

	o.Update(keyMsg('a'))
	require.Len(t, d.received, 1, "key after grace period should reach the dialog")
}

// TestOverlay_GracePeriodBurstExtendsQuietWindow verifies 那 sustained
// burst 的 keystrokes keeps resetting quiet timer, 但 fixed
// ceiling (graceMaxDelay) eventually arms the dialog regardless.
func TestOverlay_GracePeriodBurstExtendsQuietWindow(t *testing.T) {
	t.Parallel()

	d := &stubDialog{id: "test"}
	o := NewOverlay()
	o.OpenDialogWithGrace(d)

	// Simulate keys arriving every 40ms (within graceQuietPeriod).
	// Each one resets quiet timer, 但 we stay under graceMaxDelay.
	for range 5 {
		o.graceLastInputAt = time.Now().Add(-40 * time.Millisecond)
		o.Update(keyMsg('a'))
	}
	require.Empty(t, d.received, "burst within max delay should be absorbed")

	// Now exceed max delay ceiling. Even though lastInputAt 是 recent,
	// fixed 期限 forces arming.
	o.graceOpenedAt = time.Now().Add(-graceMaxDelay - time.Millisecond)
	o.graceLastInputAt = time.Now() // just typed, but max delay wins
	o.Update(keyMsg('b'))
	require.Len(t, d.received, 1, "key after max delay should reach dialog even during burst")
}

// TestOverlay_OpenDialogWithoutGraceHasNoGuard verifies 那 dialogs
// opened via OpenDialog (without grace) receive keys immediately.
func TestOverlay_OpenDialogWithoutGraceHasNoGuard(t *testing.T) {
	t.Parallel()

	d := &stubDialog{id: "test"}
	o := NewOverlay()
	o.OpenDialog(d)

	o.Update(keyMsg('a'))
	require.Len(t, d.received, 1, "dialog without grace should receive keys immediately")
}

// TestOverlay_GraceClearedOnClose verifies 那 closing front dialog
// clears grace state 所以 subsequently opened dialog without grace 是
// 不 affected.
func TestOverlay_GraceClearedOnClose(t *testing.T) {
	t.Parallel()

	d1 := &stubDialog{id: "first"}
	d2 := &stubDialog{id: "second"}
	o := NewOverlay()
	o.OpenDialogWithGrace(d1)

	// Close grace dialog 和 open normal one.
	o.CloseFrontDialog()
	o.OpenDialog(d2)

	o.Update(keyMsg('a'))
	require.Len(t, d2.received, 1, "new dialog after grace close should receive keys immediately")
}

// TestOverlay_NonKeyMessagesPassDuringGrace verifies 那 non-keypress
// messages (e.g. mouse, tick) 是 forwarded even during grace period.
func TestOverlay_NonKeyMessagesPassDuringGrace(t *testing.T) {
	t.Parallel()

	d := &stubDialog{id: "test"}
	o := NewOverlay()
	o.OpenDialogWithGrace(d)

	o.Update(tea.MouseWheelMsg{})
	require.Len(t, d.received, 1, "non-key messages should pass through during grace")
}

// TestOverlay_ReopenSameDialogSkipsGrace verifies 那 当 dialog 是
// closed 和 immediately reopened 用 same ID (e.g. successive
// 权限 prompts), grace period 是 skipped 所以 用户 can
// continue interacting without lost keystrokes.
func TestOverlay_ReopenSameDialogSkipsGrace(t *testing.T) {
	t.Parallel()

	d1 := &stubDialog{id: "permissions"}
	o := NewOverlay()
	o.OpenDialogWithGrace(d1)

	// Close 和 immediately reopen 用 same ID.
	o.CloseDialog("permissions")
	d2 := &stubDialog{id: "permissions"}
	o.OpenDialogWithGrace(d2)

	// Keys 应当 reach new dialog immediately — no grace.
	o.Update(keyMsg('a'))
	require.Len(t, d2.received, 1, "reopened dialog with same ID should skip grace")
}

// TestOverlay_ReopenDifferentDialogKeepsGrace verifies 那 reopening
// different dialog ID still gets the normal grace period.
func TestOverlay_ReopenDifferentDialogKeepsGrace(t *testing.T) {
	t.Parallel()

	d1 := &stubDialog{id: "permissions"}
	o := NewOverlay()
	o.OpenDialogWithGrace(d1)
	o.CloseDialog("permissions")

	d2 := &stubDialog{id: "other"}
	o.OpenDialogWithGrace(d2)

	o.Update(keyMsg('a'))
	require.Empty(t, d2.received, "different dialog ID should still have grace period")
}

// TestOverlay_ReopenAfterWindowExpiresKeepsGrace verifies 那 如果 enough
// time passes between close 和 reopen, grace period applies normally.
func TestOverlay_ReopenAfterWindowExpiresKeepsGrace(t *testing.T) {
	t.Parallel()

	d1 := &stubDialog{id: "permissions"}
	o := NewOverlay()
	o.OpenDialogWithGrace(d1)
	o.CloseDialog("permissions")

	// Backdate close time 所以 it falls outside reopen 窗口.
	o.lastClosedAt = time.Now().Add(-reopenGraceWindow - time.Millisecond)

	d2 := &stubDialog{id: "permissions"}
	o.OpenDialogWithGrace(d2)

	o.Update(keyMsg('a'))
	require.Empty(t, d2.received, "reopened dialog after window expires should have grace")
}
