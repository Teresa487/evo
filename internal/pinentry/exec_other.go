//go:build windows

package pinentry

import (
	"context"
	"os/exec"
)

// prepareProcess 是 no-op 在 Windows: process-group isolation 是 不
// applicable, 和 interactive 终态 handover 为 GPG dialogs 是
// POSIX concern.
func prepareProcess(cmd *exec.Cmd) {}

// watchCancel terminates child 当 ctx 是 done using 上下文
// cancellation mechanism, since Windows has no process groups here.
func watchCancel(ctx context.Context, cmd *exec.Cmd) (stop func()) {
	stopf := context.AfterFunc(ctx, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return func() { stopf() }
}
