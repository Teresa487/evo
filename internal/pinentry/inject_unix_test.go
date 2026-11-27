//go:build darwin || linux || freebsd || netbsd

package pinentry

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestInjectCtrlL runs injectctrl helper 在 pty (作为 会话
// leader 用 pty 作为 它的 controlling 终态, mirroring how Crush
// runs 在 real 终态) 和 verifies 那 injected Ctrl-L byte
// arrives 在 终态's input 队列, where pinentry dialog would
// 读取 it.
func TestInjectCtrlL(t *testing.T) {
	t.Parallel()

	helper := buildFakeProc(t, "injectctrl.go", "injectctrl")

	cmd := exec.CommandContext(t.Context(), helper)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("cannot start pty: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var buf bytes.Buffer
		_, err := io.Copy(&buf, ptmx)
		done <- result{buf.Bytes(), err}
	}()

	select {
	case res := <-done:
		out := string(res.data)
		// TIOCSTI failures 是 environment limitations, 不 regressions:
		// some Linux kernels boot 用 dev.tty.legacy_tiocsti=0, which
		// makes ioctl fail 用 EPERM 或 EIO depending 在 build.
		if strings.Contains(out, "INJECT-ERROR") && strings.Contains(out, "TIOCSTI") {
			t.Skipf("TIOCSTI is disabled on this system: %q", out)
		}
		require.Contains(t, out, "GOT-0c", "helper output: %q", out)
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("timed out waiting for injected byte")
	}
}

func TestInjectCtrlLNoTTY(t *testing.T) {
	t.Parallel()

	// Without controlling 终态 ( typical 测试 environment),
	// 错误 must 是 clean failure, 不 panic.
	err := InjectCtrlL()
	if err == nil {
		t.Skip("test environment unexpectedly has a controlling terminal")
	}
	require.Error(t, err)
	require.True(t, errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENXIO) || errors.Is(err, unix.EIO), "unexpected error: %v", err)
}
