//go:build !windows

package cmd

import (
	"os/exec"
	"syscall"
)

func detachProcess(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.Setsid = true
}

// startDetached starts command newCmd builds, detached 从 这
// process.
func startDetached(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	c := newCmd()
	detachProcess(c)
	if err := c.Start(); err != nil {
		return nil, err
	}
	return c, nil
}
