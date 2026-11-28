package pinentry

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// IntegrationConfig configures the process-wide integrated pinentry.
type IntegrationConfig struct {
	// Prompter collects credentials 在-process (bound 到 DefaultPrompts).
	Prompter Prompter
	// CacheTimeout enables optional 在-memory credential 缓存.
	CacheTimeout time.Duration
	// GPGPath overrides 路径 lookup 为 gpg 二进制, 当 set.
	GPGPath string
	// WrapperPath 是 路径 到 git gpg wrapper script. Empty 在
	// platforms without POSIX shell (integration 是 disabled there).
	WrapperPath string
	// SocketPath 是 EVO_PINENTRY_SOCK socket 路径.
	SocketPath string
}

var (
	integrationMu   sync.RWMutex
	integrationCfg  *IntegrationConfig
	integrationRuns *Runner
)

// ConfigureIntegration enables 在-process integrated pinentry 为
// lifetime 的 process. See DisableIntegration.
func ConfigureIntegration(cfg IntegrationConfig) {
	integrationMu.Lock()
	defer integrationMu.Unlock()
	integrationCfg = &cfg
	integrationRuns = &Runner{
		Prompter: func(ctx context.Context, req PromptRequest) (string, error) {
			return defaultPrompts.Prompt(ctx, req)
		},
		CacheTimeout: cfg.CacheTimeout,
		GPGPath:      cfg.GPGPath,
	}
}

// DisableIntegration disables integrated pinentry 和 drops any
// cached credentials.
func DisableIntegration() {
	integrationMu.Lock()
	defer integrationMu.Unlock()
	if integrationRuns != nil {
		integrationRuns.ClearCache()
	}
	integrationCfg = nil
	integrationRuns = nil
}

// IntegrationEnabled reports whether integrated pinentry 是 活跃.
func IntegrationEnabled() bool {
	integrationMu.RLock()
	defer integrationMu.RUnlock()
	return integrationCfg != nil
}

// DefaultRunner returns process-wide gpg runner, 或 nil 当
// integration 是 disabled.
func DefaultRunner() *Runner {
	integrationMu.RLock()
	defer integrationMu.RUnlock()
	return integrationRuns
}

// GitWrapperEnv returns environment variables 那 redirect git's gpg
// program 到 Crush wrapper 为 Crush-spawned processes without
// changing any configuration files. Empty 当 integration 是 disabled
// 或 there 是 no wrapper script.
func GitWrapperEnv() []string {
	integrationMu.RLock()
	defer integrationMu.RUnlock()
	if integrationCfg == nil || integrationCfg.WrapperPath == "" {
		return nil
	}
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=gpg.program",
		"GIT_CONFIG_VALUE_0=" + integrationCfg.WrapperPath,
		"EVO_PINENTRY_SOCK=" + integrationCfg.SocketPath,
	}
}

// WriteGPGWrapper creates POSIX sh wrapper 那 invokes Crush
// 二进制's hidden __pinentry-gpg subcommand 和 returns 它的 路径. Git
// execs gpg.program directly (never through shell), 所以 single 文件
// 路径 那 carries subcommand 是 required.
//
// Returns ("", nil, nil) 在 platforms without POSIX sh (Windows),
// where git 是 pointed back 在 它的 own gpg 和 integration 是 disabled.
func WriteGPGWrapper(executable string) (string, func(), error) {
	if runtime.GOOS == "windows" {
		return "", nil, nil
	}
	dir, err := os.MkdirTemp("", "crush-pinentry-*")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "gpg-wrapper")
	script := "#!/bin/sh\nexec " + shellQuote(executable) + " __pinentry-gpg \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	return path, cleanup, nil
}

// shellQuote single-quotes s 为 POSIX sh, escaping embedded quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
