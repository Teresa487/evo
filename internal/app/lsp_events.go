package app

import (
	"context"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// LSPEventType represents type 的 LSP 事件
type LSPEventType string

const (
	LSPEventStateChanged       LSPEventType = "state_changed"
	LSPEventDiagnosticsChanged LSPEventType = "diagnostics_changed"
)

// LSPEvent represents 事件 在 LSP system
type LSPEvent struct {
	Type            LSPEventType
	Name            string
	State           lsp.ServerState
	Error           error
	DiagnosticCount int
}

// LSPClientInfo holds information about LSP 客户端's state
type LSPClientInfo struct {
	Name            string
	State           lsp.ServerState
	Error           error
	Client          *lsp.Client
	DiagnosticCount int
	ConnectedAt     time.Time
}

var (
	lspStates = csync.NewMap[string, LSPClientInfo]()
	lspBroker = pubsub.NewBroker[LSPEvent]()
)

// SubscribeLSPEvents returns 渠道 为 LSP events
func SubscribeLSPEvents(ctx context.Context) <-chan pubsub.Event[LSPEvent] {
	return lspBroker.Subscribe(ctx)
}

// GetLSPStates returns current state 的 all LSP clients
func GetLSPStates() map[string]LSPClientInfo {
	return lspStates.Copy()
}

// GetLSPState returns state 的 specific LSP 客户端
func GetLSPState(name string) (LSPClientInfo, bool) {
	return lspStates.Get(name)
}

// updateLSPState updates state 的 LSP 客户端 和 publishes 事件
func updateLSPState(name string, state lsp.ServerState, err error, client *lsp.Client, diagnosticCount int) {
	info := LSPClientInfo{
		Name:            name,
		State:           state,
		Error:           err,
		Client:          client,
		DiagnosticCount: diagnosticCount,
	}
	if state == lsp.StateReady {
		info.ConnectedAt = time.Now()
	} else if existing, ok := lspStates.Get(name); ok {
		info.ConnectedAt = existing.ConnectedAt
	}
	lspStates.Set(name, info)

	// 发布 state change 事件
	lspBroker.Publish(pubsub.UpdatedEvent, LSPEvent{
		Type:            LSPEventStateChanged,
		Name:            name,
		State:           state,
		Error:           err,
		DiagnosticCount: diagnosticCount,
	})
}

// updateLSPDiagnostics updates diagnostic count 为 LSP 客户端 和 publishes 事件
func updateLSPDiagnostics(name string, diagnosticCount int) {
	if info, exists := lspStates.Get(name); exists {
		info.DiagnosticCount = diagnosticCount
		lspStates.Set(name, info)

		// 发布 diagnostics change 事件
		lspBroker.Publish(pubsub.UpdatedEvent, LSPEvent{
			Type:            LSPEventDiagnosticsChanged,
			Name:            name,
			State:           info.State,
			Error:           info.Error,
			DiagnosticCount: diagnosticCount,
		})
	}
}
