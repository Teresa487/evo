package cmd

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/log/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/server"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// childTestEnv marks 测试 二进制 re-运行 通过 runChildTest. Logging 是 set up
// once per process, 所以 tests 那 set it up do 所以 在 child process.
const childTestEnv = "EVO_TEST_CHILD"

// runChildTest re-runs calling 测试 在 child process 用 env added,
// 和 returns child's output. child sees childTestEnv set. Its
// files stay open until it exits, which 是 before parent's temporary
// directories 是 removed, 作为 Windows requires.
func runChildTest(t *testing.T, env ...string) string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	c := exec.CommandContext(t.Context(), exe, "-test.run=^"+t.Name()+"$")
	c.Env = append(append(os.Environ(), childTestEnv+"=1"), env...)
	out, err := c.CombinedOutput()
	require.NoError(t, err, "child test failed:\n%s", out)
	return string(out)
}

// TestConnectToServer_ClientStaysOutOfServerLog covers clients logging into
// 服务端's rotating log 文件. Every 客户端 then holds 文件 open, 所以
// 在 Windows 服务端 cannot rotate it while any 客户端 是 attached, 和
// 服务端's logging stops. 一个 客户端 must log 到 文件 的 它的 own until
// it knows 它的 工作区, 和 then 到 工作区's log.
func TestConnectToServer_ClientStaysOutOfServerLog(t *testing.T) {
	if os.Getenv(childTestEnv) == "1" {
		connectAndLog(t, os.Getenv("EVO_TEST_DATA_DIR"))
		return
	}
	t.Parallel()

	runChildTest(t,
		"EVO_CACHE_DIR="+t.TempDir(),
		"EVO_TEST_DATA_DIR="+t.TempDir(),
		"EVO_DISABLE_METRICS=1",
	)
}

// connectAndLog connects 到 fake 服务端 way CLI does, logs before
// 和 after, 和 checks where records landed.
func connectAndLog(t *testing.T, dataDir string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/workspaces", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(proto.Workspace{
			ID:     "ws",
			Config: &config.Config{Options: &config.Options{DataDirectory: dataDir}},
		}))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	clientHost = "tcp://" + u.Host
	hostURL, err := server.ParseHostURL(clientHost)
	require.NoError(t, err)

	// 服务端 has already started 它的 log.
	perHostDir, err := perHostServerDir(hostURL)
	require.NoError(t, err)
	serverLog := filepath.Join(perHostDir, "evo.log")
	require.NoError(t, os.WriteFile(serverLog, []byte(`{"msg":"Server started"}`+"\n"), 0o600))

	cmd := &cobra.Command{}
	cmd.Flags().Bool("debug", false, "")
	cmd.Flags().Bool("yolo", false, "")
	cmd.Flags().StringSlice("channels", nil, "")
	cmd.Flags().String("data-dir", "", "")
	cmd.Flags().String("cwd", "", "")
	cmd.SetContext(t.Context())

	require.NoError(t, ensureServer(cmd, hostURL))
	slog.Warn("Client record before connecting")
	_, _, _, err = connectToServer(cmd)
	require.NoError(t, err)
	slog.Warn("Client record after connecting")

	// Windows refuses 到 rename 文件 while another handle 在 it 是 open,
	// 所以 这 是 服务端 rotating 它的 log 用 客户端 attached.
	require.NoError(t, os.Rename(serverLog, serverLog+".1"),
		"the server must be able to rotate its log while a client runs")
	got, err := os.ReadFile(serverLog + ".1")
	require.NoError(t, err)
	require.NotContains(t, string(got), "Client record", "a client must not write the server's log")

	clientLog := filepath.Join(perHostDir, "client.log")
	got, err = os.ReadFile(clientLog)
	require.NoError(t, err, "the client must log its startup to a file of its own")
	require.Contains(t, string(got), "Client record before connecting")
	require.NotContains(t, string(got), "Client record after connecting")
	require.NoError(t, os.Rename(clientLog, clientLog+".1"),
		"the client must release its startup log once it knows its workspace")

	got, err = os.ReadFile(filepath.Join(dataDir, "logs", "evo.log"))
	require.NoError(t, err, "the client must log to its workspace once it knows it")
	require.Contains(t, string(got), "Client record after connecting")
}

// TestLogs_ClientServerModeShowsServerLog covers `evo logs` 在
// 客户端/服务端 mode. agent runs 在 服务端, which logs 到 它的 own
// 文件, 所以 showing 只 工作区's log finds nothing about it.
func TestLogs_ClientServerModeShowsServerLog(t *testing.T) {
	const host = "tcp://127.0.0.1:1"
	if os.Getenv(childTestEnv) == "1" {
		rootCmd.SetArgs([]string{"logs", "--cwd", os.Getenv("EVO_TEST_PROJECT"), "--host", host})
		require.NoError(t, rootCmd.ExecuteContext(t.Context()))
		return
	}
	t.Parallel()

	hostURL, err := server.ParseHostURL(host)
	require.NoError(t, err)
	cacheDir := t.TempDir()
	project := t.TempDir()
	writeLogLines(t, filepath.Join(cacheDir, "server-"+safeHostName(hostURL), "client.log"),
		`{"time":"2026-01-02T03:04:04Z","level":"WARN","msg":"Client startup record"}`,
	)
	writeLogLines(t, filepath.Join(project, ".evo", "logs", "evo.log"),
		`{"time":"2026-01-02T03:04:05Z","level":"INFO","msg":"Client record first"}`,
		`{"time":"2026-01-02T03:04:07Z","level":"INFO","msg":"Client record last"}`,
	)
	// offset changes 用 daylight saving time, 所以 records 是 ordered
	// 通过 instant they were logged, 不 通过 their 文本.
	writeLogLines(t, filepath.Join(cacheDir, "server-"+safeHostName(hostURL), "evo.log"),
		`{"time":"2026-01-02T03:04:06Z","level":"INFO","msg":"Server record in UTC"}`,
		`{"time":"2026-01-02T04:04:06.5+01:00","level":"INFO","msg":"Server record with offset"}`,
	)

	out := runChildTest(t,
		"EVO_CLIENT_SERVER=1",
		"EVO_CACHE_DIR="+cacheDir,
		"EVO_GLOBAL_CONFIG="+t.TempDir(),
		"EVO_GLOBAL_DATA="+t.TempDir(),
		"EVO_DISABLE_PROVIDER_AUTO_UPDATE=1",
		"EVO_TEST_PROJECT="+project,
	)

	var order []int
	for _, msg := range []string{
		"Client startup record",
		"Client record first",
		"Server record in UTC",
		"Server record with offset",
		"Client record last",
	} {
		i := strings.Index(out, msg)
		require.NotEqual(t, -1, i, "evo logs must show %q:\n%s", msg, out)
		order = append(order, i)
	}
	require.IsIncreasing(t, order, "records must be shown in the order they were logged:\n%s", out)
}

func writeLogLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

// TestPrintLogLineSource covers log lines 用 和 without source 字段,
// since source 是 只 logged 在 debug mode.
func TestPrintLogLineSource(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })

	printLogLine(`{"time":"2026-01-02T03:04:05Z","level":"INFO","msg":"With source","source":{"function":"main.main","file":"internal/agent/agent.go","line":1168}}`)
	printLogLine(`{"time":"2026-01-02T03:04:06Z","level":"INFO","msg":"Without source"}`)

	out := buf.String()
	require.Contains(t, out, "With source")
	require.Contains(t, out, "internal/agent/agent.go:1168")
	require.Contains(t, out, "Without source")
}
