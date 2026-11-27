// Package mcp provides functionality 为 managing 模型 上下文 Protocol (MCP)
// clients within the Crush application.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/oauth"
	mcpoauth "github.com/charmbracelet/crush/internal/oauth/mcp"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

// parseLevel converts MCP logging level string 到 slog.Level.
// entire MCP logging feature 是 deprecated per SEP-2577 但 remains
// functional; servers may still send log notifications during the
// deprecation 窗口.
func parseLevel(level string) slog.Level {
	switch level {
	case "info":
		return slog.LevelInfo
	case "notice":
		return slog.LevelInfo
	case "warning":
		return slog.LevelWarn
	default:
		return slog.LevelDebug
	}
}

// ClientSession wraps mcp.ClientSession 用 上下文 取消 function 所以
// 那 上下文 created during 会话 establishment 是 properly cleaned up
// 在 close.
type ClientSession struct {
	*mcp.ClientSession
	cancel       context.CancelFunc
	oauthHandler *mcpoauth.Handler
	// 渠道 reports whether 这 服务端 是 活跃 渠道 (it declared
	// claude/渠道 能力 和 was opted 在 via --channels 或
	// channel_enabled 在 配置).
	channel bool
	// channelCapable reports whether 服务端 declared claude/渠道
	// 能力, whether 或 不 it was opted 在.
	channelCapable bool
}

// Close cancels 会话 上下文 和 then closes underlying 会话.
func (s *ClientSession) Close() error {
	s.cancel()
	if s.oauthHandler != nil {
		s.oauthHandler.Close()
	}
	return s.ClientSession.Close()
}

var (
	sessions = csync.NewMap[string, *ClientSession]()
	states   = csync.NewMap[string, ClientInfo]()
	authURLs = csync.NewMap[string, *mcpoauth.Handler]()
	broker   = pubsub.NewBroker[Event]()
	initOnce sync.Once
	initDone = make(chan struct{})

	// initStarted records whether Initialize has been armed. WaitForInit 只
	// blocks once initialization 是 expected; coordinators built outside app
	// startup never arm it 和 所以 must 不 wait forever. initArmedAt anchors
	// WaitForInitBudget's 期限: budget 是 measured 从 arming, 不
	// 从 each 调用, 所以 sequential waiters share one 期限 instead 的
	// stacking fresh budgets.
	initMu      sync.Mutex
	initStarted bool
	initArmedAt time.Time

	// refs counts the apps sharing the process-global MCP clients (see
	// Acquire). 一个 服务端 hosts one app per 工作区, 所以 只 Close
	// 那 releases last reference may close sessions.
	refsMu sync.Mutex
	refs   int

	// renewMus serializes lazy 会话 renewals per 服务端 所以 concurrent 工具
	// calls cannot race 到 rebuild same 会话.
	renewMusMu sync.Mutex
	renewMus   = map[string]*sync.Mutex{}

	// gens hands out per-服务端 generation number. teardown bumps
	// 服务端's generation; init goroutine captures it 在 launch 和 只
	// commits 它的 会话 如果 generation 是 still current. 一个 配置
	// change 那 restarts 服务端 mid-connect thus invalidates
	// 在-flight attempt instead 的 letting it register stale 会话.
	gens = csync.NewMap[string, uint64]()

	// suppressMus serializes browser-suppression per 服务端 所以 只 one
	// remote (服务端-driven) OAuth flow 是 活跃 为 服务端 在 time.
	suppressMus = csync.NewMap[string, *sync.Mutex]()

	// newSession creates 客户端 会话. 它 是 seam 所以 tests can exercise
	// renewal concurrency without spawning real 传输.
	newSession = createSession
)

// suppressBrowserKey marks 上下文 作为 requesting OAuth 处理器 不
// open local 浏览器; caller surfaces 授权 URL itself.
type suppressBrowserKey struct{}

// ArmInit marks 那 MCP initialization 是 expected 所以 WaitForInit blocks
// until it completes. 调用 这 synchronously before launching Initialize 在
// goroutine; otherwise WaitForInit could observe 不-yet-started state 和
// return early, letting 工具 list 是 读取 before MCP tools register.
func ArmInit() {
	initMu.Lock()
	initStarted = true
	initArmedAt = time.Now()
	initMu.Unlock()
}

// DisarmInit undoes ArmInit 所以 WaitForInit stops blocking 和 returns
// immediately. 它 exists 为 tests 在 other packages 那 arm gate
// without ever running Initialize 和 must 不 leak permanently-blocking
// gate into rest 的 测试 二进制. Production code never needs it.
func DisarmInit() {
	initMu.Lock()
	initStarted = false
	initMu.Unlock()
}

// renewLock returns per-服务端 互斥锁 used 到 serialize 会话 renewals,
// creating it 在 first use.
func renewLock(name string) *sync.Mutex {
	renewMusMu.Lock()
	defer renewMusMu.Unlock()
	mu, ok := renewMus[name]
	if !ok {
		mu = &sync.Mutex{}
		renewMus[name] = mu
	}
	return mu
}

// State represents current state 的 MCP 客户端
type State int

const (
	StateDisabled State = iota
	StateStarting
	StateConnected
	StateError
	StateNeedsAuth
)

func (s State) String() string {
	switch s {
	case StateDisabled:
		return "disabled"
	case StateStarting:
		return "starting"
	case StateConnected:
		return "connected"
	case StateError:
		return "error"
	case StateNeedsAuth:
		return "needs auth"
	default:
		return "unknown"
	}
}

// EventType represents type 的 MCP 事件
type EventType uint

const (
	EventStateChanged EventType = iota
	EventToolsListChanged
	EventPromptsListChanged
	EventResourcesListChanged
	// EventChannelMessage 是 published 当 渠道 服务端 pushes
	// notifications/claude/渠道 事件. ChannelMessage carries rendered,
	// escaped <渠道> element ready 为 injection into 会话.
	EventChannelMessage
)

// 事件 represents 事件 在 MCP system
type Event struct {
	Type   EventType
	Name   string
	State  State
	Error  error
	Counts Counts
	// ChannelMessage 是 set 只 为 EventChannelMessage: fully rendered
	// 和 escaped <渠道>...</渠道> element 到 inject into 会话.
	ChannelMessage string
}

// Counts number 的 available tools, prompts, etc.
type Counts struct {
	Tools     int
	Prompts   int
	Resources int
}

// ClientInfo holds information about MCP 客户端's state.
type ClientInfo struct {
	Name        string
	State       State
	Error       error
	Client      *ClientSession
	Counts      Counts
	ConnectedAt time.Time

	// 配置 是 configuration 服务端 last successfully connected
	// 用. Reconcile compares it against live 配置 到 decide whether
	// connected 服务端 needs restart. 它 是 recorded 通过 updateState 在
	// StateConnected 和 cleared 在 StateDisabled, 所以 it never has 到 是
	// synced 通过 hand.
	Config config.MCPConfig

	// PendingConfig 是 configuration 在-flight initialization 是
	// connecting 用. 它 是 set 在 StateStarting 所以 配置 change 那
	// arrives mid-connect 是 compared against attempt actually 在
	// progress rather than the last successful one, which would leave the
	// 服务端 skipped 作为 "starting" 和 never restarted 为 new 配置.
	PendingConfig *config.MCPConfig

	// 渠道 reports whether 这 服务端 是 活跃 渠道 (declared
	// claude/渠道 能力 和 opted 在 via --channels 或
	// channel_enabled 在 配置).
	Channel bool

	// ChannelOptIn reports whether 服务端 是 opted 在 作为 渠道 (via
	// --channels 或 channel_enabled), whatever 它的 连接 state. 它 是
	// recorded 当 connect attempt starts, 所以 渠道 那 是 still
	// starting, has errored, 或 needs auth 是 still reported 作为 one.
	ChannelOptIn bool

	// ChannelCapable reports whether 服务端 declared claude/渠道
	// 能力 在 它的 last connect, whether 或 不 it 是 opted 在, 所以
	// UI can point out servers 那 could 是 channels 但 were never enabled.
	ChannelCapable bool
}

// SubscribeEvents returns 渠道 为 MCP events.
//
// 渠道 消息 events (EventChannelMessage) 是 excluded: they carry no
// 工作区 或 会话 identity, 和 MCP 总线 是 process-global. Without
// 这 filter, every 工作区 那 calls SubscribeEvents would receive every
// other 工作区's 渠道 events — cross-工作区 injection 路径.
// Consumers 那 投递 渠道 messages use SubscribeChannelEvents 和 是
// responsible 为 scoping each 事件 到 workspaces 那 declared 和
// opted 在 originating 服务端.
func SubscribeEvents(ctx context.Context) <-chan pubsub.Event[Event] {
	raw := broker.Subscribe(ctx)
	filtered := make(chan pubsub.Event[Event], 64)
	go func() {
		defer close(filtered)
		for ev := range raw {
			if ev.Payload.Type == EventChannelMessage {
				continue
			}
			select {
			case filtered <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return filtered
}

// SubscribeChannelEvents returns 渠道 carrying 只 渠道 消息
// events (EventChannelMessage). MCP 总线 是 process-global, 所以 these
// events 是 不 scoped 到 any 工作区: every consumer must check 那
// originating 服务端 是 declared 在 target 工作区's MCP 配置 和
// opted 在 (ChannelOptIn) before delivering, otherwise one 工作区's
// 渠道 messages leak into another — injection 路径 SubscribeEvents
// filters out.
func SubscribeChannelEvents(ctx context.Context) <-chan pubsub.Event[Event] {
	raw := broker.Subscribe(ctx)
	channelOnly := make(chan pubsub.Event[Event], 64)
	go func() {
		defer close(channelOnly)
		for ev := range raw {
			if ev.Payload.Type != EventChannelMessage {
				continue
			}
			select {
			case channelOnly <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return channelOnly
}

// GetStates returns current state 的 all MCP clients
func GetStates() map[string]ClientInfo {
	return states.Copy()
}

// GetState returns state 的 specific MCP 客户端
func GetState(name string) (ClientInfo, bool) {
	return states.Get(name)
}

// Acquire takes reference 在 process-global MCP clients 为 app
// 那 是 about 到 start them. Pair every Acquire 用 one Close.
func Acquire() {
	refsMu.Lock()
	defer refsMu.Unlock()
	refs++
}

// Close releases reference taken 通过 Acquire. Every app 在 process
// shares MCP clients, 所以 只 调用 那 releases last reference
// closes them. 事件 总线 是 never shut down: subscriptions end 用
// their contexts, 和 app created later 在 process still needs it.
//
// 一个 Close 那 是 不 last leaves releasing app's sessions, OAuth
// handlers 和 tools 在 place until another app's Initialize replaces them
// (connectAndRegister closes 会话 it replaces) 或 last app exits.
func Close(ctx context.Context) error {
	refsMu.Lock()
	if refs > 0 {
		refs--
	}
	if refs > 0 {
		refsMu.Unlock()
		return nil
	}
	// Snapshot under 锁 所以 nothing started 通过 app 那 acquires
	// after 这 point 是 closed here.
	open, handlers := sessions.Copy(), authURLs.Copy()
	refsMu.Unlock()

	var wg sync.WaitGroup
	for name, session := range open {
		wg.Go(func() {
			done := make(chan error, 1)
			go func() {
				done <- session.Close()
			}()
			select {
			case err := <-done:
				if err != nil &&
					!errors.Is(err, io.EOF) &&
					!errors.Is(err, context.Canceled) &&
					err.Error() != "signal: killed" {
					slog.Warn("Failed to shutdown MCP client", "name", name, "error", err)
				}
			case <-ctx.Done():
			}
		})
	}
	wg.Wait()
	// Clean up any remaining OAuth handlers.
	for _, h := range handlers {
		h.Close()
	}
	return nil
}

// Initialize initializes MCP clients based 在 provided configuration.
// forceStart lists 配置-disabled servers 用 repository-scoped enabled
// override; they 是 started even though their 配置 条目 是 disabled.
// localDisabled lists servers 用 repository-scoped disabled override;
// they 是 不 started.
func Initialize(ctx context.Context, permissions permission.Service, cfg *config.ConfigStore, forceStart, localDisabled []string) {
	ArmInit()
	slog.Info("Initializing MCP clients")
	start := time.Now()

	var wg sync.WaitGroup
	// Initialize states 为 all configured MCPs
	for name, m := range cfg.Config().MCP {
		if slices.Contains(localDisabled, name) {
			updateState(name, StateDisabled, nil, nil, Counts{})
			slog.Debug("Skipping MCP disabled for this repository", "name", name)
			continue
		}
		if m.Disabled {
			if !slices.Contains(forceStart, name) {
				updateState(name, StateDisabled, nil, nil, Counts{})
				slog.Debug("Skipping disabled MCP", "name", name)
				continue
			}
			slog.Info("Starting config-disabled MCP with repository enabled override", "name", name)
		}

		// Set initial starting state
		wg.Add(1)
		goInitClient(ctx, cfg, name, m, &wg)
	}
	wg.Wait()
	initOnce.Do(func() { close(initDone) })
	// Non-interactive runs wait 为 这 到 finish before sending 提示词, 所以
	// total 是 floor 在 their startup latency. Interactive runs do 不
	// wait, 但 total still explains 当 late-arriving tools show up.
	slog.Debug("Finished initializing MCP clients", "duration", time.Since(start).Truncate(time.Millisecond).String())
}

// WaitForInit blocks until MCP initialization 是 完成, i.e. until
// Initialize has finished 和 closed initDone. If initialization was never
// armed (ArmInit was 不 called, e.g. coordinator built outside app
// startup), there 是 nothing 到 wait 为 和 这 returns nil immediately
// rather than blocking until ctx 是 cancelled.
func WaitForInit(ctx context.Context) error {
	initMu.Lock()
	started := initStarted
	initMu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-initDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// InitWaitBudget bounds how long 消息 turn waits 为 MCP
// initialization before proceeding without servers 那 have 不
// finished. 它 是 generously above healthy stdio 服务端's startup
// (uv/npx take few seconds) 但 far below per-服务端 handshake
// timeouts (15s-120s): 服务端 那 wedges mid-handshake — e.g. one
// 那 never answers SEP-2575 服务端/discover probe — must 不
// make whole app look dead while 它的 超时 runs out.
const InitWaitBudget = 10 * time.Second

// WaitForInitBudget blocks like WaitForInit, 但 只 until budget —
// measured 从 当 initialization was armed, 不 从 这 调用 —
// elapses. Anchoring 期限 在 arming means several sequential
// waiters 在 turn's 路径 (readyWg's 工具 build, then turn itself)
// share one 期限 instead 的 each stacking fresh budget while
// 服务端 是 wedged, 和 turns arriving after 期限 don't wait 在
// all. 它 returns nil both 当 initialization completed 和 当
// budget elapsed first — 在 latter case caller proceeds 用
// whatever servers have registered 所以 far, 和 stragglers appear 在
// later turn once they finish. caller's own 上下文 ending 是 still
// reported 作为 错误.
func WaitForInitBudget(ctx context.Context, budget time.Duration) error {
	initMu.Lock()
	started := initStarted
	armedAt := initArmedAt
	initMu.Unlock()
	if !started {
		return nil
	}
	waitCtx, cancel := context.WithDeadline(ctx, armedAt.Add(budget))
	defer cancel()
	if err := WaitForInit(waitCtx); err == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	slog.Warn("MCP initialization still pending after wait budget; continuing without unfinished servers", "budget", budget)
	return nil
}

// InitializeSingle initializes single MCP 客户端 通过 name.
func InitializeSingle(ctx context.Context, name string, cfg *config.ConfigStore) error {
	m, exists := cfg.Config().MCP[name]
	if !exists {
		return fmt.Errorf("mcp '%s' not found in configuration", name)
	}

	if m.Disabled {
		updateState(name, StateDisabled, nil, nil, Counts{})
		slog.Debug("Skipping disabled MCP", "name", name)
		return nil
	}

	return initClient(ctx, cfg, name, m, currentGen(name), cfg.Resolver())
}

// InitializeSingleForced starts named MCP 客户端 even 当 它的 配置
// 条目 是 disabled. Used 通过 repository-scoped "Toggle MCPs" override.
// startup counterpart 是 forceStart argument 的 Initialize, which
// replays persisted enabled overrides. 配置 files 是 不 touched.
func InitializeSingleForced(ctx context.Context, name string, cfg *config.ConfigStore) error {
	m, exists := cfg.Config().MCP[name]
	if !exists {
		return fmt.Errorf("mcp '%s' not found in configuration", name)
	}

	return initClient(ctx, cfg, name, m, currentGen(name), cfg.Resolver())
}

// AuthenticateMCP initiates OAuth flow 为 MCP 服务端 那 是 在
// StateNeedsAuth. 它 creates OAuth 处理器 (which starts local
// callback 服务端), connects 到 服务端 (which triggers browser
// auth flow 在 401), 和 transitions 到 StateConnected 在 success.
func AuthenticateMCP(ctx context.Context, cfg *config.ConfigStore, name string) error {
	m, exists := cfg.Config().MCP[name]
	if !exists {
		return fmt.Errorf("mcp '%s' not found in configuration", name)
	}

	if !m.OAuth || m.Type != config.MCPHttp {
		return fmt.Errorf("mcp '%s' does not use OAuth authentication", name)
	}

	updateState(name, StateStarting, nil, nil, Counts{}, withPending(m), withChannelOptIn(cfg, name, m))

	// 这 是 用户-initiated flow, 所以 permit interactive browser
	// authorization 处理器 otherwise withholds during startup.
	ctx = mcpoauth.WithInteractive(ctx)

	// OAuth 处理器 persists token automatically 作为 it 是
	// exchanged, 所以 successful 连接 has already saved it.
	_, err := connectAndRegister(ctx, cfg, name, m, currentGen(name), cfg.Resolver(), ChannelOptIn(m, cfg.Overrides().EnabledChannels, name))
	if err != nil {
		return err
	}
	return nil
}

// PendingAuthServer describes MCP 服务端 awaiting OAuth.
type PendingAuthServer struct {
	Name string
	URL  string
}

// MCPAuthURL returns current OAuth authorization URL 为 named
// MCP, 或 empty 如果 none 是 在 progress.
func MCPAuthURL(name string) string {
	h, ok := authURLs.Get(name)
	if !ok || h == nil {
		return ""
	}
	return h.AuthURL()
}

// PendingAuthMCPs returns MCP servers 在 StateNeedsAuth 用 their URLs.
func PendingAuthMCPs(cfg *config.ConfigStore) []PendingAuthServer {
	var pending []PendingAuthServer
	for name, info := range states.Seq2() {
		if info.State == StateNeedsAuth {
			url := ""
			if m, ok := cfg.Config().MCP[name]; ok {
				url = m.URL
			}
			pending = append(pending, PendingAuthServer{Name: name, URL: url})
		}
	}
	slices.SortFunc(pending, func(a, b PendingAuthServer) int {
		return strings.Compare(a.Name, b.Name)
	})
	return pending
}

// BeginAuth starts OAuth flow 为 服务端 在 StateNeedsAuth 但
// suppresses opening local browser; caller 是 responsible 为
// surfacing authorization URL (via [MCPAuthURL]) 到 用户. 它 returns
// finish function 那 must 是 called exactly once 用 请求
// 上下文: finish blocks until flow completes 和 returns 结果.
//
// Only one browser-suppressed flow per 服务端 may 是 在 progress.
// returned 取消 function aborts flow without waiting; use it 当
// caller's 上下文 是 cancelled.
func BeginAuth(cfg *config.ConfigStore, name string) (finish func(ctx context.Context) error, cancel context.CancelFunc, err error) {
	m, exists := cfg.Config().MCP[name]
	if !exists {
		return nil, nil, fmt.Errorf("mcp '%s' not found in configuration", name)
	}
	if !m.OAuth || m.Type != config.MCPHttp {
		return nil, nil, fmt.Errorf("mcp '%s' does not use OAuth authentication", name)
	}

	lock := suppressLock(name)
	if !lock.TryLock() {
		return nil, nil, fmt.Errorf("mcp '%s' already has an authentication in progress", name)
	}

	flowCtx, flowCancel := context.WithCancel(context.Background())
	flowCtx = mcpoauth.WithInteractive(flowCtx)
	flowCtx = context.WithValue(flowCtx, suppressBrowserKey{}, true)

	finish = func(ctx context.Context) error {
		defer lock.Unlock()
		defer flowCancel()

		done := make(chan error, 1)
		go func() {
			done <- runAuthFlow(flowCtx, cfg, name, m)
		}()

		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			flowCancel()
			<-done
			return ctx.Err()
		}
	}
	return finish, flowCancel, nil
}

// runAuthFlow executes OAuth connect 为 BeginAuth 用 browser
// suppression enabled 在 freshly created 处理器.
func runAuthFlow(ctx context.Context, cfg *config.ConfigStore, name string, m config.MCPConfig) error {
	updateState(name, StateStarting, nil, nil, Counts{}, withPending(m), withChannelOptIn(cfg, name, m))
	_, err := connectAndRegister(ctx, cfg, name, m, currentGen(name), cfg.Resolver(), ChannelOptIn(m, cfg.Overrides().EnabledChannels, name))
	return err
}

// suppressLock returns per-服务端 互斥锁 used 到 serialize
// browser-suppressed OAuth flows, creating it 在 first use.
func suppressLock(name string) *sync.Mutex {
	mu, ok := suppressMus.Get(name)
	if !ok {
		mu = &sync.Mutex{}
		suppressMus.Set(name, mu)
	}
	return mu
}

// initClient initializes single MCP 客户端 用 given configuration.
// gen 是 服务端 generation captured 当 attempt was launched;
// resulting 会话 是 只 committed 如果 generation 是 still current, 所以
// 配置 change 那 restarts 服务端 mid-connect discards 这 attempt.
func initClient(ctx context.Context, cfg *config.ConfigStore, name string, m config.MCPConfig, gen uint64, resolver config.VariableResolver) error {
	// OAuth MCPs without usable cached token require 用户 interaction
	// (browser auth). If cached token exists 用 access token
	// (even 如果 expired), try connecting first 所以 SDK can attempt
	// silent refresh. Only defer 到 UI 如果 no token 是 available 在
	// all 或 token 是 structurally invalid (empty access token).
	if m.OAuth && m.Type == config.MCPHttp && !hasUsableToken(m.OAuthToken) {
		if m.OAuthToken != nil {
			clearOAuthToken(cfg, name)
		}
		updateState(name, StateNeedsAuth, nil, nil, Counts{}, withChannelOptIn(cfg, name, m))
		clearMCPData(name)
		slog.Info("MCP server requires OAuth authentication", "name", name)
		return nil
	}

	updateState(name, StateStarting, nil, nil, Counts{}, withPending(m), withChannelOptIn(cfg, name, m))
	_, err := connectAndRegister(ctx, cfg, name, m, gen, resolver, ChannelOptIn(m, cfg.Overrides().EnabledChannels, name))
	if err != nil {
		// If OAuth MCP fails because saved token 是 no longer
		// valid (e.g. refresh token expired 或 revoked) 或 no token
		// could 是 obtained, clear stale token 和 提示词 用户
		// 到 re-authenticate instead 的 leaving 服务端 stuck 在
		// StateError.
		if m.OAuth && m.Type == config.MCPHttp && isOAuthInitErr(err) {
			if m.OAuthToken != nil {
				clearOAuthToken(cfg, name)
			}
			updateState(name, StateNeedsAuth, nil, nil, Counts{})
			slog.Info("MCP OAuth token is no longer valid, re-authentication required", "name", name, "error", err)
			return nil
		}
		return err
	}
	return nil
}

// connectAndRegister creates 会话, lists tools 和 prompts,
// registers them 在 global state, 和 transitions 到 StateConnected.
// Returns 会话 所以 callers can perform post-processing (e.g.
// 令牌 persistence).
//
// gen 是 generation captured 当 这 attempt was launched. If
// 服务端 was torn down since (generation bumped), freshly built 会话
// 是 closed 和 discarded instead 的 being registered over whatever
// newer attempt 是 doing. 这 是 what makes 配置 change 那 lands
// mid-connect converge 在 latest 配置 rather than stale one.
func connectAndRegister(ctx context.Context, cfg *config.ConfigStore, name string, m config.MCPConfig, gen uint64, resolver config.VariableResolver, channelOptIn bool) (*ClientSession, error) {
	session, err := createSession(ctx, cfg, name, m, resolver, channelOptIn)
	if err != nil {
		return nil, err
	}

	// 一个 teardown ran while we were connecting: newer attempt owns 这
	// 服务端 now. Bail before writing 到 any shared registry 所以 we don't
	// clobber newer attempt's registrations; just drop our own 会话.
	if currentGen(name) != gen {
		slog.Debug("Discarding stale MCP session after config change", "name", name)
		closeSession(name, session)
		return nil, context.Canceled
	}

	toolCount, err := registerSessionTools(ctx, cfg, name, session)
	if err != nil {
		slog.Error("Error listing tools", "error", err)
		updateState(name, StateError, err, nil, Counts{})
		closeSession(name, session)
		return nil, err
	}

	prompts, err := getPrompts(ctx, session)
	if err != nil {
		slog.Error("Error listing prompts", "error", err)
		updateState(name, StateError, err, nil, Counts{})
		closeSession(name, session)
		return nil, err
	}

	// Re-check before publishing: 如果 teardown landed during registration
	// newer attempt owns registries now, 所以 leave them 和 our 会话
	// alone rather than overwriting 它的 state.
	if currentGen(name) != gen {
		slog.Debug("Discarding stale MCP session after config change", "name", name)
		closeSession(name, session)
		return nil, context.Canceled
	}

	updatePrompts(name, prompts)
	// Close 会话 这 one replaces. Apps share sessions 和 只
	// last app's Close walks map, 所以 会话 dropped 从 it here
	// would never 是 closed. Close it off 这 路径 所以 slow child cannot
	// hold up Initialize.
	if old, ok := sessions.Swap(name, session); ok && old != session {
		go closeSession(name, old)
	}

	updateState(name, StateConnected, nil, session, Counts{
		Tools:   toolCount,
		Prompts: len(prompts),
	}, withConfig(m))

	return session, nil
}

// persistOAuthToken saves OAuth token 从 会话 到 global
// 配置 所以 it survives restarts.

// SetConfigDisabled persists disabled flag 的 single MCP 服务端 在
// given 配置 scope 和 applies change 到 running 客户端:
// disabling tears 连接 down, enabling starts it even 如果 it was
// disabled before. 一个 repository-scoped override takes precedence, 所以 当
// localOverride 是 set 只 配置 是 written.
func SetConfigDisabled(ctx context.Context, cfg *config.ConfigStore, scope config.Scope, name string, disabled, localOverride bool) error {
	if err := cfg.SetMCPServerDisabledConfig(scope, name, disabled); err != nil {
		return err
	}
	if localOverride {
		return nil
	}
	return SetLocalDisabled(ctx, cfg, name, disabled)
}

// SetLocalDisabled applies repository-scoped toggle 到 running
// 客户端: disabling tears 连接 down, enabling starts it even 如果
// 它的 配置 条目 是 disabled. Persisting override 是 caller's job.
func SetLocalDisabled(ctx context.Context, cfg *config.ConfigStore, name string, disabled bool) error {
	if disabled {
		return DisableSingle(cfg, name)
	}
	return InitializeSingleForced(ctx, name, cfg)
}

// DisableSingle disables 和 closes single MCP 客户端 通过 name.
func DisableSingle(cfg *config.ConfigStore, name string) error {
	// teardown bumps generation, invalidating any 在-flight connect, 和
	// StateDisabled transition clears recorded 配置 所以 later
	// re-enable (even 用 unchanged 配置) 是 seen 作为 new 和 restarts.
	teardown(name)
	updateState(name, StateDisabled, nil, nil, Counts{})
	slog.Info("Disabled MCP client", "name", name)
	return nil
}

// goInitClient launches initClient 在 goroutine 用 panic recovery.
// Shared 通过 Initialize 和 Reinitialize 所以 panic-到-state policy
// lives 在 one place. wg, 如果 non-nil, 是 Done 当 attempt finishes
// (success 或 failure); Initialize uses it 到 await startup. goroutine
// captures 服务端's generation 在 launch 所以 concurrent teardown
// invalidates 它的 结果 rather than letting it register stale 会话.
func goInitClient(ctx context.Context, cfg *config.ConfigStore, name string, m config.MCPConfig, wg *sync.WaitGroup) {
	gen := currentGen(name)
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		defer func() {
			if r := recover(); r != nil {
				var err error
				switch v := r.(type) {
				case error:
					err = v
				case string:
					err = fmt.Errorf("panic: %s", v)
				default:
					err = fmt.Errorf("panic: %v", v)
				}
				updateState(name, StateError, err, nil, Counts{})
				slog.Error("Panic in MCP client initialization", "error", err, "name", name)
			}
		}()
		start := time.Now()
		err := initClient(ctx, cfg, name, m, gen, cfg.Resolver())
		slog.Debug(
			"MCP client initialization finished",
			"name", name,
			"duration", time.Since(start).Truncate(time.Millisecond).String(),
			"error", err,
		)
	}()
}

// currentGen returns 服务端's current generation without bumping it.
func currentGen(name string) uint64 {
	g, _ := gens.Get(name)
	return g
}

// teardown closes 服务端's 会话 和 clears 它的 tools, prompts,
// resources, 和 auth state, then bumps 服务端's generation 所以 any
// 在-flight initialization 为 it 是 discarded 在 commit. 它 leaves
// states 条目 intact; callers decide whether 到 删除 或 更新 it.
// Shared 通过 DisableSingle, removeServer, 和 restart 路径 在
// Reinitialize.
func teardown(name string) {
	g, _ := gens.Get(name)
	gens.Set(name, g+1)
	if session, ok := sessions.Take(name); ok {
		closeSession(name, session)
	}
	clearMCPData(name)
}

func getOrRenewClient(ctx context.Context, cfg *config.ConfigStore, name string) (*ClientSession, error) {
	m := cfg.Config().MCP[name]
	timeout := mcpTimeout(m)

	// Fast 路径: reuse healthy 会话 without taking renewal 锁.
	if sess, ok := sessions.Get(name); ok {
		if err := pingSession(ctx, sess, timeout); err == nil {
			return sess, nil
		}
	}

	// Serialize renewals per 服务端. Two concurrent 工具 calls can both
	// observe dead 会话 和 race 到 rebuild it: one may close
	// 会话 other just registered, 或 overwrite 和 leak live
	// replacement. Under 这 锁 只 first arrival rebuilds; later
	// arrivals re-check 和 reuse healthy 结果.
	mu := renewLock(name)
	mu.Lock()
	defer mu.Unlock()

	// Under 锁 map 是 stable: any 在-flight renewal has finished 和
	// either re-registered 它的 会话 或 failed 和 left none. 一个 renewal
	// removes 会话 transiently (StateError takes it before rebuilding),
	// 所以 这 check must happen here rather than before 锁 — otherwise
	// caller arriving mid-renewal sees no 会话 和 wrongly reports
	// 服务端 unavailable.
	sess, ok := sessions.Get(name)
	if !ok {
		return nil, fmt.Errorf("mcp '%s' not available", name)
	}

	// 一个 concurrent goroutine may have already renewed 会话 while we
	// waited 为 锁. Reuse it 如果 it 是 now healthy.
	pingErr := pingSession(ctx, sess, timeout)
	if pingErr == nil {
		return sess, nil
	}

	state, _ := states.Get(name)
	// StateError closes dead 会话 和 clears 它的 tools, prompts, 和
	// resources 从 registry. 报告 failure against exact
	// 会话 那 failed ping 所以 只 那 会话 是 torn down.
	updateState(name, StateError, maybeTimeoutErr(pingErr, timeout), sess, state.Counts)

	// Capture generation 所以 reconcile teardown 那 lands mid-renewal
	// invalidates 这 rebuild instead 的 letting it clobber newer one.
	gen := currentGen(name)
	newSess, err := newSession(ctx, cfg, name, m, cfg.Resolver(), ChannelOptIn(m, cfg.Overrides().EnabledChannels, name))
	if err != nil {
		clearMCPData(name)
		// If OAuth MCP fails 到 reconnect because token 是 no
		// longer valid, clear stale token 和 提示词 用户 到
		// re-authenticate instead 的 leaving it 在 错误 state.
		if m.OAuth && m.Type == config.MCPHttp {
			if m.OAuthToken != nil && isOAuthInitErr(err) {
				clearOAuthToken(cfg, name)
			}
			updateState(name, StateNeedsAuth, nil, nil, Counts{})
			slog.Info("MCP OAuth session expired, re-authentication required", "name", name, "error", err)
		}
		return nil, err
	}

	// 一个 reconcile teardown ran while we were rebuilding: newer attempt owns
	// 这 服务端 now. Bail before writing 到 any shared registry 所以 we don't
	// clobber newer attempt's registrations; just drop our own 会话.
	if currentGen(name) != gen {
		closeSession(name, newSess)
		return nil, context.Canceled
	}

	// StateError cleared 这 服务端's tools, prompts, 和 resources 从
	// registry. Re-list 和 re-register them all 在 fresh 会话 和
	// recompute counts 从 what actually registered; otherwise agent
	// reconnects 但 registries stay empty ( next 工具调用 fails 用
	// "工具 不 found") while reported counts still advertise capabilities
	// 那 是 no longer there.
	var counts Counts
	counts.Tools, err = registerSessionTools(ctx, cfg, name, newSess)
	if err != nil {
		updateState(name, StateError, err, nil, Counts{})
		closeSession(name, newSess)
		return nil, err
	}

	prompts, err := getPrompts(ctx, newSess)
	if err != nil {
		updateState(name, StateError, err, nil, Counts{})
		closeSession(name, newSess)
		return nil, err
	}
	updatePrompts(name, prompts)
	counts.Prompts = len(prompts)

	resources, err := getResources(ctx, newSess)
	if err != nil {
		updateState(name, StateError, err, nil, Counts{})
		closeSession(name, newSess)
		return nil, err
	}
	counts.Resources = updateResources(name, resources)

	// Re-check before publishing: 如果 teardown landed during registration
	// newer attempt owns registries now, 所以 leave them 和 our 会话
	// alone rather than overwriting 它的 state.
	if currentGen(name) != gen {
		closeSession(name, newSess)
		return nil, context.Canceled
	}

	sessions.Set(name, newSess)
	updateState(name, StateConnected, nil, newSess, counts, withConfig(m))
	return newSess, nil
}

// pingSession pings 会话 用 服务端's configured 超时.
func pingSession(ctx context.Context, s *ClientSession, timeout time.Duration) error {
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := s.Ping(pingCtx, nil)
	// MethodNotFound means 服务端 answered; it just doesn't implement ping.
	var wireErr *jsonrpc.Error
	if errors.As(err, &wireErr) && wireErr.Code == jsonrpc.CodeMethodNotFound {
		return nil
	}
	return err
}

// closeSession closes MCP 会话, logging 只 unexpected errors. EOF,
// 上下文 cancellation, 和 killed child 是 ordinary 结果 的 tearing
// 会话 down 和 是 不 worth surfacing.
func closeSession(name string, s *ClientSession) {
	if err := s.Close(); err != nil &&
		!errors.Is(err, io.EOF) &&
		!errors.Is(err, context.Canceled) &&
		err.Error() != "signal: killed" {
		slog.Warn("Error closing MCP session", "name", name, "error", err)
	}
}

// stateOpt mutates ClientInfo transition 是 about 到 发布. 配置
// recording 是 opt-在: 只 sites 那 actually own 配置 ( connect
// 和 starting paths) pass one, 所以 many 错误 和 count-refresh 调用 sites
// can't accidentally clobber recorded 配置 通过 passing zero 值.
type stateOpt func(*ClientInfo)

// withConfig records 配置 now 在 effect. Used 在 StateConnected.
func withConfig(m config.MCPConfig) stateOpt {
	return func(i *ClientInfo) {
		i.Config = m
		i.PendingConfig = nil
	}
}

// withPending records 配置 在-flight attempt 是 connecting 用.
// Used 在 StateStarting.
func withPending(m config.MCPConfig) stateOpt {
	return func(i *ClientInfo) {
		mc := m
		i.PendingConfig = &mc
	}
}

// withChannelOptIn records whether 服务端 是 opted 在 作为 渠道.
// Applied 在 every connect attempt 所以 it tracks current 配置 和
// --channels overrides.
func withChannelOptIn(cfg *config.ConfigStore, name string, m config.MCPConfig) stateOpt {
	optIn := ChannelOptIn(m, cfg.Overrides().EnabledChannels, name)
	return func(i *ClientInfo) { i.ChannelOptIn = optIn }
}

// updateState updates state 的 MCP 客户端 和 publishes 事件.
//
// 配置 bookkeeping 是 split between caller 和 state machine:
// - Callers 那 own 配置 opt 在 via withConfig (StateConnected) 或
// withPending (StateStarting). Everyone else leaves recorded 配置
// untouched, 所以 错误 或 count refresh can't wipe it.
// - state machine owns transitions 用 fixed semantics:
// StateDisabled clears both 所以 later re-enable 用 unchanged 配置
// 是 seen 作为 new rather than skipped 作为 already initialized.
func updateState(name string, state State, err error, client *ClientSession, counts Counts, opts ...stateOpt) {
	prev, _ := states.Get(name)
	info := prev
	info.Name = name
	info.State = state
	info.Error = err
	info.Client = client
	info.Counts = counts
	info.Channel = client != nil && client.channel
	if client != nil {
		info.ChannelCapable = client.channelCapable
	}
	for _, opt := range opts {
		opt(&info)
	}
	switch state {
	case StateConnected:
		info.ConnectedAt = time.Now()
	case StateDisabled:
		info.Config = config.MCPConfig{}
		info.PendingConfig = nil
		// 一个 disabled 服务端 是 不 渠道, whatever 它的 配置 says.
		info.ChannelOptIn = false
		info.ChannelCapable = false
	case StateError:
		// 一个 会话 那 has errored 是 dead 到 us: close it 所以 child
		// process 和 它的 stdio pipes 是 released, 和 clear 它的 registry
		// entries 所以 agent stops advertising capabilities it can no
		// longer 调用 (without 那, crush_info / `/mcp` menu 和
		// 工具 list handed 到 LLM diverge). Crucially, close exactly
		// 会话 那 errored ( 客户端 argument): 如果 registry
		// already holds DIFFERENT 会话 — newer, healthy one another
		// 路径 installed — leave it 和 它的 registrations alone. Closing
		// "whatever 是 在 map" here let stale 错误 transition (e.g.
		// refresh 那 raced renewal) tear down healthy replacement.
		switch {
		case client != nil:
			if cur, ok := sessions.Get(name); ok && cur == client {
				sessions.Del(name)
				allTools.Del(name)
				allPrompts.Del(name)
				allResources.Del(name)
			}
			closeSession(name, client)
		default:
			// No specific 会话 errored (e.g. connect itself failed);
			// anything still registered under 这 name 是 unusable.
			if old, ok := sessions.Take(name); ok {
				closeSession(name, old)
			}
			allTools.Del(name)
			allPrompts.Del(name)
			allResources.Del(name)
		}
		// Never 发布 dead 会话 在 state.
		info.Client = nil
	}
	states.Set(name, info)

	// 发布 state change 事件
	broker.Publish(pubsub.UpdatedEvent, Event{
		Type:   EventStateChanged,
		Name:   name,
		State:  state,
		Error:  err,
		Counts: counts,
	})
}

func createSession(ctx context.Context, cfg *config.ConfigStore, name string, m config.MCPConfig, resolver config.VariableResolver, channelOptIn bool) (*ClientSession, error) {
	timeout := mcpTimeout(m)
	// 会话 lives until Close, 不 until ctx ends: callers pass
	// short-lived contexts ( HTTP 请求 在 客户端/服务端 mode, 工具
	// 调用 那 renews dead 会话), 和 stdio 服务端 是 killed 当
	// mcpCtx ends. ctx can still abort 会话 while it 是 being set up.
	mcpCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	cancelTimer := time.AfterFunc(timeout, cancel)

	transport, oauthHandler, err := createTransport(mcpCtx, cfg, name, m, resolver)
	if err != nil {
		updateState(name, StateError, err, nil, Counts{})
		slog.Error("Error creating MCP client", "error", err, "name", name)
		cancel()
		cancelTimer.Stop()
		return nil, err
	}

	// If caller requested browser-suppressed flow (服务端-driven
	// remote auth), suppress 处理器's local browser open; caller
	// surfaces MCPAuthURL(name) 到 用户 在 their own machine.
	if oauthHandler != nil {
		if suppress, _ := ctx.Value(suppressBrowserKey{}).(bool); suppress {
			oauthHandler.SetBrowserSuppress(true)
		}
	}

	// Wrap transport 所以 渠道 notifications can 是 intercepted.
	// gate starts undecided: notifications 那 arrive during 能力
	// negotiation 是 buffered. After Connect resolves, gate 是 opened
	// (和 缓冲 drained) 只 当 服务端 declares 渠道
	// 能力 AND was opted 在 (--channels 或 channel_enabled);
	// otherwise it 是 closed (缓冲 discarded). 这 prevents early
	// notifications 从 being lost.
	channelGate := newChannelGate()
	transport = &channelTransport{inner: transport, name: name, gate: channelGate}

	// When 服务端 是 marked Sessionless, tools/prompts/resources
	// list-changed handlers 是 omitted. go-sdk opens SEP-2575
	// "subscriptions/listen" 流 whenever any 的 those handlers 是 set 和
	// 服务端 advertises listChanged (客户端.go), 和 sessionless
	// streamable-HTTP servers such 作为 GitHub MCP answer 那 POST 用 404.
	// SDK tolerates 那 rejection, 所以 flag 只 skips doomed
	// listen 请求.
	opts := &mcp.ClientOptions{
		LoggingMessageHandler: func(ctx context.Context, req *mcp.LoggingMessageRequest) {
			level := parseLevel(string(req.Params.Level))
			slog.Log(ctx, level, "MCP log", "name", name, "logger", req.Params.Logger, "data", req.Params.Data)
		},
	}
	if !m.IsSessionless(resolver) {
		opts.ToolListChangedHandler = func(context.Context, *mcp.ToolListChangedRequest) {
			broker.Publish(pubsub.UpdatedEvent, Event{
				Type: EventToolsListChanged,
				Name: name,
			})
		}
		opts.PromptListChangedHandler = func(context.Context, *mcp.PromptListChangedRequest) {
			broker.Publish(pubsub.UpdatedEvent, Event{
				Type: EventPromptsListChanged,
				Name: name,
			})
		}
		opts.ResourceListChangedHandler = func(context.Context, *mcp.ResourceListChangedRequest) {
			broker.Publish(pubsub.UpdatedEvent, Event{
				Type: EventResourcesListChanged,
				Name: name,
			})
		}
	}
	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "crush",
			Version: version.Version,
			Title:   "Crush",
		},
		opts,
	)

	session, err := client.Connect(mcpCtx, transport, nil)
	if err != nil {
		err = maybeStdioErr(err, transport)
		updateState(name, StateError, maybeTimeoutErr(err, timeout), nil, Counts{})
		slog.Error("MCP client failed to initialize", "error", err, "name", name)
		cancel()
		cancelTimer.Stop()
		return nil, err
	}

	cancelTimer.Stop()
	slog.Debug("MCP client initialized", "name", name)

	// Resolve 渠道 gate: open 只 为 服务端 那 both declares
	// claude/渠道 能力 和 was opted 在 — via --channels 或
	// channel_enabled 在 配置. Merely listing 服务端 under mcp 是 不
	// enough. Otherwise close the gate (fail closed). Resolving drains buffered messages
	// 那 arrived during negotiation 所以 fast 服务端 does 不 lose early
	// events.
	channelCapable := hasChannelCapability(session.InitializeResult())
	isChannel := channelOptIn && channelCapable
	if isChannel {
		buffered := channelGate.resolve(true)
		for _, raw := range buffered {
			publishChannelMessage(mcpCtx, name, raw)
		}
		slog.Info("MCP channel enabled", "name", name, "buffered", len(buffered))
	} else {
		channelGate.resolve(false)
	}

	return &ClientSession{
		ClientSession:  session,
		cancel:         cancel,
		oauthHandler:   oauthHandler,
		channel:        isChannel,
		channelCapable: channelCapable,
	}, nil
}

// transportWrapper 是 implemented 通过 every transport decorator crush layers
// around base transport, 所以 diagnostics 那 need innermost transport
// can reach it without knowing which decorators 是 在 play.
type transportWrapper interface {
	unwrapTransport() mcp.Transport
}

// unwrapTransport peels every decorator off transport 和 returns
// innermost one.
func unwrapTransport(transport mcp.Transport) mcp.Transport {
	for {
		w, ok := transport.(transportWrapper)
		if !ok {
			return transport
		}
		transport = w.unwrapTransport()
	}
}

// maybeStdioErr 如果 stdio mcp prints 错误 在 non-json format, it'll fail
// 到 parse, 和 cli will then close it, causing EOF 错误.
// 所以, 如果 we got EOF err, 和 transport 是 STDIO, we try 到 exec it
// again 用 超时 和 collect output 所以 we can add details 到
// 错误.
// 这 happens particularly 当 starting things 用 npx, e.g. 如果 node can't
// 是 found 或 some other 错误 like 那.
func maybeStdioErr(err error, transport mcp.Transport) error {
	if !errors.Is(err, io.EOF) {
		return err
	}
	// transport 是 wrapped 在 one 或 more decorators before Connect (
	// 渠道 gate today); stdio transport we're probing 为 是
	// innermost one. Unwrap all 的 them — without 这 assertion below
	// never matches 和 stdio startup failures 报告 bare EOF instead 的
	// child's actual output. Every wrapper 必须 implement
	// unwrapTransport 或 it will hide 这 diagnostic again.
	transport = unwrapTransport(transport)
	ct, ok := transport.(*mcp.CommandTransport)
	if !ok {
		return err
	}
	if err2 := stdioCheck(ct.Command); err2 != nil {
		err = errors.Join(err, err2)
	}
	return err
}

func maybeTimeoutErr(err error, timeout time.Duration) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("timed out after %s", timeout)
	}
	return err
}

func createTransport(ctx context.Context, cfg *config.ConfigStore, name string, m config.MCPConfig, resolver config.VariableResolver) (mcp.Transport, *mcpoauth.Handler, error) {
	switch m.Type {
	case config.MCPStdio:
		command, err := resolver.ResolveValue(m.Command)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid mcp command: %w", err)
		}
		if strings.TrimSpace(command) == "" {
			return nil, nil, fmt.Errorf("mcp stdio config requires a non-empty 'command' field")
		}
		args, err := m.ResolvedArgs(resolver)
		if err != nil {
			return nil, nil, err
		}
		envs, err := m.ResolvedEnv(resolver)
		if err != nil {
			return nil, nil, err
		}
		cmd := exec.CommandContext(ctx, home.Long(command), args...)
		// Start 服务端 在 工作区 目录, 不 process cwd.
		// In 客户端/服务端 mode one process hosts every 工作区 和 keeps
		// cwd 的 whichever 客户端 spawned it, 所以 relative command 或
		// argument would otherwise resolve against another project.
		if cfg != nil {
			cmd.Dir = cfg.WorkingDir()
		}
		cmd.Env = append(os.Environ(), envs...)
		// 运行 child 在 它的 own process group 和 kill whole group 当
		// 会话 上下文 是 cancelled. 一个 stdio 服务端 often spawns 它的 own
		// children (signal-mcp launches signal-cli); os/exec's 默认
		// cancellation kills 只 direct child, orphaning rest 用
		// PPID 1 — production accumulated 15+ such zombies over two days.
		configureStdioProcess(cmd)
		return &mcp.CommandTransport{
			Command: cmd,
		}, nil, nil
	case config.MCPHttp:
		url, err := m.ResolvedURL(resolver)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(url) == "" {
			return nil, nil, fmt.Errorf("mcp http config requires a non-empty 'url' field")
		}

		// OAuth-enabled HTTP transport. 处理器 persists token
		// (和 客户端 registration/endpoints needed 到 refresh it)
		// 在 every exchange 和 refresh via 这 saver.
		if m.OAuth {
			tokenSaver := func(tok *oauth.Token) {
				if err := cfg.SetConfigField(config.ScopeGlobal, fmt.Sprintf("mcp.%s.oauth_token", name), tok); err != nil {
					slog.Warn("Failed to persist MCP OAuth token", "name", name, "error", err)
				} else {
					slog.Info("Persisted MCP OAuth token", "name", name)
				}
			}

			// 一个 pre-registered 客户端 是 required 为 servers 那 do 不
			// support dynamic 客户端 registration (e.g. GitHub, Slack).
			// Resolve credentials through shell like other 配置
			// values 所以 $VAR 和 $(cmd) work.
			var preregistered *oauth.OAuthClient
			if strings.TrimSpace(m.OAuthClientID) != "" {
				clientID, err := resolver.ResolveValue(m.OAuthClientID)
				if err != nil {
					return nil, nil, fmt.Errorf("oauth_client_id: %w", err)
				}
				clientSecret, err := resolver.ResolveValue(m.OAuthClientSecret)
				if err != nil {
					return nil, nil, fmt.Errorf("oauth_client_secret: %w", err)
				}
				preregistered = &oauth.OAuthClient{
					ClientID:     strings.TrimSpace(clientID),
					ClientSecret: strings.TrimSpace(clientSecret),
				}
			}

			// Normalize trailing slash 为 PRM discovery compatibility.
			normalizedURL := strings.TrimSuffix(url, "/")
			oauthHandler, oauthErr := mcpoauth.NewHandler(name, normalizedURL, m.OAuthToken, preregistered, tokenSaver, mcpoauth.IsInteractive(ctx), m.OAuthCallbackPort)
			if oauthErr != nil {
				return nil, nil, fmt.Errorf("failed to create OAuth handler for mcp %q: %w", name, oauthErr)
			}
			authURLs.Set(name, oauthHandler)
			return &mcp.StreamableClientTransport{
				Endpoint:     url,
				OAuthHandler: oauthHandler,
			}, oauthHandler, nil
		}

		headers, err := m.ResolvedHeaders(resolver)
		if err != nil {
			return nil, nil, err
		}
		client := &http.Client{
			Transport: &headerRoundTripper{
				headers: headers,
			},
		}
		return &mcp.StreamableClientTransport{
			Endpoint:   url,
			HTTPClient: client,
		}, nil, nil
	case config.MCPSSE:
		url, err := m.ResolvedURL(resolver)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(url) == "" {
			return nil, nil, fmt.Errorf("mcp sse config requires a non-empty 'url' field")
		}
		headers, err := m.ResolvedHeaders(resolver)
		if err != nil {
			return nil, nil, err
		}

		var transport http.RoundTripper = &headerRoundTripper{headers: headers}
		var oauthHandler *mcpoauth.Handler

		// SSE transports don't support the SDK's OAuthHandler natively,
		// 所以 we wrap HTTP transport 用 our own round-tripper 那
		// injects bearer tokens 和 handles 401-triggered authorization.
		// Based 在 Bruno Krugel's oauthRoundTripper 从 PR #3396.
		if m.OAuth {
			tokenSaver := func(tok *oauth.Token) {
				if err := cfg.SetConfigField(config.ScopeGlobal, fmt.Sprintf("mcp.%s.oauth_token", name), tok); err != nil {
					slog.Warn("Failed to persist MCP OAuth token", "name", name, "error", err)
				} else {
					slog.Info("Persisted MCP OAuth token", "name", name)
				}
			}

			var preregistered *oauth.OAuthClient
			if strings.TrimSpace(m.OAuthClientID) != "" {
				clientID, err := resolver.ResolveValue(m.OAuthClientID)
				if err != nil {
					return nil, nil, fmt.Errorf("oauth_client_id: %w", err)
				}
				clientSecret, err := resolver.ResolveValue(m.OAuthClientSecret)
				if err != nil {
					return nil, nil, fmt.Errorf("oauth_client_secret: %w", err)
				}
				preregistered = &oauth.OAuthClient{
					ClientID:     strings.TrimSpace(clientID),
					ClientSecret: strings.TrimSpace(clientSecret),
				}
			}

			// Normalize trailing slash 为 PRM discovery compatibility.
			normalizedURL := strings.TrimSuffix(url, "/")
			handler, oauthErr := mcpoauth.NewHandler(name, normalizedURL, m.OAuthToken, preregistered, tokenSaver, mcpoauth.IsInteractive(ctx), m.OAuthCallbackPort)
			if oauthErr != nil {
				return nil, nil, fmt.Errorf("failed to create OAuth handler for mcp %q: %w", name, oauthErr)
			}
			oauthHandler = handler
			authURLs.Set(name, handler)
			transport = newOAuthRoundTripper(handler, transport)
		}

		client := &http.Client{Transport: transport}
		return &mcp.SSEClientTransport{
			Endpoint:   url,
			HTTPClient: client,
		}, oauthHandler, nil
	default:
		return nil, nil, fmt.Errorf("unsupported mcp type: %s", m.Type)
	}
}

type headerRoundTripper struct {
	headers map[string]string
}

func (rt headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range rt.headers {
		req.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// oauthRoundTripper wraps HTTP transport 用 OAuth bearer token
// injection 和 401-triggered authorization. Used 为 SSE transports
// 那 don't support SDK's OAuthHandler natively. Based 在 Bruno
// Krugel's implementation 从 PR #3396.
type oauthRoundTripper struct {
	base    http.RoundTripper
	handler auth.OAuthHandler
}

func newOAuthRoundTripper(handler auth.OAuthHandler, base http.RoundTripper) *oauthRoundTripper {
	return &oauthRoundTripper{base: base, handler: handler}
}

func (rt *oauthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.doRequestWithToken(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if authErr := rt.handler.Authorize(req.Context(), req, resp); authErr != nil {
			return resp, nil
		}
		resp.Body.Close()
		return rt.doRequestWithToken(req.Clone(req.Context()))
	}

	return resp, nil
}

func (rt *oauthRoundTripper) doRequestWithToken(req *http.Request) (*http.Response, error) {
	ts, err := rt.handler.TokenSource(req.Context())
	if err != nil {
		return nil, fmt.Errorf("oauth token source: %w", err)
	}
	if ts != nil {
		token, err := ts.Token()
		if err == nil && token != nil {
			req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		}
	}
	return rt.base.RoundTrip(req)
}

func mcpTimeout(m config.MCPConfig) time.Duration {
	if m.Timeout > 0 {
		return time.Duration(m.Timeout) * time.Second
	}
	// OAuth flows require 用户 interaction 在 browser, 所以 use
	// generous 默认 到 avoid timing out mid-auth.
	if m.OAuth {
		return 30 * time.Second
	}
	return 10 * time.Second
}

// hasUsableToken returns true 如果 saved OAuth token has access
// token 那 can 是 used 或 refreshed. 一个 token 用 empty access
// token 是 structurally invalid 和 should 是 treated 作为 missing.
func hasUsableToken(tok *oauth.Token) bool {
	return tok != nil && tok.AccessToken != ""
}

// isOAuthInitErr returns true 如果 错误 indicates OAuth token
// 是 missing, no longer valid, 或 cannot 是 refreshed. 这 covers:
// - invalid_grant: expired 或 revoked refresh tokens
// - invalid_client: deleted 或 deactivated 客户端 registrations
// - "no token available": 处理器 had no cached token 到 use
// - interactive authorization was required 但 withheld during startup
func isOAuthInitErr(err error) bool {
	if errors.Is(err, mcpoauth.ErrInteractiveAuthRequired) {
		return true
	}
	var rErr *oauth2.RetrieveError
	if errors.As(err, &rErr) {
		return rErr.ErrorCode == "invalid_grant" || rErr.ErrorCode == "invalid_client"
	}
	msg := err.Error()
	return strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "invalid_client") ||
		strings.Contains(msg, "no token available")
}

// clearOAuthToken removes persisted OAuth token 为 named MCP
// 服务端 从 global 配置 所以 subsequent startups don't 重试
// 用 known-bad refresh token.
func clearOAuthToken(cfg *config.ConfigStore, name string) {
	key := fmt.Sprintf("mcp.%s.oauth_token", name)
	if err := cfg.RemoveConfigField(config.ScopeGlobal, key); err != nil {
		slog.Warn("Failed to clear stale MCP OAuth token", "name", name, "error", err)
	}
}

// clearMCPData removes stale MCP 服务端's tools, prompts,
// resources, 和 auth handlers 从 global state 所以 they 是 不
// served 到 agent.
func clearMCPData(name string) {
	allTools.Del(name)
	allPrompts.Del(name)
	allResources.Del(name)
	if h, ok := authURLs.Get(name); ok {
		h.Close()
		authURLs.Del(name)
	}
}

var stdioCheckTimeout = 5 * time.Second

func stdioCheck(old *exec.Cmd) error {
	ctx, cancel := context.WithTimeout(context.Background(), stdioCheckTimeout)
	defer cancel()
	// old.Args includes argv0 作为 first element; exec.CommandContext
	// prepends old.路径 作为 argv0, 所以 we must skip it 到 avoid duplication
	// (e.g. "npx npx -y pkg" instead 的 "npx -y pkg").
	args := old.Args
	if len(args) > 0 {
		args = args[1:]
	}
	cmd := exec.CommandContext(ctx, old.Path, args...)
	cmd.Dir = old.Dir
	cmd.Env = old.Env
	cmd.WaitDelay = stdioCheckTimeout
	out, err := cmd.CombinedOutput()
	if err == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return fmt.Errorf("%w: %s", err, string(out))
}
