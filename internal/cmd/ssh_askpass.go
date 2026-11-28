// ssh_askpass.go —— SSH askpass 辅助进程入口。

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/crush/internal/sshaskpass"
	"github.com/spf13/cobra"
)

// sshAskpassCmd 是 hidden subcommand 那 OpenSSH invokes 作为 它的
// SSH_ASKPASS program (via wrapper script pointed 到 通过
// environment-scoped SSH_ASKPASS variable). 它 forwards 提示词 到
// host Crush process's 提示词 socket 所以 question 是 rendered 通过
// TUI, unless driver like sshpass owns 终态 为 这 ssh,
// 和 prints answer ( secret,
// 或 "yes" 为 confirmations) 在 stdout. No ssh configuration 或 files
// 是 modified.
var sshAskpassCmd = &cobra.Command{
	Use:    "__ssh-askpass",
	Short:  "Internal: ssh askpass for the integrated SSH prompts",
	Hidden: true,
	// Disable flag parsing: 提示词 是 passed 作为 positional 文本.
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSSHAskpass(cmd.Context(), args)
	},
}

func init() {
	rootCmd.AddCommand(sshAskpassCmd)
}

// runSSHAskpass handles single askpass invocation. OpenSSH execs 这
// 用 提示词 文本 作为 它的 argument 和 expects secret (或
// answer such 作为 "yes") 在 stdout.
func runSSHAskpass(ctx context.Context, args []string) error {
	prompt := strings.Join(args, " ")
	secret, err := sshaskpass.Answer(ctx, prompt, os.Getenv("EVO_SSH_SOCK"))
	if err != nil {
		// OpenSSH aborts authentication attempt 当 askpass exits
		// non-zero, which 是 correct outcome 为 cancelled 或
		// unanswered 提示词.
		return fmt.Errorf("crush: ssh askpass: %w", err)
	}

	fmt.Fprintln(os.Stdout, secret)
	return nil
}
