// Package sshaskpass renders OpenSSH credential 和 confirmation
// prompts 作为 native Crush dialogs. Commands Crush spawns (ssh, scp,
// sftp, ssh-add, 或 anything 那 shells out 到 OpenSSH such 作为
// git-over-ssh 或 rsync) 运行 用 SSH_ASKPASS pointed 在 hidden Crush
// subcommand 那 forwards 提示词 over Unix socket 到 Crush
// process 那 owns 终态; UI shows masked password dialog
// 或, 为 confirmations (security key touches, host key acceptance),
// confirm dialog.
package sshaskpass

import (
	"context"
	"errors"
	"sync"

	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

// Kind identifies kind 的 askpass question.
type Kind string

const (
	// KindPassword 是 SSH authentication password 或 private key
	// passphrase, collected 在 masked input.
	KindPassword Kind = "password"
	// KindConfirm 是 decision question 那 must 不 是 answered
	// automatically: host key acceptance 或 ssh-add key use
	// confirmation. dialog submits "yes" 到 confirm 和 cancels 到
	// decline.
	KindConfirm Kind = "confirm"
	// KindTouch 是 passive security key touch (FIDO 用户 presence,
	// "touch your device" instruction). physical touch 是
	// approval, 所以 UI just surfaces warning 和 confirms
	// automatically; no input 是 collected.
	KindTouch Kind = "touch"
)

// PromptRequest describes credential 或 confirmation 用户 must
// provide. 它 是 published over pubsub 和 rendered 通过 UI 作为
// masked-input 或 confirm dialog, 或 auto-confirmed 作为 touch warning.
// resolved secret 是 never carried 在 这 type.
type PromptRequest struct {
	// ID identifies 请求; used 到 correlate 响应.
	ID string `json:"id"`
	// 提示词 是 phrasing OpenSSH used (e.g. "用户@host's
	// password: " 或 "Confirm 用户 presence 为 key ...").
	Prompt string `json:"prompt"`
	// KeyInfo 是 short hint identifying account 或 key 路径, 当
	// it can 是 parsed 从 提示词.
	KeyInfo string `json:"key_info,omitempty"`
	// Kind reports whether 这 是 password 或 confirmation.
	Kind Kind `json:"kind"`
	// 标题 是 short, friendly heading 为 dialog, e.g. "SSH
	// Passphrase". Parsed 从 raw 提示词.
	Title string `json:"title,omitempty"`
	// 消息 是 friendly one-line description 的 what OpenSSH 是
	// asking 为, shown 在 place 的 raw 提示词 文本.
	Message string `json:"message,omitempty"`
}

// 通知 是 published 当 提示词 是 resolved 所以 那 clients
// which did 不 answer it can dismiss their open dialogs.
type Notification struct {
	RequestID string `json:"request_id"`
}

// ErrCancelled 是 returned 当 用户 cancelled 提示词.
var ErrCancelled = errors.New("ssh prompt cancelled by user")

// ErrNoPrompter 是 returned 当 no one can answer prompts (no
// subscribers), 所以 callers should abort cleanly instead 的 hanging.
var ErrNoPrompter = errors.New("no ssh prompt listener available")

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
	// Secret 是 用户-provided credential, 或 "yes" 为 confirmations.
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

// DefaultPrompts returns process-wide 提示词 service. ssh
// askpass subprocess uses it over socket, 和 app forwards 它的
// events 到 UI.
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
	// callers use ErrNoPrompter 到 abort cleanly instead.
	if s.broker.GetSubscriberCount() == 0 {
		return "", ErrNoPrompter
	}

	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	if req.Kind == "" {
		req.Kind = KindPassword
	}

	// Concurrent ssh operations wait their turn instead 的 failing.
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
	// 缓冲 would hang ssh command.
	s.broker.PublishMustDeliver(context.Background(), pubsub.CreatedEvent, req)

	select {
	case resp := <-respCh:
		if resp.Cancelled {
			return "", ErrCancelled
		}
		return resp.Secret, nil
	case <-ctx.Done():
		// The ssh command went away unanswered: let clients dismiss
		// the now-stale dialog.
		s.notify(req.ID)
		return "", ctx.Err()
	}
}

// notify publishes resolution 所以 clients close matching dialog.
func (s *Prompts) notify(id string) {
	s.notificationBroker.Publish(pubsub.UpdatedEvent, Notification{RequestID: id})
}

// Respond resolves pending 提示词 用 用户's secret. Returns
// false 当 no matching 提示词 是 pending.
func (s *Prompts) Respond(id, secret string) bool {
	return s.resolve(PromptResponse{ID: id, Secret: secret})
}

// 取消 dismisses pending 提示词. Returns false 当 no matching
// 提示词 是 pending.
func (s *Prompts) Cancel(id string) bool {
	return s.resolve(PromptResponse{ID: id, Cancelled: true})
}

// resolve delivers 在 most one 响应 到 matching pending 提示词.
func (s *Prompts) resolve(resp PromptResponse) bool {
	s.mu.Lock()
	ch, ok := s.pending, s.pending != nil && s.pendingID == resp.ID
	s.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- resp:
	default:
		return false
	}
	s.notify(resp.ID)
	return true
}
