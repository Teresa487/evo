package app

import (
	"context"
	"sync"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
)

// NewForTest constructs minimal [App] suitable 为 在-process tests
// 那 need working 事件 总线 和 权限 service without
// booting real 配置, 数据库, LSP, MCP, 或 agent coordinator.
//
// The returned App has:
//
// - 一个 live `events` 总线 那 [App.SendEvent] publishes 到 和
// [App.Events] subscribes 从.
// - 一个 real [权限.Service] whose 请求 和 通知
// brokers 是 fanned into events 总线, 所以 subscribers 到
// [App.Events] observe same 权限 events production
// wiring would 投递 到 SSE clients.
// - An [App.agentNotifications] 总线.
//
// caller owns lifetime: 取消 ctx (或 调用 [App.Shutdown]) 到
// tear down fan-在 goroutines 和 events 总线.
func NewForTest(ctx context.Context) *App {
	app := &App{
		Permissions:        permission.NewPermissionService("", nil, nil),
		Questions:          question.NewService(),
		globalCtx:          ctx,
		events:             pubsub.NewCoalescingBroker(eventCoalesceKey),
		serviceEventsWG:    &sync.WaitGroup{},
		tuiWG:              &sync.WaitGroup{},
		agentNotifications: pubsub.NewBroker[notify.Notification](),
		runCompletions:     pubsub.NewBroker[notify.RunComplete](),
		toolProgress:       pubsub.NewCoalescingBroker(toolProgressCoalesceKey),
	}

	eventsCtx, cancel := context.WithCancel(ctx)
	app.eventsCtx = eventsCtx
	app.subscribeMustDeliver(eventsCtx, "permissions",
		app.Permissions.Subscribe)
	app.subscribeMustDeliver(eventsCtx, "permissions-notifications",
		app.Permissions.SubscribeNotifications)
	app.subscribeMustDeliver(eventsCtx, "question-batches",
		app.Questions.Subscribe)
	app.subscribeMustDeliver(eventsCtx, "question-notifications",
		app.Questions.SubscribeNotifications)
	app.subscribe(eventsCtx, "agent-notifications",
		app.agentNotifications.Subscribe)
	app.subscribe(eventsCtx, "run-completions",
		app.runCompletions.Subscribe)
	app.subscribe(eventsCtx, "tool-progress",
		app.toolProgress.Subscribe)
	app.cleanupFuncs = append(app.cleanupFuncs, func(context.Context) error {
		cancel()
		app.serviceEventsWG.Wait()
		app.events.Shutdown()
		return nil
	})
	return app
}

// ShutdownForTest tears down App's 事件 总线 和 fan-在
// goroutines. 它 是 safe 到 调用 multiple times.
//
// Use 这 在 tests instead 的 [App.Shutdown], which drives full
// production shutdown 路径 (数据库 release, LSP teardown, MCP
// shutdown) 那 synthetic 测试 apps cannot satisfy.
func (app *App) ShutdownForTest() {
	for _, cleanup := range app.cleanupFuncs {
		if cleanup != nil {
			_ = cleanup(context.Background())
		}
	}
	app.cleanupFuncs = nil
}
