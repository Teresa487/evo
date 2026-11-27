//go:build windows

package mcp

import "os/exec"

// configureStdioProcess 是 no-op 在 platforms without POSIX process groups.
// os/exec's 默认 上下文 cancellation still kills direct child.
func configureStdioProcess(cmd *exec.Cmd) {}
