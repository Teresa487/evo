package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestSessionSetupFromSubdirectoryUsesProjectDataDir(t *testing.T) {
	t.Setenv("CRUSH_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("CRUSH_GLOBAL_DATA", t.TempDir())
	t.Setenv("CRUSH_DISABLE_PROVIDER_AUTO_UPDATE", "1")

	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	gitInit := exec.CommandContext(t.Context(), "git", "init", "-q")
	gitInit.Dir = root
	require.NoError(t, gitInit.Run())
	require.NoError(t, os.Mkdir(filepath.Join(root, ".evo"), 0o700))
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	t.Chdir(sub)

	sessionListCmd.SetContext(t.Context())
	_, svc, cleanup, err := sessionSetup(sessionListCmd)
	require.NoError(t, err)
	defer cleanup()

	require.Equal(t, filepath.Join(root, ".evo"), svc.Config().Options.DataDirectory)
	require.NoDirExists(t, filepath.Join(sub, ".evo"))
}

// TestUseClientServer_HostFlag covers --主机 being ignored unless
// CRUSH_CLIENT_SERVER was set too: naming 服务端 must connect 到 it.
func TestUseClientServer_HostFlag(t *testing.T) {
	t.Setenv("CRUSH_CLIENT_SERVER", "")

	useServer := func(args ...string) bool {
		var use bool
		root := &cobra.Command{Use: "crush"}
		root.PersistentFlags().String("host", "", "")
		root.AddCommand(&cobra.Command{
			Use: "sub",
			Run: func(cmd *cobra.Command, _ []string) { use = useClientServer(cmd) },
		})
		root.SetArgs(append([]string{"sub"}, args...))
		require.NoError(t, root.Execute())
		return use
	}

	require.False(t, useServer(), "without --host or CRUSH_CLIENT_SERVER Crush must run locally")
	require.True(t, useServer("--host", "tcp://127.0.0.1:1"), "--host must connect to the server it names")
	t.Setenv("CRUSH_CLIENT_SERVER", "1")
	require.True(t, useServer(), "CRUSH_CLIENT_SERVER must connect to the default server")
}

// TestSessionList_ClientServerModeAsksServer covers 会话 commands
// reading local 数据库 even 在 客户端/服务端 mode, which shows
// wrong sessions 当 服务端 runs elsewhere.
func TestSessionList_ClientServerModeAsksServer(t *testing.T) {
	if os.Getenv(childTestEnv) == "1" {
		listSessionsOnServer(t, os.Getenv("CRUSH_TEST_PROJECT"), os.Getenv("CRUSH_TEST_DATA_DIR"))
		return
	}
	t.Parallel()

	runChildTest(t,
		"CRUSH_CLIENT_SERVER=1",
		"CRUSH_CACHE_DIR="+t.TempDir(),
		"CRUSH_GLOBAL_CONFIG="+t.TempDir(),
		"CRUSH_GLOBAL_DATA="+t.TempDir(),
		"CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1",
		"CRUSH_DISABLE_METRICS=1",
		"CRUSH_TEST_DATA_DIR="+t.TempDir(),
		"CRUSH_TEST_PROJECT="+t.TempDir(),
	)
}

// TestSessionList_ConfigOptionAsksServer covers 会话 commands
// ignoring options.experimental.client_server, which puts TUI 和
// `crush 运行` 在 客户端/服务端 mode.
func TestSessionList_ConfigOptionAsksServer(t *testing.T) {
	if os.Getenv(childTestEnv) == "1" {
		project := os.Getenv("CRUSH_TEST_PROJECT")
		require.NoError(t, os.WriteFile(filepath.Join(project, "evo.json"),
			[]byte(`{"options": {"experimental": {"client_server": true}}}`), 0o644))
		listSessionsOnServer(t, project, os.Getenv("CRUSH_TEST_DATA_DIR"))
		return
	}
	t.Parallel()

	runChildTest(t,
		"CRUSH_CLIENT_SERVER=",
		"CRUSH_CACHE_DIR="+t.TempDir(),
		"CRUSH_GLOBAL_CONFIG="+t.TempDir(),
		"CRUSH_GLOBAL_DATA="+t.TempDir(),
		"CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1",
		"CRUSH_DISABLE_METRICS=1",
		"CRUSH_TEST_DATA_DIR="+t.TempDir(),
		"CRUSH_TEST_PROJECT="+t.TempDir(),
	)
}

// listSessionsOnServer runs `crush 会话 list --json` 从 project
// against fake 服务端 那 hosts one 会话. project comes 从
// parent 测试: command changes into it, 和 Windows cannot remove
// 目录 process 是 still 在.
func listSessionsOnServer(t *testing.T, project, dataDir string) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("POST /v1/workspaces", func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(proto.Workspace{
			ID:     "ws",
			Config: &config.Config{Options: &config.Options{DataDirectory: dataDir}},
		}))
	})
	mux.HandleFunc("GET /v1/workspaces/ws/sessions", func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode([]proto.Session{
			{ID: "from-server", Title: "Session on the server"},
		}))
	})
	mux.HandleFunc("DELETE /v1/clients/{id}", func(http.ResponseWriter, *http.Request) {})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	clientHost = "tcp://" + u.Host

	cmd := &cobra.Command{}
	cmd.Flags().Bool("debug", false, "")
	cmd.Flags().String("data-dir", "", "")
	cmd.Flags().String("cwd", project, "")
	cmd.SetContext(t.Context())
	var out bytes.Buffer
	cmd.SetOut(&out)
	sessionListJSON = true

	require.NoError(t, runSessionList(cmd, nil))
	require.Contains(t, out.String(), "Session on the server",
		"in client/server mode sessions must come from the server")
}
