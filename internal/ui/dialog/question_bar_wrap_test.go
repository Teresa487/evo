package dialog

import (
	"testing"

	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// gutter bar marks which item selection 是 在. 一个 label 或
// description long enough 到 wrap must carry it down every 行, 或
// bar breaks into disconnected stubs 和 one item reads 作为 several.
//
// keyboard 路径 always did 这. Hover lit bar 在 first 行
// 的 each block 和 dropped it 在 rest, which 是 what bug looked
// like 在 screen: lit line, dark line, lit line.
func TestChoiceBarSurvivesWrapping(t *testing.T) {
	t.Parallel()

	s := styles.CharmtonePantera()
	req := question.Question{
		ID:   "q1",
		Type: question.TypeSingleChoice,
		Text: "Pick one",
		Choices: []question.Choice{
			{
				ID:          "a",
				Label:       "Keep scene grouping, accept the rewinds",
				Description: "Scenes stay whole; 273 groups, avg 3.8 photos",
			},
			{ID: "b", Label: "Groups must be consecutive runs"},
		},
	}

	for _, tc := range []struct {
		name  string
		setUp func(d *SingleChoice)
	}{
		{"keyboard", func(d *SingleChoice) { d.mouseActive = false; d.cursorIdx = 0 }},
		{"hover", func(d *SingleChoice) { d.mouseActive = true; d.hoveredChoice = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := NewSingleChoice(&s, req)
			tc.setUp(d)

			// 一个 label 的 two rows, stated outright rather than 通过
			// restating how component wraps one. width 是 narrow
			// enough 那 description wraps 作为 well.
			lines := d.buildLines(36, "> ", func(int, question.Choice, bool, int) string {
				return "label row one\nlabel row two"
			})

			var rows int
			for _, ln := range lines {
				if ln.choiceIdx != 0 || ansi.Strip(ln.text) == "" {
					continue
				}
				rows++
				require.Contains(t, ansi.Strip(ln.text), "┃",
					"every row of the highlighted choice needs the bar, got %q", ansi.Strip(ln.text))
			}
			require.Greater(t, rows, 2, "the choice must span several rows for this to test anything")
		})
	}
}
