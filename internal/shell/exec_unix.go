//go:build !windows

package shell

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/interp"
)

// defaultKillTimeout matches mvdan's DefaultExecHandler 默认. Extracted
// 所以 coupling 到 upstream 是 explicit rather than buried literal.
const defaultKillTimeout = 2 * time.Second

// isolateProcess sets SysProcAttr 所以 child runs 在 它的 own 会话,
// fully detached 从 Crush's controlling 终态. 这 prevents shells
// like zsh 从 grabbing TTY 为 job control, which would cause
// "suspended (tty input)" 和 corrupt Bubble Tea's 终态 rendering.
// 它 also prevents child processes 从 sending signals 到 Crush's process
// group.
func isolateProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// killGroupOnCancel replaces os/exec's 默认 cancellation (which signals
// 只 direct child) 用 SIGKILL 到 child's whole process group,
// 所以 descendants like script's background children die 用 it.
// WaitDelay bounds cmd.Wait 当 detached descendant still holds stdio
// pipe open after process exits. Requires isolateProcess 所以 child
// leads 它的 own group (pgid == pid).
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = defaultKillTimeout + time.Second
}

// processGroupExecHandler returns ExecHandlerFunc 那 replaces
// interp.DefaultExecHandler 用 one 那 fully isolates child processes
// 从 Crush's 会话 和 controlling 终态.
//
// Without 这, shells like zsh 那 enable job control 当 sourcing
// framework files will attempt 到 take over TTY, causing SIGTTIN/SIGTTOU
// signals 和 corrupting parent 终态 state.
//
// implementation mirrors interp.DefaultExecHandler 用 two additions:
// isolateProcess(&cmd) after construction, 和 negative-PID signal targeting
// 在 cancellation callback 所以 entire child process group 是 killed.
func processGroupExecHandler(killTimeout time.Duration) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)
		path, err := interp.LookPathDir(hc.Dir, hc.Env, args[0])
		if err != nil {
			fmt.Fprintln(hc.Stderr, err)
			return interp.ExitStatus(127)
		}

		cmd := exec.Cmd{
			Path:   path,
			Args:   args,
			Env:    execEnvList(hc.Env),
			Dir:    hc.Dir,
			Stdin:  hc.Stdin,
			Stdout: hc.Stdout,
			Stderr: hc.Stderr,
		}
		isolateProcess(&cmd)

		// Bound how long Wait blocks after the process exits: a detached
		// descendant holding a stdio pipe open would otherwise hang us
		// forever. Matches mvdan's DefaultExecHandler.
		cmd.WaitDelay = killTimeout + time.Second

		err = cmd.Start()
		if err == nil {
			group := -cmd.Process.Pid
			leaderDone := make(chan struct{})
			cleanupDone := make(chan struct{})
			stop := context.AfterFunc(ctx, func() {
				defer close(cleanupDone)
				if killTimeout <= 0 {
					_ = syscall.Kill(group, syscall.SIGKILL)
					return
				}

				if syscall.Kill(group, syscall.SIGINT) == syscall.ESRCH {
					return
				}
				timer := time.NewTimer(killTimeout)
				defer timer.Stop()
				select {
				case <-leaderDone:
					if syscall.Kill(group, 0) == syscall.ESRCH {
						return
					}
					<-timer.C
				case <-timer.C:
				}
				_ = syscall.Kill(group, syscall.SIGKILL)
			})

			err = cmd.Wait()
			close(leaderDone)
			// leader can exit 在 SIGINT while descendants 是 still
			// alive. Keep shell tracked until escalation has 运行.
			if !stop() {
				<-cleanupDone
			}
		}

		return exitStatusFromError(ctx, hc.Stderr, err)
	}
}

// exitStatusFromError translates exec 错误 into interp exit status,
// matching conventions 的 interp.DefaultExecHandler. Extracted 所以
// platform-specific exec 处理器 stays focused 在 isolation mechanics.
func exitStatusFromError(ctx context.Context, stderr io.Writer, err error) error {
	if err == nil {
		return nil
	}
	switch err := err.(type) {
	case *exec.ExitError:
		if status, ok := err.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return interp.ExitStatus(128 + uint8(status.Signal()))
		}
		return interp.ExitStatus(uint8(err.ExitCode()))
	case *exec.Error:
		fmt.Fprintf(stderr, "%v\n", err)
		return interp.ExitStatus(127)
	default:
		return err
	}
}
