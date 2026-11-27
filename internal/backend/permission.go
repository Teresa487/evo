// permission.go —— 权限请求：后端把工具权限询问转给客户端并等待答复。

package backend

import (
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
)

// GrantPermission grants, denies, 或 persistently grants 权限
// 请求. returned bool reports whether 这 调用 resolved
// pending 请求 (true) 或 found it already resolved 通过 previous
// caller (false). 一个 false return 是 不 错误.
func (b *Backend) GrantPermission(workspaceID string, req proto.PermissionGrant) (bool, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return false, err
	}

	perm := permission.PermissionRequest{
		ID:          req.Permission.ID,
		SessionID:   req.Permission.SessionID,
		ToolCallID:  req.Permission.ToolCallID,
		ToolName:    req.Permission.ToolName,
		Description: req.Permission.Description,
		Action:      req.Permission.Action,
		Params:      req.Permission.Params,
		Path:        req.Permission.Path,
		Danger:      req.Permission.Danger,
	}

	switch req.Action {
	case proto.PermissionAllow:
		return ws.Permissions.Grant(perm), nil
	case proto.PermissionAllowForSession:
		return ws.Permissions.GrantPersistent(perm), nil
	case proto.PermissionDeny:
		return ws.Permissions.Deny(perm), nil
	default:
		return false, ErrInvalidPermissionAction
	}
}

// AutoApproveSession grants every 权限 请求 会话 raises
// 从 now 在 without prompting, 作为 non-interactive 运行 does 为 它的 own
// 会话. Other sessions keep prompting.
func (b *Backend) AutoApproveSession(workspaceID, sessionID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	ws.Permissions.AutoApproveSession(sessionID)
	return nil
}

// SetPermissionMode sets 权限 mode 为 工作区.
//
// Only 权限 service 是 updated; 配置 override 是 left
// holding startup flag 在 purpose. override 是 seed 那
// app.New reads once 在 construction, 和 配置.Overrides() hands
// back bare 指针 用 no locking, 所以 writing it here would race
// readers 在 CreateWorkspace 路径 为 no benefit. Leaving
// service 作为 single writable source 的 truth 是 what keeps
// enforced mode 和 mode reported 到 clients 从 drifting apart.
func (b *Backend) SetPermissionMode(workspaceID string, mode permission.PermissionMode) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	ws.Permissions.SetPermissionMode(mode)
	return nil
}
