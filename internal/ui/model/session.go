package model

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
)

// loadSessionMsg 是 消息 indicating 那 会话 和 它的 files have
// been loaded.
type loadSessionMsg struct {
	session   *session.Session
	files     []SessionFile
	readFiles []string
	messages  []message.Message
}

// lspFilePaths returns deduplicated 文件 paths 从 both modified 和 读取
// files 为 starting LSP servers.
func (msg loadSessionMsg) lspFilePaths() []string {
	seen := make(map[string]struct{}, len(msg.files)+len(msg.readFiles))
	paths := make([]string, 0, len(msg.files)+len(msg.readFiles))
	for _, f := range msg.files {
		p := f.LatestVersion.Path
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	for _, p := range msg.readFiles {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	return paths
}

// SessionFile tracks first 和 latest versions 的 文件 在 会话,
// along 用 total additions 和 deletions.
type SessionFile struct {
	FirstVersion  history.File
	LatestVersion history.File
	Additions     int
	Deletions     int
}

// loadSession loads 会话 along 用 它的 associated files 和 computes
// diff statistics (additions 和 deletions) 为 each 文件 在 会话.
// 它 returns tea.Cmd 那, 当 executed, fetches 会话 data 和
// returns sessionFilesLoadedMsg containing processed 会话 files.
//
// returned batch also reports new current-会话 selection 到
// 工作区 所以 服务端 can 更新 它的 per-客户端 presence map.
// That 报告 是 fire-和-forget: errors 是 logged 在 debug 和
// UI never blocks 在 调用.
func (m *UI) loadSession(sessionID string) tea.Cmd {
	load := func() tea.Msg {
		session, err := m.com.Workspace.GetSession(context.Background(), sessionID)
		if err != nil {
			return util.ReportError(err)
		}

		sessionFiles, err := m.loadSessionFiles(sessionID)
		if err != nil {
			return util.ReportError(err)
		}

		readFiles, err := m.com.Workspace.FileTrackerListReadFiles(context.Background(), sessionID)
		if err != nil {
			slog.Error("Failed to load read files for session", "error", err)
		}

		// 读取 transcript here, 不 在 更新: long 会话 是 tens
		// 的 megabytes 和 would freeze 事件 loop while it loads.
		messages, err := m.com.Workspace.ListMessages(context.Background(), sessionID)
		if err != nil {
			return util.ReportError(err)
		}

		return loadSessionMsg{
			session:   &session,
			files:     sessionFiles,
			readFiles: readFiles,
			messages:  messages,
		}
	}
	return tea.Batch(load, m.refreshSkillOverrides(), m.reportCurrentSession(sessionID))
}

// reportCurrentSession returns fire-和-forget tea.Cmd 那
// informs 工作区 which 会话 这 客户端 是 currently
// viewing. In 客户端/服务端 mode 报告 scopes 事件 流:
// 权限 和 question prompts 是 只 delivered 到 clients
// viewing 会话 那 raised them. Errors 是 logged 在 debug
// 只; 调用 是 best-effort, 不 correctness-critical state.
func (m *UI) reportCurrentSession(sessionID string) tea.Cmd {
	return func() tea.Msg {
		if err := m.com.Workspace.SetCurrentSession(context.Background(), sessionID); err != nil {
			slog.Debug("Failed to report current session", "session_id", sessionID, "error", err)
		}
		return nil
	}
}

func (m *UI) loadSessionFiles(sessionID string) ([]SessionFile, error) {
	files, err := m.com.Workspace.ListSessionHistory(context.Background(), sessionID)
	if err != nil {
		return nil, err
	}

	filesByPath := make(map[string][]history.File)
	for _, f := range files {
		filesByPath[f.Path] = append(filesByPath[f.Path], f)
	}
	sessionFiles := make([]SessionFile, 0, len(filesByPath))
	for _, versions := range filesByPath {
		if len(versions) == 0 {
			continue
		}

		first := versions[0]
		last := versions[0]
		for _, v := range versions {
			if v.Version < first.Version {
				first = v
			}
			if v.Version > last.Version {
				last = v
			}
		}

		_, additions, deletions := diff.GenerateDiff(first.Content, last.Content, first.Path)

		sessionFiles = append(sessionFiles, SessionFile{
			FirstVersion:  first,
			LatestVersion: last,
			Additions:     additions,
			Deletions:     deletions,
		})
	}

	slices.SortFunc(sessionFiles, func(a, b SessionFile) int {
		if a.LatestVersion.UpdatedAt > b.LatestVersion.UpdatedAt {
			return -1
		}
		if a.LatestVersion.UpdatedAt < b.LatestVersion.UpdatedAt {
			return 1
		}
		return 0
	})
	return sessionFiles, nil
}

// handleFileEvent processes 文件 change events 和 updates 会话 文件
// list 用 new 或 updated 文件 information.
func (m *UI) handleFileEvent(file history.File) tea.Cmd {
	if m.session == nil || file.SessionID != m.session.ID {
		return nil
	}

	return func() tea.Msg {
		sessionFiles, err := m.loadSessionFiles(m.session.ID)
		// could 不 load 会话 files
		if err != nil {
			return util.NewErrorMsg(err)
		}

		return sessionFilesUpdatesMsg{
			sessionFiles: sessionFiles,
		}
	}
}

// filesInfo renders modified files section 为 sidebar, showing files
// 用 their addition/deletion counts.
func (m *UI) filesInfo(cwd string, width, maxItems int, isSection bool) string {
	t := m.com.Styles

	title := t.Files.SectionTitle.Render("Modified Files")
	if isSection {
		title = common.Section(t, "Modified Files", width)
	}
	list := t.Files.EmptyMessage.Render("None")
	var filesWithChanges []SessionFile
	for _, f := range m.sessionFiles {
		if f.Additions == 0 && f.Deletions == 0 {
			continue
		}
		filesWithChanges = append(filesWithChanges, f)
	}
	if len(filesWithChanges) > 0 {
		list = fileList(t, cwd, filesWithChanges, width, maxItems)
	}

	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
}

// fileList renders list 的 files 用 their diff statistics, truncating 到
// maxItems 和 showing "...和 N more" 消息 如果 needed.
func fileList(t *styles.Styles, cwd string, filesWithChanges []SessionFile, width, maxItems int) string {
	var renderedFiles []string

	for _, f := range filesWithChanges {
		// Build stats string 用 colors
		var statusParts []string
		if f.Additions > 0 {
			statusParts = append(statusParts, t.Files.Additions.Render(fmt.Sprintf("+%d", f.Additions)))
		}
		if f.Deletions > 0 {
			statusParts = append(statusParts, t.Files.Deletions.Render(fmt.Sprintf("-%d", f.Deletions)))
		}
		extraContent := strings.Join(statusParts, " ")

		// Format 文件 路径
		filePath := f.FirstVersion.Path
		if rel, err := filepath.Rel(cwd, filePath); err == nil {
			filePath = rel
		}
		filePath = fsext.DirTrim(filePath, 2)
		suffix := ""
		if extraContent != "" {
			suffix = " " + extraContent
		}
		maxPathWidth := max(width-lipgloss.Width(suffix), 0)
		filePath = ansi.Truncate(filePath, maxPathWidth, "…")

		line := t.Files.Path.Render(filePath)
		if extraContent != "" {
			line = fmt.Sprintf("%s %s", line, extraContent)
		}

		renderedFiles = append(renderedFiles, line)
	}

	return lipgloss.JoinVertical(lipgloss.Left, truncateRows(renderedFiles, maxItems, t.Files.TruncationHint)...)
}

// startLSPs starts LSP servers 为 given 文件 paths.
func (m *UI) startLSPs(paths []string) tea.Cmd {
	if len(paths) == 0 {
		return nil
	}

	return func() tea.Msg {
		ctx := context.Background()
		for _, path := range paths {
			m.com.Workspace.LSPStart(ctx, path)
		}
		return nil
	}
}
