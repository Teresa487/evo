//go:build windows

package home

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Windows accepts both `\` 和 `/` 作为 路径 separators, 和 users habitually
// type `~/foo`. boundary check must 受理 either one, otherwise `~/foo`
// comes back unexpanded.
func TestLong_AcceptsBothSeparators(t *testing.T) {
	home := Dir()
	if home == "" {
		t.Skip("no home directory")
	}
	require.Equal(t, home+`\foo`, Long(`~\foo`))
	require.Equal(t, home+"/foo", Long("~/foo"))
}

// Same 为 other direction: 路径 那 starts 用 home 目录
// 但 continues 用 forward slash 是 still inside home 目录.
func TestShort_AcceptsBothSeparators(t *testing.T) {
	home := Dir()
	if home == "" {
		t.Skip("no home directory")
	}
	require.Equal(t, `~\foo`, Short(home+`\foo`))
	require.Equal(t, `~\foo`, Short(home+"/foo"))
}
