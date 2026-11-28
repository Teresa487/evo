// Package clientserverrace_test 是 regression 测试 为
// EVO_CLIENT_SERVER=1 socket-init race documented 在
// docs/备注/2026-05-11-客户端-服务端-socket-init-race.md (item F5).
//
// 它 lives 在 它的 own 目录 所以 it can build even 如果 other 测试
// files 在 internal/cmd 是 temporarily broken — 这 测试 只 needs
// 二进制, 不 cmd package.
package clientserverrace_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// readinessErrSubstr 是 用户-visible 错误 string emitted 通过
// ensureServer 当 it gives up waiting 为 服务端 socket /
// readiness probe (internal/cmd/root.go). Seeing 这 在 any 客户端's
// output means the race fired.
const readinessErrSubstr = "failed to initialize evo server"

// numClients 是 intentionally larger than typical CPU count 到
// ensure spawn 锁 + readiness probe 是 exercised under
// contention.
const numClients = 8

// clientTimeout bounds each child invocation. 它 只 needs 到 是 long
// enough 为 spawn-和-readiness phase 到 完成 在 cold 缓存;
// after 那, 客户端 may legitimately keep running (e.g.
// subscribing 到 服务端 events) 和 we'll 取消 it. race we care
// about 是 observable strictly within ensureServer.
const clientTimeout = 15 * time.Second

func TestClientServerSpawnRace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping client/server spawn race test in -short mode")
	}
	// race 和 它的 fix 是 unix-socket specific. Windows uses
	// named pipes via different code 路径; 不 covered here.
	if runtime.GOOS == "windows" {
		t.Skip("skipping unix-socket specific race test on windows")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("skipping: 'go' not available on PATH")
	}

	repoRoot := repoRootFromTest(t)
	bin := buildEvoBinary(t, repoRoot)

	// Use /tmp directly 所以 unix socket 路径 stays under
	// 104-char sockaddr_un limit 在 darwin. t.TempDir() can return
	// 路径 inside /var/folders/... 那 是 too long.
	runDir, err := os.MkdirTemp("/tmp", "crush-race-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })

	socketPath := filepath.Join(runDir, "evo.sock")
	host := "unix://" + socketPath

	// Fresh, isolated XDG/HOME 所以 we don't touch 用户's real
	// state 或 any other 测试's 缓存. These all live under runDir
	// 所以 清理 是 one RemoveAll.
	cacheHome := filepath.Join(runDir, "cache")
	dataHome := filepath.Join(runDir, "data")
	configHome := filepath.Join(runDir, "config")
	homeDir := filepath.Join(runDir, "home")
	for _, d := range []string{cacheHome, dataHome, configHome, homeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	env := append(
		os.Environ(),
		"EVO_CLIENT_SERVER=1",
		"XDG_CACHE_HOME="+cacheHome,
		"XDG_DATA_HOME="+dataHome,
		"XDG_CONFIG_HOME="+configHome,
		"HOME="+homeDir,
		// Belt-和-suspenders: 如果 anything tries 到 talk 到 real
		// 提供方, fail loudly rather than make network 调用.
		"EVO_DISABLE_PROVIDER_AUTO_UPDATE=1",
	)

	// Make sure no 服务端 是 up before we start.
	if _, err := os.Stat(socketPath); err == nil {
		t.Fatalf("socket %s exists before test started", socketPath)
	}

	// Always try 到 shut down any 服务端 we spawned, regardless 的
	// outcome.
	t.Cleanup(func() { shutdownServer(t, socketPath) })

	type result struct {
		idx    int
		stdout string
		stderr string
	}
	results := make(chan result, numClients)

	// Probe /v1/health concurrently while clients 是 still
	// running. 服务端 self-shuts-down 当 last 工作区 是
	// released (internal/backend/backend.go:DeleteWorkspace), 所以 once
	// all clients exit cleanly socket may legitimately 是 gone —
	// asserting socket post-hoc would race 用 那 documented
	// self-shutdown. Instead we require 那 during parallel 运行
	// 在 least one /v1/health probe got 2xx, which proves
	// spawn-和-readiness 路径 actually produced live 服务端.
	var sawHealthy atomic.Bool
	probeDone := make(chan struct{})
	stopProbe := make(chan struct{})

	var wg sync.WaitGroup
	start := make(chan struct{})

	go func() {
		defer close(probeDone)
		<-start
		deadline := time.Now().Add(clientTimeout)
		for time.Now().Before(deadline) {
			select {
			case <-stopProbe:
				return
			default:
			}
			if err := pingHealth(socketPath); err == nil {
				sawHealthy.Store(true)
				return
			}
			select {
			case <-stopProbe:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()

	for i := range numClients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			// Each 客户端 gets 它的 own working 目录 所以
			// per-客户端 工作区 registration paths don't collide
			// 在 confusing ways.
			cwd := filepath.Join(runDir, fmt.Sprintf("ws-%d", i))
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				results <- result{idx: i, stderr: fmt.Sprintf("mkdir cwd: %v", err)}
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), clientTimeout)
			defer cancel()

			// `evo logout` exercises connectToServer (which 是
			// where readiness race lives) 和, unlike `crush
			// 运行`, spawns 服务端 当 none 是 running.
			// 上下文 超时 above bounds 运行. We assert race
			// outcomes purely 从 output, 不 exit code.
			c := exec.CommandContext(
				ctx, bin,
				"--host", host,
				"--cwd", cwd,
				"logout", "hyper",
			)
			c.Env = env
			var outBuf, errBuf strings.Builder
			c.Stdout = &outBuf
			c.Stderr = &errBuf

			<-start
			_ = c.Run()
			results <- result{
				idx:    i,
				stdout: outBuf.String(),
				stderr: errBuf.String(),
			}
		}(i)
	}

	close(start) // release all clients as simultaneously as possible
	wg.Wait()
	close(results)
	close(stopProbe)
	<-probeDone

	var raceFailures []string
	for r := range results {
		if strings.Contains(r.stderr, readinessErrSubstr) ||
			strings.Contains(r.stdout, readinessErrSubstr) {
			raceFailures = append(raceFailures, fmt.Sprintf(
				"client %d: readiness error in output\nstderr:\n%s\nstdout:\n%s",
				r.idx, r.stderr, r.stdout,
			))
		}
	}

	if len(raceFailures) > 0 {
		t.Fatalf(
			"client/server spawn race regressed: %d/%d clients failed\n\n%s",
			len(raceFailures), numClients,
			strings.Join(raceFailures, "\n---\n"),
		)
	}

	// Positive sanity check: 在 some point during parallel 运行
	// /v1/health probe must have succeeded. We deliberately do *不*
	// stat socket post-hoc: 当 every 客户端 returns cleanly
	// (e.g. exits early because no providers 是 configured),
	// last DeleteWorkspace triggers 服务端's self-shutdown 和
	// socket disappears. That 是 correct behaviour, 不 race
	// regression.
	if !sawHealthy.Load() {
		t.Fatalf("no /v1/health probe succeeded on %s while %d clients were running",
			socketPath, numClients)
	}
}

// pingHealth issues single GET /v1/health over unix socket 和
// requires 2xx 响应.
func pingHealth(socketPath string) error {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: 2 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://crush.local/v1/health", nil)
	if err != nil {
		return err
	}
	rsp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return fmt.Errorf("health check returned %s", rsp.Status)
	}
	return nil
}

// repoRootFromTest walks up 从 这 测试 文件's 目录 到 find
// repo root ( 目录 containing go.mod). Walking up 通过
// fixed count 是 fragile across reorganisations.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod walking up from %s", cwd)
		}
		dir = parent
	}
}

// buildEvoBinary builds crush 二进制 once 在 start 的
// 测试 和 returns absolute 路径. Subsequent t.清理 removes
// the built artefact.
func buildEvoBinary(t *testing.T, repoRoot string) string {
	t.Helper()

	binDir, err := os.MkdirTemp("", "crush-race-bin-")
	if err != nil {
		t.Fatalf("mkdtemp bin: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(binDir) })

	binPath := filepath.Join(binDir, "crush")

	// 一个 cold build takes minutes 在 slow CI runner, 所以 it gets time
	// 测试 运行 has left, less moment 到 报告 failure, rather
	// than a fixed budget a busy runner overruns.
	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-30*time.Second))
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, ".")
	cmd.Dir = repoRoot
	// Match project's standard build flags. CGO_ENABLED=0 保持
	// 二进制 statically linked 和 avoids surprising 测试 在
	// hosts without a C toolchain.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build crush: %v\n%s", err, out)
	}
	return binPath
}

// shutdownServer best-effort terminates any crush 服务端 bound 到
// socketPath 通过 POSTing 到 /v1/control. We don't import project's
// own 客户端 package 到 keep 这 测试 free 的 internal API churn.
func shutdownServer(t *testing.T, socketPath string) {
	t.Helper()
	if _, err := os.Stat(socketPath); err != nil {
		return
	}

	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	hc := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	defer tr.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	body := strings.NewReader(`{"command":"shutdown"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://crush.local/v1/control", body)
	if err != nil {
		t.Logf("shutdown: build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		// 服务端 may already 是 gone — 不 错误.
		t.Logf("shutdown: %v (probably already exited)", err)
		return
	}
	_ = resp.Body.Close()

	// Wait briefly 为 socket 到 disappear 所以 next 测试
	// using same 路径 doesn't race.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
