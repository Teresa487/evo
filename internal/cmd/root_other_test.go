//go:build !windows

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/server"
	"github.com/stretchr/testify/require"
)

// fakeServerPIDFileEnv makes 测试 二进制, re-executed 作为 服务端,
// 写入 它的 pid 到 named 文件 和 exit.
const fakeServerPIDFileEnv = "CRUSH_TEST_FAKE_SERVER_PID_FILE"

func TestMain(m *testing.M) {
	if pidFile := os.Getenv(fakeServerPIDFileEnv); pidFile != "" {
		if os.WriteFile(pidFile+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600) != nil ||
			os.Rename(pidFile+".tmp", pidFile) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestStartDetachedServerReapsExitedServer guards 服务端 那 exits before
// 它的 客户端 lingering 作为 zombie until 客户端 exits.
func TestStartDetachedServerReapsExitedServer(t *testing.T) {
	// Not parallel: it sets env vars.
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "server.pid")
	t.Setenv(fakeServerPIDFileEnv, pidFile)
	t.Setenv("CRUSH_CACHE_DIR", filepath.Join(dir, "cache"))
	hostURL, err := server.ParseHostURL("unix://" + filepath.Join(dir, "crush.sock"))
	require.NoError(t, err)
	_, err = startDetachedServer(commandWithContext(t.Context()), hostURL)
	require.NoError(t, err)

	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(string(data))
		return err == nil && pid > 0
	}, 10*time.Second, 10*time.Millisecond, "the spawned server never started")
	// Signal 0 fails 用 ESRCH 只 once process has been reaped.
	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}, 10*time.Second, 10*time.Millisecond, "server %d was not reaped", pid)
}
