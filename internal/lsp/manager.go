// Package lsp provides manager 为 Language 服务端 Protocol (LSP) clients.
package lsp

import (
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/fsext"
	powernapconfig "github.com/charmbracelet/x/powernap/pkg/config"
	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/sourcegraph/jsonrpc2"
)

const unavailableRetryDelay = 30 * time.Second

// Manager handles lazy initialization 的 LSP clients based 在 文件 types.
type Manager struct {
	clients     *csync.Map[string, *Client]
	unavailable *csync.Map[string, time.Time]
	cfg         *config.ConfigStore
	manager     *powernapconfig.Manager
	callback    func(name string, client *Client)
	now         func() time.Time
	lookPath    func(string) (string, error)
}

// NewManager creates a new LSP manager service.
func NewManager(cfg *config.ConfigStore) *Manager {
	manager := powernapconfig.NewManager()
	manager.LoadDefaults()

	// Merge 用户-configured LSPs into manager.
	for name, clientConfig := range cfg.Config().LSP {
		if clientConfig.Disabled {
			slog.Debug("LSP disabled by user config", "name", name)
			manager.RemoveServer(name)
			continue
		}

		// HACK: 用户 might have command name 在 their 配置 instead
		// 的 actual name. Find 和 use correct name.
		actualName := resolveServerName(manager, name)
		manager.AddServer(actualName, &powernapconfig.ServerConfig{
			Command:     clientConfig.Command,
			Args:        clientConfig.Args,
			Environment: clientConfig.Env,
			FileTypes:   clientConfig.FileTypes,
			RootMarkers: clientConfig.RootMarkers,
			InitOptions: clientConfig.InitOptions,
			Settings:    clientConfig.Options,
		})
	}

	return &Manager{
		clients:     csync.NewMap[string, *Client](),
		unavailable: csync.NewMap[string, time.Time](),
		cfg:         cfg,
		manager:     manager,
		callback:    func(string, *Client) {}, // default no-op callback
		now:         time.Now,
		lookPath:    exec.LookPath,
	}
}

// Clients returns map 的 LSP clients.
func (s *Manager) Clients() *csync.Map[string, *Client] {
	return s.clients
}

// SetCallback sets callback 那 是 invoked 当 new LSP
// 客户端 是 successfully started. 这 allows coordinator 到 add LSP tools.
func (s *Manager) SetCallback(cb func(name string, client *Client)) {
	s.callback = cb
}

// TrackConfigured notifies UI about 用户-configured LSP servers without
// starting them. Servers start 在 demand via Start().
func (s *Manager) TrackConfigured(ctx context.Context) {
	var wg sync.WaitGroup
	servers := s.manager.GetServers()
	for name := range servers {
		if !s.isUserConfigured(name) {
			continue
		}
		wg.Go(func() {
			s.callback(name, nil)
		})
	}
	wg.Wait()
}

// Start starts LSP 服务端 那 can handle given 文件 路径.
// If appropriate LSP 是 already running, 这 是 no-op.
func (s *Manager) Start(ctx context.Context, path string) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if !fsext.HasPrefix(path, s.cfg.WorkingDir()) {
		return
	}

	var wg sync.WaitGroup
	for name, server := range s.manager.GetServers() {
		wg.Go(func() {
			s.startServer(name, path, server)
		})
	}
	wg.Wait()
}

// skipAutoStartCommands contains commands 那 是 too generic 或 ambiguous 到
// auto-start without explicit 用户 configuration.
var skipAutoStartCommands = map[string]bool{
	"buck2":   true,
	"buf":     true,
	"cue":     true,
	"dart":    true,
	"deno":    true,
	"dotnet":  true,
	"dprint":  true,
	"gleam":   true,
	"java":    true,
	"julia":   true,
	"koka":    true,
	"node":    true,
	"npx":     true,
	"perl":    true,
	"plz":     true,
	"python":  true,
	"python3": true,
	"R":       true,
	"racket":  true,
	"rome":    true,
	"rubocop": true,
	"ruff":    true,
	"scarb":   true,
	"solc":    true,
	"stylua":  true,
	"swipl":   true,
	"tflint":  true,
}

func (s *Manager) startServer(name, filepath string, server *powernapconfig.ServerConfig) {
	var (
		isUserConfigured = s.isUserConfigured(name)
		autoLSP          = s.cfg.Config().Options.AutoLSP
	)
	if !isUserConfigured && autoLSP != nil && !*autoLSP {
		slog.Debug("Auto-start LSP disabled", "name", name)
		return
	}

	cfg := s.buildConfig(name, server)
	if cfg.Disabled {
		return
	}

	if client, ok := s.clients.Get(name); ok {
		switch client.GetServerState() {
		case StateReady, StateStarting, StateDisabled:
			s.callback(name, client)
			// already done, return
			return
		}
	}

	if isUserConfigured {
		if !handles(server, filepath, s.cfg.WorkingDir()) {
			return
		}
	} else if !s.canAutoStart(name, filepath, s.cfg.WorkingDir(), server) {
		return
	}

	// Check again 在 case another goroutine started it 在 meantime.
	if client, ok := s.clients.Get(name); ok {
		switch client.GetServerState() {
		case StateReady, StateStarting, StateDisabled:
			s.callback(name, client)
			return
		}
	}

	client, err := New(
		name,
		cfg,
		s.cfg.Resolver(),
		s.cfg.WorkingDir(),
		s.cfg.Config().Options.DebugLSP,
	)
	if err != nil {
		slog.Error("Failed to create LSP client", "name", name, "error", err)
		return
	}
	// Only 存储 non-nil clients. If another goroutine raced us,
	// prefer already-存储 客户端.
	if existing, ok := s.clients.Get(name); ok {
		switch existing.GetServerState() {
		case StateReady, StateStarting, StateDisabled:
			client.Shutdown()
			s.callback(name, existing)
			return
		}
	}
	s.clients.Set(name, client)
	defer func() {
		s.callback(name, client)
	}()

	switch client.GetServerState() {
	case StateReady, StateStarting, StateDisabled:
		// already done, return
		return
	}

	client.serverState.Store(StateStarting)

	// Use independent 上下文 为 initialization 所以 那 LSP 服务端
	// startup 是 不 tied 到 caller's 请求 上下文. caller's
	// 上下文 may have short 超时 或 是 canceled 当 工具调用
	// completes, 但 LSP initialization can take several seconds 和
	// 服务端 must persist beyond any single 请求.
	initCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cmp.Or(cfg.Timeout, 30))*time.Second)
	defer cancel()

	if _, err := client.Initialize(initCtx, s.cfg.WorkingDir()); err != nil {
		slog.Error("LSP client initialization failed", "name", name, "error", err)
		client.Shutdown()
		s.clients.Del(name)
		return
	}

	if err := client.WaitForServerReady(initCtx); err != nil {
		slog.Warn("LSP server not fully ready, continuing anyway", "name", name, "error", err)
		client.SetServerState(StateError)
	} else {
		client.SetServerState(StateReady)
	}

	slog.Debug("LSP client started", "name", name)
}

func (s *Manager) canAutoStart(
	name, filePath, workDir string,
	server *powernapconfig.ServerConfig,
) bool {
	if skipAutoStartCommands[server.Command] {
		slog.Debug("LSP command too generic for auto-start, skipping", "name", name, "command", server.Command)
		return false
	}

	// Filtering 通过 文件 type 是 cheap 和 usually rejects 服务端 before
	// root marker check. Do both before searching 路径, which can require stat
	// 为 every 目录 在 路径 为 every bundled 服务端.
	if !handles(server, filePath, workDir) {
		return false
	}

	if s.recentlyUnavailable(name) {
		return false
	}
	if _, err := s.lookPath(server.Command); err != nil {
		slog.Debug("LSP server not installed, skipping", "name", name, "command", server.Command)
		s.markUnavailable(name)
		return false
	}
	s.clearUnavailable(name)
	return true
}

func (s *Manager) isUserConfigured(name string) bool {
	cfg, ok := s.cfg.Config().LSP[name]
	return ok && !cfg.Disabled
}

func (s *Manager) recentlyUnavailable(name string) bool {
	lastUnavailableAt, exists := s.unavailable.Get(name)
	if !exists {
		return false
	}
	if s.now().Sub(lastUnavailableAt) < unavailableRetryDelay {
		return true
	}
	s.unavailable.Del(name)
	return false
}

func (s *Manager) markUnavailable(name string) {
	s.unavailable.Set(name, s.now())
}

func (s *Manager) clearUnavailable(name string) {
	s.unavailable.Del(name)
}

func (s *Manager) buildConfig(name string, server *powernapconfig.ServerConfig) config.LSPConfig {
	cfg := config.LSPConfig{
		Command:     server.Command,
		Args:        server.Args,
		Env:         server.Environment,
		FileTypes:   server.FileTypes,
		RootMarkers: server.RootMarkers,
		InitOptions: server.InitOptions,
		Options:     server.Settings,
	}
	if userCfg, ok := s.cfg.Config().LSP[name]; ok {
		cfg.Timeout = userCfg.Timeout
	}
	return cfg
}

func resolveServerName(manager *powernapconfig.Manager, name string) string {
	if _, ok := manager.GetServer(name); ok {
		return name
	}
	for sname, server := range manager.GetServers() {
		if server.Command == name {
			return sname
		}
	}
	return name
}

func handlesFiletype(sname string, fileTypes []string, filePath string) bool {
	if len(fileTypes) == 0 {
		return true
	}

	kind := powernap.DetectLanguage(filePath)
	name := strings.ToLower(filepath.Base(filePath))
	for _, filetype := range fileTypes {
		suffix := strings.ToLower(filetype)
		if !strings.HasPrefix(suffix, ".") {
			suffix = "." + suffix
		}
		if strings.HasSuffix(name, suffix) || filetype == string(kind) {
			slog.Debug("Handles file", "name", sname, "file", name, "filetype", filetype, "kind", kind)
			return true
		}
	}

	slog.Debug("Doesn't handle file", "name", sname, "file", name)
	return false
}

func hasRootMarkers(dir string, markers []string) bool {
	if len(markers) == 0 {
		return true
	}
	for _, pattern := range markers {
		// Use filepath.Glob 为 non-recursive check 在 root
		// 目录. 这 avoids walking entire tree (which 是
		// catastrophic 在 large monorepos 用 node_modules, etc.).
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}

func handles(server *powernapconfig.ServerConfig, filePath, workDir string) bool {
	return handlesFiletype(server.Command, server.FileTypes, filePath) &&
		hasRootMarkers(workDir, server.RootMarkers)
}

// KillAll force-kills all the LSP clients.
//
// 这 是 generally faster than [Manager.StopAll] because it doesn't wait 为
// 服务端 到 exit gracefully, 但 it can lead 到 data loss 如果 服务端 是
// 在 middle 的 writing something.
// Generally it doesn't matter 当 shutting down Crush, though.
func (s *Manager) KillAll(context.Context) {
	var wg sync.WaitGroup
	for name, client := range s.clients.Seq2() {
		wg.Go(func() {
			defer func() { s.callback(name, client) }()
			client.Shutdown()
			client.SetServerState(StateStopped)
			s.clients.Del(name)
			slog.Debug("Killed LSP client", "name", name)
		})
	}
	wg.Wait()
}

// StopAll stops all running LSP clients 和 clears 客户端 map.
func (s *Manager) StopAll(ctx context.Context) {
	var wg sync.WaitGroup
	for name, client := range s.clients.Seq2() {
		wg.Go(func() {
			defer func() { s.callback(name, client) }()
			if err := client.Close(ctx); err != nil &&
				!errors.Is(err, io.EOF) &&
				!errors.Is(err, context.Canceled) &&
				!errors.Is(err, jsonrpc2.ErrClosed) &&
				err.Error() != "signal: killed" {
				slog.Warn("Failed to stop LSP client", "name", name, "error", err)
			}
			client.cancelCtx()
			client.SetServerState(StateStopped)
			s.clients.Del(name)
			slog.Debug("Stopped LSP client", "name", name)
		})
	}
	wg.Wait()
}
