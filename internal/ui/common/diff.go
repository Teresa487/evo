package common

import (
	"github.com/charmbracelet/crush/internal/ui/diffview"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// DiffFormatter returns diff formatter 用 given styles 那 can 是
// used 到 format diff outputs.
func DiffFormatter(s *styles.Styles) *diffview.DiffView {
	formatDiff := diffview.New()
	diff := formatDiff.ChromaStyle(ChromaStyle(s, nil)).Style(s.Diff).TabWidth(4)
	return diff
}
