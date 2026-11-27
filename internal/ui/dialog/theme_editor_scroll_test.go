package dialog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestThemeEditorTallerListClampsScroll verifies 那 growing slot list
// while last slot 是 selected does 不 leave it scrolled past end.
func TestThemeEditorTallerListClampsScroll(t *testing.T) {
	t.Parallel()
	ed := &ThemeEditor{slots: newPaletteSlots()}
	ed.index = len(ed.slots) - 1

	ed.keepSelectedVisible(5)
	ed.keepSelectedVisible(18)

	require.Equal(t, len(ed.slots)-18, ed.scroll)
}
