package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

// newTestUIForPermissions builds UI 用 chat, dialog overlay, 和
// common 上下文 sufficient 到 exercise handlePermissionNotification.
func newTestUIForPermissions() *UI {
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	return u
}

func TestHandlePermissionNotification_RemoteGrantClosesDialog(t *testing.T) {
	t.Parallel()

	u := newTestUIForPermissions()
	perm := permission.PermissionRequest{
		ID:         "perm-1",
		ToolCallID: "tool-call-X",
		ToolName:   "bash",
	}
	u.dialog.OpenDialogWithGrace(dialog.NewPermissions(u.com, perm))
	require.True(t, u.dialog.ContainsDialog(dialog.PermissionsID))

	u.handlePermissionNotification(permission.PermissionNotification{
		ToolCallID: "tool-call-X",
		Granted:    true,
	})

	require.False(t, u.dialog.ContainsDialog(dialog.PermissionsID),
		"granted notification should close matching permissions dialog")
}

func TestHandlePermissionNotification_RemoteDenyClosesDialog(t *testing.T) {
	t.Parallel()

	u := newTestUIForPermissions()
	perm := permission.PermissionRequest{
		ID:         "perm-2",
		ToolCallID: "tool-call-Y",
	}
	u.dialog.OpenDialogWithGrace(dialog.NewPermissions(u.com, perm))

	u.handlePermissionNotification(permission.PermissionNotification{
		ToolCallID: "tool-call-Y",
		Denied:     true,
	})

	require.False(t, u.dialog.ContainsDialog(dialog.PermissionsID),
		"denied notification should close matching permissions dialog")
}

func TestHandlePermissionNotification_InitialPendingDoesNotClose(t *testing.T) {
	t.Parallel()

	u := newTestUIForPermissions()
	perm := permission.PermissionRequest{
		ID:         "perm-3",
		ToolCallID: "tool-call-Z",
	}
	u.dialog.OpenDialogWithGrace(dialog.NewPermissions(u.com, perm))

	// initial 通知 published 通过 权限.请求 是
	// neither granted nor denied; it must 不 dismiss dialog.
	u.handlePermissionNotification(permission.PermissionNotification{
		ToolCallID: "tool-call-Z",
	})

	require.True(t, u.dialog.ContainsDialog(dialog.PermissionsID),
		"initial pending notification must not close the dialog")
}

func TestHandlePermissionNotification_DifferentToolCallIDDoesNotClose(t *testing.T) {
	t.Parallel()

	u := newTestUIForPermissions()
	perm := permission.PermissionRequest{
		ID:         "perm-4",
		ToolCallID: "tool-call-A",
	}
	u.dialog.OpenDialogWithGrace(dialog.NewPermissions(u.com, perm))

	u.handlePermissionNotification(permission.PermissionNotification{
		ToolCallID: "tool-call-B",
		Granted:    true,
	})

	require.True(t, u.dialog.ContainsDialog(dialog.PermissionsID),
		"notification for unrelated tool call must not close the dialog")
}

// permissionWorkspace 是 countingWorkspace 用 配置
// permissions dialog reads 它的 diff mode 从.
type permissionWorkspace struct {
	*countingWorkspace
}

func (permissionWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// permissionEvent 是 事件 工作区 publishes 当 工具 asks 为
// 权限 用 given ID, 和 again whenever 请求 是
// replayed.
func permissionEvent(id string) pubsub.Event[permission.PermissionRequest] {
	return pubsub.Event[permission.PermissionRequest]{
		Type: pubsub.CreatedEvent,
		Payload: permission.PermissionRequest{
			ID:         id,
			SessionID:  "s1",
			ToolCallID: "tool-call-" + id,
			ToolName:   "bash",
		},
	}
}

func TestPermissionRequestReplayKeepsDialog(t *testing.T) {
	t.Setenv("CRUSH_PERMISSIONS_PIPED", "")
	u := newProgramStatusUI(t)
	u.com.Workspace = permissionWorkspace{u.com.Workspace.(*countingWorkspace)}
	notifier := &recordingNotifier{}
	u.notifyBackend = notifier
	u.caps.ReportFocusEvents = true
	u.notifyWindowFocused = false

	_, _ = u.Update(permissionEvent("perm-1"))
	dlg := u.dialog.Dialog(dialog.PermissionsID)
	require.NotNil(t, dlg)
	require.Len(t, notifier.sent, 1)

	// 服务端 re-sends pending 请求 whenever 客户端 reports 它的
	// 会话; 重放 must keep open dialog, 用 它的 scroll, diff
	// mode, 和 selected button, 和 不 notify again.
	_, _ = u.Update(permissionEvent("perm-1"))
	require.Same(t, dlg, u.dialog.Dialog(dialog.PermissionsID))
	require.Len(t, notifier.sent, 1)

	_, _ = u.Update(permissionEvent("perm-2"))
	require.NotSame(t, dlg, u.dialog.Dialog(dialog.PermissionsID))
	require.Len(t, notifier.sent, 2)
}
