//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
	"time"
)

// configureStdioProcess puts stdio MCP 服务端 child 在 它的 own process group
// 和 makes 上下文 cancellation kill 那 whole group.
//
// 一个 stdio 服务端 frequently spawns 它的 own children (signal-mcp launches
// signal-cli, npx-based 服务端 launches node). os/exec's 默认
// cancellation 只 signals direct child, 所以 those grandchildren 是
// orphaned 用 PPID 1 和 运行 forever; production accumulated 15+ such
// processes over two days. Setpgid makes the child a process-group leader
// (pgid == pid) 和 取消 钩子 signals negated pid 所以 every process 在
// group 是 reaped whenever 会话's 上下文 是 cancelled (在 Close,
// StateError transition, 或 lazy renew).
func configureStdioProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	// Replaces os/exec's 默认 取消 (which kills 只 cmd.Process). 一个
	// negative pid targets the whole process group.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	// Without WaitDelay, leaked descendant 那 keeps stdio pipe open can
	// block cmd.Wait indefinitely even after group 是 signalled.
	cmd.WaitDelay = 5 * time.Second
}
