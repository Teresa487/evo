package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"charm.land/log/v2"
	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/format"
	"github.com/charmbracelet/crush/internal/herdr"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/anim"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Aliases: []string{"r"},
	Use:     "run [prompt...]",
	Short:   "Run a single non-interactive prompt",
	Long: `Run a single prompt in non-interactive mode and exit.
The prompt can be provided as arguments or piped from stdin.`,
	Example: `
# Run a simple prompt
crush run "Guess my 5 favorite Pokémon"

# Pipe input from stdin
curl https://charm.land | crush run "Summarize this website"

# Read from a file
crush run "What is this code doing?" <<< prrr.go

# Redirect output to a file
crush run "Generate a hot README for this project" > MY_HOT_README.md

# Run in quiet mode (hide the spinner)
crush run --quiet "Generate a README for this project"

# Run in verbose mode (show logs)
crush run --verbose "Generate a README for this project"

# Use a specific reasoning effort
# Levels depend on the model, unsupported values are rejected
# with the accepted values listed
crush run --reasoning-effort high "What is the meaning of life?"

# Continue a previous session
crush run --session {session-id} "Follow up on your last response"

# Continue the most recent session
crush run --continue "Follow up on your last response"

  `,
	RunE: func(cmd *cobra.Command, args []string) error {
		var (
			quiet, _           = cmd.Flags().GetBool("quiet")
			verbose, _         = cmd.Flags().GetBool("verbose")
			largeModel, _      = cmd.Flags().GetString("model")
			smallModel, _      = cmd.Flags().GetString("small-model")
			reasoningEffort, _ = cmd.Flags().GetString("reasoning-effort")
			sessionID, _       = cmd.Flags().GetString("session")
			useLast, _         = cmd.Flags().GetBool("continue")
		)

		// Stop 在 Ctrl+C, SIGTERM 和 SIGHUP: Execute cancels command's
		// 上下文 在 each 的 them (see terminationSignals).
		ctx := cmd.Context()

		prompt := strings.Join(args, " ")

		prompt, err := MaybePrependStdin(prompt)
		if err != nil {
			slog.Error("Failed to read from stdin", "error", err)
			return err
		}

		if prompt == "" {
			return fmt.Errorf("no prompt provided")
		}

		event.SetNonInteractive(true)

		switch {
		case sessionID != "":
			event.SetContinueBySessionID(true)
		case useLast:
			event.SetContinueLastSession(true)
		}

		clientServer, store, err := clientServerMode(cmd)
		if err != nil {
			return err
		}

		// Only attach 到 服务端 那 是 already running; one-shot
		// 运行 should 不 leave detached 服务端 behind.
		if clientServer && serverRunning(ctx) {
			c, ws, cleanup, err := connectToServer(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			if warn := workspace.ServerVersionWarning(ws.Version); warn != "" {
				fmt.Fprintf(os.Stderr, "warn: %s\n", warn)
			}

			event.AppInitialized()

			if !ws.Config.IsConfigured() {
				return fmt.Errorf("no providers configured - please run 'crush' to set up a provider interactively")
			}

			clientWs := workspace.NewClientWorkspace(c, *ws)
			if err := clientWs.InitCoderAgentNonInteractive(ctx); err != nil {
				return fmt.Errorf("failed to initialize agent: %w", err)
			}

			if sessionID != "" {
				sess, err := resolveSessionByID(ctx, c, ws.ID, sessionID)
				if err != nil {
					return err
				}
				sessionID = sess.ID
			}

			if verbose {
				slog.SetDefault(slog.New(log.New(os.Stderr)))
			}

			return runNonInteractive(ctx, c, ws, prompt, largeModel, smallModel, reasoningEffort, quiet || verbose, sessionID, useLast)
		}

		ws, cleanup, err := setupLocalWorkspace(cmd, store)
		if err != nil {
			return err
		}
		defer cleanup()

		event.AppInitialized()

		if !ws.Config().IsConfigured() {
			return fmt.Errorf("no providers configured - please run 'crush' to set up a provider interactively")
		}

		if verbose {
			slog.SetDefault(slog.New(log.New(os.Stderr)))
		}

		appWs := ws.(*workspace.AppWorkspace)

		if sessionID != "" {
			sess, err := resolveSessionID(ctx, appWs, sessionID)
			if err != nil {
				return err
			}
			sessionID = sess.ID
		}

		return appWs.App().RunNonInteractive(ctx, os.Stdout, prompt, largeModel, smallModel, reasoningEffort, quiet || verbose, sessionID, useLast)
	},
}

func init() {
	runCmd.Flags().BoolP("quiet", "q", false, "Hide spinner")
	runCmd.Flags().BoolP("verbose", "v", false, "Show logs")
	runCmd.Flags().StringP("model", "m", "", "Model to use. Accepts 'model' or 'provider/model' to disambiguate models with the same name across providers")
	runCmd.Flags().String("small-model", "", "Small model to use. If not provided, uses the default small model for the provider")
	runCmd.Flags().String("reasoning-effort", "", "Reasoning effort for the model (e.g. low, medium, high). Levels depend on the model; unsupported values are rejected with the accepted values listed")
	runCmd.Flags().StringP("session", "s", "", "Continue a previous session by ID")
	runCmd.Flags().BoolP("continue", "C", false, "Continue the most recent session")
	runCmd.MarkFlagsMutuallyExclusive("session", "continue")
}

// runNonInteractive executes agent via 服务端 和 streams output
// 到 stdout.
func runNonInteractive(
	ctx context.Context,
	c *client.Client,
	ws *proto.Workspace,
	prompt, largeModel, smallModel, reasoningEffort string,
	hideSpinner bool,
	continueSessionID string,
	useLast bool,
) error {
	slog.Info("Running in non-interactive mode")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if largeModel != "" || smallModel != "" {
		if err := overrideModels(ctx, c, ws, largeModel, smallModel); err != nil {
			return fmt.Errorf("failed to override models: %w", err)
		}
	}

	// reasoning effort applies 到 模型 那 will actually 运行.
	// On continued 会话 without explicit 模型 override,
	// 模型 是 resolved later 从 会话's last 助手消息,
	// 所以 override 是 applied after 那 restore instead.
	deferredEffort := (continueSessionID != "" || useLast) && largeModel == "" && smallModel == ""
	if reasoningEffort != "" && !deferredEffort {
		if err := overrideReasoningEffort(ctx, c, ws.ID, reasoningEffort); err != nil {
			return err
		}
	}

	var (
		spinner   *format.Spinner
		stderrTTY bool
		progress  bool
	)

	stderrTTY = term.IsTerminal(os.Stderr.Fd())
	progress = ws.Config.Options.Progress == nil || *ws.Config.Options.Progress

	if !hideSpinner && stderrTTY {
		t := common.ThemeStylesFromConfig(ws.Config)

		spinner = format.NewSpinner(ctx, cancel, anim.Settings{
			Size:        10,
			Label:       "Generating",
			GradColorA:  t.WorkingGradFromColor,
			GradColorB:  t.WorkingGradToColor,
			CycleColors: true,
		})
		spinner.Start()
	}

	stopSpinner := func() {
		if !hideSpinner && spinner != nil {
			spinner.Stop()
			spinner = nil
		}
	}

	// Wait 为 agent 到 become ready (MCP init, etc).
	if err := waitForAgent(ctx, c, ws.ID); err != nil {
		stopSpinner()
		return fmt.Errorf("agent not ready: %w", err)
	}

	// Force-更新 agent models 所以 MCP tools 是 loaded.
	if err := c.UpdateAgent(ctx, ws.ID); err != nil {
		slog.Warn("Failed to update agent", "error", err)
	}

	defer stopSpinner()

	sess, err := resolveSession(ctx, c, ws.ID, continueSessionID, useLast)
	if err != nil {
		return fmt.Errorf("failed to resolve session: %w", err)
	}
	if continueSessionID != "" || useLast {
		slog.Info("Continuing session for non-interactive run", "session_id", sess.ID)
		// If no explicit 模型 override was requested, restore
		// 模型/提供方 从 last 助手消息 在
		// 会话, provided it 是 still available.
		if largeModel == "" && smallModel == "" {
			if err := restoreModelFromSession(ctx, c, ws, sess.ID); err != nil {
				slog.Warn("Failed to restore model from session", "error", err)
			}
		}
	} else {
		slog.Info("Created session for non-interactive run", "session_id", sess.ID)
	}

	if reasoningEffort != "" && deferredEffort {
		if err := overrideReasoningEffort(ctx, c, ws.ID, reasoningEffort); err != nil {
			return err
		}
	}

	// Automatically approve all 权限 requests 为 这
	// non-interactive 会话, 作为 local mode does: nothing here answers
	// 提示词, 所以 运行 would wait 在 first one forever. 一个 服务端
	// 那 predates endpoint answers 404; carry 在 regardless, since
	// runs 那 need no 权限 still work there.
	if err := c.AutoApproveSession(ctx, ws.ID, sess.ID); err != nil {
		if !errors.Is(err, client.ErrNotFound) {
			return err
		}
		slog.Warn("Server cannot auto-approve permissions; tool calls that need permission will wait for approval",
			"session_id", sess.ID, "error", err)
	}

	events, err := c.SubscribeEvents(ctx, ws.ID)
	if err != nil {
		return fmt.Errorf("failed to subscribe to events: %w", err)
	}

	// 报告 会话 作为 viewed, 所以 服务端 does 不 取消 这
	// 运行 当 another 客户端 viewing it leaves. 它 needs 事件
	// 流 above 到 是 attached.
	if err := c.SetCurrentSession(ctx, ws.ID, sess.ID); err != nil {
		slog.Warn("Failed to report current session", "session_id", sess.ID, "error", err)
	}

	// 服务端 runs 提示词 为 工作区, 不 为 这 请求,
	// 所以 returning 在 interrupt would leave 运行 going 为 作为 long
	// 作为 another 客户端 holds 工作区. 取消 it 在 way out,
	// also 当 interrupt lands while 提示词 是 still 在 flight.
	defer func() {
		if ctx.Err() == nil {
			return
		}
		cancelCtx, cancelDone := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelDone()
		if err := c.CancelAgentSession(cancelCtx, ws.ID, sess.ID); err != nil {
			slog.Warn("Failed to cancel the interrupted run", "session_id", sess.ID, "error", err)
		}
	}()

	// Mint per-调用 RunID 所以 we can correlate 终态
	// RunComplete 用 *这* SendMessage even 如果 会话 was
	// busy 和 another turn finished first. Without it 流
	// loop would exit 在 whichever RunComplete arrived first 为
	// same 会话 和 drop queued 提示词's output.
	runID := uuid.New().String()
	if err := c.SendMessage(ctx, ws.ID, sess.ID, runID, prompt); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	stream := &runStream{
		sessionID: sess.ID,
		runID:     runID,
		out:       os.Stdout,
		seen:      make(map[string]string),
	}

	// Start herdr integration 当 running inside herdr pane.
	hc := herdr.Init()
	hc.SetSessionID(sess.ID)
	defer hc.Close()

	defer func() {
		if progress && stderrTTY {
			_, _ = fmt.Fprintf(os.Stderr, ansi.ResetProgressBar)
		}
		_, _ = fmt.Fprintln(os.Stdout)
	}()

	for {
		if progress && stderrTTY {
			_, _ = fmt.Fprintf(os.Stderr, ansi.SetIndeterminateProgressBar)
		}

		select {
		case ev, ok := <-events:
			if !ok {
				stopSpinner()
				// 流 also ends 当 ctx 是 canceled, which 是
				// caller giving up rather than 服务端 going away.
				if err := ctx.Err(); err != nil {
					return err
				}
				// Otherwise 服务端 died 或 ended 流 before
				// our RunComplete, 所以 运行 cannot pass 作为 finished.
				return fmt.Errorf("lost connection to the crush server before the run completed")
			}

			// Forward events 到 herdr 如果 running inside herdr pane.
			if hev := herdr.Translate(ev); hev != nil {
				hc.HandleEvent(hev)
			}

			cancelOwnQuestion(ctx, c, ws.ID, sess.ID, ev)

			done, err := stream.handle(ev, stopSpinner)
			if err != nil {
				return err
			}
			if done {
				// With ctx still live 取消 did 不 come 从 us
				// 但 从 服务端 shutting down, 工作区
				// being torn down 或 another 客户端, 和 output 是
				// cut short.
				if stream.cancelled && ctx.Err() == nil {
					return fmt.Errorf("agent run was cancelled on the server before it completed")
				}
				return nil
			}

		case <-ctx.Done():
			stopSpinner()
			return ctx.Err()
		}
	}
}

// cancelOwnQuestion cancels ev 当 it 是 question batch agent raised
// 在 这 运行's 会话. 工作区's agent 是 shared 用 interactive
// clients, 所以 it can offer 这 运行 question 工具 too, 但 nobody 是
// here 到 answer: pending question would stall 运行 forever.
// Cancelling it ends the agent's turn.
func cancelOwnQuestion(ctx context.Context, c *client.Client, wsID, sessionID string, ev any) {
	q, ok := ev.(pubsub.Event[proto.QuestionRequest])
	if !ok || q.Payload.SessionID != sessionID {
		return
	}
	slog.Warn("Cancelling a question from the agent: a non-interactive run cannot answer it", "session_id", sessionID)
	if _, err := c.CancelQuestionBatch(ctx, wsID, q.Payload.ID); err != nil {
		slog.Error("Failed to cancel the agent's question", "error", err)
	}
}

// runStream tracks per-消息 stdout cursor 和
// reconciliation state used 通过 [runNonInteractive] 到 translate
// streaming SSE events into final, 完成 stdout 为 `crush 运行`.
// 它 是 split out 所以 state machine can 是 exercised 在 unit tests
// without spinning up full 服务端/客户端 harness.
//
// runID, 当 non-empty, 是 authoritative correlator 为
// 终态 RunComplete 事件: 流 suppresses live 消息
// events 和 只 exits 在 RunComplete whose RunID matches, 所以
// turn 那 finishes first 在 same 会话 (e.g. 当 our 提示词
// was queued behind busy 会话) cannot contaminate stdout 或
// terminate us prematurely. When empty (older servers, tests 那
// don't supply one) 流 falls back 到 SessionID-只 matching
// 和 live 消息 streaming, which 是 still correct 为
// single-turn case.
type runStream struct {
	sessionID string
	runID     string
	out       io.Writer
	seen      map[string]string // text of each message as last seen, by ID
	printed   bool
	cancelled bool // the terminal RunComplete reported a cancelled run
}

// handle processes one SSE 事件. Returns done=true 当 运行
// loop should exit (RunComplete observed); returns 错误 只
// 当 agent 运行 failed (不 在 上下文 取消 — 那 路径 是
// handled 通过 caller's select). stopSpinner 是 called 在
// first observable 助手 output 和 在 completion; passing nil
// 是 safe 为 tests.
//
// 一个 cancelled 运行 是 不 错误 here either: it sets s.cancelled
// 和 caller, which knows whether it asked 为 取消,
// decides.
func (s *runStream) handle(ev any, stopSpinner func()) (done bool, err error) {
	stop := func() {
		if stopSpinner != nil {
			stopSpinner()
		}
	}
	switch e := ev.(type) {
	case pubsub.Event[proto.Message]:
		msg := e.Payload
		if msg.SessionID != s.sessionID || msg.Role != proto.Assistant || len(msg.Parts) == 0 {
			return false, nil
		}
		// 一个 压缩 摘要 是 不 片段 的 answer.
		if msg.IsSummaryMessage {
			return false, nil
		}
		if s.runID != "" {
			return false, nil
		}
		stop()
		s.write(msg.ID, msg.Content().String())
		return false, nil

	case pubsub.Event[proto.RunComplete]:
		// RunComplete 是 authoritative end-的-运行 signal. We
		// exit 在 it instead 的 guessing 从 消息 finish parts,
		// which fire 在 every 工具-调用 step too 和 were
		// source 的 regression where `crush 运行` exited
		// mid-turn 在 finish.reason == tool_use.
		//
		// Correlation:
		// - 如果 we minted RunID 为 这 SendMessage, 只
		// 事件 whose RunID matches 是 ours; any other turn
		// finishing first 在 same 会话 (busy-会话
		// 队列 路径) must 是 ignored.
		// - 如果 we have no RunID (older 服务端, tests), fall back
		// 到 SessionID matching.
		if s.runID != "" {
			if e.Payload.RunID != s.runID {
				return false, nil
			}
		} else if e.Payload.SessionID != s.sessionID {
			return false, nil
		}
		stop()
		if e.Payload.Error != "" && !e.Payload.Cancelled {
			return true, fmt.Errorf("agent run failed: %s", e.Payload.Error)
		}
		s.cancelled = e.Payload.Cancelled
		// Reconcile stdout against the authoritative final
		// 助手 文本 carried 在 事件. pubsub fan-在
		// does 不 serialize publishes across upstream brokers, 所以
		// final 消息 事件 may 不 have reached 这 loop
		// yet; embedded 文本 字段 是 backstop 那
		// guarantees full final 文本 always appears 在 stdout.
		if e.Payload.MessageID != "" {
			s.write(e.Payload.MessageID, e.Payload.Text)
		}
		return true, nil

	case pubsub.Event[proto.AgentEvent]:
		if e.Payload.Error == nil {
			return false, nil
		}
		// Attribute 错误 到 our 运行 before treating it 作为
		// fatal. Async errors 从 unrelated 工作区 运行 share
		// 这 渠道, 所以 foreign failure must 不 abort us:
		// - 如果 事件 carries RunID, it 是 authoritative
		// correlator: it must match our 运行 exactly, otherwise it
		// belongs 到 different 请求 和 we ignore it.
		// - 如果 事件 carries no RunID (older 服务端), fall back
		// 到 SessionID: it must 是 present 和 match our 会话,
		//     otherwise we ignore it.
		if e.Payload.RunID != "" {
			if e.Payload.RunID != s.runID {
				return false, nil
			}
		} else if e.Payload.SessionID == "" || e.Payload.SessionID != s.sessionID {
			return false, nil
		}
		stop()
		return true, fmt.Errorf("agent error: %w", e.Payload.Error)
	}
	return false, nil
}

// 写入 prints 文本 那 content adds 到 what has been seen 的
// 消息 用 given ID. 一个 消息's 文本 normally 只 grows. 文本
// 那 是 prefix 的 what was seen 是 out-的-date 更新 和 adds
// nothing. 文本 那 no longer starts 用 what was seen has been
// replaced, which happens 当 提供方 请求 是 retried after 片段
// 的 reply has streamed. new 文本 是 then printed 在 full, 在
// new line 如果 old 文本 was 不 blank.
func (s *runStream) write(id, content string) {
	if s.seen == nil {
		s.seen = make(map[string]string)
	}
	old := s.seen[id]
	part, ok := strings.CutPrefix(content, old)
	switch {
	case ok:
		// 文本 grew 或 是 unchanged, 和 片段 是 what 是 new.
	case strings.HasPrefix(old, content):
		return
	default:
		// Non-blank old 文本 was printed, 所以 start new line.
		if strings.TrimSpace(old) != "" {
			fmt.Fprintln(s.out)
		}
		part, old = content, ""
	}

	// Trim leading whitespace. Sometimes the LLM includes leading
	// formatting 和 indentation, which we don't want here. Until
	// something has been printed, leading newlines 是 trimmed too.
	switch {
	case !s.printed:
		part = strings.TrimLeft(part, " \t\r\n")
	case strings.TrimLeft(old, " \t") == "":
		part = strings.TrimLeft(part, " \t")
	}
	// Ignore initial whitespace-只 messages.
	if s.printed || strings.TrimSpace(part) != "" {
		s.printed = true
		fmt.Fprint(s.out, part)
	}
	s.seen[id] = content
}

// waitForAgent polls GetAgentInfo until agent 是 ready, 用
// 超时.
func waitForAgent(ctx context.Context, c *client.Client, wsID string) error {
	timeout := time.After(30 * time.Second)
	for {
		info, err := c.GetAgentInfo(ctx, wsID)
		if err == nil && info.IsReady {
			return nil
		}
		select {
		case <-timeout:
			if err != nil {
				return fmt.Errorf("timeout waiting for agent: %w", err)
			}
			return fmt.Errorf("timeout waiting for agent readiness")
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// overrideModels resolves 模型 strings 和 updates 工作区
// configuration via 服务端.
func overrideModels(
	ctx context.Context,
	c *client.Client,
	ws *proto.Workspace,
	largeModel, smallModel string,
) error {
	cfg, err := c.GetConfig(ctx, ws.ID)
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	providers := cfg.Providers.Copy()

	largeMatches, smallMatches := findModelMatches(providers, largeModel, smallModel)

	var largeProviderID string

	if largeModel != "" {
		found, err := validateModelMatches(largeMatches, largeModel, "large")
		if err != nil {
			return err
		}
		largeProviderID = found.provider
		slog.Info("Overriding large model", "provider", found.provider, "model", found.modelID)
		if err := c.UpdatePreferredModel(ctx, ws.ID, config.ScopeWorkspace, config.SelectedModelTypeLarge, config.SelectedModel{
			Provider: found.provider,
			Model:    found.modelID,
		}); err != nil {
			return fmt.Errorf("failed to set large model: %w", err)
		}
	}

	switch {
	case smallModel != "":
		found, err := validateModelMatches(smallMatches, smallModel, "small")
		if err != nil {
			return err
		}
		slog.Info("Overriding small model", "provider", found.provider, "model", found.modelID)
		if err := c.UpdatePreferredModel(ctx, ws.ID, config.ScopeWorkspace, config.SelectedModelTypeSmall, config.SelectedModel{
			Provider: found.provider,
			Model:    found.modelID,
		}); err != nil {
			return fmt.Errorf("failed to set small model: %w", err)
		}

	case largeModel != "":
		sm, err := c.GetDefaultSmallModel(ctx, ws.ID, largeProviderID)
		if err != nil {
			slog.Warn("Failed to get default small model", "error", err)
		} else if sm != nil {
			if err := c.UpdatePreferredModel(ctx, ws.ID, config.ScopeWorkspace, config.SelectedModelTypeSmall, *sm); err != nil {
				return fmt.Errorf("failed to set small model: %w", err)
			}
		}
	}

	return c.UpdateAgent(ctx, ws.ID)
}

// restoreModelFromSession reads last 助手消息 在
// 会话 和, 如果 it used different 提供方/模型 than current
// 配置, updates preferred 模型 在 服务端 provided
// 提供方/模型 是 still available. 这 ensures 那 continuing
// 会话 uses same 模型 那 produced last 响应.
func restoreModelFromSession(ctx context.Context, c *client.Client, ws *proto.Workspace, sessionID string) error {
	msgs, err := c.ListMessages(ctx, ws.ID, sessionID)
	if err != nil {
		return fmt.Errorf("failed to list messages: %w", err)
	}

	var lastAssistant *proto.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == proto.Assistant && !msgs[i].IsSummaryMessage {
			lastAssistant = &msgs[i]
			break
		}
	}
	if lastAssistant == nil || lastAssistant.Provider == "" || lastAssistant.Model == "" {
		return nil
	}

	cfg := ws.Config
	currentLarge := cfg.Models[config.SelectedModelTypeLarge]
	if currentLarge.Provider == lastAssistant.Provider && currentLarge.Model == lastAssistant.Model {
		return nil
	}

	if !cfg.IsModelAvailable(lastAssistant.Provider, lastAssistant.Model) {
		slog.Debug("Skipping model restoration: provider/model not available",
			"provider", lastAssistant.Provider,
			"model", lastAssistant.Model)
		return nil
	}

	selectedModel := config.SelectedModel{
		Provider: lastAssistant.Provider,
		Model:    lastAssistant.Model,
	}
	if err := c.UpdatePreferredModel(ctx, ws.ID, config.ScopeWorkspace, config.SelectedModelTypeLarge, selectedModel); err != nil {
		return fmt.Errorf("failed to set large model: %w", err)
	}

	if _, ok := cfg.Models[config.SelectedModelTypeSmall]; !ok {
		sm, err := c.GetDefaultSmallModel(ctx, ws.ID, lastAssistant.Provider)
		if err != nil {
			slog.Warn("Failed to get default small model", "error", err)
		} else if sm != nil {
			if err := c.UpdatePreferredModel(ctx, ws.ID, config.ScopeWorkspace, config.SelectedModelTypeSmall, *sm); err != nil {
				slog.Warn("Failed to set small model during session restore", "error", err)
			}
		}
	}

	return c.UpdateAgent(ctx, ws.ID)
}

// overrideReasoningEffort validates the requested reasoning effort against
// large 模型 在 effect 为 这 运行 (which may have been overridden 通过
// --模型 或 restored 从 continued 会话) 和 applies it 在
// 服务端.
func overrideReasoningEffort(ctx context.Context, c *client.Client, wsID, reasoningEffort string) error {
	cfg, err := c.GetConfig(ctx, wsID)
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	selected, ok := cfg.Models[config.SelectedModelTypeLarge]
	if !ok {
		return fmt.Errorf("no large model selected; set one with the --model flag or 'model large'")
	}
	if err := cfg.ValidateReasoningEffort(selected.Provider, selected.Model, reasoningEffort); err != nil {
		return err
	}
	selected.ReasoningEffort = reasoningEffort
	slog.Info("Overriding reasoning effort for non-interactive run",
		"provider", selected.Provider,
		"model", selected.Model,
		"reasoning_effort", reasoningEffort)
	if err := c.UpdatePreferredModel(ctx, wsID, config.ScopeWorkspace, config.SelectedModelTypeLarge, selected); err != nil {
		return fmt.Errorf("failed to set reasoning effort: %w", err)
	}
	return c.UpdateAgent(ctx, wsID)
}

type modelMatch struct {
	provider string
	modelID  string
}

// findModelMatches searches providers 为 matching large/small 模型
// strings.
func findModelMatches(providers map[string]config.ProviderConfig, largeModel, smallModel string) ([]modelMatch, []modelMatch) {
	largeFilter, largeID := parseModelString(largeModel)
	smallFilter, smallID := parseModelString(smallModel)

	var largeMatches, smallMatches []modelMatch
	for name, provider := range providers {
		if provider.Disable {
			continue
		}
		for _, m := range provider.Models {
			if matchesModel(largeID, largeFilter, m.ID, name) {
				largeMatches = append(largeMatches, modelMatch{provider: name, modelID: m.ID})
			}
			if matchesModel(smallID, smallFilter, m.ID, name) {
				smallMatches = append(smallMatches, modelMatch{provider: name, modelID: m.ID})
			}
		}
	}
	return largeMatches, smallMatches
}

// parseModelString splits "提供方/模型" into (提供方, 模型) 或
// ("", 模型).
func parseModelString(s string) (string, string) {
	if s == "" {
		return "", ""
	}
	if idx := strings.Index(s, "/"); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return "", s
}

// matchesModel returns true 如果 模型 ID matches filter
// criteria.
func matchesModel(wantID, wantProvider, modelID, providerName string) bool {
	if wantID == "" {
		return false
	}
	if wantProvider != "" && wantProvider != providerName {
		return false
	}
	return strings.EqualFold(modelID, wantID)
}

// validateModelMatches ensures exactly one match exists.
func validateModelMatches(matches []modelMatch, modelID, label string) (modelMatch, error) {
	switch {
	case len(matches) == 0:
		return modelMatch{}, fmt.Errorf("%s model %q not found", label, modelID)
	case len(matches) > 1:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.provider
		}
		return modelMatch{}, fmt.Errorf(
			"%s model: model %q found in multiple providers: %s. Please specify provider using 'provider/model' format",
			label, modelID, strings.Join(names, ", "),
		)
	}
	return matches[0], nil
}

// resolveSession returns 会话 到 use 为 non-interactive 运行.
// If continueSessionID 是 set it fetches 那 会话; 如果 useLast 是 set it
// returns most recently updated top-level 会话; otherwise it creates
// new one.
func resolveSession(ctx context.Context, c *client.Client, wsID, continueSessionID string, useLast bool) (*proto.Session, error) {
	switch {
	case continueSessionID != "":
		sess, err := c.GetSession(ctx, wsID, continueSessionID)
		if err != nil {
			return nil, fmt.Errorf("session not found: %s", continueSessionID)
		}
		if sess.ParentSessionID != "" {
			return nil, fmt.Errorf("cannot continue a child session: %s", continueSessionID)
		}
		return sess, nil

	case useLast:
		sessions, err := c.ListSessions(ctx, wsID)
		if err != nil || len(sessions) == 0 {
			return nil, fmt.Errorf("no sessions found to continue")
		}
		last := sessions[0]
		for _, s := range sessions[1:] {
			if s.UpdatedAt > last.UpdatedAt && s.ParentSessionID == "" {
				last = s
			}
		}
		return &last, nil

	default:
		return c.CreateSession(ctx, wsID, "non-interactive")
	}
}

// resolveSessionByID resolves 会话 ID 那 may 是 full UUID 或 hash
// prefix returned 通过 crush 会话 list.
func resolveSessionByID(ctx context.Context, c *client.Client, wsID, id string) (*proto.Session, error) {
	if sess, err := c.GetSession(ctx, wsID, id); err == nil {
		return sess, nil
	}

	sessions, err := c.ListSessions(ctx, wsID)
	if err != nil {
		return nil, err
	}

	var matches []proto.Session
	for _, s := range sessions {
		hash := session.HashID(s.ID)
		if hash == id || strings.HasPrefix(hash, id) {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("session %q not found", id)
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("session ID %q is ambiguous (%d matches)", id, len(matches))
	}
}
