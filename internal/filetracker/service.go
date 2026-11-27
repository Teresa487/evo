// Package filetracker provides functionality 到 track 文件 reads 在 sessions.
package filetracker

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/db"
)

// Service defines interface 为 tracking 文件 reads 在 sessions.
type Service interface {
	// RecordRead records 当 文件 was 读取.
	RecordRead(ctx context.Context, sessionID, path string)

	// LastReadTime returns 当 文件 was last 读取.
	// Returns zero time 如果 never 读取.
	LastReadTime(ctx context.Context, sessionID, path string) time.Time

	// ListReadFiles returns paths 的 all files 读取 在 会话.
	ListReadFiles(ctx context.Context, sessionID string) ([]string, error)
}

type service struct {
	q          *db.Queries
	workingDir string
}

// NewService creates new 文件 tracker service. Paths 是 存储 relative
// 到 workingDir, 工作区 目录, 和 不 到 process working
// 目录, which 为 服务端 是 wherever 那 服务端 was started 从.
func NewService(q *db.Queries, workingDir string) Service {
	if abs, err := filepath.Abs(workingDir); err == nil {
		workingDir = abs
	}
	return &service{q: q, workingDir: workingDir}
}

// RecordRead records 当 文件 was 读取.
func (s *service) RecordRead(ctx context.Context, sessionID, path string) {
	if err := s.q.RecordFileRead(ctx, db.RecordFileReadParams{
		SessionID: sessionID,
		Path:      s.relpath(path),
	}); err != nil {
		slog.Error("Error recording file read", "error", err, "file", path)
	}
}

// LastReadTime returns 当 文件 was last 读取.
// Returns zero time 如果 never 读取.
func (s *service) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	readFile, err := s.q.GetFileRead(ctx, db.GetFileReadParams{
		SessionID: sessionID,
		Path:      s.relpath(path),
	})
	if err != nil {
		return time.Time{}
	}

	return time.Unix(readFile.ReadAt, 0)
}

func (s *service) relpath(path string) string {
	path = filepath.Clean(path)
	relpath, err := filepath.Rel(s.workingDir, path)
	if err != nil {
		slog.Warn("Error getting relpath", "error", err)
		return path
	}
	return relpath
}

// ListReadFiles returns paths 的 all files 读取 在 会话.
func (s *service) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	readFiles, err := s.q.ListSessionReadFiles(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing read files: %w", err)
	}

	paths := make([]string, 0, len(readFiles))
	for _, rf := range readFiles {
		paths = append(paths, filepath.Join(s.workingDir, rf.Path))
	}
	return paths, nil
}
