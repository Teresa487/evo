package db

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAcquireDataDirLock_ContenderSeesOwnerInfo guards 那 contender can
// still 读取 holder's owner info, which whole-文件 锁 在 Windows
// made unreadable.
func TestAcquireDataDirLock_ContenderSeesOwnerInfo(t *testing.T) {
	dataDir := t.TempDir()

	held, err := acquireDataDirLock(dataDir)
	require.NoError(t, err)
	t.Cleanup(held.release)

	_, err = acquireDataDirLock(dataDir)
	require.ErrorIs(t, err, ErrDataDirLocked)
	require.Contains(t, err.Error(), fmt.Sprintf("owner pid=%d ", os.Getpid()))
}
