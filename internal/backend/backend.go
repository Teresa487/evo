// Package backend 提供与传输方式无关的操作：管理 workspace、session、agent、
// 权限和事件。它被 HTTP 服务端、ACP 等协议层调用。
//
// 从产物反推：
//
//	HTTP /v1/...（server）
//	  → backend.Workspace.AgentRun
//	  → app.App → agent.Run
//
// 多客户端状态也在这一层：每个 client_id 的 currentSessionID、SSE 流计数、
// holdTimer，以及「同一目录共享 workspace / 不同目录隔离」。
package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/dirid"
	"github.com/charmbracelet/crush/internal/projects"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/google/uuid"
)

// Common errors returned 通过 backend operations.
var (
	ErrWorkspaceNotFound       = errors.New("workspace not found")
	ErrLSPClientNotFound       = errors.New("LSP client not found")
	ErrAgentNotInitialized     = errors.New("agent coordinator not initialized")
	ErrAgentBusy               = errors.New("agent is busy with a run")
	ErrPathRequired            = errors.New("path is required")
	ErrInvalidPermissionAction = errors.New("invalid permission action")
	ErrUnknownCommand          = errors.New("unknown command")
	ErrInvalidClientID         = errors.New("invalid client_id")
	ErrClientNotAttached       = errors.New("client not attached")
	ErrWorkspaceClosing        = errors.New("workspace closing")
	ErrServerShuttingDown      = errors.New("server is shutting down")
	ErrServerNotIdle           = errors.New("server is hosting live workspaces")
	ErrClientRetired           = errors.New("client has been retired")
	ErrInvalidTheme            = errors.New("invalid host theme")
	ErrChannelOptInMismatch    = errors.New("requested channels differ from the existing workspace; channels are an explicit opt-in and are not shared across duplicate creates")
	ErrPromptOverrideMismatch  = errors.New("another Crush session already has this directory open with different prompts; --prompt only applies to the session that opens it, so close the others and try again")
)

// DefaultCreateGrace 是 窗口 在 which 客户端 must open SSE
// 流 after creating 工作区 before 它的 creation hold 是
// released. An explicitly started 服务端 disables 这 和 它的 other
// automatic 工作区 reaping 用 [Backend.DisableWorkspaceAutoClose],
// allowing work 到 continue headlessly after creating 客户端 exits.
// Exposed 作为 package variable 所以 tests can shorten it.
var DefaultCreateGrace = 30 * time.Second

// DefaultIdleShutdownDelay 是 how long 服务端 stays alive after 它的
// last 工作区 是 released before it shuts itself down. delay
// exists 所以 客户端 那 closes one 会话 和 opens another moments
// later ( same 目录 或 different one) reuses still-running
// 服务端 instead 的 racing 它的 shutdown: 用 immediate shutdown
// new 客户端 can attach 到 — 或 创建 工作区 在 — 服务端 那 是
// already tearing down, 和 then observe 它的 coder agent 作为 "offline".
// Any 工作区 创建 within 窗口 cancels pending shutdown.
// 一个 服务端 那 never hosts 工作区 waits it out 从 moment it
// starts listening (see [Backend.ArmIdleShutdown]).
// Overridable via CRUSH_SERVER_IDLE_TIMEOUT (seconds; 0 restores the
// old shut-down-immediately behavior).
var DefaultIdleShutdownDelay = 60 * time.Second

// DefaultDetachGrace 是 how long 客户端's claim 在 工作区 survives
// after 它的 last SSE 流 drops without explicit release. 流
// 是 客户端's refcount claim, 所以 tearing 工作区 down instant
// it closes turns any momentary drop (a hiccup, a suspended laptop, a proxy
// 超时) into permanently lost 工作区: 客户端's reconnect comes
// back milliseconds later 到 ID 服务端 no longer knows. 一个 客户端
// 那 released 它的 claim first ( clean exit) skips grace. Overridable
// via CRUSH_SERVER_DETACH_GRACE (seconds; 0 restores immediate teardown).
var DefaultDetachGrace = 10 * time.Second

// ShutdownFunc 是 called 当 backend needs 到 trigger 服务端
// shutdown (e.g. 当 last 工作区 是 removed).
type ShutdownFunc func()

// Backend provides transport-agnostic business logic 为 Crush
// 服务端. 它 manages workspaces 和 delegates 到 [app.App] services.
//
// Locking order: 当 both [Backend.mu] 和 [工作区.clientsMu] 是
// held 在 once, [Backend.mu] 是 acquired first. Detach paths
// ([detachStream], [releaseHoldLocked], [expireHold]) 只 hold
// [工作区.clientsMu] briefly, drop it, then 调用 [teardown] which
// takes [Backend.mu] (和 then re-takes [工作区.clientsMu] 到
// re-check 那 工作区 has 不 been re-claimed). 这 avoids
// AB/BA hazard 用 [CreateWorkspace], which holds [Backend.mu]
// while calling [registerClient] 所以 那 工作区 cannot 是 torn
// down beneath it.
type Backend struct {
	workspaces *csync.Map[string, *Workspace]
	// pathIndex maps resolved absolute 工作区 路径 到 它的
	// 工作区 ID. Reads 和 writes 是 serialised via mu 所以
	// concurrent CreateWorkspace calls 在 same 路径 deduplicate
	// deterministically.
	pathIndex map[string]string
	// pending counts CreateWorkspace calls 那 have committed 到
	// slow initialization 路径 (配置/db/app setup) 但 have 不 yet
	// registered their 工作区 在 map. 它 是 guarded 通过 mu.
	// teardown must observe pending == 0 在 addition 到 empty
	// 工作区 map before triggering 服务端 shutdown: otherwise
	// teardown 的 last live 工作区 could race ahead 的
	// concurrent 创建 — which releases mu during 它的 slow init — 和
	// shut whole 服务端 down out 从 under 工作区 being
	// born.
	pending int
	// shutdownTimer, 当 non-nil, 是 armed 空闲-shutdown timer
	// waiting out lingerDelay before it shuts 服务端 down. 它 是
	// guarded 通过 mu 和 cancelled moment new 创建 arrives.
	shutdownTimer *time.Timer
	// idleShutdown, 当 true ( 默认), lets 服务端 shut itself
	// down once it goes 空闲: arm 在 startup 和 linger after
	// last 工作区 leaves both stop it. 它 是 cleared 在 服务端
	// started explicitly 用 `crush 服务端`, which runs until it 是
	// stopped; one spawned 在 demand 通过 客户端 keeps 默认.
	// Guarded 通过 mu.
	idleShutdown bool
	// autoClose, 当 true ( 默认), lets 客户端 claims expire 和
	// releases workspaces once their last claim 是 gone. 它 是 cleared 在
	// 服务端 started explicitly 用 `crush 服务端`, where headless
	// caller may exit after starting work 那 must keep running. Guarded
	// 通过 mu.
	autoClose bool
	// closing latches decision 到 exit. Every site 那 commits 到
	// a shutdown sets it while still holding mu, because shutdownFn has
	// 到 运行 unlocked; CreateWorkspace refuses once it 是 set. That
	// makes shutdown-vs-创建 decision atomic: 创建 can never
	// 是 handed 工作区 在 process 那 是 already leaving.
	// [Backend.ShutdownWorkspaces] sets it too, 为 external stop.
	closing bool
	// teardowns counts 工作区 shutdowns [Backend.teardown] has taken
	// over 但 不 finished. Their workspaces have left map, 所以
	// [Backend.ShutdownWorkspaces] waits 在 这 too rather than let
	// process exit halfway through one. Add happens under mu right after a
	// 工作区 是 unregistered, which cannot happen once ShutdownWorkspaces
	// has latched closing 和 emptied map, 所以 no Add races 它的 Wait.
	teardowns sync.WaitGroup
	// retired holds IDs 的 clients 那 announced their exit via
	// RetireClient. Creates 从 retired 客户端 是 refused, which 是 what
	// lets 客户端 release 工作区 whose ID it never learned.
	//
	// Entries 是 never pruned, 和 deliberately 所以: 条目 是 exactly
	// what refuses 创建 那 was already 在 wire 当 客户端
	// said goodbye, 和 HTTP gives no ordering between two requests, 所以
	// there 是 no moment 在 which 服务端 can prove none 是 still coming.
	// Dropping 条目 到 save memory would reintroduce orphaned
	// 工作区 这 mechanism exists 到 prevent. cost 是 one UUID per
	// 客户端 process, 和 服务端 只 outlives 它的 clients 为 作为 long
	// 作为 sessions keep arriving inside 空闲-shutdown 窗口.
	retired map[string]struct{}
	mu      sync.Mutex

	cfg         *config.ConfigStore
	ctx         context.Context
	shutdownFn  ShutdownFunc
	createGrace time.Duration
	lingerDelay time.Duration
	detachGrace time.Duration
}

// clientState tracks one 客户端's claim 在 工作区.
//
//   - streams counts number 的 live SSE 事件 streams 客户端
//     currently has open against 工作区.
//   - holdTimer 是 non-nil 在 two timer-held states: 客户端
//     created 工作区 但 has 不 yet attached SSE 流
//     (fires after createGrace), 或 客户端's last SSE 流 dropped
//     without an explicit release (fires after detachGrace, giving the
//     客户端's reconnect loop 窗口 到 re-attach). Either way
//     timer releases claim 当 it expires.
//   - currentSessionID records which 会话 这 客户端 是 currently
//     viewing. Empty string means 客户端 has no 会话 selected
//     (e.g. landing screen). Cleared automatically 当
//     clientState 条目 是 removed.
//   - sessionReported records whether 客户端 has ever reported
//     current 会话 via [Backend.SetCurrentSession]. Clients 那
//     never 报告 one (e.g. API clients predating 会话 reporting)
//     是 indistinguishable 从 landing-screen clients 通过 会话 ID
//     alone; 这 flag lets SSE filter fail open 为 them.
//   - released marks 那 客户端 gave up 它的 claim explicitly while
//     streams were still open, which clean exit does. final 流
//     detach then tears down immediately instead 的 waiting out
//     detach grace 为 客户端 那 是 不 coming back.
//
// streams 和 holdTimer 是 mutually exclusive 在 practice ( hold
// timer 是 stopped moment SSE 流 attaches), 但 both being
// zero/nil means 条目 has been released 和 should 是 removed.
type clientState struct {
	streams          int
	holdTimer        *time.Timer
	currentSessionID string
	sessionReported  bool
	released         bool
}

// 工作区 represents running [app.App] 工作区 用 它的
// associated resources 和 state.
type Workspace struct {
	*app.App
	ID     string
	Path   string
	Cfg    *config.ConfigStore
	Env    []string
	Skills *skills.Manager

	// resolvedPath 是 路径 used 作为 dedup key 在
	// Backend.pathIndex. 它 是 filepath.EvalSymlinks(filepath.Abs(路径))
	// 用 fallback 到 cleaned absolute 路径.
	resolvedPath string
	// dirID identifies 目录 found 在 resolvedPath 当
	// 工作区 was created, 或 是 zero (unknown) ID. 它 dedupes
	// spellings 的 目录 那 resolvedPath does 不 unify. 它
	// 是 不 re-validated: 这 assumes 目录 outlives
	// 工作区, 所以 它的 inode 或 文件 ID 是 不 reused 通过 another one.
	dirID dirid.ID

	// ctx 是 工作区-scoped 运行 上下文. 它 是 derived 从
	// backend 上下文 在 CreateWorkspace 和 lives 为
	// lifetime 的 工作区; 取消 tears it down. Agent runs
	// dispatched 在 behalf 的 这 工作区 是 bound 到 ctx 所以
	// their lifetime 是 owned 通过 工作区, 不 通过 any single
	// 客户端's HTTP 请求.
	ctx    context.Context
	cancel context.CancelFunc

	// runMu guards closing 和 gates 派发 的 new agent runs.
	// closing 是 set 通过 Shutdown 所以 no new runs 是 accepted once
	// teardown has begun. runWG tracks dispatched agent goroutines
	// 所以 Shutdown can wait 为 them 到 return before app 清理.
	runMu   sync.Mutex
	closing bool
	runWG   sync.WaitGroup

	// agentMu guards embedded App.AgentCoordinator once 工作区
	// 是 published. Every 客户端 的 工作区 shares 那 coordinator,
	// 所以 it 是 只 created while missing ([工作区.initAgent]) 和 never
	// replaced; readers take it through [工作区.Coordinator].
	agentMu sync.RWMutex

	// clientsMu guards clients. 它 是 held 只 briefly (no IO).
	clientsMu sync.Mutex
	// clients tracks each 客户端's claim 在 这 工作区. Refcount
	// 是 derived 值: len(clients).
	clients map[string]*clientState

	// sessions announces 当 客户端 的 工作区 changes 会话
	// it views. 它 是 apart 从 app's events, which agent's
	// sessions 发布 在, 所以 那 it reaches streams 在 它的 own.
	sessions     *pubsub.Broker[proto.ClientSession]
	sessionsOnce sync.Once

	// themeMu guards hostTheme. 它 是 held 只 briefly (no IO) 和 never
	// together 用 clientsMu.
	themeMu sync.Mutex
	// hostTheme 是 theme host application set 为 every 客户端 的
	// 工作区, 或 nil. 它 是 never persisted 和 是 不 tied 到
	// setting 客户端: it stays until host clears it 或 工作区
	// goes away. 存储 themes 是 never mutated, 只 replaced.
	hostTheme *proto.HostTheme

	// rootCache memoizes [Backend.RootSessionID] resolutions, keyed 通过
	// 会话 ID. 会话 parentage 是 fixed 在 creation, 所以 entries
	// never go stale; 缓存 keeps DB 读取 per 事件 per 客户端
	// off SSE hot 路径.
	rootCache sync.Map // map[string]string

	// shutdownFn 是 function invoked 通过 [Backend.teardown] 到
	// release 工作区's underlying resources. 它 defaults 到
	// embedded [app.App.Shutdown]; tests may override it 到 avoid
	// driving a full [app.App] through shutdown.
	shutdownFn func()
}

// invokeShutdown calls 工作区 shutdown 钩子 如果 set, falling
// back 到 工作区 [工作区.Shutdown] wrapper 当 不.
func (w *Workspace) invokeShutdown() {
	if w.shutdownFn != nil {
		w.shutdownFn()
		return
	}
	if w.App != nil {
		w.Shutdown()
	}
}

// Shutdown tears 工作区 down 在 order 那 是 safe 为
// agent runs whose lifetime 是 bound 到 工作区 上下文. 它
// shadows promoted [app.App.Shutdown] 所以 callers reaching
// ws.Shutdown() always observe 这 ordering:
//
//  1. 标记 工作区 closing 所以 no new agent runs 是 accepted.
//  2. 取消 工作区 运行 上下文 所以 any dispatched goroutine
//     那 has 不 yet registered 它的 per-会话 取消 still observes
//     cancellation.
//  3. 取消 活跃 coordinator work 为 runs 那 already registered
//     their per-会话 取消 function.
//  4. Wait 为 dispatched agent goroutines 到 return.
//  5. 运行 embedded [app.App.Shutdown] 清理 (DB, LSP, etc).
//
// CancelAll 是 idempotent, 所以 second 调用 inside app.App.Shutdown
// 是 harmless; important guarantee 是 那 取消 -> CancelAll ->
// runWG.Wait completes before embedded 清理 touches DB.
func (w *Workspace) Shutdown() {
	w.runMu.Lock()
	w.closing = true
	w.runMu.Unlock()

	if w.cancel != nil {
		w.cancel()
	}
	if coord := w.Coordinator(); coord != nil {
		coord.CancelAll()
	}
	w.runWG.Wait()
	if w.App != nil {
		w.App.Shutdown()
	}
}

// New creates new [Backend]. returned backend routes MCP 渠道
// events into hosted workspaces 为 它的 lifetime (until ctx 是
// canceled); see [Backend.startChannelRouter].
func New(ctx context.Context, cfg *config.ConfigStore, shutdownFn ShutdownFunc) *Backend {
	b := &Backend{
		workspaces:   csync.NewMap[string, *Workspace](),
		pathIndex:    make(map[string]string),
		retired:      make(map[string]struct{}),
		cfg:          cfg,
		ctx:          ctx,
		shutdownFn:   shutdownFn,
		createGrace:  DefaultCreateGrace,
		lingerDelay:  idleShutdownDelayFromEnv(),
		detachGrace:  durationFromEnv("CRUSH_SERVER_DETACH_GRACE", DefaultDetachGrace),
		idleShutdown: true,
		autoClose:    true,
	}
	b.startChannelRouter()
	return b
}

// idleShutdownDelayFromEnv returns 空闲-shutdown delay, honoring
// CRUSH_SERVER_IDLE_TIMEOUT override (在 seconds; 0 disables lingering).
func idleShutdownDelayFromEnv() time.Duration {
	return durationFromEnv("CRUSH_SERVER_IDLE_TIMEOUT", DefaultIdleShutdownDelay)
}

// durationFromEnv reads whole number 的 seconds 从 named
// environment variable, falling back 到 def 当 it 是 unset 或
// unparseable. Zero 是 meaningful 值 为 both lifecycle windows it
// configures, 所以 it 是 accepted.
func durationFromEnv(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return def
}

// SetCreateGrace overrides 创建-grace 窗口. Intended 为 tests
// 那 need short timeouts.
func (b *Backend) SetCreateGrace(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.createGrace = d
}

// SetDetachGrace overrides how long 客户端's claim survives after 它的
// last SSE 流 drops. 一个 值 <= 0 restores tear-down-immediately
// behavior. Intended 为 tests.
func (b *Backend) SetDetachGrace(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.detachGrace = d
}

// SetIdleShutdownDelay overrides how long 服务端 lingers after 它的
// last 工作区 是 released before shutting down. 一个 值 <= 0 restores
// shut-down-immediately behavior. Intended 为 tests.
func (b *Backend) SetIdleShutdownDelay(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lingerDelay = d
}

// DisableIdleShutdown turns off automatic 空闲 shutdown: 服务端
// keeps running 用 no workspaces 和 no creates 在 flight until it 是
// stopped explicitly. 一个 服务端 spawned 在 demand 通过 客户端 shuts down
// once 它的 客户端 是 gone, 但 one started explicitly 用 `crush 服务端`
// was asked 为 在 purpose. Intended 到 是 called before 服务端 starts
// serving.
func (b *Backend) DisableIdleShutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.idleShutdown = false
	b.cancelShutdownLocked()
}

// DisableWorkspaceAutoClose 保持 hosted workspaces open after their
// 客户端 claims go away. Creation holds no longer expire, interrupted
// streams no longer tear 工作区 down, 和 retired clients do 不
// release their claims. Workspaces remain hosted until they 是 deleted
// explicitly 或 服务端 shuts down, which 是 what explicitly
// started 服务端 needs 为 headless work. Intended 到 是 called before
// 服务端 starts serving.
func (b *Backend) DisableWorkspaceAutoClose() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.autoClose = false
}

// GetWorkspace retrieves 工作区 通过 ID.
func (b *Backend) GetWorkspace(id string) (*Workspace, error) {
	ws, ok := b.workspaces.Get(id)
	if !ok {
		return nil, ErrWorkspaceNotFound
	}
	return ws, nil
}

// ListWorkspaces returns all running workspaces.
func (b *Backend) ListWorkspaces() []proto.Workspace {
	workspaces := []proto.Workspace{}
	for _, ws := range b.workspaces.Seq2() {
		workspaces = append(workspaces, workspaceToProto(ws))
	}
	return workspaces
}

// CreateWorkspace initializes new 工作区 从 given
// parameters, 或 returns existing 工作区 如果 one already exists 为
// same 目录 (first-wins semantics).
//
// args.ClientID must 是 valid UUID identifying calling 客户端;
// resulting 工作区 registers creation hold 在 behalf 的 那
// 客户端 which 是 released either 通过 first SSE attach (which
// converts it into 流 claim) 或 通过 grace 窗口 expiring.
func (b *Backend) CreateWorkspace(args proto.Workspace) (*Workspace, proto.Workspace, error) {
	if args.Path == "" {
		return nil, proto.Workspace{}, ErrPathRequired
	}
	clientID, err := validateClientID(args.ClientID)
	if err != nil {
		return nil, proto.Workspace{}, err
	}

	key, err := resolveWorkspaceKey(args.Path)
	if err != nil {
		return nil, proto.Workspace{}, fmt.Errorf("failed to resolve workspace path: %w", err)
	}
	dirID, _ := dirid.Of(key)

	b.mu.Lock()
	if err := b.admitLocked(clientID); err != nil {
		b.mu.Unlock()
		return nil, proto.Workspace{}, err
	}
	// 一个 客户端 是 arriving: 取消 any pending 空闲 shutdown 所以 we never
	// hand back 工作区 在 服务端 那 是 about 到 tear itself down.
	b.cancelShutdownLocked()
	if existingID, ok := b.lookupLocked(key, dirID); ok {
		if ws, found := b.workspaces.Get(existingID); found {
			// Hold b.mu while registering: teardown also
			// acquires b.mu before tearing 工作区
			// down, 所以 这 guarantees 工作区 we
			// return cannot 是 torn out 从 under us
			// between lookup 和 registerClient. 锁 order
			// here 是 b.mu -> ws.clientsMu.
			if err := joinMismatch(ws, args); err != nil {
				b.mu.Unlock()
				return nil, proto.Workspace{}, err
			}
			logFirstWinsMismatch(ws, args)
			b.registerClient(ws, clientID)
			b.mu.Unlock()
			return ws, workspaceToProto(ws), nil
		}
		// pathIndex referenced 工作区 那 has since been
		// removed; clean stale 条目 和 fall through.
		delete(b.pathIndex, key)
	}
	// Commit 到 slow creation 路径. 标记 这 创建 作为 pending
	// while mu 是 still held 所以 teardown 那 runs during
	// unlocked init below cannot observe empty backend 和 shut
	// 服务端 down. deferred decrement runs after 工作区 has
	// been registered (或 创建 has failed), keeping invariant
	// 那 pending 只 drops once 工作区 是 visible 在 map.
	b.pending++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.pending--
		// If 这 创建 ended up registering nothing (it failed, 或
		// deduped onto existing 工作区 那 has since gone) 和
		// it was holding last teardown back, 服务端 may now 是
		// 空闲 用 no pending work. Arm 空闲-shutdown timer here 所以
		// failed 创建 racing last teardown does 不 leak
		// empty 服务端 那 plain teardown already declined 到 reap.
		shutdownNow := b.scheduleShutdownIfIdleLocked()
		b.mu.Unlock()
		if shutdownNow {
			slog.Info("No workspaces remain after create settled, shutting down server...")
			b.shutdownFn()
		}
	}()

	if err := config.ValidatePromptNames(args.Prompts); err != nil {
		return nil, proto.Workspace{}, err
	}

	id := uuid.New().String()
	cfg, err := config.Init(context.Background(), args.Path, args.DataDir, args.Debug)
	if err != nil {
		return nil, proto.Workspace{}, fmt.Errorf("failed to initialize config: %w", err)
	}

	cfg.Overrides().EnabledChannels = args.Channels
	cfg.Overrides().PermissionMode = proto.ProtoModeToPermission(args.PermissionMode)
	cfg.Overrides().Prompts = args.Prompts

	if err := createDotCrushDir(cfg.Config().Options.DataDirectory); err != nil {
		return nil, proto.Workspace{}, fmt.Errorf("failed to create data directory: %w", err)
	}

	// Record project 作为 local mode does, 所以 `crush projects` 和
	// `crush stats --all` know about it. list 是 只 bookkeeping, 所以
	// failing 到 更新 it must 不 keep 工作区 从 opening.
	if err := projects.Register(args.Path, cfg.Config().Options.DataDirectory); err != nil {
		slog.Warn("Failed to register project", "path", args.Path, "error", err)
	}

	conn, err := db.Connect(b.ctx, cfg.Config().Options.DataDirectory, db.WithDataDirLock(true))
	if err != nil {
		return nil, proto.Workspace{}, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Discover skills once per 工作区, before app.New. backend
	// hosts multiple workspaces concurrently, 所以 manager 是
	// constructed WITHOUT WithGlobalMirror 到 prevent last-writer-wins
	// cross-talk between workspaces.
	discoveryCfg := skillsDiscoveryConfig(cfg)
	allSkills, activeSkills, skillStates := skills.DiscoverFromConfig(discoveryCfg)
	skillsMgr := skills.NewManager(
		allSkills, activeSkills, skillStates,
		skills.WithResolvedPaths(discoveryCfg.ResolvePaths()),
		skills.WithWorkingDir(discoveryCfg.WorkingDir),
	)

	appWorkspace, err := app.New(b.ctx, conn, cfg, skillsMgr)
	if err != nil {
		return nil, proto.Workspace{}, fmt.Errorf("failed to create app workspace: %w", err)
	}

	wsCtx, wsCancel := context.WithCancel(b.ctx)
	ws := &Workspace{
		App:          appWorkspace,
		ID:           id,
		Path:         args.Path,
		Cfg:          cfg,
		Env:          args.Env,
		Skills:       skillsMgr,
		resolvedPath: key,
		dirID:        dirID,
		ctx:          wsCtx,
		cancel:       wsCancel,
		clients:      make(map[string]*clientState),
	}
	scopePermissionPrompts(ws)

	b.mu.Lock()
	// Re-check admission: 客户端 may have retired while slow
	// init above ran 用 b.mu released, 和 registering claim 为
	// 客户端 那 has already announced 它的 exit would strand
	// 工作区, 或 服务端 may have been told 到 stop meanwhile.
	if err := b.admitLocked(clientID); err != nil {
		b.mu.Unlock()
		ws.invokeShutdown()
		return nil, proto.Workspace{}, err
	}
	// Re-check index under 锁: concurrent caller may have
	// won race between initial unlock 和 here.
	if existingID, ok := b.lookupLocked(key, dirID); ok {
		if existing, found := b.workspaces.Get(existingID); found {
			// Register under b.mu 所以 teardown cannot 运行
			// between lookup 和 registerClient. 锁 order
			// 是 b.mu -> ws.clientsMu.
			if err := joinMismatch(existing, args); err != nil {
				b.mu.Unlock()
				ws.invokeShutdown()
				return nil, proto.Workspace{}, err
			}
			logFirstWinsMismatch(existing, args)
			b.registerClient(existing, clientID)
			b.mu.Unlock()
			ws.invokeShutdown()
			return existing, workspaceToProto(existing), nil
		}
		delete(b.pathIndex, key)
	}
	b.workspaces.Set(id, ws)
	b.pathIndex[key] = id
	// Register originating 客户端's hold while still holding
	// b.mu 所以 工作区 是 observable 用 它的 claim 从
	// moment it appears 在 index.
	b.registerClient(ws, clientID)
	b.mu.Unlock()

	// Clients warn their users themselves, 从 version every
	// returned 工作区 carries.
	if args.Version != "" && args.Version != version.Version {
		slog.Warn(
			"Client/server version mismatch",
			"client", args.Version,
			"server", version.Version,
		)
	}

	return ws, workspaceToProto(ws), nil
}

// skillsDiscoveryConfig adapts *配置.ConfigStore 到
// skills.DiscoveryConfig 那 DiscoverFromConfig consumes.
func skillsDiscoveryConfig(cfg *config.ConfigStore) skills.DiscoveryConfig {
	opts := cfg.Config().Options
	var paths, disabled []string
	if opts != nil {
		paths = opts.SkillsPaths
		disabled = opts.DisabledSkills
	}
	var resolver func(string) (string, error)
	if r := cfg.Resolver(); r != nil {
		resolver = r.ResolveValue
	}
	return skills.DiscoveryConfig{
		SkillsPaths:    paths,
		DisabledSkills: disabled,
		WorkingDir:     cfg.WorkingDir(),
		Resolver:       resolver,
	}
}

// skillStatesToProto converts internal skill discovery states into the
// wire format.
func skillStatesToProto(states []*skills.SkillState) []proto.SkillState {
	if len(states) == 0 {
		return nil
	}
	out := make([]proto.SkillState, len(states))
	for i, s := range states {
		entry := proto.SkillState{
			Name:  s.Name,
			Path:  s.Path,
			State: proto.SkillDiscoveryState(s.State),
		}
		if s.Err != nil {
			entry.Error = s.Err.Error()
		}
		out[i] = entry
	}
	return out
}

// AttachClient registers new SSE 流 为 given 客户端 在
// 工作区. 流's deferred 清理 must 调用 DetachClient 用
// same arguments 到 release claim.
//
// lookup 和 clients-map mutation 是 performed under
// [Backend.mu] 所以 那 AttachClient cannot race 用 [Backend.teardown]:
// teardown also holds [Backend.mu] while removing 工作区 从
// b.workspaces, 所以 once AttachClient observes 工作区 和 takes
// ws.clientsMu (under b.mu), no concurrent teardown 可以 succeed without
// re-checking (now non-empty) clients map. 锁 order 是
// canonical b.mu -> ws.clientsMu.
func (b *Backend) AttachClient(workspaceID, clientID string) error {
	if _, err := validateClientID(clientID); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	ws, ok := b.workspaces.Get(workspaceID)
	if !ok {
		return ErrWorkspaceNotFound
	}

	ws.clientsMu.Lock()
	defer ws.clientsMu.Unlock()
	cs, ok := ws.clients[clientID]
	if !ok {
		// Defensive: SSE attach without prior CreateWorkspace 通过
		// 这 客户端 still installs 流 claim 所以 流
		// stays alive 为 它的 duration.
		ws.clients[clientID] = &clientState{streams: 1}
		return nil
	}
	if cs.holdTimer != nil {
		cs.holdTimer.Stop()
		cs.holdTimer = nil
	}
	cs.streams++
	return nil
}

// DetachClient releases one SSE 流's hold 在 工作区. When
// 客户端 has no streams left 和 no pending creation hold, 它的 claim
// either enters detach grace — giving reconnecting 客户端 time 到
// re-attach — 或, 如果 grace 是 disabled 或 客户端 already released
// 它的 claim, 是 removed, tearing 工作区 down once refcount
// hits zero.
func (b *Backend) DetachClient(workspaceID, clientID string) {
	ws, ok := b.workspaces.Get(workspaceID)
	if !ok {
		return
	}
	b.detachStream(ws, clientID)
}

// admitLocked reports whether clientID may still take claim 在 这
// 服务端. 它 must 是 called 用 b.mu held, which 是 what makes
// answer atomic 用 respect 到 shutdown latch 和 到 RetireClient.
func (b *Backend) admitLocked(clientID string) error {
	if b.closing {
		return ErrServerShuttingDown
	}
	if _, ok := b.retired[clientID]; ok {
		return ErrClientRetired
	}
	return nil
}

// RetireClient records 那 客户端 has exited 和 releases every claim it
// holds, across every 工作区. 它 是 authoritative "这 客户端 是
// gone" signal, 和 reason 客户端 never has 到 guess whether 创建
// whose 响应 it lost left 工作区 behind: either 创建 landed
// first 和 这 调用 releases 它的 claim, 或 it arrives later 和 是
// refused, registering nothing.
//
// When 工作区 auto-close 是 disabled, 客户端's claims 是 still
// released, 但 它的 workspaces 和 their runs keep going 为 headless work.
//
// Idempotent. 锁 order 是 canonical b.mu -> ws.clientsMu; teardowns
// 为 workspaces 客户端 was last 在 运行 after b.mu 是 released 和
// re-check under both locks.
func (b *Backend) RetireClient(clientID string) error {
	if _, err := validateClientID(clientID); err != nil {
		return err
	}

	b.mu.Lock()
	if b.retired == nil {
		b.retired = make(map[string]struct{})
	}
	b.retired[clientID] = struct{}{}
	autoClose := b.autoClose
	var orphaned []*Workspace
	viewing := make(map[*Workspace]string)
	for _, ws := range b.workspaces.Seq2() {
		ws.clientsMu.Lock()
		if cs, ok := ws.clients[clientID]; ok {
			if cs.holdTimer != nil {
				cs.holdTimer.Stop()
			}
			forgetClient(ws, clientID, cs)
			viewing[ws] = cs.currentSessionID
			if len(ws.clients) == 0 {
				orphaned = append(orphaned, ws)
			}
		}
		ws.clientsMu.Unlock()
	}
	b.mu.Unlock()

	if !autoClose {
		return nil
	}
	for ws, sessionID := range viewing {
		ws.cancelUnwatchedRun(sessionID)
	}
	for _, ws := range orphaned {
		b.teardown(ws)
	}
	return nil
}

// releaseHold releases creation hold 为 客户端, 如果 any. 活跃
// 流 claims 是 unaffected. Idempotent: returns nil 如果
// 工作区 或 客户端's hold no longer exist.
func (b *Backend) releaseHold(workspaceID, clientID string) error {
	if _, err := validateClientID(clientID); err != nil {
		return err
	}
	ws, ok := b.workspaces.Get(workspaceID)
	if !ok {
		return nil
	}
	b.releaseHoldLocked(ws, clientID)
	return nil
}

// registerClient installs (idempotently) given 客户端's claim 在
// 工作区 和 starts grace timer 如果 条目 是 fresh.
//
// 一个 duplicate 创建 从 客户端 whose claim 是 timer-held (waiting 到
// attach, 或 waiting out detach grace) re-arms it 为 full 创建
// grace 和 supersedes any earlier explicit release: 客户端 是 coming
// back. The re-arm installs a fresh clientState rather than resetting the
// old timer, 所以 already-fired timer racing 这 调用 fails expireHold's
// identity check instead 的 killing new claim.
func (b *Backend) registerClient(ws *Workspace, clientID string) {
	ws.clientsMu.Lock()
	defer ws.clientsMu.Unlock()
	if old, ok := ws.clients[clientID]; ok {
		old.released = false
		if old.holdTimer == nil {
			// Live streams hold claim; nothing 到 re-arm.
			return
		}
		old.holdTimer.Stop()
		ws.clients[clientID] = b.newHeldClient(ws, clientID, old.currentSessionID, old.sessionReported, b.createGrace)
		return
	}
	ws.clients[clientID] = b.newHeldClient(ws, clientID, "", false, b.createGrace)
}

// newHeldClient builds clientState whose claim 是 held 只 通过 timer
// 那 releases it after grace. Callers must hold ws.clientsMu.
func (b *Backend) newHeldClient(ws *Workspace, clientID, sessionID string, reported bool, grace time.Duration) *clientState {
	cs := &clientState{currentSessionID: sessionID, sessionReported: reported}
	cs.holdTimer = time.AfterFunc(grace, func() {
		b.expireHold(ws, clientID, cs)
	})
	return cs
}

// expireHold 是 body 的 grace timer. 它 runs 在 它的 own
// goroutine 和 races against AttachClient/releaseHold; timer
// stays valid 只 while 条目's holdTimer still points 在 it.
func (b *Backend) expireHold(ws *Workspace, clientID string, timer *clientState) {
	ws.clientsMu.Lock()
	cs, ok := ws.clients[clientID]
	if !ok || cs != timer || cs.holdTimer == nil || cs.streams > 0 {
		ws.clientsMu.Unlock()
		return
	}
	cs.holdTimer = nil
	forgetClient(ws, clientID, cs)
	viewing := cs.currentSessionID
	teardown := len(ws.clients) == 0
	ws.clientsMu.Unlock()
	b.claimEnded(ws, viewing, teardown)
}

// claimEnded cancels 运行 departed 客户端 was viewing 如果 nobody else
// views it, 和 tears 工作区 down once no claims 是 left. With
// 工作区 auto-close disabled it does neither: 工作区 和 它的
// runs belong 到 服务端 和 carry 在 headless.
func (b *Backend) claimEnded(ws *Workspace, viewing string, teardown bool) {
	b.mu.Lock()
	autoClose := b.autoClose
	b.mu.Unlock()
	if !autoClose {
		return
	}
	ws.cancelUnwatchedRun(viewing)
	if teardown {
		b.teardown(ws)
	}
}

// releaseHoldLocked releases 客户端's claim 在 explicit 删除. With
// 工作区 auto-close disabled, 工作区 left 用 no clients 是 closed
// 通过 删除 从 any 客户端, since none holds it any more.
func (b *Backend) releaseHoldLocked(ws *Workspace, clientID string) {
	ws.clientsMu.Lock()
	cs, ok := ws.clients[clientID]
	if !ok {
		unclaimed := len(ws.clients) == 0
		ws.clientsMu.Unlock()
		b.mu.Lock()
		autoClose := b.autoClose
		b.mu.Unlock()
		if unclaimed && !autoClose {
			b.teardown(ws)
		}
		return
	}
	if cs.holdTimer != nil {
		cs.holdTimer.Stop()
		cs.holdTimer = nil
	}
	teardown := false
	viewing := ""
	if cs.streams == 0 {
		forgetClient(ws, clientID, cs)
		viewing = cs.currentSessionID
		teardown = len(ws.clients) == 0
	} else {
		// 客户端 gave up 它的 claim while streams 是 still open, which
		// 是 what clean exit looks like. Remember it 所以 final detach
		// skips the reconnect grace.
		cs.released = true
	}
	ws.clientsMu.Unlock()
	ws.cancelUnwatchedRun(viewing)
	if teardown {
		b.teardown(ws)
	}
}

func (b *Backend) detachStream(ws *Workspace, clientID string) {
	b.mu.Lock()
	grace := b.detachGrace
	b.mu.Unlock()

	ws.clientsMu.Lock()
	cs, ok := ws.clients[clientID]
	if !ok {
		ws.clientsMu.Unlock()
		return
	}
	if cs.streams > 0 {
		cs.streams--
	}
	teardown := false
	viewing := ""
	if cs.streams == 0 && cs.holdTimer == nil {
		if grace > 0 && !cs.released {
			// 流 dropped without 客户端 releasing 它的 claim, 所以
			// treat it 作为 interruption rather than exit: hold
			// 工作区 under timer long enough 为 客户端's
			// reconnect 到 re-attach (AttachClient stops timer).
			cs.holdTimer = time.AfterFunc(grace, func() {
				b.expireHold(ws, clientID, cs)
			})
		} else {
			forgetClient(ws, clientID, cs)
			viewing = cs.currentSessionID
			teardown = len(ws.clients) == 0
		}
	}
	ws.clientsMu.Unlock()
	b.claimEnded(ws, viewing, teardown)
}

// cancelUnwatchedRun cancels 运行 在 sessionID unless 客户端 left 在
// 工作区 是 still viewing 那 会话. Callers pass 会话
// 客户端 was viewing 当 它的 claim 在 工作区 ended, 和 must 不
// hold w.clientsMu. 权限 prompts 和 questions 是 只 delivered 到
// 会话's viewers, 所以 运行 nobody views would otherwise go 在
// headless 和 wait forever 在 first 提示词 it raises; quitting crush
// 在 local mode cancels 它的 runs same way. 一个 客户端 inside 它的 detach
// grace still counts 作为 viewer, since it 是 expected back.
func (w *Workspace) cancelUnwatchedRun(sessionID string) {
	if sessionID == "" || w.App == nil || w.AgentCoordinator == nil {
		return
	}
	w.clientsMu.Lock()
	for _, cs := range w.clients {
		if cs.currentSessionID == sessionID {
			w.clientsMu.Unlock()
			return
		}
	}
	w.clientsMu.Unlock()
	if w.AgentCoordinator.IsSessionBusy(sessionID) {
		slog.Info("Cancelling run in a session no client is viewing",
			"workspace", w.ID, "session_id", sessionID)
	}
	w.AgentCoordinator.Cancel(sessionID)
}

// publishClientSession tells 工作区's 事件 streams which 会话
// 客户端 now views. 它 是 called 用 clientsMu held; publishing never
// blocks.
func publishClientSession(ws *Workspace, clientID, sessionID string) {
	ws.clientSessionBroker().Publish(pubsub.UpdatedEvent, proto.ClientSession{ClientID: clientID, SessionID: sessionID})
}

// clientSessionBroker returns 总线 的 客户端 会话 changes,
// creating it 在 first use.
func (ws *Workspace) clientSessionBroker() *pubsub.Broker[proto.ClientSession] {
	ws.sessionsOnce.Do(func() { ws.sessions = pubsub.NewBroker[proto.ClientSession]() })
	return ws.sessions
}

// forgetClient removes 客户端's claim 在 工作区. Its 会话 是
// cleared 为 事件 streams first, 所以 那 they do 不 keep showing
// 客户端 那 是 gone. 它 是 called 用 clientsMu held.
func forgetClient(ws *Workspace, clientID string, cs *clientState) {
	if cs.currentSessionID != "" {
		publishClientSession(ws, clientID, "")
	}
	delete(ws.clients, clientID)
}

// ClientSessions returns 会话 each 客户端 的 工作区 views,
// 为 clients 那 have one. 它 是 sent 到 事件 流 作为 it
// opens, 所以 客户端 那 starts after another one learns 它的 会话.
func (b *Backend) ClientSessions(workspaceID string) ([]proto.ClientSession, error) {
	ws, ok := b.workspaces.Get(workspaceID)
	if !ok {
		return nil, ErrWorkspaceNotFound
	}
	ws.clientsMu.Lock()
	defer ws.clientsMu.Unlock()
	var out []proto.ClientSession
	for id, cs := range ws.clients {
		if cs.currentSessionID != "" {
			out = append(out, proto.ClientSession{ClientID: id, SessionID: cs.currentSessionID})
		}
	}
	return out, nil
}

// teardown removes 工作区 从 index, shuts down 它的
// underlying [app.App], 和 triggers 服务端 shutdown 如果 it was
// last 工作区 alive.
//
// Callers reach teardown after observing len(ws.clients) == 0 while
// holding ws.clientsMu 和 then releasing it. Between 那 release
// 和 b.mu.锁 below, concurrent CreateWorkspace may have
// re-registered 客户端 (CreateWorkspace holds b.mu while doing 所以,
// 所以 it 是 mutually exclusive 用 这 critical section). teardown
// re-checks under both locks (在 canonical b.mu -> ws.clientsMu
// order) 和 aborts 如果 工作区 has been re-claimed.
func (b *Backend) teardown(ws *Workspace) {
	b.mu.Lock()
	ws.clientsMu.Lock()
	if len(ws.clients) > 0 {
		// Race: CreateWorkspace re-registered 客户端
		// between detach 路径 dropping ws.clientsMu 和 us
		// taking b.mu. Abort: 工作区 是 still alive.
		ws.clientsMu.Unlock()
		b.mu.Unlock()
		return
	}
	ws.clientsMu.Unlock()
	if !b.unregisterLocked(ws) {
		b.mu.Unlock()
		return
	}
	// 这 调用 now owns shutdown; count it while mu 是 still held 所以
	// concurrent ShutdownWorkspaces waits 为 it (see b.teardowns).
	b.teardowns.Add(1)
	// Arm (或, 用 lingering disabled, 请求) 空闲 shutdown. 它
	// 只 proceeds once there 是 genuinely nothing left: no live
	// workspaces AND no 创建 在 flight. Deferring via linger lets
	// 客户端 returning moments later reuse 这 服务端 instead 的 racing
	// 它的 shutdown.
	shutdownNow := b.scheduleShutdownIfIdleLocked()
	b.mu.Unlock()

	// Done 是 deferred 所以 shutdown 那 panics, which HTTP layer
	// recovers 从, cannot leave 服务端 stop waiting 在 it.
	func() {
		defer b.teardowns.Done()
		ws.invokeShutdown()
	}()

	if shutdownNow {
		slog.Info("Last workspace removed, shutting down server...")
		b.shutdownFn()
	}
}

// lookupLocked returns ID 的 工作区 registered 为
// 目录 在 key. 它 matches resolved 路径 first 和 then, 当 dir
// 是 known, 目录 itself: EvalSymlinks leaves some spellings 的
// one 目录 apart (another case 在 case-insensitive filesystem; 在
// Windows junction, subst drive 或 UNC 路径), 和 each 的 them must join
// same 工作区 rather than open 它的 data 目录 second time.
// 它 does no IO 和 must 是 called 用 b.mu held.
func (b *Backend) lookupLocked(key string, dir dirid.ID) (string, bool) {
	if id, ok := b.pathIndex[key]; ok {
		return id, true
	}
	for id, ws := range b.workspaces.Seq2() {
		if dir.Same(ws.dirID) {
			return id, true
		}
	}
	return "", false
}

// unregisterLocked removes ws 从 backend 和 reports whether 这
// caller now owns 它的 shutdown. 它 must 是 called 用 b.mu held.
func (b *Backend) unregisterLocked(ws *Workspace) bool {
	if !b.workspaces.CompareAndDelete(ws.ID, ws) {
		return false
	}
	if id, ok := b.pathIndex[ws.resolvedPath]; ok && id == ws.ID {
		delete(b.pathIndex, ws.resolvedPath)
	}
	return true
}

// scheduleShutdownIfIdleLocked decides what 到 do about 服务端 shutdown
// after 工作区 count 或 pending-创建 change. 它 must 是 called
// 用 b.mu held.
//
// 它 returns true 只 当 caller should shut 服务端 down
// synchronously (after releasing b.mu) — 那 是, 当 服务端 是 空闲
// 和 lingering 是 disabled (lingerDelay <= 0). When lingering 是 enabled
// it instead arms one-shot timer 那 re-checks idleness after
// lingerDelay 和 shuts down then, 和 returns false. When 服务端 是
// 不 空闲 ( 工作区 是 live 或 创建 是 在 flight) it does
// nothing 和 returns false.
//
// Returning true also latches [Backend.closing], since caller has 到
// release b.mu before it can 运行 shutdownFn.
func (b *Backend) scheduleShutdownIfIdleLocked() (shutdownNow bool) {
	if b.shutdownFn == nil || b.closing || !b.idleShutdown {
		return false
	}
	if b.workspaces.Len() != 0 || b.pending != 0 {
		return false
	}
	if b.lingerDelay <= 0 {
		b.closing = true
		return true
	}
	if b.shutdownTimer == nil {
		b.shutdownTimer = time.AfterFunc(b.lingerDelay, b.maybeShutdown)
	}
	return false
}

// ArmIdleShutdown starts 空闲-shutdown timer 在 服务端 那 是 不
// hosting anything yet. 它 是 meant 到 是 called once 服务端 是
// reachable. timer 是 otherwise armed 只 当 工作区 或
// 创建 goes away, 所以 服务端 whose spawning 客户端 never got 作为 far 作为
// creating 工作区 (it was interrupted, 或 failed first) would 运行
// forever. first 创建 cancels timer, 和 [Backend.maybeShutdown]
// re-checks idleness 当 it fires.
//
// With lingering disabled (lingerDelay <= 0) fresh 服务端 would exit
// before 它的 客户端 could 创建 anything, 所以 it waits out 创建
// grace instead.
func (b *Backend) ArmIdleShutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.shutdownFn == nil || b.closing || b.shutdownTimer != nil || !b.idleShutdown {
		return
	}
	if b.workspaces.Len() != 0 || b.pending != 0 {
		return
	}
	delay := b.lingerDelay
	if delay <= 0 {
		delay = b.createGrace
	}
	b.shutdownTimer = time.AfterFunc(delay, b.maybeShutdown)
}

// cancelShutdownLocked stops any armed 空闲-shutdown timer. 它 must 是
// called 用 b.mu held.
func (b *Backend) cancelShutdownLocked() {
	if b.shutdownTimer != nil {
		b.shutdownTimer.Stop()
		b.shutdownTimer = nil
	}
}

// maybeShutdown 是 空闲-shutdown timer callback. 它 shuts 服务端
// down 只 如果 it 是 still 空闲 当 linger 窗口 elapses; any
// 创建 那 arrived 在 meantime cancelled timer (或 bumped
// pending / 工作区 count), 所以 这 re-check makes linger
// race-free. Deciding 到 exit latches [Backend.closing] under same
// 锁, 所以 创建 arriving 在 gap before shutdownFn runs 是
// refused instead 的 being initialized 在 departing process.
func (b *Backend) maybeShutdown() {
	b.mu.Lock()
	b.shutdownTimer = nil
	idle := b.workspaces.Len() == 0 && b.pending == 0
	if idle {
		b.closing = true
	}
	fn := b.shutdownFn
	b.mu.Unlock()
	if idle && fn != nil {
		slog.Info("Server idle, shutting down...")
		fn()
	}
}

// DeleteWorkspace 是 public 条目 point used 通过 HTTP 删除
// 处理器. 它 releases named 客户端's creation hold; live streams
// 从 same 客户端 remain attached 和 continue holding
// 工作区 open until their own deferred DetachClient runs.
func (b *Backend) DeleteWorkspace(id, clientID string) error {
	return b.releaseHold(id, clientID)
}

// SetCurrentSession records which 会话 given 客户端 是
// currently viewing within 工作区. Passing empty sessionID
// clears 客户端's current-会话 条目 (e.g. 客户端 has
// returned 到 landing screen). recorded 会话 scopes
// 客户端's SSE 流: 权限 和 question prompts 是 只
// delivered 到 clients viewing 会话 那 raised them.
//
// 客户端 must 是 actually attached — i.e. 它的 [clientState] 条目
// must exist 和 have 在 least one live 流. 一个 bare creation hold
// (streams == 0) 是 rejected 用 [ErrClientNotAttached]. 这
// guards against zombie writes 从 客户端 那 has detached 和
// against ghost presence 从 hold-只 客户端 那 never opened
// SSE 流.
func (b *Backend) SetCurrentSession(ctx context.Context, workspaceID, clientID, sessionID string) error {
	if _, err := validateClientID(clientID); err != nil {
		return err
	}
	ws, ok := b.workspaces.Get(workspaceID)
	if !ok {
		return ErrWorkspaceNotFound
	}
	ws.clientsMu.Lock()
	cs, ok := ws.clients[clientID]
	if !ok || cs.streams == 0 {
		// No 条目, 或 hold-只 (no live 流): refuse
		// 写入. presence record 这 是 meant 到 feed
		// should 只 reflect clients 那 can actually observe
		// 会话 events.
		ws.clientsMu.Unlock()
		return ErrClientNotAttached
	}
	changed := cs.currentSessionID != sessionID
	cs.currentSessionID = sessionID
	cs.sessionReported = true
	if changed {
		publishClientSession(ws, clientID, sessionID)
	}
	ws.clientsMu.Unlock()

	// 一个 提示词 那 was raised while no 客户端 viewed 会话
	// would otherwise stay unanswered until 运行 是 cancelled.
	// Re-发布 any pending 提示词 为 会话 客户端 just
	// switched 到; per-客户端 流 filter limits delivery 到
	// 那 会话's viewers.
	if sessionID != "" {
		b.republishPendingPrompts(ctx, ws, sessionID)
	}
	return nil
}

// ClientCurrentSession returns 会话 given 客户端 是
// currently viewing within 工作区, 或 "" 当 客户端 has
// no 会话 selected (e.g. landing screen) 或 是 unknown.
// second return 值 reports whether 客户端 has ever reported
// 会话: 客户端 那 never did (e.g. API 客户端 predating
// 会话 reporting) 是 treated differently 从 one 那 explicitly
// cleared 它的 selection.
func (b *Backend) ClientCurrentSession(workspaceID, clientID string) (string, bool) {
	ws, ok := b.workspaces.Get(workspaceID)
	if !ok {
		return "", false
	}
	ws.clientsMu.Lock()
	defer ws.clientsMu.Unlock()
	cs, ok := ws.clients[clientID]
	if !ok {
		return "", false
	}
	return cs.currentSessionID, cs.sessionReported
}

// AttachedClients returns number 的 clients currently viewing
// sessionID 在 given 工作区. Only clients 用 在 least one live
// SSE 流 (streams > 0) AND matching currentSessionID 是 counted;
// pure creation holds do 不 contribute. Returns [ErrWorkspaceNotFound]
// 如果 工作区 是 unknown.
func (b *Backend) AttachedClients(workspaceID, sessionID string) (int, error) {
	ws, ok := b.workspaces.Get(workspaceID)
	if !ok {
		return 0, ErrWorkspaceNotFound
	}
	return ws.AttachedClientsForSession(sessionID), nil
}

// AttachedClientsForSession returns number 的 clients 在 这
// 工作区 whose currentSessionID equals sessionID 和 which have 在
// least one live SSE 流. Hold-只 clients (streams == 0) do 不
// contribute. Acquires 工作区's [clientsMu] briefly;
// returned count 是 point-在-time snapshot.
func (w *Workspace) AttachedClientsForSession(sessionID string) int {
	w.clientsMu.Lock()
	defer w.clientsMu.Unlock()
	n := 0
	for _, cs := range w.clients {
		if cs.streams > 0 && cs.currentSessionID == sessionID {
			n++
		}
	}
	return n
}

// GetWorkspaceProto returns proto representation 的 工作区.
func (b *Backend) GetWorkspaceProto(id string) (proto.Workspace, error) {
	ws, err := b.GetWorkspace(id)
	if err != nil {
		return proto.Workspace{}, err
	}
	return workspaceToProto(ws), nil
}

// VersionInfo returns 服务端 version information.
func (b *Backend) VersionInfo() proto.VersionInfo {
	return proto.VersionInfo{
		Version:   version.Version,
		Commit:    version.Commit,
		BuildID:   version.BuildID,
		GoVersion: runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

// 配置 returns 服务端-level configuration.
func (b *Backend) Config() *config.ConfigStore {
	return b.cfg
}

// ShutdownWorkspaces refuses new creates 和 tears down every hosted
// 工作区 在 parallel, 所以 interrupted work 是 recorded before exit. 它
// also waits 为 teardowns already under way, such 作为 one started 通过
// expired detach grace. 它 returns ctx's 错误, naming stragglers 在
// log, 如果 ctx ends first.
func (b *Backend) ShutdownWorkspaces(ctx context.Context) error {
	b.mu.Lock()
	b.closing = true
	b.cancelShutdownLocked()
	var owned []*Workspace
	for _, ws := range slices.Collect(b.workspaces.Seq()) {
		if b.unregisterLocked(ws) {
			owned = append(owned, ws)
		}
	}
	b.mu.Unlock()

	remaining := csync.NewMap[string, struct{}]()
	var wg sync.WaitGroup
	for _, ws := range owned {
		slog.Info("Shutting down workspace", "workspace", ws.ID)
		remaining.Set(ws.ID, struct{}{})
		wg.Go(func() {
			ws.invokeShutdown()
			remaining.Del(ws.ID)
		})
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		b.teardowns.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		slog.Warn("Gave up waiting for workspaces to shut down; their final state may not be recorded",
			"workspaces", slices.Sorted(maps.Keys(remaining.Copy())))
		return ctx.Err()
	}
}

// ShutdownIfIdle shuts 服务端 down 只 当 it 是 hosting no
// workspaces 和 has no creates 在 flight, reporting false 当 it declined
// because work 是 live.
//
// 这 是 只 shutdown 客户端 may 请求. 一个 客户端 asks 在 order 到
// replace version-mismatched 服务端, 和 it cannot check idleness itself
// without second round trip new 会话 can slip into. Deciding here,
// under 锁 creates 和 teardowns take, closes 那 窗口: answer
// 是 atomic, 和 granting it latches decision 所以 creates arriving
// afterwards 是 refused.
//
// 一个 服务端 started explicitly always declines: 用户 asked 为 it, 和
// replacement 客户端 spawns would 不 keep running way it does.
func (b *Backend) ShutdownIfIdle() bool {
	b.mu.Lock()
	if !b.idleShutdown {
		b.mu.Unlock()
		slog.Warn("Refusing shutdown request: the server was started explicitly")
		return false
	}
	live, pending := b.workspaces.Len(), b.pending
	idle := live == 0 && pending == 0
	if idle {
		b.closing = true
	}
	fn := b.shutdownFn
	b.mu.Unlock()
	if !idle {
		slog.Warn("Refusing shutdown request: server is not idle",
			"workspaces", live, "pending_creates", pending)
		return false
	}
	if fn != nil {
		fn()
	}
	return true
}

// Closing reports whether 服务端 has committed 到 shutting down.
// decision 是 final: once it reports true, it always will.
func (b *Backend) Closing() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closing
}

// resolveWorkspaceKey returns stable canonical form 的 路径 suitable
// 为 use 作为 dedup key. 它 applies filepath.Abs, then attempts
// filepath.EvalSymlinks; because EvalSymlinks errors 在 non-existent
// paths, it falls back 到 cleaned absolute 路径 在 那 case.
func resolveWorkspaceKey(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// validateClientID returns trimmed UUID string 或 错误 如果
// input 是 empty 或 不 valid UUID.
func validateClientID(id string) (string, error) {
	if id == "" {
		return "", ErrInvalidClientID
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidClientID, err)
	}
	return id, nil
}

func workspaceToProto(ws *Workspace) proto.Workspace {
	cfg := ws.Cfg.Config()
	// 报告 mode 权限 service actually enforces rather
	// than startup override. override 是 one-shot seed 那
	// app.New reads 在 construction; runtime toggles reach 只
	// service, 所以 reporting override would show 客户端 "normal"
	// while 服务端 auto-approves everything.
	//
	// ws.Permissions 是 safe 到 dereference here: it 是 promoted 从
	// the embedded app.App, which CreateWorkspace always populates
	// alongside ws.Cfg, 和 ws.Cfg 是 already dereferenced above.
	out := proto.Workspace{
		ID:             ws.ID,
		Path:           ws.Path,
		PermissionMode: proto.PermissionModeToProto(ws.Permissions.PermissionMode()),
		Channels:       ws.Cfg.Overrides().EnabledChannels,
		Prompts:        ws.Cfg.Overrides().Prompts,
		DataDir:        cfg.Options.DataDirectory,
		Debug:          cfg.Options.Debug,
		Config:         cfg,
		Version:        version.Version,
	}
	if ws.Skills != nil {
		out.Skills = skillStatesToProto(ws.Skills.States())
	}
	return out
}

// joinMismatch reports why duplicate 创建 may 不 join existing.
// Channels must match exactly. Prompts 只 have 到 match 当 caller
// asked 为 some: plain 客户端 may join 工作区 opened 用 --提示词,
// 但 --提示词 existing 工作区 would ignore must 不 是 dropped
// without a word.
func joinMismatch(existing *Workspace, args proto.Workspace) error {
	if !stringSlicesEqual(existing.Cfg.Overrides().EnabledChannels, args.Channels) {
		return ErrChannelOptInMismatch
	}
	if len(args.Prompts) > 0 && !maps.Equal(existing.Cfg.Overrides().Prompts, args.Prompts) {
		return ErrPromptOverrideMismatch
	}
	return nil
}

// logFirstWinsMismatch emits a debug line whenever a second
// CreateWorkspace 在 same resolved 路径 arrives 用 flags 那
// differ 从 originating 工作区. existing 工作区 wins;
// incoming flags 是 silently ignored.
//
// comparison 是 done against incoming args 作为 caller sent
// them — including empty/zero values — rather than after defaulting.
// 这 means 那, 为 example, second caller who omits DataDir
// while first set one 将 still log mismatch.
func logFirstWinsMismatch(existing *Workspace, args proto.Workspace) {
	existingCfg := existing.Cfg.Config()
	existingChannels := existing.Cfg.Overrides().EnabledChannels
	// Compare internal modes 所以 unset 请求 ("") matches
	// normalized 默认 ("normal") instead 的 spuriously logging.
	existingMode := existing.Cfg.Overrides().PermissionMode
	requestedMode := proto.ProtoModeToPermission(args.PermissionMode)
	envDiffers := !stringSlicesEqual(existing.Env, args.Env)
	if existingMode == requestedMode &&
		existingCfg.Options.Debug == args.Debug &&
		existingCfg.Options.DataDirectory == args.DataDir &&
		!envDiffers &&
		stringSlicesEqual(existingChannels, args.Channels) &&
		maps.Equal(existing.Cfg.Overrides().Prompts, args.Prompts) {
		return
	}
	slog.Debug(
		"Workspace flag mismatch on duplicate create; first wins",
		"workspace_id", existing.ID,
		"path", existing.Path,
		"existing_permission_mode", proto.PermissionModeToProto(existingMode),
		"requested_permission_mode", proto.PermissionModeToProto(requestedMode),
		"existing_debug", existingCfg.Options.Debug,
		"requested_debug", args.Debug,
		"existing_data_dir", existingCfg.Options.DataDirectory,
		"requested_data_dir", args.DataDir,
		// Only flag the difference: the env values may hold secrets.
		"env_differs", envDiffers,
		"existing_channels", existingChannels,
		"requested_channels", args.Channels,
		"existing_prompts", existing.Cfg.Overrides().Prompts,
		"requested_prompts", args.Prompts,
	)
}

// stringSlicesEqual reports whether 和 b contain same strings
// 在 same order. nil 和 empty 是 treated 作为 equal.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
