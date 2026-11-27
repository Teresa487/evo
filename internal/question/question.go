// Package question provides services 为 asking 用户 questions
// via TUI 和 blocking until answer 是 received. 它 mirrors
// 权限 service pattern: 发布 请求 over pubsub,
// block 在 渠道, 和 resolve 当 UI sends back answers.
//
// 一个 会话 asks one question 在 time ( 工具 blocks until
// answered), 但 several sessions can ask 在 once, 所以 answers 和
// cancellations name batch they resolve 通过 它的 请求 ID.
package question

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

// ErrCancelled 是 returned 通过 Ask 当 用户 cancels question.
var ErrCancelled = errors.New("question cancelled by user")

// Type identifies kind 的 question 到 present.
type Type string

const (
	TypeYesNo        Type = "yes_no"
	TypeSingleChoice Type = "single_choice"
	TypeMultiChoice  Type = "multi_choice"
	TypeFreeText     Type = "free_text"
)

// Choice represents a single selectable option.
type Choice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question 是 single question definition within 请求.
type Question struct {
	ID          string   `json:"id"`
	Type        Type     `json:"type"`
	Label       string   `json:"label,omitempty"`
	Text        string   `json:"question"`
	Description string   `json:"description,omitempty"`
	Choices     []Choice `json:"choices,omitempty"`
}

// QuestionNoteKey 是 Answer.备注 key 为 note 在 question
// itself; 备注 在 individual choices 是 keyed 通过 choice ID.
const QuestionNoteKey = "_question"

// Answer carries 用户's 响应 到 single Question.
type Answer struct {
	QuestionID  string            `json:"question_id"`
	SelectedIDs []string          `json:"selected_ids,omitempty"`
	FillInText  string            `json:"fill_in_text,omitempty"`
	Yes         *bool             `json:"yes,omitempty"`
	Notes       map[string]string `json:"notes,omitempty"`
}

// HasNotes reports whether any 备注 were attached.
func (a Answer) HasNotes() bool { return len(a.Notes) > 0 }

// 请求 是 service envelope published 到 UI. 它 contains
// one 或 more Questions. 一个 single question renders without tabs;
// multiple questions render 作为 tabbed form 用 confirmation.
type Request struct {
	ID                 string     `json:"id"`
	SessionID          string     `json:"session_id"`
	ToolCallID         string     `json:"tool_call_id"`
	Questions          []Question `json:"questions"`
	ConfirmTitle       string     `json:"confirm_title,omitempty"`
	ConfirmDescription string     `json:"confirm_description,omitempty"`
}

// Validate checks 那 请求 has valid fields. For multiple
// questions, ConfirmTitle 和 ConfirmDescription 是 required.
func (r Request) Validate() error {
	if len(r.Questions) == 0 {
		return fmt.Errorf("at least one question is required")
	}
	if len(r.Questions) > MaxQuestions {
		return fmt.Errorf("questions exceed maximum of %d (got %d)", MaxQuestions, len(r.Questions))
	}
	for i, q := range r.Questions {
		if err := q.Validate(); err != nil {
			return fmt.Errorf("question %d: %w", i+1, err)
		}
	}
	return nil
}

// Validate checks 那 Question has valid fields. 错误 messages
// 是 written 为 LLM consumption: specific 和 actionable.
func (q Question) Validate() error {
	label := q.identifier()
	if q.Text == "" {
		return fmt.Errorf("%s: question text is required", label)
	}
	if len(q.Text) > MaxQuestionLength {
		return fmt.Errorf("%s: text exceeds %d characters (got %d)", label, MaxQuestionLength, len(q.Text))
	}
	if q.Description == "" {
		return fmt.Errorf("%s: description is required", label)
	}
	if len(q.Description) > MaxDescriptionLength {
		return fmt.Errorf("%s: description exceeds %d characters (got %d)", label, MaxDescriptionLength, len(q.Description))
	}
	switch q.Type {
	case TypeYesNo, TypeFreeText:
		// No choices needed.
	case TypeSingleChoice, TypeMultiChoice:
		if len(q.Choices) < 2 {
			return fmt.Errorf("%s: %s requires at least 2 choices in the \"choices\" array (got %d). Use \"choices\", not \"options\"", label, q.Type, len(q.Choices))
		}
		if len(q.Choices) > MaxChoices {
			return fmt.Errorf("%s: choices exceed maximum of %d (got %d)", label, MaxChoices, len(q.Choices))
		}
		seen := make(map[string]bool, len(q.Choices))
		for i, c := range q.Choices {
			if c.ID == "" {
				return fmt.Errorf("%s: choice %d must have an \"id\" field", label, i+1)
			}
			if seen[c.ID] {
				return fmt.Errorf("%s: choice %d has duplicate id %q", label, i+1, c.ID)
			}
			seen[c.ID] = true
			if c.Label == "" {
				return fmt.Errorf("%s: choice %d (%s) must have a \"label\" field", label, i+1, c.ID)
			}
			if len(c.Label) > MaxChoiceLabelLength {
				return fmt.Errorf("%s: choice %d label exceeds %d characters (got %d)", label, i+1, MaxChoiceLabelLength, len(c.Label))
			}
			if len(c.Description) > MaxChoiceDescriptionLength {
				return fmt.Errorf("%s: choice %d description exceeds %d characters (got %d)", label, i+1, MaxChoiceDescriptionLength, len(c.Description))
			}
		}
	default:
		return fmt.Errorf("%s: unknown type %q (must be yes_no, single_choice, multi_choice, or free_text)", label, q.Type)
	}
	return nil
}

// identifier returns human-readable label 为 错误 messages.
// Uses question label, 文本 excerpt, 或 fallback.
func (q Question) identifier() string {
	if q.Label != "" {
		return fmt.Sprintf("[%s]", q.Label)
	}
	if q.Text != "" {
		t := q.Text
		if len(t) > 40 {
			t = t[:40] + "…"
		}
		return fmt.Sprintf("[%s]", t)
	}
	return "[unnamed question]"
}

const (
	MaxQuestionLength          = 240
	MaxDescriptionLength       = 600
	MaxChoiceLabelLength       = 200
	MaxChoiceDescriptionLength = 200
	MaxChoices                 = 5
	MaxQuestions               = 5
)

// 通知 是 published 当 question batch 是 resolved 所以
// 那 non-answering clients can dismiss their open forms.
type Notification struct {
	BatchID   string `json:"batch_id"`
	SessionID string `json:"session_id"`
}

// Service manages lifecycle 的 question requests. Several
// sessions can each have batch pending 在 once, 所以 answers 和
// cancellations name the batch they resolve.
type Service interface {
	pubsub.Subscriber[Request]

	// SubscribeNotifications returns 渠道 为 question
	// resolution notifications.
	SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[Notification]

	// Ask publishes questions 和 blocks until 用户 answers
	// 或 上下文 是 cancelled.
	Ask(ctx context.Context, req Request) ([]Answer, error)

	// Answer delivers answers 到 pending batch 用 given ID.
	// An empty ID names 只 pending batch, 为 callers 那
	// predate batch IDs. Returns false 如果 no such batch 是 pending.
	Answer(batchID string, answers []Answer) bool

	// 取消 cancels pending batch 用 given ID, treating
	// empty ID 作为 Answer does. Returns false 如果 no such batch 是
	// pending.
	Cancel(batchID string) bool

	// Pending returns requests 那 是 currently awaiting
	// answer, 在 no particular order.
	Pending() []Request
}

type questionService struct {
	broker             *pubsub.Broker[Request]
	notificationBroker *pubsub.Broker[Notification]
	mu                 sync.Mutex
	pending            map[string]*pendingBatch
}

// pendingBatch 是 请求 awaiting answer, 用 channels 它的
// Ask 调用 waits 在.
type pendingBatch struct {
	req       Request
	answers   chan []Answer
	cancelled chan struct{}
}

// NewService creates a new question service.
func NewService() *questionService {
	return &questionService{
		broker:             pubsub.NewBroker[Request](),
		notificationBroker: pubsub.NewBroker[Notification](),
		pending:            make(map[string]*pendingBatch),
	}
}

// Subscribe returns 渠道 为 question events.
func (s *questionService) Subscribe(ctx context.Context) <-chan pubsub.Event[Request] {
	return s.broker.Subscribe(ctx)
}

// SubscribeNotifications returns 渠道 为 question resolution
// notifications.
func (s *questionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[Notification] {
	return s.notificationBroker.Subscribe(ctx)
}

// Ask publishes 请求 和 blocks until 用户 answers.
func (s *questionService) Ask(ctx context.Context, req Request) ([]Answer, error) {
	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	for i := range req.Questions {
		if req.Questions[i].ID == "" {
			req.Questions[i].ID = uuid.New().String()
		}
	}

	// Apply defaults 为 multi-question confirm fields.
	if len(req.Questions) >= 2 {
		if req.ConfirmTitle == "" {
			req.ConfirmTitle = "Ready to go?"
		}
		if req.ConfirmDescription == "" {
			req.ConfirmDescription = "Review your answers above and confirm."
		}
	}

	if err := req.Validate(); err != nil {
		return nil, err
	}

	p := &pendingBatch{
		req:       req,
		answers:   make(chan []Answer, 1),
		cancelled: make(chan struct{}),
	}
	s.mu.Lock()
	if _, ok := s.pending[req.ID]; ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("question batch %q is already pending", req.ID)
	}
	s.pending[req.ID] = p
	s.mu.Unlock()

	defer func() {
		// 一个 resolver 那 took batch already removed it, 和 它的
		// ID may since have been reused.
		s.mu.Lock()
		if s.pending[req.ID] == p {
			delete(s.pending, req.ID)
		}
		s.mu.Unlock()
	}()

	s.broker.Publish(pubsub.CreatedEvent, req)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.cancelled:
		return nil, ErrCancelled
	case answers := <-p.answers:
		return answers, nil
	}
}

// take removes pending batch 用 given ID 和 returns it.
// Removing it under 锁 makes caller 它的 只 resolver. An
// empty ID takes 只 pending batch; 用 several pending it 是
// ambiguous 和 takes none.
func (s *questionService) take(batchID string) (*pendingBatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batchID == "" && len(s.pending) == 1 {
		for id := range s.pending {
			batchID = id
		}
	}
	p, ok := s.pending[batchID]
	if ok {
		delete(s.pending, batchID)
	}
	return p, ok
}

// Answer resolves pending batch 用 given ID. Returns false
// 如果 no such batch 是 pending (already answered 或 cancelled).
func (s *questionService) Answer(batchID string, answers []Answer) bool {
	p, ok := s.take(batchID)
	if !ok {
		return false
	}
	// 渠道 是 buffered 和 take made 这 它的 只 sender, 所以
	// 这 never blocks.
	p.answers <- answers

	// 发布 通知 所以 non-answering clients can dismiss
	// their open question forms.
	s.notificationBroker.Publish(pubsub.CreatedEvent, Notification{
		BatchID:   p.req.ID,
		SessionID: p.req.SessionID,
	})
	return true
}

// 取消 cancels pending batch 用 given ID. Returns false
// 如果 no such batch 是 pending.
func (s *questionService) Cancel(batchID string) bool {
	p, ok := s.take(batchID)
	if !ok {
		return false
	}
	close(p.cancelled)

	// 发布 通知 所以 non-answering clients can dismiss
	// their open question forms.
	s.notificationBroker.Publish(pubsub.CreatedEvent, Notification{
		BatchID:   p.req.ID,
		SessionID: p.req.SessionID,
	})
	return true
}

// Pending returns requests 那 是 currently awaiting answer,
// 在 no particular order.
func (s *questionService) Pending() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	reqs := make([]Request, 0, len(s.pending))
	for _, p := range s.pending {
		reqs = append(reqs, p.req)
	}
	return reqs
}
