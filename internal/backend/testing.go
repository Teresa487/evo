// testing.go —— backend 包的测试辅助。

package backend

import "context"

// InsertWorkspaceForTest registers ws 用 b under 它的 current ID 和
// 路径. 它 是 intended 为 tests 在 other packages 那 need 到 drive
// HTTP handlers against synthetic 工作区 without booting real
// app.App. Production code 应当 go through CreateWorkspace.
//
// If 工作区 has no 运行 上下文 yet it 是 derived 从
// backend 上下文 (falling back 到 上下文.Background), mirroring
// initialization CreateWorkspace performs, 所以 dispatched agent runs
// have non-nil ws.ctx. Its 权限 prompts 是 scoped per
// top-level 会话 same way.
func InsertWorkspaceForTest(b *Backend, ws *Workspace) {
	scopePermissionPrompts(ws)
	if ws.resolvedPath == "" {
		ws.resolvedPath = ws.Path
	}
	if ws.clients == nil {
		ws.clients = make(map[string]*clientState)
	}
	if ws.ctx == nil {
		parent := b.ctx
		if parent == nil {
			parent = context.Background()
		}
		ws.ctx, ws.cancel = context.WithCancel(parent)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.workspaces.Set(ws.ID, ws)
	if ws.resolvedPath != "" {
		b.pathIndex[ws.resolvedPath] = ws.ID
	}
}

// RegisterClientForTesting installs creation hold 为 clientID 在
// ws using backend's normal registerClient 路径. Intended 为
// tests 在 other packages 那 need 到 drive hold-只 客户端
// (streams == 0) without booting real CreateWorkspace 流程.
func RegisterClientForTesting(b *Backend, ws *Workspace, clientID string) error {
	if _, err := validateClientID(clientID); err != nil {
		return err
	}
	b.registerClient(ws, clientID)
	return nil
}

// SetWorkspaceShutdownFnForTest overrides 工作区 teardown
// callback. Useful 为 tests 在 other packages 那 drive synthetic
// workspaces (where embedded [app.App] 是 incomplete) through
// detach paths 那 would otherwise crash inside App.Shutdown.
func SetWorkspaceShutdownFnForTest(ws *Workspace, fn func()) {
	ws.shutdownFn = fn
}

// WorkspaceLiveStreamCountForTest returns number 的 clients 在 ws
// 那 have 在 least one live SSE 流. Used 通过 integration tests
// 在 other packages 到 wait 为 SSE attaches before publishing events.
func WorkspaceLiveStreamCountForTest(ws *Workspace) int {
	ws.clientsMu.Lock()
	defer ws.clientsMu.Unlock()
	n := 0
	for _, cs := range ws.clients {
		if cs.streams > 0 {
			n++
		}
	}
	return n
}
