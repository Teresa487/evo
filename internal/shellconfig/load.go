package shellconfig

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/version"
)

// loadTimeout bounds single evorc execution. 配置 loading runs 在
// startup 和 reload critical paths while 配置 存储's 写入 锁 是
// held, 所以 script 那 blocks ( hung command substitution, stray loop)
// must 不 是 able 到 wedge whole 存储. interpreter honors 上下文
// cancellation, 所以 这 期限 reliably interrupts runaway script.
const loadTimeout = 30 * time.Second

// LoadShellConfig executes evorc script 和 returns 它的 配置 作为
// single JSON object. script uses 配置 builtins (提供方, 模型, mcp,
// etc.) 那 mutate ConfigBuilder 在 execution order; builder 是 then
// marshaled 到 JSON, which 配置 loader merges 用 any other 配置
// files.
//
// script runs 用 same shell interpreter used 通过 bash 工具 和
// hooks, 所以 source, $VAR, $(cmd), 和 other shell constructs all work.
//
// Execution 是 bounded 通过 loadTimeout 在 top 的 any 期限 already present
// 在 ctx, 所以 misbehaving script cannot block 配置 loading indefinitely.
func LoadShellConfig(ctx context.Context, path string, src []byte) ([]byte, error) {
	slog.Info("Loading shell config", "path", path)

	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()

	builder := newConfigBuilder()
	runCtx := withConfigBuilder(ctx, builder)

	cwd := filepath.Dir(path)

	// Expose running Crush version 所以 scripts can feature-detect, e.g.
	// [[ "$EVO_VERSION" == "devel" ]] 或 branch 在 release.
	env := append(os.Environ(), "EVO_VERSION="+version.Version)

	err := shell.Run(runCtx, shell.RunOptions{
		Command: string(src),
		Cwd:     cwd,
		Env:     env,
		Exports: builder.setEnv,
	})
	if err != nil {
		if shell.IsInterrupt(err) {
			slog.Error("Shell config execution timed out or was cancelled", "path", path, "error", err)
			return nil, fmt.Errorf("shell config %s: execution timed out or was cancelled: %w", path, err)
		}
		slog.Error("Shell config execution failed", "path", path, "error", err)
		return nil, fmt.Errorf("executing shell config %s: %w", path, err)
	}

	if builder.empty() {
		slog.Warn("Shell config produced no config", "path", path)
		return nil, nil
	}

	data, err := builder.JSON()
	if err != nil {
		slog.Error("Failed to marshal shell config", "path", path, "error", err)
		return nil, fmt.Errorf("marshaling shell config %s: %w", path, err)
	}

	slog.Info("Shell config loaded successfully", "path", path, "bytes", len(data))
	return data, nil
}
