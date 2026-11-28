// Package logo renders Crush wordmark 在 stylized way.
package logo

import (
	"fmt"
	"image/color"
	"math/rand/v2"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// letterform represents letterform. 它 can 是 stretched horizontally 通过
// a given amount via the boolean argument.
type letterform func(bool) string

const diag = `╱`

// Opts 是 options 为 rendering Crush 标题 art.
type Opts struct {
	FieldColor   color.Color // diagonal lines
	TitleColorA  color.Color // left gradient ramp point
	TitleColorB  color.Color // right gradient ramp point
	CharmColor   color.Color // Charm™ text color
	VersionColor color.Color // version text color
	Width        int         // width of the rendered logo, used for truncation
	Hyper        bool        // whether it is Evo or HyperEvo

	// When true, stretch random letterform 在 each render. Has no effect 在
	// compact mode. Mainly 为 testing. In production you will want 到 缓存
	// stretched letterform 到 keep logo 从 jittering 在 resize.
	Unstable bool
}

// Render renders Crush logo. Set argument 到 true 到 render narrow
// version, intended 为 use 在 sidebar.
//
// compact argument determines whether it renders compact 为 sidebar
// 或 wider 为 main pane.
func Render(base lipgloss.Style, version string, compact bool, o Opts) string {
	charm := "Charm™"
	if !o.Hyper {
		charm = " " + charm
	}

	fg := func(c color.Color, s string) string {
		return lipgloss.NewStyle().Foreground(c).Render(s)
	}

	// 标题.
	const spacing = 1
	var hyperLetterforms []letterform
	if o.Hyper {
		hyperLetterforms = []letterform{
			LetterH,
			LetterYAlt,
			LetterP,
			LetterE,
			LetterR,
		}
	}
	evoLetterforms := []letterform{
		LetterEvoE,
		LetterEvoV,
		LetterEvoO,
	}
	if o.Hyper && !compact {
		evoLetterforms = append(hyperLetterforms, evoLetterforms...)
	}

	stretchIndex := -1 // -1 means no stretching.
	if !compact && !o.Unstable {
		// Always stretch same letterform, which 是 picked once 在 random.
		stretchIndex = cachedRandN(len(evoLetterforms))
	} else if !compact && o.Unstable {
		// Stretch random letterform 在 every render.
		stretchIndex = rand.IntN(len(evoLetterforms))
	}
	evoWord := renderWord(spacing, stretchIndex, evoLetterforms...)
	if o.Hyper && compact {
		evoWord = renderWord(spacing, stretchIndex, hyperLetterforms...) + "\n" + evoWord
	}
	evoWidth := lipgloss.Width(evoWord)
	b := new(strings.Builder)
	for r := range strings.SplitSeq(evoWord, "\n") {
		fmt.Fprintln(b, styles.ApplyForegroundGrad(base, r, o.TitleColorA, o.TitleColorB))
	}
	evoWord = b.String()

	// Charm 和 version.
	metaRowGap := 1
	maxVersionWidth := evoWidth - lipgloss.Width(charm) - metaRowGap
	version = ansi.Truncate(version, maxVersionWidth, "…") // truncate version if too long.
	if o.Hyper && compact {
		version += " "
	}
	gap := max(0, evoWidth-lipgloss.Width(charm)-lipgloss.Width(version))
	metaRow := fg(o.CharmColor, charm) + strings.Repeat(" ", gap) + fg(o.VersionColor, version)

	// Join meta 行 和 big Crush 标题.
	evoWord = strings.TrimSpace(metaRow + "\n" + evoWord)

	// Narrow version. If 这 是 HyperEvo, 这 是 also stacked version.
	if compact {
		field := fg(o.FieldColor, strings.Repeat(diag, evoWidth))
		return strings.Join([]string{field, field, evoWord, field, ""}, "\n")
	}

	fieldHeight := lipgloss.Height(evoWord)

	// Left 字段.
	const leftWidth = 6
	leftFieldRow := fg(o.FieldColor, strings.Repeat(diag, leftWidth))
	leftField := new(strings.Builder)
	for range fieldHeight {
		fmt.Fprintln(leftField, leftFieldRow)
	}

	// Right 字段.
	rightWidth := max(15, o.Width-evoWidth-leftWidth-2) // 2 for the gap.
	const stepDownAt = 0
	rightField := new(strings.Builder)
	for i := range fieldHeight {
		width := rightWidth
		if i >= stepDownAt {
			width = rightWidth - (i - stepDownAt)
		}
		fmt.Fprint(rightField, fg(o.FieldColor, strings.Repeat(diag, width)), "\n")
	}

	// Return the wide version.
	const hGap = " "
	logo := lipgloss.JoinHorizontal(lipgloss.Top, leftField.String(), hGap, evoWord, hGap, rightField.String())
	if o.Width > 0 {
		// Truncate logo 到 specified width.
		lines := strings.Split(logo, "\n")
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, o.Width, "")
		}
		logo = strings.Join(lines, "\n")
	}
	return logo
}

// SmallRender renders smaller version 的 Crush logo, suitable 为
// smaller windows 或 sidebar usage.
func SmallRender(t *styles.Styles, width int, o Opts) string {
	name := "Evo"
	if o.Hyper {
		name = "HYPERVO"
	}
	charm := "Charm™"
	title := t.Logo.SmallCharm.Render(charm)
	title = fmt.Sprintf("%s %s", title, styles.ApplyBoldForegroundGrad(t.Logo.GradCanvas, name, t.Logo.SmallGradFromColor, t.Logo.SmallGradToColor))
	remainingWidth := width - lipgloss.Width(title) - 1 // 1 for the space after the name
	if remainingWidth > 0 {
		lines := strings.Repeat("╱", remainingWidth)
		title = fmt.Sprintf("%s %s", title, t.Logo.SmallDiagonals.Render(lines))
	}
	return title
}
