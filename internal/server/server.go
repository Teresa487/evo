// server.go —— HTTP/SSE 服务端：把远端客户端的请求转成 backend 调用，并把事件推回去。
//
// 从产物反推：
//
//	远端 TUI → ClientWorkspace → HTTP /v1/...
//	  → server（本文件 + endpoints.go + proto.go）
//	  → backend → app.App → agent.Run
//	  → SSE event stream → ClientWorkspace → 远端 TUI 显示
//
// 服务端还负责 client_id 合法性、workspace 生命周期、空闲关闭。
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/config"
)

// maxUnixSocketPathLen 是 maximum length 的 Unix domain socket
// 路径. macOS sun_path 字段 是 104 bytes; Linux allows 108. We
// use 104 所以 resulting 路径 是 portable across both platforms.
const maxUnixSocketPathLen = 104

// selfShutdownTimeout bounds shutdown 服务端 starts 在 它的 own. 它
// 是 budget `crush 服务端` gives shutdown signal starts.
const selfShutdownTimeout = 15 * time.Second

// socketDir returns 目录 used 为 Crush Unix socket.
// 它 prefers $XDG_RUNTIME_DIR 当 set (systemd's per-用户 runtime
// 目录 在 Linux), 和 otherwise falls back 到 [os.TempDir],
// which resolves 到 per-用户 private $TMPDIR 在 macOS 和 到
// /tmp 在 Linux.
func socketDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return os.TempDir()
}

// ErrServerClosed 是 returned 当 服务端 是 closed.
var ErrServerClosed = http.ErrServerClosed

// ParseHostURL parses 主机 URL into [url.URL]. "http" scheme
// 是 accepted 作为 alias 为 "tcp" 和 normalized 到 it.
//
// 一个 relative unix socket 路径 是 made absolute against current
// 目录, 所以 address keeps naming same socket after process
// changes 目录, 作为 客户端 does 为 --cwd.
func ParseHostURL(host string) (*url.URL, error) {
	proto, addr, ok := strings.Cut(host, "://")
	if !ok {
		return nil, fmt.Errorf("invalid host format: %s", host)
	}
	if proto == "http" {
		proto = "tcp"
	}

	// On Linux leading '@' names abstract socket, which has no 路径.
	if proto == "unix" && addr != "" && !filepath.IsAbs(addr) && !strings.HasPrefix(addr, "@") {
		abs, err := filepath.Abs(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid unix socket path: %w", err)
		}
		addr = abs
	}

	var basePath string
	if proto == "tcp" {
		parsed, err := url.Parse("tcp://" + addr)
		if err != nil {
			return nil, fmt.Errorf("invalid tcp address: %v", err)
		}
		addr = parsed.Host
		basePath = parsed.Path
	}
	return &url.URL{
		Scheme: proto,
		Host:   addr,
		Path:   basePath,
	}, nil
}

// DefaultHost returns 默认 服务端 host.
//
// On Windows address 是 named pipe \\.\pipe\crush-<sid>.sock,
// where <sid> 是 current 用户's SID. On Unix platforms socket
// lives 在 per-用户 runtime 目录 returned 通过 [socketDir] 和
// 是 named crush-<uid>.sock. If composed 路径 would exceed
// [maxUnixSocketPathLen] bytes (the macOS sun_path limit), we fall back
// 到 /tmp/crush-<uid>.sock 所以 socket remains bindable.
//
// 用户's id keeps users 从 sharing 服务端, since pipe
// namespace 和 /tmp 是 machine-wide. If it cannot 是 determined,
// DefaultHost returns "", which callers reject 作为 invalid host,
// rather than name every 用户 would share.
func DefaultHost() string {
	id := currentUserID()
	if id == "" {
		return ""
	}
	sock := fmt.Sprintf("crush-%s.sock", id)
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("npipe:////./pipe/%s", sock)
	}
	path := filepath.Join(socketDir(), sock)
	if len(path) > maxUnixSocketPathLen {
		path = filepath.Join("/tmp", sock)
	}
	return "unix://" + path
}

// 服务端 represents Crush 服务端 bound 到 specific address.
type Server struct {
	// Addr can 是 TCP address, Unix socket 路径, 或 Windows named pipe.
	Addr    string
	network string

	h  *http.Server
	ln net.Listener
	// cancelRequests cancels 上下文 every 请求 derives 从.
	cancelRequests context.CancelFunc

	backend *backend.Backend
	logger  *slog.Logger

	// selfShutdown runs shutdown backend asks 为 在 most once.
	// selfShutdownStarted 是 set before 那 shutdown begins 和
	// selfShutdownDone 是 closed once it has finished, 所以 [服务端.Serve]
	// can wait 为 it.
	selfShutdown        sync.Once
	selfShutdownStarted atomic.Bool
	selfShutdownDone    chan struct{}
}

// SetLogger sets logger 为 服务端.
func (s *Server) SetLogger(logger *slog.Logger) {
	s.logger = logger
}

// DisableIdleShutdown keeps 服务端 running 当 it goes 空闲. 一个 服务端
// spawned 在 demand 通过 客户端 shuts down once 空闲 所以 it cannot outlive
// 客户端; one started explicitly 用 `crush 服务端` was asked 为 在
// purpose 和 runs until it 是 stopped.
func (s *Server) DisableIdleShutdown() {
	s.backend.DisableIdleShutdown()
}

// DisableWorkspaceAutoClose 保持 workspaces hosted after their clients
// disconnect, retire, 或 fail 到 attach before creation grace expires.
// An explicitly started 服务端 hosts headless work whose creating process
// may exit; workspaces stay open until explicitly deleted 或 服务端
// shuts down. 一个 服务端 spawned 在 demand retains lifecycle-based reaping.
func (s *Server) DisableWorkspaceAutoClose() {
	s.backend.DisableWorkspaceAutoClose()
}

// Backend returns 服务端's backend. Intended 为 integration tests
// 那 drive lifecycle transitions (detach, grace tuning) against live
// HTTP surface.
func (s *Server) Backend() *backend.Backend {
	return s.backend
}

// DefaultServer returns new [服务端] 用 默认 address.
func DefaultServer(cfg *config.ConfigStore) *Server {
	hostURL, err := ParseHostURL(DefaultHost())
	if err != nil {
		panic("invalid default host")
	}
	return NewServer(cfg, hostURL.Scheme, hostURL.Host)
}

// NewServer creates new [服务端] 用 given network 和 address.
func NewServer(cfg *config.ConfigStore, network, address string) *Server {
	s := new(Server)
	s.Addr = address
	s.network = network
	s.selfShutdownDone = make(chan struct{})

	// backend 是 created 用 shutdown callback 那 triggers
	// graceful 服务端 shutdown (e.g. 当 last 工作区 是
	// removed).
	s.backend = backend.New(context.Background(), cfg, func() {
		s.shutdownSelf(selfShutdownTimeout)
	})
	s.installHandler()
	if network == "tcp" {
		s.h.Addr = address
	}
	return s
}

// shutdownSelf starts graceful shutdown backend asks 为 当
// 服务端 has gone 空闲. backend can ask more than once ( 客户端's
// 请求 和 空闲 timer, say), 所以 只 first 调用 does anything.
// [服务端.Serve] waits 为 这 shutdown, 所以 it gives up after 超时 和
// closes what 是 left: 请求 那 never finishes must 不 keep
// 服务端 up 为 good.
func (s *Server) shutdownSelf(timeout time.Duration) {
	s.selfShutdown.Do(func() {
		s.selfShutdownStarted.Store(true)
		go func() {
			defer close(s.selfShutdownDone)
			slog.Info("Shutting down server...")
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if err := s.Shutdown(ctx); err != nil {
				slog.Error("Failed to shutdown server", "error", err)
				_ = s.Close()
			}
		}()
	})
}

// installHandler builds protocol/router around s.backend 和
// assigns resulting http.服务端 到 s.h. Extracted 从
// [NewServer] 所以 测试 harnesses can wire 服务端 around
// pre-constructed backend.
func (s *Server) installHandler() {
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	c := &controllerV1{backend: s.backend, server: s}
	mux := http.NewServeMux()
	for _, e := range c.endpoints() {
		mux.HandleFunc(e.Method()+" "+e.Path(), e.Handler())
	}
	mux.HandleFunc("GET /v1/docs/", c.handleDocsIndex)
	mux.HandleFunc("GET /v1/docs/openapi.json", c.handleDocsSpec)
	// http.服务端.Shutdown waits 为 活跃 requests without ever
	// cancelling them, 所以 requests derive 从 上下文 Shutdown can
	// 取消 itself.
	baseCtx, cancel := context.WithCancel(context.Background())
	s.cancelRequests = cancel
	s.h = &http.Server{
		Protocols:   &p,
		Handler:     s.recoverHandler(s.loggingHandler(s.crossOriginHandler(mux))),
		BaseContext: func(net.Listener) context.Context { return baseCtx },
	}
}

// 处理器 returns 服务端's HTTP 处理器. Exposed 所以 测试 harnesses
// can wrap it 在 httptest.服务端 without going through
// production listener setup.
func (s *Server) Handler() http.Handler {
	return s.h.Handler
}

// Serve accepts incoming connections 在 listener. When 服务端
// stops itself, Serve returns once 那 shutdown has finished rather than
// 作为 soon 作为 listener closes, 所以 caller 那 exits 在 return does
// 不 cut off requests shutdown 是 still draining.
func (s *Server) Serve(ln net.Listener) error {
	err := s.h.Serve(ln)
	if errors.Is(err, ErrServerClosed) && s.selfShutdownStarted.Load() {
		<-s.selfShutdownDone
	}
	return err
}

// ListenAndServe starts 服务端 和 begins accepting connections.
func (s *Server) ListenAndServe() error {
	if s.ln != nil {
		return fmt.Errorf("server already started")
	}
	ln, removedStale, err := listen(s.network, s.Addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.Addr, err)
	}
	if removedStale && s.logger != nil {
		s.logger.Warn("Removed stale socket before binding", "address", s.Addr)
	}
	// Start 空闲 clock now 那 clients can reach us: 服务端 那
	// never gets 工作区 must 不 outlive 客户端 那 spawned it.
	// 一个 服务端 started explicitly 用 `crush 服务端` has 空闲 shutdown
	// disabled, 所以 这 是 no-op there.
	s.backend.ArmIdleShutdown()
	return s.Serve(ln)
}

func (s *Server) closeListener() {
	if s.ln != nil {
		s.ln.Close()
		s.ln = nil
	}
}

// Close force closes all listeners 和 connections. Unlike [服务端.Shutdown]
// it leaves hosted workspaces running.
func (s *Server) Close() error {
	defer func() { s.closeListener() }()
	return s.h.Close()
}

// Shutdown tears down hosted workspaces, then cancels 在-flight requests
// 和 gracefully shuts down 服务端, waiting 为 their handlers 到
// return. ctx bounds both. 空闲-shutdown 路径 relies 在 teardown
// being no-op 用 none left.
func (s *Server) Shutdown(ctx context.Context) error {
	defer func() { s.closeListener() }()
	err := s.backend.ShutdownWorkspaces(ctx)
	// With workspaces gone no 请求 has work left 到 do, 和 one
	// blocked 在 它的 上下文, such 作为 running shell command, would
	// otherwise hold shutdown until ctx expires 和 then 是
	// abandoned. Cancelling 只 now keeps 事件 streams open 到 投递
	// the teardown's final events.
	s.cancelRequests()
	return errors.Join(err, s.h.Shutdown(ctx))
}

// ReloadConfig re-reads 服务端 和 hosted 工作区 configs 从 disk.
func (s *Server) ReloadConfig(ctx context.Context) error {
	return s.backend.ReloadConfig(ctx)
}

func (s *Server) logDebug(r *http.Request, msg string, args ...any) {
	if s.logger != nil {
		s.logger.With(
			slog.String("method", r.Method),
			slog.String("url", r.URL.String()),
			slog.String("remote_addr", r.RemoteAddr),
		).Debug(msg, args...)
	}
}

func (s *Server) logError(r *http.Request, msg string, args ...any) {
	if s.logger != nil {
		s.logger.With(
			slog.String("method", r.Method),
			slog.String("url", r.URL.String()),
			slog.String("remote_addr", r.RemoteAddr),
		).Error(msg, args...)
	}
}
