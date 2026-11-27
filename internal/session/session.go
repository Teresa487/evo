package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
	"github.com/zeebo/xxh3"
)

type TodoStatus string

const (
	TodoStatusPending    TodoStatus = "pending"
	TodoStatusInProgress TodoStatus = "in_progress"
	TodoStatusCompleted  TodoStatus = "completed"
)

// HashID returns XXH3 hash 的 会话 ID (UUID) 作为 hex string.
func HashID(id string) string {
	h := xxh3.New()
	h.WriteString(id)
	return fmt.Sprintf("%x", h.Sum(nil))
}

type Todo struct {
	Content    string     `json:"content"`
	Status     TodoStatus `json:"status"`
	ActiveForm string     `json:"active_form"`
}

// HasIncompleteTodos returns true 如果 there 是 any non-completed todos.
func HasIncompleteTodos(todos []Todo) bool {
	for _, todo := range todos {
		if todo.Status != TodoStatusCompleted {
			return true
		}
	}
	return false
}

type Session struct {
	ID               string
	ParentSessionID  string
	Title            string
	MessageCount     int64
	PromptTokens     int64
	CompletionTokens int64
	EstimatedUsage   bool
	SummaryMessageID string
	Cost             float64
	Todos            []Todo
	Channel          string
	CreatedAt        int64
	UpdatedAt        int64
}

type Service interface {
	pubsub.Subscriber[Session]
	Create(ctx context.Context, title string) (Session, error)
	CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error)
	CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error)
	Get(ctx context.Context, id string) (Session, error)
	GetLast(ctx context.Context) (Session, error)
	List(ctx context.Context) ([]Session, error)
	Save(ctx context.Context, session Session) (Session, error)
	SetChannel(ctx context.Context, sessionID, channel string) (Session, error)
	UpdateTitleAndUsage(ctx context.Context, sessionID, title string, promptTokens, completionTokens int64, cost float64) error
	Rename(ctx context.Context, id string, title string) error
	Delete(ctx context.Context, id string) error
	MCPDisabledServers(ctx context.Context) ([]string, error)
	SetMCPServerDisabled(ctx context.Context, name string, disabled bool) error
	MCPServersEnabled(ctx context.Context) ([]string, error)
	SkillsDisabled(ctx context.Context) ([]string, error)
	SetSkillDisabled(ctx context.Context, name string, disabled, configDisabled bool) error
	SkillsEnabled(ctx context.Context) ([]string, error)

	// Agent 工具 会话 management
	CreateAgentToolSessionID(messageID, toolCallID string) string
	ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool)
	IsAgentToolSession(sessionID string) bool
}

type service struct {
	*pubsub.Broker[Session]
	db *sql.DB
	q  *db.Queries

	// Estimated usage stays 在 memory 所以 fetch-modify-save paths (e.g.,
	// updating todos 或 parent-会话 cost) do 不 rebuild 会话 从
	// SQLite 和 incorrectly clear UI "~" marker.
	estimatedUsageMu sync.RWMutex
	estimatedUsage   map[string]bool
}

func (s *service) Create(ctx context.Context, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:    uuid.New().String(),
		Title: title,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	event.SessionCreated()
	return session, nil
}

func (s *service) CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              toolCallID,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           title,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              "title-" + parentSessionID,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           "Generate a title",
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := s.q.WithTx(tx)

	dbSession, err := qtx.GetSessionByID(ctx, id)
	if err != nil {
		return err
	}
	if err = qtx.DeleteSessionMessages(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session messages: %w", err)
	}
	if err = qtx.DeleteSessionFiles(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session files: %w", err)
	}
	if err = qtx.DeleteSession(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	session := s.fromDBItem(dbSession)
	s.clearEstimatedUsageState(dbSession.ID)
	s.Publish(pubsub.DeletedEvent, session)
	event.SessionDeleted()
	return nil
}

func (s *service) Get(ctx context.Context, id string) (Session, error) {
	dbSession, err := s.q.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.applyEstimatedUsageState(&session)
	return session, nil
}

func (s *service) GetLast(ctx context.Context) (Session, error) {
	dbSession, err := s.q.GetLastSession(ctx)
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.applyEstimatedUsageState(&session)
	return session, nil
}

func (s *service) Save(ctx context.Context, session Session) (Session, error) {
	todosJSON, err := marshalTodos(session.Todos)
	if err != nil {
		return Session{}, err
	}

	dbSession, err := s.q.UpdateSession(ctx, db.UpdateSessionParams{
		ID:               session.ID,
		Title:            session.Title,
		PromptTokens:     session.PromptTokens,
		CompletionTokens: session.CompletionTokens,
		SummaryMessageID: sql.NullString{
			String: session.SummaryMessageID,
			Valid:  session.SummaryMessageID != "",
		},
		Cost: session.Cost,
		Todos: sql.NullString{
			String: todosJSON,
			Valid:  todosJSON != "",
		},
	})
	if err != nil {
		return Session{}, err
	}
	estimatedUsage := session.EstimatedUsage
	s.setEstimatedUsageState(session.ID, estimatedUsage)
	session = s.fromDBItem(dbSession)
	session.EstimatedUsage = estimatedUsage
	s.Publish(pubsub.UpdatedEvent, session)
	return session, nil
}

func (s *service) SetChannel(ctx context.Context, sessionID, channel string) (Session, error) {
	dbSession, err := s.q.SetSessionChannel(ctx, db.SetSessionChannelParams{
		ID: sessionID,
		Channel: sql.NullString{
			String: channel,
			Valid:  channel != "",
		},
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.applyEstimatedUsageState(&session)
	s.Publish(pubsub.UpdatedEvent, session)
	return session, nil
}

// UpdateTitleAndUsage updates 只 标题 和 usage fields atomically.
// 这 是 safer than fetching, modifying, 和 saving entire 会话.
func (s *service) UpdateTitleAndUsage(ctx context.Context, sessionID, title string, promptTokens, completionTokens int64, cost float64) error {
	if err := s.q.UpdateSessionTitleAndUsage(ctx, db.UpdateSessionTitleAndUsageParams{
		ID:               sessionID,
		Title:            title,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		Cost:             cost,
	}); err != nil {
		return err
	}
	s.publishSessionUpdate(ctx, sessionID)
	return nil
}

// Rename updates 只 标题 的 会话 without touching updated_at 或
// 用量字段。
func (s *service) Rename(ctx context.Context, id string, title string) error {
	if err := s.q.RenameSession(ctx, db.RenameSessionParams{
		ID:    id,
		Title: title,
	}); err != nil {
		return err
	}
	s.publishSessionUpdate(ctx, id)
	return nil
}

func (s *service) List(ctx context.Context) ([]Session, error) {
	dbSessions, err := s.q.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(dbSessions))
	for i, dbSession := range dbSessions {
		sessions[i] = s.fromDBItem(dbSession)
		s.applyEstimatedUsageState(&sessions[i])
	}
	return sessions, nil
}

// publishSessionUpdate re-fetches 会话 和 publishes UpdatedEvent 所以
// 那 UI subscribers reflect 标题 或 usage changes.
func (s *service) publishSessionUpdate(ctx context.Context, sessionID string) {
	session, err := s.Get(ctx, sessionID)
	if err != nil {
		slog.Error("Failed to re-fetch session for event publish", "error", err, "sessionID", sessionID)
		return
	}
	s.Publish(pubsub.UpdatedEvent, session)
}

func (s *service) applyEstimatedUsageState(session *Session) {
	s.estimatedUsageMu.RLock()
	session.EstimatedUsage = s.estimatedUsage[session.ID]
	s.estimatedUsageMu.RUnlock()
}

func (s *service) setEstimatedUsageState(sessionID string, estimatedUsage bool) {
	s.estimatedUsageMu.Lock()
	defer s.estimatedUsageMu.Unlock()
	if estimatedUsage {
		s.estimatedUsage[sessionID] = true
		return
	}
	delete(s.estimatedUsage, sessionID)
}

func (s *service) clearEstimatedUsageState(sessionID string) {
	s.estimatedUsageMu.Lock()
	delete(s.estimatedUsage, sessionID)
	s.estimatedUsageMu.Unlock()
}

func (s *service) fromDBItem(item db.Session) Session {
	todos, err := unmarshalTodos(item.Todos.String)
	if err != nil {
		slog.Error("Failed to unmarshal todos", "session_id", item.ID, "error", err)
	}
	return Session{
		ID:               item.ID,
		ParentSessionID:  item.ParentSessionID.String,
		Title:            item.Title,
		MessageCount:     item.MessageCount,
		PromptTokens:     item.PromptTokens,
		CompletionTokens: item.CompletionTokens,
		SummaryMessageID: item.SummaryMessageID.String,
		Cost:             item.Cost,
		Todos:            todos,
		Channel:          item.Channel.String,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}

func marshalTodos(todos []Todo) (string, error) {
	if len(todos) == 0 {
		return "", nil
	}
	data, err := json.Marshal(todos)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalTodos(data string) ([]Todo, error) {
	if data == "" {
		return []Todo{}, nil
	}
	var todos []Todo
	if err := json.Unmarshal([]byte(data), &todos); err != nil {
		return []Todo{}, err
	}
	return todos, nil
}

// MCPDisabledServers returns MCP servers disabled 为 这
// repository. 数据库 是 scoped 到 project, 所以 override set
// 是 shared 通过 every 会话 在 repository, including 子代理
// sessions. New repositories start empty 和 follow 配置.
func (s *service) MCPDisabledServers(ctx context.Context) ([]string, error) {
	return s.q.ListMCPDisabledServers(ctx)
}

// SetMCPServerDisabled adds 或 removes repository-scoped MCP override.
// Enabling 配置-disabled 服务端 also records enabled override 所以
// runtime start survives restart; override 是 removed 当 服务端
// 是 disabled again.
func (s *service) SetMCPServerDisabled(ctx context.Context, name string, disabled bool) error {
	var err error
	if disabled {
		err = s.q.InsertMCPDisabledServer(ctx, name)
	} else {
		err = s.q.DeleteMCPDisabledServer(ctx, name)
	}
	if err != nil {
		return err
	}
	if disabled {
		return s.q.DeleteMCPEnabledServer(ctx, name)
	}
	return s.q.InsertMCPEnabledServer(ctx, name)
}

// MCPServersEnabled returns MCP servers 用 repository-scoped
// enabled override: 配置-disabled servers 用户 enabled 为 这
// repository. Startup force-starts them 所以 override survives restarts.
func (s *service) MCPServersEnabled(ctx context.Context) ([]string, error) {
	return s.q.ListMCPEnabledServers(ctx)
}

// HasMCPOverride reports whether named MCP 服务端 has
// repository-scoped override, which takes precedence over 配置.
func HasMCPOverride(ctx context.Context, s Service, name string) (bool, error) {
	disabled, err := s.MCPDisabledServers(ctx)
	if err != nil {
		return false, err
	}
	enabled, err := s.MCPServersEnabled(ctx)
	if err != nil {
		return false, err
	}
	return slices.Contains(disabled, name) || slices.Contains(enabled, name), nil
}

// SkillsDisabled returns skills disabled 为 这 repository.
// Same semantics 作为 MCPDisabledServers: project-scoped, shared 通过 every
// 会话 在 repository.
func (s *service) SkillsDisabled(ctx context.Context) ([]string, error) {
	return s.q.ListSkillsDisabled(ctx)
}

// SetSkillDisabled adds 或 removes repository-scoped skill override.
// Enabling 配置-disabled skill records enabled override 所以 it
// stays enabled across restarts. Enabling skill 配置 does 不
// disable records none, 所以 later 配置 disable still applies here.
func (s *service) SetSkillDisabled(ctx context.Context, name string, disabled, configDisabled bool) error {
	var err error
	if disabled {
		err = s.q.InsertSkillsDisabled(ctx, name)
	} else {
		err = s.q.DeleteSkillsDisabled(ctx, name)
	}
	if err != nil {
		return err
	}
	if !disabled && configDisabled {
		return s.q.InsertSkillsEnabled(ctx, name)
	}
	return s.q.DeleteSkillsEnabled(ctx, name)
}

// SkillsEnabled returns skills 用 repository-scoped enabled
// override: 配置-disabled skills 用户 enabled 为 这 repository.
func (s *service) SkillsEnabled(ctx context.Context) ([]string, error) {
	return s.q.ListSkillsEnabled(ctx)
}

func NewService(q *db.Queries, conn *sql.DB) Service {
	broker := pubsub.NewBroker[Session]()
	return &service{
		Broker:         broker,
		db:             conn,
		q:              q,
		estimatedUsage: make(map[string]bool),
	}
}

// CreateAgentToolSessionID creates 会话 ID 为 agent 工具 sessions using format "messageID$$toolCallID"
func (s *service) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return fmt.Sprintf("%s$$%s", messageID, toolCallID)
}

// ParseAgentToolSessionID parses agent 工具 会话 ID into 它的 components
func (s *service) ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool) {
	parts := strings.Split(sessionID, "$$")
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// IsAgentToolSession checks 如果 会话 ID follows agent 工具 会话 format
func (s *service) IsAgentToolSession(sessionID string) bool {
	_, _, ok := s.ParseAgentToolSessionID(sessionID)
	return ok
}
