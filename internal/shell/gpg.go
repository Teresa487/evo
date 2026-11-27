// gpg.go —— GPG 相关命令支持（pinentry）。

package shell

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/crush/internal/pinentry"
	"mvdan.cc/sh/v3/interp"
)

// gpgHandler intercepts top-level gpg commands 运行 从 Crush's shell
// 和 drives them through integrated pinentry runner, 所以
// passphrase/PIN 提示词 是 rendered 通过 TUI instead 的 external
// 终态 pinentry. Commands 那 是 不 credential-relevant pass
// through unchanged; 当 loopback pinentry 是 unavailable (older GPG,
// agent without loopback support, headless 运行) runner itself
// reverts 到 plain gpg 和 existing 终态-handover behavior.
func gpgHandler() execMiddleware {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if !pinentry.ShouldIntercept(args) {
				return next(ctx, args)
			}
			runner := pinentry.DefaultRunner()
			if runner == nil {
				return next(ctx, args)
			}

			hc := interp.HandlerCtx(ctx)
			err := runner.Run(ctx, pinentry.RunOptions{
				Args:   args,
				Env:    execEnvList(hc.Env),
				Dir:    hc.Dir,
				Stdin:  hc.Stdin,
				Stdout: hc.Stdout,
				Stderr: hc.Stderr,
			})
			if err == nil {
				return nil
			}
			// Map runner's errors back 到 interpreter exit
			// statuses 所以 shell behaves 作为 gpg would have.
			var ee *pinentry.ExitError
			switch {
			case errors.Is(err, pinentry.ErrCancelled):
				// Pinentry cancellation 是 GPG's exit code 2.
				return interp.ExitStatus(2)
			case errors.As(err, &ee):
				return interp.ExitStatus(uint8(ee.Code))
			case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
				return err
			default:
				fmt.Fprintln(hc.Stderr, err)
				return interp.ExitStatus(1)
			}
		}
	}
}
