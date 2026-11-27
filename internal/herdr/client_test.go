package herdr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// recordingSender captures state transitions without connecting 到
// real Unix socket.
type recordingSender struct {
	states []string
}

func (r *recordingSender) send(req reportRequest) error {
	r.states = append(r.states, req.Params.State)
	return nil
}

func (r *recordingSender) close() {}

// newTestClient creates 客户端 那 records state transitions
// without connecting 到 real Unix socket.
func newTestClient() *Client {
	rec := &recordingSender{states: make([]string, 0, 16)}
	return &Client{
		state: stateIdle,
		snd:   rec,
	}
}

// reportedStates returns states recorded 通过 测试 sender.
func reportedStates(c *Client) []string {
	return c.snd.(*recordingSender).states
}

func TestBasicLifecycle(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// 助手消息 starts working.
	c.HandleEvent(AssistantMessage{SessionID: "sess-1"})
	assert.Equal(t, []string{stateWorking}, reportedStates(c))

	// 运行 完成 returns 到 空闲.
	c.HandleEvent(RunComplete{SessionID: "sess-1"})
	assert.Equal(t, []string{stateWorking, stateIdle}, reportedStates(c))
}

func TestPermissionBlockAndUnblock(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// Start working.
	c.HandleEvent(AssistantMessage{SessionID: "sess-1"})

	// 权限 请求 blocks.
	c.HandleEvent(PermissionRequested{})
	assert.Equal(t, []string{stateWorking, stateBlocked}, reportedStates(c))

	// 权限 granted returns 到 working (运行 still 活跃).
	c.HandleEvent(PermissionResolved{})
	assert.Equal(t, []string{stateWorking, stateBlocked, stateWorking}, reportedStates(c))

	// 运行 完成 returns 到 空闲.
	c.HandleEvent(RunComplete{SessionID: "sess-1"})
	assert.Equal(t, []string{stateWorking, stateBlocked, stateWorking, stateIdle}, reportedStates(c))
}

func TestPermissionBeforeAssistantMessage(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// 权限 请求 arrives before any 助手消息.
	// 这 can happen 当 工具 calls fire before 文本 output.
	c.HandleEvent(PermissionRequested{})
	assert.Equal(t, []string{stateBlocked}, reportedStates(c))

	// 权限 resolved should return 到 working, 不 空闲,
	// because 权限 请求 implied 运行 was 活跃.
	c.HandleEvent(PermissionResolved{})
	assert.Equal(t, []string{stateBlocked, stateWorking}, reportedStates(c))
}

func TestSessionIDPropagation(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// SetSessionID before events.
	c.SetSessionID("early-session")
	assert.Equal(t, "early-session", c.sessionID)

	// RunComplete also updates 会话 ID.
	c.HandleEvent(RunComplete{SessionID: "final-session"})
	assert.Equal(t, "final-session", c.sessionID)
}

func TestDedupSkipsRedundantState(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// Two 助手 messages 在 行 should 只 报告 working once.
	c.HandleEvent(AssistantMessage{SessionID: "s1"})
	c.HandleEvent(AssistantMessage{SessionID: "s1"})
	assert.Equal(t, []string{stateWorking}, reportedStates(c))
}

func TestSummarizingTriggersWorking(t *testing.T) {
	t.Parallel()
	c := newTestClient()

	// Summarizing 事件 should trigger working.
	c.HandleEvent(Summarizing{})
	assert.Equal(t, []string{stateWorking}, reportedStates(c))

	// Second summarizing should 不 trigger another state change.
	c.HandleEvent(Summarizing{})
	assert.Equal(t, []string{stateWorking}, reportedStates(c))
}

func TestNilClientSafe(t *testing.T) {
	t.Parallel()
	var c *Client
	// These should 不 panic 在 nil receiver.
	c.SetSessionID("s1")
	c.HandleEvent(AssistantMessage{SessionID: "s1"})
	c.HandleEvent(RunComplete{SessionID: "s1"})
	c.HandleEvent(PermissionRequested{})
	c.HandleEvent(PermissionResolved{})
	c.HandleEvent(Summarizing{})
}

func TestRegisterInitial(t *testing.T) {
	t.Parallel()
	rec := &recordingSender{states: make([]string, 0, 16)}
	c := &Client{
		state: stateIdle,
		seq:   100,
		snd:   rec,
	}
	c.registerInitial()
	assert.Equal(t, []string{stateIdle}, rec.states)
	// seq must strictly increase 所以 herdr accepts 报告.
	assert.Equal(t, uint64(101), c.seq)
}

// TestInitDisabledUnderTest guards critical safety property 那
// herdr never attaches 到 real pane 从 测试 二进制. 测试
// processes inherit developer's HERDR_* environment, 所以 missing
// guard would release live pane's agent 在 teardown. Because 这
// 测试 itself runs under `go 测试`, Init must return nil even 用
// 完成, valid-looking environment.
func TestInitDisabledUnderTest(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/does-not-matter.sock")
	t.Setenv("HERDR_PANE_ID", "test:pane")
	assert.Nil(t, newFromEnv())
}
