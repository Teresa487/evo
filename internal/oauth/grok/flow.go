package grok

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/oauth/callback"
)

const (
	// callbackPath 必须 stay exactly "/callback": xAI registers
	// shared 客户端's loopback redirect URIs 通过 路径
	// (http://127.0.0.1:{any port}/callback), 所以 any other 路径 是
	// rejected 用 "redirect_uri does 不 match any registered URI".
	// port 是 exempt per RFC 8252, which 是 why listener may
	// bind an OS-assigned one.
	callbackPath = "/callback"
	startPath    = "/auth/start"

	// accountsOrigin 是 origin 的 xAI's accounts app, which serves
	// the consent screen. Rather than a plain redirect, the consent
	// page may hand authorization code 到 loopback 服务端 用
	// cross-origin fetch 从 这 origin, 所以 callback responses
	// carry CORS headers naming it.
	accountsOrigin = "https://accounts.x.ai"
)

// BrowserFlow runs 交互式 授权: it serves 回环
// redirect target while 用户 completes authorization 在 their
// browser, then exchanges resulting code 为 tokens.
//
// Unlike OpenAI, xAI does 不 allow-list fixed callback ports, 所以
// listener binds OS-assigned port 和 redirect URI carries it.
type BrowserFlow struct {
	authURL     string
	pkce        PKCE
	state       string
	nonce       string
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
	nonce, err := Nonce()
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
		nonce:       nonce,
		redirectURI: fmt.Sprintf("http://127.0.0.1:%d%s", port, callbackPath),
		listener:    listener,
		result:      make(chan *http.Request, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc(startPath, flow.handleStart)
	mux.HandleFunc(callbackPath, flow.handleCallback)
	flow.server = &http.Server{Handler: mux}
	flow.authURL = AuthorizeURL(flow.redirectURI, pkce, state, nonce)
	flow.startURL = fmt.Sprintf("http://127.0.0.1:%d%s", port, startPath)

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

// CompleteWithCode finishes flow 用 code pasted back 从
// authorization page: either full callback URL 或 bare code.
// state 是 validated 当 paste carries one; bare code has
// none 到 check, exactly like Grok CLI's paste fallback. 它 lets
// 用户 who declined browser 权限, 或 whose browser cannot
// reach loopback callback, finish signing 在 通过 hand.
func (f *BrowserFlow) CompleteWithCode(ctx context.Context, input string) (*oauth.Token, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("no code entered")
	}

	var code, state string
	if u, err := url.Parse(input); err == nil && u.Scheme != "" && u.Host != "" {
		query := u.Query()
		if errCode := query.Get("error"); errCode != "" {
			return nil, fmt.Errorf("authorization failed: %s: %s", errCode, query.Get("error_description"))
		}
		code = query.Get("code")
		if code == "" {
			return nil, fmt.Errorf("pasted URL contained no code")
		}
		state = query.Get("state")
	} else {
		code = input
	}

	if state != "" && state != f.state {
		return nil, fmt.Errorf("authorization state mismatch")
	}

	return ExchangeCode(ctx, code, f.redirectURI, f.pkce)
}

// handleStart serves handoff page 那 opens real authorization
// URL 在 self-closable tab.
func (f *BrowserFlow) handleStart(w http.ResponseWriter, _ *http.Request) {
	_ = callback.Serve(w, callback.Result{
		Subject:     "Grok",
		ContinueURL: f.authURL,
	})
}

// handleCallback renders landing page 和 hands redirect 请求
// 到 waiting flow.
func (f *BrowserFlow) handleCallback(w http.ResponseWriter, r *http.Request) {
	// consent page 在 accounts.x.ai may 投递 code 用
	// cross-origin fetch instead 的 redirect, 所以 every callback
	// 响应 names 那 origin. preflight answer also grants
	// private-network access: fetch targets loopback address 从
	// a public page, which browsers gate behind a preflight.
	w.Header().Set("Access-Control-Allow-Origin", accountsOrigin)
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	query := r.URL.Query()
	result := callback.Result{
		Subject:          "Grok",
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

// listenCallback binds loopback listener 在 OS-assigned port.
func listenCallback() (net.Listener, int, error) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, fmt.Errorf("listen OAuth callback: %w", err)
	}
	return listener, listener.Addr().(*net.TCPAddr).Port, nil
}

// RedirectURI 是 loopback URI 这 flow presents 到 authorization
// 服务端. Exposed 为 tests 和 diagnostics.
func (f *BrowserFlow) RedirectURI() string {
	return f.redirectURI
}
