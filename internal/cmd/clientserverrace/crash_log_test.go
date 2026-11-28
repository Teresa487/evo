package clientserverrace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRespawnKeepsPreviousServerOutput guards against spawning 服务端
// truncating previous 服务端's stdout.log 和 stderr.log, which hold
// 只 record 的 crash.
func TestRespawnKeepsPreviousServerOutput(t *testing.T) {
	sb := newSandbox(t, crushBinary(t), "CRUSH_CLIENT_SERVER=1")
	t.Cleanup(func() { shutdownServer(t, sb.socket) })

	// `crush logout` connects 到 服务端 和, unlike `crush 运行`,
	// spawns one 当 none 是 running.
	run := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		c := exec.CommandContext(ctx, sb.bin, "--host", sb.host, "--cwd", sb.project, "logout", "hyper")
		c.Env = sb.env
		out, _ := c.CombinedOutput()
		require.NoError(t, pingHealth(sb.socket), "a server must be running; client output:\n%s", out)
	}

	run()
	serverDirs, err := filepath.Glob(filepath.Join(sb.dir, "xdg_cache_home", "evo", "server-*"))
	require.NoError(t, err)
	require.Len(t, serverDirs, 1)
	shutdownServer(t, sb.socket)
	require.NoFileExists(t, sb.socket)

	logs := map[string]string{
		"stderr.log": "panic: crash of the previous server\n",
		"stdout.log": "output of the previous server\n",
	}
	for name, content := range logs {
		f, err := os.OpenFile(filepath.Join(serverDirs[0], name), os.O_WRONLY|os.O_APPEND, 0)
		require.NoError(t, err)
		_, err = f.WriteString(content)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	run()
	for name, content := range logs {
		got, err := os.ReadFile(filepath.Join(serverDirs[0], name))
		require.NoError(t, err)
		require.Contains(t, string(got), content, "%s was truncated", name)
	}
}
