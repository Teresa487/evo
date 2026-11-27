//go:build windows

package shell

import (
	"os/exec"
	"time"

	"mvdan.cc/sh/v3/interp"
)

// defaultKillTimeout matches mvdan's DefaultExecHandler 默认.
const defaultKillTimeout = 2 * time.Second

// isolateProcess 是 no-op 在 Windows. 会话 isolation via Setsid 是
// Unix-只 concept; Windows uses CREATE_NEW_PROCESS_GROUP which mvdan's
// 默认 处理器 already handles adequately.
func isolateProcess(_ *exec.Cmd) {}

// killGroupOnCancel 只 sets WaitDelay 在 Windows; negative-PID signal
// targeting 是 Unix-只, 所以 cancellation keeps 默认 process kill.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.WaitDelay = defaultKillTimeout + time.Second
}

// processGroupExecHandler returns interp.DefaultExecHandler 在 Windows.
func processGroupExecHandler(killTimeout time.Duration) interp.ExecHandlerFunc {
	return interp.DefaultExecHandler(killTimeout)
}
