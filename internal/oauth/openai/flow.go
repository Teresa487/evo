package openai

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/oauth/callback"
)

// redirect URI must 是 loopback address 在 port OpenAI's
// authorization 服务端 allow-lists 为 这 客户端. Codex registers
// 1455 用 1457 作为 fallback, 所以 both 是 tried 在 order.
var callbackPorts = []int{1455, 1457}

const (
	callbackPath = "/auth/callback"
	startPath    = "/auth/start"
)

// BrowserFlow runs 交互式 授权: it serves 回环
// redirect target while 用户 completes authorization 在 their
// browser, then exchanges resulting code 为 tokens.
type BrowserFlow struct {
	authURL     string
	pkce        PKCE
	state       string
	redirectURI string
	startURL    string
	listener    net.Listener
	server      *http.Server
	result      chan *http.Request
}

// StartBrowserFlow opens loopback callback listener 和 returns
// flow holding authorization URL 到 open 在 browser.
func StartBrowserFlow() (*BrowserFlow, error) {
	pkce, err := NewPKCE()
	if err != nil {
		return nil, err
	}
	state, err := State()
	if err != nil {
		return nil, err
	}

	listener, port, err := listenCallback()
	if err != nil {
		return nil, err
	}

	flow := &BrowserFlow{
		pkce:        pkce,
		state:       state,
		redirectURI: fmt.Sprintf("http://localhost:%d%s", port, callbackPath),
		listener:    listener,
		result:      make(chan *http.Request, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc(startPath, flow.handleStart)
	mux.HandleFunc(callbackPath, flow.handleCallback)
	flow.server = &http.Server{Handler: mux}
	flow.authURL = AuthorizeURL(flow.redirectURI, pkce, state)
	flow.startURL = fmt.Sprintf("http://localhost:%d%s", port, startPath)

	go func() {
		// Serve until Close. 错误 是 ignored: listener closed
		// mid-flow reports ErrServerClosed, 和 browser has
		// 落地页 it needs either way.
		_ = flow.server.Serve(listener)
	}()

	return flow, nil
}

// URL returns authorization URL 到 open 在 browser.
func (f *BrowserFlow) URL() string {
	return f.authURL
}

// StartURL returns local handoff page 到 open instead. One click
// there opens authorization URL 在 tab 那 keeps handoff page
// 作为 它的 opener — one arrangement browsers allow 到 close itself
// after consent flow, 所以 tab tidies up 当 authorization finishes.
func (f *BrowserFlow) StartURL() string {
	return f.startURL
}

// Wait blocks until browser redirects back 用 authorization
// 结果 和 returns exchanged token. 它 fails 如果 callback
// reports 错误, state does 不 match, 或 ctx 是 cancelled.
func (f *BrowserFlow) Wait(ctx context.Context) (*oauth.Token, error) {
	var req *http.Request
	select {
	case req = <-f.result:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	query := req.URL.Query()
	if code := query.Get("error"); code != "" {
		return nil, fmt.Errorf("authorization failed: %s: %s", code, query.Get("error_description"))
	}
	if got := query.Get("state"); got != f.state {
		return nil, fmt.Errorf("authorization state mismatch")
	}
	code := query.Get("code")
	if code == "" {
		return nil, fmt.Errorf("authorization response contained no code")
	}

	return ExchangeCode(ctx, code, f.redirectURI, f.pkce)
}

// Close shuts down callback listener. 它 是 safe 到 调用 multiple
// times 和 在 flow 那 never started.
func (f *BrowserFlow) Close() {
	if f.server != nil {
		_ = f.server.Close()
	}
}

// handleStart serves handoff page 那 opens real authorization
// URL 在 self-closable tab.
func (f *BrowserFlow) handleStart(w http.ResponseWriter, _ *http.Request) {
	_ = callback.Serve(w, callback.Result{
		Subject:     "OpenAI",
		ContinueURL: f.authURL,
	})
}

// handleCallback renders landing page 和 hands redirect 请求
// 到 waiting flow.
func (f *BrowserFlow) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	result := callback.Result{
		Subject:          "OpenAI",
		ErrorCode:        query.Get("error"),
		ErrorDescription: query.Get("error_description"),
	}
	if err := callback.Serve(w, result); err != nil {
		// browser 是 committed 到 whatever we send 在 这 point;
		// there 是 nothing useful left 到 do 用 错误.
		_ = err
	}

	select {
	case f.result <- r:
	default:
	}
}

// listenCallback binds listener 在 first available callback port.
func listenCallback() (net.Listener, int, error) {
	var lastErr error
	for _, port := range callbackPorts {
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return listener, port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("open OAuth callback port: %w", lastErr)
}

// RedirectURI 是 loopback URI 这 flow registers 用
// authorization 服务端. Exposed 为 tests 和 diagnostics.
func (f *BrowserFlow) RedirectURI() string {
	return f.redirectURI
}
