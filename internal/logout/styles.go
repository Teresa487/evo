package logout

import (
	"charm.land/lipgloss/v2"
	"charm.land/tone"
)

// buttonPadding 是 horizontal padding inside button.
const buttonPadding = 5

var (
	questionStyle = lipgloss.NewStyle().
			Bold(true)

	buttonFocusedStyle = lipgloss.NewStyle().
				Foreground(tone.Sash).
				Background(tone.Blush)
	buttonBlurredStyle = lipgloss.NewStyle().
				Foreground(tone.Soda).
				Background(tone.BBQ)

	choiceSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(tone.Blush)
)

// renderButton renders selectable button 用 underlined accelerator
// key (它的 first letter).
func renderButton(text string, selected bool) string {
	base := buttonBlurredStyle
	if selected {
		base = buttonFocusedStyle
	}
	rendered := base.Padding(0, buttonPadding).Render(text)
	if text != "" {
		// Underline the accelerator key (the first letter). The range style
		// must 不 carry padding, 作为 StyleRanges re-renders matched 文本.
		rendered = lipgloss.StyleRanges(rendered,
			lipgloss.NewRange(buttonPadding, buttonPadding+1, base.Underline(true)))
	}
	return rendered
}
