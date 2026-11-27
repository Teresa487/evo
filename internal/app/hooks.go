// hooks.go —— app 层的 hook 装配：把配置里的 hooks 接到 agent 的工具/轮次钩子上。

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
)

// sessionEndTimeout bounds SessionEnd hooks 所以 slow script cannot
// hold process open 在 exit.
const sessionEndTimeout = 2 * time.Second

// CreateSession creates new 会话 和 fires SessionStart. Every
// 会话 creation 路径 goes through here 所以 钩子 是 consistent.
func (app *App) CreateSession(ctx context.Context, title string) (session.Session, error) {
	sess, err := app.Sessions.Create(ctx, title)
	if err != nil {
		return session.Session{}, err
	}
	app.hookRunner.Run(ctx, hooks.Event{Name: hooks.EventSessionStart, SessionID: sess.ID, Source: "startup"})
	return sess, nil
}

// resolveSession resolves which 会话 到 use 为 non-interactive
// 运行: given 会话, most recent top-level one, 或 new one.
// Resumed sessions fire SessionStart 用 source "resume".
func (app *App) resolveSession(ctx context.Context, continueSessionID string, useLast bool) (session.Session, error) {
	var sess session.Session
	var err error
	switch {
	case continueSessionID != "":
		if app.Sessions.IsAgentToolSession(continueSessionID) {
			return session.Session{}, fmt.Errorf("cannot continue an agent tool session: %s", continueSessionID)
		}
		sess, err = app.Sessions.Get(ctx, continueSessionID)
		if err != nil {
			return session.Session{}, fmt.Errorf("session not found: %s", continueSessionID)
		}
		if sess.ParentSessionID != "" {
			return session.Session{}, fmt.Errorf("cannot continue a child session: %s", continueSessionID)
		}
	case useLast:
		sess, err = app.Sessions.GetLast(ctx)
		if err != nil {
			return session.Session{}, fmt.Errorf("no sessions found to continue")
		}
	default:
		return app.CreateSession(ctx, agent.DefaultSessionName)
	}
	app.hookRunner.Run(ctx, hooks.Event{Name: hooks.EventSessionStart, SessionID: sess.ID, Source: "resume"})
	return sess, nil
}

// fireSessionEnd fires SessionEnd 为 会话 用户 was working
// 在. 它 runs after agents have stopped 所以 Interrupt precedes it.
func (app *App) fireSessionEnd() {
	ctx, cancel := context.WithTimeout(context.Background(), sessionEndTimeout)
	defer cancel()
	sessionID, _ := app.currentSessionID.Load().(string)
	app.hookRunner.Run(ctx, hooks.Event{
		Name:      hooks.EventSessionEnd,
		SessionID: sessionID,
		Reason:    "exit",
	})
}

// permissionHooks fires PermissionRequest 当 提示词 是 shown 和
// PermissionResult 当 it 是 granted, denied, 或 cancelled.
type permissionHooks struct {
	runner *hooks.Runner
}

func (p permissionHooks) PromptShown(ctx context.Context, req permission.PermissionRequest) {
	p.runner.Run(ctx, permissionEvent(hooks.EventPermissionRequest, req))
}

func (p permissionHooks) PromptResolved(ctx context.Context, req permission.PermissionRequest, outcome permission.Outcome) {
	ev := permissionEvent(hooks.EventPermissionResult, req)
	ev.Decision = string(outcome)
	p.runner.Run(ctx, ev)
}

func permissionEvent(name string, req permission.PermissionRequest) hooks.Event {
	input, err := json.Marshal(req.Params)
	if err != nil {
		slog.Warn("Failed to encode permission params for hook", "tool", req.ToolName, "error", err)
		input = []byte("{}")
	}
	return hooks.Event{
		Name:      name,
		SessionID: req.SessionID,
		Tool:      &hooks.ToolCall{ID: req.ToolCallID, Name: req.ToolName, Input: string(input)},
	}
}
