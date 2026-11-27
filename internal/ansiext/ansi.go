package ansiext

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Escape replaces control characters 用 their Unicode Control Picture
// representations 到 ensure they 是 displayed correctly 在 UI.
func Escape(content string) string {
	var sb strings.Builder
	sb.Grow(len(content))
	for _, r := range content {
		switch {
		case r >= 0 && r <= 0x1f: // Control characters 0x00-0x1F
			sb.WriteRune('\u2400' + r)
		case r == ansi.DEL:
			sb.WriteRune('\u2421')
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
