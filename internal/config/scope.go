// scope.go —— 配置作用域（全局/项目）的定义与路径选择。

package config

import "fmt"

// Scope determines which 配置 文件 是 targeted 为 读取/写入 operations.
type Scope int

const (
	// ScopeGlobal targets global data 配置 (~/.local/share/crush/crush.json).
	ScopeGlobal Scope = iota
	// ScopeWorkspace targets 工作区 配置 (.evo/crush.json).
	ScopeWorkspace
)

// String returns human-readable label 为 scope.
func (s Scope) String() string {
	switch s {
	case ScopeGlobal:
		return "global"
	case ScopeWorkspace:
		return "workspace"
	default:
		return fmt.Sprintf("Scope(%d)", int(s))
	}
}

// ErrNoWorkspaceConfig 是 returned 当 工作区-scoped 写入 是
// attempted 在 ConfigStore 那 has no 工作区 配置 路径.
var ErrNoWorkspaceConfig = fmt.Errorf("no workspace config path configured")
