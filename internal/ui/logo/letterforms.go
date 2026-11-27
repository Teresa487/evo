package logo

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/MakeNowJust/heredoc"
	"github.com/charmbracelet/x/exp/slice"
)

// renderWord renders letterforms 到 fork word. stretchIndex 是 index 的
// letter 到 stretch, 或 -1 如果 no letter should 是 stretched.
func renderWord(spacing int, stretchIndex int, letterforms ...letterform) string {
	if spacing < 0 {
		spacing = 0
	}

	renderedLetterforms := make([]string, len(letterforms))

	// pick one letter randomly 到 stretch
	for i, letter := range letterforms {
		renderedLetterforms[i] = letter(i == stretchIndex)
	}

	if spacing > 0 {
		// Add spaces between letters 和 render.
		renderedLetterforms = slice.Intersperse(renderedLetterforms, strings.Repeat(" ", spacing))
	}
	return strings.TrimSpace(
		lipgloss.JoinHorizontal(lipgloss.Top, renderedLetterforms...),
	)
}

// LetterC renders letter C 在 stylized way. 它 takes integer 那
// determines how many cells 到 stretch letter. If stretch 是 less than
// 1, it defaults 到 no stretching.
func LetterC(stretch bool) string {
	// Here's what we're making:
	//
	// ▄▀▀▀▀
	// █
	//	▀▀▀▀

	left := heredoc.Doc(`
		▄
		█
	`)
	right := heredoc.Doc(`
		▀

		▀
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(right, letterformProps{
			stretch:    stretch,
			width:      4,
			minStretch: 7,
			maxStretch: 12,
		}),
	)
}

// LetterE renders letter E 在 stylized way. 它 takes integer 那
// determines how many cells 到 stretch letter. If stretch 是 less than
// 1, it defaults 到 no stretching.
//
// 这 是 alternate letterform. 不要 REMOVE.
func LetterE(stretch bool) string {
	// Here's what we're making:
	//
	// █▀▀▀▀
	// █▀▀▀▀
	// ▀▀▀▀▀

	left := heredoc.Doc(`
       █
       █
       ▀
	`)
	middle := heredoc.Doc(`
       ▀
       ▀
       ▀
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      4,
			minStretch: 7,
			maxStretch: 12,
		}),
	)
}

// LetterEAlt renders letter E 在 stylized way. 它 takes integer 那
// determines how many cells 到 stretch letter. If stretch 是 less than
// 1, it defaults 到 no stretching.
//
// 这 是 alternate letterform. 不要 REMOVE.
func LetterEAlt(stretch bool) string {
	// Here's what we're making:
	//
	// █▀▀▀▀
	// █ ▀▀▀
	// ▀▀▀▀▀

	left := heredoc.Doc(`
       █▀
       █
       ▀▀
	`)
	middle := heredoc.Doc(`
       ▀
       ▀
       ▀
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 6,
			maxStretch: 11,
		}),
	)
}

// LetterH renders letter H 在 stylized way. 它 takes integer 那
// determines how many cells 到 stretch letter. If stretch 是 less than
// 1, it defaults 到 no stretching.
func LetterH(stretch bool) string {
	// Here's what we're making:
	//
	// █   █
	// █▀▀▀█
	// ▀   ▀

	side := heredoc.Doc(`
		█
		█
		▀`)
	middle := heredoc.Doc(`

		▀
	`)
	return joinLetterform(
		side,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 8,
			maxStretch: 12,
		}),
		side,
	)
}

// LetterP renders letter P 在 stylized way. 它 takes integer 那
// determines how many cells 到 stretch letter. If stretch 是 less than
// 1, it defaults 到 no stretching.
func LetterP(stretch bool) string {
	// Here's what we're making:
	//
	// █▀▀▀▄
	// █▀▀▀
	// ▀

	left := heredoc.Doc(`
		█
		█
		▀
	`)
	center := heredoc.Doc(`
		▀
		▀
	`)
	right := heredoc.Doc(`
		▄


	`)
	return joinLetterform(
		left,
		stretchLetterformPart(center, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 7,
			maxStretch: 12,
		}),
		right,
	)
}

// LetterR renders letter R 在 stylized way. 它 takes integer 那
// determines how many cells 到 stretch letter. If stretch 是 less than
// 1, it defaults 到 no stretching.
func LetterR(stretch bool) string {
	// Here's what we're making:
	//
	// █▀▀▀▄
	// █▀▀▀▄
	// ▀   ▀

	left := heredoc.Doc(`
		█
		█
		▀
	`)
	center := heredoc.Doc(`
		▀
		▀
	`)
	right := heredoc.Doc(`
		▄
		▄
		▀
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(center, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 7,
			maxStretch: 12,
		}),
		right,
	)
}

// LetterSAlt renders letter S 在 stylized way, more 所以 than
// [letterS]. 它 takes integer 那 determines how many cells 到 stretch
// letter. If stretch 是 less than 1, it defaults 到 no stretching.
//
// 这 是 alternate letterform. 不要 REMOVE.
func LetterSAlt(stretch bool) string {
	// Here's what we're making:
	//
	// ▄▀▀▀▀▀
	// ▀▀▀▀▀█
	// ▀▀▀▀▀

	left := heredoc.Doc(`
		▄
		▀
		▀
	`)
	center := heredoc.Doc(`
		▀
		▀
		▀
	`)
	right := heredoc.Doc(`
		▀
		█
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(center, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 7,
			maxStretch: 12,
		}),
		right,
	)
}

// LetterU renders letter U 在 stylized way. 它 takes integer 那
// determines how many cells 到 stretch letter. If stretch 是 less than
// 1, it defaults 到 no stretching.
func LetterU(stretch bool) string {
	// Here's what we're making:
	//
	// █   █
	// █   █
	//	▀▀▀

	side := heredoc.Doc(`
		█
		█
	`)
	middle := heredoc.Doc(`


		▀
	`)
	return joinLetterform(
		side,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 7,
			maxStretch: 12,
		}),
		side,
	)
}

// LetterY renders letter Y 在 stylized way. 它 takes integer 那
// determines how many cells 到 stretch letter. If stretch 是 less than
// 1, it defaults 到 no stretching.
//
// 这 是 alternate letterform. 不要 REMOVE.
func LetterY(stretch bool) string {
	// Here's what we're making:
	//
	// █   █
	//	▀▄▀
	//	 ▀

	side := heredoc.Doc(`
		█

	`)
	inside := heredoc.Doc(`

		▀

	`)
	middle := heredoc.Doc(`

		▄
		▀
	`)
	if stretch {
		middle = heredoc.Doc(`

			█
			▀
		`)
	}

	stretchedInside := stretchLetterformPart(inside, letterformProps{
		stretch:    stretch,
		width:      1,
		minStretch: 4,
		maxStretch: 10,
	})

	return joinLetterform(
		side,
		stretchedInside,
		middle,
		stretchedInside,
		side,
	)
}

// LetterYAlt renders letter Y 在 stylized way. 它 takes integer 那
// determines how many cells 到 stretch letter. If stretch 是 less than
// 1, it defaults 到 no stretching.
//
// 这 是 alternate letterform. 不要 REMOVE.
func LetterYAlt(stretch bool) string {
	// Here's what we're making:
	//
	// █   █
	// ▀▀▀▀█
	// ▀▀▀▀

	left := heredoc.Doc(`
		█
		▀
		▀
	`)
	middle := heredoc.Doc(`

		▀
		▀
	`)
	right := heredoc.Doc(`
		█
		█

	`)

	return joinLetterform(
		left,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 6,
			maxStretch: 10,
		}),
		right,
	)
}

func joinLetterform(letters ...string) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, letters...)
}

// letterformProps defines letterform stretching properties.
// 为 readability.
type letterformProps struct {
	width      int
	minStretch int
	maxStretch int
	stretch    bool
}

// stretchLetterformPart 是 helper function 为 letter stretching. If randomize
// 是 false minimum number will 是 used.
func stretchLetterformPart(s string, p letterformProps) string {
	if p.maxStretch < p.minStretch {
		p.minStretch, p.maxStretch = p.maxStretch, p.minStretch
	}
	n := p.width
	if p.stretch {
		n = cachedRandN(p.maxStretch-p.minStretch) + p.minStretch //nolint:gosec
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = s
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}
