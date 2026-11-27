package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
)

//go:embed templates/agent_dispatch_tool.md
var agentDispatchToolDescription string

//go:embed templates/agent_send_tool.md
var agentSendToolDescription string

//go:embed templates/agent_stop_tool.md
var agentStopToolDescription string

const (
	AgentDispatchToolName = "agent_dispatch"
	AgentSendToolName     = "agent_send"
	AgentStopToolName     = "agent_stop"
)

// stopForShutdown 是 stop reason used 当 Crush itself 是 quitting.
// Nobody 是 left 到 读取 报告 then, 所以 none 是 written; next 运行 在
// parent 会话 finds 派发 unanswered 和 reports it 作为
// interrupted instead.
const stopForShutdown = "Crush is shutting down"

type AgentDispatchParams struct {
	Prompt string `json:"prompt" description:"The task for the subagent to perform"`
	Label  string `json:"label" description:"A short label for this subagent's scope; the result returns the name to address it by"`
	Access string `json:"access,omitempty" description:"\"read\" (default) for a subagent that can only inspect the codebase, or \"write\" for one that can also edit files"`

	// 模型 override. When none 的 these 是 given 子代理 runs 在
	// 会话's 子代理 模型 用 它的 configured settings.
	Model     string `json:"model,omitempty" description:"The model ID for this subagent, as used by the provider API. Omit provider to use the parent agent's provider. Other settings default to the model's own"`
	Provider  string `json:"provider,omitempty" description:"The provider ID for the overridden model; requires model. Defaults to the parent agent's provider when only model is given"`
	Reasoning string `json:"reasoning,omitempty" description:"How hard the subagent reasons: one of the values listed in brackets after its model. Defaults to the model's own"`
}

// AgentSendResponseMetadata tells UI which 子代理 消息 went 到,
// 和 whether it restarted finished one, 所以 transcript can show 那
// 子代理 作为 working again.
type AgentSendResponseMetadata struct {
	Label    string `json:"label"`
	Reopened bool   `json:"reopened,omitempty"`
	// Set 当 消息 moved 子代理 到 another 模型, 所以
	// restart resumes it there.
	Model     string `json:"model,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	RunModel  string `json:"run_model,omitempty"`
}

// AgentDispatchResponseMetadata records started 派发's override 作为
// 调用 asked 为 it, 用 提供方 resolved, 所以 resume after restart
// runs 在 same 提供方 whatever 会话's 模型 是 通过 then.
type AgentDispatchResponseMetadata struct {
	Label     string `json:"label"`
	Model     string `json:"model,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	// RunModel 是 提供方/模型 为 display; rehydration ignores it.
	RunModel string `json:"run_model,omitempty"`
}

type AgentSendParams struct {
	Label   string `json:"label" description:"The name of the subagent to send to, as returned when it was created"`
	Message string `json:"message" description:"The message to deliver to that subagent"`

	// 模型 switch, 为 子代理 那 是 不 running. Same meaning 作为 在
	// agent_dispatch; 子代理 keeps 它的 会话.
	Model     string `json:"model,omitempty" description:"Move the subagent to this model before delivering, keeping its session. Only for a subagent that is not running. Same IDs as agent_dispatch's model list"`
	Provider  string `json:"provider,omitempty" description:"The provider ID for model; requires model. Defaults to the parent agent's provider"`
	Reasoning string `json:"reasoning,omitempty" description:"How hard the subagent reasons on its new model: one of the values in agent_dispatch's model list"`
}

func (p AgentSendParams) switchesModel() bool {
	return strings.TrimSpace(p.Model) != "" || strings.TrimSpace(p.Provider) != "" || strings.TrimSpace(p.Reasoning) != ""
}

type AgentStopParams struct {
	Label  string `json:"label" description:"The name of the subagent to stop, as returned when it was created"`
	Reason string `json:"reason,omitempty" description:"Why it is being stopped; recorded in its report"`
}

// dispatchState 是 where dispatched 子代理 是 在 它的 life.
type dispatchState int

const (
	dispatchRunning dispatchState = iota
	dispatchFinished
	dispatchFailed
	dispatchStopped
)

// reportArrivesOnItsOwn closes every 结果 那 leaves 子代理 working.
// Models 读取 started-但-unfinished job 作为 something 到 wait 在, 和
// without 这 they sleep 和 poll agent_status until 报告 lands,
// which it would have done anyway.
const reportArrivesOnItsOwn = " The report arrives on its own and wakes you, so carry on with" +
	" anything that does not need it, then end your turn. Do not sleep or" +
	" check agent_status to wait for it."

// SubagentState 是 子代理's state 作为 agent_status reports it.
type SubagentState string

const (
	SubagentRunning     SubagentState = "running"
	SubagentStopping    SubagentState = "stopping"
	SubagentFinished    SubagentState = "finished"
	SubagentFailed      SubagentState = "failed"
	SubagentStopped     SubagentState = "stopped"
	SubagentInterrupted SubagentState = "interrupted"
)

func (s dispatchState) reported() SubagentState {
	switch s {
	case dispatchRunning:
		return SubagentRunning
	case dispatchFailed:
		return SubagentFailed
	case dispatchStopped:
		return SubagentStopped
	}
	return SubagentFinished
}

// dispatchedAgent tracks one detached 子代理 across all 的 它的 runs:
// first one, 和 every follow-up 那 reopens it.
type dispatchedAgent struct {
	Label           string
	SessionID       string
	ParentSessionID string
	Access          string
	// agent 是 SessionAgent instance actually running 这 子代理.
	// Busy state 和 消息 队列 live 在 那 instance, 不 在
	// coordinator's main agent, 所以 消息 meant 为 子代理 has 到
	// 是 addressed 到 it directly.
	agent SessionAgent

	mu sync.Mutex
	// gen numbers runs. Each 运行 settles 只 它的 own generation, 所以
	// 运行 那 是 slow 到 unwind cannot 标记 newer 运行 作为 finished.
	gen        uint64
	state      dispatchState
	cancel     context.CancelFunc
	stopReason string
	started    time.Time
	ended      time.Time
	// done closes 当 current 运行 settles.
	done chan struct{}
	// inline 是 set while caller waits 在 运行 和 takes 它的 outcome
	// instead 的 报告.
	inline  bool
	outcome runOutcome

	// rolledUpCost 是 child 会话's cumulative cost already added 到
	// parent. Each 运行 rolls up 只 what it spent since, since
	// child's own total 是 cumulative across every turn.
	rolledUpCost atomic.Uint64
}

// runOutcome 是 what one 运行 的 子代理 returned.
type runOutcome struct {
	resp fantasy.ToolResponse
	err  error
}

// newDispatchedAgent returns 条目 为 子代理 那 是 不 running.
// Callers start it 用 claim.
func newDispatchedAgent(label, access, sessionID, parentSessionID string, agent SessionAgent) *dispatchedAgent {
	return &dispatchedAgent{
		Label:           label,
		Access:          access,
		SessionID:       sessionID,
		ParentSessionID: parentSessionID,
		agent:           agent,
		state:           dispatchFinished,
	}
}

// claim starts new 运行 如果 子代理 是 空闲. returned 上下文 是
// detached 从 whatever turn asked 为 运行: 子代理 outlives
// turn 那 started it 和 是 stopped 只 through stop.
func (d *dispatchedAgent) claim() (context.Context, uint64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state == dispatchRunning {
		return nil, 0, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.gen++
	d.state = dispatchRunning
	d.cancel = cancel
	d.stopReason = ""
	d.started = time.Now()
	d.ended = time.Time{}
	d.done = make(chan struct{})
	d.inline = false
	d.outcome = runOutcome{}
	return ctx, d.gen, true
}

// settle records how 运行 gen ended. 它 returns stop reason 如果 运行
// was stopped, 和 whether outcome should 是 reported rather than taken
// 通过 inline caller. 一个 stale generation 是 ignored.
func (d *dispatchedAgent) settle(gen uint64, out runOutcome) (stopReason string, report bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if gen != d.gen {
		return "", false
	}
	switch {
	case d.stopReason != "":
		d.state = dispatchStopped
	case out.err != nil || out.resp.IsError:
		d.state = dispatchFailed
	default:
		d.state = dispatchFinished
	}
	d.ended = time.Now()
	d.outcome = out
	d.cancel()
	close(d.done)
	return d.stopReason, !d.inline
}

// runInline marks 运行 gen 作为 having caller 那 takes 它的 outcome, 和
// returns 渠道 那 closes 当 it settles.
func (d *dispatchedAgent) runInline(gen uint64) <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if gen == d.gen {
		d.inline = true
	}
	return d.done
}

// detach hands 运行 gen over 到 报告 路径. 它 returns false 如果 运行
// already settled, 在 which case inline caller takes 它的 outcome.
func (d *dispatchedAgent) detach(gen uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if gen != d.gen || d.state != dispatchRunning {
		return false
	}
	d.inline = false
	return true
}

func (d *dispatchedAgent) result() (fantasy.ToolResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.outcome.resp, d.outcome.err
}

// stop cancels 运行 在 flight. 它 returns false 如果 nothing 是 running.
// 条目 stays running until 运行 has actually unwound.
func (d *dispatchedAgent) stop(reason string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state != dispatchRunning {
		return false
	}
	if d.stopReason == "" {
		d.stopReason = reason
	}
	d.cancel()
	return true
}

func (d *dispatchedAgent) running() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state == dispatchRunning
}

// stopping reports whether stop was requested 为 运行 still unwinding.
func (d *dispatchedAgent) stopping() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state == dispatchRunning && d.stopReason != ""
}

// dispatchSnapshot 是 consistent 读取 的 条目's lifecycle fields.
type dispatchSnapshot struct {
	State      dispatchState
	StopReason string
	Started    time.Time
	Ended      time.Time
}

func (d *dispatchedAgent) snapshot() dispatchSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return dispatchSnapshot{
		State:      d.state,
		StopReason: d.stopReason,
		Started:    d.started,
		Ended:      d.ended,
	}
}

// awaitSettled waits until 运行 在 flight has unwound, 或 超时
// passes. 它 reports whether 子代理 是 空闲.
func (d *dispatchedAgent) awaitSettled(timeout time.Duration) bool {
	d.mu.Lock()
	running, done := d.state == dispatchRunning, d.done
	d.mu.Unlock()
	if !running {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// awaitDeliverable waits until 子代理's 会话 是 actually running
// 和 can 受理 消息, 和 reports whether it can. 一个 运行 是 claimed
// before 它的 goroutine has started 会话, 所以 消息 sent right
// after dispatching arrives while 会话 是 不 yet busy; treating 那
// 窗口 作为 "finished" would drop 消息. Waiting closes gap
// without blocking indefinitely 在 子代理 那 died 在 startup.
func (d *dispatchedAgent) awaitDeliverable(ctx context.Context) bool {
	const (
		pollEvery = 50 * time.Millisecond
		giveUp    = 10 * time.Second
	)
	deadline := time.NewTimer(giveUp)
	defer deadline.Stop()
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()

	for {
		if !d.running() || d.stopping() {
			return false
		}
		if d.runner().HasActiveTurn(d.SessionID) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

// dispatchKey namespaces label 到 它的 parent 会话, 所以 two sessions can
// use the same label without colliding.
func dispatchKey(parentSessionID, label string) string {
	return parentSessionID + "\x00" + label
}

// agentDispatchTool launches 子代理 那 runs detached 从 这 工具
// 调用. 工具 returns 作为 soon 作为 子代理 starts; 结果 是
// delivered later 作为 消息 into parent 会话.
func (c *coordinator) agentDispatchTool(ctx context.Context, parentAgentID string, parentModel func() Model) (fantasy.AgentTool, error) {
	readOnly := readOnlyParent(parentAgentID)
	agents, err := c.taskAgents(ctx, readOnly)
	if err != nil {
		return nil, err
	}

	return fantasy.NewParallelAgentTool(
		AgentDispatchToolName,
		strings.TrimSpace(agentDispatchToolDescription)+dispatchModelCatalog(c.cfg.Config(), parentModel()),
		func(ctx context.Context, params AgentDispatchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}
			if strings.TrimSpace(params.Label) == "" {
				return fantasy.NewTextErrorResponse("label is required"), nil
			}

			agent, refusal := resolveSubAgent(agents, params.Access, readOnly)
			if refusal != "" {
				return fantasy.NewTextErrorResponse(refusal), nil
			}
			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.ToolResponse{}, errors.New("agent message id missing from context")
			}

			// 一个 模型 override swaps shared 子代理 为 dedicated
			// one running 在 requested 模型. 它 是 resolved before
			// label 是 taken, 所以 refused override leaves label free
			// 和 调用 can 是 retried immediately.
			access := accessOrRead(params.Access)
			overrideAgent, sel, hasOverride, refusal := c.overrideDispatchAgent(ctx, access, params, parentModel())
			if refusal != "" {
				return fantasy.NewTextErrorResponse(refusal), nil
			}
			if overrideAgent != nil {
				agent = overrideAgent
			}

			parentSessionID := tools.GetSessionFromContext(ctx)
			if parentSessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}

			// sub-会话 ID derives 从 bare 工具调用 ID 所以
			// UI can walk back 从 child-会话 事件 到 这 工具's
			// chat item 和 nest 子代理's 工具 calls under it.
			toolCallID := call.ID

			// Named last, 所以 refused 派发 does 不 use up first name.
			label := c.names.name(params.Label)

			// Checking label 和 registering 条目 happen under
			// one 锁, 所以 two parallel dispatches cannot both take it.
			key := dispatchKey(parentSessionID, label)
			c.dispatchMu.Lock()
			if existing, ok := c.dispatched.Get(key); ok && existing.running() {
				c.dispatchMu.Unlock()
				return fantasy.NewTextErrorResponse(fmt.Sprintf("subagent %q is already running; pick another label or stop it first", label)), nil
			}
			entry := newDispatchedAgent(
				label,
				access,
				c.sessions.CreateAgentToolSessionID(agentMessageID, toolCallID),
				parentSessionID,
				agent,
			)
			runCtx, gen, _ := entry.claim()
			c.dispatched.Set(key, entry)
			c.dispatchMu.Unlock()

			c.runDispatched(runCtx, entry, gen, func(ctx context.Context) (fantasy.ToolResponse, error) {
				return c.runSubAgent(ctx, subAgentParams{
					Agent:          agent,
					SessionID:      parentSessionID,
					AgentMessageID: agentMessageID,
					ToolCallID:     toolCallID,
					Prompt:         params.Prompt,
					SessionTitle:   "Subagent: " + label,
					SkipCostRollup: true,
				})
			})

			meta := AgentDispatchResponseMetadata{
				Label:    label,
				RunModel: modelDisplayName(agent.Model()),
			}
			if hasOverride {
				meta.Model, meta.Provider, meta.Reasoning = overrideRecord(params, sel)
			}
			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(fmt.Sprintf("Started subagent %q.", label)+reportArrivesOnItsOwn),
				meta,
			), nil
		},
	), nil
}

// runDispatched runs one generation 的 子代理 在 background, then
// settles 条目 和 reports outcome 到 parent.
func (c *coordinator) runDispatched(ctx context.Context, entry *dispatchedAgent, gen uint64, run func(context.Context) (fantasy.ToolResponse, error)) {
	// Tracked 从 here, 不 从 当 会话 starts, 所以 busy
	// check never sees claimed 运行 作为 空闲.
	untrack := c.trackSubSession(entry.SessionID)
	go func() {
		resp, err := run(ctx)
		untrack()
		c.rollUpDispatchCost(context.Background(), entry)
		// Settle before reporting: 报告 starts parent turn, 和
		// 那 turn may well send 这 子代理 follow-up, which needs
		// it 空闲.
		stopReason, report := entry.settle(gen, runOutcome{resp, err})
		c.pruneDispatched()
		if !report {
			return
		}

		switch stopReason {
		case "":
			c.reportDispatchResult(entry, resp, err)
		case stopForShutdown:
		default:
			// 一个 stopped 子代理 has nothing 到 报告 和 它的 parent
			// already knows it was stopped, 所以 record outcome
			// without starting a turn over it.
			c.recordDispatchReport(entry.ParentSessionID, message.SubAgentReport{
				Label:  entry.Label,
				Output: "Stopped: " + stopReason + ".",
				Failed: true,
			})
		}
	}()
}

// runner 是 agent running 这 子代理; agent_send can swap it.
func (d *dispatchedAgent) runner() SessionAgent {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.agent
}

func (d *dispatchedAgent) setRunner(agent SessionAgent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.agent = agent
}

// reopen runs another turn 在 finished 子代理's existing 会话, 所以 it
// answers 用 它的 whole task still 在 上下文. 这 是 what makes
// 子代理 answerable rather than one-shot: 会话 是 real 和 它的
// history 是 intact, 所以 follow-up 是 just next turn 在 it.
//
// 一个 non-nil agent replaces 子代理's runner, 和 只 当 claim
// succeeds, 所以 refused reopen leaves 子代理 作为 it was.
//
// Returns false 如果 子代理 是 不 空闲, meaning somebody else claimed
// it first 或 it 是 still working.
func (c *coordinator) reopen(entry *dispatchedAgent, prompt string, agent SessionAgent) bool {
	// Claiming 和 registering 是 one step under dispatchMu: two concurrent
	// follow-ups resolve 到 one reopen, 和 条目 pruned meanwhile 是 put
	// back rather than left running outside the registry.
	c.dispatchMu.Lock()
	key := dispatchKey(entry.ParentSessionID, entry.Label)
	if current, ok := c.dispatched.Get(key); ok {
		entry = current
	}
	swap := agent != nil
	if !swap {
		agent = entry.runner()
	}
	agentCall, err := c.subAgentCall(agent, entry.SessionID, prompt)
	if err != nil {
		c.dispatchMu.Unlock()
		return false
	}
	runCtx, gen, ok := entry.claim()
	if ok {
		if swap {
			entry.setRunner(agent)
		}
		c.dispatched.Set(key, entry)
	}
	c.dispatchMu.Unlock()
	if !ok {
		return false
	}

	c.runDispatched(runCtx, entry, gen, func(ctx context.Context) (fantasy.ToolResponse, error) {
		result, err := agent.Run(ctx, agentCall)
		if err != nil {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("Subagent failed: %s", err)), nil
		}
		return subAgentResponse(result), nil
	})
	return true
}

// rollUpDispatchCost adds what 子代理 spent since last roll-up 到
// 它的 parent 会话.
func (c *coordinator) rollUpDispatchCost(ctx context.Context, entry *dispatchedAgent) {
	child, err := c.sessions.Get(ctx, entry.SessionID)
	if err != nil {
		return
	}
	previous := math.Float64frombits(entry.rolledUpCost.Load())
	delta := child.Cost - previous
	if delta <= 0 {
		return
	}
	entry.rolledUpCost.Store(math.Float64bits(child.Cost))

	parent, err := c.sessions.Get(ctx, entry.ParentSessionID)
	if err != nil {
		return
	}
	parent.Cost += delta
	if _, err := c.sessions.Save(ctx, parent); err != nil {
		slog.Warn("Failed to roll up subagent cost", "label", entry.Label, "error", err)
	}
}

// reportDispatchResult delivers finished 子代理's output 到 parent:
// it folds into turn 在 progress 或 starts one. 一个 busy parent queues it,
// 和 it reaches transcript 当 模型 reads it; 如果 waking parent
// fails before then, it 是 recorded anyway 所以 it 是 never lost.
func (c *coordinator) reportDispatchResult(entry *dispatchedAgent, resp fantasy.ToolResponse, err error) {
	var (
		body   string
		failed bool
	)
	switch {
	case err != nil:
		body, failed = fmt.Sprintf("Subagent failed: %s", err), true
	case resp.Content == "":
		body, failed = "Subagent completed but produced no text output.", true
	default:
		body, failed = resp.Content, resp.IsError
	}

	report := message.SubAgentReport{
		Label:  entry.Label,
		Output: body,
		Failed: failed,
		Model:  modelDisplayName(entry.runner().Model()),
	}
	ctx := context.Background()
	if _, err := c.runReport(ctx, entry.ParentSessionID, report); err != nil {
		slog.Error(
			"Failed to wake the parent with a subagent report",
			"label", entry.Label,
			"parent_session", entry.ParentSessionID,
			"error", err,
		)
		if !c.reportRecorded(ctx, entry.ParentSessionID, report) {
			c.recordDispatchReport(entry.ParentSessionID, report)
		}
	}
}

// reportRecorded reports whether 报告 是 already 在 parent's
// transcript, 作为 it 是 当 turn it started failed after writing it.
func (c *coordinator) reportRecorded(ctx context.Context, parentSessionID string, report message.SubAgentReport) bool {
	msgs, err := c.messages.List(ctx, parentSessionID)
	if err != nil {
		return false
	}
	for _, msg := range slices.Backward(msgs) {
		if slices.Contains(msg.SubAgentReports(), report) {
			return true
		}
	}
	return false
}

// recordDispatchReport stores 报告 在 parent 会话 without starting
// turn. 模型 reads it 用 rest 的 history 在 它的 next turn,
// 和 UI settles 子代理's item 当 it lands.
func (c *coordinator) recordDispatchReport(parentSessionID string, report message.SubAgentReport) (message.Message, bool) {
	msg, err := c.messages.Create(context.Background(), parentSessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{report},
	})
	if err != nil {
		slog.Error(
			"Failed to record subagent report",
			"label", report.Label,
			"parent_session", parentSessionID,
			"error", err,
		)
		return message.Message{}, false
	}
	return msg, true
}

// agentSendTool delivers 消息 到 running detached 子代理.
// 消息 是 queued 在 子代理's 会话, where sessionAgent folds it
// into 子代理's turn 在 它的 next step.
func (c *coordinator) agentSendTool(parentModel func() Model) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		AgentSendToolName,
		agentSendToolDescription,
		func(ctx context.Context, params AgentSendParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			label := strings.TrimSpace(params.Label)
			if label == "" {
				return fantasy.NewTextErrorResponse("label is required"), nil
			}
			if params.Message == "" {
				return fantasy.NewTextErrorResponse("message is required"), nil
			}

			parentSessionID := tools.GetSessionFromContext(ctx)
			if parentSessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}

			key := dispatchKey(parentSessionID, label)
			entry, ok := c.dispatched.Get(key)
			if !ok {
				// Not 在 registry, which after restart 是 every
				// 子代理. transcript 和 child 会话 still hold
				// work, 所以 rebuild 条目 rather than refusing.
				rebuilt, err := c.rehydrateDispatch(ctx, parentSessionID, label)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf(
						"no subagent %q; running: %s",
						label, c.runningDispatchLabels(parentSessionID),
					)), nil
				}
				entry = rebuilt
			}

			if params.switchesModel() {
				return c.sendOnNewModel(ctx, entry, params, parentModel()), nil
			}

			// Each pass either folds 消息 into running turn 或
			// reopens finished 子代理. Losing race 到 another follow-up
			// 那 reopened it first means 它的 new 运行 takes 消息.
			for range 3 {
				if current, ok := c.dispatched.Get(key); ok {
					entry = current
				}
				if entry.stopping() {
					entry.awaitSettled(10 * time.Second)
				}
				if entry.awaitDeliverable(ctx) {
					agent := entry.runner()
					agentCall, err := c.subAgentCall(agent, entry.SessionID, params.Message)
					if err != nil {
						return fantasy.NewTextErrorResponse("Failed to deliver message: " + err.Error()), nil
					}
					agentCall.QueueOnly = true
					_, err = agent.Run(context.WithoutCancel(ctx), agentCall)
					if err == nil {
						return fantasy.WithResponseMetadata(fantasy.NewTextResponse(fmt.Sprintf(
							"Delivered to %q. It will answer in its report.",
							label,
						)+reportArrivesOnItsOwn), AgentSendResponseMetadata{Label: label}), nil
					}
					if !errors.Is(err, ErrSessionIdle) {
						return fantasy.NewTextErrorResponse("Failed to deliver message: " + err.Error()), nil
					}
					entry.awaitSettled(10 * time.Second)
				}
				if c.reopen(entry, params.Message, nil) {
					return fantasy.WithResponseMetadata(fantasy.NewTextResponse(fmt.Sprintf(
						"Delivered to %q, which had finished. It will answer in a new report.",
						label,
					)+reportArrivesOnItsOwn), AgentSendResponseMetadata{Label: label, Reopened: true}), nil
				}
			}
			if ctx.Err() != nil {
				return fantasy.ToolResponse{}, ctx.Err()
			}
			return fantasy.NewTextErrorResponse(fmt.Sprintf(
				"subagent %q is still starting up and could not take the message yet; try again shortly",
				label,
			)), nil
		},
	)
}

// agentStopTool stops running detached 子代理. Its 会话 是 kept, 所以
// later agent_send resumes it 用 它的 work still 在 上下文.
func (c *coordinator) agentStopTool() fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		AgentStopToolName,
		agentStopToolDescription,
		func(ctx context.Context, params AgentStopParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			label := strings.TrimSpace(params.Label)
			if label == "" {
				return fantasy.NewTextErrorResponse("label is required"), nil
			}
			parentSessionID := tools.GetSessionFromContext(ctx)
			if parentSessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}

			entry, ok := c.dispatched.Get(dispatchKey(parentSessionID, label))
			if !ok || !entry.running() {
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"subagent %q is not running; running: %s",
					label, c.runningDispatchLabels(parentSessionID),
				)), nil
			}
			reason := strings.TrimSpace(params.Reason)
			if reason == "" {
				reason = "stopped by the main agent"
			}
			entry.stop(reason)
			if !entry.awaitSettled(10 * time.Second) {
				return fantasy.NewTextResponse(fmt.Sprintf("Stopping subagent %q.", label)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Stopped subagent %q.", label)), nil
		},
	)
}

// keptFinishedDispatches 是 how many finished subagents stay 在 registry.
// Each pins 它的 agent; forgotten one 是 rebuilt 从 transcript 如果 it
// 是 messaged again.
const keptFinishedDispatches = 8

// pruneDispatched forgets all 但 most recently finished subagents.
func (c *coordinator) pruneDispatched() {
	c.dispatchMu.Lock()
	defer c.dispatchMu.Unlock()
	type finished struct {
		key   string
		ended time.Time
	}
	var idle []finished
	for key, entry := range c.dispatched.Seq2() {
		if snap := entry.snapshot(); snap.State != dispatchRunning {
			idle = append(idle, finished{key, snap.Ended})
		}
	}
	if len(idle) <= keptFinishedDispatches {
		return
	}
	slices.SortFunc(idle, func(a, b finished) int { return b.ended.Compare(a.ended) })
	for _, f := range idle[keptFinishedDispatches:] {
		c.dispatched.Del(f.key)
	}
}

// runningDispatchLabels lists subagents still running 为 会话.
func (c *coordinator) runningDispatchLabels(parentSessionID string) string {
	var labels []string
	for _, entry := range c.dispatched.Seq2() {
		if entry.ParentSessionID == parentSessionID && entry.running() {
			labels = append(labels, entry.Label)
		}
	}
	if len(labels) == 0 {
		return "none"
	}
	sort.Strings(labels)
	return strings.Join(labels, ", ")
}

// dispatchRunProvider reads 提供方 子代理 ran 在 从 它的 own
// transcript, 为 派发 whose 结果 never landed. 一个 读取 failure 是
// 错误, 不 "never ran", 所以 resume cannot pick wrong 提供方.
func (c *coordinator) dispatchRunProvider(ctx context.Context, sessionID string) (string, error) {
	msgs, err := c.messages.List(ctx, sessionID)
	if err != nil {
		return "", err
	}
	for _, msg := range msgs {
		if msg.Role == message.Assistant && msg.Provider != "" {
			return msg.Provider, nil
		}
	}
	return "", nil
}

// rehydrateDispatch rebuilds 子代理's registry 条目 从 what 是 在
// disk. registry 是 在-memory, 所以 restart empties it, 但 nothing 那
// matters was 在 memory 到 begin 用: parent's transcript records
// 派发 调用, child 会话 ID derives 从 那 调用's IDs, 和
// child 会话 holds whole conversation. 条目 是 reconstructed 作为
// finished 子代理, which 是 what it 是, 所以 caller reopens it.
func (c *coordinator) rehydrateDispatch(ctx context.Context, parentSessionID, label string) (*dispatchedAgent, error) {
	messages, err := c.messages.List(ctx, parentSessionID)
	if err != nil {
		return nil, err
	}
	record, ok := dispatchHistory(messages).byLabel[label]
	if !ok {
		return nil, fmt.Errorf("no dispatch recorded for label %q", label)
	}
	// 一个 backgrounded search ran 在 fetch 子代理, 不 task agent, 所以
	// it cannot 是 rebuilt 从 task variants below. Its 会话 是
	// kept, 但 reopening it after restart 是 不 supported.
	if record.Tool == tools.AgenticFetchToolName {
		return nil, fmt.Errorf("subagent %q was a search and cannot be resumed", label)
	}

	// Rehydration replays access level recorded 在 transcript, 所以
	// both variants have 到 是 available 到 look it up. 一个 读取-只 parent
	// could never have recorded 写入 派发 在 first place.
	agents, err := c.taskAgents(ctx, false)
	if err != nil {
		return nil, err
	}
	agent, ok := agents[record.Access]
	if !ok {
		return nil, fmt.Errorf("unknown access %q", record.Access)
	}

	// An override resumes 作为 it was asked 为, re-resolved against live
	// 配置; 如果 it no longer serves, 子代理 是 不 quietly moved 到
	// 会话 默认.
	if record.Model != "" || record.Reasoning != "" {
		params := AgentDispatchParams{Model: record.Model, Provider: record.Provider, Reasoning: record.Reasoning}
		parent := Model{}
		if params.Model != "" && params.Provider == "" {
			// 结果 never landed: use 提供方 it ran 在, else 会话's.
			provider, err := c.dispatchRunProvider(ctx, c.sessions.CreateAgentToolSessionID(record.MessageID, record.CallID))
			if err != nil {
				return nil, fmt.Errorf("subagent %q cannot be resumed: reading its transcript failed: %w", label, err)
			}
			params.Provider = provider
			if provider == "" {
				parent = c.mainModel()
			}
		}
		overrideAgent, _, _, refusal := c.overrideDispatchAgent(ctx, record.Access, params, parent)
		if refusal != "" {
			return nil, fmt.Errorf("subagent %q cannot be resumed on its model override: %s", label, refusal)
		}
		agent = overrideAgent
	}

	sessionID := c.sessions.CreateAgentToolSessionID(record.MessageID, record.CallID)
	child, err := c.sessions.Get(ctx, sessionID)
	if err != nil {
		// 派发 was recorded 但 它的 会话 是 gone, 所以 there 是
		// no history 到 reopen into.
		return nil, err
	}

	entry := newDispatchedAgent(label, record.Access, sessionID, parentSessionID, agent)
	// Seed roll-up 用 what child has already cost. parent was
	// billed 为 all 的 it during original 运行, 所以 starting 从 zero
	// would bill the whole total a second time.
	entry.rolledUpCost.Store(math.Float64bits(child.Cost))

	c.dispatchMu.Lock()
	defer c.dispatchMu.Unlock()
	if existing, ok := c.dispatched.Get(dispatchKey(parentSessionID, label)); ok {
		return existing, nil
	}
	c.dispatched.Set(dispatchKey(parentSessionID, label), entry)
	return entry, nil
}

// stopAllDispatched stops every running 子代理 和 waits, up 到 超时,
// 为 them 到 unwind.
func (c *coordinator) stopAllDispatched(reason string, timeout time.Duration) {
	var stopped []*dispatchedAgent
	for _, entry := range c.dispatched.Seq2() {
		if entry.stop(reason) {
			stopped = append(stopped, entry)
		}
	}
	deadline := time.Now().Add(timeout)
	for _, entry := range stopped {
		if !entry.awaitSettled(time.Until(deadline)) {
			slog.Warn("Subagent still running after being stopped", "label", entry.Label)
		}
	}
}

// trackSubSession marks 子代理 会话 作为 live until returned func
// 是 called. coordinator's busy check consults 这, 所以 reader settling
// half-finished sessions never mistakes working 子代理 为 dead one.
func (c *coordinator) trackSubSession(sessionID string) func() {
	c.subSessionsMu.Lock()
	if c.subSessions == nil {
		c.subSessions = map[string]int{}
	}
	c.subSessions[sessionID]++
	c.subSessionsMu.Unlock()
	return func() {
		c.subSessionsMu.Lock()
		defer c.subSessionsMu.Unlock()
		if c.subSessions[sessionID]--; c.subSessions[sessionID] <= 0 {
			delete(c.subSessions, sessionID)
		}
	}
}

func (c *coordinator) subSessionBusy(sessionID string) bool {
	c.subSessionsMu.Lock()
	defer c.subSessionsMu.Unlock()
	return c.subSessions[sessionID] > 0
}
