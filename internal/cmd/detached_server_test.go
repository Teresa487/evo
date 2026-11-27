package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDetachedServerCmd_RunsFromServerDir guards long-lived 服务端
// inheriting, 和 所以 pinning, spawning 客户端's working 目录.
func TestDetachedServerCmd_RunsFromServerDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.Equal(t, dir, detachedServerCmd("crush", dir, "server").Dir)
}

// TestDetachedServerCmd_MarksItSpawned guards marker 那 tells
// spawned 服务端 到 keep 它的 空闲 shutdown, 所以 it cannot outlive
// 客户端 那 started it. 一个 服务端 用户 runs explicitly has no such
// marker 和 stays up 当 空闲.
func TestDetachedServerCmd_MarksItSpawned(t *testing.T) {
	t.Setenv(spawnedServerEnv, "")

	c := detachedServerCmd("crush", t.TempDir(), "server")
	require.Contains(t, c.Env, spawnedServerEnv+"=1",
		"a spawned server must be marked so it still shuts down when idle")
}
