// Package agent 是 Crush AI agent 的核心编排层。
//
// 从产物（用户收到的一条回复）往上游反推，本文件的核心链路是：
//
//	sendChannelReply(channelReplyText(currentAssistant))   ← 回复出口（channelreply.go）
//	  ↑ currentAssistant 由 Stream 回调逐段写入            ← agent.go 本文件的 OnTextDelta
//	  ↑ agent.Stream(AgentStreamCall{Prompt, Files, Messages})
//	  ↑ PrepareStep 里创建空 assistant 消息、重排工具结果
//	  ↑ history, files := preparePrompt(msgs, attachments)  ← 组装发给上游的 Messages/Files
//	  ↑ msgs := getSessionMessages(session)                 ← 从摘要点读历史
//	  ↑ createUserMessage(call, modelContext)               ← 把本轮输入+附件落库
//
// Run() 是这条链的主函数；preparePrompt() 决定「模型每轮看到什么」；
// createUserMessage() 决定「输入如何持久化」；summarize() 决定「历史如何截断」。
// 排队、取消、渠道回复、TurnEnd 钩子都围绕同一条链做状态保护。
//
// 它提供基于 session 的 AI agent 功能，管理对话、工具执行和消息处理。
// 它协调语言模型、消息、session 和工具之间的交互，同时处理
// 自动摘要、排队和 token 管理等功能。
package agent

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	"charm.land/lipgloss/v2"
	"charm.land/tone"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/stringext"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/ansi"
)

const (
	DefaultSessionName = "Untitled Session"

	// 自动摘要阈值相关的常量
	largeContextWindowThreshold = 200_000
	largeContextWindowBuffer    = 20_000
	smallContextWindowRatio     = 0.2
)

var userAgent = fmt.Sprintf("Charm-Crush/%s (https://charm.land/crush)", version.Version)

// providerStoppedTurnMessage 是当 provider 自行结束响应（fantasy 的 error 或其他
// finish 原因）而非正常完成时，记录下来的横幅标题。细节在每轮单独填充。
const providerStoppedTurnMessage = "Provider stopped the response"

// 用于从生成的标题里去掉 <think> 标签。
var (
	thinkTagRegex       = regexp.MustCompile(`(?s)<think>.*?</think>`)
	orphanThinkTagRegex = regexp.MustCompile(`</?think>`)
)

type SessionAgentCall struct {
	SessionID string
	// RunID 非空时，是调用方提供的关联标识，会随本轮发出的 notify.RunComplete
	// 事件回传。当调用被排到繁忙 session 后面时它会被保留，这样排队轮次的
	// 终态事件仍能被原始调用方识别。需要可靠完成契约的调用方（例如对可能繁忙的
	// session 执行 `crush run`）必须设置它；当多个并发轮次共享同一 session 时，
	// 单靠 SessionID 是有歧义的。
	RunID             string
	Channel           string
	HiddenUserMessage bool
	// QueueOnly 表示只把调用投递给一个正在进行的轮次；如果 session 空闲，
	// Run 会返回 ErrSessionIdle，而不会新起一轮。
	QueueOnly bool
	Prompt    string
	// Report 是本调用要投递的子 agent 报告。它只在投递时才写入转录，
	// 因此正好落在模型会读到的位置。Prompt 仍然是模型据此读取的内容。
	Report *message.SubAgentReport
	// Model 是下面这些选项所针对的主模型。Run 在入口处会把未设置的模型
	// 固定为当前模型。
	Model            Model
	ProviderOptions  fantasy.ProviderOptions
	Attachments      []message.Attachment
	MaxOutputTokens  int64
	Temperature      *float64
	TopP             *float64
	TopK             *int64
	FrequencyPenalty *float64
	PresencePenalty  *float64
	NonInteractive   bool

	// HostContext 是发送 prompt 时宿主应用正在查看的内容。模型只在本次
	// prompt 里看到它；它不会随消息一起存储。
	HostContext proto.HostContext
	// OnComplete 非空时，会替换默认的 RunComplete 发布路径：内部 Run 把终态
	// 负载交给这个回调，而不是发到 RunComplete 总线上。协调器用这个钩子把
	// 「未授权 → 重新认证 → 重试」整条链合并成单个用户可见的终态事件，
	// 这样非交互客户端（如 `crush run`）就不会在成功重试之前因为一个过期的
	// 失败 RunComplete 而退出。把调用排到繁忙 session 后面时，会刻意去掉它
	// （见 Run）：等排队的递归被排空时，最初的 coordinator.Run 早已返回，
	// 因此退回默认的总线发布能让事件继续被订阅者看到。
	OnComplete func(notify.RunComplete)
	// Accepted 非空时，是调用被派发到 goroutine 之前（client/server 的
	// fire-and-forget 路径）由 BeginAccepted 取得的受理占位。一旦确定了
	// accepted ->（入口取消 | 排队 | 活跃）的状态转移，Run 就会在
	// dispatchMu[SessionID] 下消费它。为 nil 时（进程内/本地调用方，如
	// AppWorkspace）行为不变，不做受理跟踪。
	Accepted *AcceptedRun
	// acceptSeq 携带的是：本调用被入队且 Accepted 句柄被剥离后，产生它的那个
	// 句柄的受理序号。排空队列的路径会把它和 session 的取消标记比较，从而在
	// 取消之前入队的后续调用会被丢弃、取消之后入队的则保留。0 表示未跟踪
	// （没有受理占位的进程内入队），排空路径把它视为被任何现存标记覆盖，
	// 以保持引入序号之前的行为。
	acceptSeq uint64
	// OnAuthRefresh 非空时，当流因认证错误（HTTP 401）失败，fantasy 会调用它。
	// 回调应刷新凭据并在成功时返回 nil，此时 fantasy 会透明地重试该流。
	// 返回错误则直接暴露原始认证错误，不做重试。
	OnAuthRefresh func(ctx context.Context, err *fantasy.ProviderError) error
	// channelMeta 保存发起 channel 来源轮次时 <channel> 元素的属性，
	// 在 Run 入口处解析一次。它会随自动摘要后的续轮保留下来——那轮 Prompt
	// 被改写、不再携带该元素，因此当长的 channel 轮次被摘要时，回复目标不会丢。
	channelMeta map[string]string
}

// filterToolsForChannel scopes 工具 list f或 turn. 一个 渠道-originated
// turn (渠道 != "") sees 只 originating 渠道 server's tools plus
// all non-渠道 tools — 模型's reach 是 restricted 到 渠道 it
// is replying through, 所以 it cannot accidentally send vidifferent
// messaging backend. 一个 local turn (渠道 == "") keeps every 工具,
// including 渠道 server tools, 所以 用户 在 TUI cstill ask the
// agent 到 send 消息 through Signal 或 any other enabled 渠道.
func filterToolsForChannel(agentTools []fantasy.AgentTool, channel string, states map[string]mcp.ClientInfo) []fantasy.AgentTool {
	if channel == "" {
		return agentTools
	}
	filtered := make([]fantasy.AgentTool, 0, len(agentTools))
	for _, agentTool := range agentTools {
		mcpTool, ok := agentTool.(interface{ MCP() string })
		if !ok {
			filtered = append(filtered, agentTool)
			continue
		}
		state, found := states[mcpTool.MCP()]
		if !found || !state.Channel || channel == mcpTool.MCP() {
			filtered = append(filtered, agentTool)
		}
	}
	return filtered
}

// ErrSessionIdle 是 returned f或 QueueOnly 调用 当 no turn 是 活跃.
var ErrSessionIdle = errors.New("session is idle")

type SessionAgent interface {
	Run(context.Context, SessionAgentCall) (*fantasy.AgentResult, error)
	BeginAccepted(sessionID string) *AcceptedRun
	SetTools(tools []fantasy.AgentTool)
	SetSystemPrompt(systemPrompt string)
	Cancel(sessionID string)
	CancelAll()
	HasActiveTurn(sessionID string) bool
	HasPendingWork(sessionID string) bool
	IsBusy() bool
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	ClearQueue(sessionID string)
	AddNote(sessionID, text string)
	Summarize(context.Context, string, Model, fantasy.ProviderOptions, func(context.Context, *fantasy.ProviderError) error) error
	Model() Model
	GenerateTitle(ctx context.Context, sessionID, userPrompt string)
}

type Model struct {
	Model      fantasy.LanguageModel
	CatwalkCfg catwalk.Model
	ModelCfg   config.SelectedModel
	FlatRate   bool
	// OAuth 是 true 当 提供方 signs 在 通过 OAuth, 所以 rejected
	// credential calls f或 new sign-in rather th配置 change.
	OAuth bool
	// ProviderName 是 提供方's display name, such as "GitHub Copilot".
	ProviderName string
}

// activeCancel wraps 上下文.CancelFunc 用 unique 指针 identity.
// 指针 是 used f或 compare-and-删除 在 派发 completion path:
// 当 finishing 运行's deferred 清理 fires, it must 只 remove its own
// 条目 — 不 newer 运行's 条目 那 was installed 在 窗口 between
// explicit Del 和 function return.
type activeCancel struct {
	cancel context.CancelFunc
}

type sessionAgent struct {
	models             func() ModelSet
	systemPromptPrefix *csync.Value[string]
	systemPrompt       *csync.Value[string]
	tools              *csync.Slice[fantasy.AgentTool]

	isSubAgent bool
	sessions   session.Service
	messages   message.Service
	// cfg backs 渠道 reply routing (配置 lookup + MCP 工具
	// invocation). Nil 在 tests 和 subagents 那 never see 渠道
	// 轮次；sendChannelReply 把 nil 视为「路由已禁用」。
	cfg                  *config.ConfigStore
	disableAutoSummarize bool
	// prompts 是 every 提示词's text as 读取 at startup; summarize and
	// generateTitle take theirs from it. Nil falls back 到 built-ins.
	prompts map[string]string
	// permissions answers whether 用户 是 being asked about 工具
	// calls at all. Nil 在 tests 和 anywhere 那 never needs 到 know.
	permissions permission.Service
	notify      pubsub.Publisher[notify.Notification]
	runComplete pubsub.Publisher[notify.RunComplete]
	hooks       *hooks.Runner

	messageQueue   *csync.Map[string, []SessionAgentCall]
	activeRequests *csync.Map[string, *activeCancel]
	// 备注 是 what host told agent since its last turn, by
	// 会话, 到 add 到 next 提示词. They 是 不 chat messages.
	notes *csync.Map[string, []string]
	// titles tracks in-flight 标题 generation 所以 CancelAll cdrain it.
	titles *titleTracker

	// dispatchMu holds per-会话 互斥锁 那 serializes the
	// accepted -> (取消-on-条目 | queued | 活跃) transition in
	// 运行 against concurrent 取消. 锁 是 held 只 during
	// brief handoff (no DB 或 LLM I/O under 锁).
	dispatchMu *csync.Map[string, *sync.Mutex]
	// acceptedRuns counts dispatched-but-not-yet-活跃 runs per
	// 会话. 一个 计数器 > 0 means dispatched 提示词 是 在 flight
	// 和 has 不 yet completed 派发 handoff 在 运行. Only
	// BeginAccepted increments it; 只 AcceptedRun.Close decrements
	// 它。
	acceptedRuns *csync.Map[string, int]
	// cancelMark records, per 会话, high-water 受理 序号: an
	// accepted handle 是 canceled 通过 it iff handle's 序号 是 at
	// 或 below 标记. 取消 raises 标记 到 latest 序号
	// assigned at 取消 time, 所以 single 取消 covers every 提示词
	// accepted-but-not-yet-活跃 then, while 提示词 accepted later
	// (higher 序号) 是 never poisoned. Absent 或 0 means no pending
	// 取消. 它 是 只 raised 通过 取消 当 acceptedRuns > 0, 所以 an
	// 空闲 Escape never records 标记.
	cancelMark *csync.Map[string, uint64]
	// dispatchMuCreate guards lazy creation 的 per-会话 entries in
	// dispatchMu 所以 two goroutines can't race 到 锁 different 互斥锁
	// instances f或 same 会话.
	dispatchMuCreate sync.Mutex
	// acceptedMu serializes increments/decrements 的 acceptedRuns and
	// assignment 的 受理 序号 numbers from acceptSeqGen. It
	// is separate from dispatchMu 所以 AcceptedRun.Close (which may 运行
	// while 运行 holds dispatchMu f或 same 会话) does not
	// deadlock 通过 re-entering 派发 锁.
	acceptedMu sync.Mutex
	// acceptSeqGen 是 monotonic source 的 受理 序号 numbers.
	// Each BeginAccepted increments it under acceptedMu 和 stamps the
	// returned handle, 所以 sequences strictly increase 在 受理 order
	// across agent. 取消 uses its current value as per-会话
	// high-water 标记.
	acceptSeqGen uint64
}

type SessionAgentOptions struct {
	// Models hands out agent's current models on every 调用, 所以 a
	// credential refresh reaches requests already 在 flight.
	Models               func() ModelSet
	SystemPromptPrefix   string
	SystemPrompt         string
	IsSubAgent           bool
	DisableAutoSummarize bool
	Sessions             session.Service
	Messages             message.Service
	Permissions          permission.Service
	Cfg                  *config.ConfigStore
	// Prompts 是 every 提示词's text, 读取 once at startup. Nil uses the
	// 内置实现。
	Prompts     map[string]string
	Tools       []fantasy.AgentTool
	Notify      pubsub.Publisher[notify.Notification]
	RunComplete pubsub.Publisher[notify.RunComplete]
	// Hooks runs lifecycle hooks f或 turns 和 压缩. Nil runs none.
	Hooks *hooks.Runner
	// titles 是 shared 通过 coordinator's agents 所以 one drain covers all.
	titles *titleTracker
}

func NewSessionAgent(
	opts SessionAgentOptions,
) SessionAgent {
	return &sessionAgent{
		models:               opts.Models,
		systemPromptPrefix:   csync.NewValue(opts.SystemPromptPrefix),
		systemPrompt:         csync.NewValue(opts.SystemPrompt),
		isSubAgent:           opts.IsSubAgent,
		sessions:             opts.Sessions,
		messages:             opts.Messages,
		cfg:                  opts.Cfg,
		prompts:              opts.Prompts,
		disableAutoSummarize: opts.DisableAutoSummarize,
		permissions:          opts.Permissions,
		tools:                csync.NewSliceFrom(opts.Tools),
		notify:               opts.Notify,
		runComplete:          opts.RunComplete,
		hooks:                opts.Hooks,
		messageQueue:         csync.NewMap[string, []SessionAgentCall](),
		notes:                csync.NewMap[string, []string](),
		activeRequests:       csync.NewMap[string, *activeCancel](),
		titles:               cmp.Or(opts.titles, new(titleTracker)),
		dispatchMu:           csync.NewMap[string, *sync.Mutex](),
		acceptedRuns:         csync.NewMap[string, int](),
		cancelMark:           csync.NewMap[string, uint64](),
	}
}

// retryBudget 返回本 agent 每次模型调用允许的重试次数（不含首次尝试）。
// 子 agent 跑的是后台工作，和标题、摘要一样使用较小的预算。
func (a *sessionAgent) retryBudget() int {
	if a.isSubAgent {
		return backgroundRetries
	}
	return mainConversationRetries
}

// AcceptedRun owns exactly one 受理 占位 taken by
// BeginAccepted. 它 是 只 carrier 的 受理-state across the
// backend.runAgent / Coordinator.运行 / sessionAgent.运行 layers: a
// 计数器 > 0 means dispatched 提示词 是 在 flight 和 has 不 yet
// completed 派发 handoff 在 运行. Close 是 只 way to
// release 占位 和 是 idempotent.
type AcceptedRun struct {
	agent     *sessionAgent
	sessionID string
	// seq 是 monotonic 受理 序号 stamped 通过 BeginAccepted. A
	// 取消 covers 这 handle iff seq 是 at 或 below 会话's
	// 取消 标记, 所以 handle accepted after 取消 (higher seq) is
	// never poisoned 通过 it.
	seq  uint64
	done atomic.Bool
}

// Close decrements 受理 计数器 f或 这 占位. 它 是 safe
// to 调用 multiple times; 只 first 调用 has effect.
func (r *AcceptedRun) Close() {
	if r == nil {
		return
	}
	if !r.done.CompareAndSwap(false, true) {
		return
	}
	r.agent.endAccepted(r.sessionID)
}

// SessionID exposes 会话 这 占位 是 f或 所以 运行 path
// 无需额外参数就能使用它。
func (r *AcceptedRun) SessionID() string {
	if r == nil {
		return ""
	}
	return r.sessionID
}

// BeginAccepted increments 受理 计数器 f或 sessionID 和 returns
// handle whose Close 是 只 way 到 decrement it. 它 是 only
// 条目 point 那 mutates acceptedRuns.
func (a *sessionAgent) BeginAccepted(sessionID string) *AcceptedRun {
	a.acceptedMu.Lock()
	defer a.acceptedMu.Unlock()
	count, _ := a.acceptedRuns.Get(sessionID)
	a.acceptedRuns.Set(sessionID, count+1)
	a.acceptSeqGen++
	return &AcceptedRun{agent: a, sessionID: sessionID, seq: a.acceptSeqGen}
}

// endAccepted decrements 受理 计数器 f或 sessionID. 它 是 only
// called viAcceptedRun.Close. 它 uses dedicated 锁 (not the
// per-会话 派发 互斥锁) 所以 it c运行 while 运行 holds dispatchMu
// f或 same 会话 without deadlocking.
//
// When count reaches zero 会话's 取消 标记 是 dropped: no
// accepted handle remains f或 it 到 cover, 和 any handle accepted later
// gets strictly higher 序号 那 标记 would 不 match anyway.
// Handles canceled on 条目 never reach RunComplete, 所以 这 是 only
// place 那 clears 标记 f或 all-canceled batch. Sibling handles
// covered 通过 same 标记 是 serialized on per-会话 派发
// 互斥锁 和 读取 标记 before they Close, 所以 这 never clears it out
// from under covered handle still waiting 到 enter 运行.
func (a *sessionAgent) endAccepted(sessionID string) {
	a.acceptedMu.Lock()
	defer a.acceptedMu.Unlock()
	count, ok := a.acceptedRuns.Get(sessionID)
	if !ok || count <= 1 {
		a.acceptedRuns.Del(sessionID)
		a.cancelMark.Del(sessionID)
		return
	}
	a.acceptedRuns.Set(sessionID, count-1)
}

// sessionMu returns per-会话 派发 互斥锁, creating it on first
// use. Creation 是 guarded 所以 concurrent callers always observe same
// 互斥锁 instance f或 given 会话.
func (a *sessionAgent) sessionMu(sessionID string) *sync.Mutex {
	if mu, ok := a.dispatchMu.Get(sessionID); ok {
		return mu
	}
	a.dispatchMuCreate.Lock()
	defer a.dispatchMuCreate.Unlock()
	if mu, ok := a.dispatchMu.Get(sessionID); ok {
		return mu
	}
	mu := &sync.Mutex{}
	a.dispatchMu.Set(sessionID, mu)
	return mu
}

// enqueueCall appends 调用 到 会话's 消息 队列. The
// OnComplete 钩子 是 stripped: caller 那 supplied it (typically
// coordinator.运行) has its own 重试/coalesce scope 那 ends 当 it
// returns, 所以 通过 time 队列 drains nobody 是 left 到 consume the
// buffered 终态 事件. recursive 运行 falls back 到 default
// 总线 发布, which 是 what existing subscribers expect f或 queued
// 轮次。
func (a *sessionAgent) enqueueCall(call SessionAgentCall) {
	existing, ok := a.messageQueue.Get(call.SessionID)
	if !ok {
		existing = []SessionAgentCall{}
	}
	queued := call
	if call.Accepted != nil {
		// Preserve 受理 序号 after handle 是 stripped so
		// 队列-drain paths ctell follow-up queued before a
		// 取消 (covered 通过 标记) from one queued after it.
		queued.acceptSeq = call.Accepted.seq
	}
	queued.OnComplete = nil
	queued.Accepted = nil
	// Once queued 调用 是 accepted: 如果 no step folds it in, it runs as
	// its own turn rather thbeing refused as 空闲.
	queued.QueueOnly = false
	a.messageQueue.Set(call.SessionID, insertQueued(existing, queued))
}

// insertQueued appends 调用, except 那 用户's own prompts go ahead of
// queued 子代理 reports: delivered together they 读取 as one 消息, and
// short line trailing long 报告 gets missed.
func insertQueued(queue []SessionAgentCall, call SessionAgentCall) []SessionAgentCall {
	at := len(queue)
	if call.Report == nil {
		for at > 0 && queue[at-1].Report != nil {
			at--
		}
	}
	return slices.Insert(queue, at, call)
}

// drainQueueForStep partitions 会话's queued calls f或 current
// streaming step under per-会话 派发 互斥锁 所以 filtering is
// atomic against concurrent 取消: canceledBySeq requires caller to
// hold 那 互斥锁, 和 evaluating it here (rather thafter unlocking)
// prevents 取消 recorded between drain 和 check from being
// 观察结果不一致。
//
// Calls covered 通过 pending 取消 是 dropped 和 returned 在 canceled
// f或 caller 到 settle (see settleDroppedCalls). Uncanceled calls without
// RunID 是 returned 在 fold 到 be folded into 活跃 turn,
// preserving existing follow-up behavior. Uncanceled calls 那 carry
// RunID 是 left 在 队列 所以 each runs as its own turn vithe
// recursive 运行 path 和 publishes its own RunComplete, giving every
// RunID-bearing 提示词 explicit lifecycle instead 的 being silently
// absorbed into another turn. fold 是 processed 通过 caller without the
// 锁 held.
func (a *sessionAgent) drainQueueForStep(sessionID string) (fold, canceled []SessionAgentCall) {
	dispatchLock := a.sessionMu(sessionID)
	dispatchLock.Lock()
	defer dispatchLock.Unlock()
	queuedCalls, _ := a.messageQueue.Get(sessionID)
	var keep []SessionAgentCall
	for _, queued := range queuedCalls {
		if a.canceledBySeq(sessionID, queued.acceptSeq) {
			canceled = append(canceled, queued)
			continue
		}
		if queued.RunID != "" {
			keep = append(keep, queued)
			continue
		}
		fold = append(fold, queued)
	}
	if len(keep) == 0 {
		a.messageQueue.Del(sessionID)
	} else {
		a.messageQueue.Set(sessionID, keep)
	}
	return fold, canceled
}

type foldedPromptInsertion struct {
	index    int
	messages []fantasy.Message
}

// applyFoldedPromptInsertions restores queued prompts 那 PrepareStep added
// 在更早的自主步骤中。Fantasy 会从它的初始提示词加模型/工具响应重建每一步，
// original 提示词 plus 模型/工具 responses, 所以 PrepareStep-only additions
// otherwise disappear on following step 和 invalidate 提示词 prefix.
func applyFoldedPromptInsertions(messages []fantasy.Message, insertions []foldedPromptInsertion) []fantasy.Message {
	if len(insertions) == 0 {
		return messages
	}

	extra := 0
	for _, insertion := range insertions {
		extra += len(insertion.messages)
	}
	result := make([]fantasy.Message, 0, len(messages)+extra)
	start := 0
	for _, insertion := range insertions {
		index := min(max(insertion.index, start), len(messages))
		result = append(result, messages[start:index]...)
		result = append(result, insertion.messages...)
		start = index
	}
	result = append(result, messages[start:]...)
	return result
}

// settleDroppedCalls 结算那些被移除但未运行的排队调用。
// 子代理 报告 是 written 到 transcript, where 模型 reads it
// next turn, 所以 cancelling turn never loses one. Each 调用 那 carries a
// RunID gets 终态 cancelled RunComplete. 一个 queued 提示词 removed
// from 队列 without ever running — covered 通过 pending 取消, or
// cleared 通过 取消/ClearQueue — would otherwise leave caller blocked on
// that RunID: `crush 运行` ignores live 消息 events 和 exits 只 on a
// RunID 匹配的 RunComplete。没有 RunID 的调用没有这样的等待者
// 和 是 dropped silently as before. 一个 detached, bounded 上下文 keeps the
// must-投递 发布 alive even 当 运行 上下文 那 triggered the
// drop 是 already canceled.
func (a *sessionAgent) settleDroppedCalls(drops []SessionAgentCall) {
	if len(drops) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, d := range drops {
		if d.Report != nil {
			if _, err := a.createUserMessage(ctx, d, ""); err != nil {
				slog.Error("Failed to record a dropped subagent report", "session_id", d.SessionID, "error", err)
			}
		}
		if d.RunID == "" {
			continue
		}
		a.publishRunComplete(ctx, d, notify.RunComplete{
			SessionID: d.SessionID,
			RunID:     d.RunID,
			Cancelled: true,
		})
	}
}

// clearQueueAndNotify removes all queued prompts f或 会话 and
// publishes 终态 cancelled RunComplete f或 any 那 carried RunID,
// 所以 callers waiting on those RunIDs (e.g. `crush 运行`) 是 不 left
// hanging 当 their queued 提示词 是 discarded without running.
func (a *sessionAgent) clearQueueAndNotify(sessionID string) {
	queued, ok := a.messageQueue.Get(sessionID)
	a.messageQueue.Del(sessionID)
	if !ok {
		return
	}
	a.settleDroppedCalls(queued)
}

// clearPendingCancel removes any pending-取消 标记 f或 sessionID. It
// takes per-会话 派发 锁 所以 it 是 ordered against 取消
// 和 派发 handoff.
func (a *sessionAgent) clearPendingCancel(sessionID string) {
	mu := a.sessionMu(sessionID)
	mu.Lock()
	defer mu.Unlock()
	a.cancelMark.Del(sessionID)
}

// canceledBySeq reports whether accepted handle 或 queued 调用 with
// given 受理 序号 是 covered 通过 pending 取消 f或 the
// 会话. Callers must hold 会话's 派发 互斥锁. 一个 tracked
// 序号 (seq > 0) 是 covered 只 当 it 是 at 或 below 取消
// high-water 标记, 所以 提示词 accepted after 取消 (higher seq) is
// never poisoned. An untracked 序号 (seq == 0, in-process enqueue
// with no 受理 占位) 是 covered whenever any 标记 是 present,
// preserving pre-序号 behavior. 标记 是 不 consumed: it
// stays 所以 every sibling handle it covers observes same 取消, and
// 更晚的句柄（更高序号）无论如何都忽略它。
func (a *sessionAgent) canceledBySeq(sessionID string, seq uint64) bool {
	mark, ok := a.cancelMark.Get(sessionID)
	if !ok || mark == 0 {
		return false
	}
	return seq == 0 || seq <= mark
}

// persistCanceledTurn writes 用户/助手 records f或 turn that
// was canceled before (或 just as) streaming would have produced them.
// 它 creates 用户 消息 只 当 it was 不 already created 通过 an
// earlier createUserMessage 调用 (userMsgCreated), then writes an
// 助手 消息 用 FinishReasonCanceled. Both writes use
// 上下文.WithoutCancel(ctx) 所以 workspace shutdown (which cancels 运行
// 上下文) can't drop them.
func (a *sessionAgent) persistCanceledTurn(ctx context.Context, call SessionAgentCall, userMsgCreated bool) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if !userMsgCreated {
		if _, err := a.createUserMessage(writeCtx, call, ""); err != nil {
			return err
		}
	}
	largeModel := call.Model
	assistant, err := a.messages.Create(writeCtx, call.SessionID, message.CreateMessageParams{
		Role:      message.Assistant,
		Parts:     []message.ContentPart{},
		Model:     largeModel.ModelCfg.Model,
		ModelName: a.modelDisplayName(largeModel.ModelCfg),
		Provider:  largeModel.ModelCfg.Provider,
	})
	if err != nil {
		return err
	}
	assistant.AddFinish(message.FinishReasonCanceled, "User canceled request", "")
	return a.messages.Update(writeCtx, assistant)
}

// publishRunComplete emits authoritative 终态 事件 f或 turn.
// 它 honors per-调用 OnComplete 钩子 当 set (所以 coordinat或 can
// coalesce retries) 和 otherwise falls back 到 RunComplete 总线.
// ctx 是 used 只 f或 bounded-blocking must-投递 发布; the
// 终态 payload 是 supplied 通过 caller. 这 是 single emit path
// shared 通过 streaming defer 和 取消-on-条目 early return 所以 a
// caller waiting on RunComplete (e.g. `crush 运行` 用 RunID) always
// observes exactly one 终态 事件 regardless 的 which 运行 branch ends
// 本轮。
func (a *sessionAgent) publishRunComplete(ctx context.Context, call SessionAgentCall, complete notify.RunComplete) {
	if call.OnComplete != nil {
		call.OnComplete(complete)
		return
	}
	if a.runComplete == nil {
		return
	}
	a.runComplete.PublishMustDeliver(ctx, pubsub.UpdatedEvent, complete)
}

// ValidateCall 做低成本的结构校验，
// sessionAgent.运行 requires before 调用 cbe dispatched: 调用 must
// carry either non-empty 提示词 或 text attachment, 和 it must name a
// 会话. 它 是 exported 所以 callers 那 受理 运行 before dispatching it
// (e.g. backend.SendMessage) capply same checks 和 keep 错误
// 让契约保持一致。
func ValidateCall(call SessionAgentCall) error {
	if strings.TrimSpace(call.Prompt) == "" && !message.ContainsTextAttachment(call.Attachments) {
		return ErrEmptyPrompt
	}
	if call.SessionID == "" {
		return ErrSessionMissing
	}
	return nil
}

// turnFinish maps upstream finish reason 的 step onto what the
// 助手 消息 records: reason itself plus banner copy the
// agent owns f或 stops it has 到 explain (empty copy means TUI
// supplies its own). Abnormal stops 是 logged because they otherwise
// look exactly like turn 那 completed normally — 响应 just
// 中途停下。
func turnFinish(largeModel Model, sessionID string, reason fantasy.FinishReason) (message.FinishReason, string, string) {
	switch reason {
	case fantasy.FinishReasonStop:
		return message.FinishReasonEndTurn, "", ""
	case fantasy.FinishReasonToolCalls:
		return message.FinishReasonToolUse, "", ""
	case fantasy.FinishReasonLength:
		// turn looks like normal, completed answer 那 simply
		// stops mid-sentence, 所以 leave trace 在 log even though
		// TUI 持有用于展示的副本。
		slog.Warn(
			"Model response truncated at the output token limit",
			"session_id", sessionID,
			"provider", largeModel.ModelCfg.Provider,
			"model", largeModel.ModelCfg.Model,
			"finish_reason", string(reason),
		)
		return message.FinishReasonMaxTokens, "", ""
	case fantasy.FinishReasonContentFilter:
		// 提供方 safety classifier stopped 模型
		// (Anthropic stop_reason=拒绝, OpenAI content_filter).
		// TUI owns display copy; we 只 persist the
		// reason 所以 UI cshow REFUSED banner.
		slog.Warn(
			"Provider content filter stopped the model",
			"session_id", sessionID,
			"finish_reason", string(reason),
		)
		return message.FinishReasonContentFilter, "", ""
	case fantasy.FinishReasonError, fantasy.FinishReasonOther:
		// 提供方 gave up on 响应 from its own side
		// (e.g. insufficient_system_resource) 但 流 still
		// completed, 所以 no 错误 reaches 运行's 错误 path. Record
		// it as 错误 finish, 用 copy generic enough f或 any
		// 提供方 reason, 或 turn renders as normal completed
		// answer 那 stops short.
		slog.Warn(
			"Provider stopped the model",
			"session_id", sessionID,
			"provider", largeModel.ModelCfg.Provider,
			"model", largeModel.ModelCfg.Model,
			"finish_reason", string(reason),
		)
		return message.FinishReasonError, providerStoppedTurnMessage, fmt.Sprintf(
			"The provider ended this response before it was complete (finish_reason=%s). Try again, or use a different model.",
			string(reason),
		)
	}
	return message.FinishReasonUnknown, "", ""
}

func (a *sessionAgent) Run(ctx context.Context, call SessionAgentCall) (result *fantasy.AgentResult, retErr error) {
	if err := ValidateCall(call); err != nil {
		return nil, err
	}
	if call.Model.Model == nil {
		call.Model = a.models().Large
	}

	if call.Channel != "" && call.channelMeta == nil {
		call.channelMeta, _ = parseChannelMeta(call.Prompt)
	}

	// genCtx/cancel 是本轮的运行上下文和取消函数，在下面按 session 的派发锁内创建，
	// 这样并发的 Cancel 能在 assistant 消息产生之前就看到 activeRequests 里的条目。
	var (
		genCtx         context.Context
		cancel         context.CancelFunc
		userMsgCreated bool
	)

	// 把派发决定（入口取消 | 排队 | 活跃）与并发的 Cancel 串行化。Cancel 拿的是
	// 同一把按 session 的锁，所以每次取消至少能看到以下之一：取消标记、
	// activeRequests 条目、或它随后清空的 messageQueue 条目。把锁持有跨越忙检查和
	// 活跃注册，也让这两步变成原子的：两个并发的进程内调用方——一串 channel 事件，
	// 或一个 channel 事件和一条键入 prompt 竞争——不可能同时通过忙检查、在同一
	// session 上启动两轮运行。
	sessMu := a.sessionMu(call.SessionID)
	sessMu.Lock()

	if call.Accepted != nil && a.canceledBySeq(call.SessionID, call.Accepted.seq) {
		// 入口取消：在这个已受理的运行被派发但尚未活跃期间来了取消，且本句柄的
		// 受理序号小于等于 session 的取消标记。标记保留在原处，这样它所覆盖的
		// 兄弟句柄也能观察到同一次取消；释放受理占位、丢掉锁，不进入 Stream，
		// 直接落盘一个被取消的轮次。
		//
		// 这条路径在发布 RunComplete 的流式 defer 安装之前就返回了，所以要显式
		// 发出终态事件。否则等待这个 RunID 的 RunComplete 的调用方（例如
		// `crush run`，它忽略消息事件、只阻塞在 RunComplete 上）会在一个刚被
		// 取消的已受理运行上永久挂起。
		call.Accepted.Close()
		sessMu.Unlock()
		complete := notify.RunComplete{
			SessionID: call.SessionID,
			RunID:     call.RunID,
			Cancelled: true,
		}
		if err := a.persistCanceledTurn(ctx, call, false); err != nil {
			complete.Error = err.Error()
			a.publishRunComplete(ctx, call, complete)
			return nil, err
		}
		a.publishRunComplete(ctx, call, complete)
		return nil, nil
	}

	if a.HasActiveTurn(call.SessionID) {
		// 忙：有更早的 prompt 正在活跃。把本调用入队，让它被折叠进当前轮次
		// （或排在其后），并释放受理占位。此后到达的 Cancel 会看到活跃条目
		// 并清空队列。
		//
		// enqueueCall 会剥离 OnComplete：提供该钩子的调用方（通常是
		// coordinator.Run）有自己的重试/合并作用域，在它返回时就结束了，
		// 所以等队列排空时已经没人消费缓冲的终态事件了。排队轮次因此退回
		// 默认的总线发布，这正是现有订阅者所期望的。
		a.enqueueCall(call)
		if call.Accepted != nil {
			call.Accepted.Close()
		}
		sessMu.Unlock()
		return nil, nil
	}

	if call.QueueOnly {
		if call.Accepted != nil {
			call.Accepted.Close()
		}
		sessMu.Unlock()
		return nil, ErrSessionIdle
	}

	// 空闲：成为活跃运行。在丢锁之前注册取消函数，这样在这里和创建 assistant
	// 之间到达的 Cancel 不会丢失。
	runCtx := context.WithValue(ctx, tools.SessionIDContextKey, call.SessionID)
	runCtx = withRefusalRetries(runCtx, refusalRetriesPerTurn)
	genCtx, cancel = context.WithCancel(runCtx)
	ac := &activeCancel{cancel: cancel}
	a.activeRequests.Set(call.SessionID, ac)
	if call.Accepted != nil {
		call.Accepted.Close()
	}
	sessMu.Unlock()

	defer cancel()
	// 条件清理：只有当我们的条目没被更新的运行替换掉时才移除它。没有这个
	// 保护，延迟的 Del 会在完成窗口里某个并发运行注册之后触发，悄悄抹掉
	// 新运行的取消函数、破坏取消能力。
	defer a.activeRequests.CompareAndDelete(call.SessionID, ac)

	// skipRunComplete 在排队递归路径之前设置，这样外层 Run 不会发布一个
	// 与递归调用自身的 RunComplete 竞争、并被其取代的 RunComplete
	// （每条排队的用户 prompt 都是独立的一轮，各自只发布一个终态事件）。
	var skipRunComplete bool
	// currentAssistant 在这里声明，这样下面延迟的 RunComplete 发布能捕获
	// PrepareStep 稍后为每个流式步骤重新赋值的那个指针。当 defer 执行时，
	// 通过该指针能取到的就是本轮最终的 assistant 消息。
	var currentAssistant *message.Message
	// 返回前排空所有被 debounce 的消息更新。message.Service 在终态更新时
	// 已经会同步刷盘，但这里加一个 defer 可以在每一次 Run 退出时（成功、
	// 错误、上游 panic 恢复）都保证这个契约，调用方无需关心。
	//
	// 刷盘完成后——意味着所有单条消息的 Publish(UpdatedEvent) 都已触发并
	// 缓冲进每个订阅者的 channel——再发布本轮权威的 RunComplete 事件。
	// 「先刷盘再发布」的顺序让行为良好的客户端最有机会在 RunComplete 之前
	// 看到最终的消息事件；内嵌的 Text 字段则用于让乱序观察事件的客户端
	// 对齐（pubsub broker 的汇聚不会序列化来自不同上游 broker 的发布）。
	defer func() {
		// 使用一个脱离运行上下文的 ctx：workspace 关闭会在这个 goroutine
		// 返回前取消 ctx，但缓冲的流式增量仍必须在 DB 关闭前落盘。
		// 一个短超时给刷盘设上界。
		flushCtx, flushCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer flushCancel()
		if flushErr := a.messages.FlushAll(flushCtx); flushErr != nil {
			slog.Error("Failed to flush pending message updates after run", "error", flushErr)
		}
		if skipRunComplete {
			return
		}
		complete := notify.RunComplete{SessionID: call.SessionID, RunID: call.RunID}
		if currentAssistant != nil {
			complete.MessageID = currentAssistant.ID
			complete.Text = currentAssistant.Content().String()
		}
		if retErr != nil {
			complete.Error = retErr.Error()
			complete.Cancelled = errors.Is(retErr, context.Canceled)
		} else if ctx.Err() != nil {
			complete.Cancelled = true
		}
		// 提供了 per-call 钩子时优先用它，这样协调器能把重试
		// （例如未授权 → 重新认证 → 重试）合并成单个用户可见的终态事件。
		// 兜底的 must-deliver 发布对权威终态事件采用有界阻塞语义，
		// 这样暂时满的订阅者 channel 不会悄悄丢掉它、让等待 RunComplete 的
		// 非交互客户端挂起。
		a.publishRunComplete(ctx, call, complete)
	}()

	// 在锁下复制可变字段，避免和 SetTools 竞争。
	agentTools := filterToolsForChannel(a.tools.Copy(), call.Channel, mcp.GetStates())
	largeModel := call.Model
	systemPrompt := a.systemPrompt.Get()
	promptPrefix := a.systemPromptPrefix.Get()
	var instructions strings.Builder

	for _, server := range mcp.GetStates() {
		if server.State != mcp.StateConnected {
			continue
		}
		if s := server.Client.InitializeResult().Instructions; s != "" {
			instructions.WriteString(s)
			instructions.WriteString("\n\n")
		}
	}

	if s := instructions.String(); s != "" {
		systemPrompt += "\n\n<mcp-instructions>\n" + s + "\n</mcp-instructions>"
	}

	if len(agentTools) > 0 {
		// 给最后一个工具加上 Anthropic 缓存。
		agentTools[len(agentTools)-1].SetProviderOptions(a.getCacheControlOptions())
	}

	agent := fantasy.NewAgent(
		largeModel.Model,
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithTools(agentTools...),
		fantasy.WithUserAgent(userAgent),
	)

	sessionLock := sync.Mutex{}
	currentSession, err := a.sessions.Get(ctx, call.SessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	msgs, err := a.getSessionMessages(ctx, currentSession)
	if err != nil {
		return nil, fmt.Errorf("failed to get session messages: %w", err)
	}
	// 在后台从第一条真实（非 shell）用户 prompt 生成标题；Run 不能等它，
	// 由 CancelAll 来排空这个后台任务。
	if !hasUserTextMessage(msgs) {
		titleCtx := context.WithoutCancel(ctx)
		a.titles.begin()
		go func() {
			defer a.titles.end()
			a.generateTitle(titleCtx, call.SessionID, call.Prompt)
		}()
	}

	// 把用户消息加入 session。prompt 之前发送的上下文会随它一起存储，
	// 这样后续请求会重放同一段文本。
	modelContext := hostContextPrompt(call.HostContext) + notesPrompt(a.takeNotes(call.SessionID))
	_, err = a.createUserMessage(ctx, call, modelContext)
	if err != nil {
		return nil, err
	}
	userMsgCreated = true

	// 把 session 放进上下文。运行上下文 genCtx 和它的取消函数已经在上面
	// 派发锁之下创建并注册好了——受理路径和进程内路径都是如此。
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, call.SessionID)

	history, files := a.preparePrompt(msgs, largeModel.CatwalkCfg.Capabilities.Vision, largeModel.CatwalkCfg.Capabilities.Audio, call.Attachments...)

	startTime := time.Now()
	a.eventPromptSent(call.SessionID)
	a.fireTurnHook(genCtx, call, hooks.Event{Name: hooks.EventTurnStart})

	var stepMessages []fantasy.Message
	var foldedPromptInsertions []foldedPromptInsertion
	var shouldSummarize bool
	// 由 OnToolCall 写入、由 OnToolResult 读取；fantasy 对并行工具调用会
	// 并发调用这两个回调。
	sanitizedToolCalls := csync.NewMap[string, bool]()
	// 本轮无错完成的工具调用的完整名称。由 OnToolResult 写入（并行工具调用
	// 时并发执行），在 Stream 返回后读取——sendChannelReply 用它判断模型
	// 是否已经在来源 channel 上回复过了。
	completedToolCalls := csync.NewMap[string, struct{}]()
	// 本轮已经得到回复的调用；其余的在轮次结束时结算。
	answeredToolCalls := csync.NewMap[string, struct{}]()
	// MaxOutputTokens 为 0 时不发送——有些 provider（例如 LM Studio）会拒绝它
	var maxOutputTokens *int64
	if call.MaxOutputTokens > 0 {
		maxOutputTokens = &call.MaxOutputTokens
	}
	result, err = agent.Stream(genCtx, fantasy.AgentStreamCall{
		Prompt:           modelContext + message.PromptWithTextAttachments(call.Prompt, call.Attachments),
		Files:            files,
		Messages:         history,
		Headers:          sessionHeaders(call.SessionID),
		ProviderOptions:  call.ProviderOptions,
		MaxOutputTokens:  maxOutputTokens,
		TopP:             call.TopP,
		Temperature:      call.Temperature,
		PresencePenalty:  call.PresencePenalty,
		TopK:             call.TopK,
		FrequencyPenalty: call.FrequencyPenalty,
		MaxRetries:       retries(a.retryBudget()),
		PrepareStep: func(callContext context.Context, options fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
			// Use latest tools (updated 通过 SetTools 当 MCP tools
			// change), filtered f或 会话's 渠道 和 minus MCP
			// servers disabled f或 这 repository.
			prepared.Tools = a.filterDisabledMCPTools(
				callContext,
				filterToolsForChannel(a.tools.Copy(), call.Channel, mcp.GetStates()),
			)

			// Drain queued follow-up prompts f或 这 step. Calls covered
			// by 取消 recorded while they sat 在 队列 是 dropped:
			// 取消 那 arrived after 提示词 was queued must 不 let
			// 让它作为本步骤的一部分运行。覆盖判定按调用逐个依据受理序号，
			// 这样在取消之后（更高序号）入队的后续 prompt 不会被丢。
			// 携带 RunID 的被丢 prompt 仍会拿到终态的取消 RunComplete，
			// 等待它的调用方不会挂起。未取消且无 RunID 的 prompt 被折叠进本轮；
			// 未取消但有 RunID 的继续留在队列里，通过下面的递归运行路径
			// 各自作为独立一轮运行（各自带一个 RunComplete）。
			fold, canceled := a.drainQueueForStep(call.SessionID)
			a.settleDroppedCalls(canceled)
			var foldedMessages []fantasy.Message
			for _, queued := range fold {
				userMessage, createErr := a.createUserMessage(callContext, queued, "")
				if createErr != nil {
					return callContext, prepared, createErr
				}
				foldedMessages = append(foldedMessages, userMessage.ToAIMessage()...)
			}

			if len(foldedMessages) > 0 {
				foldedPromptInsertions = append(foldedPromptInsertions, foldedPromptInsertion{
					index:    len(options.Messages),
					messages: foldedMessages,
				})
			}

			prepared.Messages = applyFoldedPromptInsertions(options.Messages, foldedPromptInsertions)
			for i := range prepared.Messages {
				prepared.Messages[i].ProviderOptions = nil
			}

			prepared.Messages = dropEmptyToolMedia(prepared.Messages)
			prepared.Messages = a.workaroundProviderMediaLimitations(prepared.Messages, largeModel)

			lastSystemRoleInx := 0
			systemMessageUpdated := false
			for i, msg := range prepared.Messages {
				// 只给最后一条消息加缓存控制。
				if msg.Role == fantasy.MessageRoleSystem {
					lastSystemRoleInx = i
				} else if !systemMessageUpdated {
					prepared.Messages[lastSystemRoleInx].ProviderOptions = a.getCacheControlOptions()
					systemMessageUpdated = true
				}
				// 再给最后 2 条消息加缓存控制。
				if i > len(prepared.Messages)-3 {
					prepared.Messages[i].ProviderOptions = a.getCacheControlOptions()
				}
			}

			if promptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{fantasy.NewSystemMessage(promptPrefix)}, prepared.Messages...)
			}

			sessionLock.Lock()
			stepMessages = cloneFantasyMessages(prepared.Messages)
			sessionLock.Unlock()

			var assistantMsg message.Message
			assistantMsg, err = a.messages.Create(callContext, call.SessionID, message.CreateMessageParams{
				Role:      message.Assistant,
				Parts:     []message.ContentPart{},
				Model:     largeModel.ModelCfg.Model,
				ModelName: a.modelDisplayName(largeModel.ModelCfg),
				Provider:  largeModel.ModelCfg.Provider,
			})
			if err != nil {
				return callContext, prepared, err
			}
			callContext = context.WithValue(callContext, tools.MessageIDContextKey, assistantMsg.ID)
			callContext = context.WithValue(callContext, tools.ChannelContextKey, call.Channel)
			callContext = context.WithValue(callContext, tools.SupportsImagesContextKey, largeModel.CatwalkCfg.Capabilities.Vision)
			callContext = context.WithValue(callContext, tools.ModelNameContextKey, largeModel.CatwalkCfg.Name)
			currentAssistant = &assistantMsg
			return callContext, prepared, err
		},
		OnReasoningStart: func(id string, reasoning fantasy.ReasoningContent) error {
			currentAssistant.AppendReasoningContent(reasoning.Text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnReasoningDelta: func(id string, text string) error {
			currentAssistant.AppendReasoningContent(text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnReasoningEnd: func(id string, reasoning fantasy.ReasoningContent) error {
			// 处理 anthropic 的签名
			if anthropicData, ok := reasoning.ProviderMetadata[anthropic.Name]; ok {
				if reasoning, ok := anthropicData.(*anthropic.ReasoningOptionMetadata); ok {
					currentAssistant.AppendReasoningSignature(reasoning.Signature)
				}
			}
			if googleData, ok := reasoning.ProviderMetadata[google.Name]; ok {
				if reasoning, ok := googleData.(*google.ReasoningMetadata); ok {
					currentAssistant.AppendThoughtSignature(reasoning.Signature, reasoning.ToolID)
				}
			}
			if openaiData, ok := reasoning.ProviderMetadata[openai.Name]; ok {
				if reasoning, ok := openaiData.(*openai.ResponsesReasoningMetadata); ok {
					currentAssistant.SetReasoningResponsesData(reasoning)
				}
			}
			currentAssistant.FinishThinking()
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnTextDelta: func(id string, text string) error {
			// Strip leading newline 只 当 printing. 这 is
			// particularly important 在 non-interactive mode where leading
			// newlines 是 very visible.
			currentAssistant.AppendContent(text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnToolInputStart: func(id string, toolName string) error {
			toolCall := message.ToolCall{
				ID:               id,
				Name:             toolName,
				ProviderExecuted: false,
				Finished:         false,
			}
			currentAssistant.AddToolCall(toolCall)
			// Use parent ctx instead 的 genCtx 到 ensure 更新 succeeds
			// even 如果 请求 是 canceled mid-流
			return a.messages.Update(ctx, *currentAssistant)
		},
		OnRetry: func(err *fantasy.ProviderError, delay time.Duration) {
			slog.Warn("Provider request failed, retrying", providerRetryLogFields(err, delay)...)
			// Reset streamed content 所以 retried 响应 doesn't
			// concatenate 用 partial content from failed attempt.
			// 在最后一次尝试（不再重试）时，任何已生成的部分内容
			// stays 在 消息 as useful 上下文 beneath 错误.
			currentAssistant.ResetStreamedContent()
			if updateErr := a.messages.Update(genCtx, *currentAssistant); updateErr != nil {
				slog.Error("Failed to reset message on retry", "error", updateErr)
			}
		},
		OnAuthRefresh: call.OnAuthRefresh,
		ModelProvider: func() fantasy.LanguageModel {
			slog.Info("ModelProvider called",
				"provider", largeModel.ModelCfg.Provider,
				"model", largeModel.ModelCfg.Model)
			return a.currentBuildOf(largeModel)
		},
		OnToolCall: func(tc fantasy.ToolCallContent) error {
			input, wasSanitized := sanitizeToolInput(tc.ToolName, tc.ToolCallID, tc.Input)
			if wasSanitized {
				sanitizedToolCalls.Set(tc.ToolCallID, true)
			}
			toolCall := message.ToolCall{
				ID:               tc.ToolCallID,
				Name:             tc.ToolName,
				Input:            input,
				ProviderExecuted: false,
				Finished:         true,
			}
			currentAssistant.AddToolCall(toolCall)
			// Use parent ctx instead 的 genCtx 到 ensure 更新 succeeds
			// even 如果 请求 是 canceled mid-流
			return a.messages.Update(ctx, *currentAssistant)
		},
		OnToolResult: func(result fantasy.ToolResultContent) error {
			toolResult := a.convertToToolResult(result)
			if _, ok := sanitizedToolCalls.Get(result.ToolCallID); ok {
				toolResult.Content = "Tool call failed: arguments were not valid JSON. Please check your tool call format and try again."
				toolResult.IsError = true
			} else if toolResult.IsError && genCtx.Err() != nil {
				// turn was cancelled out from under 工具.
				toolResult.Content = interruptedToolCallResult
			}
			if !toolResult.IsError && toolResult.Name != "" {
				completedToolCalls.Set(toolResult.Name, struct{}{})
			}
			toolResult.FinishedAtMs = time.Now().UnixMilli()
			// Use parent ctx instead 的 genCtx 到 ensure 消息 是 created
			// even 如果 请求 是 canceled mid-流
			_, createMsgErr := a.messages.Create(ctx, currentAssistant.SessionID, message.CreateMessageParams{
				Role: message.Tool,
				Parts: []message.ContentPart{
					toolResult,
				},
			})
			if createMsgErr == nil {
				answeredToolCalls.Set(result.ToolCallID, struct{}{})
			}
			return createMsgErr
		},
		OnStepFinish: func(stepResult fantasy.StepResult) error {
			for _, w := range stepResult.Warnings {
				slog.Warn("Provider warning", "type", w.Type, "message", w.Message)
			}
			finishReason, finishMessage, finishDetails := turnFinish(largeModel, call.SessionID, stepResult.FinishReason)
			// If 工具 结果 halted turn (e.g. 钩子 halt 或 a
			// 权限 denial), step ends on FinishReasonToolCalls but
			// 模型 will 不 be called again. Treat it as end 的 the
			// turn 所以 UI crender 助手 footer.
			if finishReason == message.FinishReasonToolUse {
				for _, tr := range stepResult.Content.ToolResults() {
					if tr.StopTurn {
						finishReason = message.FinishReasonEndTurn
						break
					}
				}
			}
			currentAssistant.AddFinish(finishReason, finishMessage, finishDetails)
			currentAssistant.PrismModelID, currentAssistant.PrismModelName = extractPrismModel(stepResult.ProviderMetadata)
			currentAssistant.PrismHypercreditSavings, currentAssistant.PrismDollarSavings = extractPrismSavings(stepResult.ProviderMetadata)
			sessionLock.Lock()
			defer sessionLock.Unlock()

			updatedSession, getSessionErr := a.sessions.Get(ctx, call.SessionID)
			if getSessionErr != nil {
				return getSessionErr
			}
			usage, estimated := fallbackStepUsage(stepMessages, stepResult)
			pricingModel := a.modelForStepCost(largeModel, stepResult.ProviderMetadata)
			a.updateSessionUsage(pricingModel, &updatedSession, usage, a.openrouterCost(stepResult.ProviderMetadata), estimated)
			_, sessionErr := a.sessions.Save(ctx, updatedSession)
			if sessionErr != nil {
				return sessionErr
			}
			currentSession = updatedSession
			return a.messages.Update(genCtx, *currentAssistant)
		},
		StopWhen: []fantasy.StopCondition{
			func(_ []fantasy.StepResult) bool {
				cw := int64(largeModel.CatwalkCfg.ContextWindow)
				// If 上下文 窗口 是 unknown (0), skip auto-summarize
				// 避免立即截断自定义/本地模型。
				if cw == 0 {
					return false
				}
				tokens := currentSession.CompletionTokens + currentSession.PromptTokens
				remaining := cw - tokens
				var threshold int64
				if cw > largeContextWindowThreshold {
					threshold = largeContextWindowBuffer
				} else {
					threshold = int64(float64(cw) * smallContextWindowRatio)
				}
				if (remaining <= threshold) && !a.disableAutoSummarize {
					shouldSummarize = true
					return true
				}
				return false
			},
			func(steps []fantasy.StepResult) bool {
				return hasRepeatedToolCalls(steps, loopDetectionWindowSize, loopDetectionMaxRepeats)
			},
		},
	})

	a.eventPromptResponded(call.SessionID, time.Since(startTime).Truncate(time.Second))

	// 最终写入使用脱离运行上下文的 ctx：workspace 关闭会在 agent goroutine
	// 结束前取消 ctx，但轮次的收尾仍必须落盘。
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cleanupCancel()
	if currentAssistant != nil {
		answered := func(id string) bool {
			_, ok := answeredToolCalls.Get(id)
			return ok
		}
		if settleErr := a.settleTurn(cleanupCtx, currentAssistant, answered, err); settleErr != nil {
			slog.Error("Failed to settle unanswered tool calls",
				"session_id", call.SessionID, "error", settleErr)
		}
	}

	if err != nil {
		isCancelErr := errors.Is(err, context.Canceled)
		slog.Info("Agent stream returned error",
			"error", err.Error(),
			"error_type", fmt.Sprintf("%T", err),
			"oauth", largeModel.OAuth,
			"is_cancel", isCancelErr)
		// 无论下面走哪条返回路径，都在轮次终态写入后触发一次；
		// 并且要比被取消的运行上下文活得更久。
		defer a.fireTurnHook(context.WithoutCancel(ctx), call, turnEndEvent(err, currentAssistant))
		if currentAssistant == nil {
			// 创建 assistant 之前的取消窗口：运行在 activeRequests.Set 之后、
			// PrepareStep 创建 assistant 消息之前被取消了。没有这段处理，
			// 轮次会没有任何 FinishReasonCanceled 标记、也没有用户可见记录地返回。
			// 用户消息上面已经创建过了，所以 persistCanceledTurn 只写 assistant 记录。
			if isCancelErr {
				if persistErr := a.persistCanceledTurn(ctx, call, userMsgCreated); persistErr != nil {
					return nil, persistErr
				}
			}
			return result, err
		}
		// 出错时也要结束思考态，把 reasoning 状态关掉。
		currentAssistant.FinishThinking()
		var fantasyErr *fantasy.Error
		var providerErr *fantasy.ProviderError
		var requestTimedOutErr *requestTimeoutError
		const defaultTitle = "Provider Error"
		linkStyle := lipgloss.NewStyle().Foreground(tone.Guac).Underline(true)
		if isCancelErr {
			currentAssistant.AddFinish(message.FinishReasonCanceled, "User canceled request", "")
		} else if errors.Is(err, errRefused) {
			// 每次重试都被拒；保留拒绝横幅，不要报成通用的 provider 错误。
			currentAssistant.AddFinish(message.FinishReasonContentFilter, "", "")
		} else if errors.As(err, &requestTimedOutErr) {
			// 在 provider 分支之前检查，这样我们自己请求超时施加的期限
			// 不会被误报成 provider 错误。
			currentAssistant.AddFinish(message.FinishReasonError, "Request timed out", requestTimedOutErr.userMessage())
		} else if largeModel.OAuth && errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized {
			currentAssistant.AddFinish(message.FinishReasonError, "Unauthorized", signInAgainMessage(largeModel))
		} else if errors.As(err, &providerErr) {
			if providerErr.Message == "The requested model is not supported." {
				url := "https://github.com/settings/copilot/features"
				link := linkStyle.Hyperlink(url, "id=copilot").Render(url)
				currentAssistant.AddFinish(
					message.FinishReasonError,
					"Copilot model not enabled",
					fmt.Sprintf("%q is not enabled in Copilot. Go to the following page to enable it. Then, wait 5 minutes before trying again. %s", largeModel.CatwalkCfg.Name, link),
				)
			} else {
				currentAssistant.AddFinish(message.FinishReasonError, cmp.Or(stringext.Capitalize(providerErr.Title), defaultTitle), providerErr.Message)
			}
		} else if errors.As(err, &fantasyErr) {
			currentAssistant.AddFinish(message.FinishReasonError, cmp.Or(stringext.Capitalize(fantasyErr.Title), defaultTitle), fantasyErr.Message)
		} else if fantasy.IsTransportError(err) {
			wrapped := fantasy.NewTransportError(err)
			currentAssistant.AddFinish(message.FinishReasonError, stringext.Capitalize(wrapped.Title), wrapped.Message)
		} else {
			currentAssistant.AddFinish(message.FinishReasonError, defaultTitle, err.Error())
		}
		updateErr := a.messages.Update(cleanupCtx, *currentAssistant)
		if updateErr != nil {
			return nil, updateErr
		}
		// channel 来源的轮次没有调用方在盯着错误，所以要告诉 channel 侧
		// 出了问题，别让发送者一直挂着。sendChannelReply 会脱离运行上下文，
		// 这样即使 provider 错误把运行上下文拆掉了，这条通知仍能送达。
		if channelErrorReplyWanted(call.Channel, err) {
			a.sendChannelReply(ctx, call,
				"Something went wrong while handling your message. Please try again.",
				completedToolCalls.Copy())
		}
		return nil, err
	}

	// 因压缩而提前中断、仍有工具调用待处理的轮次，会由一个排队的后续轮次继续；
	// 那个后续轮次才真正回复并在结束时触发 TurnEnd。
	continued := false
	if shouldSummarize {
		a.activeRequests.Del(call.SessionID)
		summarizeErr := a.summarize(genCtx, largeModel, call.SessionID, compactTriggerAuto, call.ProviderOptions, call.OnAuthRefresh)
		switch {
		case errors.Is(summarizeErr, ErrCompactionBlocked):
			// 没压缩成的话，下一步只会再次撞上限，所以本轮到此结束。
			slog.Info("PreCompact hook blocked compaction", "session_id", call.SessionID)
		case summarizeErr != nil:
			return nil, summarizeErr
		case len(currentAssistant.ToolCalls()) > 0:
			existing, _ := a.messageQueue.Get(call.SessionID)
			call.Prompt = fmt.Sprintf("The previous session was interrupted because it got too long, the initial user request was: `%s`", call.Prompt)
			// 改写后的 prompt 是一条新消息，不是那份 report。
			call.Report = nil
			a.messageQueue.Set(call.SessionID, append(existing, call))
			continued = true
		}
	}
	finished := currentAssistant != nil && !continued

	// 把已完成轮次的响应路由回它来源的 channel。这发生在繁忙状态释放之前，
	// 这样排在后面的后续推送不会超车、抢在它的回复之前。当响应中途停下时，
	// 回复文本会带一条结尾说明，因为 channel 接收方只看得到文本，
	// 看不到 TUI 的横幅。
	if finished {
		a.sendChannelReply(ctx, call, channelReplyText(currentAssistant), completedToolCalls.Copy())
	}

	// 在发布通知之前释放活跃请求。TUI handler 轮询 IsSessionBusy()，
	// 且只在 tea.Msg 到达时重新求值，所以清理必须先于通知，
	// 否则订阅者在收到通知的那一刻看到的是过期的繁忙状态。
	a.activeRequests.Del(call.SessionID)
	cancel()

	if finished {
		a.fireTurnHook(ctx, call, turnEndEvent(nil, currentAssistant))
	}

	// 发出 agent 已完成本轮的通知（嵌套/非交互 session 跳过）。
	if !call.NonInteractive && a.notify != nil {
		a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID:    call.SessionID,
			SessionTitle: currentSession.Title,
			Type:         notify.TypeAgentFinished,
		})
	}

	// 在 dispatchMu 下把控制权交给下一个排队的 prompt（如果有），
	// 这样从本轮完成到排队轮次的转换对并发 Cancel 是原子的。
	// 本 session 的 activeRequests 刚在上面删掉了，没有锁的话会有一个窗口：
	// session 看起来是空闲的，取消就成了一个空操作、停不下排队的 prompt。
	// 持有锁让我们能观察到针对该 session 记录的待处理取消，丢掉队列而不运行它；
	// 并且（对递归而言）给出队的调用递一个新的受理占位，使 acceptedRuns
	// 在递归 Run 自身的派发交接期间始终 > 0——让整个转换过程中 session
	// 对 Cancel 都可见，并关上出队 -> 重新注册之间的窗口。
	mu := a.sessionMu(call.SessionID)
	mu.Lock()
	queuedMessages, _ := a.messageQueue.Get(call.SessionID)
	if mark, ok := a.cancelMark.Get(call.SessionID); ok && mark > 0 && len(queuedMessages) > 0 {
		// 针对本 session 记录了一个取消（例如它在本轮活跃、且后续 prompt 已入队
		// 期间到达）。丢掉它覆盖的排队 prompt（受理序号小于等于标记，或未跟踪的）；
		// 保留取消之后（更高序号）入队的，让它们仍会运行。
		var kept, dropped []SessionAgentCall
		for _, q := range queuedMessages {
			if q.acceptSeq == 0 || q.acceptSeq <= mark {
				dropped = append(dropped, q)
				continue
			}
			kept = append(kept, q)
		}
		queuedMessages = kept
		a.messageQueue.Set(call.SessionID, kept)
		a.settleDroppedCalls(dropped)
	}
	if len(queuedMessages) == 0 {
		// No queued work. Clear 取消 标记 只 当 no accepted
		// 运行 remains 在 flight 那 it might still cover; otherwise a
		// sibling 提示词 (序号 at 或 below 标记) waiting to
		// enter 运行 would lose its cancellation. When accepted runs are
		// gone, 这 al所以 clears stale 标记 所以 it can't catch a
		// future 运行.
		a.messageQueue.Del(call.SessionID)
		a.acceptedMu.Lock()
		inFlight, _ := a.acceptedRuns.Get(call.SessionID)
		a.acceptedMu.Unlock()
		if inFlight == 0 {
			a.cancelMark.Del(call.SessionID)
		}
		mu.Unlock()
		return result, err
	}
	// There 是 queued messages, restart loop. Suppress outer
	// defer's emit: it would otherwise observe recursive 运行's retErr
	// （通过下面的 return 覆盖具名返回值）针对这里
	// turn's MessageID/Text 和 发布 mixed, racing 事件.
	skipRunComplete = true
	// Decide whether 这 turn still owes its own 终态 RunComplete.
	// Each submitted 提示词 用 RunID has its own lifecycle, 所以 turn
	// that 是 finished 和 handing off 到 *different* queued 提示词 must
	// 发布 its own RunComplete here — leaving it 到 recursive turn
	// （它带着不同的 RunID）会让等待下面的调用方挂起
	// this turn's RunID. exception 是 summarize-continuation path,
	// which re-queues 这 same 调用 (same RunID) 到 resume after a
	// 摘要; 在 那 case eventual 终态 turn f或 这 RunID
	// publishes, 所以 publishing now would double-emit.
	outerOwesRunComplete := call.RunID != ""
	if outerOwesRunComplete {
		for _, q := range queuedMessages {
			if q.RunID == call.RunID {
				outerOwesRunComplete = false
				break
			}
		}
	}
	firstQueuedMessage := queuedMessages[0]
	a.messageQueue.Set(call.SessionID, queuedMessages[1:])
	// Reserve fresh 受理 f或 dequeued 提示词 before dropping the
	// 锁 所以 acceptedRuns > 0 across handoff into recursive
	// 运行. 这 closes 窗口 between 这 dequeue 和 recursive
	// 运行 registering its activeRequests 条目: 取消 arriving in
	// that 窗口 now records pending 取消 (acceptedRuns > 0) that
	// recursive 运行's accepted path observes as 取消-on-条目.
	firstQueuedMessage.Accepted = a.BeginAccepted(call.SessionID)
	mu.Unlock()
	if outerOwesRunComplete {
		complete := notify.RunComplete{SessionID: call.SessionID, RunID: call.RunID}
		if currentAssistant != nil {
			complete.MessageID = currentAssistant.ID
			complete.Text = currentAssistant.Content().String()
		}
		if ctx.Err() != nil {
			complete.Cancelled = true
		}
		a.publishRunComplete(ctx, call, complete)
	}
	return a.Run(ctx, firstQueuedMessage)
}

// Summarize compacts 会话 on 用户's 请求 用 模型, which
// must be 模型 opts were built for. 它 returns ErrCompactionBlocked
// 当 PreCompact 钩子 blocks it.
func (a *sessionAgent) Summarize(ctx context.Context, sessionID string, model Model, opts fantasy.ProviderOptions, onAuthRefresh func(context.Context, *fantasy.ProviderError) error) error {
	if a.HasPendingWork(sessionID) {
		return ErrSessionBusy
	}
	return a.summarize(ctx, model, sessionID, compactTriggerManual, opts, onAuthRefresh)
}

// summarize compacts 会话 用 largeModel, which must be 模型
// 这些选项所针对的。
func (a *sessionAgent) summarize(ctx context.Context, largeModel Model, sessionID, trigger string, opts fantasy.ProviderOptions, onAuthRefresh func(context.Context, *fantasy.ProviderError) error) error {
	systemPromptPrefix := a.systemPromptPrefix.Get()

	currentSession, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}
	msgs, err := a.getSessionMessages(ctx, currentSession)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		// Nothing 到 summarize.
		return nil
	}

	if pre := a.hooks.Run(ctx, hooks.Event{Name: hooks.EventPreCompact, SessionID: sessionID, Trigger: trigger}); pre.Blocked() {
		return fmt.Errorf("%w: %s", ErrCompactionBlocked, pre.Reason)
	}

	aiMsgs, _ := a.preparePrompt(msgs, largeModel.CatwalkCfg.Capabilities.Vision, largeModel.CatwalkCfg.Capabilities.Audio)

	genCtx, cancel := context.WithCancel(ctx)
	ac := &activeCancel{cancel: cancel}
	a.activeRequests.Set(sessionID, ac)
	defer a.activeRequests.CompareAndDelete(sessionID, ac)
	defer cancel()
	defer func() {
		if flushErr := a.messages.FlushAll(ctx); flushErr != nil {
			slog.Error("Failed to flush pending message updates after summarize", "error", flushErr)
		}
	}()

	agent := fantasy.NewAgent(
		largeModel.Model,
		fantasy.WithSystemPrompt(promptOrBuiltin(a.prompts, "summary")),
		fantasy.WithUserAgent(userAgent),
	)
	summaryMessage, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:             message.Assistant,
		Model:            largeModel.ModelCfg.Model,
		ModelName:        a.modelDisplayName(largeModel.ModelCfg),
		Provider:         largeModel.ModelCfg.Provider,
		IsSummaryMessage: true,
	})
	if err != nil {
		return err
	}

	summaryPromptText := buildSummaryPrompt(currentSession.Todos)

	resp, err := agent.Stream(genCtx, fantasy.AgentStreamCall{
		Prompt:          summaryPromptText,
		Messages:        aiMsgs,
		Headers:         sessionHeaders(sessionID),
		ProviderOptions: opts,
		MaxRetries:      retries(backgroundRetries),
		OnAuthRefresh:   onAuthRefresh,
		ModelProvider: func() fantasy.LanguageModel {
			return a.currentBuildOf(largeModel)
		},
		PrepareStep: func(callContext context.Context, options fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
			prepared.Messages = options.Messages
			if systemPromptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{fantasy.NewSystemMessage(systemPromptPrefix)}, prepared.Messages...)
			}
			return callContext, prepared, nil
		},
		OnReasoningDelta: func(id string, text string) error {
			summaryMessage.AppendReasoningContent(text)
			return a.messages.Update(genCtx, summaryMessage)
		},
		OnReasoningEnd: func(id string, reasoning fantasy.ReasoningContent) error {
			// 处理 anthropic 签名。
			if anthropicData, ok := reasoning.ProviderMetadata["anthropic"]; ok {
				if signature, ok := anthropicData.(*anthropic.ReasoningOptionMetadata); ok && signature.Signature != "" {
					summaryMessage.AppendReasoningSignature(signature.Signature)
				}
			}
			summaryMessage.FinishThinking()
			return a.messages.Update(genCtx, summaryMessage)
		},
		OnTextDelta: func(id, text string) error {
			summaryMessage.AppendContent(text)
			return a.messages.Update(genCtx, summaryMessage)
		},
	})
	if err != nil {
		isCancelErr := errors.Is(err, context.Canceled)
		if isCancelErr {
			// 用户 cancelled summarize we need 到 remove 摘要 消息.
			deleteErr := a.messages.Delete(ctx, summaryMessage.ID)
			return deleteErr
		}
		// 标记 摘要 消息 as finished 用 错误 所以 UI
		// 停止转圈。
		summaryMessage.AddFinish(message.FinishReasonError, "Summarization Error", err.Error())
		if updateErr := a.messages.Update(ctx, summaryMessage); updateErr != nil {
			return updateErr
		}
		return err
	}

	var overrideCost *float64
	for _, step := range resp.Steps {
		summaryMessage.PrismModelID, summaryMessage.PrismModelName = extractPrismModel(step.ProviderMetadata)
		summaryMessage.PrismHypercreditSavings, summaryMessage.PrismDollarSavings = extractPrismSavings(step.ProviderMetadata)
		stepCost := a.openrouterCost(step.ProviderMetadata)
		if stepCost != nil {
			newCost := *stepCost
			if overrideCost != nil {
				newCost += *overrideCost
			}
			overrideCost = &newCost
		}
	}
	if hyperCost := a.hyperStepsCost(largeModel, resp.Steps); hyperCost != nil {
		overrideCost = hyperCost
	}

	// 一个 压缩 摘要 那 stopped short 是 record 会话
	// goes on reading from, 所以 record 和 log why it 是 incomplete
	// 而不是把它记成干净的 end_turn。未知的结束原因
	// (providers 那 never 报告 one) keep historical end_turn.
	finishReason, finishMessage, finishDetails := turnFinish(largeModel, sessionID, resp.Response.FinishReason)
	if finishReason == message.FinishReasonUnknown {
		finishReason = message.FinishReasonEndTurn
	}
	summaryMessage.AddFinish(finishReason, finishMessage, finishDetails)
	err = a.messages.Update(genCtx, summaryMessage)
	if err != nil {
		return err
	}

	a.updateSessionUsage(largeModel, &currentSession, resp.TotalUsage, overrideCost, false)

	// Just 在 case, get just last usage info.
	usage := resp.Response.Usage
	currentSession.SummaryMessageID = summaryMessage.ID
	currentSession.CompletionTokens = summaryCompletionTokens(usage, summaryMessage)
	currentSession.PromptTokens = 0
	currentSession.EstimatedUsage = usageIsZero(usage)
	_, err = a.sessions.Save(genCtx, currentSession)
	if err != nil {
		return err
	}
	a.hooks.Run(ctx, hooks.Event{Name: hooks.EventPostCompact, SessionID: sessionID, Trigger: trigger})

	// Release 活跃 请求 before processing queued messages 所以 that
	// 运行() does 不 see 会话 as busy.
	a.activeRequests.Del(sessionID)
	cancel()

	// Process any messages 那 were queued while summarizing.
	queuedMessages, ok := a.messageQueue.Get(sessionID)
	if !ok || len(queuedMessages) == 0 {
		return nil
	}
	firstQueuedMessage := queuedMessages[0]
	a.messageQueue.Set(sessionID, queuedMessages[1:])
	_, qErr := a.Run(ctx, firstQueuedMessage)
	return qErr
}

func (a *sessionAgent) getCacheControlOptions() fantasy.ProviderOptions {
	if t, _ := strconv.ParseBool(os.Getenv("CRUSH_DISABLE_ANTHROPIC_CACHE")); t {
		return fantasy.ProviderOptions{}
	}
	return fantasy.ProviderOptions{
		anthropic.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
		bedrock.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
		vercel.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
	}
}

// sessionHeaders returns HTTP headers we use f或 缓存 affinity on
// every LLM 请求 f或 given 会话.
//
// We use 会话 hash 是 used instead 的 raw UUID 所以 header
// value 是 deterministic 和 opaque.
func sessionHeaders(sessionID string) map[string]string {
	hash := session.HashID(sessionID)
	return map[string]string{
		"x-session-id":       hash,
		"x-session-affinity": hash,
	}
}

func (a *sessionAgent) createUserMessage(ctx context.Context, call SessionAgentCall, modelContext string) (message.Message, error) {
	if call.Report != nil {
		return a.messages.Create(ctx, call.SessionID, message.CreateMessageParams{
			Role:  message.User,
			Parts: []message.ContentPart{*call.Report},
		})
	}
	parts := []message.ContentPart{message.TextContent{Text: call.Prompt, Hidden: call.HiddenUserMessage, Context: modelContext}}
	var attachmentParts []message.ContentPart
	for _, attachment := range call.Attachments {
		attachmentParts = append(attachmentParts, message.BinaryContent{Path: attachment.FilePath, MIMEType: attachment.MimeType, Data: attachment.Content})
	}
	parts = append(parts, attachmentParts...)
	msg, err := a.messages.Create(ctx, call.SessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: parts,
	})
	if err != nil {
		return message.Message{}, fmt.Errorf("failed to create user message: %w", err)
	}
	return msg, nil
}

func (a *sessionAgent) preparePrompt(msgs []message.Message, supportsImages, supportsAudio bool, attachments ...message.Attachment) ([]fantasy.Message, []fantasy.FilePart) {
	var history []fantasy.Message
	if !a.isSubAgent {
		history = append(history, fantasy.NewUserMessage(
			fmt.Sprintf(
				"<system_reminder>%s</system_reminder>",
				`This is a reminder that your todo list is currently empty. DO NOT mention this to the user explicitly because they are already aware.
If you are working on tasks that would benefit from a todo list please use the "todos" tool to create one.
If not, please feel free to ignore. Again do not mention this message to the user.`,
			),
		))
	}
	// 先收集 assistant 消息里出现的所有工具调用 ID，再按调用 ID 给每个工具结果建索引。
	// 工具结果会紧跟在"发出该调用的 assistant 消息"之后重新发出，而不是按它存储时的
	// 位置：消息可能并发写入同一个 session（例如工具还在跑时恢复了会话），用户消息
	// 就会插到工具调用和它的结果之间。LLM API 要求每个工具调用后面必须紧跟它的结果、
	// 中间不能有其他消息；像 Kimi、DeepSeek 这类严格要求相邻的 provider 否则会拒绝
	// 请求，把 session 永久锁死。
	knownToolCallIDs := make(map[string]struct{})
	for _, m := range msgs {
		if m.Role != message.Assistant {
			continue
		}
		for _, tc := range m.ToolCalls() {
			knownToolCallIDs[tc.ID] = struct{}{}
		}
	}
	toolResultsByCall := make(map[string]fantasy.ToolResultPart)
	for _, m := range msgs {
		if m.Role != message.Tool {
			continue
		}
		for _, aiMsg := range m.ToAIMessage() {
			for _, part := range aiMsg.Content {
				tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
				if !ok {
					// 目前 tool 角色的 ToAIMessage 只会产出 ToolResultPart；
					// 记日志，避免意外类型被悄悄丢弃。
					slog.Warn(
						"Dropping unexpected non-tool-result part from tool message",
						"part_type", fmt.Sprintf("%T", part),
					)
					continue
				}
				if _, known := knownToolCallIDs[tr.ToolCallID]; !known {
					slog.Warn(
						"Dropping orphaned tool result with no matching tool call",
						"tool_call_id", tr.ToolCallID,
					)
					continue
				}
				// 每个调用只保留一个结果：真实结果优先于错误，因为常见的重复
				// 是先来一个过早的"interrupted"，随后才是真正的输出。
				if kept, seen := toolResultsByCall[tr.ToolCallID]; seen {
					_, keptIsError := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](kept.Output)
					slog.Warn("Dropping duplicate tool result", "session_id", m.SessionID,
						"tool_call_id", tr.ToolCallID, "kept_later", keptIsError)
					if !keptIsError {
						continue
					}
				}
				toolResultsByCall[tr.ToolCallID] = tr
			}
		}
	}

	for _, m := range msgs {
		if len(m.Parts) == 0 {
			continue
		}
		// 助手 消息 without content 或 工具 calls (cancelled before it returned anything).
		if m.Role == message.Assistant && len(m.ToolCalls()) == 0 && m.Content().Text == "" && m.ReasoningContent().String() == "" {
			continue
		}
		// 工具 results 是 emitted right after their 助手 消息.
		if m.Role == message.Tool {
			continue
		}
		aiMsgs := m.ToAIMessage()
		if !supportsImages {
			for i := range aiMsgs {
				if aiMsgs[i].Role == fantasy.MessageRoleUser {
					aiMsgs[i].Content = filterFileParts(aiMsgs[i].Content)
				}
			}
		}
		history = append(history, aiMsgs...)

		if m.Role == message.Assistant && len(m.ToolCalls()) > 0 {
			history = append(history, toolResultsForCalls(m, toolResultsByCall))
		}
	}

	var files []fantasy.FilePart
	for _, attachment := range attachments {
		if attachment.IsText() {
			continue
		}
		if attachment.IsAudio() {
			if !supportsAudio {
				continue
			}
		} else if !supportsImages {
			continue
		}
		files = append(files, fantasy.FilePart{
			Filename:  attachment.FileName,
			Data:      attachment.Content,
			MediaType: attachment.MimeType,
		})
	}

	return history, files
}

// filterDisabledMCPTools 移除那些通过
// "Toggle MCPs" dialog. override set 是 repository-scoped 和 shared
// by every 会话 在 repository, including 子代理 sessions.
// Connections 是 process-global 和 left untouched; 只 工具 list
// 变更。
func (a *sessionAgent) filterDisabledMCPTools(ctx context.Context, toolList []fantasy.AgentTool) []fantasy.AgentTool {
	disabledServers, err := a.sessions.MCPDisabledServers(ctx)
	if err != nil {
		slog.Error("Failed to list disabled MCP servers", "error", err)
		return toolList
	}
	if len(disabledServers) == 0 {
		return toolList
	}
	disabled := make(map[string]struct{}, len(disabledServers))
	for _, name := range disabledServers {
		disabled[name] = struct{}{}
	}
	filtered := make([]fantasy.AgentTool, 0, len(toolList))
	for _, t := range toolList {
		if mcpTool, ok := t.(*tools.Tool); ok {
			if _, off := disabled[mcpTool.MCP()]; off {
				continue
			}
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// filterFileParts removes fantasy.FilePart entries from slice 的 消息
// parts. Used 到 strip image attachments from historical 用户 messages when
// current 模型 does 不 support them.
func filterFileParts(parts []fantasy.MessagePart) []fantasy.MessagePart {
	filtered := make([]fantasy.MessagePart, 0, len(parts))
	for _, part := range parts {
		if _, ok := fantasy.AsMessagePart[fantasy.FilePart](part); ok {
			continue
		}
		filtered = append(filtered, part)
	}
	return filtered
}

func (a *sessionAgent) getSessionMessages(ctx context.Context, session session.Session) ([]message.Message, error) {
	// 读取 只 tail compacted 会话 actually sends. full
	// transcript cbe tens 的 megabytes on single shared connection.
	msgs, err := a.messages.ListFromSummary(ctx, session.ID, session.SummaryMessageID)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}

	if session.SummaryMessageID != "" {
		summaryMsgIndex := -1
		for i, msg := range msgs {
			if msg.ID == session.SummaryMessageID {
				summaryMsgIndex = i
				break
			}
		}
		if summaryMsgIndex != -1 {
			msgs = msgs[summaryMsgIndex:]
			msgs[0].Role = message.User
		}
	}
	return msgs, nil
}

// hasUserTextMessage reports whether any 用户 消息 在 msgs contains
// text content (as opposed 到 只 shell commands 或 other non-text parts).
func hasUserTextMessage(msgs []message.Message) bool {
	for _, msg := range msgs {
		if msg.Role != message.User {
			continue
		}
		for _, part := range msg.Parts {
			if tc, ok := part.(message.TextContent); ok && tc.Text != "" {
				return true
			}
		}
	}
	return false
}

// GenerateTitle generates 会话 标题 based on initial 提示词.
func (a *sessionAgent) GenerateTitle(ctx context.Context, sessionID string, userPrompt string) {
	a.titles.begin()
	defer a.titles.end()
	a.generateTitle(ctx, sessionID, userPrompt)
}

// generateTitle 是 GenerateTitle without tracking, f或 callers that
// register 用 a.titles themselves.
func (a *sessionAgent) generateTitle(ctx context.Context, sessionID string, userPrompt string) {
	if userPrompt == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, titleTimeout)
	defer cancel()

	// Ensure 会话 always gets 标题 even 如果 every path below
	// fails 或 上下文 是 cancelled before we finish.
	var titleSaved bool
	defer func() {
		if !titleSaved {
			// WithoutCancel al所以 drops 标题 超时's 期限.
			fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := a.sessions.Rename(fallbackCtx, sessionID, DefaultSessionName); err != nil {
				slog.Error("Failed to save fallback session title", "error", err)
			}
		}
	}()

	models := a.models()
	smallModel, largeModel := models.Small, models.Large
	systemPromptPrefix := a.systemPromptPrefix.Get()
	titlePrompt := promptOrBuiltin(a.prompts, "title")

	newAgent := func(m fantasy.LanguageModel, p string, tok int64) fantasy.Agent {
		return fantasy.NewAgent(
			m,
			fantasy.WithSystemPrompt(p+"\n /no_think"),
			fantasy.WithMaxOutputTokens(tok),
			fantasy.WithUserAgent(userAgent),
		)
	}

	streamCall := fantasy.AgentStreamCall{
		Prompt:     fmt.Sprintf("Generate a concise title for the following content:\n\n%s\n <think>\n\n</think>", userPrompt),
		Headers:    sessionHeaders(sessionID),
		MaxRetries: retries(backgroundRetries),
		PrepareStep: func(callCtx context.Context, opts fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
			prepared.Messages = opts.Messages
			if systemPromptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{
					fantasy.NewSystemMessage(systemPromptPrefix),
				}, prepared.Messages...)
			}
			return callCtx, prepared, nil
		},
	}

	type modelAttempt struct {
		name  string
		model Model
	}
	attempts := []modelAttempt{
		{"small", smallModel},
		{"large", largeModel},
	}

	var resp *fantasy.AgentResult
	var err error
	var model Model
	var success bool
	for _, attempt := range attempts {
		tok := int64(40)
		if attempt.model.CatwalkCfg.CanReason() {
			tok = attempt.model.CatwalkCfg.DefaultMaxTokens
		}
		agent := newAgent(attempt.model.Model, titlePrompt, tok)
		resp, err = agent.Stream(ctx, streamCall)
		if err == nil && resp.Response.FinishReason != fantasy.FinishReasonLength {
			model = attempt.model
			slog.Debug("Generated title with " + attempt.name + " model")
			success = true
			break
		}
		if err != nil {
			slog.Error("Error generating title with "+attempt.name+" model; trying next", "err", err)
		} else {
			slog.Error("Title generation hit token limit with " + attempt.name + " model; trying next")
		}
	}
	if !success {
		// deferred fallback will save default 会话 name.
		return
	}

	// Cleup 标题.
	var title string
	title = strings.ReplaceAll(resp.Response.Content.Text(), "\n", " ")

	// Remove thinking tags 如果 present.
	title = thinkTagRegex.ReplaceAllString(title, "")
	title = orphanThinkTagRegex.ReplaceAllString(title, "")

	title = strings.TrimSpace(title)
	if title == "" {
		// LLM returned empty content. Use 提示词 itself as a
		// fallback 标题, truncated 到 50 chars, before resorting to
		// 通用默认值。
		fallback := strings.ReplaceAll(userPrompt, "\n", " ")
		fallback = strings.TrimSpace(fallback)
		if len(fallback) > 50 {
			fallback = ansi.Truncate(fallback, 50, "…")
		}
		title = cmp.Or(fallback, DefaultSessionName)
	}

	// Calculate usage 和 cost.
	var overrideCost *float64
	for _, step := range resp.Steps {
		stepCost := a.openrouterCost(step.ProviderMetadata)
		if stepCost != nil {
			newCost := *stepCost
			if overrideCost != nil {
				newCost += *overrideCost
			}
			overrideCost = &newCost
		}
	}
	if hyperCost := a.hyperStepsCost(model, resp.Steps); hyperCost != nil {
		overrideCost = hyperCost
	}

	cost := usageCost(model.CatwalkCfg.Pricing, resp.TotalUsage)

	// Use override cost 如果 available (e.g., from OpenRouter 或 Hyper Prism).
	if overrideCost != nil {
		cost = *overrideCost
	}

	// 跳过成本累加
	if model.FlatRate {
		cost = 0
	}

	promptTokens := contextTokens(resp.TotalUsage)
	completionTokens := resp.TotalUsage.OutputTokens

	// Atomically 更新 只 标题 和 usage fields 到 avoid overriding other
	// concurrent 会话 updates.
	saveErr := a.sessions.UpdateTitleAndUsage(ctx, sessionID, title, promptTokens, completionTokens, cost)
	if saveErr != nil {
		slog.Error("Failed to save session title and usage", "error", saveErr)
		return
	}
	titleSaved = true
}

func (a *sessionAgent) openrouterCost(metadata fantasy.ProviderMetadata) *float64 {
	openrouterMetadata, ok := metadata[openrouter.Name]
	if !ok {
		return nil
	}

	opts, ok := openrouterMetadata.(*openrouter.ProviderMetadata)
	if !ok {
		return nil
	}
	return &opts.Usage.Cost
}

// extractPrismModel returns ID 和 name 的 模型 那 actually
// served turn, as reported 通过 Hyper Prism 模型 router headers,
// 或 empty strings 当 turn was 不 routed through Prism 模型.
func extractPrismModel(metadata fantasy.ProviderMetadata) (modelID, modelName string) {
	openaiMeta, ok := metadata[openai.Name]
	if !ok {
		return "", ""
	}
	pm, ok := openaiMeta.(*openai.ProviderMetadata)
	if !ok {
		return "", ""
	}
	_ = pm.ExtraField(hyper.PrismModelIDField, &modelID)
	_ = pm.ExtraField(hyper.PrismModelNameField, &modelName)
	return modelID, modelName
}

// modelDisplayName resolves display name 的 模型 from the
// current 配置, falling back 到 模型 ID 当 no 条目 exists or
// 条目 has no name. 结果 是 persisted on 助手 messages
// 所以 UI keeps showing meaningful name after 模型 是 renamed
// 或 removed from 配置.
func (a *sessionAgent) modelDisplayName(model config.SelectedModel) string {
	if a.cfg != nil {
		if cfg := a.cfg.Config(); cfg != nil {
			if m := cfg.GetModel(model.Provider, model.Model); m != nil && m.Name != "" {
				return m.Name
			}
		}
	}
	return model.Model
}

func (a *sessionAgent) modelForStepCost(model Model, metadata fantasy.ProviderMetadata) Model {
	if model.ModelCfg.Provider != hyper.Name || a.cfg == nil {
		return model
	}
	routedModelID, _ := extractPrismModel(metadata)
	if routedModelID == "" {
		return model
	}
	cfg := a.cfg.Config()
	if cfg == nil {
		return model
	}
	routedModel := cfg.GetModel(hyper.Name, routedModelID)
	if routedModel == nil {
		return model
	}
	model.CatwalkCfg = *routedModel
	return model
}

func (a *sessionAgent) hyperStepsCost(model Model, steps []fantasy.StepResult) *float64 {
	if model.ModelCfg.Provider != hyper.Name || len(steps) == 0 {
		return nil
	}
	var cost float64
	for _, step := range steps {
		pricingModel := a.modelForStepCost(model, step.ProviderMetadata)
		cost += usageCost(pricingModel.CatwalkCfg.Pricing, step.Usage)
	}
	return &cost
}

// extractPrismSavings returns hypercredit 和 dollar savings from
// routing turn through Hyper Prism 模型 router, as reported by
// its savings trailers, 或 nil 当 不 reported 或 malformed.
func extractPrismSavings(metadata fantasy.ProviderMetadata) (hypercredits, dollars *float64) {
	openaiMeta, ok := metadata[openai.Name]
	if !ok {
		return nil, nil
	}
	pm, ok := openaiMeta.(*openai.ProviderMetadata)
	if !ok {
		return nil, nil
	}
	return extraFieldFloat(pm, hyper.PrismHypercreditSavingsField), extraFieldFloat(pm, hyper.PrismDollarSavingsField)
}

func extraFieldFloat(pm *openai.ProviderMetadata, key string) *float64 {
	var value string
	if !pm.ExtraField(key, &value) {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		slog.Warn("Could not parse Prism savings", "key", key, "value", value, "error", err)
		return nil
	}
	return &parsed
}

func usageCost(pricing catwalk.Pricing, usage fantasy.Usage) float64 {
	return pricing.CacheCreate/1e6*float64(usage.CacheCreationTokens) +
		pricing.CacheHit/1e6*float64(usage.CacheReadTokens) +
		pricing.Input/1e6*float64(usage.InputTokens) +
		pricing.Output/1e6*float64(usage.OutputTokens)
}

func (a *sessionAgent) updateSessionUsage(model Model, session *session.Session, usage fantasy.Usage, overrideCost *float64, estimated bool) {
	if !usageIsZero(usage) {
		session.EstimatedUsage = estimated
	}

	cost := usageCost(model.CatwalkCfg.Pricing, usage)

	if !estimated {
		a.eventTokensUsed(session.ID, model, usage, cost)
	}

	if estimated {
		cost = 0
	} else {
		// Use override cost 如果 available (e.g., from OpenRouter).
		if overrideCost != nil {
			cost = *overrideCost
		}

		// 跳过成本累加
		if model.FlatRate {
			cost = 0
		}
	}

	session.Cost += cost
	updateSessionTokenCounters(session, usage)
}

// contextTokens returns size 的 提示词 提供方 processed for
// step. Providers 用 提示词 caching (Anthropic, Bedrock, Vercel) 报告
// 提示词 as three disjoint buckets: tokens served from 缓存, tokens
// newly written 到 缓存, 和 uncached remainder. All three occupy
// 上下文 窗口, 所以 all three count. Providers without 缓存 writes
// leave CacheCreationTokens at zero, 和 OpenAI-style providers already
// subtract cached tokens from InputTokens, 所以 nothing 是 counted twice.
func contextTokens(usage fantasy.Usage) int64 {
	return usage.InputTokens + usage.CacheReadTokens + usage.CacheCreationTokens
}

func updateSessionTokenCounters(session *session.Session, usage fantasy.Usage) {
	if usage.OutputTokens != 0 {
		session.CompletionTokens = usage.OutputTokens
	}
	if promptTokens := contextTokens(usage); promptTokens != 0 {
		session.PromptTokens = promptTokens
	}
}

func summaryCompletionTokens(usage fantasy.Usage, summaryMessage message.Message) int64 {
	if usage.OutputTokens != 0 {
		return usage.OutputTokens
	}
	return approxTokenCount(summaryMessage.Content().Text) + approxTokenCount(summaryMessage.ReasoningContent().String())
}

func (a *sessionAgent) Cancel(sessionID string) {
	// Serialize against 派发 handoff 在 运行 所以 accepted ->
	// (取消-on-条目 | queued | 活跃) transition 是 atomic against
	// this 取消. Every 取消 observes at least one of: 活跃
	// 请求, accepted 运行 (recorded as pending 取消), 或 a
	// 队列 条目 it then clears. If none 的 those hold, 空闲 Escape
	// is true no-op 和 must 不 poison next 提示词.
	mu := a.sessionMu(sessionID)
	mu.Lock()
	defer mu.Unlock()

	// 取消 regular requests. Don't use Take() here - we need 条目 to
	// remain 在 activeRequests 所以 IsBusy() returns true until goroutine
	// fully completes (including 错误 handling 那 may access DB).
	// defer 在 processRequest will cleup 条目.
	if ac, ok := a.activeRequests.Get(sessionID); ok && ac != nil {
		slog.Debug("Request cancellation initiated", "session_id", sessionID)
		ac.cancel()
	}

	// Al所以 check f或 summarize requests.
	if ac, ok := a.activeRequests.Get(sessionID + "-summarize"); ok && ac != nil {
		slog.Debug("Summarize cancellation initiated", "session_id", sessionID)
		ac.cancel()
	}

	// Record pending 取消 只 当 dispatched-but-not-yet-活跃
	// 运行 exists. 这 catches runs still 在 goroutine scheduler or
	// about 到 enter 运行's busy-队列 branch, while leaving 空闲
	// 会话 untouched. 活跃 和 accepted 是 不 mutually exclusive:
	// 当 运行 是 活跃 和 follow-up has been accepted, both the
	// 取消 above 和 这 pending record fire.
	//
	// Raise 会话's 取消 标记 到 latest 受理 序号
	// assigned 所以 far. Every 提示词 currently accepted-but-not-yet-
	// 活跃 has 序号 at 或 below 那 value, 所以 one 取消 covers
	// all 的 them; 提示词 accepted after 这 取消 gets strictly
	// higher 序号 和 是 never poisoned. Using max keeps repeated
	// cancels idempotent while same prompts 是 在 flight 和 lets a
	// later 取消 extend coverage 到 prompts accepted since.
	a.acceptedMu.Lock()
	count, ok := a.acceptedRuns.Get(sessionID)
	mark := a.acceptSeqGen
	a.acceptedMu.Unlock()
	if ok && count > 0 {
		slog.Debug("Recording cancel mark for accepted runs", "session_id", sessionID, "count", count, "mark", mark)
		existing, _ := a.cancelMark.Get(sessionID)
		a.cancelMark.Set(sessionID, max(existing, mark))
	}

	if a.QueuedPrompts(sessionID) > 0 {
		slog.Debug("Clearing queued prompts", "session_id", sessionID)
		a.clearQueueAndNotify(sessionID)
	}
}

func (a *sessionAgent) ClearQueue(sessionID string) {
	if a.QueuedPrompts(sessionID) > 0 {
		slog.Debug("Clearing queued prompts", "session_id", sessionID)
		a.clearQueueAndNotify(sessionID)
	}
}

// AddNote keeps text f或 会话's next 提示词, which agent reads
// with it. Nothing runs 和 nothing 是 shown 在 chat.
func (a *sessionAgent) AddNote(sessionID, text string) {
	if text == "" {
		return
	}
	existing, _ := a.notes.Get(sessionID)
	a.notes.Set(sessionID, append(existing, text))
}

// takeNotes returns 和 forgets 会话's 备注, f或 提示词 about 到 运行.
func (a *sessionAgent) takeNotes(sessionID string) []string {
	notes, _ := a.notes.Get(sessionID)
	a.notes.Del(sessionID)
	return notes
}

// CancelAll cancels every 运行 和 waits, up 到 5s 在 total, f或 runs
// 和 any in-flight 标题 generation 到 finish writing.
func (a *sessionAgent) CancelAll() {
	deadline := time.Now().Add(5 * time.Second)
	if a.IsBusy() {
		for key := range a.activeRequests.Seq2() {
			a.Cancel(key) // key is sessionID
		}
		for key := range a.acceptedRuns.Seq2() {
			a.Cancel(key)
		}
		for a.IsBusy() {
			if time.Now().After(deadline) {
				slog.Warn("Gave up waiting for agent runs to cancel; their final state may not be recorded")
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	if !a.titles.wait(time.Until(deadline)) {
		slog.Warn("Gave up waiting for session title generation to finish")
	}
}

// IsBusy reports whether any 会话 has 活跃 或 accepted 运行.
func (a *sessionAgent) IsBusy() bool {
	for ac := range a.activeRequests.Seq() {
		if ac != nil {
			return true
		}
	}
	return a.acceptedRuns.Len() > 0
}

// HasActiveTurn ignores accepts 所以 运行, holding its own, doesn't 队列 behind itself.
func (a *sessionAgent) HasActiveTurn(sessionID string) bool {
	_, busy := a.activeRequests.Get(sessionID)
	return busy
}

// HasPendingWork reports whether sessionID has 活跃 或 accepted 运行.
func (a *sessionAgent) HasPendingWork(sessionID string) bool {
	if a.HasActiveTurn(sessionID) {
		return true
	}
	accepted, _ := a.acceptedRuns.Get(sessionID)
	return accepted > 0
}

func (a *sessionAgent) QueuedPrompts(sessionID string) int {
	l, ok := a.messageQueue.Get(sessionID)
	if !ok {
		return 0
	}
	return len(l)
}

// QueuedPromptsList returns queued prompts 用户 sees. Hidden
// continuations 是 left out, as they 是 在 transcript, though
// QueuedPrompts 仍会把它们计入。
func (a *sessionAgent) QueuedPromptsList(sessionID string) []string {
	l, ok := a.messageQueue.Get(sessionID)
	if !ok {
		return nil
	}
	prompts := make([]string, 0, len(l))
	for _, call := range l {
		if !call.HiddenUserMessage {
			prompts = append(prompts, call.Prompt)
		}
	}
	return prompts
}

func (a *sessionAgent) SetTools(tools []fantasy.AgentTool) {
	a.tools.SetSlice(tools)
}

func (a *sessionAgent) SetSystemPrompt(systemPrompt string) {
	a.systemPrompt.Set(systemPrompt)
}

func (a *sessionAgent) Model() Model {
	return a.models().Large
}

// convertToToolResult converts fantasy 工具 结果 到 消息 工具 结果.
func (a *sessionAgent) convertToToolResult(result fantasy.ToolResultContent) message.ToolResult {
	baseResult := message.ToolResult{
		ToolCallID: result.ToolCallID,
		Name:       result.ToolName,
		Metadata:   result.ClientMetadata,
	}

	switch result.Result.GetType() {
	case fantasy.ToolResultContentTypeText:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](result.Result); ok {
			baseResult.Content = r.Text
		}
	case fantasy.ToolResultContentTypeError:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result.Result); ok {
			baseResult.Content = r.Error.Error()
			baseResult.IsError = true
		}
	case fantasy.ToolResultContentTypeMedia:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](result.Result); ok {
			switch {
			case r.Data == "":
				slog.Warn("Tool returned media with no data, storing a note instead",
					"tool", result.ToolName, "tool_call_id", result.ToolCallID)
				baseResult.Content = emptyMediaText(r.Text)
			case !stringext.IsValidBase64(r.Data):
				slog.Warn(
					"Tool returned media with invalid base64 data, discarding image",
					"tool", result.ToolName,
					"tool_call_id", result.ToolCallID,
				)
				baseResult.Content = "Tool returned image data with invalid encoding"
				baseResult.IsError = true
			default:
				content := r.Text
				if content == "" {
					content = fmt.Sprintf("Loaded %s content", r.MediaType)
				}
				baseResult.Content = content
				baseResult.Data = r.Data
				baseResult.MIMEType = r.MediaType
			}
		}
	}

	return baseResult
}

// workaroundProviderMediaLimitations converts medicontent 在 工具 results to
// 用户 messages f或 providers 那 don't natively support images 在 工具 results.
//
// Problem: OpenAI, Google, OpenRouter, 和 other OpenAI-compatible providers
// don't support sending images/mediin 工具 结果 messages - they 只 受理
// text 在 工具 results. However, they DO support images 在 用户 messages.
//
// If we send mediin 工具 results 到 these providers, API returns 错误.
//
// Solution: F或 these providers, we:
// 1. Replace mediin 工具 结果 用 text placeholder
// 2. Inject 用户 消息 immediately after 用 image as file attachment
// 3. 这 maintains 工具 execution flow while working around API limitations
//
// Anthropic 和 Bedrock support images natively 在 工具 results, 所以 we skip
// this workaround f或 them.
//
// 转换示例：
//
// BEFORE: [工具 结果: image data]
// AFTER:  [工具 结果: "Image loaded - see attached"], [用户: image attachment]
func (a *sessionAgent) workaroundProviderMediaLimitations(messages []fantasy.Message, largeModel Model) []fantasy.Message {
	providerSupportsMedia := largeModel.ModelCfg.Provider == string(catwalk.InferenceProviderAnthropic) ||
		largeModel.ModelCfg.Provider == string(catwalk.InferenceProviderBedrock) ||
		largeModel.ModelCfg.Provider == string(catwalk.InferenceProviderBedrockEurope)

	if providerSupportsMedia {
		return messages
	}

	supportsImages := largeModel.CatwalkCfg.Capabilities.Vision

	convertedMessages := make([]fantasy.Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Role != fantasy.MessageRoleTool {
			convertedMessages = append(convertedMessages, msg)
			continue
		}

		textParts := make([]fantasy.MessagePart, 0, len(msg.Content))
		var mediaFiles []fantasy.FilePart

		for _, part := range msg.Content {
			toolResult, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			if !ok {
				textParts = append(textParts, part)
				continue
			}

			if media, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](toolResult.Output); ok {
				if !supportsImages {
					// 模型 cannot process images. Replace 用 text
					// placeholder 和 skip creating synthetic 用户
					// 消息 用 FilePart, which would brick the
					// 会话 on text-only models.
					textParts = append(textParts, fantasy.ToolResultPart{
						ToolCallID: toolResult.ToolCallID,
						Output: fantasy.ToolResultOutputContentText{
							Text: "[Image/media content not supported by this model]",
						},
						ProviderOptions: toolResult.ProviderOptions,
					})
					continue
				}

				decoded, err := base64.StdEncoding.DecodeString(media.Data)
				if err != nil {
					slog.Warn("Failed to decode media data", "error", err)
					textParts = append(textParts, part)
					continue
				}

				mediaFiles = append(mediaFiles, fantasy.FilePart{
					Data:      decoded,
					MediaType: media.MediaType,
					Filename:  fmt.Sprintf("tool-result-%s", toolResult.ToolCallID),
				})

				textParts = append(textParts, fantasy.ToolResultPart{
					ToolCallID: toolResult.ToolCallID,
					Output: fantasy.ToolResultOutputContentText{
						Text: "[Image/media content loaded - see attached file]",
					},
					ProviderOptions: toolResult.ProviderOptions,
				})
			} else {
				textParts = append(textParts, part)
			}
		}

		convertedMessages = append(convertedMessages, fantasy.Message{
			Role:    fantasy.MessageRoleTool,
			Content: textParts,
		})

		if len(mediaFiles) > 0 {
			convertedMessages = append(convertedMessages, fantasy.NewUserMessage(
				"Here is the media content from the tool result:",
				mediaFiles...,
			))
		}
	}

	return convertedMessages
}

// buildSummaryPrompt constructs 提示词 text f或 会话 summarization.
func buildSummaryPrompt(todos []session.Todo) string {
	var sb strings.Builder
	sb.WriteString("Provide a detailed summary of our conversation above.")
	if len(todos) > 0 {
		sb.WriteString("\n\n## Current Todo List\n\n")
		for _, t := range todos {
			fmt.Fprintf(&sb, "- [%s] %s\n", t.Status, t.Content)
		}
		sb.WriteString("\nInclude these tasks and their statuses in your summary. ")
		sb.WriteString("Instruct the resuming assistant to use the `todos` tool to continue tracking progress on these tasks.")
	}
	return sb.String()
}

func providerRetryLogFields(err *fantasy.ProviderError, delay time.Duration) []any {
	fields := []any{
		"retry_delay", delay.String(),
	}
	if err == nil {
		return fields
	}
	fields = append(fields, "status_code", err.StatusCode)
	if err.Title != "" {
		fields = append(fields, "title", err.Title)
	}
	if err.Message != "" {
		fields = append(fields, "message", err.Message)
	}
	return fields
}

// sanitizeToolInput validates 工具调用 JSON from 提供方.
// Malformed input 是 replaced 用 empty object 到 prevent
// stuck conversations from truncated 或 malformed 模型 output.
// 第二个返回值表示是否发生了清洗。
func sanitizeToolInput(toolName, toolCallID, input string) (string, bool) {
	if !json.Valid([]byte(input)) {
		slog.Warn(
			"Malformed tool call JSON from provider, replacing with empty object",
			"tool", toolName,
			"id", toolCallID,
			"input_len", len(input),
		)
		return "{}", true
	}
	return input, false
}

// signInAgainMessage tells 用户 how 到 replace credential 提供方
// 在刷新之后仍被拒绝。
func signInAgainMessage(m Model) string {
	name := cmp.Or(m.ProviderName, m.ModelCfg.Provider)
	return fmt.Sprintf("Please sign in to %s again. You can also run \"crush login %s --force\".",
		name, m.ModelCfg.Provider)
}
