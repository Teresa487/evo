package cmd

import (
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// promptTestCmd parses args against a fresh command carrying the same
// --提示词 flag 作为 rootCmd, 所以 tests never share mutable cobra state.
func promptTestCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "crush"}
	addPromptFlag(cmd.Flags())
	require.NoError(t, cmd.ParseFlags(args))
	return cmd
}

// flag must 是 persistent 所以 `crush 运行 --提示词 ...` accepts it.
func TestPromptFlagAvailableOnRunCmd(t *testing.T) {
	t.Parallel()

	flag := runCmd.Flags().Lookup("prompt")
	require.NotNil(t, flag, "--prompt must be available on `evo run`")
	require.Equal(t, "stringArray", flag.Value.Type())
}

func TestPromptOverridesNone(t *testing.T) {
	t.Parallel()

	prompts, err := promptOverrides(promptTestCmd(t))
	require.NoError(t, err)
	require.Nil(t, prompts)
}

func TestPromptOverridesRepeatable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	coder := filepath.Join(dir, "coder.md.tpl")
	summary := filepath.Join(dir, "a,b.md")

	prompts, err := promptOverrides(promptTestCmd(t,
		"--prompt", "coder="+coder,
		"--prompt", "summary="+summary,
	))
	require.NoError(t, err)
	require.Equal(t, config.PromptPaths{"coder": coder, "summary": summary}, prompts)
}

// Relative paths 是 pinned 到 shell's cwd before they reach 服务端
// 那 runs somewhere else.
func TestPromptOverridesMakesPathsAbsolute(t *testing.T) {
	t.Parallel()

	prompts, err := promptOverrides(promptTestCmd(t, "--prompt", "coder=coder.md.tpl"))
	require.NoError(t, err)
	want, err := filepath.Abs("coder.md.tpl")
	require.NoError(t, err)
	require.Equal(t, want, prompts["coder"])
}

func TestPromptOverridesRejectsBadInput(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ arg, err string }{
		{"coder", "expected name=path"},
		{"=coder.md", "expected name=path"},
		{"coder=", "expected name=path"},
		{"codr=coder.md", `unknown prompt "codr"`},
	} {
		_, err := promptOverrides(promptTestCmd(t, "--prompt", tc.arg))
		require.ErrorContains(t, err, tc.err, tc.arg)
	}
}

// TestResolveFlagPaths_BeforeCwd covers relative unix --host 和
// --提示词 being resolved against --cwd, which command changes into,
// instead 的 against 目录 evo was started 在.
func TestResolveFlagPaths_BeforeCwd(t *testing.T) {
	// Not parallel: it changes 目录 和 swaps package var.
	start, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(start)
	origHost := clientHost
	clientHost = "unix://evo.sock"
	t.Cleanup(func() { clientHost = origHost })

	cmd := promptTestCmd(t, "--prompt", "coder=./coder.md.tpl")
	require.NoError(t, resolveFlagPaths(cmd))
	t.Chdir(t.TempDir())

	require.Equal(t, "unix://"+filepath.Join(start, "evo.sock"), clientHost)
	prompts, err := promptOverrides(cmd)
	require.NoError(t, err)
	require.Equal(t, config.PromptPaths{"coder": filepath.Join(start, "coder.md.tpl")}, prompts)
}
