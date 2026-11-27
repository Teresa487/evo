package fsext

import (
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Expand 是 wrapper around [expand.Literal]. 它 will escape input
// string, expand any shell symbols (such 作为 '~') 和 resolve any environment
// variables.
func Expand(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	s = filepath.ToSlash(s)
	p := syntax.NewParser()
	word, err := p.Document(strings.NewReader(s))
	if err != nil {
		return "", err
	}
	cfg := &expand.Config{
		Env:      expand.FuncEnviron(os.Getenv),
		ReadDir2: os.ReadDir,
		GlobStar: true,
	}
	return expand.Literal(cfg, word)
}
