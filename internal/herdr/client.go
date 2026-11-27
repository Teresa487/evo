// Package herdr provides native integration 用 herdr 终态
// multiplexer. When Crush runs inside a herdr-managed pane it reports
// agent state (空闲, working, blocked) 和 会话 identity over
// herdr's Unix socket API 所以 herdr can display accurate status without
// screen scraping.
//
// 客户端 consumes small, herdr-specific 事件 vocabulary rather
// than accepting raw proto 或 domain types. Callers translate their
// events into herdr.事件 before forwarding. 这 keeps 客户端
// decoupled 从 both proto 和 internal domain layers.
package herdr

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// State values matching herdr's PaneAgentState enum.
const (
	stateIdle    = "idle"
	stateWorking = "working"
	stateBlocked = "blocked"
)

// 事件 是 herdr-specific 事件 vocabulary. Each type maps 到
// distinct state transition 在 agent lifecycle. Callers translate
// 从 proto 或 domain types into these before calling HandleEvent.
type Event interface {
	herdrEvent()
}

// AssistantMessage indicates the agent produced output. Transitions
// 到 working 如果 不 already 活跃.
type AssistantMessage struct {
	SessionID string
}

func (AssistantMessage) herdrEvent() {}

// RunComplete indicates agent finished turn. Transitions 到 空闲.
type RunComplete struct {
	SessionID string
}

func (RunComplete) herdrEvent() {}

// PermissionRequested indicates agent 是 waiting 为 用户 approval.
// Transitions 到 blocked.
type PermissionRequested struct{}

func (PermissionRequested) herdrEvent() {}

// PermissionResolved indicates 权限 decision was made.
// Transitions back 到 working 如果 运行 是 活跃, 空闲 otherwise.
type PermissionResolved struct{}

func (PermissionResolved) herdrEvent() {}

// Summarizing indicates agent 是 compacting 上下文. Transitions
// 到 working 如果 不 already 活跃.
type Summarizing struct{}

func (Summarizing) herdrEvent() {}

// sender abstracts transport layer 为 reporting state 到 herdr.
// Production uses a Unix socket; tests use a recorder.
type sender interface {
	send(req reportRequest) error
	close()
}

// 客户端 reports Crush agent state 到 running herdr instance.
type Client struct {
	socketPath string
	paneID     string

	mu        sync.Mutex
	sessionID string
	state     string
	runActive bool
	seq       uint64

	snd sender
}

// defaultClient 是 process-wide herdr 客户端. Initialized once
// via Init(). All integration sites share 这 single instance 所以
// 只 one Unix socket 连接 exists per process.
var (
	defaultClient *Client
	initOnce      sync.Once
)

// Init returns process-wide herdr 客户端, creating it 在 first
// 调用 从 environment variables. Returns nil 当 Crush 是 不
// running inside herdr pane. Safe 到 调用 从 any goroutine.
func Init() *Client {
	initOnce.Do(func() {
		defaultClient = newFromEnv()
	})
	return defaultClient
}

func newFromEnv() *Client {
	if os.Getenv("HERDR_ENV") != "1" {
		return nil
	}
	// 一个 测试 二进制 inherits launching shell's HERDR_* env, 所以
	// without 这 it would attach 到 developer's live pane 和
	// release 它的 agent 在 teardown. Skip herdr entirely under 测试.
	if flag.Lookup("test.v") != nil {
		slog.Debug("Herdr integration disabled: running under go test")
		return nil
	}
	socketPath := os.Getenv("HERDR_SOCKET_PATH")
	paneID := os.Getenv("HERDR_PANE_ID")
	if socketPath == "" || paneID == "" {
		slog.Debug(
			"Herdr integration disabled: incomplete environment",
			"has_socket", socketPath != "",
			"has_pane_id", paneID != "",
		)
		return nil
	}
	c := &Client{
		socketPath: socketPath,
		paneID:     paneID,
		state:      stateIdle,
		seq:        uint64(time.Now().UnixNano()),
		snd:        newUnixSender(socketPath),
	}
	c.registerInitial()
	return c
}

// registerInitial sends initial 空闲-state 报告 到 herdr 所以
// pane knows about agent immediately, 不 just after first
// 事件. Called once during 客户端 creation. Bypasses dedup
// check since initial state must always 是 reported regardless
// 的 redundancy.
//
// herdr remembers highest seq it has seen per source 为
// lifetime 的 pane 和 silently drops any 报告 用 seq 那
// 是 不 strictly greater. Because crush seeds seq 从 wall
// clock 在 startup (see newFromEnv), restarted crush 在 same
// pane always reports above previous 运行's high-water 标记, 所以
// first 报告 是 accepted instead 的 being rejected 作为 stale.
func (c *Client) registerInitial() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snd.send(c.newRequestLocked("pane.report_agent", "init", stateIdle))
}

// Close releases agent's authority 在 pane 和 shuts down
// background writer. Safe 到 调用 在 nil 客户端.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.releaseAgent()
	c.snd.close()
}

// releaseAgent sends pane.release_agent 请求 到 herdr 所以
// pane 是 freed 为 new agent 到 claim authority. 这 是
// clean-shutdown protocol per herdr's socket API. Sends directly
// 在 socket 到 ensure delivery even 如果 写入 loop 是 busy.
func (c *Client) releaseAgent() {
	c.mu.Lock()
	defer c.mu.Unlock()
	req := c.newRequestLocked("pane.release_agent", "release", "")
	if err := dialSend(c.socketPath, req); err != nil {
		slog.Debug("Herdr release_agent failed", "error", err)
	}
}

// HandleEvent processes single herdr 事件 和 reports state changes.
// Safe 到 调用 从 any goroutine.
func (c *Client) HandleEvent(ev Event) {
	if c == nil {
		return
	}
	switch e := ev.(type) {
	case AssistantMessage:
		c.onAssistantMessage(e.SessionID)
	case RunComplete:
		c.onRunComplete(e.SessionID)
	case PermissionRequested:
		c.onPermissionRequest()
	case PermissionResolved:
		c.onPermissionResolved()
	case Summarizing:
		c.onSummarizing()
	}
}

// SetSessionID sets 会话 ID 为 reporting. 调用 这 当
// 会话 是 created 或 resolved, before events start flowing.
func (c *Client) SetSessionID(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = id
}

func (c *Client) onAssistantMessage(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionID != "" {
		c.sessionID = sessionID
	}
	if !c.runActive {
		c.runActive = true
		c.reportLocked(stateWorking)
	}
}

func (c *Client) onRunComplete(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runActive = false
	if sessionID != "" {
		c.sessionID = sessionID
	}
	c.reportLocked(stateIdle)
}

func (c *Client) onPermissionRequest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 一个 权限 请求 implies 运行 是 活跃, even 如果 no
	// 助手消息 has arrived yet (e.g. 工具 calls 那 fire
	// before any 文本 output).
	if !c.runActive {
		c.runActive = true
	}
	c.reportLocked(stateBlocked)
}

func (c *Client) onPermissionResolved() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runActive {
		c.reportLocked(stateWorking)
	} else {
		c.reportLocked(stateIdle)
	}
}

func (c *Client) onSummarizing() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.runActive {
		c.runActive = true
	}
	c.reportLocked(stateWorking)
}

// newRequestLocked builds seq-stamped JSON-RPC 请求 到 herdr.
// Must 是 called 用 c.mu held. Every 请求 increments c.seq 所以
// herdr accepts it 作为 strictly newer than last (see
// registerInitial 为 why monotonic seq matters). State 是 empty 为
// requests 那 carry no agent state, such 作为 pane.release_agent.
func (c *Client) newRequestLocked(method, idPrefix, state string) reportRequest {
	c.seq++
	return reportRequest{
		ID:     fmt.Sprintf("crush:%s:%d", idPrefix, time.Now().UnixNano()),
		Method: method,
		Params: reportParams{
			PaneID:         c.paneID,
			Source:         "crush",
			Agent:          "crush",
			State:          state,
			Seq:            c.seq,
			AgentSessionID: c.sessionID,
		},
	}
}

// reportLocked sends pane.report_agent 请求 到 herdr. Must 是
// called 用 c.mu held. Skips redundant reports 当 state has
// 不 changed.
func (c *Client) reportLocked(state string) {
	if state == c.state {
		return
	}
	c.state = state
	c.snd.send(c.newRequestLocked("pane.report_agent", "report", state))
}

// reportRequest 是 JSON-RPC envelope sent 到 herdr.
type reportRequest struct {
	ID     string       `json:"id"`
	Method string       `json:"method"`
	Params reportParams `json:"params"`
}

// reportParams carries the agent state payload.
type reportParams struct {
	PaneID         string `json:"pane_id"`
	Source         string `json:"source"`
	Agent          string `json:"agent"`
	State          string `json:"state"`
	Seq            uint64 `json:"seq"`
	AgentSessionID string `json:"agent_session_id"`
}

// unixSender sends JSON-RPC requests over a Unix domain socket using
// single background writer goroutine 和 buffered 渠道. 这
// serializes writes 和 avoids spawning unbounded goroutines under
// high 事件 throughput. Each 报告 opens short-lived 连接.
type unixSender struct {
	socketPath string
	ch         chan reportRequest
	cancel     context.CancelFunc
}

func newUnixSender(socketPath string) *unixSender {
	ctx, cancel := context.WithCancel(context.Background())
	s := &unixSender{
		socketPath: socketPath,
		ch:         make(chan reportRequest, 16),
		cancel:     cancel,
	}
	go s.writeLoop(ctx)
	return s
}

func (s *unixSender) send(req reportRequest) error {
	select {
	case s.ch <- req:
	default:
		// Drop 如果 缓冲 是 full. State reports 是
		// best-effort; blocking agent 是 worse than
		// missing a transition.
	}
	return nil
}

func (s *unixSender) close() {
	s.cancel()
}

func (s *unixSender) writeLoop(ctx context.Context) {
	for {
		select {
		case req, ok := <-s.ch:
			if !ok {
				return
			}
			if err := dialSend(s.socketPath, req); err != nil {
				slog.Debug("Herdr report failed", "error", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// dialSend opens short-lived Unix socket 连接 到 herdr,
// sends single JSON-RPC 请求, 和 drains 响应.
func dialSend(socketPath string, req reportRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	_, err = conn.Write(data)
	if err != nil {
		return err
	}

	// Drain 响应 到 完成 请求 cycle.
	_, _ = io.Copy(io.Discard, conn)
	return nil
}
