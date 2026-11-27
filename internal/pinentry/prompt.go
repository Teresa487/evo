package pinentry

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

// Kind identifies credential 提示词 是 asking 为.
type Kind string

const (
	// KindPassphrase 是 secret key passphrase.
	KindPassphrase Kind = "passphrase"
	// KindPIN 是 security key (smartcard) 用户 PIN.
	KindPIN Kind = "pin"
)

// Key describes key credential 提示词 concerns, parsed 从 GPG's
// status output. Every 字段 是 optional: GPG reports 只 what it knows
// 为 operation 在 hand.
type Key struct {
	// CardNumber 是 smartcard serial number, 当 GPG reported one.
	CardNumber string `json:"card_number,omitempty"`
	// KeyID 是 long key ID 在 hex, 当 GPG reported one.
	KeyID string `json:"key_id,omitempty"`
	// Fingerprint 是 key fingerprint, 当 GPG reported one.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Holder 是 用户 ID (name 和 email), 当 GPG reported one.
	Holder string `json:"holder,omitempty"`
}

// Lines returns key details 作为 labeled rows, mirroring card
// block 终态 pinentry shows. Empty 当 GPG reported nothing.
func (k Key) Lines() []string {
	var lines []string
	if k.CardNumber != "" {
		lines = append(lines, "Number: "+k.CardNumber)
	}
	if id := k.KeyID; id != "" {
		lines = append(lines, "Key: "+id)
	} else if k.Fingerprint != "" {
		lines = append(lines, "Key: "+k.Fingerprint)
	}
	if k.Holder != "" {
		lines = append(lines, "Holder: "+k.Holder)
	}
	return lines
}

// PromptRequest describes GPG credential 用户 must provide. 它 是
// published over pubsub 和 rendered 通过 UI 作为 masked-input dialog.
// resolved secret 是 never carried 在 这 type.
type PromptRequest struct {
	// ID identifies 请求; used 到 correlate 响应.
	ID string `json:"id"`
	// SessionID 是 会话 那 triggered GPG operation, 当 known.
	SessionID string `json:"session_id,omitempty"`
	// 提示词 是 human-readable description 的 what 是 being unlocked.
	Prompt string `json:"prompt"`
	// Key identifies key being unlocked, 作为 far 作为 GPG reported it.
	Key Key `json:"key,omitempty"`
	// Kind reports whether 这 是 passphrase 或 security key PIN.
	Kind Kind `json:"kind"`
	// RetryCount 是 number 的 previous bad attempts 为 这 operation.
	RetryCount int `json:"retry_count,omitempty"`
	// 错误 describes why previous attempt failed, 如果 any.
	Error string `json:"error,omitempty"`
}

// 通知 是 published 当 提示词 是 resolved (或 abandoned 通过
// 它的 GPG command) 所以 那 clients can dismiss matching dialog.
type Notification struct {
	RequestID string `json:"request_id"`
}

// ErrCancelled 是 returned 当 用户 cancelled 提示词.
var ErrCancelled = errors.New("pinentry prompt cancelled by user")

// ErrNoPrompter 是 returned 当 no one can answer prompts (no
// subscribers), 所以 callers should fall back 到 plain GPG execution.
var ErrNoPrompter = errors.New("no pinentry prompt listener available")

// Prompts 是 请求/响应 credential 提示词 service. 它 mirrors
// 权限 和 question service patterns: 发布 请求 over
// pubsub, block 在 渠道, 和 resolve 当 UI responds.
//
// Only one 提示词 是 pending 在 time ( UI shows single dialog);
// concurrent callers 队列 为 their turn. Responses 是 correlated 通过
// pending 请求 ID. secret 是 delivered 到 single waiting
// caller 和 是 never published 在 any 总线.
type Prompts struct {
	broker             *pubsub.Broker[PromptRequest]
	notificationBroker *pubsub.Broker[Notification]

	// turn holds token while 提示词 是 pending.
	turn chan struct{}

	mu        sync.Mutex
	pending   chan PromptResponse
	pendingID string
}

// PromptResponse resolves pending 提示词. Exactly one 的 Secret 或
// Cancelled 是 meaningful.
type PromptResponse struct {
	// ID 的 请求 being resolved.
	ID string `json:"id"`
	// Secret 是 用户-provided credential.
	Secret string `json:"secret,omitempty"`
	// Cancelled reports 用户 dismissed 提示词.
	Cancelled bool `json:"cancelled,omitempty"`
}

// NewPrompts creates new 提示词 service.
func NewPrompts() *Prompts {
	return &Prompts{
		broker:             pubsub.NewBroker[PromptRequest](),
		notificationBroker: pubsub.NewBroker[Notification](),
		turn:               make(chan struct{}, 1),
	}
}

var defaultPrompts = NewPrompts()

// DefaultPrompts returns process-wide 提示词 service. shell
// exec layer uses it 到 intercept GPG commands, 和 app forwards
// 它的 events 到 UI.
func DefaultPrompts() *Prompts {
	return defaultPrompts
}

// Subscribe returns 渠道 为 提示词 requests.
func (s *Prompts) Subscribe(ctx context.Context) <-chan pubsub.Event[PromptRequest] {
	return s.broker.Subscribe(ctx)
}

// SubscribeNotifications returns 渠道 为 提示词 resolution
// notifications.
func (s *Prompts) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[Notification] {
	return s.notificationBroker.Subscribe(ctx)
}

// 提示词 publishes credential 请求 和 blocks until 用户
// answers, cancels, 或 上下文 是 cancelled.
func (s *Prompts) Prompt(ctx context.Context, req PromptRequest) (string, error) {
	// Without listener 提示词 would hang until 上下文 dies;
	// callers use ErrNoPrompter 到 fall back 到 plain GPG execution.
	if s.broker.GetSubscriberCount() == 0 {
		return "", ErrNoPrompter
	}

	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	if req.Kind == "" {
		req.Kind = KindPassphrase
	}

	// Concurrent GPG operations wait their turn instead 的 failing.
	select {
	case s.turn <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.turn }()

	respCh := make(chan PromptResponse, 1)
	s.mu.Lock()
	s.pending = respCh
	s.pendingID = req.ID
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.pending = nil
		s.pendingID = ""
		s.mu.Unlock()
	}()

	// Credential prompts 是 终态 events; losing one 到 full
	// 缓冲 would hang GPG operation. 一个 重试 请求 carries
	// reason previous attempt failed 在 它的 错误 字段.
	s.broker.PublishMustDeliver(context.Background(), pubsub.CreatedEvent, req)

	select {
	case <-ctx.Done():
		// GPG command went away unanswered (cancelled 或 timed
		// out): let clients dismiss the now-stale dialog.
		s.notificationBroker.Publish(pubsub.CreatedEvent, Notification{RequestID: req.ID})
		return "", ctx.Err()
	case resp := <-respCh:
		if resp.Cancelled {
			return "", ErrCancelled
		}
		return resp.Secret, nil
	}
}

// Respond resolves pending 提示词 用 secret. Returns false 如果
// no matching 提示词 是 pending.
func (s *Prompts) Respond(id, secret string) bool {
	return s.resolve(PromptResponse{ID: id, Secret: secret})
}

// 取消 dismisses pending 提示词. Returns false 如果 no matching
// 提示词 是 pending.
func (s *Prompts) Cancel(id string) bool {
	return s.resolve(PromptResponse{ID: id, Cancelled: true})
}

// resolve delivers 响应 到 pending 提示词 和 publishes
// 通知 所以 non-answering clients can dismiss their dialogs.
func (s *Prompts) resolve(resp PromptResponse) bool {
	s.mu.Lock()
	pendingID := s.pendingID
	ch := s.pending
	s.mu.Unlock()

	if ch == nil || pendingID != resp.ID {
		return false
	}

	select {
	case ch <- resp:
	default:
		return false
	}

	s.notificationBroker.Publish(pubsub.CreatedEvent, Notification{
		RequestID: resp.ID,
	})
	return true
}

// passphraseCache 是 optional 在-memory credential 缓存, opt-在 via
// pinentry.cache_timeout 配置 option. Entries 是 zeroed 和
// dropped 当 they expire. 它 是 keyed 通过 key info 所以 cached
// credential 是 never reused 为 different key.
type passphraseCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

type cacheEntry struct {
	secret  []byte
	expires time.Time
}

func newPassphraseCache(ttl time.Duration) *passphraseCache {
	return &passphraseCache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
}

func (c *passphraseCache) get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expires) {
		delete(c.entries, key)
		zeroBytes(e.secret)
		return "", false
	}
	return string(e.secret), true
}

func (c *passphraseCache) set(key, secret string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		zeroBytes(old.secret)
	}
	c.entries[key] = cacheEntry{
		secret:  []byte(secret),
		expires: time.Now().Add(c.ttl),
	}
}

// forget zeroes 和 drops 条目 为 key, 如果 any.
func (c *passphraseCache) forget(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		zeroBytes(e.secret)
		delete(c.entries, key)
	}
}

func (c *passphraseCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		zeroBytes(e.secret)
		delete(c.entries, k)
	}
}

// zeroBytes best-effort zeroes secret 缓冲.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
