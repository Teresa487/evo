package lsp

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/charmbracelet/crush/internal/lsp/util"
	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

// HandleWorkspaceConfiguration handles 工作区 configuration requests
func HandleWorkspaceConfiguration(_ context.Context, _ string, params json.RawMessage) (any, error) {
	return []map[string]any{{}}, nil
}

// HandleWorkDoneProgressCreate handles 服务端-initiated 窗口/workDoneProgress/创建
// requests. 客户端 advertises 窗口.workDoneProgress: true 在 它的 capabilities
// (see makeClientCapabilities 在 powernap), which per LSP spec grants servers
// 权限 到 send 这 请求 — 所以 it must 是 answered, even 作为 no-op, 或
// 服务端 (e.g. typescript-language-服务端) treats unhandled 响应 作为 fatal 和
// crashes. See github.com/charmbracelet/x issue tracking powernap 能力 gaps.
func HandleWorkDoneProgressCreate(_ context.Context, _ string, _ json.RawMessage) (any, error) {
	return nil, nil
}

// HandleRegisterCapability handles 能力 registration requests
func HandleRegisterCapability(_ context.Context, _ string, params json.RawMessage) (any, error) {
	var registerParams protocol.RegistrationParams
	if err := json.Unmarshal(params, &registerParams); err != nil {
		slog.Error("Error unmarshaling registration params", "error", err)
		return nil, err
	}

	for _, reg := range registerParams.Registrations {
		switch reg.Method {
		case "workspace/didChangeWatchedFiles":
			// Parse the registration options
			optionsJSON, err := json.Marshal(reg.RegisterOptions)
			if err != nil {
				slog.Error("Error marshaling registration options", "error", err)
				continue
			}
			var options protocol.DidChangeWatchedFilesRegistrationOptions
			if err := json.Unmarshal(optionsJSON, &options); err != nil {
				slog.Error("Error unmarshaling registration options", "error", err)
				continue
			}
			// 存储 文件 watchers registrations
			notifyFileWatchRegistration(reg.ID, options.Watchers)
		}
	}
	return nil, nil
}

// HandleApplyEdit handles 工作区 edit requests
func HandleApplyEdit(encoding powernap.OffsetEncoding) func(_ context.Context, _ string, params json.RawMessage) (any, error) {
	return func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var edit protocol.ApplyWorkspaceEditParams
		if err := json.Unmarshal(params, &edit); err != nil {
			return nil, err
		}

		err := util.ApplyWorkspaceEdit(edit.Edit, encoding)
		if err != nil {
			slog.Error("Error applying workspace edit", "error", err)
			return protocol.ApplyWorkspaceEditResult{Applied: false, FailureReason: err.Error()}, nil
		}

		return protocol.ApplyWorkspaceEditResult{Applied: true}, nil
	}
}

// FileWatchRegistrationHandler 是 function 那 will 是 called 当 文件 watch registrations 是 received
type FileWatchRegistrationHandler func(id string, watchers []protocol.FileSystemWatcher)

// fileWatchHandler holds current 处理器 为 文件 watch registrations
var fileWatchHandler FileWatchRegistrationHandler

// RegisterFileWatchHandler sets 处理器 为 文件 watch registrations
func RegisterFileWatchHandler(handler FileWatchRegistrationHandler) {
	fileWatchHandler = handler
}

// notifyFileWatchRegistration notifies 处理器 about new 文件 watch registrations
func notifyFileWatchRegistration(id string, watchers []protocol.FileSystemWatcher) {
	if fileWatchHandler != nil {
		fileWatchHandler(id, watchers)
	}
}

// HandleServerMessage handles 服务端 messages
func HandleServerMessage(_ context.Context, method string, params json.RawMessage) {
	var msg protocol.ShowMessageParams
	if err := json.Unmarshal(params, &msg); err != nil {
		slog.Debug("Error unmarshal server message", "error", err)
		return
	}

	switch msg.Type {
	case protocol.Error:
		slog.Error("LSP Server", "message", msg.Message)
	case protocol.Warning:
		slog.Warn("LSP Server", "message", msg.Message)
	case protocol.Info:
		slog.Info("LSP Server", "message", msg.Message)
	case protocol.Log:
		slog.Debug("LSP Server", "message", msg.Message)
	}
}

// HandleDiagnostics handles diagnostic notifications 从 LSP 服务端
func HandleDiagnostics(client *Client, params json.RawMessage) {
	var diagParams protocol.PublishDiagnosticsParams
	if err := json.Unmarshal(params, &diagParams); err != nil {
		slog.Error("Error unmarshaling diagnostics params", "error", err)
		return
	}

	client.diagnostics.Set(diagParams.URI, diagParams.Diagnostics)

	// Calculate total diagnostic count
	totalCount := 0
	for _, diagnostics := range client.diagnostics.Seq2() {
		totalCount += len(diagnostics)
	}

	// Trigger callback 如果 set
	if client.onDiagnosticsChanged != nil {
		client.onDiagnosticsChanged(client.name, totalCount)
	}
}
