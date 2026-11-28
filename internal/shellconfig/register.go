// Package shellconfig implements Bash-powered 配置 format 为 Crush.
//
// 它 provides shell builtins (提供方, 模型, mcp, lsp, permissions, 钩子,
// option) 那 populate 配置 通过 mutating ConfigBuilder
// 存储 在 shell 上下文. builtins 是 registered 在 init time via
// shell.RegisterBuiltin 和 是 gated 通过 ConfigBuilder 在 上下文 —
// they 是 no-ops during normal bash 工具 execution.
//
// 这 package sits between shell 和 配置: it imports shell (为
// RegisterBuiltin 和 运行), 和 配置 imports shellconfig 到 运行 evorc
// files.
package shellconfig

import (
	"fmt"
	"io"

	"github.com/charmbracelet/crush/internal/shell"
)

func init() {
	shell.RegisterBuiltin("provider", handleProvider)
	shell.RegisterBuiltin("model", handleModel)
	shell.RegisterBuiltin("mcp", handleMCP)
	shell.RegisterBuiltin("lsp", handleLSP)
	shell.RegisterBuiltin("permissions", handlePermissions)
	shell.RegisterBuiltin("hook", handleHook)
	shell.RegisterBuiltin("option", handleOption)
}

// usage prints usage 消息 到 stderr 和 returns 错误.
func usage(stderr io.Writer, msg string) error {
	fmt.Fprintln(stderr, msg)
	return fmt.Errorf("%s", msg)
}

// appendArr appends 值 到 string slice 存储 在 m[key], creating it 如果
// needed, 和 returns 结果.
func appendArr(m map[string]any, key, value string) []any {
	arr, _ := m[key].([]any)
	return append(arr, value)
}
