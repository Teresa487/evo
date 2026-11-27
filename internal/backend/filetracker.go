// filetracker.go —— 记录会话读过的文件，供上下文与权限判断使用。

package backend

import (
	"context"
	"time"
)

// FileTrackerRecordRead records 文件 读取 为 会话.
func (b *Backend) FileTrackerRecordRead(ctx context.Context, workspaceID, sessionID, path string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	ws.FileTracker.RecordRead(ctx, sessionID, path)
	return nil
}

// FileTrackerLastReadTime returns last 读取 time 为 文件 在 会话.
func (b *Backend) FileTrackerLastReadTime(ctx context.Context, workspaceID, sessionID, path string) (time.Time, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return time.Time{}, err
	}

	return ws.FileTracker.LastReadTime(ctx, sessionID, path), nil
}

// FileTrackerListReadFiles returns list 的 读取 files 为 会话.
func (b *Backend) FileTrackerListReadFiles(ctx context.Context, workspaceID, sessionID string) ([]string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.FileTracker.ListReadFiles(ctx, sessionID)
}
