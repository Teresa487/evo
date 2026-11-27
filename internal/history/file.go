package history

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

const (
	InitialVersion = 0
)

type File struct {
	ID        string
	SessionID string
	Path      string
	Content   string
	Version   int64
	CreatedAt int64
	UpdatedAt int64
}

// Service manages 文件 versions 和 history 为 sessions.
type Service interface {
	pubsub.Subscriber[File]
	Create(ctx context.Context, sessionID, path, content string) (File, error)

	// CreateVersion creates new version 的 文件.
	CreateVersion(ctx context.Context, sessionID, path, content string) (File, error)

	Get(ctx context.Context, id string) (File, error)
	GetByPathAndSession(ctx context.Context, path, sessionID string) (File, error)
	ListBySession(ctx context.Context, sessionID string) ([]File, error)
	ListLatestSessionFiles(ctx context.Context, sessionID string) ([]File, error)
	Delete(ctx context.Context, id string) error
	DeleteSessionFiles(ctx context.Context, sessionID string) error
}

type service struct {
	*pubsub.Broker[File]
	db *sql.DB
	q  *db.Queries
}

func NewService(q *db.Queries, db *sql.DB) Service {
	return &service{
		Broker: pubsub.NewBroker[File](),
		q:      q,
		db:     db,
	}
}

func (s *service) Create(ctx context.Context, sessionID, path, content string) (File, error) {
	return s.createWithVersion(ctx, sessionID, path, content, InitialVersion)
}

// CreateVersion creates new version 的 文件 用 auto-incremented version
// number. If no previous versions exist 为 路径, it creates initial
// version. provided content 是 存储 作为 new version.
func (s *service) CreateVersion(ctx context.Context, sessionID, path, content string) (File, error) {
	// Get latest version 为 这 路径
	files, err := s.q.ListFilesByPath(ctx, path)
	if err != nil {
		return File{}, err
	}

	if len(files) == 0 {
		// No previous versions, 创建 initial
		return s.Create(ctx, sessionID, path, content)
	}

	// Get the latest version
	latestFile := files[0] // Files are ordered by version DESC, created_at DESC
	nextVersion := latestFile.Version + 1

	return s.createWithVersion(ctx, sessionID, path, content, nextVersion)
}

func (s *service) createWithVersion(ctx context.Context, sessionID, path, content string, version int64) (File, error) {
	// Maximum number 的 retries 为 事务 conflicts
	const maxRetries = 3
	var file File
	var err error

	// 重试 loop 为 事务 conflicts
	for attempt := range maxRetries {
		// Start 事务
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return File{}, fmt.Errorf("failed to begin transaction: %w", txErr)
		}

		// 创建 new queries instance 用 事务
		qtx := s.q.WithTx(tx)

		// Try 到 创建 文件 within 事务
		dbFile, txErr := qtx.CreateFile(ctx, db.CreateFileParams{
			ID:        uuid.New().String(),
			SessionID: sessionID,
			Path:      path,
			Content:   content,
			Version:   version,
		})
		if txErr != nil {
			// Rollback 事务
			tx.Rollback()

			// Check 如果 这 是 uniqueness constraint violation
			if strings.Contains(txErr.Error(), "UNIQUE constraint failed") {
				if attempt < maxRetries-1 {
					// If we have retries left, increment version 和 try again
					version++
					continue
				}
			}
			return File{}, txErr
		}

		// Commit 事务
		if txErr = tx.Commit(); txErr != nil {
			return File{}, fmt.Errorf("failed to commit transaction: %w", txErr)
		}

		file = s.fromDBItem(dbFile)
		s.Publish(pubsub.CreatedEvent, file)
		return file, nil
	}

	return file, err
}

func (s *service) Get(ctx context.Context, id string) (File, error) {
	dbFile, err := s.q.GetFile(ctx, id)
	if err != nil {
		return File{}, err
	}
	return s.fromDBItem(dbFile), nil
}

func (s *service) GetByPathAndSession(ctx context.Context, path, sessionID string) (File, error) {
	dbFile, err := s.q.GetFileByPathAndSession(ctx, db.GetFileByPathAndSessionParams{
		Path:      path,
		SessionID: sessionID,
	})
	if err != nil {
		return File{}, err
	}
	return s.fromDBItem(dbFile), nil
}

func (s *service) ListBySession(ctx context.Context, sessionID string) ([]File, error) {
	dbFiles, err := s.q.ListFilesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	files := make([]File, len(dbFiles))
	for i, dbFile := range dbFiles {
		files[i] = s.fromDBItem(dbFile)
	}
	return files, nil
}

func (s *service) ListLatestSessionFiles(ctx context.Context, sessionID string) ([]File, error) {
	dbFiles, err := s.q.ListLatestSessionFiles(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	files := make([]File, len(dbFiles))
	for i, dbFile := range dbFiles {
		files[i] = s.fromDBItem(dbFile)
	}
	return files, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	file, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.q.DeleteFile(ctx, id)
	if err != nil {
		return err
	}
	s.Publish(pubsub.DeletedEvent, file)
	return nil
}

func (s *service) DeleteSessionFiles(ctx context.Context, sessionID string) error {
	files, err := s.ListBySession(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, file := range files {
		err = s.Delete(ctx, file.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *service) fromDBItem(item db.File) File {
	return File{
		ID:        item.ID,
		SessionID: item.SessionID,
		Path:      item.Path,
		Content:   item.Content,
		Version:   item.Version,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}
