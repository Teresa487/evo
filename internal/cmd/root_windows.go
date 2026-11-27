//go:build windows
// +build windows

package cmd

import (
	"errors"
	"log/slog"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detachProcess starts 服务端 在 new process group 用 console 的
// 它的 own 那 has no 窗口. console programs 服务端 runs (git,
// LSP 和 MCP servers, bash 工具 commands) inherit 那 hidden console.
// DETACHED_PROCESS would leave 服务端 without console, 和 Windows
// gives every console program started 通过 such process new, visible
// console 窗口. 它 also makes Windows ignore CREATE_NO_WINDOW.
func detachProcess(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.CreationFlags = syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW
}

// startDetached starts command newCmd builds, detached 从 这
// process 和 outside job object 这 process 是 在, 如果 any. 一个 child
// stays 在 它的 parent's job unless it breaks away, 和 some jobs kill their
// processes 当 they close: Win32-OpenSSH runs each 会话 在 one, which
// would take 服务端 shared 用 other terminals down 用 会话
// 那 spawned it. 一个 job 那 does 不 allow breakaway makes start fail
// 用 ERROR_ACCESS_DENIED, 和 command 是 then started inside job
// 从 new exec.Cmd, since exec.Cmd cannot 是 started twice.
func startDetached(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	c := newCmd()
	detachProcess(c)
	c.SysProcAttr.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
	err := c.Start()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		slog.Warn("Failed to start process outside the job object, retrying inside it", "error", err)
		c = newCmd()
		detachProcess(c)
		err = c.Start()
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}
