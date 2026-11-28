// coordinator.go —— 比 sessionAgent 更高一层的协调器。
//
// 负责创建/管理 sessionAgent、解析 provider/model、子 agent 派发、权限与 hook、
// MCP、渠道事件注入等。产物链里它把前端请求转成具体的 agent.Run。

package agent

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/discover"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/oauth/copilot"
	openaioauth "github.com/charmbracelet/crush/internal/oauth/openai"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	openaisdk "github.com/charmbracelet/openai-go/option"
	"github.com/qjebbs/go-jsons"
)

// Coordinator errors.
var (
	errCoderAgentNotConfigured    = errors.New("coder agent not configured")
	errPlanAgentNotConfigured     = errors.New("plan agent not configured")
	errMainAgentNotFound          = errors.New("main agent not found")
	errModelProviderNotConfigured = errors.New("model provider not configured")
	errModelNotSelected           = errors.New("model not selected")
	errModelNotFound              = errors.New("model not found in provider config")
)

// servingProvider returns fantasy 提供方 package 那 serves
// given 提供方 和 模型. Providers 用 bespoke SDKs (Bedrock, Azure,
// Google, Vertex AI, OpenRouter, Vercel, Hyper) 是 selected 通过 ID;
// everything else, gateways like Copilot 和 OpenCode included, 是
// determined 通过 catwalk type, which describes API endpoint
// format 模型 speaks 和 所以 key 它的 提供方 options 是 读取
// 从.
func servingProvider(providerCfg config.ProviderConfig, model catwalk.Model) string {
	switch providerCfg.ID {
	case string(catwalk.InferenceProviderBedrock), string(catwalk.InferenceProviderBedrockEurope):
		return bedrock.Name
	case string(catwalk.InferenceProviderAzure):
		return azure.Name
	case string(catwalk.InferenceProviderGemini), string(catwalk.InferenceProviderVertexAI):
		return google.Name
	case string(catwalk.InferenceProviderOpenRouter):
		return openrouter.Name
	case string(catwalk.InferenceProviderVercel):
		return vercel.Name
	case hyper.Name:
		return hyper.Name
	}

	switch model.EffectiveType(providerCfg.Type) {
	case catwalk.TypeResponses:
		return openai.Name
	case catwalk.TypeMessages:
		return anthropic.Name
	case catwalk.TypeCompletions:
		return openaicompat.Name
	default:
		// Custom 提供方 types (litellm, ollama, lmstudio, ...) 是
		// handled 通过 caller through IsKnownCustomProvider.
		return string(providerCfg.Type)
	}
}

type Coordinator interface {
	SetMainAgent(agentName string) error
	// MainAgent names current main agent, 作为 last set 通过 SetMainAgent.
	MainAgent() string
	Run(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error)
	// RunAccepted runs 调用 那 was already accepted via
	// BeginAccepted 在 fire-和-forget 派发 路径. handle 是
	// 只 carrier 的 受理-state across backend.runAgent /
	// Coordinator / sessionAgent.运行 layers: it reaches
	// sessionAgent.运行 作为 SessionAgentCall.Accepted, where it 是
	// consumed under dispatchMu once accepted -> (取消-在-条目 |
	// queued | 活跃) transition 是 chosen.
	RunAccepted(ctx context.Context, accept *AcceptedRun, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error)
	BeginAccepted(sessionID string) *AcceptedRun
	Cancel(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	// ListSettledMessages reads 会话's transcript, first settling any
	// 工具 calls dead process left without reply.
	ListSettledMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	ClearQueue(sessionID string)
	AddNote(sessionID, text string)
	Summarize(context.Context, string) error
	Model() Model
	UpdateModels(ctx context.Context) error
	GenerateTitle(ctx context.Context, sessionID, prompt string)
	// SetHostContext records what host application has 在 view;
	// each 运行 从 then 在 tells 模型 about it.
	SetHostContext(hc proto.HostContext)
	HostContext() proto.HostContext
}

type coordinator struct {
	cfg          *config.ConfigStore
	sessions     session.Service
	messages     message.Service
	permissions  permission.Service
	questions    question.Service
	history      history.Service
	filetracker  filetracker.Service
	lspManager   *lsp.Manager
	notify       pubsub.Publisher[notify.Notification]
	runComplete  pubsub.Publisher[notify.RunComplete]
	toolProgress pubsub.Publisher[tools.ToolProgress]
	interactive  bool
	shellOwner   any

	// agentMu guards mainAgent 和 mainAgentName: SetMainAgent runs 在
	// HTTP 处理器 goroutines while runs, cancels, 和 probes 读取
	// current agent 从 their own goroutines.
	agentMu       sync.RWMutex
	mainAgent     SessionAgent
	mainAgentName string
	agents        map[string]SessionAgent
	hookRunner    *hooks.Runner

	// models 是 shared 通过 every agent; rebuildMu serializes rebuilds 的 it.
	models    csync.Value[coordinatorModels]
	rebuildMu sync.Mutex

	// awsRefresh shares one AWS SSO login among requests 那 need it.
	awsRefresh singleflight.Group
	// modelsGen counts rebuilds; override models rebuild 当 it moves.
	modelsGen atomic.Uint64
	// overrideModels shares builds 的 per-派发 模型 overrides.
	overrideModelsMu sync.Mutex
	overrideModels   map[overrideModelKey]*overrideModel

	// dispatched tracks detached subagents 通过 parent 会话 和 label.
	dispatched *csync.Map[string, *dispatchedAgent]
	// dispatchMu makes checking label 和 registering it one step.
	dispatchMu sync.Mutex
	// names hands each new 子代理 它的 first name.
	names subagentNamer
	// backgroundAfter 是 how long blocking 子代理 runs before it 是
	// moved 到 background. 它 mirrors bash 工具's threshold 和 是
	// 字段 只 所以 tests can shorten it.
	backgroundAfter time.Duration
	// reconciled records parent sessions already checked 为 subagents
	// previous process left unreported. Keys 是 会话 IDs.
	reconciled sync.Map
	// subSessions counts runs 在 flight 在 each 子代理 会话, 所以
	// busy check sees subagents main agent 是 不 running itself.
	subSessionsMu sync.Mutex
	subSessions   map[string]int
	// settleMu serializes transcript repairs 所以 two readers cannot both
	// settle one orphaned 调用.
	settleMu sync.Mutex

	// Skills discovery results (会话-start snapshot).
	allSkills    []*skills.Skill // Pre-filter: all discovered after dedup.
	activeSkills []*skills.Skill // Post-filter: active skills only.
	skillTracker *skills.Tracker

	// prompts holds each main agent's 提示词 template, 和 skillKeys
	// skill toggle state 它的 系统提示词 was last built 用, 所以
	// toggle rebuilds 提示词 在 next turn. Guarded 通过 skillKeysMu.
	prompts     map[string]*prompt.Prompt
	skillKeys   map[string]string
	skillKeysMu sync.Mutex

	// promptTexts 是 every 提示词's 文本, 读取 once 在 startup.
	promptTexts map[string]string

	readyWg errgroup.Group

	hostContext csync.Value[proto.HostContext]

	// titles 是 shared 通过 every agent built here, sub-agents included, 所以
	// CancelAll 在 current agent drains all 标题 generation.
	titles titleTracker
}

// CoordinatorOptions holds dependencies 为 NewCoordinator. Using
// struct keeps constructor self-documenting 和 avoids long
// positional parameter list.
type CoordinatorOptions struct {
	Config       *config.ConfigStore
	Sessions     session.Service
	Messages     message.Service
	Permissions  permission.Service
	Questions    question.Service
	History      history.Service
	FileTracker  filetracker.Service
	LSPManager   *lsp.Manager
	Notify       pubsub.Publisher[notify.Notification]
	RunComplete  pubsub.Publisher[notify.RunComplete]
	ToolProgress pubsub.Publisher[tools.ToolProgress]
	Skills       *skills.Manager
	HookRunner   *hooks.Runner
	Interactive  bool
	// ShellOwner tags background shells bash 工具 starts, 所以
	// owner can stop 它的 own shells without touching anyone else's.
	ShellOwner any
}

func NewCoordinator(ctx context.Context, opts CoordinatorOptions) (Coordinator, error) {
	// Skills 是 pre-discovered 通过 caller (see app.New /
	// backend.CreateWorkspace) 和 passed 在 via manager. If no
	// manager was provided (legacy callers), fall back 到 在-line
	// discovery 所以 coordinator still works.
	var allSkills, activeSkills []*skills.Skill
	if opts.Skills != nil {
		allSkills = opts.Skills.AllSkills()
		activeSkills = opts.Skills.ActiveSkills()
	} else {
		allSkills, activeSkills = discoverSkills(opts.Config)
	}
	skillTracker := skills.NewTracker(activeSkills)

	c := &coordinator{
		cfg:             opts.Config,
		sessions:        opts.Sessions,
		messages:        opts.Messages,
		permissions:     opts.Permissions,
		questions:       opts.Questions,
		history:         opts.History,
		filetracker:     opts.FileTracker,
		lspManager:      opts.LSPManager,
		notify:          opts.Notify,
		runComplete:     opts.RunComplete,
		toolProgress:    opts.ToolProgress,
		agents:          make(map[string]SessionAgent),
		allSkills:       allSkills,
		activeSkills:    activeSkills,
		skillTracker:    skillTracker,
		interactive:     opts.Interactive,
		hookRunner:      opts.HookRunner,
		shellOwner:      opts.ShellOwner,
		dispatched:      csync.NewMap[string, *dispatchedAgent](),
		backgroundAfter: subAgentBackgroundAfter,
	}

	agentCfg, ok := opts.Config.Config().Agents[config.AgentCoder]
	if !ok {
		return nil, errCoderAgentNotConfigured
	}

	promptTexts, err := loadPromptTexts(c.cfg)
	if err != nil {
		return nil, err
	}
	c.promptTexts = promptTexts

	coderPrompt, err := c.newPrompt("coder", c.skillOverrideOptions()...)
	if err != nil {
		return nil, err
	}

	agent, err := c.buildAgent(ctx, &c.readyWg, coderPrompt, agentCfg, false)
	if err != nil {
		return nil, err
	}
	c.agents[config.AgentCoder] = agent

	planCfg, ok := c.cfg.Config().Agents[config.AgentPlan]
	if !ok {
		return nil, errPlanAgentNotConfigured
	}

	planSystemPrompt, err := c.newPrompt("plan", append(
		c.skillOverrideOptions(),
		prompt.WithToolNames(c.effectiveToolNames(planCfg, false)),
	)...)
	if err != nil {
		return nil, err
	}

	planAgent, err := c.buildAgent(ctx, &c.readyWg, planSystemPrompt, planCfg, false)
	if err != nil {
		return nil, err
	}
	c.agents[config.AgentPlan] = planAgent

	c.prompts = map[string]*prompt.Prompt{
		config.AgentCoder: coderPrompt,
		config.AgentPlan:  planSystemPrompt,
	}
	key := c.skillToggleKey(ctx)
	c.skillKeys = map[string]string{
		config.AgentCoder: key,
		config.AgentPlan:  key,
	}

	c.mainAgent = agent
	c.mainAgentName = config.AgentCoder
	return c, nil
}

// activeAgent returns coordinator's current main agent 和 它的 配置
// name 作为 one snapshot. Callers use snapshot 为 whole operation,
// 所以 SetMainAgent racing mid-flight never splits 运行, 模型 refresh, 或
// summarize across two agents.
func (c *coordinator) activeAgent() (SessionAgent, string) {
	c.agentMu.RLock()
	defer c.agentMu.RUnlock()
	return c.mainAgent, c.mainAgentName
}

// currentAgent returns the current main agent.
func (c *coordinator) currentAgent() SessionAgent {
	agent, _ := c.activeAgent()
	return agent
}

func (c *coordinator) SetMainAgent(agentName string) error {
	c.agentMu.Lock()
	defer c.agentMu.Unlock()
	agent, ok := c.agents[agentName]
	if !ok {
		return fmt.Errorf("%w: %s", errMainAgentNotFound, agentName)
	}
	c.mainAgent = agent
	c.mainAgentName = agentName
	return nil
}

// MainAgent implements Coordinator.
func (c *coordinator) MainAgent() string {
	_, name := c.activeAgent()
	return name
}

// 运行 implements Coordinator.
func (c *coordinator) Run(ctx context.Context, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	return c.run(ctx, nil, nil, sessionID, prompt, attachments...)
}

// RunAccepted implements Coordinator.
func (c *coordinator) RunAccepted(ctx context.Context, accept *AcceptedRun, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	return c.run(ctx, accept, nil, sessionID, prompt, attachments...)
}

// 运行 是 shared implementation behind 运行 和 RunAccepted. When
// 受理 是 non-nil it 是 threaded onto SessionAgentCall 作为
// Accepted 所以 sessionAgent.运行 can consume 受理 占位 under
// dispatchMu; 当 nil ( 在-process/local 路径) no 受理 tracking
// applies.
// runReport delivers 子代理 报告: it folds into turn 在 progress, 或
// starts one.
func (c *coordinator) runReport(ctx context.Context, sessionID string, report message.SubAgentReport) (*fantasy.AgentResult, error) {
	return c.run(ctx, nil, &report, sessionID, report.PromptText())
}

func (c *coordinator) run(ctx context.Context, accept *AcceptedRun, report *message.SubAgentReport, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	if err := c.readyWg.Wait(); err != nil {
		return nil, err
	}

	// MCP servers connect asynchronously (see mcp.Initialize).
	//
	// Interactive runs never wait 为 那 到 finish: 工具 list below
	// 是 built 从 whatever 是 registered right now, servers still
	// connecting 是 simply absent 从 这 运行's palette, 和 they 是
	// picked up 通过 later runs once they register 和 发布
	// EventToolsListChanged. Blocking here froze TUI 为 duration
	// 的 slowest 服务端's connect 超时 whenever 提示词 was sent
	// before initialization finished — most visibly 在 first 消息.
	//
	// Non-interactive runs get single shot 在 工具 palette, 所以 they
	// do wait 为 initialization 到 settle — 但 bounded 通过 InitWaitBudget
	// rather than each 服务端's connect 超时, 所以 服务端 wedged
	// mid-handshake cannot stall headless 运行 为 minutes. Past
	// budget the turn proceeds without the stragglers; their tools simply
	// stay absent 从 这 运行.
	if !c.interactive {
		if err := mcp.WaitForInitBudget(ctx, mcp.InitWaitBudget); err != nil {
			return nil, fmt.Errorf("failed to wait for MCP initialization: %w", err)
		}
	}

	// refresh models before each 运行. Snapshot agent first: 运行,
	// 它的 模型 settings, 和 模型 refresh below must all target
	// same agent even 如果 SetMainAgent swaps main agent mid-flight.
	agent, agentName := c.activeAgent()
	if err := c.updateAgentModels(ctx, agent, agentName); err != nil {
		return nil, fmt.Errorf("failed to update models: %w", err)
	}
	if err := c.refreshSkillPrompt(ctx, agent, agentName); err != nil {
		slog.Error("Failed to rebuild system prompt after skill toggle", "error", err)
	}

	model := agent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return nil, errModelProviderNotConfigured
	}

	mergedOptions, temp, topP, topK, freqPenalty, presPenalty := mergeCallOptions(model, providerCfg)

	// Inject session_id 为 OpenRouter sticky routing (no-op 为 other providers).
	injectOpenrouterSessionID(mergedOptions, sessionID)

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		// NOTE(@andreynering): We don't return here because 事件 handling 到 ask 用户 到 reauthenticate
		// depends 在 flow below. If refresh fails, proceed 用 token we have.
		slog.Error("Failed to refresh OAuth2 token. Proceeding with existing token.", "error", err)
	}

	// Coalesce per-attempt RunComplete payloads 所以 只 final
	// outcome reaches subscribers. Without 这, first attempt's
	// failed RunComplete (unauthorized) would race ahead 的
	// 重试's success, 和 `crush 运行` would exit 在 stale 错误
	// before ever seeing 重试 结果. Each attempt's
	// SessionAgentCall.OnComplete 钩子 overwrites latest; we 发布
	// exactly once after retries resolve, via PublishMustDeliver, 所以
	// momentarily-full 订阅者 缓冲 can't silently drop
	// 终态 事件.
	var (
		latest    notify.RunComplete
		hasLatest bool
	)
	onComplete := func(rc notify.RunComplete) {
		latest = rc
		hasLatest = true
	}
	// Before first turn 在 会话, tell it about any 子代理
	// previous process left without 报告.
	c.reconcileDispatches(ctx, agent, sessionID)

	// Propagate the caller-supplied RunID (set via agent.WithRunID
	// 在 HTTP boundary 在 backend.SendMessage) onto
	// SessionAgentCall 所以 终态 RunComplete 事件 echoes it
	// back. Both attempts 在 重试 chain reuse same RunID;
	// coalesce closure publishes final outcome under 那
	// same correlator.
	runID := RunIDFromContext(ctx)
	channel := ChannelFromContext(ctx)
	c.syncSessionChannel(ctx, sessionID, channel)
	run := func() (*fantasy.AgentResult, error) {
		return agent.Run(ctx, SessionAgentCall{
			SessionID:         sessionID,
			RunID:             runID,
			Channel:           channel,
			Prompt:            prompt,
			HostContext:       c.hostContext.Get(),
			Report:            report,
			HiddenUserMessage: message.HiddenUserMessage(ctx),
			Attachments:       attachments,
			Model:             model,
			MaxOutputTokens:   maxTokens,
			ProviderOptions:   mergedOptions,
			Temperature:       temp,
			TopP:              topP,
			TopK:              callTopK(providerCfg, topK),
			FrequencyPenalty:  freqPenalty,
			PresencePenalty:   presPenalty,
			OnComplete:        onComplete,
			Accepted:          accept,
			OnAuthRefresh:     c.makeAuthRefreshCallback(providerCfg),
		})
	}
	beforeLoaded := c.skillTracker.LoadedNames()
	result, originalErr := run()
	logTurnSkillUsage(sessionID, prompt, c.activeSkills, c.skillTracker, beforeLoaded)

	c.askToSignInAgain(originalErr, model)

	if hasLatest && c.runComplete != nil {
		c.runComplete.PublishMustDeliver(ctx, pubsub.UpdatedEvent, latest)
		// Signal 到 dispatcher (backend.runAgent) 那
		// authoritative 终态 RunComplete 为 这 运行 was already
		// emitted, 所以 it does 不 发布 duplicate fallback 为
		// 错误 it 是 about 到 receive.
		MarkRunCompletePublished(ctx)
	}
	return result, originalErr
}

// syncSessionChannel reconciles 会话's persisted 渠道 binding 用
// origin 的 turn about 到 运行. 一个 渠道-originated turn (re)binds
// 会话 到 那 渠道 — newest push wins — 和 local turn
// clears stale binding, since 会话 是 no longer 渠道-driven once
// 用户 takes it over directly. 这 是 binding's whole lifecycle:
// it 是 只 ever reflection 的 most recent turn's origin, 和
// column 是 dropped 用 会话 行 当 会话 是 deleted, 所以
// there 是 no separate state 到 reap.
//
// Failures 是 logged 和 turn proceeds: binding 是 provenance 为
// reply routing, 不 precondition 为 running.
func (c *coordinator) syncSessionChannel(ctx context.Context, sessionID, channel string) {
	sess, err := c.sessions.Get(ctx, sessionID)
	if err != nil {
		// 一个 missing 会话 是 expected (it may 是 created later 在
		// 运行), 但 real 数据库 failure would otherwise 是 silent.
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("Failed to load session for channel binding sync",
				"session", sessionID, "channel", channel, "error", err)
		}
		return
	}
	if sess.Channel == channel {
		return
	}
	if _, err := c.sessions.SetChannel(ctx, sessionID, channel); err != nil {
		slog.Warn("Failed to sync session channel binding",
			"session", sessionID, "channel", channel, "error", err)
	}
}

// effectiveReasoningEffort returns reasoning effort 到 apply 为 提供方 calls.
// 它 prefers 用户-selected effort 当 valid, otherwise 模型 默认 当
// valid, 和 finally falls back 到 first configured reasoning level.
func effectiveReasoningEffort(model Model) string {
	if !model.CatwalkCfg.CanReason() {
		return ""
	}

	levels := model.CatwalkCfg.ReasoningEffortLevels()
	if effort := model.ModelCfg.ReasoningEffort; effort != "" && slices.Contains(levels, effort) {
		return effort
	}
	if effort := model.CatwalkCfg.Reasoning.DefaultEffortLevel; effort != "" && slices.Contains(levels, effort) {
		return effort
	}
	if len(levels) > 0 {
		return levels[0]
	}
	return ""
}

func getProviderOptions(model Model, providerCfg config.ProviderConfig) fantasy.ProviderOptions {
	options := fantasy.ProviderOptions{}

	cfgOpts := []byte("{}")
	providerCfgOpts := []byte("{}")
	catwalkOpts := []byte("{}")

	if model.ModelCfg.ProviderOptions != nil {
		data, err := json.Marshal(model.ModelCfg.ProviderOptions)
		if err == nil {
			cfgOpts = data
		}
	}

	if providerCfg.ProviderOptions != nil {
		data, err := json.Marshal(providerCfg.ProviderOptions)
		if err == nil {
			providerCfgOpts = data
		}
	}

	if model.CatwalkCfg.Options.ProviderOptions != nil {
		data, err := json.Marshal(model.CatwalkCfg.Options.ProviderOptions)
		if err == nil {
			catwalkOpts = data
		}
	}

	readers := []io.Reader{
		bytes.NewReader(catwalkOpts),
		bytes.NewReader(providerCfgOpts),
		bytes.NewReader(cfgOpts),
	}

	got, err := jsons.Merge(readers)
	if err != nil {
		slog.Error("Could not merge call config", "err", err)
		return options
	}

	mergedOptions := make(map[string]any)

	err = json.Unmarshal([]byte(got), &mergedOptions)
	if err != nil {
		slog.Error("Could not create config for call", "err", err)
		return options
	}

	reasoningEffort := effectiveReasoningEffort(model)
	levels := model.CatwalkCfg.ReasoningEffortLevels()
	shouldSetEffort := model.CatwalkCfg.CanReason() &&
		reasoningEffort != "" &&
		slices.Contains(levels, reasoningEffort)

	switch servingProvider(providerCfg, model.CatwalkCfg) {
	case openai.Name:
		_, hasReasoningEffort := mergedOptions["reasoning_effort"]
		if !hasReasoningEffort && shouldSetEffort {
			mergedOptions["reasoning_effort"] = reasoningEffort
		}
		if model.CatwalkCfg.EffectiveType(providerCfg.Type) == catwalk.TypeResponses {
			if openai.IsResponsesReasoningModel(model.CatwalkCfg.ID) {
				mergedOptions["reasoning_summary"] = "auto"
				mergedOptions["include"] = []openai.IncludeType{openai.IncludeReasoningEncryptedContent}
			}
			parsed, err := openai.ParseResponsesOptions(mergedOptions)
			if err == nil {
				options[openai.Name] = parsed
			}
		} else {
			parsed, err := openai.ParseOptions(mergedOptions)
			if err == nil {
				options[openai.Name] = parsed
			}
		}

	case azure.Name:
		// Azure SDK picks responses API per 模型 用 它的 own
		// heuristic, 所以 options parsing follows same heuristic 到 stay
		// 在 sync 用 requests it sends.
		_, hasReasoningEffort := mergedOptions["reasoning_effort"]
		if !hasReasoningEffort && shouldSetEffort {
			mergedOptions["reasoning_effort"] = reasoningEffort
		}
		if openai.IsResponsesModel(model.CatwalkCfg.ID) {
			if openai.IsResponsesReasoningModel(model.CatwalkCfg.ID) {
				mergedOptions["reasoning_summary"] = "auto"
				mergedOptions["include"] = []openai.IncludeType{openai.IncludeReasoningEncryptedContent}
			}
			parsed, err := openai.ParseResponsesOptions(mergedOptions)
			if err == nil {
				options[azure.Name] = parsed
			}
		} else {
			parsed, err := openai.ParseOptions(mergedOptions)
			if err == nil {
				options[azure.Name] = parsed
			}
		}

	case anthropic.Name, bedrock.Name:
		var (
			_, hasEffort = mergedOptions["effort"]
			_, hasThink  = mergedOptions["thinking"]
			extraBody    = make(map[string]any)
		)

		switch providerCfg.ID {
		case string(catwalk.InferenceProviderAlibabaSingapore), string(catwalk.InferenceProviderAlibabaUS):
			switch {
			case !hasEffort && shouldSetEffort:
				extraBody["reasoning_effort"] = reasoningEffort
			case !hasThink && model.CatwalkCfg.CanReason():
				if model.ModelCfg.Think {
					extraBody["thinking"] = map[string]any{"type": "enabled"}
				} else {
					extraBody["thinking"] = map[string]any{"type": "disabled"}
				}
			}
			mergedOptions["extra_body"] = extraBody

		default:
			switch {
			case !hasEffort && shouldSetEffort:
				mergedOptions["effort"] = reasoningEffort
			case !hasThink && model.ModelCfg.Think:
				mergedOptions["thinking"] = map[string]any{"budget_tokens": 2000}
			}
		}

		parsed, err := anthropic.ParseOptions(mergedOptions)
		if err == nil {
			options[anthropic.Name] = parsed
		}

	case openrouter.Name:
		_, hasReasoning := mergedOptions["reasoning"]
		if !hasReasoning && shouldSetEffort {
			mergedOptions["reasoning"] = map[string]any{
				"enabled": true,
				"effort":  reasoningEffort,
			}
		}
		parsed, err := openrouter.ParseOptions(mergedOptions)
		if err == nil {
			options[openrouter.Name] = parsed
		}

	case vercel.Name:
		_, hasReasoning := mergedOptions["reasoning"]
		if !hasReasoning && shouldSetEffort {
			mergedOptions["reasoning"] = map[string]any{
				"enabled": true,
				"effort":  reasoningEffort,
			}
		}
		parsed, err := vercel.ParseOptions(mergedOptions)
		if err == nil {
			options[vercel.Name] = parsed
		}

	case google.Name:
		_, hasReasoning := mergedOptions["thinking_config"]
		if !hasReasoning {
			if strings.HasPrefix(model.CatwalkCfg.ID, "gemini-2") {
				mergedOptions["thinking_config"] = map[string]any{
					"thinking_budget":  2000,
					"include_thoughts": true,
				}
			} else {
				mergedOptions["thinking_config"] = map[string]any{
					"thinking_level":   reasoningEffort,
					"include_thoughts": true,
				}
			}
		}
		parsed, err := google.ParseOptions(mergedOptions)
		if err == nil {
			options[google.Name] = parsed
		}

	case openaicompat.Name, hyper.Name:
		extraBody := make(map[string]any)

		_, hasReasoningEffort := mergedOptions["reasoning_effort"]
		if !hasReasoningEffort && shouldSetEffort {
			switch providerCfg.ID {
			case string(catwalk.InferenceProviderIoNet):
				extraBody["reasoning"] = map[string]string{"effort": reasoningEffort}
			case string(catwalk.InferenceProviderOpenCodeGo), string(catwalk.InferenceProviderOpenCodeZen):
				// MiniMax models use "thinking" parameter instead 的
				// "reasoning_effort". Other models 在 these providers still
				// use standard 字段.
				if !strings.HasPrefix(strings.ToLower(model.CatwalkCfg.ID), "minimax") {
					mergedOptions["reasoning_effort"] = reasoningEffort
				}
			default:
				mergedOptions["reasoning_effort"] = reasoningEffort
			}
		}

		// "reasoning effort" 是 standard OpenAI 字段, 但 "thinking" 是 不.
		// Setting it 在 right way 为 each 提供方.
		// TODO: Abstract 这 在 Fantasy somehow?
		// TODO: Allow custom providers 到 specify how 到 set 这?
		switch providerCfg.ID {
		case hyper.Name:
			extraBody["thinking"] = model.ModelCfg.Think
		case string(catwalk.InferenceProviderIoNet):
			if _, ok := extraBody["reasoning"]; !ok && model.CatwalkCfg.CanReason() {
				if model.ModelCfg.Think {
					extraBody["reasoning"] = map[string]string{"effort": "medium"}
				} else {
					extraBody["reasoning"] = map[string]string{"effort": "none"}
				}
			}

		case string(catwalk.InferenceProviderZAI), string(catwalk.InferenceProviderDeepSeek):
			if model.ModelCfg.Think || reasoningEffort != "" {
				extraBody["thinking"] = map[string]any{"type": "enabled"}
			} else {
				extraBody["thinking"] = map[string]any{"type": "disabled"}
			}

		case string(catwalk.InferenceProviderFireworks):
			// NOTE: Fireworks break 如果 we set both `reasoning_effort` 和 `thinking`.
			if reasoningEffort == "" {
				if model.ModelCfg.Think {
					extraBody["thinking"] = map[string]any{"type": "enabled"}
				} else {
					extraBody["thinking"] = map[string]any{"type": "disabled"}
				}
			}

		case string(catwalk.InferenceProviderBaseten):
			extraBody["chat_template_args"] = map[string]any{
				"enable_thinking": model.ModelCfg.Think || reasoningEffort != "" && reasoningEffort != "none",
			}

		case string(catwalk.InferenceProviderOpenCodeGo), string(catwalk.InferenceProviderOpenCodeZen):
			// MiniMax M3 uses "thinking" parameter 到 control reasoning.
			// "reasoning_split" must 是 true 所以 thinking content 是 returned
			// 在 "reasoning_content" 字段 instead 的 inline 在 "content".
			if strings.HasPrefix(strings.ToLower(model.CatwalkCfg.ID), "minimax") {
				if model.CatwalkCfg.CanReason() && (model.ModelCfg.Think || reasoningEffort != "") {
					extraBody["thinking"] = map[string]any{"type": "adaptive"}
					extraBody["reasoning_split"] = true
				} else {
					extraBody["thinking"] = map[string]any{"type": "disabled"}
				}
			}

		case string(catwalk.InferenceProviderAlibabaSingapore), string(catwalk.InferenceProviderAlibabaUS):
			if model.CatwalkCfg.CanReason() && !shouldSetEffort {
				extraBody["enable_thinking"] = model.ModelCfg.Think
			}
		}

		mergedOptions["extra_body"] = extraBody

		parsed, err := openaicompat.ParseOptions(mergedOptions)
		if err == nil {
			options[openaicompat.Name] = parsed
		}

	default:
		// Known custom providers (litellm, llamacpp, lmstudio, ollama,
		// omlx) 是 openai-compat under hood.
		if discover.IsKnownCustomProvider(string(providerCfg.Type)) {
			// Set "top_k" under "extra_body", 作为 it 是 不 片段 的 OpenAI protocol
			// 和 will 是 explicitly omitted 通过 Fantasy downstream.
			topK := cmp.Or(model.ModelCfg.TopK, model.CatwalkCfg.Options.TopK)
			if topK != nil {
				extraBody, hasExtraBody := mergedOptions["extra_body"].(map[string]any)
				if !hasExtraBody {
					extraBody = make(map[string]any)
					mergedOptions["extra_body"] = extraBody
				}
				if _, hasTopK := extraBody["top_k"]; !hasTopK {
					extraBody["top_k"] = *topK
				}
			}

			_, hasReasoningEffort := mergedOptions["reasoning_effort"]
			if !hasReasoningEffort && shouldSetEffort {
				mergedOptions["reasoning_effort"] = reasoningEffort
			}

			parsed, err := openaicompat.ParseOptions(mergedOptions)
			if err == nil {
				options[openaicompat.Name] = parsed
			} else {
				if topK != nil {
					slog.Warn(
						"Failed to parse provider_options, falling back to top_k only",
						"provider", providerCfg.ID,
						"error", err,
					)

					fallbackMergeOptions := map[string]any{
						"extra_body": map[string]any{"top_k": *topK},
					}
					parsed, err := openaicompat.ParseOptions(fallbackMergeOptions)
					if err == nil {
						options[openaicompat.Name] = parsed
					} else {
						slog.Warn(
							"Failed to parse fallback provider options, this should never happen",
							"provider", providerCfg.ID,
							"error", err,
						)
					}
				}
			}
		}
	}

	return options
}

// injectOpenrouterSessionID adds session_id 到 OpenRouter ProviderOptions
// ExtraBody 为 sticky routing 和 upstream 会话 grouping.
func injectOpenrouterSessionID(opts fantasy.ProviderOptions, sessionID string) {
	if o, ok := opts[openrouter.Name].(*openrouter.ProviderOptions); ok {
		if o.ExtraBody == nil {
			o.ExtraBody = make(map[string]any)
		}
		if _, exists := o.ExtraBody["session_id"]; !exists {
			o.ExtraBody["session_id"] = session.HashID(sessionID)
		}
	}
}

func mergeCallOptions(model Model, cfg config.ProviderConfig) (fantasy.ProviderOptions, *float64, *float64, *int64, *float64, *float64) {
	modelOptions := getProviderOptions(model, cfg)
	temp := cmp.Or(model.ModelCfg.Temperature, model.CatwalkCfg.Options.Temperature)
	topP := cmp.Or(model.ModelCfg.TopP, model.CatwalkCfg.Options.TopP)
	topK := cmp.Or(model.ModelCfg.TopK, model.CatwalkCfg.Options.TopK)
	freqPenalty := cmp.Or(model.ModelCfg.FrequencyPenalty, model.CatwalkCfg.Options.FrequencyPenalty)
	presPenalty := cmp.Or(model.ModelCfg.PresencePenalty, model.CatwalkCfg.Options.PresencePenalty)
	return modelOptions, temp, topP, topK, freqPenalty, presPenalty
}

// skillOverrideOptions returns 提示词 options carrying
// repository-scoped skill toggle overrides. They 读取 DB 在 Build
// time, same point where 配置's disabled list 是 applied;
// refreshSkillPrompt rebuilds 在 next turn after toggle.
func (c *coordinator) skillOverrideOptions() []prompt.Option {
	disabled := func(ctx context.Context) []string {
		names, err := c.sessions.SkillsDisabled(ctx)
		if err != nil {
			slog.Error("Failed to list disabled skills for prompt", "error", err)
			return nil
		}
		return names
	}
	enabled := func(ctx context.Context) []string {
		names, err := c.sessions.SkillsEnabled(ctx)
		if err != nil {
			slog.Error("Failed to list enabled skills for prompt", "error", err)
			return nil
		}
		return names
	}
	return []prompt.Option{
		prompt.WithWorkingDir(c.cfg.WorkingDir()),
		prompt.WithSkillOverrides(disabled, enabled),
	}
}

// skillToggleKey fingerprints everything 那 decides which skills
// 系统提示词 lists: 配置's disabled list 和 repository
// overrides.
func (c *coordinator) skillToggleKey(ctx context.Context) string {
	var configDisabled []string
	if opts := c.cfg.Config().Options; opts != nil {
		configDisabled = opts.DisabledSkills
	}
	repoDisabled, _ := c.sessions.SkillsDisabled(ctx)
	repoEnabled, _ := c.sessions.SkillsEnabled(ctx)
	return fmt.Sprint(configDisabled, repoDisabled, repoEnabled)
}

// refreshSkillPrompt rebuilds agent's 系统提示词 当 skill
// toggles changed since it was last built, 所以 toggle applies 从
// next turn. Otherwise 提示词 是 left alone, keeping 提供方's
// 提示词 缓存 warm.
func (c *coordinator) refreshSkillPrompt(ctx context.Context, agent SessionAgent, name string) error {
	p, ok := c.prompts[name]
	if !ok {
		return nil
	}
	key := c.skillToggleKey(ctx)

	c.skillKeysMu.Lock()
	defer c.skillKeysMu.Unlock()
	if c.skillKeys[name] == key {
		return nil
	}
	model := agent.Model().Model
	systemPrompt, err := p.Build(ctx, model.Provider(), model.Model(), c.cfg)
	if err != nil {
		return err
	}
	agent.SetSystemPrompt(systemPrompt)
	c.skillKeys[name] = key
	return nil
}

func (c *coordinator) buildAgent(ctx context.Context, ready *errgroup.Group, prompt *prompt.Prompt, agent config.Agent, isSubAgent bool) (SessionAgent, error) {
	return c.buildAgentWithModels(ctx, ready, prompt, agent, isSubAgent, nil)
}

// buildAgentWithModels builds agent whose 模型 set can 是 replaced.
// modelsFn nil keeps coordinator's shared set: main models 为
// coder 和 plan agents, 子代理 models 为 subagents. 一个 派发 用
// 模型 override passes function serving 那 override, 所以 every
// per-turn 模型 读取 picks it up.
func (c *coordinator) buildAgentWithModels(ctx context.Context, ready *errgroup.Group, prompt *prompt.Prompt, agent config.Agent, isSubAgent bool, modelsFn func() ModelSet) (SessionAgent, error) {
	if c.models.Get().large.Model == nil {
		if err := c.rebuildModels(ctx); err != nil {
			return nil, err
		}
	}
	models := c.mainModels
	if isSubAgent {
		models = c.subAgentModels
	}
	if modelsFn != nil {
		models = modelsFn
	}
	large := models().Large

	largeProviderCfg, _ := c.cfg.Config().Providers.Get(large.ModelCfg.Provider)
	result := NewSessionAgent(SessionAgentOptions{
		Models:               models,
		SystemPromptPrefix:   largeProviderCfg.SystemPromptPrefix,
		SystemPrompt:         "",
		IsSubAgent:           isSubAgent,
		DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
		Sessions:             c.sessions,
		Messages:             c.messages,
		Permissions:          c.permissions,
		Cfg:                  c.cfg,
		Prompts:              c.promptTexts,
		Tools:                nil,
		Notify:               c.notify,
		RunComplete:          c.runComplete,
		Hooks:                c.hookRunner,
		titles:               &c.titles,
	})

	// The readiness goroutines below perform one-time setup — building the
	// 系统提示词 和 initial 工具 list — whose results
	// coordinator needs 为 它的 whole lifetime, 所以 they must survive
	// caller's 上下文 being canceled. Several 条目 points build agent
	// 从 short-lived HTTP 请求 上下文: 服务端's
	// InitAgent/UpdateAgent handlers, 和 UpdateModels -> buildTools ->
	// agentTool -> buildAgent 为 子代理. 工具-list build reads
	// MCP registry 作为 it stands; servers still connecting 是 picked up
	// 通过 later runs. WithoutCancel drops cancellation while keeping 上下文
	// values; work 是 local 和 always completes.
	//
	// The goroutines join ready, which the caller owns. Only the coordinator
	// constructor may pass c.readyWg: runs Wait 在 it, 和 WaitGroup must
	// 不 gain work while Wait 是 在 flight. Agents built mid-会话, such
	// 作为 task subagents rebuilt 在 every turn, use group 的 their own.
	initCtx := context.WithoutCancel(ctx)

	ready.Go(func() error {
		systemPrompt, err := prompt.Build(initCtx, large.Model.Provider(), large.Model.Model(), c.cfg)
		if err != nil {
			return err
		}
		result.SetSystemPrompt(systemPrompt)
		return nil
	})

	ready.Go(func() error {
		tools, err := c.buildTools(initCtx, agent, isSubAgent, func() Model { return models().Large })
		if err != nil {
			return err
		}
		result.SetTools(tools)
		return nil
	})

	return result, nil
}

// effectiveToolNames narrows agent's configured 工具 list 到 tools
// 这 coordinator actually attaches. 运行 mode decides how delegation
// reports back: TUI can receive 子代理's 报告 作为 later turn,
// headless 运行 exits before one could arrive, 所以 delegation blocks there 和
// detaches here. Subagents get neither, since they 可以 neither fan out nor
// ask questions.
func (c *coordinator) effectiveToolNames(agentCfg config.Agent, isSubAgent bool) []string {
	return slices.DeleteFunc(slices.Clone(agentCfg.AllowedTools), func(name string) bool {
		switch name {
		case tools.QuestionToolName:
			return isSubAgent || !c.interactive
		case AgentToolName:
			return isSubAgent || c.interactive
		case AgentDispatchToolName, AgentSendToolName, AgentStatusToolName, AgentStopToolName:
			return isSubAgent || !c.interactive
		}
		return false
	})
}

// buildTools builds agent's tools. parentModel reports 模型
// agent runs 在, which dispatched 子代理 inherits.
func (c *coordinator) buildTools(ctx context.Context, agent config.Agent, isSubAgent bool, parentModel func() Model) ([]fantasy.AgentTool, error) {
	allowed := c.effectiveToolNames(agent, isSubAgent)

	var allTools []fantasy.AgentTool
	if slices.Contains(allowed, AgentToolName) {
		agentTool, err := c.agentTool(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		allTools = append(allTools, agentTool)
	}
	if slices.Contains(allowed, AgentDispatchToolName) {
		dispatchTool, err := c.agentDispatchTool(ctx, agent.ID, parentModel)
		if err != nil {
			return nil, err
		}
		allTools = append(allTools, dispatchTool)
	}
	if slices.Contains(allowed, AgentSendToolName) {
		allTools = append(allTools, c.agentSendTool(parentModel))
	}
	if slices.Contains(allowed, AgentStatusToolName) {
		allTools = append(allTools, c.agentStatusTool())
	}
	if slices.Contains(allowed, AgentStopToolName) {
		allTools = append(allTools, c.agentStopTool())
	}

	if slices.Contains(allowed, tools.AgenticFetchToolName) {
		agenticFetchTool, err := c.agenticFetchTool(ctx, nil)
		if err != nil {
			return nil, err
		}
		allTools = append(allTools, agenticFetchTool)
	}

	// Get 模型 name 为 agent
	modelID := ""
	if modelCfg, ok := c.cfg.Config().Models[agent.Model]; ok {
		if model := c.cfg.Config().GetModel(modelCfg.Provider, modelCfg.Model); model != nil {
			modelID = model.ID
		}
	}

	logFile := filepath.Join(c.cfg.Config().Options.DataDirectory, "logs", "evo.log")

	allTools = append(
		allTools,
		tools.NewBashTool(c.permissions, c.cfg.WorkingDir(), c.cfg.Config().Options.DataDirectory, c.cfg.Config().Options.Attribution, modelID, c.cfg.Config().Options.GetMaxBackgroundJobs(), c.cfg.Config().Options.GetCoreutils(), c.cfg.Config().Permissions, c.shellOwner, c.toolProgress),
		tools.NewCrushInfoTool(c.cfg, c.lspManager, c.allSkills, c.activeSkills, c.skillTracker),
		tools.NewCrushLogsTool(logFile),
		tools.NewJobOutputTool(c.cfg.Config().Options.DataDirectory, c.toolProgress),
		tools.NewJobKillTool(),
		tools.NewDownloadTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewEditTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
		tools.NewMultiEditTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
		tools.NewFetchTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewGlobTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Glob),
		tools.NewGrepTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Grep),
		tools.NewLsTool(c.permissions, c.cfg.WorkingDir(), c.cfg.Config().Tools.Ls),
		tools.NewSourcegraphTool(nil),
		tools.NewTodosTool(c.sessions),
		tools.NewViewTool(c.lspManager, c.permissions, c.filetracker, c.skillTracker, c.cfg.WorkingDir(), c.cfg.Config().Options.SkillsPaths...),
		tools.NewWriteTool(c.lspManager, c.permissions, c.history, c.filetracker, c.cfg.WorkingDir()),
	)

	// Interactive-只, 和 never given 到 subagents; effectiveToolNames
	// 是 what decides both.
	if slices.Contains(allowed, tools.QuestionToolName) {
		allTools = append(allTools, tools.NewQuestionTool(c.questions))
	}

	// Add LSP tools 如果 用户 has configured LSPs 或 auto_lsp 是 enabled (nil 或 true).
	if len(c.cfg.Config().LSP) > 0 || c.cfg.Config().Options.AutoLSP == nil || *c.cfg.Config().Options.AutoLSP {
		allTools = append(
			allTools,
			tools.NewDiagnosticsTool(c.lspManager),
			tools.NewReferencesTool(c.lspManager),
			tools.NewLSPRestartTool(c.lspManager),
			tools.NewSymbolsTool(c.lspManager),
			tools.NewDefinitionTool(c.lspManager),
			tools.NewCallHierarchyTool(c.lspManager),
			tools.NewRenameTool(c.lspManager, c.permissions, c.history, c.filetracker),
			tools.NewReplaceSymbolTool(c.lspManager, c.permissions, c.history, c.filetracker),
		)
	}

	if len(c.cfg.Config().MCP) > 0 {
		allTools = append(
			allTools,
			tools.NewListMCPResourcesTool(c.cfg, c.permissions),
			tools.NewReadMCPResourceTool(c.cfg, c.permissions),
		)
	}

	var filteredTools []fantasy.AgentTool
	for _, tool := range allTools {
		if slices.Contains(allowed, tool.Info().Name) {
			filteredTools = append(filteredTools, tool)
		}
	}

	for _, tool := range tools.GetMCPTools(c.permissions, c.cfg, c.cfg.WorkingDir()) {
		if agent.AllowedMCP == nil {
			// No MCP restrictions
			filteredTools = append(filteredTools, tool)
			continue
		}
		if len(agent.AllowedMCP) == 0 {
			// No MCPs allowed
			slog.Debug("No MCPs allowed", "tool", tool.Name(), "agent", agent.Name)
			break
		}

		for mcp, tools := range agent.AllowedMCP {
			if mcp != tool.MCP() {
				continue
			}
			if len(tools) == 0 || slices.Contains(tools, tool.MCPToolName()) {
				filteredTools = append(filteredTools, tool)
				break
			}
			slog.Debug("MCP not allowed", "tool", tool.Name(), "agent", agent.Name)
		}
	}
	slices.SortFunc(filteredTools, func(a, b fantasy.AgentTool) int {
		return strings.Compare(a.Info().Name, b.Info().Name)
	})

	// Wrap tools 用 钩子 interception 为 top-level agent 只.
	// Subagents ( `agent` task 工具, `agentic_fetch`, etc.) 运行
	// without 钩子 interception 到 avoid firing 用户's 钩子 N times
	// per delegated turn. top-level invocation 的 子代理 工具
	// itself 是 still wrapped 从 coder's side.
	filteredTools = wrapToolsWithHooks(filteredTools, c.hookRunner, isSubAgent)

	return filteredTools, nil
}

// buildModel builds 模型 selected 为 one slot 从 live 配置.
// isSubAgent marks requests 作为 agent-initiated 为 providers 那 bill 在 it.
func (c *coordinator) buildModel(ctx context.Context, slot config.SelectedModelType, isSubAgent bool) (Model, error) {
	modelCfg, ok := c.cfg.Config().Models[slot]
	if !ok {
		return Model{}, fmt.Errorf("%s %w", slot, errModelNotSelected)
	}
	return c.buildModelFromSelection(ctx, modelCfg, isSubAgent)
}

// buildModelFromSelection builds 模型 为 explicit selection,
// rather than one 的 configured slots. 一个 per-派发 模型 override
// goes through here, 所以 override gets everything configured 模型
// gets: 提供方 construction, catalog resolution, 请求 timeouts, 和
// 提供方-specific wrappers.
func (c *coordinator) buildModelFromSelection(ctx context.Context, modelCfg config.SelectedModel, isSubAgent bool) (Model, error) {
	providerCfg, ok := c.cfg.Config().Providers.Get(modelCfg.Provider)
	if !ok {
		return Model{}, fmt.Errorf("%s %w", modelCfg.Provider, errModelProviderNotConfigured)
	}
	// Resolve selected 模型 从 提供方's whole catalog:
	// API-key 模型, ChatGPT subscription 模型, 或 Grok one.
	// subscription catalogs live outside Models, 所以 looking 只 there
	// strands Grok/ChatGPT 模型 那 catwalk doesn't also 发布.
	catwalkModel := c.cfg.Config().GetModel(modelCfg.Provider, modelCfg.Model)
	if catwalkModel == nil {
		return Model{}, fmt.Errorf("%s %w", modelCfg.Model, errModelNotFound)
	}

	provider, err := c.buildProvider(providerCfg, modelCfg, isSubAgent, catwalkModel.EffectiveType(providerCfg.Type))
	if err != nil {
		return Model{}, err
	}

	modelID := modelCfg.Model
	if modelCfg.Provider == openrouter.Name && isExactoSupported(modelID) {
		modelID += ":exacto"
	}

	languageModel, err := provider.LanguageModel(ctx, modelID)
	if err != nil {
		return Model{}, err
	}

	// 在轮次取消时结束每个请求，并用配置的超时兜底，这样不可达或挂起的
	// provider 会失败而不是永远卡住会话。额度会按提示词规模放大，
	// 且每次重试都重新计时。
	languageModel = newRequestGuardModel(languageModel, c.cfg.Config().Options.GetRequestTimeout())
	languageModel = newRefusalRetryModel(languageModel)

	// Hyper completions no longer 报告 hypercredit balance, 所以 wrap
	// Hyper models 到 fetch it 从 /v1/credits 在 every 请求.
	if modelCfg.Provider == hyper.Name {
		languageModel = newHyperCreditsModel(languageModel, c.hyperAPIKey)
	}

	return Model{
		Model:        languageModel,
		CatwalkCfg:   *catwalkModel,
		ModelCfg:     modelCfg,
		FlatRate:     providerCfg.FlatRate,
		OAuth:        providerCfg.OAuthToken != nil,
		ProviderName: providerCfg.Name,
	}, nil
}

// hyperAPIKey resolves Hyper API key 从 live 配置, 所以
// OAuth token refreshed after models were built 是 picked up 通过
// next credits fetch.
func (c *coordinator) hyperAPIKey() string {
	return config.ResolveHyperAPIKey(c.cfg.Config())
}

func (c *coordinator) buildAnthropicProvider(baseURL, apiKey string, headers map[string]string, providerID string) (fantasy.Provider, error) {
	var opts []anthropic.Option

	switch {
	case strings.HasPrefix(apiKey, "Bearer "):
		// NOTE: Prevent SDK 从 picking up API key 从 env.
		os.Setenv("ANTHROPIC_API_KEY", "")
		headers["Authorization"] = apiKey
	case providerID == string(catwalk.InferenceProviderMiniMax) || providerID == string(catwalk.InferenceProviderMiniMaxChina):
		// NOTE: Prevent SDK 从 picking up API key 从 env.
		os.Setenv("ANTHROPIC_API_KEY", "")
		headers["Authorization"] = "Bearer " + apiKey
	case apiKey != "":
		// X-Api-Key 请求头
		opts = append(opts, anthropic.WithAPIKey(apiKey))
	}

	if len(headers) > 0 {
		opts = append(opts, anthropic.WithHeaders(headers))
	}

	if baseURL != "" {
		opts = append(opts, anthropic.WithBaseURL(baseURL))
	}

	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
		opts = append(opts, anthropic.WithHTTPClient(httpClient))
	}
	return anthropic.New(opts...)
}

func (c *coordinator) buildOpenaiProvider(baseURL, apiKey string, headers map[string]string, token *oauth.Token, wireType catwalk.Type) (fantasy.Provider, error) {
	opts := []openai.Option{
		openai.WithAPIKey(apiKey),
		openai.WithUseResponsesAPI(),
		// Whether 模型 speaks Responses API comes 从 catwalk's
		// 模型 type, 不 从 模型 ID heuristic.
		openai.WithResponsesAPIFunc(func(string) bool {
			return wireType == catwalk.TypeResponses
		}),
	}
	var httpClient *http.Client
	if c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	if token != nil {
		// ChatGPT OAuth: requests go through the Codex backend, which
		// expects account headers 和 rejects some 请求 fields, 所以
		// they pass through Codex 传输.
		if httpClient == nil {
			httpClient = &http.Client{}
		}
		httpClient.Transport = &openaioauth.Transport{
			Base:  httpClient.Transport,
			Token: token,
		}
	}
	if httpClient != nil {
		opts = append(opts, openai.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, openai.WithHeaders(headers))
	}
	if baseURL != "" {
		opts = append(opts, openai.WithBaseURL(baseURL))
	}
	return openai.New(opts...)
}

func (c *coordinator) buildOpenrouterProvider(_, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	if headers == nil {
		headers = make(map[string]string)
	}
	if _, ok := headers["HTTP-Referer"]; !ok {
		headers["HTTP-Referer"] = "https://charm.land/crush"
	}
	if _, ok := headers["X-Title"]; !ok {
		headers["X-Title"] = "Evo"
	}
	opts := []openrouter.Option{
		openrouter.WithAPIKey(apiKey),
	}
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
		opts = append(opts, openrouter.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, openrouter.WithHeaders(headers))
	}
	return openrouter.New(opts...)
}

func (c *coordinator) buildVercelProvider(_, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	opts := []vercel.Option{
		vercel.WithAPIKey(apiKey),
	}
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
		opts = append(opts, vercel.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, vercel.WithHeaders(headers))
	}
	return vercel.New(opts...)
}

func (c *coordinator) buildOpenaiCompatProvider(baseURL, apiKey string, headers map[string]string, extraBody map[string]any, providerID string, isSubAgent bool, wireType catwalk.Type) (fantasy.Provider, error) {
	opts := []openaicompat.Option{
		openaicompat.WithBaseURL(baseURL),
		openaicompat.WithAPIKey(apiKey),
	}

	// Set HTTP 客户端 based 在 提供方 和 debug mode.
	var httpClient *http.Client
	// Gateway providers route per 模型 between chat completions 和
	// responses API based 在 模型's catwalk type.
	responsesFunc := func(string) bool { return wireType == catwalk.TypeResponses }
	switch providerID {
	case string(catwalk.InferenceProviderCopilot):
		opts = append(
			opts,
			openaicompat.WithUseResponsesAPI(),
			openaicompat.WithResponsesAPIFunc(responsesFunc),
		)
		httpClient = copilot.NewClient(isSubAgent, c.cfg.Config().Options.Debug)

	case string(catwalk.InferenceProviderOpenCodeGo), string(catwalk.InferenceProviderOpenCodeZen):
		opts = append(
			opts,
			openaicompat.WithUseResponsesAPI(),
			openaicompat.WithResponsesAPIFunc(responsesFunc),
		)

	case hyper.Name:
		// Hyper may route requests through Prism 模型; capture
		// router headers 所以 UI can show which 模型 answered.
		opts = append(
			opts,
			openaicompat.WithLanguageModelOptions(
				openai.WithLanguageModelHeaderFunc(hyper.HeaderFunc),
			),
			// Hyper supports both Brotli 和 GZip 为 compressed requests.
			openaicompat.WithRequestCompression(
				openaicompat.EncodingBrotli,
				openaicompat.EncodingGzip,
			),
		)
	}
	if httpClient == nil && c.cfg.Config().Options.Debug {
		httpClient = log.NewHTTPClient()
	}
	if httpClient != nil {
		opts = append(opts, openaicompat.WithHTTPClient(httpClient))
	}

	if len(headers) > 0 {
		opts = append(opts, openaicompat.WithHeaders(headers))
	}

	for extraKey, extraValue := range extraBody {
		opts = append(opts, openaicompat.WithSDKOptions(openaisdk.WithJSONSet(extraKey, extraValue)))
	}

	return openaicompat.New(opts...)
}

func (c *coordinator) buildAzureProvider(baseURL, apiKey string, headers map[string]string, options map[string]string) (fantasy.Provider, error) {
	opts := []azure.Option{
		azure.WithBaseURL(baseURL),
		azure.WithAPIKey(apiKey),
		azure.WithUseResponsesAPI(),
	}
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
		opts = append(opts, azure.WithHTTPClient(httpClient))
	}
	if options == nil {
		options = make(map[string]string)
	}
	if apiVersion, ok := options["apiVersion"]; ok {
		opts = append(opts, azure.WithAPIVersion(apiVersion))
	}
	if len(headers) > 0 {
		opts = append(opts, azure.WithHeaders(headers))
	}

	return azure.New(opts...)
}

func (c *coordinator) buildBedrockProvider(apiKey string, headers map[string]string, providerID string) (fantasy.Provider, error) {
	var opts []bedrock.Option
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
		opts = append(opts, bedrock.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, bedrock.WithHeaders(headers))
	}

	switch {
	case apiKey != "":
		opts = append(opts, bedrock.WithAPIKey(apiKey))
	case os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "":
		opts = append(opts, bedrock.WithAPIKey(os.Getenv("AWS_BEARER_TOKEN_BEDROCK")))
	default:
		// Skip, let SDK do 认证.
	}

	switch providerID {
	case string(catwalk.InferenceProviderBedrockEurope):
		opts = append(opts, bedrock.WithRegion("eu-west-1"))
	default:
		opts = append(opts, bedrock.WithRegion("us-east-1"))
	}

	return bedrock.New(opts...)
}

func (c *coordinator) buildGoogleProvider(baseURL, apiKey string, headers map[string]string) (fantasy.Provider, error) {
	opts := []google.Option{
		google.WithBaseURL(baseURL),
		google.WithGeminiAPIKey(apiKey),
	}
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
		opts = append(opts, google.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, google.WithHeaders(headers))
	}
	return google.New(opts...)
}

func (c *coordinator) buildGoogleVertexProvider(headers map[string]string, options map[string]string) (fantasy.Provider, error) {
	opts := []google.Option{}
	if c.cfg.Config().Options.Debug {
		httpClient := log.NewHTTPClient()
		opts = append(opts, google.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, google.WithHeaders(headers))
	}

	project := options["project"]
	location := options["location"]

	opts = append(opts, google.WithVertex(project, location))

	return google.New(opts...)
}

func (c *coordinator) isAnthropicThinking(model config.SelectedModel) bool {
	if model.Think {
		return true
	}
	opts, err := anthropic.ParseOptions(model.ProviderOptions)
	return err == nil && opts.Thinking != nil
}

func (c *coordinator) buildProvider(providerCfg config.ProviderConfig, model config.SelectedModel, isSubAgent bool, wireType catwalk.Type) (fantasy.Provider, error) {
	wireType = config.NormalizeProviderType(wireType)
	headers := maps.Clone(providerCfg.ExtraHeaders)
	if headers == nil {
		headers = make(map[string]string)
	}

	// handle special headers 为 anthropic
	if wireType == catwalk.TypeMessages && c.isAnthropicThinking(model) {
		if v, ok := headers["anthropic-beta"]; ok {
			headers["anthropic-beta"] = v + ",interleaved-thinking-2025-05-14"
		} else {
			headers["anthropic-beta"] = "interleaved-thinking-2025-05-14"
		}
	}

	apiKey, _ := c.cfg.Resolve(providerCfg.APIKey)
	baseURL, _ := c.cfg.Resolve(providerCfg.BaseURL)

	switch providerCfg.ID {
	case string(catwalk.InferenceProviderBedrock), string(catwalk.InferenceProviderBedrockEurope):
		return c.buildBedrockProvider(apiKey, headers, providerCfg.ID)
	case string(catwalk.InferenceProviderAzure):
		return c.buildAzureProvider(baseURL, apiKey, headers, providerCfg.ExtraParams)
	case string(catwalk.InferenceProviderVertexAI):
		return c.buildGoogleVertexProvider(headers, providerCfg.ExtraParams)
	case string(catwalk.InferenceProviderGemini):
		return c.buildGoogleProvider(baseURL, apiKey, headers)
	case string(catwalk.InferenceProviderOpenRouter):
		return c.buildOpenrouterProvider(baseURL, apiKey, headers)
	case string(catwalk.InferenceProviderVercel):
		return c.buildVercelProvider(baseURL, apiKey, headers)
	case string(catwalk.InferenceProviderCopilot),
		string(catwalk.InferenceProviderOpenCodeGo), string(catwalk.InferenceProviderOpenCodeZen):
		// Messages models 在 OpenCode gateways 是 served 通过
		// Anthropic Messages API 在 endpoint root, without /v1
		// prefix chat completions 和 responses endpoints use.
		if wireType == catwalk.TypeMessages {
			baseURL = strings.TrimSuffix(baseURL, "/v1")
			return c.buildAnthropicProvider(baseURL, apiKey, headers, providerCfg.ID)
		}
		return c.buildOpenaiCompatProvider(baseURL, apiKey, headers, providerCfg.ExtraBody, providerCfg.ID, isSubAgent, wireType)
	case string(catwalk.InferenceProviderOpenAI):
		// 一个 ChatGPT login 是 提供方's single credential: every
		// 请求 goes through Codex backend 用 OAuth token.
		token := providerCfg.OAuthToken
		if token != nil {
			baseURL = openaioauth.CodexBaseURL
			apiKey = token.AccessToken
			headers["originator"] = "crush"
			if token.AccountID != "" {
				headers["chatgpt-account-id"] = token.AccountID
			}
		}
		return c.buildOpenaiProvider(baseURL, apiKey, headers, token, wireType)
	}

	switch wireType {
	case catwalk.TypeMessages:
		return c.buildAnthropicProvider(baseURL, apiKey, headers, providerCfg.ID)
	case catwalk.TypeResponses:
		return c.buildOpenaiProvider(baseURL, apiKey, headers, providerCfg.OAuthToken, wireType)
	case catwalk.TypeCompletions, catwalk.Type(hyper.Name):
		// Hyper's 提供方 advertises 它的 own type rather than wire
		// type; it speaks chat completions.
		switch providerCfg.ID {
		case hyper.Name:
			baseURL = hyper.BaseURL() + "/v1"
			headers["x-crush-id"] = event.GetID()
		case string(catwalk.InferenceProviderZAI):
			if providerCfg.ExtraBody == nil {
				providerCfg.ExtraBody = map[string]any{}
			}
			providerCfg.ExtraBody["tool_stream"] = true
		}
		return c.buildOpenaiCompatProvider(baseURL, apiKey, headers, providerCfg.ExtraBody, providerCfg.ID, isSubAgent, wireType)
	default:
		// Known custom providers (litellm, llamacpp, lmstudio, ollama,
		// omlx) 是 openai-compat under hood.
		if discover.IsKnownCustomProvider(string(providerCfg.Type)) {
			return c.buildOpenaiCompatProvider(baseURL, apiKey, headers, providerCfg.ExtraBody, providerCfg.ID, isSubAgent, wireType)
		}
		return nil, fmt.Errorf("provider type not supported: %q", providerCfg.Type)
	}
}

func isExactoSupported(modelID string) bool {
	supportedModels := []string{
		"moonshotai/kimi-k2-0905",
		"deepseek/deepseek-v3.1-terminus",
		"z-ai/glm-4.6",
		"openai/gpt-oss-120b",
		"qwen/qwen3-coder",
	}
	return slices.Contains(supportedModels, modelID)
}

// BeginAccepted reserves 受理 slot 为 sessionID 在 活跃
// agent 和 returns ownership handle. 它 是 fire-和-forget
// 派发 路径's 只 way 到 标记 运行 作为 accepted-但-不-yet-活跃
// 所以 取消 arriving before 运行 registers 在 activeRequests 是 不
// lost.
func (c *coordinator) BeginAccepted(sessionID string) *AcceptedRun {
	return c.currentAgent().BeginAccepted(sessionID)
}

func (c *coordinator) Cancel(sessionID string) {
	// Detached subagents outlive turn 那 started them, 所以 cancelling
	// parent's turn leaves them running; they 报告 back 当 they
	// finish, like background job. Cancelling 子代理's own 会话
	// stops 那 子代理.
	for _, entry := range c.dispatched.Seq2() {
		if entry.SessionID == sessionID {
			entry.stop("cancelled by the user")
		}
	}
	c.currentAgent().Cancel(sessionID)
}

// CancelAll stops everything, detached subagents included. 它 是
// shutdown 路径, 所以 subagents 是 stopped without reporting: nobody 是
// left 到 读取 报告, 和 next 运行 在 each parent 会话 reports
// them 作为 interrupted instead.
func (c *coordinator) CancelAll() {
	c.currentAgent().CancelAll()
	c.stopAllDispatched(stopForShutdown, 5*time.Second)
}

func (c *coordinator) ClearQueue(sessionID string) {
	c.currentAgent().ClearQueue(sessionID)
}

func (c *coordinator) AddNote(sessionID, text string) {
	c.currentAgent().AddNote(sessionID, text)
}

func (c *coordinator) IsBusy() bool {
	return c.currentAgent().IsBusy()
}

// IsSessionBusy reports whether anything 是 working 在 sessionID, subagents
// included. Readers use it 到 decide whether half-written 会话 是 dead
// 和 safe 到 settle, 所以 it must 不 miss 子代理 running 在 agent
// instance other than the main one.
func (c *coordinator) IsSessionBusy(sessionID string) bool {
	for _, agent := range c.agents {
		if agent.HasPendingWork(sessionID) {
			return true
		}
	}
	return c.subSessionBusy(sessionID)
}

func (c *coordinator) SetHostContext(hc proto.HostContext) { c.hostContext.Set(hc) }

func (c *coordinator) HostContext() proto.HostContext { return c.hostContext.Get() }

func (c *coordinator) Model() Model {
	return c.currentAgent().Model()
}

func (c *coordinator) UpdateModels(ctx context.Context) error {
	// 一个 ChatGPT 或 Grok login without 它的 模型 catalog — fetch 在
	// login failed, 或 credentials predate it — would leave
	// models dialog's subscription section empty. Fill it 在 lazily;
	// guards make 这 no-op once catalog exists.
	c.cfg.RefetchOpenAIChatGPTModels(ctx)
	c.cfg.RefetchGrokModels(ctx)

	agent, name := c.activeAgent()
	return c.updateAgentModels(ctx, agent, name)
}

// updateAgentModels rebuilds shared models 和 given agent's tools
// 从 current 配置.
func (c *coordinator) updateAgentModels(ctx context.Context, agent SessionAgent, name string) error {
	if err := c.rebuildModels(ctx); err != nil {
		return err
	}

	agentCfg, ok := c.cfg.Config().Agents[name]
	if !ok {
		return fmt.Errorf("%w: %s", errMainAgentNotFound, name)
	}

	tools, err := c.buildTools(ctx, agentCfg, false, agent.Model)
	if err != nil {
		return err
	}
	agent.SetTools(tools)
	return nil
}

func (c *coordinator) QueuedPrompts(sessionID string) int {
	return c.currentAgent().QueuedPrompts(sessionID)
}

func (c *coordinator) QueuedPromptsList(sessionID string) []string {
	return c.currentAgent().QueuedPromptsList(sessionID)
}

func (c *coordinator) Summarize(ctx context.Context, sessionID string) error {
	agent := c.currentAgent()
	model := agent.Model()
	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return errModelProviderNotConfigured
	}

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		slog.Error("Failed to refresh OAuth2 token before summarize. Proceeding with existing token.", "error", err)
	}

	// Auth failures during summarize 流程 through fantasy's OnAuthRefresh,
	// same 路径 used 通过 regular turns.
	opts := getProviderOptions(model, providerCfg)
	injectOpenrouterSessionID(opts, sessionID)
	return agent.Summarize(ctx, sessionID, model, opts, c.makeAuthRefreshCallback(providerCfg))
}

// GenerateTitle generates 会话 标题 using current agent.
func (c *coordinator) GenerateTitle(ctx context.Context, sessionID, prompt string) {
	agent := c.currentAgent()
	if agent == nil {
		return
	}
	agent.GenerateTitle(ctx, sessionID, prompt)
}

// refreshTokenIfExpired proactively refreshes OAuth token 如果 it has expired.
func (c *coordinator) refreshTokenIfExpired(ctx context.Context, providerCfg config.ProviderConfig) error {
	if providerCfg.OAuthToken == nil || !providerCfg.OAuthToken.IsExpired() {
		return nil
	}
	slog.Debug("Token needs to be refreshed", "provider", providerCfg.ID)
	return c.refreshOAuth2Token(ctx, providerCfg)
}

// retryAfterUnauthorized attempts 到 refresh credentials after auth 错误
// 和 returns nil 如果 请求 should 是 retried. For OAuth providers whose
// refresh token 是 revoked, 和 为 Bedrock providers whose AWS SSO 会话
// has expired, it triggers interactive re-authentication 和 blocks until
// 用户 completes it (或 上下文 是 cancelled).
func (c *coordinator) retryAfterUnauthorized(ctx context.Context, providerCfg config.ProviderConfig) error {
	switch {
	case providerCfg.OAuthToken != nil:
		slog.Debug("Received 401. Refreshing token and retrying", "provider", providerCfg.ID)
		if err := c.refreshOAuth2Token(ctx, providerCfg); err != nil {
			// If 刷新令牌 was 已撤销, trigger 交互式
			// re-auth 和 wait 为 用户 到 完成 it.
			var exchangeErr *oauth.TokenExchangeError
			if c.notify != nil && errors.As(err, &exchangeErr) && exchangeErr.IsRefreshTokenRevoked() {
				slog.Info("Refresh token revoked, waiting for re-authentication", "provider", providerCfg.ID)
				c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
					Type:       notify.TypeReAuthenticate,
					ProviderID: providerCfg.ID,
				})
				return c.waitForInteractiveReauth(ctx, providerCfg.ID)
			}
			return err
		}
		return nil
	case providerCfg.AWSAuthRefresh != "":
		return c.refreshAWSCredentials(ctx, providerCfg)
	case strings.Contains(providerCfg.APIKeyTemplate, "$"):
		slog.Debug("Received 401. Refreshing API Key template and retrying", "provider", providerCfg.ID)
		return c.refreshApiKeyTemplate(ctx, providerCfg)
	default:
		return nil
	}
}

// errNoInteractiveAuth 是 returned 通过 OnAuthRefresh callback 当
// 提供方 needs interactive re-authentication 但 no notifier 是 available
// 到 drive it (e.g. headless runs). Returning it surfaces original auth
// 错误 rather than retrying.
var errNoInteractiveAuth = errors.New("interactive authentication unavailable")

// waitForInteractiveReauth blocks until interactive re-authentication 为
// 提供方 completes (signalled via SignalAuthComplete) 或 上下文 是
// cancelled, then rebuilds models 所以 next attempt picks up fresh
// credentials. Returns nil 当 caller should 重试.
func (c *coordinator) waitForInteractiveReauth(ctx context.Context, providerID string) error {
	// Use detached 上下文 用 generous 超时 所以 wait survives
	// agent 运行 cancellation. 用户 needs time 到 完成 browser-based
	// 认证.
	waitCtx, waitCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer waitCancel()
	slog.Info("Blocking on WaitForTokenChange", "provider", providerID)
	if waitErr := c.cfg.WaitForTokenChange(waitCtx, providerID); waitErr != nil {
		slog.Info("WaitForTokenChange returned error", "provider", providerID, "error", waitErr)
		return waitErr
	}
	// If original 上下文 was cancelled during wait, fantasy's 重试
	// would fail immediately, 所以 surface cancellation instead.
	if ctx.Err() != nil {
		slog.Warn("Original context cancelled during auth wait, cannot retry",
			"provider", providerID, "ctx_err", ctx.Err())
		return ctx.Err()
	}
	// Rebuild models 所以 ModelProvider picks up fresh credentials.
	if updateErr := c.rebuildModels(waitCtx); updateErr != nil {
		slog.Error("Failed to update models after re-authentication", "error", updateErr)
		return updateErr
	}
	slog.Info("Models updated, returning nil to retry", "provider", providerID)
	return nil
}

// isUnauthorized reports whether err 是 HTTP 401 从 提供方.
func isUnauthorized(err error) bool {
	var providerErr *fantasy.ProviderError
	return errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized
}

// makeAuthRefreshCallback returns OnAuthRefresh callback 为 fantasy 那
// delegates 到 coordinator's existing credential refresh logic. Returns
// nil 如果 no refresh mechanism 是 configured 为 提供方.
func (c *coordinator) makeAuthRefreshCallback(providerCfg config.ProviderConfig) func(context.Context, *fantasy.ProviderError) error {
	if providerCfg.OAuthToken == nil &&
		!strings.Contains(providerCfg.APIKeyTemplate, "$") &&
		providerCfg.AWSAuthRefresh == "" {
		return nil
	}
	return func(ctx context.Context, _ *fantasy.ProviderError) error {
		return c.retryAfterUnauthorized(ctx, providerCfg)
	}
}

func (c *coordinator) refreshOAuth2Token(ctx context.Context, providerCfg config.ProviderConfig) error {
	if err := c.cfg.RefreshOAuthToken(ctx, config.ScopeGlobal, providerCfg.ID); err != nil {
		slog.Error("Failed to refresh OAuth token after 401 error", "provider", providerCfg.ID, "error", err)
		return err
	}
	return c.rebuildModels(ctx)
}

func (c *coordinator) refreshApiKeyTemplate(ctx context.Context, providerCfg config.ProviderConfig) error {
	newAPIKey, err := c.cfg.Resolve(providerCfg.APIKeyTemplate)
	if err != nil {
		slog.Error("Failed to re-resolve API key after 401 error", "provider", providerCfg.ID, "error", err)
		return err
	}

	providerCfg.APIKey = newAPIKey
	c.cfg.Config().Providers.Set(providerCfg.ID, providerCfg)
	return c.rebuildModels(ctx)
}

// askToSignInAgain asks 用户 到 sign 在 again 当 OAuth credential
// 是 still refused after refresh-和-重试.
func (c *coordinator) askToSignInAgain(err error, model Model) {
	if err == nil || !isUnauthorized(err) || c.notify == nil || !model.OAuth {
		return
	}
	c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
		Type:       notify.TypeReAuthenticate,
		ProviderID: model.ModelCfg.Provider,
	})
}

// subAgentParams holds parameters 为 running 子代理.
type subAgentParams struct {
	Agent          SessionAgent
	SessionID      string
	AgentMessageID string
	ToolCallID     string
	Prompt         string
	SessionTitle   string
	// SessionSetup 是 optional callback invoked after 会话 creation
	// 但 before agent execution, 为 custom 会话 configuration.
	SessionSetup func(sessionID string)
	// SkipCostRollup leaves billing parent 到 caller. Detached
	// subagents roll their cost up themselves after every 运行, including
	// runs 那 fail 或 是 stopped 片段-way.
	SkipCostRollup bool
}

// callTopK returns topK 为 use 在 fantasy.调用.TopK, suppressing it 为
// known custom providers: getProviderOptions already carries top_k 为
// them via extra_body, 和 passing it here too makes Fantasy emit
// spurious "top_k unsupported" warning 为 every turn.
func callTopK(providerCfg config.ProviderConfig, topK *int64) *int64 {
	if discover.IsKnownCustomProvider(string(providerCfg.Type)) {
		return nil
	}
	return topK
}

// subAgentCall assembles 调用 子代理 turn runs under: 模型
// settings 为 子代理's own 模型, 不 parent's. Both initial
// task 和 any 消息 sent 到 running 子代理 go through it, 所以
// mid-flight 消息 cannot start turn 用 unconfigured 模型.
func (c *coordinator) subAgentCall(agent SessionAgent, sessionID, prompt string) (SessionAgentCall, error) {
	model := agent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return SessionAgentCall{}, errModelProviderNotConfigured
	}

	opts := getProviderOptions(model, providerCfg)
	injectOpenrouterSessionID(opts, sessionID)

	return SessionAgentCall{
		SessionID:        sessionID,
		Prompt:           prompt,
		Model:            model,
		MaxOutputTokens:  maxTokens,
		ProviderOptions:  opts,
		Temperature:      model.ModelCfg.Temperature,
		TopP:             model.ModelCfg.TopP,
		TopK:             callTopK(providerCfg, model.ModelCfg.TopK),
		FrequencyPenalty: model.ModelCfg.FrequencyPenalty,
		PresencePenalty:  model.ModelCfg.PresencePenalty,
		NonInteractive:   true,
		OnAuthRefresh:    c.makeAuthRefreshCallback(providerCfg),
	}, nil
}

// runSubAgent runs 子代理 和 handles 会话 management 和 cost accumulation.
// 它 creates sub-会话, runs agent 用 given 提示词, 和 propagates
// cost 到 parent 会话.
func (c *coordinator) runSubAgent(ctx context.Context, params subAgentParams) (fantasy.ToolResponse, error) {
	// 创建 sub-会话
	agentToolSessionID := c.sessions.CreateAgentToolSessionID(params.AgentMessageID, params.ToolCallID)
	session, err := c.sessions.CreateTaskSession(ctx, agentToolSessionID, params.SessionID, params.SessionTitle)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("create session: %w", err)
	}

	// 一个 子代理 works 在 它的 parent's behalf, 所以 it inherits parent's
	// automatic approval. crush 运行 approves 它的 own 会话 那 way, 和
	// nobody would 是 there 到 answer 子代理's prompts otherwise.
	c.permissions.InheritAutoApproval(params.SessionID, session.ID)

	// 调用 会话 setup function 如果 provided
	if params.SessionSetup != nil {
		params.SessionSetup(session.ID)
	}

	call, err := c.subAgentCall(params.Agent, session.ID, params.Prompt)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}

	done := c.trackSubSession(session.ID)
	result, err := params.Agent.Run(ctx, call)
	done()
	c.askToSignInAgain(err, call.Model)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to generate response: %s", err)), nil
	}

	// 更新 parent 会话 cost 在 best-effort basis. 一个 failure here must
	// 不 discard 子代理 output 那 was already produced.
	if !params.SkipCostRollup {
		if err := c.updateParentSessionCost(ctx, session.ID, params.SessionID); err != nil {
			slog.Warn(
				"Failed to update parent session cost",
				"child_session", session.ID,
				"parent_session", params.SessionID,
				"error", err,
			)
		}
	}

	return subAgentResponse(result), nil
}

const (
	subAgentNoOutput  = "Subagent completed but produced no text output."
	subAgentRefused   = "Subagent stopped early: the provider's safety classifier refused its response, so it never finished the task. The same task on the same model will likely be refused again; rephrase it or dispatch a new subagent on a different model."
	subAgentTruncated = "Subagent stopped early: it ran out of output tokens mid-response, so it never finished the task. Ask it to continue in smaller steps."
)

// subAgentResponse turns finished 子代理 运行 into parent's 工具
// 结果. 运行's last step decides: fantasy falls back 到 earlier
// step's 文本 当 last one has none, which after cut-short step 是
// narration, 不 报告.
func subAgentResponse(result *fantasy.AgentResult) fantasy.ToolResponse {
	if result == nil {
		return fantasy.NewTextErrorResponse(subAgentNoOutput)
	}
	var last fantasy.StepResult
	if n := len(result.Steps); n > 0 {
		last = result.Steps[n-1]
	}
	switch last.FinishReason {
	case fantasy.FinishReasonContentFilter:
		return fantasy.NewTextErrorResponse(withLastWords(subAgentRefused, last))
	case fantasy.FinishReasonLength:
		return fantasy.NewTextErrorResponse(withLastWords(subAgentTruncated, last))
	}
	if output := result.Response.Content.Text(); output != "" {
		return fantasy.NewTextResponse(output)
	}
	return fantasy.NewTextErrorResponse(subAgentNoOutput)
}

// withLastWords appends whatever 文本 cut-short step did produce.
func withLastWords(reason string, step fantasy.StepResult) string {
	text := strings.TrimSpace(step.Content.Text())
	if text == "" {
		return reason
	}
	return reason + "\n\nIts last output before stopping:\n\n" + text
}

// updateParentSessionCost accumulates cost 从 child 会话 到 它的 parent 会话.
func (c *coordinator) updateParentSessionCost(ctx context.Context, childSessionID, parentSessionID string) error {
	childSession, err := c.sessions.Get(ctx, childSessionID)
	if err != nil {
		return fmt.Errorf("get child session: %w", err)
	}

	parentSession, err := c.sessions.Get(ctx, parentSessionID)
	if err != nil {
		return fmt.Errorf("get parent session: %w", err)
	}

	parentSession.Cost += childSession.Cost

	if _, err := c.sessions.Save(ctx, parentSession); err != nil {
		return fmt.Errorf("save parent session: %w", err)
	}

	return nil
}

// discoverSkills 是 thin fallback wrapper used 只 当 no
// skills.Manager has been threaded through 到 coordinator. All
// production 调用 sites (backend.CreateWorkspace, setupLocalWorkspace)
// 运行 discovery 在 advance 和 pass results via manager;
// reaching 这 路径 means caller bypassed both. 它 deliberately does
// NOT 发布 到 package-level 总线 — there 是 no subscribers 在
// 那 case, 所以 doing 所以 would 是 misleading without delivering
// snapshot anywhere useful.
func discoverSkills(cfg *config.ConfigStore) (allSkills, activeSkills []*skills.Skill) {
	opts := cfg.Config().Options
	var paths, disabled []string
	if opts != nil {
		paths = opts.SkillsPaths
		disabled = opts.DisabledSkills
	}
	var resolver func(string) (string, error)
	if r := cfg.Resolver(); r != nil {
		resolver = r.ResolveValue
	}
	allSkills, activeSkills, states := skills.DiscoverFromConfig(skills.DiscoveryConfig{
		SkillsPaths:    paths,
		DisabledSkills: disabled,
		Resolver:       resolver,
	})
	logDiscoveryStats(states, paths, allSkills, activeSkills, disabled)
	return allSkills, activeSkills
}

// logTurnSkillUsage emits a per-turn diagnostic line showing which skills
// (如果 any) were loaded during 这 turn 和 which looked relevant based 在
// cheap keyword match against 用户 提示词. goal 是 到 surface
// "should-have-loaded 但 didn't" situations 为 later analysis.
//
// Logged 在 Info level under component=skills; heavy fields 是 elided 当
// there 是 nothing interesting 到 报告.
func logTurnSkillUsage(
	sessionID string,
	prompt string,
	activeSkills []*skills.Skill,
	tracker *skills.Tracker,
	before []string,
) {
	if tracker == nil || len(activeSkills) == 0 {
		return
	}

	after := tracker.LoadedNames()

	beforeSet := make(map[string]bool, len(before))
	for _, n := range before {
		beforeSet[n] = true
	}
	var loadedThisTurn []string
	for _, n := range after {
		if !beforeSet[n] {
			loadedThisTurn = append(loadedThisTurn, n)
		}
	}

	slog.Info(
		"Skill turn summary",
		"component", "skills",
		"session_id", sessionID,
		"prompt_len", len(prompt),
		"active_total", len(activeSkills),
		"loaded_total", len(after),
		"loaded_this_turn", loadedThisTurn,
	)
}

// logDiscoveryStats emits a single structured log line summarising skill
// discovery 为 current 会话. 它 是 intentionally low-volume: one
// line per 会话 start. Builtin vs 用户 counts 是 derived 从
// SkillState.路径 — builtin states use "builtin/" embed prefix.
func logDiscoveryStats(
	states []*skills.SkillState,
	userPaths []string,
	allSkills, activeSkills []*skills.Skill,
	disabled []string,
) {
	var builtinOK, builtinErr, userOK, userErr int
	for _, s := range states {
		isBuiltin := strings.HasPrefix(s.Path, "builtin/")
		switch {
		case isBuiltin && s.State == skills.StateNormal:
			builtinOK++
		case isBuiltin && s.State == skills.StateError:
			builtinErr++
		case !isBuiltin && s.State == skills.StateNormal:
			userOK++
		case !isBuiltin && s.State == skills.StateError:
			userErr++
		}
	}

	activeNames := make([]string, 0, len(activeSkills))
	for _, s := range activeSkills {
		activeNames = append(activeNames, s.Name)
	}

	xml := skills.ToPromptXML(activeSkills)

	slog.Info(
		"Skill discovery complete",
		"component", "skills",
		"builtin_ok", builtinOK,
		"builtin_errors", builtinErr,
		"user_ok", userOK,
		"user_errors", userErr,
		"user_paths", len(userPaths),
		"deduped_total", len(allSkills),
		"active", len(activeSkills),
		"disabled", len(disabled),
		"prompt_bytes", len(xml),
		"prompt_tok_est", skills.ApproxTokenCount(xml),
		"active_names", activeNames,
	)
}
