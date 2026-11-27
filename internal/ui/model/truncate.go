package model

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// truncateRows keeps 在 most maxItems rows, spending last slot 在
// "…和 N more" hint 当 they do 不 all fit.
func truncateRows(rows []string, maxItems int, hint lipgloss.Style) []string {
	if maxItems <= 0 {
		return nil
	}
	if len(rows) <= maxItems {
		return rows
	}
	remaining := len(rows) - (maxItems - 1)
	return append(rows[:maxItems-1:maxItems-1], hint.Render(fmt.Sprintf("…and %d more", remaining)))
}
