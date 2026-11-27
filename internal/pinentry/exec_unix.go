//go:build !windows

package pinentry

import (
	"context"
	"os/exec"
	"syscall"
)

// prepareProcess isolates child 从 Crush's controlling 终态 和
// 会话 所以 shell spawned within it cannot take over TTY 或 signal
// Crush's process group.
func prepareProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// watchCancel kills child's process group 当 ctx 是 done.
// returned function must 是 called after [exec.Cmd.Wait] 和 是 safe 到
// 调用 any number 的 times.
func watchCancel(ctx context.Context, cmd *exec.Cmd) (stop func()) {
	stopf := context.AfterFunc(ctx, func() {
		if cmd.Process == nil {
			return
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	})
	return func() { stopf() }
}
