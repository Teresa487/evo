package dialog

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// maxVisibleToggleRows bounds the toggles dialogs' viewports.
const maxVisibleToggleRows = 12

// minToggleDialogWidth 是 comfortable minimum width 所以 short names do
// 不 shrink dialog 和 truncate help line.
const minToggleDialogWidth = 68

// scrollbarColumnWidth 是 columns scrollbar column adds 到
// 行 area: one space before track glyph.
const scrollbarColumnWidth = 2

// toggleVisibleOffset returns first visible 行 index 为 窗口 的
// visible rows over count items, scrolling 所以 cursor always stays 在
// view.
func toggleVisibleOffset(cursor, offset, count, visible int) int {
	offset = min(offset, max(0, count-visible))
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+visible {
		offset = cursor - visible + 1
	}
	return max(0, offset)
}

// joinToggleRows stacks rendered rows 和, 当 content overflows
// viewport, appends scrollbar column like sessions 和 models
// pickers.
func joinToggleRows(t *styles.Styles, rows []string, contentSize, visible, offset int) string {
	if len(rows) == 0 {
		return ""
	}
	// Compose line 通过 line instead 的 block-level JoinHorizontal:
	// scrollbar string splits into exactly len(rows) lines, 所以 pairing
	// them keeps every 行's track glyph 在 same line.
	scrollLines := strings.Split(common.Scrollbar(t, len(rows), contentSize, visible, offset), "\n")
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(row)
		if i < len(scrollLines) {
			b.WriteString(" ")
			b.WriteString(scrollLines[i])
		}
	}
	// Blank spacer lines above 和 below, matching unscrolled layout.
	return lipgloss.JoinVertical(lipgloss.Left, "", b.String(), "")
}
