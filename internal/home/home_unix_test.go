//go:build !windows

package home

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// On Unix backslash 是 ordinary filename character, 所以 it must 不 是
// treated 作为 路径 boundary. `~\foo` 是 文件 literally named `~\foo`, 和
// Dir()+`\foo` 是 sibling 的 home 目录, 不 child 的 it.
func TestBackslashIsNotAPathBoundaryOnUnix(t *testing.T) {
	home := Dir()
	if home == "" {
		t.Skip("no home directory")
	}
	require.Equal(t, `~\foo`, Long(`~\foo`))
	require.Equal(t, home+`\foo`, Short(home+`\foo`))
}
