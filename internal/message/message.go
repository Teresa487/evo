// message.go —— 消息的持久化服务，是「上下文从哪里读、结果存到哪里」的源头。
//
// 从产物反推：
//
//	用户看到的回复 = currentAssistant.Content()
//	  ← service.Update 把 Stream 增量写进 Message（带 debounce/flush）
//	  ← service.Create 在 Run 的 PrepareStep 里建空 assistant 消息
//	  ← service.List / ListFromSummary 在下一轮把历史读出来
//
// 关键能力：
//   - Create/Update/Delete/List：消息 CRUD
//   - Flush/FlushAll：把 debounce 的流式增量落盘；Run 结束前必须调用
//   - ListFromSummary：压缩后只读「摘要 + 之后的消息」，实现上下文截断
//   - marshalParts/unmarshalParts：内容片段的 JSON 编解码
//
// 它是存储层，不理解「什么是回答」；只保证消息正确落盘和可重放。
package message

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

// defaultUpdateDebounce 是 默认 debounce 窗口 为 [Service.更新].
// Streaming deltas 那 arrive within 窗口 是 coalesced into
// single SQL 写入 和 single pubsub 事件. 终态 updates
// (finish/错误/取消/工具-调用 structural changes) bypass
// debounce 和 刷盘 synchronously.
const defaultUpdateDebounce = 33 * time.Millisecond

type CreateMessageParams struct {
	Role             MessageRole
	Parts            []ContentPart
	Model            string
	ModelName        string
	Provider         string
	IsSummaryMessage bool
}

// Service 是 public interface 到 消息 存储.
//
// [Service.更新] 是 eventually consistent: it accepts new state into
// 在-memory 缓冲 和 writes it 到 SQLite plus publishes
// [pubsub.UpdatedEvent] 在 next debounce tick (默认
// [defaultUpdateDebounce]) 或 在 next 终态-state 更新,
// whichever comes first. 终态-state updates — those 那 finish
// 消息, add 或 finish 工具调用, 或 end reasoning section —
// 刷盘 synchronously before [Service.更新] returns.
//
// Callers 那 need stronger ordering (e.g. tests, shutdown,
// 会话-switch reads) must use [Service.刷盘] 或 [Service.FlushAll]
// 在通过 [Service.Get] / [Service.List] 读取之前。如果没有
// explicit 刷盘, 读取 can race debounce timer 和 miss
// most recent 在-memory state.
type Service interface {
	pubsub.Subscriber[Message]
	Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error)
	Update(ctx context.Context, message Message) error
	Get(ctx context.Context, id string) (Message, error)
	List(ctx context.Context, sessionID string) ([]Message, error)
	// ListFromSummary returns messages 在 或 after summaryMessageID,
	// which 是 all compacted 会话 still sends.
	ListFromSummary(ctx context.Context, sessionID, summaryMessageID string) ([]Message, error)
	ListUserMessages(ctx context.Context, sessionID string) ([]Message, error)
	ListAllUserMessages(ctx context.Context) ([]Message, error)
	GetLastAssistantMessage(ctx context.Context, sessionID string) (Message, error)
	Delete(ctx context.Context, id string) error
	DeleteSessionMessages(ctx context.Context, sessionID string) error

	// 刷盘 synchronously drains any pending debounced state 为
	// given 消息 ID, performs SQL 写入, 和 publishes
	// resulting [pubsub.UpdatedEvent]. Idempotent; cheap no-op 如果 no
	// updates 是 pending. Use 这 before any 读取 那 must observe
	// latest [Service.更新].
	Flush(ctx context.Context, id string) error

	// FlushAll synchronously drains pending debounced state 为 every
	// 消息 known 到 service. Intended 为 shutdown 和
	// 会话-switch paths.
	FlushAll(ctx context.Context) error
}

// pendingState holds 在-memory coalescing 缓冲 为 single
// 消息 ID. All fields except where noted 是 guarded 通过
// service.mu. flushing flag serializes concurrent flushers 为
// same ID 所以 SQL writes never reorder.
type pendingState struct {
	// latest 是 most recent [消息] passed 到 [Service.更新]
	// 那 has 不 yet been flushed.
	latest Message

	// dirty 是 true 当 latest contains state 那 has 不 been
	// written 到 SQL since last successful 刷盘.
	dirty bool

	// flushing 是 true while goroutine 是 performing SQL 写入
	// 为 这 ID. New updates 是 still accepted (和 re-标记 dirty)
	// 但 other flushers must back off.
	flushing bool

	// timer 是 活跃 debounce timer, 或 nil 如果 no 刷盘 是
	// scheduled. Stopped 和 reset 当 终态 更新 preempts
	// debounce 窗口.
	timer *time.Timer

	// baseline 是 projection 的 snapshot most recently
	// written 到 SQL 那 终态-state detection needs. Used 作为
	// baseline 为 [shouldFlushNow].
	baseline flushBaseline

	// hasFlushed 是 false until first successful 写入 为 这
	// ID; until then baseline 是 zero 值 和 must 不 是
	// treated 作为 real prior state.
	hasFlushed bool
}

// flushBaseline 是 compact projection 的 flushed [消息] 那
// [shouldFlushNow] compares against. Pending state lives 为 作为 long
// 作为 process does, 所以 it retains 这 instead 的 whole
// 消息: finished 流 would otherwise pin 它的 parts — reasoning
// 文本, 工具 inputs, 工具 results — 在 heap forever.
type flushBaseline struct {
	toolCallsFinished   []bool
	reasoningFinishedAt int64
}

// newFlushBaseline projects fields [shouldFlushNow] reads out 的
// flushed 消息.
func newFlushBaseline(m *Message) flushBaseline {
	calls := m.ToolCalls()
	finished := make([]bool, len(calls))
	for i, c := range calls {
		finished[i] = c.Finished
	}
	return flushBaseline{
		toolCallsFinished:   finished,
		reasoningFinishedAt: m.ReasoningContent().FinishedAt,
	}
}

type service struct {
	*pubsub.Broker[Message]
	q        db.Querier
	debounce time.Duration

	mu      sync.Mutex
	pending map[string]*pendingState
}

// ServiceOption configures [Service] 在 construction.
type ServiceOption func(*service)

// WithDebounce overrides debounce 窗口 为 [Service.更新]. 一个
// zero 或 negative 值 disables debouncing entirely (every 更新
// flushes synchronously). Intended primarily 为 tests.
func WithDebounce(d time.Duration) ServiceOption {
	return func(s *service) {
		s.debounce = d
	}
}

// CoalesceKey 让较新的 UpdatedEvent 快照替换尚未投递的
// older one 为 same 消息, bounding what slow 订阅者 holds.
func CoalesceKey(ev pubsub.Event[Message]) string {
	if ev.Type != pubsub.UpdatedEvent {
		return ""
	}
	return ev.Payload.ID
}

func NewService(q db.Querier, opts ...ServiceOption) Service {
	s := &service{
		Broker:   pubsub.NewCoalescingBroker(CoalesceKey),
		q:        q,
		debounce: defaultUpdateDebounce,
		pending:  make(map[string]*pendingState),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *service) Delete(ctx context.Context, id string) error {
	message, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.q.DeleteMessage(ctx, message.ID)
	if err != nil {
		return err
	}
	// Drop any pending coalesced state 为 这 ID. We never want 到
	// 刷盘 back over deleted 行.
	s.mu.Lock()
	if p, ok := s.pending[id]; ok {
		if p.timer != nil {
			p.timer.Stop()
		}
		delete(s.pending, id)
	}
	s.mu.Unlock()
	// Clone 消息 before publishing 到 avoid race conditions 用
	// concurrent modifications 到 Parts slice.
	s.Publish(pubsub.DeletedEvent, message.Clone())
	return nil
}

func (s *service) Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error) {
	if params.Role != Assistant {
		params.Parts = append(params.Parts, Finish{
			Reason: "stop",
		})
	}
	partsJSON, err := marshalParts(params.Parts)
	if err != nil {
		return Message{}, err
	}
	isSummary := int64(0)
	if params.IsSummaryMessage {
		isSummary = 1
	}
	dbMessage, err := s.q.CreateMessage(ctx, db.CreateMessageParams{
		ID:               uuid.New().String(),
		SessionID:        sessionID,
		Role:             string(params.Role),
		Parts:            string(partsJSON),
		Model:            sql.NullString{String: string(params.Model), Valid: true},
		ModelName:        sql.NullString{String: params.ModelName, Valid: params.ModelName != ""},
		Provider:         sql.NullString{String: params.Provider, Valid: params.Provider != ""},
		IsSummaryMessage: isSummary,
	})
	if err != nil {
		return Message{}, err
	}
	message, err := s.fromDBItem(dbMessage)
	if err != nil {
		return Message{}, err
	}
	// Clone 消息 before publishing 到 avoid race conditions 用
	// concurrent modifications 到 Parts slice.
	s.Publish(pubsub.CreatedEvent, message.Clone())
	return message, nil
}

func (s *service) DeleteSessionMessages(ctx context.Context, sessionID string) error {
	messages, err := s.List(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if message.SessionID == sessionID {
			err = s.Delete(ctx, message.ID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// 更新 accepts new state 为 消息 和 either flushes
// synchronously (终态 updates, debounce <= 0) 或 buffers it until
// next debounce tick. See [Service] 为 contract.
func (s *service) Update(ctx context.Context, msg Message) error {
	cloned := msg.Clone()

	// Zero 或 negative debounce: 刷盘 every 更新 synchronously. 这
	// preserves pre-coalescing behaviour 为 tests 和 any caller
	// 那 explicitly opted out via [WithDebounce].
	if s.debounce <= 0 {
		s.mu.Lock()
		p, ok := s.pending[msg.ID]
		if !ok {
			p = &pendingState{}
			s.pending[msg.ID] = p
		}
		p.latest = cloned
		p.dirty = true
		s.mu.Unlock()
		return s.flushOne(ctx, msg.ID, true)
	}

	s.mu.Lock()
	p, ok := s.pending[msg.ID]
	if !ok {
		p = &pendingState{}
		s.pending[msg.ID] = p
	}
	p.latest = cloned
	p.dirty = true

	var prev *flushBaseline
	if p.hasFlushed {
		prev = &p.baseline
	}
	terminal := shouldFlushNow(prev, &cloned)

	if terminal {
		if p.timer != nil {
			p.timer.Stop()
			p.timer = nil
		}
		s.mu.Unlock()
		return s.flushOne(ctx, msg.ID, true)
	}

	// Debounce: schedule single 刷盘 per pending state. If 刷盘
	// 是 already running we let it finish; trailing dirty bit will
	// 是 picked up 通过 next 更新 或 通过 刷盘.
	if p.timer == nil && !p.flushing {
		id := msg.ID
		p.timer = time.AfterFunc(s.debounce, func() {
			// Detached 从 caller ctx 所以 cancelled 流 上下文
			// does 不 strand buffered 写入.
			_ = s.flushOne(context.Background(), id, false)
		})
	}
	s.mu.Unlock()
	return nil
}

// 刷盘 implements [Service.刷盘].
func (s *service) Flush(ctx context.Context, id string) error {
	return s.flushOne(ctx, id, true)
}

// FlushAll implements [Service.FlushAll]. 它 snapshots every ID 用
// outstanding work — either dirty buffered state 或 刷盘 already 在
// flight — then drains each one. Picking up 在-flight IDs ensures
// FlushAll cannot return while timer-fired 写入 是 still mid-SQL,
// which 是 what shutdown 和 会话-switch callers rely 在.
func (s *service) FlushAll(ctx context.Context) error {
	s.mu.Lock()
	ids := make([]string, 0, len(s.pending))
	for id, p := range s.pending {
		if p.dirty || p.flushing {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	var firstErr error
	for _, id := range ids {
		if err := s.flushOne(ctx, id, true); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// flushOne drains single 消息 ID. When syncCaller 是 true
// caller 是 willing 到 wait through concurrent 在-flight 刷盘 所以
// 那, 在 return, flushed state equals latest 在 moment 的
// 返回。
// When false (timer-fired 路径) we bail 如果 another flusher 是 already
// running; 那 flusher will pick up trailing dirty bit.
//
// Order matters: sync caller must wait 为 any 在-flight 刷盘 到
// drain even 当 缓冲 是 currently clean — 那 在-flight
// 写入 has 不 yet updated SQL 行, 所以 returning early would
// violate contract 那 在 success flushed state reflects
// 最新状态。
func (s *service) flushOne(ctx context.Context, id string, syncCaller bool) error {
	for {
		s.mu.Lock()
		p, ok := s.pending[id]
		if !ok {
			s.mu.Unlock()
			return nil
		}
		if p.flushing {
			if !syncCaller {
				s.mu.Unlock()
				return nil
			}
			s.mu.Unlock()
			// Brief yield; 在-flight 写入 should land 在 <1ms typical.
			time.Sleep(time.Millisecond)
			continue
		}
		if !p.dirty {
			s.mu.Unlock()
			return nil
		}

		if p.timer != nil {
			p.timer.Stop()
			p.timer = nil
		}
		snap := p.latest
		// Decide whether 这 snapshot represents 终态 事件
		// against prior baseline. We must do 这 before resetting
		// dirty/flushing because shouldFlushNow looks 在 p.baseline
		// (which 是 what was 在 disk before 这 写入).
		var prev *flushBaseline
		if p.hasFlushed {
			prev = &p.baseline
		}
		isTerminal := shouldFlushNow(prev, &snap)
		p.flushing = true
		p.dirty = false
		s.mu.Unlock()

		err := s.write(ctx, snap)

		s.mu.Lock()
		p.flushing = false
		if err == nil {
			p.baseline = newFlushBaseline(&snap)
			p.hasFlushed = true
		} else {
			// Restore dirty 所以 next caller retries.
			p.dirty = true
		}
		// If delta arrived during SQL 写入 和 we 是 sync
		// caller, 用户 expects 那 delta 到 land too.
		wasDirty := p.dirty
		if !wasDirty {
			// Nothing left 到 写入, 所以 drop snapshot. Pending
			// entries outlive streams 那 created them; holding
			// latest here would pin every finished 消息's parts
			// 为 life 的 process.
			p.latest = Message{}
		}
		s.mu.Unlock()

		if err != nil {
			return err
		}

		// 终态 events — 消息 finished, 工具调用 added 或
		// finished, reasoning ended — use bounded must-投递
		// 路径 所以 they never get dropped under 渠道 contention.
		if isTerminal {
			s.PublishMustDeliver(ctx, pubsub.UpdatedEvent, snap)
		} else {
			s.Publish(pubsub.UpdatedEvent, snap)
		}

		if wasDirty && syncCaller {
			continue
		}
		return nil
	}
}

// 写入 performs unguarded SQL 写入 + UpdatedAt stamp. Caller
// 负责发布。
func (s *service) write(ctx context.Context, msg Message) error {
	parts, err := marshalParts(msg.Parts)
	if err != nil {
		return err
	}
	finishedAt := sql.NullInt64{}
	if f := msg.FinishPart(); f != nil {
		finishedAt.Int64 = f.Time
		finishedAt.Valid = true
	}
	if err := s.q.UpdateMessage(ctx, db.UpdateMessageParams{
		ID:                      msg.ID,
		Parts:                   string(parts),
		ModelName:               sql.NullString{String: msg.ModelName, Valid: msg.ModelName != ""},
		PrismModelID:            sql.NullString{String: msg.PrismModelID, Valid: msg.PrismModelID != ""},
		PrismModelName:          sql.NullString{String: msg.PrismModelName, Valid: msg.PrismModelName != ""},
		PrismHypercreditSavings: nullableFloat(msg.PrismHypercreditSavings),
		PrismDollarSavings:      nullableFloat(msg.PrismDollarSavings),
		FinishedAt:              finishedAt,
	}); err != nil {
		return err
	}
	return nil
}

func nullableFloat(v *float64) sql.NullFloat64 {
	if v == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *v, Valid: true}
}

func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	value := v.Float64
	return &value
}

// shouldFlushNow returns true 当 next represents structural
// change 那 must 不 是 silently coalesced: 消息 just
// finished, 工具-调用 set grew, 工具调用 transitioned 到
// finished, 或 reasoning just finished. prev 是 last-flushed
// snapshot (或 nil 如果 no 写入 has landed yet).
func shouldFlushNow(prev *flushBaseline, next *Message) bool {
	if next.IsFinished() {
		return true
	}

	var prevCalls []bool
	var prevReasoningFinishedAt int64
	if prev != nil {
		prevCalls = prev.toolCallsFinished
		prevReasoningFinishedAt = prev.reasoningFinishedAt
	}
	nextCalls := next.ToolCalls()
	if len(nextCalls) != len(prevCalls) {
		return true
	}
	for i := range nextCalls {
		// Bounds-safe: lengths 是 equal here.
		if nextCalls[i].Finished != prevCalls[i] {
			return true
		}
		// 一个 工具调用's input 只 matters once it has landed (Finished
		// flips true). Earlier deltas 到 Input 是 debounced 用
		// rest 的 streaming state.
	}
	if next.ReasoningContent().FinishedAt > 0 && prevReasoningFinishedAt == 0 {
		return true
	}
	return false
}

func (s *service) Get(ctx context.Context, id string) (Message, error) {
	dbMessage, err := s.q.GetMessage(ctx, id)
	if err != nil {
		return Message{}, err
	}
	return s.fromDBItem(dbMessage)
}

func (s *service) List(ctx context.Context, sessionID string) ([]Message, error) {
	dbMessages, err := s.q.ListMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) ListFromSummary(ctx context.Context, sessionID, summaryMessageID string) ([]Message, error) {
	if summaryMessageID == "" {
		return s.List(ctx, sessionID)
	}
	dbMessages, err := s.q.ListMessagesBySessionFromSummary(ctx, db.ListMessagesBySessionFromSummaryParams{
		SessionID: sessionID,
		ID:        summaryMessageID,
	})
	if err != nil {
		return nil, err
	}
	// No rows means 摘要 消息 是 gone; fall back 到 whole
	// 会话 rather than sending nothing.
	if len(dbMessages) == 0 {
		return s.List(ctx, sessionID)
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) ListUserMessages(ctx context.Context, sessionID string) ([]Message, error) {
	dbMessages, err := s.q.ListUserMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) ListAllUserMessages(ctx context.Context) ([]Message, error) {
	dbMessages, err := s.q.ListAllUserMessages(ctx)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) GetLastAssistantMessage(ctx context.Context, sessionID string) (Message, error) {
	dbMessage, err := s.q.GetLastAssistantMessageBySession(ctx, sessionID)
	if err != nil {
		return Message{}, err
	}
	return s.fromDBItem(dbMessage)
}

func (s *service) fromDBItem(item db.Message) (Message, error) {
	parts, err := unmarshalParts([]byte(item.Parts))
	if err != nil {
		return Message{}, err
	}
	return Message{
		ID:                      item.ID,
		SessionID:               item.SessionID,
		Role:                    MessageRole(item.Role),
		Parts:                   parts,
		Model:                   item.Model.String,
		ModelName:               item.ModelName.String,
		Provider:                item.Provider.String,
		CreatedAt:               item.CreatedAt,
		UpdatedAt:               item.UpdatedAt,
		IsSummaryMessage:        item.IsSummaryMessage != 0,
		PrismModelID:            item.PrismModelID.String,
		PrismModelName:          item.PrismModelName.String,
		PrismHypercreditSavings: floatPtr(item.PrismHypercreditSavings),
		PrismDollarSavings:      floatPtr(item.PrismDollarSavings),
	}, nil
}

type partType string

const (
	reasoningType      partType = "reasoning"
	textType           partType = "text"
	imageURLType       partType = "image_url"
	binaryType         partType = "binary"
	toolCallType       partType = "tool_call"
	toolResultType     partType = "tool_result"
	finishType         partType = "finish"
	shellCommandType   partType = "shell_command"
	subAgentReportType partType = "sub_agent_report"
)

type partWrapper struct {
	Type partType    `json:"type"`
	Data ContentPart `json:"data"`
}

func marshalParts(parts []ContentPart) ([]byte, error) {
	wrappedParts := make([]partWrapper, len(parts))

	for i, part := range parts {
		var typ partType

		switch part.(type) {
		case ReasoningContent:
			typ = reasoningType
		case TextContent:
			typ = textType
		case ImageURLContent:
			typ = imageURLType
		case BinaryContent:
			typ = binaryType
		case ToolCall:
			typ = toolCallType
		case ToolResult:
			typ = toolResultType
		case Finish:
			typ = finishType
		case ShellCommand:
			typ = shellCommandType
		case SubAgentReport:
			typ = subAgentReportType
		default:
			return nil, fmt.Errorf("unknown part type: %T", part)
		}

		wrappedParts[i] = partWrapper{
			Type: typ,
			Data: part,
		}
	}
	return json.Marshal(wrappedParts)
}

// rawPartWrapper 是 [partWrapper] 为 decoding: payload stays encoded
// until type tag says what 到 decode it into.
type rawPartWrapper struct {
	Type partType        `json:"type"`
	Data json.RawMessage `json:"data"`
}

func unmarshalParts(data []byte) ([]ContentPart, error) {
	// One pass gets every type tag 和 它的 still-encoded payload. Splitting
	// 先转成 []json.RawMessage 会对同一批字节遍历两次。
	var wrapped []rawPartWrapper
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, err
	}

	parts := make([]ContentPart, 0, len(wrapped))
	for _, w := range wrapped {
		part, err := unmarshalPart(w.Type, w.Data)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}

	return parts, nil
}

// unmarshalPart decodes single 片段's payload according 到 它的 type tag.
func unmarshalPart(typ partType, data json.RawMessage) (ContentPart, error) {
	switch typ {
	case reasoningType:
		return decodePart[ReasoningContent](data)
	case textType:
		return decodePart[TextContent](data)
	case imageURLType:
		return decodePart[ImageURLContent](data)
	case binaryType:
		return decodePart[BinaryContent](data)
	case toolCallType:
		return decodePart[ToolCall](data)
	case toolResultType:
		return decodePart[ToolResult](data)
	case finishType:
		return decodePart[Finish](data)
	case shellCommandType:
		return decodePart[ShellCommand](data)
	case subAgentReportType:
		return decodePart[SubAgentReport](data)
	default:
		return nil, fmt.Errorf("unknown part type: %s", typ)
	}
}

func decodePart[T ContentPart](data json.RawMessage) (ContentPart, error) {
	var part T
	if err := json.Unmarshal(data, &part); err != nil {
		return nil, err
	}
	return part, nil
}
