package clientserverrace_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunStopsOnTerminationSignal guards against `crush 运行` ignoring
// SIGTERM 和 SIGHUP, which root command catches, 和 streaming 在
// until 它的 agent finishes.
func TestRunStopsOnTerminationSignal(t *testing.T) {
	bin := crushBinary(t)
	for _, clientServer := range []bool{false, true} {
		for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
			t.Run(fmt.Sprintf("client-server=%t/%v", clientServer, sig), func(t *testing.T) {
				streaming, cancelled, url := startSlowLLM(t)
				sb := newSandbox(t, bin,
					"CRUSH_CLIENT_SERVER="+strconv.FormatBool(clientServer),
					"CRUSH_DISABLE_DEFAULT_PROVIDERS=1")
				writeMockConfig(t, sb, url)
				if clientServer {
					sb.start(t, "server", "server", "--host", sb.host)
					if !assert.Eventually(t, func() bool { return pingHealth(sb.socket) == nil },
						30*time.Second, 50*time.Millisecond) {
						t.Fatalf("server never became healthy\n%s", sb.output("server"))
					}
				}

				run := sb.start(t, "run", "--host", sb.host, "--cwd", sb.project, "run", "hello")
				select {
				case <-streaming:
				case <-run.done:
					t.Fatalf("run exited before streaming: %v\n%s", run.err, sb.output("run"))
				case <-time.After(time.Minute):
					t.Fatalf("run never started streaming\n%s", sb.output("run"))
				}

				require.NoError(t, run.cmd.Process.Signal(sig))
				for what, ch := range map[string]chan struct{}{"run exited": run.done, "turn cancelled": cancelled} {
					select {
					case <-ch:
					case <-time.After(10 * time.Second):
						t.Fatalf("no %q within 10s of %v", what, sig)
					}
				}
			})
		}
	}
}

// startSlowLLM serves OpenAI-compatible chat completions whose turns 流
// until 请求 goes away. 它 returns channels closed once turn 是
// streaming 和 once 那 turn's 请求 是 gone, 和 它的 base URL.
func startSlowLLM(t *testing.T) (streaming, cancelled chan struct{}, url string) {
	t.Helper()
	const chunk = `data: {"id":"c","object":"chat.completion.chunk","created":0,` +
		`"model":"mock-model","choices":[{"index":0,"delta":%s,"finish_reason":%s}]}` + "\n\n"
	streaming, cancelled = make(chan struct{}), make(chan struct{})
	stop := make(chan struct{})
	var streamingOnce, cancelledOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		// 标题 generation runs alongside turn; answer it 在 once.
		if strings.Contains(string(body), "Generate a concise title") {
			fmt.Fprintf(w, chunk, `{"role":"assistant","content":"Title"}`, "null")
			fmt.Fprintf(w, chunk, `{}`, `"stop"`)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		for {
			fmt.Fprintf(w, chunk, `{"role":"assistant","content":"tok "}`, "null")
			w.(http.Flusher).Flush()
			streamingOnce.Do(func() { close(streaming) })
			select {
			case <-r.Context().Done():
				cancelledOnce.Do(func() { close(cancelled) })
				return
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(func() {
		close(stop)
		srv.Close()
	})
	return streaming, cancelled, srv.URL + "/v1"
}

// writeMockConfig points sandbox's global 配置 在 LLM 在 url.
func writeMockConfig(t *testing.T, sb *sandbox, url string) {
	t.Helper()
	dir := filepath.Join(sb.dir, "xdg_config_home", "evo")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	cfg := fmt.Sprintf(`{
  "providers": {"mock": {"type": "openai-compat", "base_url": %q, "api_key": "test",
    "models": [{"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128}]}},
  "models": {"large": {"provider": "mock", "model": "mock-model"},
    "small": {"provider": "mock", "model": "mock-model"}}
}`, url)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "evo.json"), []byte(cfg), 0o600))
}
