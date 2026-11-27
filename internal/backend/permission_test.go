package backend

import (
	"context"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestSetPermissionMode_ReportedInWorkspaceProto pins the invariant
// 那 mode 客户端 是 shown 是 mode 服务端 enforces.
// 工作区 snapshot used 到 是 built 从 startup flag, 所以
// any runtime toggle left every 客户端 reading stale 值 forever.
// dangerous direction 是 客户端 displaying "normal" while
// 服务端 auto-approves everything, 所以 both directions 是 asserted.
func TestSetPermissionMode_ReportedInWorkspaceProto(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	b := New(context.Background(), nil, func() {})
	b.SetCreateGrace(2 * time.Second)
	t.Cleanup(func() { drainBackend(t, b) })

	// Start 在 yolo 所以 那 toggling down 到 normal 是 just 作为 much
	// real assertion 作为 toggling up; 工作区 started 在 normal
	// would make down-toggle pass 通过 accident.
	args := protoWS(t.TempDir(), t.TempDir(), uuid.New().String())
	args.PermissionMode = proto.WorkspacePermissionModeYolo
	ws, created, err := b.CreateWorkspace(args)
	require.NoError(t, err)
	require.Equal(t, proto.WorkspacePermissionModeYolo, created.PermissionMode)

	for _, tc := range []struct {
		name string
		mode permission.PermissionMode
		want proto.WorkspacePermissionMode
	}{
		{"yolo to normal", permission.PermissionModeNormal, proto.WorkspacePermissionModeNormal},
		{"normal to sysadmin", permission.PermissionModeSysadmin, proto.WorkspacePermissionModeSysadmin},
		{"sysadmin to yolo", permission.PermissionModeYolo, proto.WorkspacePermissionModeYolo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, b.SetPermissionMode(ws.ID, tc.mode))
			require.Equal(t, tc.mode, ws.Permissions.PermissionMode(),
				"the server must enforce the mode it was just given")

			got, err := b.GetWorkspaceProto(ws.ID)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.PermissionMode,
				"workspace snapshot must report the enforced mode")

			// ListWorkspaces 是 second 客户端-facing 路径 onto
			// same snapshot; fix 那 misses it still lies 到 clients.
			list := b.ListWorkspaces()
			require.Len(t, list, 1)
			require.Equal(t, tc.want, list[0].PermissionMode,
				"workspace listing must report the enforced mode")
		})
	}
}
