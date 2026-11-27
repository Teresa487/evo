package permission

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

// hookApprovalKey 是 unexported 上下文 key used 到 标记 工具调用 作为
// pre-approved 通过 PreToolUse 钩子. 值 是 工具调用 ID 所以
// approval can't 是 reused across calls 那 happen 到 share 上下文.
type hookApprovalKey struct{}

// WithHookApproval returns 上下文 那 marks given 工具调用 ID 作为
// pre-approved 通过 钩子. When 权限 service sees matching
// 请求 it short-circuits normal 提示词 和 grants immediately.
func WithHookApproval(ctx context.Context, toolCallID string) context.Context {
	return context.WithValue(ctx, hookApprovalKey{}, toolCallID)
}

// hookApproved reports whether 上下文 carries 钩子 approval 为
// given 工具调用 ID.
func hookApproved(ctx context.Context, toolCallID string) bool {
	if toolCallID == "" {
		return false
	}
	v, _ := ctx.Value(hookApprovalKey{}).(string)
	return v == toolCallID
}

// PermissionMode represents current 权限 mode.
type PermissionMode int

const (
	// PermissionModeNormal prompts 为 all non-safe commands.
	PermissionModeNormal PermissionMode = iota
	// PermissionModeYolo never prompts 和 refuses dangerous commands.
	PermissionModeYolo
	// PermissionModeSysadmin never prompts 和 refuses nothing.
	PermissionModeSysadmin
)

// SkipsPrompts reports whether the mode answers requests without asking.
func (m PermissionMode) SkipsPrompts() bool {
	return m == PermissionModeYolo || m == PermissionModeSysadmin
}

// RefusesDangerous reports whether dangerous command 是 refused instead 的 asked about.
func (m PermissionMode) RefusesDangerous() bool {
	return m == PermissionModeYolo
}

type CreatePermissionRequest struct {
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
	Danger      string `json:"danger,omitempty"`
	// GrantKey narrows what "allow 为 这 会话" covers. Leave it empty
	// 当 路径 already says what 是 being acted 在; set it 当 it does
	// 不, 作为 为 shell command, where 路径 是 只 目录.
	GrantKey string `json:"grant_key,omitempty"`
}

type PermissionNotification struct {
	SessionID  string `json:"session_id"`
	ToolCallID string `json:"tool_call_id"`
	Granted    bool   `json:"granted"`
	Denied     bool   `json:"denied"`
	// Cancelled 是 set 当 请求 was abandoned before decision
	// ( turn was cancelled while 提示词 was open).
	Cancelled bool `json:"cancelled"`
}

// Outcome 是 how 权限 提示词 用户 saw was resolved.
type Outcome string

const (
	OutcomeGranted   Outcome = "granted"
	OutcomeDenied    Outcome = "denied"
	OutcomeCancelled Outcome = "cancelled"
)

// PromptObserver hears about every 提示词 用户 是 shown 和 how it
// was resolved, 在 那 order. Auto-approved calls show no 提示词 和
// 是 不 reported. Calls 运行 off requesting goroutine, 所以 slow
// observer never delays 提示词 或 工具.
type PromptObserver interface {
	PromptShown(ctx context.Context, req PermissionRequest)
	PromptResolved(ctx context.Context, req PermissionRequest, outcome Outcome)
}

// ModeChangedEvent 是 published whenever 权限 mode changes.
type ModeChangedEvent struct {
	Mode PermissionMode `json:"mode"`
}

type PermissionRequest struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
	Danger      string `json:"danger,omitempty"`
	// GrantKey narrows what 会话 grant covers. See
	// [CreatePermissionRequest.GrantKey].
	GrantKey string `json:"grant_key,omitempty"`
}

type Service interface {
	pubsub.Subscriber[PermissionRequest]
	// GrantPersistent grants 权限 请求 和 remembers grant
	// 为 会话. 它 returns true 如果 这 调用 actually resolved
	// pending 请求; false 如果 请求 had already been resolved
	// (e.g., 通过 another concurrent caller) 或 是 unknown.
	GrantPersistent(permission PermissionRequest) bool
	// Grant grants 权限 请求. 它 returns true 如果 这 调用
	// actually resolved pending 请求; false 如果 请求 had
	// already been resolved 或 是 unknown.
	Grant(permission PermissionRequest) bool
	// Deny denies 权限 请求. 它 returns true 如果 这 调用
	// actually resolved pending 请求; false 如果 请求 had
	// already been resolved 或 是 unknown.
	Deny(permission PermissionRequest) bool
	Request(ctx context.Context, opts CreatePermissionRequest) (bool, error)
	// ActiveRequests returns requests 那 是 currently awaiting
	// decision, 在 most one per scope (see SessionScope). 一个 请求 是
	// 只 reported while it can still 是 resolved; 请求 whose
	// waiter was cancelled 是 不 returned.
	ActiveRequests() []PermissionRequest
	// SetSessionScope makes requests wait 只 为 earlier requests 在
	// same scope instead 的 every earlier 请求. 一个 nil scope
	// restores 默认 的 one scope 为 whole service.
	SetSessionScope(scope SessionScope)
	AutoApproveSession(sessionID string)
	// InheritAutoApproval approves child 会话 automatically 当 它的
	// parent 会话 already 是. 一个 child 的 any other 会话 keeps
	// prompting.
	InheritAutoApproval(parentSessionID, childSessionID string)
	SetPermissionMode(mode PermissionMode)
	PermissionMode() PermissionMode
	SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification]
	SubscribeModeChanges(ctx context.Context) <-chan pubsub.Event[ModeChangedEvent]
}

// SessionScope maps 会话 到 scope 它的 权限 requests 是
// processed 在. 一个 请求 是 只 shown once earlier requests 在 它的
// scope 是 resolved, 所以 each scope shows one 提示词 在 time, while
// requests 在 different scopes never wait 为 each other.
type SessionScope func(ctx context.Context, sessionID string) string

// PermissionKey 是 composite key 为 会话 权限 lookups.
type PermissionKey struct {
	SessionID string
	ToolName  string
	Action    string
	Path      string
	// Grant narrows 会话 grant 到 one specific operation. For tools
	// whose 路径 already identifies what 是 being touched, such 作为 reading
	// 或 editing 文件, it 是 empty 和 路径 carries meaning. For
	// bash it holds command, because there 路径 是 只 working
	// 目录: without 这, approving `ls` 为 会话 would approve
	// every later command 运行 在 same 目录, `sudo` included.
	Grant string
}

// pendingRequest couples 请求 用 渠道 它的 caller waits 在.
//
// Keeping 请求 rather than 只 渠道 means resolution 是
// recorded against what 服务端 actually asked, 不 against whatever
// answering 客户端 says it was answering. Those 是 same thing 当
// 客户端 是 honest 和 difference matters 当 it 是 不.
type pendingRequest struct {
	req    PermissionRequest
	respCh chan bool
}

type permissionService struct {
	*pubsub.Broker[PermissionRequest]

	notificationBroker    *pubsub.Broker[PermissionNotification]
	modeBroker            *pubsub.Broker[ModeChangedEvent]
	workingDir            string
	sessionPermissions    *csync.Map[PermissionKey, bool]
	pendingRequests       *csync.Map[string, pendingRequest]
	autoApproveSessions   map[string]bool
	autoApproveSessionsMu sync.RWMutex
	allowedTools          []string
	observer              PromptObserver
	mode                  PermissionMode
	modeMu                sync.RWMutex

	// Requests 是 processed one 在 time per scope (see SessionScope).
	// activeRequests holds 请求 each scope 是 showing, 通过 ID.
	scope          atomic.Pointer[SessionScope]
	requestLocks   map[string]*requestLock
	requestLocksMu sync.Mutex
	activeRequests *csync.Map[string, PermissionRequest]
}

// requestLock serializes requests 的 one scope. refs counts
// requests holding 或 waiting 为 it, 所以 it 是 dropped once 它的 scope
// has no requests left.
type requestLock struct {
	sync.Mutex
	refs int
}

// resolve atomically removes pending 请求 条目 为 given
// 权限 和, 如果 it was still pending, publishes exactly one
// PermissionNotification 和 forwards outcome 到 waiter 在
// respCh. 它 returns true 如果 这 调用 resolved 请求, false 如果
// it had already been resolved (e.g., 通过 another concurrent caller) 或
// 请求 ID 是 unknown.
//
// If onResolve 是 non-nil it runs after pending 条目 has been
// taken 但 before 通知 是 published 或 waiter 是
// unblocked. 这 lets GrantPersistent record 会话 权限
// 只 当 it actually wins race, 所以 losing GrantPersistent
// 那 lost 到 Deny does 不 leak auto-approve 条目.
//
// All three public resolution methods (Grant, GrantPersistent, Deny)
// route through 这 helper 所以 multi-订阅者 UIs can race safely:
// the first caller wins, the rest become no-ops.
func (s *permissionService) resolve(permission PermissionRequest, granted, denied bool, onResolve func(PermissionRequest)) bool {
	pending, ok := s.pendingRequests.Take(permission.ID)
	if !ok {
		return false
	}

	// Everything below describes 请求 服务端 issued, 不 copy
	// answer arrived 用. Only ID 是 taken 从 caller, 和 it
	// 是 unguessable 和 single-use. 一个 客户端 那 echoed back different
	// 会话, 工具, action 或 路径 would otherwise have 那 echo recorded
	// 作为 thing being approved.
	req := pending.req

	if onResolve != nil {
		onResolve(req)
	}

	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		SessionID:  req.SessionID,
		ToolCallID: req.ToolCallID,
		Granted:    granted,
		Denied:     denied,
	})

	// respCh 是 buffered (cap 1) 和 只 ever has 在 most one sender
	// per 请求 because Take removes 条目 under map 锁,
	// 所以 这 send never blocks.
	pending.respCh <- granted
	return true
}

// permissionKeyFor builds 会话-grant key 为 请求.
func permissionKeyFor(req PermissionRequest) PermissionKey {
	return PermissionKey{
		SessionID: req.SessionID,
		ToolName:  req.ToolName,
		Action:    req.Action,
		Path:      req.Path,
		Grant:     req.GrantKey,
	}
}

func (s *permissionService) GrantPersistent(permission PermissionRequest) bool {
	// Record persistent grant 只 如果 这 调用 wins
	// pending-请求 race. Otherwise losing GrantPersistent 那
	// lost 到 Deny would still leave auto-approve 条目 behind,
	// silently flipping later denied calls 到 allowed.
	return s.resolve(permission, true, false, func(req PermissionRequest) {
		s.sessionPermissions.Set(permissionKeyFor(req), true)
	})
}

func (s *permissionService) Grant(permission PermissionRequest) bool {
	return s.resolve(permission, true, false, nil)
}

func (s *permissionService) Deny(permission PermissionRequest) bool {
	return s.resolve(permission, false, true, nil)
}

func (s *permissionService) Request(ctx context.Context, opts CreatePermissionRequest) (bool, error) {
	s.modeMu.RLock()
	mode := s.mode
	s.modeMu.RUnlock()

	if mode.SkipsPrompts() {
		return true, nil
	}

	// Check 如果 工具/action combination 是 在 allowlist
	commandKey := opts.ToolName + ":" + opts.Action
	if slices.Contains(s.allowedTools, commandKey) || slices.Contains(s.allowedTools, opts.ToolName) {
		return true, nil
	}

	// 一个 PreToolUse 钩子 那 returned decision=allow stamps 上下文
	// 用 工具调用 ID. Treat 那 作为 pre-approval 和 skip
	// 提示词 entirely. We still 发布 granted 通知 所以 UI
	// 和 audit subscribers see outcome.
	if hookApproved(ctx, opts.ToolCallID) {
		s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
			SessionID:  opts.SessionID,
			ToolCallID: opts.ToolCallID,
			Granted:    true,
		})
		return true, nil
	}

	unlock := s.lockScope(ctx, opts.SessionID)
	defer unlock()

	// tell UI 那 权限 was requested
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		SessionID:  opts.SessionID,
		ToolCallID: opts.ToolCallID,
	})

	s.autoApproveSessionsMu.RLock()
	autoApprove := s.autoApproveSessions[opts.SessionID]
	s.autoApproveSessionsMu.RUnlock()

	if autoApprove {
		s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
			SessionID:  opts.SessionID,
			ToolCallID: opts.ToolCallID,
			Granted:    true,
		})
		return true, nil
	}

	fileInfo, err := os.Stat(opts.Path)
	dir := opts.Path
	if err == nil {
		if fileInfo.IsDir() {
			dir = opts.Path
		} else {
			dir = filepath.Dir(opts.Path)
		}
	}

	if dir == "." {
		dir = s.workingDir
	}
	permission := PermissionRequest{
		ID:          uuid.New().String(),
		Path:        dir,
		SessionID:   opts.SessionID,
		ToolCallID:  opts.ToolCallID,
		ToolName:    opts.ToolName,
		Description: opts.Description,
		Action:      opts.Action,
		Params:      opts.Params,
		Danger:      opts.Danger,
		GrantKey:    opts.GrantKey,
	}

	if _, ok := s.sessionPermissions.Get(permissionKeyFor(permission)); ok {
		s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
			SessionID:  opts.SessionID,
			ToolCallID: opts.ToolCallID,
			Granted:    true,
		})
		return true, nil
	}

	s.activeRequests.Set(permission.ID, permission)
	defer s.activeRequests.Del(permission.ID)

	respCh := make(chan bool, 1)
	s.pendingRequests.Set(permission.ID, pendingRequest{req: permission, respCh: respCh})
	defer s.pendingRequests.Del(permission.ID)

	// 发布 请求
	s.Publish(pubsub.CreatedEvent, permission)
	resolved := s.observe(ctx, permission)

	select {
	case <-ctx.Done():
		// 提示词 是 abandoned without decision; tell subscribers
		// 所以 请求 they saw 是 不 left open.
		if _, ok := s.pendingRequests.Take(permission.ID); ok {
			s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
				SessionID:  opts.SessionID,
				ToolCallID: opts.ToolCallID,
				Cancelled:  true,
			})
			resolved(OutcomeCancelled)
			return false, ctx.Err()
		}
		// 一个 decision won race; resolve always delivers it.
		resolved(outcomeOf(<-respCh))
		return false, ctx.Err()
	case granted := <-respCh:
		resolved(outcomeOf(granted))
		return granted, nil
	}
}

// lockScope waits until no earlier 请求 在 scope 的 sessionID 是
// awaiting decision, 和 returns function 那 lets next
// 请求 在 那 scope through.
func (s *permissionService) lockScope(ctx context.Context, sessionID string) func() {
	var key string
	if scope := s.scope.Load(); scope != nil {
		key = (*scope)(ctx, sessionID)
	}

	s.requestLocksMu.Lock()
	l, ok := s.requestLocks[key]
	if !ok {
		l = &requestLock{}
		s.requestLocks[key] = l
	}
	l.refs++
	s.requestLocksMu.Unlock()

	l.Lock()
	return func() {
		l.Unlock()
		s.requestLocksMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(s.requestLocks, key)
		}
		s.requestLocksMu.Unlock()
	}
}

// observe reports 提示词 到 observer 和 returns function
// 那 reports 它的 outcome. Both 运行 在 one goroutine 所以 observer
// always hears them 在 order, 和 outlive ctx 所以 cancelled turn still
// reports the cancellation.
func (s *permissionService) observe(ctx context.Context, req PermissionRequest) func(Outcome) {
	if s.observer == nil {
		return func(Outcome) {}
	}
	ctx = context.WithoutCancel(ctx)
	outcome := make(chan Outcome, 1)
	go func() {
		s.observer.PromptShown(ctx, req)
		s.observer.PromptResolved(ctx, req, <-outcome)
	}()
	return func(o Outcome) { outcome <- o }
}

func outcomeOf(granted bool) Outcome {
	if granted {
		return OutcomeGranted
	}
	return OutcomeDenied
}

// ActiveRequests returns requests 那 是 currently awaiting
// decision. An 条目 outlives decision 在 它的 请求 (或
// cancellation 的 它的 waiter) until 请求 returns, 所以 请求 是
// 只 reported while 它的 ID 是 still registered 在 pendingRequests,
// i.e. while Grant/Deny 可以 actually resolve it.
func (s *permissionService) ActiveRequests() []PermissionRequest {
	var active []PermissionRequest
	for req := range s.activeRequests.Seq() {
		if _, ok := s.pendingRequests.Get(req.ID); ok {
			active = append(active, req)
		}
	}
	return active
}

func (s *permissionService) SetSessionScope(scope SessionScope) {
	if scope == nil {
		s.scope.Store(nil)
		return
	}
	s.scope.Store(&scope)
}

func (s *permissionService) AutoApproveSession(sessionID string) {
	s.autoApproveSessionsMu.Lock()
	s.autoApproveSessions[sessionID] = true
	s.autoApproveSessionsMu.Unlock()
}

func (s *permissionService) InheritAutoApproval(parentSessionID, childSessionID string) {
	s.autoApproveSessionsMu.Lock()
	defer s.autoApproveSessionsMu.Unlock()
	if s.autoApproveSessions[parentSessionID] {
		s.autoApproveSessions[childSessionID] = true
	}
}

func (s *permissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification] {
	return s.notificationBroker.Subscribe(ctx)
}

func (s *permissionService) SetPermissionMode(mode PermissionMode) {
	s.modeMu.Lock()
	s.mode = mode
	s.modeMu.Unlock()
	s.modeBroker.Publish(pubsub.UpdatedEvent, ModeChangedEvent{Mode: mode})
}

func (s *permissionService) SubscribeModeChanges(ctx context.Context) <-chan pubsub.Event[ModeChangedEvent] {
	return s.modeBroker.Subscribe(ctx)
}

func (s *permissionService) PermissionMode() PermissionMode {
	s.modeMu.RLock()
	defer s.modeMu.RUnlock()
	return s.mode
}

func NewPermissionService(workingDir string, allowedTools []string, observer PromptObserver) Service {
	return &permissionService{
		Broker:              pubsub.NewBroker[PermissionRequest](),
		notificationBroker:  pubsub.NewBroker[PermissionNotification](),
		modeBroker:          pubsub.NewBroker[ModeChangedEvent](),
		workingDir:          workingDir,
		sessionPermissions:  csync.NewMap[PermissionKey, bool](),
		autoApproveSessions: make(map[string]bool),
		mode:                PermissionModeNormal,
		allowedTools:        allowedTools,
		pendingRequests:     csync.NewMap[string, pendingRequest](),
		observer:            observer,
		requestLocks:        make(map[string]*requestLock),
		activeRequests:      csync.NewMap[string, PermissionRequest](),
	}
}
