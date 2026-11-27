package shell

import (
	"context"
	"log/slog"
	"strings"

	"github.com/charmbracelet/crush/internal/message"
)

// PersistOutput stores bang-mode shell command 结果 作为 用户消息.
// Oversized output 是 truncated first, 用 full 文本 spilled 到 文件
// under spillDir, 所以 single noisy command cannot swamp 上下文 窗口
// 或 会话.
// If target 会话 no longer exists (deleted before 或 during
// command), persistence 是 skipped without surfacing 错误.
func PersistOutput(
	ctx context.Context,
	messages message.Service,
	sessionID, command, output string,
	exitCode int,
	spillDir string,
) error {
	if sessionID == "" {
		return nil
	}

	_, err := messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{message.ShellCommand{
			Command:  command,
			Output:   TruncateForPersist(output, spillDir),
			ExitCode: exitCode,
		}},
	})
	// messages 表 has single foreign key (session_id), 所以 FK
	// failure here can 只 mean 会话 是 gone. We match 在 错误
	// 文本 because codebase builds against two swappable SQLite drivers
	// (modernc 和 ncruces) 和 这 是 one signal stable across both.
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		slog.Debug(
			"Skipping shell command persistence: session no longer exists",
			"session_id", sessionID,
		)
		return nil
	}
	return err
}
