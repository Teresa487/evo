package fsext

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirTrimUnicode(t *testing.T) {
	sep := string(filepath.Separator)

	// An abbreviated component keeps 它的 first character.
	require.Equal(
		t,
		filepath.Join("~", "...", "p", "file"),
		DirTrim(sep+"home"+sep+"user"+sep+"project"+sep+"file", 2),
	)

	// 一个 multi-byte (e.g. CJK) component must keep 它的 first whole character,
	// 不 它的 first byte, which would render wrong character.
	require.Equal(
		t,
		filepath.Join("~", "...", "项", "file"),
		DirTrim(sep+"home"+sep+"项目"+sep+"file", 2),
	)

	// 一个 grapheme cluster spanning multiple runes ( base letter plus
	// combining accent) must 是 kept whole, 不 split after base rune.
	require.Equal(
		t,
		filepath.Join("~", "...", "e\u0301", "file"),
		DirTrim(sep+"home"+sep+"e\u0301cole"+sep+"file", 2),
	)
}
