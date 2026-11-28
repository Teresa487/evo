//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// Regression 测试 为 windows-latest CI flake where
// TestConfigStore_SetConfigFields_concurrentInProcess failed 用
// "rename ...evo.json.<rand>.tmp ...evo.json: Access 是 denied":
// renaming over destination while another handle 是 open 在 it
// without sharing must 是 retried until handle closes, 不
// surfaced 作为 错误.
func TestAtomicWriteFile_RetriesWhileDestinationHandleOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	require.NoError(t, atomicWriteFile(path, []byte(`{"v":1}`), 0o600))

	// Hold destination open 用 no sharing flags 所以 rename
	// fails 用 ERROR_ACCESS_DENIED/ERROR_SHARING_VIOLATION until
	// handle closes, mimicking antivirus scan 或 exclusive reader.
	p, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	require.NoError(t, err)
	go func() {
		time.Sleep(200 * time.Millisecond)
		windows.CloseHandle(h)
	}()

	require.NoError(t, atomicWriteFile(path, []byte(`{"v":2}`), 0o600))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{"v":2}`, string(data))
}
