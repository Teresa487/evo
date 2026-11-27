//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd

package pinentry

// procExeSupported reports whether /proc/<pid>/exe can 是 读取 到 resolve
// process executable 当 process 表 does 不 provide 路径.
const procExeSupported = false

// BeginHandover 是 no-op without POSIX 终态 到 hand over.
func BeginHandover(bool) {}

// EndHandover 是 no-op without POSIX 终态 到 hand over.
func EndHandover() {}
