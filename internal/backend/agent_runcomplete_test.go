package backend

import (
	"context"
	"errors"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// errorCoordinator 是 minimal agent.Coordinator whose RunAccepted
// returns configurable 错误. When markPublished 是 true it stamps
// 运行-完成 marker 在 上下文 before returning, simulating
// real coordinator 那 already published 运行's authoritative
// 终态 RunComplete (所以 runAgent must 不 emit duplicate fallback).
type errorCoordinator struct {
	err           error
	markPublished bool
}

func (c *errorCoordinator) Run(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	return nil, c.err
}

func (c *errorCoordinator) RunAccepted(ctx context.Context, accept *agent.AcceptedRun, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	if c.markPublished {
		agent.MarkRunCompletePublished(ctx)
	}
	return nil, c.err
}

func (c *errorCoordinator) BeginAccepted(sessionID string) *agent.AcceptedRun { return nil }
func (c *errorCoordinator) Cancel(string)                                     {}
func (c *errorCoordinator) CancelAll()                                        {}
func (c *errorCoordinator) IsBusy() bool                                      { return false }
func (c *errorCoordinator) IsSessionBusy(string) bool                         { return false }
func (c *errorCoordinator) QueuedPrompts(string) int                          { return 0 }
func (c *errorCoordinator) QueuedPromptsList(string) []string                 { return nil }
func (c *errorCoordinator) ClearQueue(string)                                 {}
func (c *errorCoordinator) AddNote(string, string)                            {}
func (c *errorCoordinator) Summarize(context.Context, string) error           { return nil }
func (c *errorCoordinator) Model() agent.Model                                { return agent.Model{} }
func (c *errorCoordinator) UpdateModels(context.Context) error                { return nil }
func (c *errorCoordinator) SetMainAgent(string) error                         { return nil }
func (c *errorCoordinator) MainAgent() string                                 { return "" }
func (c *errorCoordinator) GenerateTitle(context.Context, string, string)     {}
func (c *errorCoordinator) SetHostContext(proto.HostContext)                  {}
func (c *errorCoordinator) HostContext() proto.HostContext                    { return proto.HostContext{} }

// insertRunCompleteWorkspace installs 工作区 backed 通过 real
// app.App (所以 runCompletions 总线 exists) 用 given
// coordinator 和 工作区 运行 上下文 derived 从 base.
func insertRunCompleteWorkspace(t *testing.T, b *Backend, base context.Context, coord agent.Coordinator) *Workspace {
	t.Helper()
	a := app.NewForTest(base)
	a.AgentCoordinator = coord
	t.Cleanup(a.ShutdownForTest)
	ws := &Workspace{
		ID:           uuid.New().String(),
		Path:         t.TempDir(),
		resolvedPath: t.TempDir(),
		clients:      make(map[string]*clientState),
		shutdownFn:   func() {},
	}
	ws.App = a
	ws.ctx, ws.cancel = context.WithCancel(base)
	b.mu.Lock()
	b.workspaces.Set(ws.ID, ws)
	b.pathIndex[ws.resolvedPath] = ws.ID
	b.mu.Unlock()
	return ws
}

// TestRunAgent_PreRunErrorPublishesTerminalRunComplete proves 那
// 错误 returned 从 RunAccepted before coordinator could 发布
// 它的 own 终态 事件 (e.g. readyWg 或 UpdateModels failure,
// modeled here 通过 stub coordinator) still yields reliable 终态
// RunComplete 为 运行's RunID. Without it, `crush 运行` caller
// blocking 在 那 RunID would hang because lossy TypeAgentError
// 事件 是 不 guaranteed 终态 signal.
func TestRunAgent_PreRunErrorPublishesTerminalRunComplete(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend(t)
	runErr := errors.New("update models failed")
	ws := insertRunCompleteWorkspace(t, b, context.Background(), &errorCoordinator{err: runErr})

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := ws.RunCompletions().Subscribe(subCtx)

	err := b.SendMessage(ws.ID, proto.AgentMessage{SessionID: "S1", RunID: "run-1", Prompt: "hi"})
	require.NoError(t, err)

	select {
	case ev := <-ch:
		require.Equal(t, "run-1", ev.Payload.RunID,
			"the terminal RunComplete must carry the dispatched RunID")
		require.Equal(t, "S1", ev.Payload.SessionID)
		require.Equal(t, runErr.Error(), ev.Payload.Error,
			"the fallback terminal event must be marked errored")
		require.False(t, ev.Payload.Cancelled)
	case <-time.After(2 * time.Second):
		t.Fatal("no terminal RunComplete published for a pre-run error; a run waiter would hang")
	}
}

// TestRunAgent_NoFallbackWhenCoordinatorPublished ensures the fallback
// 是 suppressed 当 coordinator already emitted 运行's
// authoritative 终态 RunComplete, 所以 callers never observe
// duplicate 终态 事件 为 same RunID.
func TestRunAgent_NoFallbackWhenCoordinatorPublished(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend(t)
	runErr := errors.New("stream failed after publishing terminal event")
	ws := insertRunCompleteWorkspace(t, b, context.Background(),
		&errorCoordinator{err: runErr, markPublished: true})

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := ws.RunCompletions().Subscribe(subCtx)

	err := b.SendMessage(ws.ID, proto.AgentMessage{SessionID: "S1", RunID: "run-1", Prompt: "hi"})
	require.NoError(t, err)

	// Wait 为 dispatched 运行 goroutine 到 return 所以 any 发布
	// has already happened.
	ws.runWG.Wait()

	select {
	case ev := <-ch:
		t.Fatalf("runAgent published a duplicate terminal RunComplete: %+v", ev.Payload)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestRunAgent_CancellationPublishesNoErrorTerminal verifies 那
// 上下文.Canceled 结果 从 RunAccepted produces no errored 终态
// RunComplete 从 runAgent: cancellation 是 sessionAgent.运行's
// responsibility (it publishes cancelled marker) 和 dispatcher
// must 不 synthesize 错误 终态 为 it.
func TestRunAgent_CancellationPublishesNoErrorTerminal(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend(t)
	ws := insertRunCompleteWorkspace(t, b, context.Background(),
		&errorCoordinator{err: context.Canceled})

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := ws.RunCompletions().Subscribe(subCtx)

	err := b.SendMessage(ws.ID, proto.AgentMessage{SessionID: "S1", RunID: "run-1", Prompt: "hi"})
	require.NoError(t, err)

	ws.runWG.Wait()

	select {
	case ev := <-ch:
		t.Fatalf("cancellation must not publish a terminal RunComplete: %+v", ev.Payload)
	case <-time.After(200 * time.Millisecond):
	}
}

func (*errorCoordinator) ListSettledMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}
