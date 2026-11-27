// Code generated 通过 sqlc. 不要 EDIT.
// versions:
//   sqlc v1.30.0

package db

import (
	"context"
)

type Querier interface {
	CreateFile(ctx context.Context, arg CreateFileParams) (File, error)
	CreateMessage(ctx context.Context, arg CreateMessageParams) (Message, error)
	CreateSession(ctx context.Context, arg CreateSessionParams) (Session, error)
	DeleteFile(ctx context.Context, id string) error
	DeleteMCPDisabledServer(ctx context.Context, name string) error
	DeleteMCPEnabledServer(ctx context.Context, name string) error
	DeleteMessage(ctx context.Context, id string) error
	DeleteSession(ctx context.Context, id string) error
	DeleteSessionFiles(ctx context.Context, sessionID string) error
	DeleteSessionMessages(ctx context.Context, sessionID string) error
	DeleteSkillsDisabled(ctx context.Context, name string) error
	DeleteSkillsEnabled(ctx context.Context, name string) error
	GetAverageResponseTime(ctx context.Context) (int64, error)
	GetFile(ctx context.Context, id string) (File, error)
	GetFileByPathAndSession(ctx context.Context, arg GetFileByPathAndSessionParams) (File, error)
	GetFileRead(ctx context.Context, arg GetFileReadParams) (ReadFile, error)
	GetHourDayHeatmap(ctx context.Context) ([]GetHourDayHeatmapRow, error)
	GetLastAssistantMessageBySession(ctx context.Context, sessionID string) (Message, error)
	GetLastSession(ctx context.Context) (Session, error)
	GetMessage(ctx context.Context, id string) (Message, error)
	GetRecentActivity(ctx context.Context) ([]GetRecentActivityRow, error)
	GetSessionByID(ctx context.Context, id string) (Session, error)
	GetToolUsage(ctx context.Context) ([]GetToolUsageRow, error)
	GetTotalStats(ctx context.Context) (GetTotalStatsRow, error)
	GetUsageByDay(ctx context.Context) ([]GetUsageByDayRow, error)
	GetUsageByDayOfWeek(ctx context.Context) ([]GetUsageByDayOfWeekRow, error)
	GetUsageByHour(ctx context.Context) ([]GetUsageByHourRow, error)
	GetUsageByModel(ctx context.Context) ([]GetUsageByModelRow, error)
	InsertMCPDisabledServer(ctx context.Context, name string) error
	InsertMCPEnabledServer(ctx context.Context, name string) error
	InsertSkillsDisabled(ctx context.Context, name string) error
	InsertSkillsEnabled(ctx context.Context, name string) error
	// Backs 提示词 history 当 no 会话 是 open. Needs
	// idx_messages_role_created_at 到 seek rather than scan 表.
	// Child sessions 是 excluded: 子代理's task 提示词 是 用户
	// 消息 too, 但 用户 never typed it, 所以 it has no place 在
	// 提示词 history.
	ListAllUserMessages(ctx context.Context) ([]Message, error)
	ListFilesByPath(ctx context.Context, path string) ([]File, error)
	ListFilesBySession(ctx context.Context, sessionID string) ([]File, error)
	ListLatestSessionFiles(ctx context.Context, sessionID string) ([]File, error)
	ListMCPDisabledServers(ctx context.Context) ([]string, error)
	ListMCPEnabledServers(ctx context.Context) ([]string, error)
	ListMessagesBySession(ctx context.Context, sessionID string) ([]Message, error)
	// Messages 从 摘要 onward, which 是 all compacted 会话 sends.
	// created_at has one-second resolution, 所以 few messages preceding
	// 摘要 can come back too; caller slices 从 摘要 通过 ID.
	ListMessagesBySessionFromSummary(ctx context.Context, arg ListMessagesBySessionFromSummaryParams) ([]Message, error)
	ListNewFiles(ctx context.Context) ([]File, error)
	ListSessionReadFiles(ctx context.Context, sessionID string) ([]ReadFile, error)
	ListSessions(ctx context.Context) ([]Session, error)
	ListSkillsDisabled(ctx context.Context) ([]string, error)
	ListSkillsEnabled(ctx context.Context) ([]string, error)
	// Backs 提示词 history, which steps back one 条目 在 time.
	ListUserMessagesBySession(ctx context.Context, sessionID string) ([]Message, error)
	RecordFileRead(ctx context.Context, arg RecordFileReadParams) error
	RenameSession(ctx context.Context, arg RenameSessionParams) error
	SetSessionChannel(ctx context.Context, arg SetSessionChannelParams) (Session, error)
	UpdateMessage(ctx context.Context, arg UpdateMessageParams) error
	// 渠道 是 deliberately 不 written here: SetSessionChannel owns
	// binding, 所以 load-modify-Save cycle 用 stale 会话 cannot
	// clobber 渠道 coordinator just set.
	UpdateSession(ctx context.Context, arg UpdateSessionParams) (Session, error)
	UpdateSessionTitleAndUsage(ctx context.Context, arg UpdateSessionTitleAndUsageParams) error
}

var _ Querier = (*Queries)(nil)
