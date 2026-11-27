package shell

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/charmbracelet/crush/internal/pinentry"
	"github.com/stretchr/testify/require"
)

// fakeGPGShim installs pinentry package's fake gpg 作为 "gpg" 在
// 路径 的 shell 和 returns 它的 路径.
func fakeGPGShim(t *testing.T) (path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gpg shim only runs on POSIX shells")
	}
	data, err := os.ReadFile(filepath.Join("..", "pinentry", "testdata", "fakegpg.sh"))
	require.NoError(t, err)
	path = filepath.Join(t.TempDir(), "gpg")
	require.NoError(t, os.WriteFile(path, data, 0o755))
	return path
}

// configureIntegration enables the integrated pinentry against the fake
// gpg shim 和 默认 提示词 service, 和 registers 清理.
func configureIntegration(t *testing.T, gpgPath string) {
	t.Helper()
	t.Cleanup(pinentry.DisableIntegration)
	pinentry.ConfigureIntegration(pinentry.IntegrationConfig{
		Prompter: func(ctx context.Context, req pinentry.PromptRequest) (string, error) {
			return pinentry.DefaultPrompts().Prompt(ctx, req)
		},
		GPGPath: gpgPath,
	})
}

// answerPrompts responds 到 every 提示词 请求 从 默认
// 提示词 service 用 given secret.
func answerPrompts(t *testing.T, secret string) {
	t.Helper()
	svc := pinentry.DefaultPrompts()
	ch := svc.Subscribe(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			close(done)
		}
	})
	go func() {
		for {
			select {
			case <-done:
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				svc.Respond(ev.Payload.ID, secret)
			}
		}
	}()
}

func TestGpgHandlerInterceptsCredentialOperations(t *testing.T) {
	gpgPath := fakeGPGShim(t)
	configureIntegration(t, gpgPath)
	answerPrompts(t, "secret")

	sh := NewShell(&Options{
		WorkingDir: t.TempDir(),
		Env:        append(os.Environ(), "FAKE_GPG_MODE=locked"),
	})
	// pipeline gives gpg closed stdin instead 的 测试's
	// 终态, 和 --detach-sign marks credential-relevant operation.
	stdout, stderr, err := sh.Exec(context.Background(), "echo data | gpg --detach-sign")
	require.NoError(t, err, "stdout: %s stderr: %s", stdout, stderr)
}

func TestGpgHandlerPassthroughWhenDisabled(t *testing.T) {
	gpgPath := fakeGPGShim(t)
	// Integration disabled: command runs plain fake gpg, 用 no
	// loopback flags 或 提示词 involved.
	t.Setenv("PATH", filepath.Dir(gpgPath)+string(os.PathListSeparator)+os.Getenv("PATH"))

	sh := NewShell(&Options{
		WorkingDir: t.TempDir(),
		Env:        append(os.Environ(), "FAKE_GPG_MODE=locked"),
	})
	stdout, _, err := sh.Exec(context.Background(), "echo data | gpg --detach-sign")
	require.NoError(t, err)
	require.Equal(t, "plain\ndata\n", stdout)
}

func TestGpgHandlerFallsBackOnUnsupportedLoopback(t *testing.T) {
	gpgPath := fakeGPGShim(t)
	configureIntegration(t, gpgPath)

	sh := NewShell(&Options{
		WorkingDir: t.TempDir(),
		Env:        append(os.Environ(), "FAKE_GPG_MODE=unsupported"),
	})
	// Unsupported loopback: runner reverts 到 plain gpg, which still
	// gets the piped input the probe consumed.
	stdout, _, err := sh.Exec(context.Background(), "echo data | gpg --detach-sign")
	require.NoError(t, err)
	require.Equal(t, "plain\ndata\n", stdout)
}

func TestGpgHandlerKeepsOperandsAndBlockList(t *testing.T) {
	gpgPath := fakeGPGShim(t)
	configureIntegration(t, gpgPath)
	answerPrompts(t, "secret")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("data"), 0o644))
	sh := NewShell(&Options{
		WorkingDir: dir,
		Env:        append(os.Environ(), "FAKE_GPG_MODE=locked"),
	})
	// 一个 文件 operand must 不 swallow loopback flags.
	_, stderr, err := sh.Exec(context.Background(), "gpg --clearsign f.txt")
	require.NoError(t, err, "stderr: %s", stderr)

	// Deny rules still apply 到 gpg commands runner drives.
	blocked := NewShell(&Options{
		WorkingDir: dir,
		Env:        append(os.Environ(), "FAKE_GPG_MODE=locked"),
		BlockFuncs: []BlockFunc{CommandsBlocker([]string{"gpg"})},
	})
	_, _, err = blocked.Exec(context.Background(), "gpg --clearsign f.txt")
	require.Error(t, err)
}

func TestGpgHandlerLeavesNonCredentialGpgAlone(t *testing.T) {
	gpgPath := fakeGPGShim(t)
	configureIntegration(t, gpgPath)
	var prompted atomic.Bool
	svc := pinentry.DefaultPrompts()
	ch := svc.Subscribe(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-done:
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				prompted.Store(true)
				svc.Respond(ev.Payload.ID, "secret")
			}
		}
	}()

	t.Setenv("PATH", filepath.Dir(gpgPath))
	sh := NewShell(&Options{
		WorkingDir: t.TempDir(),
		Env:        append(os.Environ(), "FAKE_GPG_MODE=cached"),
	})
	// --list-keys 是 不 credential operation: runs plain 二进制.
	_, _, err := sh.Exec(context.Background(), "gpg --list-keys")
	require.NoError(t, err)
	require.False(t, prompted.Load(), "non-credential gpg must not prompt")
}
