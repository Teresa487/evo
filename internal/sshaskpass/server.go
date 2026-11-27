package sshaskpass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
)

// socketRequest 是 what askpass wrapper subprocess sends 到
// Crush process hosting the socket.
type socketRequest struct {
	Request PromptRequest `json:"request"`
}

// socketResponse 是 what host sends back.
type socketResponse struct {
	Secret    string `json:"secret,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
	Error     string `json:"error,omitempty"`
}

var (
	sharedMu   sync.Mutex
	sharedRefs int
	sharedStop func()
)

// StartIntegration enables integrated ssh askpass 为 这 process:
// it writes SSH_ASKPASS wrapper script, opens 提示词 socket,
// configures process-wide integration, 和 starts serving wrapper
// requests.
//
// integration 是 shared 和 reference counted: 在 服务端 mode every
// 工作区 starts it, 和 it stays up until last returned 清理
// runs, 所以 closing one 工作区 never disables it 为 others.
//
// Returns 清理 function 那 stops socket, removes
// wrapper, 和 disables integration. 它 是 safe 到 取消 ctx 到 stop
// serving.
func StartIntegration(ctx context.Context, executable string) (cleanup func(), err error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedRefs == 0 {
		stop, err := startIntegration(ctx, executable)
		if err != nil {
			return nil, err
		}
		sharedStop = stop
	}
	sharedRefs++

	var once sync.Once
	return func() {
		once.Do(func() {
			sharedMu.Lock()
			defer sharedMu.Unlock()
			if sharedRefs--; sharedRefs == 0 {
				sharedStop()
				sharedStop = nil
			}
		})
	}, nil
}

func startIntegration(ctx context.Context, executable string) (cleanup func(), err error) {
	wrapperPath := ""
	var wrapperCleanup func()
	if runtime.GOOS != "windows" {
		wrapperPath, wrapperCleanup, err = WriteAskpassWrapper(executable)
		if err != nil {
			return nil, err
		}
	}

	dir, err := os.MkdirTemp("", "crush-sshaskpass-sock-*")
	if err != nil {
		if wrapperCleanup != nil {
			wrapperCleanup()
		}
		return nil, err
	}
	socketPath := filepath.Join(dir, "ssh.sock")

	// Owner-只 permissions keep credential socket unreadable 到
	// other local users.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", socketPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		if wrapperCleanup != nil {
			wrapperCleanup()
		}
		return nil, err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
		if wrapperCleanup != nil {
			wrapperCleanup()
		}
		return nil, err
	}

	ConfigureIntegration(IntegrationConfig{
		AskpassPath: wrapperPath,
		SocketPath:  socketPath,
	})

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		serveConnections(ctx, ln)
	}()

	cleanup = func() {
		_ = ln.Close()
		<-serveDone
		_ = os.RemoveAll(dir)
		if wrapperCleanup != nil {
			wrapperCleanup()
		}
		DisableIntegration()
	}
	return cleanup, nil
}

// serveConnections accepts 和 handles wrapper connections until
// listener closes.
func serveConnections(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleConnection(ctx, conn)
	}
}

// handleConnection services single askpass wrapper 请求: block 在
// 提示词 service until 用户 answers (或 上下文 dies), then
// return the secret over the socket.
func handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	var req socketRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}

	// Bound wait 所以 stuck ssh cannot hold socket forever.
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// wrapper sends nothing after 它的 请求, 所以 读取 只
	// returns once it hangs up: 它的 ssh command was cancelled. Drop
	// 提示词 then, rather than leaving stale dialog 那 holds up
	// next one.
	go func() {
		buf := make([]byte, 1)
		for {
			if _, err := conn.Read(buf); err != nil {
				cancel()
				return
			}
		}
	}()

	secret, err := defaultPrompts.Prompt(reqCtx, req.Request)

	resp := socketResponse{Secret: secret, Cancelled: errors.Is(err, ErrCancelled)}
	if err != nil && !errors.Is(err, ErrCancelled) {
		resp.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

// AskCredentialOverSocket sends single 提示词 请求 到 host
// socket 和 returns secret. Used 通过 ssh askpass subcommand,
// which runs 作为 child 的 ssh 和 cannot talk 到 在-process
// 提示词 service directly.
func AskCredentialOverSocket(ctx context.Context, socketPath string, req PromptRequest) (string, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return "", fmt.Errorf("sshaskpass: connect to host socket: %w", err)
	}
	defer conn.Close()

	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	enc := json.NewEncoder(conn)
	if err := enc.Encode(socketRequest{Request: req}); err != nil {
		return "", fmt.Errorf("sshaskpass: write request: %w", err)
	}

	type result struct {
		resp socketResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var resp socketResponse
		err := json.NewDecoder(conn).Decode(&resp)
		ch <- result{resp: resp, err: err}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return "", fmt.Errorf("sshaskpass: read response: %w", r.err)
		}
		switch {
		case r.resp.Cancelled:
			return "", ErrCancelled
		case r.resp.Error != "":
			return "", errors.New(r.resp.Error)
		default:
			return r.resp.Secret, nil
		}
	}
}
