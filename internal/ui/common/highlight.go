package common

import (
	"bytes"
	"image/color"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/xchroma"
)

// SyntaxHighlight applies syntax highlighting 到 given source code based
// 在 文件 name 和 background color. 它 returns highlighted code 作为
// string.
func SyntaxHighlight(st *styles.Styles, source, fileName string, bg color.Color) (string, error) {
	// Determine language lexer 到 use. filename match 是 memoized
	// (和 already coalesced) since it 是 expensive 和 stable per name.
	l := xchroma.MatchLexer(fileName)
	if l == nil {
		l = lexers.Analyse(source)
	}
	if l == nil {
		l = lexers.Fallback
	}
	return syntaxHighlight(st, source, l, bg)
}

// SyntaxHighlightLexerName applies syntax highlighting using the lexer
// registered under given language name. 它 falls back 到 fallback
// lexer 当 name 是 unknown, 所以 callers can safely 请求 languages
// like "bash" without checking 为 availability.
func SyntaxHighlightLexerName(st *styles.Styles, source, lexerName string, bg color.Color) (string, error) {
	l := lexers.Get(lexerName)
	if l == nil {
		l = lexers.Fallback
	}
	return syntaxHighlight(st, source, l, bg)
}

// syntaxHighlight formats given source using provided lexer 和
// memoized Chroma style 为 theme 和 background.
func syntaxHighlight(st *styles.Styles, source string, l chroma.Lexer, bg color.Color) (string, error) {
	// Get the formatter
	f := formatters.Get("terminal16m")
	if f == nil {
		f = formatters.Fallback
	}

	// Memoized: building style per 调用 是 expensive 和 只 depends
	// 在 theme 和 background.
	s := ChromaStyle(st, bg)

	// Tokenize 和 format
	it, err := l.Tokenise(nil, source)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = f.Format(&buf, s, it)
	return buf.String(), err
}
