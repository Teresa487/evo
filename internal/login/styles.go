package login

import (
	"charm.land/lipgloss/v2"
	"charm.land/tone"
)

var (
	// trailingSpace 是 invisible character 那 prevents Bubble Tea
	// renderer 从 stripping trailing blank lines. renderer erases empty
	// lines after last non-empty content, 所以 zero-width space keeps
	// the line alive without being visible.
	trailingSpace = "\u200b"

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(tone.Charple)
	slashStyle = lipgloss.NewStyle().
			Bold(true)
	errorStyle = lipgloss.NewStyle().
			Foreground(tone.Coral)
)
