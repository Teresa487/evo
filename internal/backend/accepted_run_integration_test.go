package backend

import (
	"context"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/agenttest"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// gatedCoordinator wraps real agent.Coordinator 和 parks RunAccepted
// before delegating 到 it. Every method other than RunAccepted 是
// inherited 从 embedded coordinator, 所以 BeginAccepted (called 通过
// Backend.SendMessage) 和 RunAccepted (called 通过 dispatched 运行)
// 是 production agent.Coordinator implementations under 测试, 不
// stubs. gate 只 delays 条目 into real RunAccepted 所以
// 取消 can 是 made 到 land 在 accepted-但-不-yet-活跃 窗口
// deterministically: 受理 handle 是 不 consumed 通过
// sessionAgent.运行 until real RunAccepted runs after gate opens.
type gatedCoordinator struct {
	agent.Coordinator
	entered chan struct{}
	gate    chan struct{}
}

func (c *gatedCoordinator) RunAccepted(ctx context.Context, accept *agent.AcceptedRun, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	close(c.entered)
	<-c.gate
	return c.Coordinator.RunAccepted(ctx, accept, sessionID, prompt, attachments...)
}

// newRealCoordinator builds a production agent.Coordinator over a
// DB-backed 会话/消息 存储, wrapped 在 gate. 它 是 constructed
// through real agent.NewCoordinator 路径 (via 测试-只
// agenttest helper) 用 offline-resolvable 模型:
// 取消-在-条目 路径 under 测试 persists canceled turn 和 returns
// before any 模型 调用, 所以 no network I/O happens.
func newRealCoordinator(t *testing.T) (*gatedCoordinator, session.Service, message.Service) {
	t.Helper()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)

	coord, err := agenttest.NewCoordinator(t.Context(), t.TempDir(), sessions, messages)
	require.NoError(t, err)

	return &gatedCoordinator{
		Coordinator: coord,
		entered:     make(chan struct{}),
		gate:        make(chan struct{}),
	}, sessions, messages
}

// TestSendMessage_AcceptedCancelRace_RealMachinery exercises the
// 202/取消 race end-到-end through Backend.SendMessage against
// production agent.Coordinator (BeginAccepted + RunAccepted), 不
// stub. 它 asserts 那 取消 arriving after 提示词 是 accepted
// 但 before 运行 becomes 活跃 是 不 lost: accepted handle
// reaches sessionAgent.运行 和 drives 取消-在-条目, which persists
// canceled turn instead 的 streaming.
//
// 这 测试 would fail 如果 Coordinator.BeginAccepted returned nil (取消
// would find no accepted 运行 和 record no 标记, 和 运行 would
// receive nil Accepted handle 和 skip 取消-在-条目) 或 如果
// Coordinator.RunAccepted dropped handle 在 它的 way into
// sessionAgent.运行 ( 运行 would likewise skip 取消-在-条目 和 try
// 到 流 模型). In either case no FinishReasonCanceled turn
// would 是 persisted.
func TestSendMessage_AcceptedCancelRace_RealMachinery(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend(t)

	coord, sessions, messages := newRealCoordinator(t)
	sess, err := sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	ws := insertAgentWorkspace(t, b, coord)

	require.NoError(t, b.SendMessage(ws.ID, proto.AgentMessage{SessionID: sess.ID, Prompt: "hi"}))

	// Coordinator.BeginAccepted ran synchronously inside SendMessage
	// before 派发; dispatched 运行 has now entered gate 但
	// has 不 yet called real RunAccepted, 所以 受理 handle 是
	// 不 yet consumed: 提示词 是 accepted 但 不 活跃.
	select {
	case <-coord.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatched run never entered RunAccepted")
	}

	// 一个 取消 arriving now lands 在 accepted-但-不-yet-活跃
	// 窗口 和 是 只 recorded because BeginAccepted incremented
	// 受理 计数器.
	require.NoError(t, b.CancelSession(ws.ID, sess.ID))

	// Release gate 所以 real RunAccepted threads handle into
	// sessionAgent.运行, which drives 取消-在-条目.
	close(coord.gate)

	// dispatched 运行 returns nil (取消-在-条目), 所以 runWG drains.
	waited := make(chan struct{})
	go func() {
		ws.runWG.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("runWG.Wait did not complete after the canceled run returned")
	}

	// accepted-但-不-yet-活跃 取消 persisted canceled turn
	// rather than streaming real 响应.
	msgs, err := messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	require.Equal(t, message.User, msgs[0].Role)
	require.Equal(t, message.Assistant, msgs[1].Role)
	require.Equal(t, message.FinishReasonCanceled, msgs[1].FinishReason())
}
