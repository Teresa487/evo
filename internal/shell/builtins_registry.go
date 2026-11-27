// builtins_registry.go —— 内置命令（jq、gpg 等）的注册表。

package shell

import (
	"context"
	"io"
)

// BuiltinHandler 是 function 那 handles shell builtin command.
// 它 receives full args slice (including command name 作为 args[0]),
// 上下文 (which may carry ConfigBuilder), 和 I/O streams.
type BuiltinHandler func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error

// builtins holds all 在-process builtin handlers, including jq (registered
// 在 这 package) 和 配置 builtins (registered 通过 shellconfig via
// RegisterBuiltin 到 avoid import cycle). Dispatched 通过 builtinHandler 在
// 运行.go.
var builtins = make(map[string]BuiltinHandler)

func init() {
	RegisterBuiltin("jq", handleJQ)
}

// RegisterBuiltin registers builtin command 处理器. Must 是 called during
// init(), before any shell execution. If 处理器 是 already registered 为
// the same name, the new one replaces it.
func RegisterBuiltin(name string, handler BuiltinHandler) {
	builtins[name] = handler
}
