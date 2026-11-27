package clientserverrace_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// crushBinary builds crush, skipping where 二进制 tests cannot 运行.
func crushBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping binary test in -short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("skipping: needs unix sockets and signals")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("skipping: 'go' not available on PATH")
	}
	return buildCrushBinary(t, repoRootFromTest(t))
}

// sandbox keeps crush processes away 从 用户's real state, 提供方
// credentials 和 默认 socket.
type sandbox struct {
	bin, dir, project, socket, host string
	env                             []string
}

func newSandbox(t *testing.T, bin string, env ...string) *sandbox {
	t.Helper()
	// 一个 /tmp 路径 keeps socket under darwin's 104-byte sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "crush-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sb := &sandbox{
		bin:     bin,
		dir:     dir,
		project: filepath.Join(dir, "project"),
		socket:  filepath.Join(dir, "crush.sock"),
	}
	sb.host = "unix://" + sb.socket
	require.NoError(t, os.MkdirAll(sb.project, 0o700))
	sb.env = []string{"CRUSH_DISABLE_METRICS=1", "CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1"}
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		d := filepath.Join(dir, strings.ToLower(name))
		require.NoError(t, os.MkdirAll(d, 0o700))
		sb.env = append(sb.env, name+"="+d)
	}
	for _, name := range []string{"PATH", "TMPDIR", "USER"} {
		if v, ok := os.LookupEnv(name); ok {
			sb.env = append(sb.env, name+"="+v)
		}
	}
	sb.env = append(sb.env, env...)
	return sb
}

// proc 是 crush process started 通过 [sandbox.start].
type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // Set once done is closed.
}

// start runs crush 用 args, logging 到 name.log. 测试's 上下文 kills
// process 当 测试 ends, 和 清理 reaps it.
func (sb *sandbox) start(t *testing.T, name string, args ...string) *proc {
	t.Helper()
	log, err := os.Create(filepath.Join(sb.dir, name+".log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })

	cmd := exec.CommandContext(t.Context(), sb.bin, args...)
	cmd.Env = sb.env
	cmd.Stdout = log
	cmd.Stderr = log
	require.NoError(t, cmd.Start())
	p := &proc{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() { <-p.done })
	return p
}

// output returns what named process has logged 所以 far.
func (sb *sandbox) output(name string) string {
	out, _ := os.ReadFile(filepath.Join(sb.dir, name+".log"))
	return string(out)
}
