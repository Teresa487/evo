package list

import (
	"image"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/stringext"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// DefaultHighlighter 是 默认 highlighter function 那 applies inverse style.
var DefaultHighlighter Highlighter = func(x, y int, c *uv.Cell) *uv.Cell {
	if c == nil {
		return c
	}
	c.Style.Attrs |= uv.AttrReverse
	return c
}

// Highlighter represents function 那 defines how 到 highlight 文本.
type Highlighter func(x, y int, c *uv.Cell) *uv.Cell

// HighlightContent returns content 用 highlighted regions based 在 specified parameters.
func HighlightContent(content string, area image.Rectangle, startLine, startCol, endLine, endCol int) string {
	content = stringext.NormalizeSpace(content)

	if startLine < 0 || startCol < 0 {
		return ""
	}

	width, height := area.Dx(), area.Dy()
	buf := renderBuffer(content, area, width, height)

	// Treat -1 作为 "end 的 content".
	if endLine < 0 {
		endLine = height - 1
	}
	if endCol < 0 {
		endCol = width
	}

	rows := extractRows(buf, startLine, startCol, endLine, endCol, height)
	return joinRows(rows, width) + "\n"
}

// renderBuffer draws content into screen 缓冲 的 given dimensions.
func renderBuffer(content string, area image.Rectangle, width, height int) uv.ScreenBuffer {
	buf := uv.NewScreenBuffer(width, height)
	styled := uv.NewStyledString(content)
	styled.Draw(&buf, area)
	return buf
}

// extractRows extracts 文本 的 selected region 从 缓冲,
// one string per 行, trimmed 到 last cell holding content.
func extractRows(buf uv.ScreenBuffer, startLine, startCol, endLine, endCol, height int) []string {
	rows := make([]string, 0, endLine-startLine+1)
	for y := startLine; y <= endLine && y < height; y++ {
		if y >= buf.Height() {
			break
		}

		line := buf.Line(y)
		colStart := 0
		if y == startLine {
			colStart = min(startCol, len(line))
		}
		colEnd := len(line)
		if y == endLine {
			colEnd = min(endCol, len(line))
		}

		rows = append(rows, extractRow(line, colStart, colEnd))
	}
	return rows
}

// extractRow returns 文本 的 single 缓冲 line between colStart 和
// colEnd, trimmed 到 last cell holding any content (including explicit
// spaces: renderers like glamour pad rows 用 real space cells, 所以 content
// usually reaches the full width).
//
// Codespan padding cells 是 converted back into backticks: markdown inline
// code renders blank padding 在 place 的 它的 backticks
// ([styles.CodespanPadding]), 和 copy 的 selection must reproduce
// original source 文本, 不 rendered blank padding. sentinel 是
// no-break space tagged 用 variation selector, 所以 real no-break space
// 在 消息 文本 does 不 match it 和 是 copied verbatim.
func extractRow(line uv.Line, colStart, colEnd int) string {
	lastCellX := -1
	for x := colStart; x < colEnd; x++ {
		cell := line.At(x)
		if cell != nil && cell.Content != "" {
			lastCellX = x
		}
	}

	var row strings.Builder
	for x := colStart; x <= lastCellX; x++ {
		cell := line.At(x)
		if cell != nil {
			if cell.Content == styles.CodespanPadding {
				row.WriteString("`")
			} else {
				row.WriteString(cell.Content)
			}
		}
	}
	return row.String()
}

// joinRows stitches screen rows back into 文本, deciding per 行 boundary
// whether it was real newline 或 word wrap. Blank rows 是 paragraph
// breaks; otherwise isWordWrap decides.
func joinRows(rows []string, width int) string {
	var sb strings.Builder
	for i, row := range rows {
		text := strings.TrimRight(row, " ")
		sb.WriteString(text)
		if i == len(rows)-1 {
			break
		}

		next := rows[i+1]
		switch {
		case strings.TrimSpace(next) == "":
			// Blank 行: paragraph break.
			sb.WriteString("\n")
		case isWordWrap(text, next, width):
			sb.WriteString(" ")
		default:
			sb.WriteString("\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// isWordWrap reports whether boundary between current 行 和
// next 是 renderer word wrap rather than real newline.
//
// Renderers like glamour word-wrap paragraphs, filling each wrapped 行
// close 到 full width; 行 whose 文本 reaches past wrap
// threshold therefore continues 在 next 行, while shorter 行 ends
// block ( heading, list item, last line 的 paragraph).
// threshold 是 generous because glamour fills wrapped rows 到 roughly 80%
// 或 more 的 width.
//
// Two overrides apply: blank next 行 是 paragraph break (handled 通过
// caller), 和 next 行 那 starts new markdown block — bullet
// 或 heading — always begins 在 它的 own line, even 如果 current 行 是
// full-width ( list item can itself wrap right up 到 width before
// following item).
func isWordWrap(text, next string, width int) bool {
	if startsBlock(next) {
		return false
	}
	return width > 0 && ansi.StringWidth(text) >= width*3/5
}

// startsBlock reports whether 行 begins new markdown block such 作为
// list item 或 heading, rather than continuing wrapped paragraph.
// Indented rows 是 continuations 的 nested content (e.g. second line
// 的 list item), 不 new blocks.
func startsBlock(row string) bool {
	if row != strings.TrimLeft(row, " ") {
		return false
	}
	switch {
	case strings.HasPrefix(row, "- "), strings.HasPrefix(row, "* "),
		strings.HasPrefix(row, "+ "), strings.HasPrefix(row, "• "),
		strings.HasPrefix(row, "#"):
		return true
	}
	if i := strings.IndexAny(row, ".)"); i > 0 && i < 4 {
		for j := range i {
			if row[j] < '0' || row[j] > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// Highlight highlights region 的 文本 within given content 和 region.
func Highlight(content string, area image.Rectangle, startLine, startCol, endLine, endCol int, highlighter Highlighter) string {
	buf := HighlightBuffer(content, area, startLine, startCol, endLine, endCol, highlighter)
	if buf == nil {
		return content
	}
	return buf.Render()
}

// HighlightBuffer highlights region 的 文本 within given content 和
// region, returning a [uv.ScreenBuffer].
func HighlightBuffer(content string, area image.Rectangle, startLine, startCol, endLine, endCol int, highlighter Highlighter) *uv.ScreenBuffer {
	content = stringext.NormalizeSpace(content)

	if startLine < 0 || startCol < 0 {
		return nil
	}

	if highlighter == nil {
		highlighter = DefaultHighlighter
	}

	width, height := area.Dx(), area.Dy()
	buf := uv.NewScreenBuffer(width, height)
	styled := uv.NewStyledString(content)
	styled.Draw(&buf, area)

	// Treat -1 作为 "end 的 content"
	if endLine < 0 {
		endLine = height - 1
	}
	if endCol < 0 {
		endCol = width
	}

	for y := startLine; y <= endLine && y < height; y++ {
		if y >= buf.Height() {
			break
		}

		line := buf.Line(y)

		// Determine column range 为 这 line
		colStart := 0
		if y == startLine {
			colStart = min(startCol, len(line))
		}

		colEnd := len(line)
		if y == endLine {
			colEnd = min(endCol, len(line))
		}

		// Track last non-empty position 作为 we go
		lastContentX := -1

		// Single pass: check content 和 track last non-empty position
		for x := colStart; x < colEnd; x++ {
			cell := line.At(x)
			if cell == nil {
				continue
			}

			// 更新 last content position 如果 non-empty
			if cell.Content != "" && cell.Content != " " {
				lastContentX = x
			}
		}

		// Only apply highlight up 到 last content position
		highlightEnd := colEnd
		if lastContentX >= 0 {
			highlightEnd = lastContentX + 1
		} else if lastContentX == -1 {
			highlightEnd = colStart // No content on this line
		}

		// Apply highlight style 只 到 cells 用 content
		for x := colStart; x < highlightEnd; x++ {
			if !image.Pt(x, y).In(area) {
				continue
			}
			cell := line.At(x)
			if cell != nil {
				highlighter(x, y, cell)
			}
		}
	}

	return &buf
}

// ToHighlighter converts [lipgloss.Style] 到 [Highlighter].
func ToHighlighter(lgStyle lipgloss.Style) Highlighter {
	return func(_ int, _ int, c *uv.Cell) *uv.Cell {
		if c != nil {
			c.Style = ToStyle(lgStyle)
		}
		return c
	}
}

// ToStyle converts inline [lipgloss.Style] 到 [uv.Style].
func ToStyle(lgStyle lipgloss.Style) uv.Style {
	var uvStyle uv.Style

	// Colors 是 already color.Color
	uvStyle.Fg = lgStyle.GetForeground()
	uvStyle.Bg = lgStyle.GetBackground()

	// Build attributes using bitwise OR
	var attrs uint8

	if lgStyle.GetBold() {
		attrs |= uv.AttrBold
	}

	if lgStyle.GetItalic() {
		attrs |= uv.AttrItalic
	}

	if lgStyle.GetUnderline() {
		uvStyle.Underline = uv.UnderlineSingle
	}

	if lgStyle.GetStrikethrough() {
		attrs |= uv.AttrStrikethrough
	}

	if lgStyle.GetFaint() {
		attrs |= uv.AttrFaint
	}

	if lgStyle.GetBlink() {
		attrs |= uv.AttrBlink
	}

	if lgStyle.GetReverse() {
		attrs |= uv.AttrReverse
	}

	uvStyle.Attrs = attrs

	return uvStyle
}

// AdjustArea adjusts given area rectangle 通过 subtracting margins, borders,
// 和 padding 从 style.
func AdjustArea(area image.Rectangle, style lipgloss.Style) image.Rectangle {
	topMargin, rightMargin, bottomMargin, leftMargin := style.GetMargin()
	topBorder, rightBorder, bottomBorder, leftBorder := style.GetBorderTopSize(),
		style.GetBorderRightSize(),
		style.GetBorderBottomSize(),
		style.GetBorderLeftSize()
	topPadding, rightPadding, bottomPadding, leftPadding := style.GetPadding()

	return image.Rectangle{
		Min: image.Point{
			X: area.Min.X + leftMargin + leftBorder + leftPadding,
			Y: area.Min.Y + topMargin + topBorder + topPadding,
		},
		Max: image.Point{
			X: area.Max.X - (rightMargin + rightBorder + rightPadding),
			Y: area.Max.Y - (bottomMargin + bottomBorder + bottomPadding),
		},
	}
}
