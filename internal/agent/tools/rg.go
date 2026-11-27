// rg.go —— ripgrep 的封装与可用性探测。

package tools

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/crush/internal/log"
)

var getRg = sync.OnceValue(func() string {
	if testing.Testing() {
		return ""
	}
	path, err := exec.LookPath("rg")
	if err != nil {
		if log.Initialized() {
			slog.Warn("Ripgrep (rg) not found in $PATH. Some grep features might be limited or slower.")
		}
		return ""
	}
	return path
})

func getRgCmd(ctx context.Context, globPattern string) *exec.Cmd {
	name := getRg()
	if name == "" {
		return nil
	}
	// Note: we intentionally do 不 pass -L (follow symlinks). Following
	// symlinks lets rg escape the search root (into module caches, the nix
	// 存储, $HOME, etc.) 和 chase cycles, which pins all cores 和 can
	// hang. 这 keeps glob scoped 到 tree it was pointed 在, matching
	// the grep search command.
	args := []string{"--files", "--null"}
	if globPattern != "" {
		if !filepath.IsAbs(globPattern) && !strings.HasPrefix(globPattern, "/") {
			globPattern = "/" + globPattern
		}
		args = append(args, "--glob", globPattern)
	}
	return exec.CommandContext(ctx, name, args...)
}

func getRgSearchCmd(ctx context.Context, pattern, path, include string) *exec.Cmd {
	name := getRg()
	if name == "" {
		return nil
	}
	// Use -n 到 show line numbers, -0 为 null separation 到 handle Windows paths
	args := []string{"--json", "-H", "-n", "-0", pattern}
	if include != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, path)

	return exec.CommandContext(ctx, name, args...)
}
