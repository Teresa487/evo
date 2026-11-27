package common

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// ButtonOpts defines configuration 为 single button
type ButtonOpts struct {
	// 文本 是 button label
	Text string
	// UnderlineIndex 是 0-based index 的 character 到 underline (-1 为 none)
	UnderlineIndex int
	// Selected indicates whether 这 button 是 currently selected
	Selected bool
	// Hovered indicates whether mouse 是 hovering over button
	Hovered bool
	// Inactive marks button inside 提示词 那 是 不 在 活跃
	// pane. 它 renders lighter than plain blurred style 所以
	// choices stay legible while the chat has focus.
	Inactive bool
	// Padding inner horizontal padding defaults 到 2 如果 这 是 0
	Padding int
}

// Button creates button 用 underlined character 和 selection state
func Button(t *styles.Styles, opts ButtonOpts) string {
	// Select style based 在 selection/hover state.
	style := t.Button.Blurred
	if opts.Selected && opts.Hovered {
		style = t.Button.Focused.Bold(true)
	} else if opts.Hovered {
		style = t.Button.Hovered.Bold(true)
	} else if opts.Selected {
		style = t.Button.Focused
	} else if opts.Inactive {
		style = t.Button.Inactive
	}

	text := opts.Text
	if opts.Padding == 0 {
		opts.Padding = 2
	}

	// index 是 out 的 bound
	if opts.UnderlineIndex > -1 && opts.UnderlineIndex > len(text)-1 {
		opts.UnderlineIndex = -1
	}

	text = style.Padding(0, opts.Padding).Render(text)

	if opts.UnderlineIndex != -1 {
		text = lipgloss.StyleRanges(text, lipgloss.NewRange(opts.Padding+opts.UnderlineIndex, opts.Padding+opts.UnderlineIndex+1, style.Underline(true)))
	}

	return text
}

// ButtonGroup creates 行 的 selectable buttons
// Spacing 是 separator between buttons
// Use " " 或 similar 为 horizontal layout
// Use "\n" 为 vertical layout
// Defaults 到 " " (horizontal)
func ButtonGroup(t *styles.Styles, buttons []ButtonOpts, spacing string) string {
	if len(buttons) == 0 {
		return ""
	}

	if spacing == "" {
		spacing = "  "
	}

	parts := make([]string, len(buttons))
	for i, button := range buttons {
		parts[i] = Button(t, button)
	}

	return strings.Join(parts, spacing)
}

// ButtonHitCompositor builds lipgloss Compositor 用 one hit layer per
// button. 一个 spacing 值 containing newline positions buttons vertically;
// otherwise they 是 positioned horizontally. Layer IDs 是 "btn_0",
// "btn_1", etc. spacing must match what ButtonGroup uses 为 rendering.
func ButtonHitCompositor(sty *styles.Styles, opts []ButtonOpts, spacing string, x, y int) *lipgloss.Compositor {
	if len(opts) == 0 {
		return nil
	}
	if spacing == "" {
		spacing = "  "
	}
	vertical := strings.Contains(spacing, "\n")
	spacingWidth := lipgloss.Width(spacing)
	spacingHeight := strings.Count(spacing, "\n")
	var layers []*lipgloss.Layer
	bx, by := x, y
	for i, o := range opts {
		b := Button(sty, o)
		w := lipgloss.Width(b)
		hitStr := strings.Repeat(" ", w)
		layers = append(layers, lipgloss.NewLayer(hitStr).X(bx).Y(by).ID(fmt.Sprintf("btn_%d", i)))
		if vertical {
			bx = x
			by += lipgloss.Height(b) + max(0, spacingHeight-1)
		} else {
			bx += w + spacingWidth
		}
	}
	return lipgloss.NewCompositor(layers...)
}

// HitButtonIndex checks compositor 为 button hit 和 returns
// button index, 或 -1 如果 no button was hit.
func HitButtonIndex(c *lipgloss.Compositor, x, y int) int {
	if c == nil {
		return -1
	}
	hit := c.Hit(x, y)
	if hit.Empty() {
		return -1
	}
	var idx int
	if _, err := fmt.Sscanf(hit.ID(), "btn_%d", &idx); err != nil {
		return -1
	}
	return idx
}
