package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
)

//go:embed templates/agent_status_tool.md
var agentStatusToolDescription string

const AgentStatusToolName = "agent_status"

type AgentStatusParams struct {
	Label string `json:"label,omitempty" description:"The name of one subagent, to show only its detail: its task, recent tool calls, and latest output. Omit to list every subagent in this session"`
}

// dispatchRecord 是 what parent transcript says about one 子代理 label:
// latest 派发 under it, 和 whether 那 派发 是 still waiting
// 在 报告.
type dispatchRecord struct {
	Label     string
	Access    string
	Task      string
	MessageID string
	CallID    string
	// 工具 是 工具调用 那 started 这 运行: agent_dispatch, 或
	// agent 或 agentic_fetch 工具 当 blocking 调用 was moved 到
	// background. 它 picks which agent follow-up reopens into.
	Tool string
	// 模型 override 作为 asked 为; 结果 fills 在 提供方.
	Model     string
	Provider  string
	Reasoning string
	// Open 是 true while latest 派发 或 follow-up has no 报告.
	Open       bool
	LastReport *message.SubAgentReport
	prev       *dispatchRecord
}

// dispatchRecords indexes transcript's dispatches 通过 label, keeping
// order labels were first dispatched 在.
type dispatchRecords struct {
	byLabel map[string]*dispatchRecord
	order   []string
}

// link files record 作为 latest under label.
func (r *dispatchRecords) link(record *dispatchRecord, label string) {
	record.Label = label
	record.prev = r.byLabel[label]
	if record.prev == nil {
		r.order = append(r.order, label)
	}
	r.byLabel[label] = record
}

// unlink takes record back out, 所以 它的 label means what it did before.
func (r *dispatchRecords) unlink(record *dispatchRecord) {
	if r.byLabel[record.Label] != record {
		return
	}
	if record.prev != nil {
		r.byLabel[record.Label] = record.prev
		return
	}
	delete(r.byLabel, record.Label)
	r.order = slices.DeleteFunc(r.order, func(label string) bool { return label == record.Label })
}

// dispatchHistory replays parent transcript 在 order 到 find, 为 each
// label, 它的 latest 派发 和 whether it has reported since. 一个 label can
// 是 reused across long 会话, 和 finished 子代理 can 是 reopened
// 通过 follow-up, 所以 只 replaying events 在 order gets 这 right.
func dispatchHistory(msgs []message.Message) dispatchRecords {
	records := dispatchRecords{byLabel: map[string]*dispatchRecord{}}
	byCallID := map[string]*dispatchRecord{}
	// bgCallMsg records 消息 blocking 子代理 调用 lives 在, 所以
	// later "moved 到 background" 结果 can derive 它的 child 会话 从
	// same IDs 运行 used.
	bgCallMsg := map[string]string{}
	for _, msg := range msgs {
		for _, call := range msg.ToolCalls() {
			switch call.Name {
			case AgentToolName, tools.AgenticFetchToolName:
				bgCallMsg[call.ID] = msg.ID
				continue
			case AgentDispatchToolName:
			default:
				continue
			}
			var params AgentDispatchParams
			if json.Unmarshal([]byte(call.Input), &params) != nil {
				continue
			}
			label := strings.TrimSpace(params.Label)
			if label == "" {
				continue
			}
			record := &dispatchRecord{
				Label:     label,
				Access:    accessOrRead(params.Access),
				Task:      params.Prompt,
				MessageID: msg.ID,
				CallID:    call.ID,
				Tool:      AgentDispatchToolName,
				Model:     strings.TrimSpace(params.Model),
				Provider:  strings.TrimSpace(params.Provider),
				Reasoning: strings.TrimSpace(params.Reasoning),
				Open:      true,
			}
			// Filed under label asked 为 until 结果 names it.
			records.link(record, label)
			byCallID[call.ID] = record
		}
		for _, result := range msg.ToolResults() {
			switch result.Name {
			case AgentDispatchToolName:
				record, ok := byCallID[result.ToolCallID]
				if !ok {
					continue
				}
				// 一个 refused 派发 never started anything, 所以 label
				// still means whatever it meant before.
				if result.IsError {
					records.unlink(record)
					continue
				}
				// 一个 started 派发 records name it gave 子代理
				// 和 模型 override it resolved, 所以 restart resumes
				// 在 提供方 派发 actually used.
				var meta AgentDispatchResponseMetadata
				if result.Metadata != "" && json.Unmarshal([]byte(result.Metadata), &meta) == nil {
					record.Model = meta.Model
					record.Provider = meta.Provider
					record.Reasoning = meta.Reasoning
				}
				if name := strings.TrimSpace(meta.Label); name != "" && name != record.Label {
					records.unlink(record)
					records.link(record, name)
				}
			case AgentSendToolName:
				var meta AgentSendResponseMetadata
				if result.Metadata == "" || json.Unmarshal([]byte(result.Metadata), &meta) != nil || !meta.Reopened {
					continue
				}
				if record, ok := records.byLabel[meta.Label]; ok {
					record.Open = true
					if meta.Model != "" || meta.Reasoning != "" {
						record.Model, record.Provider, record.Reasoning = meta.Model, meta.Provider, meta.Reasoning
					}
				}
			case AgentToolName, tools.AgenticFetchToolName:
				// 一个 blocking 子代理 那 outran threshold 和 was
				// moved 到 background behaves 从 here 在 exactly
				// like 派发: 它的 结果 carries label it took,
				// 和 它的 报告 lands later under 那 label.
				var meta AgentBackgroundMetadata
				if result.Metadata == "" || json.Unmarshal([]byte(result.Metadata), &meta) != nil || !meta.Backgrounded {
					continue
				}
				label := strings.TrimSpace(meta.Label)
				if label == "" {
					continue
				}
				records.link(&dispatchRecord{
					Access:    accessOrRead(meta.Access),
					Task:      meta.Task,
					MessageID: bgCallMsg[result.ToolCallID],
					CallID:    result.ToolCallID,
					Tool:      result.Name,
					Open:      true,
				}, label)
			}
		}
		for _, report := range msg.SubAgentReports() {
			if record, ok := records.byLabel[report.Label]; ok {
				record.Open = false
				record.LastReport = &report
			}
		}
	}
	return records
}

// reconcileDispatches tells parent 会话 about subagents 那 were cut
// off 当 Crush last exited. transcript shows them dispatched 和 never
// reporting, 和 nothing 在 这 process 是 running them, 所以 without 这
// parent would wait forever 在 报告 那 是 never coming.
//
// 它 runs once per 会话 per process, 只 while 会话 是 空闲, 所以
// 报告 lands between turns rather than 在 middle 的 one.
func (c *coordinator) reconcileDispatches(ctx context.Context, agent SessionAgent, sessionID string) {
	if _, done := c.reconciled.Load(sessionID); done || agent.HasActiveTurn(sessionID) {
		return
	}
	msgs, err := c.messages.List(ctx, sessionID)
	if err != nil {
		return
	}
	c.reconciled.Store(sessionID, struct{}{})

	records := dispatchHistory(msgs)
	for _, label := range records.order {
		record, ok := records.byLabel[label]
		if !ok || !record.Open {
			continue
		}
		if _, live := c.dispatched.Get(dispatchKey(sessionID, label)); live {
			continue
		}
		c.recordDispatchReport(sessionID, message.SubAgentReport{
			Label:  label,
			Output: "Interrupted: Crush exited while this subagent was running. Its session is kept; agent_send resumes it with its work in context.",
			Failed: true,
		})
	}
}

// agentStatusTool lets main agent see what 它的 subagents 是 doing.
func (c *coordinator) agentStatusTool() fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		AgentStatusToolName,
		agentStatusToolDescription,
		func(ctx context.Context, params AgentStatusParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			parentSessionID := tools.GetSessionFromContext(ctx)
			if parentSessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}
			msgs, err := c.messages.List(ctx, parentSessionID)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("read session: %w", err)
			}
			records := dispatchHistory(msgs)

			if label := strings.TrimSpace(params.Label); label != "" {
				record, ok := records.byLabel[label]
				if !ok {
					return fantasy.NewTextErrorResponse(fmt.Sprintf(
						"no subagent %q; known: %s",
						label, knownLabels(records),
					)), nil
				}
				text, sum := c.describeDispatch(ctx, parentSessionID, record, true)
				if sum.State == SubagentRunning {
					text += "\n\n" + stillRunningNote
				}
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(text),
					AgentStatusResponseMetadata{Agents: []AgentStatusSummary{sum}}), nil
			}

			if len(records.order) == 0 {
				return fantasy.NewTextResponse("No subagents dispatched."), nil
			}
			var (
				lines   []string
				meta    AgentStatusResponseMetadata
				running bool
			)
			for _, label := range records.order {
				record, ok := records.byLabel[label]
				if !ok {
					continue
				}
				text, sum := c.describeDispatch(ctx, parentSessionID, record, false)
				lines = append(lines, text)
				meta.Agents = append(meta.Agents, sum)
				running = running || sum.State == SubagentRunning
			}
			if running {
				lines = append(lines, "", stillRunningNote)
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(strings.Join(lines, "\n")), meta), nil
		},
	)
}

// stillRunningNote ends status 结果 那 shows 子代理 still 在 work.
// 一个 模型 那 has 运行 out 的 other work reaches 为 agent_status 作为 way
// 到 wait, 和 calls it again 在 every answer 那 says "running".
const stillRunningNote = "Running subagents report on their own and wake you when they do. " +
	"If nothing else needs doing, end your turn now rather than checking again."

func knownLabels(records dispatchRecords) string {
	var labels []string
	for _, label := range records.order {
		if _, ok := records.byLabel[label]; ok {
			labels = append(labels, label)
		}
	}
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, ", ")
}

// AgentStatusResponseMetadata lets UI draw agent_status results 作为 rows.
type AgentStatusResponseMetadata struct {
	Agents []AgentStatusSummary `json:"agents"`
}

// AgentStatusSummary 是 one 子代理's state, 作为 agent_status reports it.
type AgentStatusSummary struct {
	Label     string        `json:"label"`
	State     SubagentState `json:"state"`
	Detail    string        `json:"detail,omitempty"` // Stop reason.
	Timing    string        `json:"timing,omitempty"` // "for 2m" or "5m ago".
	Access    string        `json:"access"`
	ToolCalls int           `json:"tool_calls"`
	Cost      float64       `json:"cost,omitempty"`
}

// summarizeDispatch reads one 子代理's state. registry 是
// authority 在 子代理 这 process 是 running; transcript covers
// rest, including those cut off 通过 restart.
func (c *coordinator) summarizeDispatch(parentSessionID string, record *dispatchRecord, activity subAgentProgress) AgentStatusSummary {
	sum := AgentStatusSummary{
		Label:     record.Label,
		State:     SubagentFinished,
		Access:    record.Access,
		ToolCalls: activity.toolCalls,
		Cost:      activity.cost,
	}
	if record.LastReport != nil && record.LastReport.Failed {
		sum.State = SubagentFailed
	}
	childID := c.sessions.CreateAgentToolSessionID(record.MessageID, record.CallID)
	entry, ok := c.dispatched.Get(dispatchKey(parentSessionID, record.Label))
	if !ok || entry.SessionID != childID {
		if record.Open {
			sum.State, sum.Detail = SubagentInterrupted, "Crush exited"
		}
		return sum
	}
	snap := entry.snapshot()
	sum.State = snap.State.reported()
	switch {
	case snap.State == dispatchRunning:
		if snap.StopReason != "" {
			sum.State = SubagentStopping
		}
		sum.Timing = "for " + formatElapsed(time.Since(snap.Started))
	case !snap.Ended.IsZero():
		sum.Timing = formatElapsed(time.Since(snap.Ended)) + " ago"
	}
	if snap.State == dispatchStopped {
		sum.Detail = snap.StopReason
	}
	return sum
}

// describeDispatch renders one 子代理's status 为 模型.
func (c *coordinator) describeDispatch(ctx context.Context, parentSessionID string, record *dispatchRecord, detail bool) (string, AgentStatusSummary) {
	activity := c.subAgentActivity(ctx, c.sessions.CreateAgentToolSessionID(record.MessageID, record.CallID))
	sum := c.summarizeDispatch(parentSessionID, record, activity)

	var sb strings.Builder
	fmt.Fprintf(&sb, "- %s: %s", sum.Label, sum.State)
	if sum.Detail != "" {
		fmt.Fprintf(&sb, " (%s)", sum.Detail)
	}
	if sum.Timing != "" {
		fmt.Fprintf(&sb, " %s", sum.Timing)
	}
	fmt.Fprintf(&sb, ", %s access, %d tool calls", sum.Access, sum.ToolCalls)
	if sum.Cost > 0 {
		fmt.Fprintf(&sb, ", $%.2f", sum.Cost)
	}
	if activity.lastCall != "" {
		fmt.Fprintf(&sb, "\n  latest: %s", activity.lastCall)
	}
	if !detail {
		return sb.String(), sum
	}

	fmt.Fprintf(&sb, "\n\nTask:\n%s", truncateForStatus(record.Task, 1500))
	if len(activity.recentCalls) > 0 {
		sb.WriteString("\n\nRecent tool calls, oldest first:")
		for _, line := range activity.recentCalls {
			fmt.Fprintf(&sb, "\n- %s", line)
		}
	}
	if activity.lastText != "" {
		fmt.Fprintf(&sb, "\n\nLatest output:\n%s", truncateForStatus(activity.lastText, 2000))
	}
	if record.LastReport != nil {
		fmt.Fprintf(&sb, "\n\nLast report:\n%s", truncateForStatus(record.LastReport.Output, 2000))
	}
	return sb.String(), sum
}

// subAgentProgress summarizes what 子代理 会话 has done 所以 far.
type subAgentProgress struct {
	toolCalls   int
	lastCall    string
	recentCalls []string
	lastText    string
	cost        float64
}

func (c *coordinator) subAgentActivity(ctx context.Context, sessionID string) subAgentProgress {
	var progress subAgentProgress
	if session, err := c.sessions.Get(ctx, sessionID); err == nil {
		progress.cost = session.Cost
	}
	msgs, err := c.messages.List(ctx, sessionID)
	if err != nil {
		return progress
	}

	results := map[string]message.ToolResult{}
	for _, msg := range msgs {
		for _, result := range msg.ToolResults() {
			results[result.ToolCallID] = result
		}
	}

	const recent = 8
	var calls []message.ToolCall
	for _, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}
		if text := strings.TrimSpace(msg.Content().Text); text != "" {
			progress.lastText = text
		}
		calls = append(calls, msg.ToolCalls()...)
	}
	progress.toolCalls = len(calls)
	// Only calls shown 是 formatted; formatting every 调用 made each
	// status check cost 作为 much 作为 子代理's whole history.
	for _, call := range calls[max(0, len(calls)-recent):] {
		outcome := "running"
		if result, ok := results[call.ID]; ok {
			outcome = "ok"
			if result.IsError {
				outcome = "error"
			}
		}
		line := call.Name
		if input := truncateForStatus(compactJSON(call.Input), 120); input != "" {
			line += " " + input
		}
		progress.recentCalls = append(progress.recentCalls, fmt.Sprintf("%s [%s]", line, outcome))
	}
	if n := len(progress.recentCalls); n > 0 {
		progress.lastCall = progress.recentCalls[n-1]
	}
	return progress
}

// compactJSON squeezes 工具 input onto one line.
func compactJSON(input string) string {
	var v any
	if json.Unmarshal([]byte(input), &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return strings.Join(strings.Fields(input), " ")
}

func truncateForStatus(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return strings.TrimSpace(s[:limit]) + "..."
}

func formatElapsed(d time.Duration) string {
	return d.Round(time.Second).String()
}

// DispatchState 是 where latest 派发 under label stands, 作为 far 作为
// parent transcript 可以 tell.
type DispatchState struct {
	// CallID 是 agent_dispatch 工具调用 那 started it.
	CallID string
	// Open 是 true while it has 不 reported since it was dispatched 或
	// last reopened. An open 派发 是 either still running 或 was cut
	// off; 只 backend can say which.
	Open bool
}

// LatestDispatches returns, 为 each 子代理 label 在 parent transcript,
// state 的 它的 latest 派发.
func LatestDispatches(msgs []*message.Message) map[string]DispatchState {
	plain := make([]message.Message, 0, len(msgs))
	for _, msg := range msgs {
		if msg != nil {
			plain = append(plain, *msg)
		}
	}
	records := dispatchHistory(plain)
	states := make(map[string]DispatchState, len(records.byLabel))
	for label, record := range records.byLabel {
		states[label] = DispatchState{CallID: record.CallID, Open: record.Open}
	}
	return states
}
