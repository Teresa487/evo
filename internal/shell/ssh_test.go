package shell

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/sshaskpass"
	"github.com/stretchr/testify/require"
)

// configureSSHAskpass enables integrated SSH askpass 用 fake
// SSH_ASKPASS program 和 registers 清理.
func configureSSHAskpass(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("askpass wrapper only runs on POSIX shells")
	}
	t.Cleanup(sshaskpass.DisableIntegration)
	askpass := filepath.Join(t.TempDir(), "ssh-askpass")
	require.NoError(t, os.WriteFile(askpass, []byte("#!/bin/sh\necho secret\n"), 0o755))
	sshaskpass.ConfigureIntegration(sshaskpass.IntegrationConfig{
		AskpassPath: askpass,
		SocketPath:  "/tmp/evo-test.sock",
	})
	return askpass
}

// stripSSHEnv removes askpass-related variables 所以 tests 那 assert
// "no injection" 是 不 polluted 通过 host environment.
func stripSSHEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "SSH_ASKPASS"),
			strings.HasPrefix(kv, "EVO_SSH_SOCK="):
			continue
		}
		out = append(out, kv)
	}
	return out
}

func execStdout(t *testing.T, sh *Shell, command string) string {
	t.Helper()
	stdout, _, err := sh.Exec(context.Background(), command)
	require.NoError(t, err)
	return stdout
}

// TestSSHAskpassEnvInjected proves commands 运行 through shell see
// askpass integration: SSH_ASKPASS program 是 executable 从
// child environment 和 answers, 和 SSH_ASKPASS_REQUIRE 是 forced
// 所以 headless OpenSSH clients always consult it.
func TestSSHAskpassEnvInjected(t *testing.T) {
	configureSSHAskpass(t)

	sh := NewShell(&Options{
		WorkingDir: t.TempDir(),
		Env:        stripSSHEnv(os.Environ()),
	})
	stdout := execStdout(t, sh, `"$SSH_ASKPASS" "deploy@example.com's password: "`)
	require.True(t, strings.Contains(stdout, "secret"), "askpass answer reached the command, got: %s", stdout)
	stdout = execStdout(t, sh, `echo "$SSH_ASKPASS_REQUIRE"`)
	require.True(t, strings.Contains(stdout, "force"), "SSH_ASKPASS_REQUIRE is forced, got: %s", stdout)
}

// TestSSHAskpassEnvNotInjectedWhenDisabled 证明 shell leaves
// askpass variables alone 当 integration 是 off.
func TestSSHAskpassEnvNotInjectedWhenDisabled(t *testing.T) {
	sh := NewShell(&Options{
		WorkingDir: t.TempDir(),
		Env:        stripSSHEnv(os.Environ()),
	})
	stdout := execStdout(t, sh, `echo "${SSH_ASKPASS:-unset}"`)
	require.True(t, strings.Contains(stdout, "unset"), "no askpass program injected, got: %s", stdout)
	stdout = execStdout(t, sh, `echo "${SSH_ASKPASS_REQUIRE:-unset}"`)
	require.True(t, strings.Contains(stdout, "unset"), "no askpass mode injected, got: %s", stdout)
}

// TestSSHAskpassEnvOverridesStaleValues proves values inherited 从
// parent process 是 replaced, 不 appended: child environment must
// carry exactly one SSH_ASKPASS pointing 在 wrapper.
func TestSSHAskpassEnvOverridesStaleValues(t *testing.T) {
	askpass := configureSSHAskpass(t)

	sh := NewShell(&Options{
		WorkingDir: t.TempDir(),
		Env: append(
			append(stripSSHEnv(os.Environ()),
				"SSH_ASKPASS=/nonexistent/stale-askpass",
				"SSH_ASKPASS_REQUIRE=never"),
			"EVO_SSH_SOCK=/nonexistent/old.sock",
		),
	})

	stdout := execStdout(t, sh, `"$SSH_ASKPASS" prompt`)
	require.True(t, strings.Contains(stdout, "secret"), "the fresh askpass is used, got: %s", stdout)

	stdout = execStdout(t, sh, `env`)
	joined := strings.Join(strings.Fields(stdout), "\n")
	require.Equal(t, 1, strings.Count(joined, "SSH_ASKPASS="), "stale SSH_ASKPASS replaced, got: %s", joined)
	require.True(t, strings.Contains(joined, "SSH_ASKPASS="+askpass), "the fresh wrapper path is present, got: %s", joined)
	require.True(t, strings.Contains(joined, "SSH_ASKPASS_REQUIRE=force"), "the stale mode is replaced, got: %s", joined)
	require.True(t, strings.Contains(joined, "EVO_SSH_SOCK=/tmp/evo-test.sock"), "the stale socket is replaced, got: %s", joined)
}
