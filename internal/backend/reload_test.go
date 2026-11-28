package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestReloadConfig_ReloadsEachWorkspace covers 服务端 sent SIGHUP: every
// hosted 工作区 re-reads 它的 配置, 和 one 那 fails 到 load keeps
// 它的 old 配置 without holding back rest.
func TestReloadConfig_ReloadsEachWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(home, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(home, ".local", "share", "crush"))

	b, _ := newTestBackend(t)
	good := insertReloadWorkspace(t, b)
	bad := insertReloadWorkspace(t, b)

	writeCrushrc(t, good, "option notifications osc\n")
	writeCrushrc(t, bad, "exit 1\n")

	err := b.ReloadConfig(t.Context())
	require.ErrorContains(t, err, bad.ID)
	require.NotContains(t, err.Error(), good.ID)
	require.Equal(t, "osc", good.Cfg.Config().Options.Notifications)
	require.Equal(t, "bell", bad.Cfg.Config().Options.Notifications)
}

func insertReloadWorkspace(t *testing.T, b *Backend) *Workspace {
	t.Helper()
	wd := t.TempDir()
	ws := &Workspace{ID: uuid.New().String(), Path: wd}
	writeCrushrc(t, ws, "option notifications bell\n")
	cfg, err := config.Init(t.Context(), wd, "", false)
	require.NoError(t, err)
	ws.Cfg = cfg
	InsertWorkspaceForTest(b, ws)
	return ws
}

func writeCrushrc(t *testing.T, ws *Workspace, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "evorc"), []byte(body), 0o644))
}
