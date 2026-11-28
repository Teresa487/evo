//go:build windows

package server

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// TestDefaultHost_SID guards against users sharing machine-wide 默认
// pipe: it must 是 named after process owner's SID.
func TestDefaultHost_SID(t *testing.T) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	require.Equal(t, "npipe:////./pipe/evo-"+tu.User.Sid.String()+".sock", DefaultHost())
}
