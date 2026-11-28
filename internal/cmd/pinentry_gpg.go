// pinentry_gpg.go —— GPG pinentry 辅助，用于凭据输入。

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/charmbracelet/crush/internal/pinentry"
	"github.com/spf13/cobra"
)

// pinentryGpgCmd 是 hidden subcommand 那 git invokes 作为 它的 gpg
// program (via wrapper script pointed 到 通过 environment-scoped
// gpg.program 配置). 它 runs real gpg through loopback pinentry
// 和 collects passphrase/PIN over host Crush process's 提示词
// socket, 所以 提示词 是 rendered 通过 TUI instead 的 external
// 终态 pinentry. No GPG 或 git configuration 是 modified.
var pinentryGpgCmd = &cobra.Command{
	Use:    "__pinentry-gpg",
	Short:  "Internal: gpg wrapper for the integrated pinentry",
	Hidden: true,
	// Disable flag parsing: every argument belongs 到 gpg.
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPinentryGpg(cmd.Context(), args)
	},
}

func init() {
	rootCmd.AddCommand(pinentryGpgCmd)
}

func runPinentryGpg(ctx context.Context, args []string) error {
	// Prepend gpg argv 所以 runner sees full command line.
	gpgArgs := append([]string{"gpg"}, args...)

	socketPath := os.Getenv("EVO_PINENTRY_SOCK")
	if socketPath == "" {
		return runPlainGpg(ctx, gpgArgs)
	}

	// When loopback pinentry 是 unavailable (old GPG, agent without
	// loopback support) runner reverts 到 plain gpg itself, which
	// triggers 终态-handover watcher.
	runner := &pinentry.Runner{
		Prompter: pinentry.SocketPrompter(socketPath),
	}
	return exitWithStatus(runner.Run(ctx, pinentry.RunOptions{
		Args:   gpgArgs,
		Env:    os.Environ(),
		Dir:    cwd(),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}))
}

// runPlainGpg execs gpg 作为-是, preserving stdin/stdout/stderr 和
// exit status.
func runPlainGpg(ctx context.Context, args []string) error {
	bin, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// exitWithStatus maps runner's errors 到 process exit codes 所以 git
// sees the same status gpg would have produced.
func exitWithStatus(err error) error {
	if err == nil {
		return nil
	}
	var ee *pinentry.ExitError
	switch {
	case errors.Is(err, pinentry.ErrCancelled):
		os.Exit(2)
	case errors.As(err, &ee):
		os.Exit(ee.Code)
	default:
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return nil
}

// cwd returns current working 目录 或 ".".
func cwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}
