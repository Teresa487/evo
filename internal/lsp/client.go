package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/home"
	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/charmbracelet/x/powernap/pkg/transport"
)

// DiagnosticCounts holds count 的 diagnostics 通过 severity.
type DiagnosticCounts struct {
	Error       int
	Warning     int
	Information int
	Hint        int
}

type Client struct {
	client *powernap.Client
	name   string
	debug  bool

	// Working 目录 这 LSP 是 scoped 到.
	cwd string

	// 文件 types 这 LSP 服务端 handles (e.g., .go, .rs, .py)
	fileTypes []string

	// Configuration 为 这 LSP 客户端
	config config.LSPConfig

	// Long-lived 上下文 为 客户端's lifetime, independent 的 any
	// 请求-scoped 上下文. Used 为 restart 和 other operations 那
	// must survive beyond initial 工具调用 那 created 客户端.
	ctx       context.Context
	cancelCtx context.CancelFunc
	resolver  config.VariableResolver

	// Diagnostic change callback
	onDiagnosticsChanged func(name string, count int)

	// Diagnostic 缓存
	diagnostics *csync.VersionedMap[protocol.DocumentURI, []protocol.Diagnostic]

	// Cached diagnostic counts 到 avoid map copy 在 every UI render.
	diagCountsCache   DiagnosticCounts
	diagCountsVersion uint64
	diagCountsMu      sync.Mutex

	// Files 是 currently opened 通过 LSP
	openFiles *csync.Map[string, *OpenFileInfo]

	// 服务端 state
	serverState atomic.Value
}

// New creates new LSP 客户端 using powernap implementation.
func New(
	name string,
	cfg config.LSPConfig,
	resolver config.VariableResolver,
	cwd string,
	debug bool,
) (*Client, error) {
	// Use long-lived 上下文 independent 的 caller's 请求 上下文.
	// caller's 上下文 may 是 canceled 当 工具调用 completes,
	// 但 LSP 客户端 must survive across multiple requests 和 restarts.
	clientCtx, cancelCtx := context.WithCancel(context.Background())
	client := &Client{
		name:        name,
		fileTypes:   cfg.FileTypes,
		diagnostics: csync.NewVersionedMap[protocol.DocumentURI, []protocol.Diagnostic](),
		openFiles:   csync.NewMap[string, *OpenFileInfo](),
		config:      cfg,
		ctx:         clientCtx,
		cancelCtx:   cancelCtx,
		debug:       debug,
		resolver:    resolver,
		cwd:         cwd,
	}
	client.serverState.Store(StateStopped)

	if err := client.createPowernapClient(); err != nil {
		return nil, err
	}

	return client, nil
}

// Initialize initializes LSP 客户端 和 returns 服务端 capabilities.
func (c *Client) Initialize(ctx context.Context, workspaceDir string) (*protocol.InitializeResult, error) {
	// Register handlers 为 requests 服务端 may send during
	// initialize handshake itself (e.g. typescript-language-服务端 issuing
	// 窗口/workDoneProgress/创建 while loading project, before
	// initialize has returned). Registering after 客户端.Initialize() 是 too
	// late 为 those — 服务端 treats unhandled 响应 作为 fatal.
	c.registerHandlers()

	if err := c.client.Initialize(ctx, false); err != nil {
		return nil, fmt.Errorf("failed to initialize the lsp client: %w", err)
	}

	// Convert powernap capabilities 到 protocol capabilities
	caps := c.client.GetCapabilities()
	protocolCaps := protocol.ServerCapabilities{
		TextDocumentSync: caps.TextDocumentSync,
		CompletionProvider: func() *protocol.CompletionOptions {
			if caps.CompletionProvider != nil {
				return &protocol.CompletionOptions{
					TriggerCharacters:   caps.CompletionProvider.TriggerCharacters,
					AllCommitCharacters: caps.CompletionProvider.AllCommitCharacters,
					ResolveProvider:     caps.CompletionProvider.ResolveProvider,
				}
			}
			return nil
		}(),
	}

	result := &protocol.InitializeResult{
		Capabilities: protocolCaps,
	}

	return result, nil
}

// closeTimeout 是 maximum time 到 wait 为 graceful LSP shutdown.
const closeTimeout = 5 * time.Second

// Kill kills 客户端 without doing anything else.
func (c *Client) Kill() { c.client.Kill() }

// Shutdown permanently cancels 客户端's long-lived 上下文 和 kills
// underlying process. Unlike Restart, 这 是 终态: 客户端 cannot 是
// reused after Shutdown.
func (c *Client) Shutdown() {
	c.cancelCtx()
	c.client.Kill()
}

// GetOffsetEncoding returns negotiated offset encoding 为 这 客户端.
func (c *Client) GetOffsetEncoding() powernap.OffsetEncoding {
	return c.client.GetOffsetEncoding()
}

// Close closes all open files 在 客户端, then shuts down gracefully.
// If shutdown takes longer than closeTimeout, it falls back 到 Kill().
func (c *Client) Close(ctx context.Context) error {
	c.CloseAllFiles(ctx)

	// Use 超时 到 prevent hanging 在 unresponsive LSP servers.
	// jsonrpc2's send 锁 doesn't respect 上下文 cancellation, 所以 we
	// need 到 fall back 到 Kill() which closes underlying 连接.
	closeCtx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		if err := c.client.Shutdown(closeCtx); err != nil {
			slog.Warn("Failed to shutdown LSP client", "error", err)
		}
		done <- c.client.Exit()
	}()

	select {
	case err := <-done:
		return err
	case <-closeCtx.Done():
		c.client.Kill()
		return closeCtx.Err()
	}
}

// createPowernapClient creates new powernap 客户端 用 current configuration.
func (c *Client) createPowernapClient() error {
	rootURI := string(protocol.URIFromPath(c.cwd))

	command, err := c.resolver.ResolveValue(c.config.Command)
	if err != nil {
		return fmt.Errorf("invalid lsp command: %w", err)
	}

	args, err := c.config.ResolvedArgs(c.resolver)
	if err != nil {
		return fmt.Errorf("invalid lsp args: %w", err)
	}

	envs, err := c.config.ResolvedEnv(c.resolver)
	if err != nil {
		return fmt.Errorf("invalid lsp env: %w", err)
	}

	clientConfig := powernap.ClientConfig{
		Command:     home.Long(command),
		Args:        args,
		RootURI:     rootURI,
		Environment: envs,
		Settings:    c.config.Options,
		InitOptions: c.config.InitOptions,
		WorkspaceFolders: []protocol.WorkspaceFolder{
			{
				URI:  rootURI,
				Name: filepath.Base(c.cwd),
			},
		},
	}

	powernapClient, err := powernap.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create lsp client: %w", err)
	}

	c.client = powernapClient
	return nil
}

// registerHandlers registers standard LSP 通知 和 请求 handlers.
func (c *Client) registerHandlers() {
	c.RegisterServerRequestHandler("workspace/applyEdit", HandleApplyEdit(c.client.GetOffsetEncoding()))
	c.RegisterServerRequestHandler("workspace/configuration", HandleWorkspaceConfiguration)
	c.RegisterServerRequestHandler("client/registerCapability", HandleRegisterCapability)
	c.RegisterServerRequestHandler("window/workDoneProgress/create", HandleWorkDoneProgressCreate)
	c.RegisterNotificationHandler("window/showMessage", func(ctx context.Context, method string, params json.RawMessage) {
		if c.debug {
			HandleServerMessage(ctx, method, params)
		}
	})
	c.RegisterNotificationHandler("textDocument/publishDiagnostics", func(_ context.Context, _ string, params json.RawMessage) {
		HandleDiagnostics(c, params)
	})
}

// Restart closes current LSP 客户端 和 creates new one 用 same configuration.
func (c *Client) Restart() error {
	var openFiles []string
	for uri := range c.openFiles.Seq2() {
		openFiles = append(openFiles, string(uri))
	}

	// 取消 old long-lived 上下文 和 创建 fresh one 所以 那
	// reinitialization 是 不 affected 通过 any prior cancellation.
	c.cancelCtx()
	c.ctx, c.cancelCtx = context.WithCancel(context.Background())

	closeCtx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	if err := c.Close(closeCtx); err != nil {
		slog.Warn("Error closing client during restart", "name", c.name, "error", err)
	}

	c.SetServerState(StateStopped)

	c.diagCountsCache = DiagnosticCounts{}
	c.diagCountsVersion = 0

	if err := c.createPowernapClient(); err != nil {
		return err
	}

	initCtx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()

	c.SetServerState(StateStarting)

	// Register handlers before Initialize 所以 servers 那 send
	// requests during handshake (e.g. 窗口/workDoneProgress/创建)
	// don't crash 在 unhandled 响应.
	c.registerHandlers()

	if err := c.client.Initialize(initCtx, false); err != nil {
		c.SetServerState(StateError)
		return fmt.Errorf("failed to initialize lsp client: %w", err)
	}

	if err := c.WaitForServerReady(initCtx); err != nil {
		slog.Error("Server failed to become ready after restart", "name", c.name, "error", err)
		c.SetServerState(StateError)
		return err
	}

	for _, uri := range openFiles {
		if err := c.OpenFile(initCtx, uri); err != nil {
			slog.Warn("Failed to reopen file after restart", "file", uri, "error", err)
		}
	}
	return nil
}

// ServerState represents state 的 LSP 服务端
type ServerState int

const (
	StateUnstarted ServerState = iota
	StateStarting
	StateReady
	StateError
	StateStopped
	StateDisabled
)

// GetServerState returns current state 的 LSP 服务端
func (c *Client) GetServerState() ServerState {
	if val := c.serverState.Load(); val != nil {
		return val.(ServerState)
	}
	return StateStarting
}

// SetServerState sets current state 的 LSP 服务端
func (c *Client) SetServerState(state ServerState) {
	c.serverState.Store(state)
}

// GetName returns name 的 LSP 客户端
func (c *Client) GetName() string {
	return c.name
}

// FileTypes returns 文件 types 这 LSP 客户端 handles
func (c *Client) FileTypes() []string {
	return slices.Clone(c.fileTypes)
}

// SetDiagnosticsCallback sets callback function 为 diagnostic changes
func (c *Client) SetDiagnosticsCallback(callback func(name string, count int)) {
	c.onDiagnosticsChanged = callback
}

// WaitForServerReady waits 为 服务端 到 是 ready
func (c *Client) WaitForServerReady(ctx context.Context) error {
	// Set initial state
	c.SetServerState(StateStarting)

	// Try 到 ping 服务端 用 simple 请求
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	if c.debug {
		slog.Debug("Waiting for LSP server to be ready...")
	}

	c.openKeyConfigFiles(ctx)

	for {
		select {
		case <-ctx.Done():
			c.SetServerState(StateError)
			return fmt.Errorf("timeout waiting for LSP server to be ready")
		case <-ticker.C:
			// Check 如果 客户端 是 running
			if !c.client.IsRunning() {
				if c.debug {
					slog.Debug("LSP server not ready yet", "server", c.name)
				}
				continue
			}

			// 服务端 是 ready
			c.SetServerState(StateReady)
			if c.debug {
				slog.Debug("LSP server is ready")
			}
			return nil
		}
	}
}

// OpenFileInfo contains information about open 文件
type OpenFileInfo struct {
	Version int32
	URI     protocol.DocumentURI
}

// HandlesFile checks 如果 这 LSP 客户端 handles given 文件 based 在 它的
// extension 和 whether it's within working 目录.
func (c *Client) HandlesFile(path string) bool {
	if c == nil {
		return false
	}
	if !fsext.HasPrefix(path, c.cwd) {
		slog.Debug("File outside workspace", "name", c.name, "file", path, "workDir", c.cwd)
		return false
	}
	return handlesFiletype(c.name, c.fileTypes, path)
}

// OpenFile opens 文件 在 LSP 服务端.
func (c *Client) OpenFile(ctx context.Context, filepath string) error {
	if !c.HandlesFile(filepath) {
		return nil
	}

	uri := string(protocol.URIFromPath(filepath))

	if _, exists := c.openFiles.Get(uri); exists {
		return nil // Already open
	}

	// Skip files 那 do 不 exist 或 cannot 是 读取
	content, err := os.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	// Notify 服务端 about opened document
	if err = c.client.NotifyDidOpenTextDocument(ctx, uri, string(powernap.DetectLanguage(filepath)), 1, string(content)); err != nil {
		return err
	}

	c.openFiles.Set(uri, &OpenFileInfo{
		Version: 1,
		URI:     protocol.DocumentURI(uri),
	})

	return nil
}

// NotifyChange notifies 服务端 about 文件 change.
func (c *Client) NotifyChange(ctx context.Context, filepath string) error {
	if c == nil {
		return nil
	}
	uri := string(protocol.URIFromPath(filepath))

	content, err := os.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	fileInfo, isOpen := c.openFiles.Get(uri)
	if !isOpen {
		return fmt.Errorf("cannot notify change for unopened file: %s", filepath)
	}

	// Increment version
	fileInfo.Version++

	// 创建 change 事件
	changes := []protocol.TextDocumentContentChangeEvent{
		{
			Value: protocol.TextDocumentContentChangeWholeDocument{
				Text: string(content),
			},
		},
	}

	return c.client.NotifyDidChangeTextDocument(ctx, uri, int(fileInfo.Version), changes)
}

// IsFileOpen checks 如果 文件 是 currently open.
func (c *Client) IsFileOpen(filepath string) bool {
	uri := string(protocol.URIFromPath(filepath))
	_, exists := c.openFiles.Get(uri)
	return exists
}

// CloseAllFiles closes all currently open files.
func (c *Client) CloseAllFiles(ctx context.Context) {
	for uri := range c.openFiles.Seq2() {
		if c.debug {
			slog.Debug("Closing file", "file", uri)
		}
		if err := c.client.NotifyDidCloseTextDocument(ctx, uri); err != nil {
			slog.Warn("Error closing file", "uri", uri, "error", err)
			continue
		}
		c.openFiles.Del(uri)
	}
}

// GetFileDiagnostics returns diagnostics 为 specific 文件.
func (c *Client) GetFileDiagnostics(uri protocol.DocumentURI) []protocol.Diagnostic {
	diags, _ := c.diagnostics.Get(uri)
	return diags
}

// GetDiagnostics returns all diagnostics 为 all files.
func (c *Client) GetDiagnostics() map[protocol.DocumentURI][]protocol.Diagnostic {
	if c == nil {
		return nil
	}
	return c.diagnostics.Copy()
}

// GetDiagnosticCounts returns cached diagnostic counts 通过 severity.
// Uses VersionedMap version 到 avoid recomputing 在 every 调用.
func (c *Client) GetDiagnosticCounts() DiagnosticCounts {
	if c == nil {
		return DiagnosticCounts{}
	}
	currentVersion := c.diagnostics.Version()

	c.diagCountsMu.Lock()
	defer c.diagCountsMu.Unlock()

	if currentVersion == c.diagCountsVersion {
		return c.diagCountsCache
	}

	// Recompute counts.
	counts := DiagnosticCounts{}
	for _, diags := range c.diagnostics.Seq2() {
		for _, diag := range diags {
			switch diag.Severity {
			case protocol.SeverityError:
				counts.Error++
			case protocol.SeverityWarning:
				counts.Warning++
			case protocol.SeverityInformation:
				counts.Information++
			case protocol.SeverityHint:
				counts.Hint++
			}
		}
	}

	c.diagCountsCache = counts
	c.diagCountsVersion = currentVersion
	return counts
}

// OpenFileOnDemand opens 文件 只 如果 it's 不 already open.
func (c *Client) OpenFileOnDemand(ctx context.Context, filepath string) error {
	if c == nil {
		return nil
	}
	// Check 如果 文件 是 already open
	if c.IsFileOpen(filepath) {
		return nil
	}

	// Open 文件
	return c.OpenFile(ctx, filepath)
}

// RegisterNotificationHandler registers 通知 处理器.
func (c *Client) RegisterNotificationHandler(method string, handler transport.NotificationHandler) {
	c.client.RegisterNotificationHandler(method, handler)
}

// RegisterServerRequestHandler handles 服务端 requests.
func (c *Client) RegisterServerRequestHandler(method string, handler transport.Handler) {
	c.client.RegisterHandler(method, handler)
}

// openKeyConfigFiles opens important configuration files 那 help initialize 服务端.
func (c *Client) openKeyConfigFiles(ctx context.Context) {
	// Try 到 open each 文件, ignoring errors 如果 they don't exist
	for _, file := range c.config.RootMarkers {
		file = filepath.Join(c.cwd, file)
		if _, err := os.Stat(file); err == nil {
			// 文件 exists, try 到 open it
			if err := c.OpenFile(ctx, file); err != nil {
				slog.Error("Failed to open key config file", "file", file, "error", err)
			} else {
				slog.Debug("Opened key config file for initialization", "file", file)
			}
		}
	}
}

// NotifyWorkspaceChange sends 工作区-level 文件 change 通知 到
// trigger re-analysis 的 all files. 这 是 useful 当 overall project
// state may have changed (e.g., after project-wide refactoring) 和
// diagnostics 为 files 不 currently being edited may 是 stale.
func (c *Client) NotifyWorkspaceChange(ctx context.Context) error {
	if c == nil {
		return nil
	}
	return c.client.NotifyDidChangeWatchedFiles(ctx, []protocol.FileEvent{
		{URI: protocol.DocumentURI(protocol.URIFromPath(c.cwd)), Type: protocol.Changed},
	})
}

// RefreshOpenFiles re-notifies LSP 服务端 about all currently open files,
// which triggers re-analysis 和 fresh diagnostics 为 entire project.
func (c *Client) RefreshOpenFiles(ctx context.Context) {
	if c == nil {
		return
	}
	for uri, info := range c.openFiles.Seq2() {
		path, err := protocol.DocumentURI(uri).Path()
		if err != nil {
			slog.Warn("Failed to convert URI to path", "uri", uri, "error", err)
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("Failed to read file for refresh", "path", path, "error", err)
			continue
		}
		info.Version++
		changes := []protocol.TextDocumentContentChangeEvent{
			{
				Value: protocol.TextDocumentContentChangeWholeDocument{
					Text: string(content),
				},
			},
		}
		if err := c.client.NotifyDidChangeTextDocument(ctx, uri, int(info.Version), changes); err != nil {
			slog.Warn("Failed to notify file change", "uri", uri, "error", err)
		}
	}
}

// WaitForDiagnostics waits until diagnostics stop changing 为 settling
// period, indicating LSP 服务端 has finished processing. If no
// diagnostics change within firstChangeDuration, it returns early since the
// 服务端 likely isn't going 到 republish.
func (c *Client) WaitForDiagnostics(ctx context.Context, timeout time.Duration) {
	if c == nil {
		return
	}

	const (
		firstChangeDuration = 1 * time.Second
		settleDuration      = 300 * time.Millisecond
	)

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	firstChangeTimer := time.NewTimer(min(timeout, firstChangeDuration))
	defer firstChangeTimer.Stop()
	previousVersion := c.diagnostics.Version()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-firstChangeTimer.C:
			// No change arrived quickly — 服务端 isn't republishing.
			return
		case <-ticker.C:
			currentVersion := c.diagnostics.Version()
			if currentVersion != previousVersion {
				// Diagnostics changed — now wait 为 them 到 settle.
				c.waitForDiagnosticsToSettle(ctx, deadline.C, settleDuration)
				return
			}
		}
	}
}

// waitForDiagnosticsToSettle 等待 until diagnostics version stays same
// 为 settleDuration, indicating LSP 服务端 has finished publishing.
func (c *Client) waitForDiagnosticsToSettle(ctx context.Context, deadline <-chan time.Time, settleDuration time.Duration) {
	lastVersion := c.diagnostics.Version()
	settleTicker := time.NewTicker(50 * time.Millisecond)
	defer settleTicker.Stop()

	// Track how long the version has been stable.
	stableStart := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			return
		case <-settleTicker.C:
			currentVersion := c.diagnostics.Version()
			if currentVersion != lastVersion {
				// New change detected — reset the stable timer.
				lastVersion = currentVersion
				stableStart = time.Now()
			} else if time.Since(stableStart) >= settleDuration {
				// Diagnostics have been stable 为 settle duration.
				return
			}
		}
	}
}

// FindReferences finds all references 到 symbol 在 given position.
func (c *Client) FindReferences(ctx context.Context, filepath string, line, character int, includeDeclaration bool) ([]protocol.Location, error) {
	if err := c.OpenFileOnDemand(ctx, filepath); err != nil {
		return nil, err
	}

	// Add 超时 到 prevent hanging 在 slow LSP servers.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// NOTE: line 和 character should 是 0-based.
	// See: https://microsoft.github.io/language-服务端-protocol/specifications/lsp/3.17/specification/#position
	return c.client.FindReferences(ctx, filepath, line-1, character-1, includeDeclaration)
}

// Rename renames symbol 在 given position across all files.
func (c *Client) Rename(ctx context.Context, filepath string, line, character int, newName string) (*protocol.WorkspaceEdit, error) {
	if err := c.OpenFileOnDemand(ctx, filepath); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return c.client.RequestRename(ctx, filepath, line-1, character-1, newName) //nolint:wrapcheck
}

// DocumentSymbols returns document symbols 为 given 文件.
func (c *Client) DocumentSymbols(ctx context.Context, filepath string) ([]protocol.DocumentSymbolResult, error) {
	if err := c.OpenFileOnDemand(ctx, filepath); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return c.client.RequestDocumentSymbols(ctx, filepath) //nolint:wrapcheck
}

// Definition finds definition 的 symbol 在 given position.
func (c *Client) Definition(ctx context.Context, filepath string, line, character int) ([]protocol.Location, error) {
	if err := c.OpenFileOnDemand(ctx, filepath); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return c.client.RequestDefinition(ctx, filepath, line-1, character-1) //nolint:wrapcheck
}

// PrepareCallHierarchy prepares 调用 hierarchy item 在 given position.
func (c *Client) PrepareCallHierarchy(ctx context.Context, filepath string, line, character int) ([]protocol.CallHierarchyItem, error) {
	if err := c.OpenFileOnDemand(ctx, filepath); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return c.client.PrepareCallHierarchy(ctx, filepath, line-1, character-1) //nolint:wrapcheck
}

// IncomingCalls returns all callers 的 given 调用 hierarchy item.
func (c *Client) IncomingCalls(ctx context.Context, item protocol.CallHierarchyItem) ([]protocol.CallHierarchyIncomingCall, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return c.client.IncomingCalls(ctx, item) //nolint:wrapcheck
}

// OutgoingCalls returns all callees 的 given 调用 hierarchy item.
func (c *Client) OutgoingCalls(ctx context.Context, item protocol.CallHierarchyItem) ([]protocol.CallHierarchyOutgoingCall, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return c.client.OutgoingCalls(ctx, item) //nolint:wrapcheck
}
