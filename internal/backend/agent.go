package backend

import (
	"context"
	"errors"
	"os"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/notify"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/shell"
)

// SendMessage validates 和 accepts 提示词 为 工作区's agent,
// then dispatches 运行 在 goroutine bound 到 工作区 上下文
// 和 returns immediately. 它 does 不 wait 为 LLM turn 到
// 完成: 运行's lifetime 是 owned 通过 工作区, 不 通过
// caller. Errors 从 dispatched 运行 reach observers through
// agent 事件 channels ( notify.TypeAgentError 通知), 不
// through 这 return 值.
//
// SendMessage returns synchronously 当 请求 cannot 是 accepted:
// ErrWorkspaceNotFound 如果 工作区 是 missing, ErrAgentNotInitialized
// 如果 它的 coordinator 是 nil, structural validation errors 从
// agent.ValidateCall (ErrEmptyPrompt, ErrSessionMissing) 当 提示词
// 或 会话 是 missing, 和 ErrWorkspaceClosing 如果 工作区 是
// being torn down.
func (b *Backend) SendMessage(workspaceID string, msg proto.AgentMessage) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	return b.dispatchRun(ws, msg, "")
}

// dispatchRun validates 和 accepts 提示词 为 工作区's agent
// 和 dispatches 运行. 渠道 names MCP 服务端 那 originated
// turn 当 运行 was dispatched 通过 服务端-side 渠道
// injection 路径 (injectChannelMessage); it 是 empty 为 every
// 客户端-initiated 运行 because proto.AgentMessage cannot carry
// 渠道 从 wire, 所以 只 这 package can bind 渠道 到
// 运行.
func (b *Backend) dispatchRun(ws *Workspace, msg proto.AgentMessage, channel string) error {
	coord := ws.Coordinator()
	if coord == nil {
		return ErrAgentNotInitialized
	}

	if err := agent.ValidateCall(agent.SessionAgentCall{
		SessionID:   msg.SessionID,
		Prompt:      msg.Prompt,
		Attachments: proto.AttachmentsToMessage(msg.Attachments),
	}); err != nil {
		return err
	}

	accept := coord.BeginAccepted(msg.SessionID)

	ws.runMu.Lock()
	if ws.closing {
		ws.runMu.Unlock()
		accept.Close()
		return ErrWorkspaceClosing
	}
	ws.runWG.Add(1)
	ws.runMu.Unlock()

	go b.runAgent(ws, coord, msg, channel, accept)
	return nil
}

// runAgent executes accepted agent 运行 为 工作区 在 coord,
// coordinator 那 accepted it. 它 owns 受理 占位 (releasing
// it 在 return) 和 runWG ticket added 通过 SendMessage. 运行 是 bound
// 到 工作区 上下文 所以 它的 lifetime 是 independent 的 any 客户端's
// HTTP 请求.
//
// On non-取消 错误 it surfaces failure 到 observers via
// notify.TypeAgentError 通知 (lossy, best-effort). That alone 是
// 不 reliable 终态 signal: agent-事件 fan-在 uses lossy
// subscribers, 所以 `crush 运行` caller blocking 在 它的 RunID could hang
// 如果 事件 是 dropped. To guarantee termination, 当 msg.RunID 是
// non-empty 和 coordinator did 不 already 发布 运行's
// authoritative 终态 RunComplete (e.g. 错误 was returned before
// sessionAgent.运行 executed, such 作为 readyWg 或 UpdateModels failure),
// runAgent emits errored RunComplete 在 must-投递
// runCompletions 总线 所以 waiter observes deterministic 终态
// 事件. 上下文.Canceled 是 expected (sessionAgent.运行 already
// publishes cancelled 终态 marker) 和 produces no 错误
// 终态 事件.
//
// When msg.RunID 是 non-empty it 是 attached 到 上下文 via
// agent.WithRunID 所以 coordinator can stamp 终态
// notify.RunComplete 事件 用 那 correlator. 一个 运行-完成 marker
// 是 also attached 所以 coordinator can 报告 whether it published
// 终态 事件, letting runAgent avoid duplicate fallback.
func (b *Backend) runAgent(ws *Workspace, coord agent.Coordinator, msg proto.AgentMessage, channel string, accept *agent.AcceptedRun) {
	defer ws.runWG.Done()
	defer accept.Close()

	ctx := ws.ctx
	if msg.HiddenUserMessage {
		ctx = message.WithHiddenUserMessage(ctx)
	}
	if msg.RunID != "" {
		ctx = agent.WithRunID(ctx, msg.RunID)
	}
	ctx = agent.WithRunCompleteMarker(ctx)

	if channel != "" {
		ctx = agent.WithChannel(ctx, channel)
	}
	_, err := coord.RunAccepted(ctx, accept, msg.SessionID, msg.Prompt, proto.AttachmentsToMessage(msg.Attachments)...)
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}

	ws.AgentNotifications().Publish(pubsub.CreatedEvent, notify.Notification{
		SessionID: msg.SessionID,
		RunID:     msg.RunID,
		Type:      notify.TypeAgentError,
		Message:   err.Error(),
	})

	// Reliable 终态 fallback. Only needed 当 RunID waiter
	// exists 和 coordinator has 不 already emitted 运行's
	// 终态 RunComplete; otherwise 这 would 是 duplicate.
	if msg.RunID == "" || agent.RunCompletePublished(ctx) {
		return
	}
	if rc := ws.RunCompletions(); rc != nil {
		rc.PublishMustDeliver(ctx, pubsub.UpdatedEvent, notify.RunComplete{
			SessionID: msg.SessionID,
			RunID:     msg.RunID,
			Error:     err.Error(),
		})
	}
}

// GetAgentInfo returns agent's 模型, busy status, 和 活跃 agent.
func (b *Backend) GetAgentInfo(workspaceID string) (proto.AgentInfo, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return proto.AgentInfo{}, err
	}

	var agentInfo proto.AgentInfo
	if coord := ws.Coordinator(); coord != nil {
		m := coord.Model()
		agentInfo = proto.AgentInfo{
			Model:     m.CatwalkCfg,
			ModelCfg:  m.ModelCfg,
			IsBusy:    coord.IsBusy(),
			IsReady:   true,
			MainAgent: coord.MainAgent(),
		}
	}
	return agentInfo, nil
}

// Coordinator returns 工作区's agent coordinator, 或 nil 当 it
// has 不 been created yet.
func (w *Workspace) Coordinator() agent.Coordinator {
	if w.App == nil {
		return nil
	}
	w.agentMu.RLock()
	defer w.agentMu.RUnlock()
	return w.AgentCoordinator
}

// initAgent creates 工作区's coder agent coordinator 当 it does
// 不 exist yet 和 reports whether 这 调用 created it. An existing
// coordinator 是 never replaced: every 客户端 的 工作区 shares it,
// 和 new one would 不 know runs, queues 和 cancels 的 old one.
// Creating it 是 refused once 工作区 是 closing, 所以 every 写入 的
// coordinator happens before [工作区.Shutdown] reads it.
func (w *Workspace) initAgent(ctx context.Context, interactive bool) (bool, error) {
	if w.Coordinator() != nil {
		return false, nil
	}
	w.agentMu.Lock()
	defer w.agentMu.Unlock()
	if w.AgentCoordinator != nil {
		return false, nil
	}
	w.runMu.Lock()
	closing := w.closing
	w.runMu.Unlock()
	if closing {
		return false, ErrWorkspaceClosing
	}
	if interactive {
		return true, w.InitCoderAgent(ctx)
	}
	return true, w.InitCoderAgentNonInteractive(ctx)
}

// InitAgent makes 工作区's coder agent ready 为 客户端 那 是
// attaching. agent 是 created, 用 interactivity 客户端 asks
// 为, 只 当 工作区 has none yet. Otherwise shared agent 是
// kept 作为 it 是, interactivity included, 和 它的 models 和 tools 是
// refreshed 从 current 配置. 一个 non-interactive 客户端 (`crush 运行`)
// gets one shot 在 工具 palette, 所以 为 it refresh first waits,
// within usual budget, 为 MCP initialization, 作为 non-interactive
// agent's 运行 would.
func (b *Backend) InitAgent(ctx context.Context, workspaceID string, interactive bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	created, err := ws.initAgent(ctx, interactive)
	if err != nil || created {
		return err
	}
	if !interactive {
		if err := mcptools.WaitForInitBudget(ctx, mcptools.InitWaitBudget); err != nil {
			return err
		}
	}
	return ws.Coordinator().UpdateModels(ctx)
}

// UpdateAgent reloads agent 模型 configuration.
func (b *Backend) UpdateAgent(ctx context.Context, workspaceID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	coord := ws.Coordinator()
	if coord == nil {
		return errors.New("agent configuration is missing")
	}
	return coord.UpdateModels(ctx)
}

// SetMainAgent switches 工作区's 活跃 agent (coder 或 plan). 它
// 是 rejected while agent 是 running 所以 switch can never strand
// 运行's queued prompts 在 previous agent — same protection
// TUI gives itself 用 它的 busy check before toggling input mode.
func (b *Backend) SetMainAgent(workspaceID, agentID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	coord := ws.Coordinator()
	if coord == nil {
		return ErrAgentNotInitialized
	}

	if coord.IsBusy() {
		return ErrAgentBusy
	}

	return coord.SetMainAgent(agentID)
}

// CancelSession cancels ongoing agent operation 为 given
// 会话.
func (b *Backend) CancelSession(workspaceID, sessionID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if coord := ws.Coordinator(); coord != nil {
		coord.Cancel(sessionID)
	}
	return nil
}

// SummarizeSession triggers 会话 summarization.
func (b *Backend) SummarizeSession(ctx context.Context, workspaceID, sessionID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	coord := ws.Coordinator()
	if coord == nil {
		return ErrAgentNotInitialized
	}

	return coord.Summarize(ctx, sessionID)
}

// QueuedPrompts returns number 的 queued prompts 为 会话.
func (b *Backend) QueuedPrompts(workspaceID, sessionID string) (int, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return 0, err
	}

	coord := ws.Coordinator()
	if coord == nil {
		return 0, nil
	}

	return coord.QueuedPrompts(sessionID), nil
}

// ClearQueue clears 提示词 队列 为 会话.
func (b *Backend) ClearQueue(workspaceID, sessionID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if coord := ws.Coordinator(); coord != nil {
		coord.ClearQueue(sessionID)
	}
	return nil
}

// AddNote keeps 文本 为 会话's next 提示词. 它 does 不 start 运行.
func (b *Backend) AddNote(workspaceID, sessionID, text string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if ws.AgentCoordinator == nil {
		return ErrAgentNotInitialized
	}
	ws.AgentCoordinator.AddNote(sessionID, text)
	return nil
}

// QueuedPromptsList returns list 的 queued 提示词 strings 为
// 会话.
func (b *Backend) QueuedPromptsList(workspaceID, sessionID string) ([]string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	coord := ws.Coordinator()
	if coord == nil {
		return nil, nil
	}

	return coord.QueuedPromptsList(sessionID), nil
}

// GetDefaultSmallModel returns 默认 small 模型 为 提供方.
func (b *Backend) GetDefaultSmallModel(workspaceID, providerID string) (config.SelectedModel, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return config.SelectedModel{}, err
	}

	return ws.GetDefaultSmallModel(providerID), nil
}

// RunShellCommand runs shell command 在 工作区 目录 和
// persists command + output 作为 用户消息 在 会话. When
// command started 会话, 会话 是 titled after it.
func (b *Backend) RunShellCommand(ctx context.Context, workspaceID string, req proto.ShellCommandRequest) (proto.ShellCommandResponse, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return proto.ShellCommandResponse{}, err
	}

	// Oversized output spills into 工作区 data 目录. Tests
	// build workspaces without 配置 存储; those fall back 到
	// system temp 目录.
	var dataDir string
	if ws.Cfg != nil {
		dataDir = ws.Cfg.Config().Options.DataDirectory
	}

	var persist shell.PersistFunc
	if req.SessionID != "" {
		persist = func(cmd, output string, exitCode int) error {
			return shell.PersistOutput(ctx, ws.Messages, req.SessionID, cmd, output, exitCode, dataDir)
		}
	}

	result, err := shell.RunAndPersist(ctx, shell.RunOptions{
		Command:   req.Command,
		Cwd:       ws.Path,
		Env:       append(os.Environ(), ws.Env...),
		TermWidth: req.TermWidth,
		Coreutils: wsCoreutils(ws),
	}, persist)
	if err != nil {
		return proto.ShellCommandResponse{}, err
	}

	if req.IsFirstMessage && req.SessionID != "" {
		if coord := ws.Coordinator(); coord != nil {
			// In background, 所以 output 是 不 held back 为
			// 标题, 和 bound 到 工作区 rather than 请求:
			// 客户端 hanging up must 不 cut 标题 short, teardown must.
			go coord.GenerateTitle(ws.ctx, req.SessionID, "$ "+req.Command)
		}
	}

	return proto.ShellCommandResponse{
		Output:   result.Output,
		ExitCode: result.ExitCode,
	}, nil
}

// wsCoreutils returns 工作区's coreutils mode, 或 shell 默认
// 为 测试 workspaces built without 配置 存储.
func wsCoreutils(ws *Workspace) shell.CoreutilsMode {
	if ws == nil || ws.Cfg == nil {
		return shell.CoreutilsTurtle
	}
	return ws.Cfg.Config().Options.GetCoreutils()
}
