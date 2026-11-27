package server

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestGitBranchEndpoint covers GET /v1/workspaces/{id}/git/branch:
// 服务端 must 报告 branch checked out 在 工作区's
// working 目录 所以 客户端/服务端 TUIs can display it.
//
// Cannot 运行 在 parallel: it isolates HOME/XDG_* via t.Setenv 所以
// 配置.Init does 不 读取 host machine's real 配置.
func TestGitBranchEndpoint(t *testing.T) {
	h := newRealCreateHarness(t)
	h.backend.SetCreateGrace(200 * time.Millisecond)
	h.backend.SetDetachGrace(200 * time.Millisecond)

	wsPath := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = wsPath
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_CONFIG_SYSTEM="+os.DevNull,
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	runGit("init")
	runGit("checkout", "-b", "feature/git-branch-endpoint")

	ws := h.postWorkspace(t, proto.Workspace{
		Path:     wsPath,
		DataDir:  t.TempDir(),
		ClientID: uuid.New().String(),
	})

	// Windows cannot remove temp data 目录 while pooled
	// DB 连接 是 open, 所以 release it 在 teardown 和 wait 为
	// 创建 grace 到 expire before TempDir 清理 runs.
	bw, err := h.backend.GetWorkspace(ws.ID)
	require.NoError(t, err)
	wsDataDir := bw.Cfg.Config().Options.DataDirectory
	released := make(chan struct{})
	backend.SetWorkspaceShutdownFnForTest(bw, func() {
		_ = db.Release(wsDataDir)
		close(released)
	})
	t.Cleanup(func() {
		select {
		case <-released:
		case <-time.After(10 * time.Second):
			t.Error("workspace was not released")
		}
	})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		h.httpSrv.URL+"/v1/workspaces/"+ws.ID+"/git/branch", nil)
	require.NoError(t, err)
	resp, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out proto.GitBranchResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Equal(t, "feature/git-branch-endpoint", out.Branch)
}

// TestGitBranchEndpoint_UnknownWorkspace verifies 端点 maps
// missing 工作区 到 404-style 错误 响应 rather than
// panicking.
func TestGitBranchEndpoint_UnknownWorkspace(t *testing.T) {
	h := newE2EHarness(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		h.httpSrv.URL+"/v1/workspaces/"+uuid.New().String()+"/git/branch", nil)
	require.NoError(t, err)
	resp, err := h.httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.NotEqual(t, http.StatusOK, resp.StatusCode)
}
