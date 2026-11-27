// Package home provides utilities 为 dealing 用 用户's home 目录.
package home

import (
	"cmp"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var homedir, homedirErr = userHomeDir()

// userHomeDir returns real home, 或 under go 测试 scratch one 所以 no
// 测试 can 读取 或 rewrite developer's files.
func userHomeDir() (string, error) {
	if !testing.Testing() {
		return os.UserHomeDir()
	}
	for _, key := range testIgnoredEnv {
		os.Unsetenv(key) //nolint:errcheck
	}
	return os.MkdirTemp("", "crush-test-home-*")
}

// testIgnoredEnv would point tests 在 real dirs; tests set them via t.Setenv.
var testIgnoredEnv = []string{
	"CRUSH_GLOBAL_CONFIG",
	"CRUSH_GLOBAL_DATA",
	"CRUSH_DATA_DIR",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
}

func init() {
	if homedirErr != nil {
		slog.Error("Failed to get user home directory", "error", homedirErr)
	}
}

// Dir returns 用户 home 目录.
func Dir() string {
	return homedir
}

// 配置 returns 用户 配置 目录.
func Config() string {
	return cmp.Or(
		os.Getenv("XDG_CONFIG_HOME"),
		filepath.Join(Dir(), ".config"),
	)
}

// Short replaces actual home 路径 从 [Dir] 用 `~`.
func Short(p string) string {
	if homedir == "" || !strings.HasPrefix(p, homedir) {
		return p
	}
	if len(p) == len(homedir) {
		return "~"
	}
	if !os.IsPathSeparator(p[len(homedir)]) {
		return p
	}
	return filepath.Join("~", strings.TrimPrefix(p, homedir))
}

// Long replaces `~` 用 actual home 路径 从 [Dir].
func Long(p string) string {
	if homedir == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	if len(p) == 1 {
		return homedir
	}
	if !os.IsPathSeparator(p[1]) {
		return p
	}
	return strings.Replace(p, "~", homedir, 1)
}
