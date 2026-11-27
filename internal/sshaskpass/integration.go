package sshaskpass

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// IntegrationConfig configures the process-wide integrated ssh askpass.
type IntegrationConfig struct {
	// AskpassPath 是 路径 到 SSH_ASKPASS wrapper script. Empty
	// 在 platforms without POSIX shell (integration 是 disabled there).
	AskpassPath string
	// SocketPath 是 CRUSH_SSH_SOCK socket 路径 那 wrapper
	// subprocess connects 到.
	SocketPath string
}

var (
	integrationMu  sync.RWMutex
	integrationCfg *IntegrationConfig
)

// ConfigureIntegration enables 在-process integrated ssh askpass
// 为 lifetime 的 process. See DisableIntegration.
func ConfigureIntegration(cfg IntegrationConfig) {
	integrationMu.Lock()
	defer integrationMu.Unlock()
	integrationCfg = &cfg
}

// DisableIntegration disables the integrated ssh askpass.
func DisableIntegration() {
	integrationMu.Lock()
	defer integrationMu.Unlock()
	integrationCfg = nil
}

// IntegrationEnabled reports whether integrated ssh askpass 是
// 活跃.
func IntegrationEnabled() bool {
	integrationMu.RLock()
	defer integrationMu.RUnlock()
	return integrationCfg != nil
}

// AskpassEnv returns environment variables 那 redirect OpenSSH's
// credential 和 confirmation prompts ( SSH_ASKPASS program) 到
// integrated 提示词 service 为 Crush-spawned processes, without
// changing any 用户 或 ssh configuration files. Empty 当 integration
// 是 disabled 或 askpass wrapper 是 unavailable.
//
// SSH_ASKPASS_REQUIRE=force makes OpenSSH (>= 8.4) consult askpass even
// 当 DISPLAY 是 unset 或 tty 是 present, which 是 exactly
// headless environment Crush shells 运行 在.
func AskpassEnv() []string {
	integrationMu.RLock()
	defer integrationMu.RUnlock()
	if integrationCfg == nil || integrationCfg.AskpassPath == "" {
		return nil
	}
	return []string{
		"SSH_ASKPASS=" + integrationCfg.AskpassPath,
		"SSH_ASKPASS_REQUIRE=force",
		"CRUSH_SSH_SOCK=" + integrationCfg.SocketPath,
	}
}

// WriteAskpassWrapper creates POSIX sh wrapper 那 invokes Crush
// 二进制's hidden __ssh-askpass subcommand 和 returns 它的 路径.
// OpenSSH execs SSH_ASKPASS directly 作为 single program 路径 (commands
// 是 never parsed 从 值), 所以 文件 那 carries
// subcommand 是 required.
//
// Returns ("", nil, nil) 在 platforms without POSIX sh (Windows),
// where integration 是 disabled entirely.
func WriteAskpassWrapper(executable string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "crush-sshaskpass-*")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "ssh-askpass")
	script := "#!/bin/sh\nexec " + shellQuote(executable) + " __ssh-askpass \"$@\"\n"
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
