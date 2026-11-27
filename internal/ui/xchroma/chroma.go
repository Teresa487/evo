package xchroma

import (
	"fmt"
	"image/color"
	"io"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// lexers.Match glob-matches the filename against every registered lexer's
// patterns, which 是 surprisingly expensive (hundreds 的 filepath.Match
// calls per lookup). 结果 为 given filename 是 stable, 所以 we
// memoize it. 这 dominated resize cost 在 large, code-heavy sessions
// where every diff 和 code view re-matched 在 each frame.
var (
	lexerCacheMu sync.RWMutex
	lexerCache   = map[string]chroma.Lexer{}
)

// MatchLexer returns chroma lexer 为 given filename, coalesced 和
// memoized. 它 returns nil 当 no lexer matches (callers can fall back 到
// content analysis). returned lexer 是 safe 到 reuse across calls.
func MatchLexer(fileName string) chroma.Lexer {
	lexerCacheMu.RLock()
	l, ok := lexerCache[fileName]
	lexerCacheMu.RUnlock()
	if ok {
		return l
	}

	l = lexers.Match(fileName)
	if l != nil {
		l = chroma.Coalesce(l)
	}

	lexerCacheMu.Lock()
	lexerCache[fileName] = l
	lexerCacheMu.Unlock()
	return l
}

// Formatter 是 func 那 returns custom formatter 为 Chroma 那 uses
// Lip Gloss 为 foreground styling, while keeping forced background color.
func Formatter(bgColor color.Color, processValue func(string) string) chroma.Formatter {
	return chroma.FormatterFunc(func(w io.Writer, style *chroma.Style, it chroma.Iterator) error {
		for token := it(); token != chroma.EOF; token = it() {
			value := token.Value
			if processValue != nil {
				value = processValue(value)
			}

			entry := style.Get(token.Type)
			if entry.IsZero() {
				if _, err := fmt.Fprint(w, value); err != nil {
					return err
				}
				continue
			}

			s := lipgloss.NewStyle().
				Background(bgColor)

			if entry.Bold == chroma.Yes {
				s = s.Bold(true)
			}
			if entry.Underline == chroma.Yes {
				s = s.Underline(true)
			}
			if entry.Italic == chroma.Yes {
				s = s.Italic(true)
			}
			if entry.Colour.IsSet() {
				s = s.Foreground(lipgloss.Color(entry.Colour.String()))
			}

			if _, err := fmt.Fprint(w, renderLines(s, value)); err != nil {
				return err
			}
		}
		return nil
	})
}

// renderLines styles each line 的 值 separately, joining them 用 plain
// newlines. Tokens may contain trailing 或 embedded newlines (e.g. Chroma's
// CommentSingle includes line terminator), 和 rendering multi-line
// string through Lip Gloss makes it equalize line widths, padding the line
// after comment 用 stray spaces 和 corrupting 它的 indentation.
func renderLines(s lipgloss.Style, value string) string {
	if !strings.Contains(value, "\n") {
		return s.Render(value)
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = s.Render(line)
	}
	return strings.Join(lines, "\n")
}
