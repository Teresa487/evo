package mcpoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/oauth/callback"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/pkg/browser"
	"golang.org/x/oauth2"
)

// ErrInteractiveAuthRequired 是 returned 通过 Authorize 当 服务端 needs
// interactive (browser) authorization 但 current 上下文 does 不
// permit it. Background connections such 作为 startup deliberately withhold
// 权限 所以 failed 或 missing token surfaces 作为 needs-auth state
// instead 的 silently opening browser 和 blocking initialization.
// 用户 then triggers interactive flow explicitly.
var ErrInteractiveAuthRequired = errors.New("interactive OAuth authorization required")

// interactiveKey marks 上下文 作为 permitting interactive browser flow.
type interactiveKey struct{}

// WithInteractive returns 上下文 那 permits interactive browser
// authorization flow. Only 用户-initiated authentication should use it.
func WithInteractive(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveKey{}, true)
}

// IsInteractive reports whether ctx permits 交互式 浏览器 流程.
func IsInteractive(ctx context.Context) bool {
	v, _ := ctx.Value(interactiveKey{}).(bool)
	return v
}

// callbackPath 是 路径 authorization 服务端 redirects back 到. 它
// 是 片段 的 registered redirect URI, 所以 it must 不 change without
// re-registering clients.
const callbackPath = "/callback"

// callbackPorts 是 localhost ports tried, 在 order, 为 OAuth
// redirect listener. first available one 是 used.
var callbackPorts = []int{
	40704, 40705, 40706, 40707, 40708,
	40709, 40710, 40711, 40712, 40713,
}

// 处理器 implements auth.OAuthHandler 为 MCP HTTP servers. 它 wraps
// go-sdk AuthorizationCodeHandler 和 persists token (plus
// 客户端 registration 和 endpoints needed 到 refresh it) 所以 那
// restarts 和 background refreshes never force 用户 back through
// 浏览器.
//
// Persistence 是 wired through SDK's own hooks: NewTokenSource 是
// invoked after code exchange 和 为 every refresh, 和
// InitialTokenSource injects restored token 在 startup. That removes
// need 到 hand-roll authorization-服务端 discovery.
type Handler struct {
	inner    auth.OAuthHandler
	receiver *callbackReceiver

	// openURL opens authorization URL 在 用户's browser. 它 是
	// 字段 所以 tests can simulate headless environment 或 drive
	// callback directly.
	openURL func(string) error

	// interactive permits browser authorization flow. 它 是 false 为
	// background connections (startup) 所以 missing 或 unrefreshable token
	// surfaces 作为 needs-auth state instead 的 opening browser 和
	// blocking initialization.
	interactive bool

	mu             sync.Mutex
	cachedToken    *oauth2.Token
	authURL        string
	serverURL      string
	onTokenRefresh func(*oauth.Token)

	// suppressBrowser, 当 true, prevents openURL 从 being invoked
	// ( authorization URL 是 still recorded 和 logged). Used 当
	// flow 是 driven remotely, e.g. 通过 connected 客户端 那 opens
	// browser 在 它的 own machine.
	suppressBrowser bool
}

var _ auth.OAuthHandler = (*Handler)(nil)

// NewHandler creates new OAuth 处理器 为 MCP 服务端. savedToken,
// 如果 present, restores prior 会话: 它的 access/refresh tokens 和
// captured 客户端 registration 是 injected 所以 SDK can use 和
// silently refresh them without browser round-trip. preregistered, 如果
// set, supplies explicit OAuth 客户端 为 servers 那 do 不 support
// dynamic 客户端 registration. onTokenRefresh 是 called whenever token
// 是 obtained 或 refreshed 所以 caller can persist it. interactive
// permits browser flow; pass false 为 background connections
// (startup) 所以 bad token never opens browser.
func NewHandler(
	serverName string,
	serverURL string,
	savedToken *oauth.Token,
	preregistered *oauth.OAuthClient,
	onTokenRefresh func(*oauth.Token),
	interactive bool,
	callbackPort int,
) (*Handler, error) {
	receiver := &callbackReceiver{
		serverName: serverName,
		fixedPort:  callbackPort,
	}

	// Resolve redirect port without binding it. listener 是 只
	// opened 当 authorization actually runs (see fetchAuthorizationCode),
	// 所以 处理器 那 restores valid token never occupies port 和
	// several Crush processes can share it; 只 one doing live login
	// binds, 和 只 为 duration 的 那 login.
	//
	// 一个 fixed port comes straight 从 配置. Otherwise we probe 为
	// first free candidate, just long enough 到 learn which 是 open. Either
	// way chosen port 是 baked into redirect URI below 和 pinned 在
	// receiver, 所以 bindLocked always rebinds SAME port. probe 是
	// 不 占位 — another process can take port before first
	// real login — 但 busy port then fails loudly rather than silently
	// binding port redirect URI does 不 point 在.
	port := callbackPort
	if port == 0 {
		lc := &net.ListenConfig{}
		for _, p := range callbackPorts {
			probe, err := lc.Listen(context.Background(), "tcp", fmt.Sprintf("localhost:%d", p))
			if err == nil {
				_ = probe.Close()
				port = p
				break
			}
		}
		if port == 0 {
			return nil, errors.New("failed to start OAuth callback listener: all candidate ports in use")
		}
	}
	receiver.fixedPort = port

	redirectURL := fmt.Sprintf("http://localhost:%d%s", port, callbackPath)

	h := &Handler{
		receiver:       receiver,
		serverURL:      serverURL,
		openURL:        browser.OpenURL,
		interactive:    interactive,
		onTokenRefresh: onTokenRefresh,
	}
	receiver.handler = h

	// newTokenSource 是 SDK 钩子 invoked once after successful code
	// exchange. token it hands us 是 brand new, 所以 persist it right
	// away, then wrap source 所以 later refreshes persist 在 change.
	// resolved oauth2.配置 carries registered 客户端 ID 和
	// discovered endpoints, which we persist alongside token 所以
	// later start 可以 刷新 without rediscovery.
	newTokenSource := func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
		h.persist(cfg, tok)
		base := cfg.TokenSource(ctx, tok)
		return NewSavingTokenSource(base, cfg, tok, func(c *oauth2.Config, t *oauth2.Token) {
			h.persist(c, t)
		}), nil
	}

	cfg := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:              redirectURL,
		AuthorizationCodeFetcher: receiver.fetchAuthorizationCode,
		RequestRefreshToken:      true,
		NewTokenSource:           newTokenSource,
		// Some servers (e.g. Sentry) send the RFC 9207 iss parameter
		// without advertising it 在 their metadata. 受理 matching
		// unadvertised iss instead 的 failing flow.
		AcceptUnadvertisedIss: true,
		// Use metadata-fixing HTTP 客户端 所以 trailing-slash issuers 在
		// OAuth metadata responses don't trip the SDK's strict RFC 8414
		// validation. Also rewrite internal-cluster redirects back 到
		// external hostname 所以 flow works outside cluster.
		// Based 在 Bruno Krugel's fix 从 PR #3396.
		Client: newOAuthMetadataClient(http.DefaultTransport, serverURL),
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				ClientName:   "Crush",
				RedirectURIs: []string{redirectURL},
				GrantTypes:   []string{"authorization_code", "refresh_token"},
			},
		},
	}

	// Restore saved 客户端 registration 作为 pre-registered 客户端 所以
	// Use pre-registered 客户端 所以 SDK skips dynamic registration.
	// An explicitly configured 客户端 (为 servers 那 don't support DCR,
	// like GitHub 或 Slack) takes precedence over one captured 从
	// previous registration.
	client := preregistered
	if client == nil || client.ClientID == "" {
		if savedToken != nil && savedToken.Client != nil {
			client = savedToken.Client
		}
	}
	if client != nil && client.ClientID != "" {
		cfg.PreregisteredClient = &oauthex.ClientCredentials{
			ClientID: client.ClientID,
		}
		if client.ClientSecret != "" {
			cfg.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{
				ClientSecret: client.ClientSecret,
			}
		}
	}

	// Restore saved token 作为 initial token source 所以 SDK uses
	// it directly (和 refreshes it) instead 的 triggering browser
	// flow. Seed saver 用 restored token 所以 只 genuine
	// refresh writes 到 disk; plain restart causes no token churn.
	if hasRefreshableToken(savedToken) {
		restored := &oauth2.Token{
			AccessToken:  savedToken.AccessToken,
			RefreshToken: savedToken.RefreshToken,
			Expiry:       time.Unix(savedToken.ExpiresAt, 0),
		}
		oc := &oauth2.Config{
			ClientID:     savedToken.Client.ClientID,
			ClientSecret: savedToken.Client.ClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:   savedToken.Client.AuthURL,
				TokenURL:  savedToken.Client.TokenURL,
				AuthStyle: oauth2.AuthStyle(savedToken.Client.AuthStyle),
			},
		}
		base := oc.TokenSource(context.Background(), restored)
		cfg.InitialTokenSource = NewSavingTokenSource(base, oc, restored, func(c *oauth2.Config, t *oauth2.Token) {
			h.persist(c, t)
		})
		h.cachedToken = restored
	}

	inner, err := auth.NewAuthorizationCodeHandler(cfg)
	if err != nil {
		receiver.close()
		return nil, fmt.Errorf("failed to create OAuth handler: %w", err)
	}
	h.inner = inner

	slog.Info(
		"MCP OAuth handler created",
		"name", serverName,
		"redirect_url", redirectURL,
		"restored_token", h.cachedToken != nil,
	)

	return h, nil
}

// hasRefreshableToken reports whether saved 令牌 carries enough state
// 到 是 used 和 refreshed without re-authorizing: access token plus
// 令牌端点 captured previously.
func hasRefreshableToken(t *oauth.Token) bool {
	return t != nil && t.AccessToken != "" && t.Client != nil && t.Client.TokenURL != ""
}

// AuthURL returns last authorization URL opened 在 browser.
func (h *Handler) AuthURL() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.authURL
}

// SetBrowserSuppress controls whether browser 是 opened automatically
// 当 authorization URL 是 generated. Pass unlock function 那
// restores previous behavior; 处理器 re-enables browser 当
// returned function 是 called. 这 是 used 通过 服务端-driven flow
// where remote 客户端 opens browser locally.
func (h *Handler) SetBrowserSuppress(suppress bool) func() {
	h.mu.Lock()
	prev := h.suppressBrowser
	h.suppressBrowser = suppress
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		h.suppressBrowser = prev
		h.mu.Unlock()
	}
}

// Token returns current OAuth token, 或 nil 如果 不 yet authorized.
func (h *Handler) Token() *oauth2.Token {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cachedToken
}

// TokenSource implements auth.OAuthHandler. 它 delegates 到 inner
// 处理器, whose token source 是 already wrapped 为 persistence via
// NewTokenSource 和 seeded (当 restoring) via InitialTokenSource.
func (h *Handler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return h.inner.TokenSource(ctx)
}

// Authorize implements auth.OAuthHandler. 它 runs SDK authorization
// flow; resulting token 是 captured 和 persisted through
// NewTokenSource saver.
func (h *Handler) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	// Never open browser 为 background 连接 (e.g. startup).
	// caller surfaces needs-auth state 和 用户 triggers
	// interactive flow via 处理器 created 用 interactive=true.
	if !h.interactive {
		return ErrInteractiveAuthRequired
	}
	if err := h.inner.Authorize(ctx, req, resp); err != nil {
		// SDK reports 这 当 服务端 supports none 的
		// registration methods offered 和 no 客户端 was pre-registered.
		// Point 用户 在 配置 字段 那 fixes it.
		if strings.Contains(err.Error(), "no configured client registration methods") {
			return fmt.Errorf("%q does not support automatic OAuth client registration; register an OAuth app with the provider and set oauth_client_id (and oauth_client_secret if required) for this MCP server: %w", h.serverURL, err)
		}
		return err
	}
	ts, err := h.inner.TokenSource(ctx)
	if err != nil {
		return err
	}
	if ts == nil {
		// SDK short-circuits non-授权 responses (e.g.
		// genuine 403) without establishing 令牌 source. Leave any
		// restored token 在 place.
		return nil
	}
	// Reading 令牌 drives saver, which persists it.
	if _, err := ts.Token(); err != nil {
		return err
	}
	slog.Info("MCP OAuth token captured")
	return nil
}

// persist records latest token 在 memory 和 hands serialisable
// copy (including 客户端 registration 和 endpoints 从 cfg) 到
// caller-supplied saver.
func (h *Handler) persist(cfg *oauth2.Config, tok *oauth2.Token) {
	h.mu.Lock()
	h.cachedToken = tok
	h.mu.Unlock()

	if h.onTokenRefresh == nil {
		return
	}

	out := &oauth.Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
	}
	if !tok.Expiry.IsZero() {
		out.ExpiresIn = int(time.Until(tok.Expiry).Seconds())
	}
	out.SetExpiresAt()
	if cfg != nil {
		out.Client = &oauth.OAuthClient{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			AuthURL:      cfg.Endpoint.AuthURL,
			TokenURL:     cfg.Endpoint.TokenURL,
			AuthStyle:    int(cfg.Endpoint.AuthStyle),
		}
	}
	h.onTokenRefresh(out)
}

// Close shuts down callback 服务端.
func (h *Handler) Close() {
	h.receiver.close()
}

// callbackReceiver owns localhost listener 那 authorization
// 服务端 redirects back 到, 和 hands each authorization 结果 到
// flow waiting 为 it.
//
// listener 是 bound lazily, 在 first authorization attempt, 和
// released 作为 soon 作为 那 attempt settles. 一个 处理器 那 holds valid
// token never binds 在 all, 所以 any number 的 Crush processes may coexist;
// callback port 是 occupied 只 为 few seconds actual login
// 是 在 flight.
type callbackReceiver struct {
	handler *Handler
	// serverName labels callback page 所以 用户 can see which MCP
	// 服务端 they just authorized.
	serverName string

	// fixedPort, 当 > 0, 是 OAuthCallbackPort 配置: single
	// port registered redirect URI points 在. When 0, port 是
	// chosen 从 callbackPorts 在 bind time.
	fixedPort int

	mu sync.Mutex
	// flight 是 authorization currently awaiting redirect, 如果 any.
	// Connecting 到 服务端 can issue several requests 在 once, 所以 more
	// than one 的 them can meet 401 和 ask 到 authorize. They share
	// single flight rather than each opening their own browser tab 和
	// racing 为 one redirect.
	flight *authFlight
	// 服务端 和 port describe live listener. They 是 set 只 while
	// authorization 是 在 flight 和 cleared moment it settles.
	server *http.Server
	port   int
	closed bool
}

// authFlight 是 one authorization attempt: browser tab was opened 和
// redirect 是 expected. 它 settles exactly once, whichever arrives first —
// 结果, 错误, 或 receiver shutting down.
type authFlight struct {
	done   chan struct{}
	once   sync.Once
	result *auth.AuthorizationResult
	err    error

	// joined counts callers waiting 在 这 flight instead 的 opening tab.
	joined atomic.Int32
}

// settle records outcome 的 flight 和 wakes everyone waiting 在
// it. Only first 调用 has any effect, 所以 duplicate redirect (
// reloaded tab, say) cannot overwrite 结果 already 在 use.
func (f *authFlight) settle(result *auth.AuthorizationResult, err error) {
	f.once.Do(func() {
		f.result, f.err = result, err
		close(f.done)
	})
}

// begin returns flight 到 wait 在 和 whether caller created it.
// creator 是 responsible 为 opening browser 和, once attempt
// 是 over, 为 clearing it 所以 later authorization can start fresh.
// first (creating) caller also binds the callback listener; joiners wait
// 在 flight 那 是 already serving.
func (r *callbackReceiver) begin() (*authFlight, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.flight != nil {
		r.flight.joined.Add(1)
		return r.flight, false, nil
	}
	if err := r.bindLocked(); err != nil {
		return nil, false, err
	}
	r.flight = &authFlight{done: make(chan struct{})}
	return r.flight, true, nil
}

// end retires flight 如果 it 是 still current one, 所以 next
// authorization opens fresh browser tab instead 的 waiting 在 redirect
// 那 will never come.
func (r *callbackReceiver) end(flight *authFlight) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.flight == flight {
		r.flight = nil
	}
}

// current returns 在-progress flight, 或 nil 当 no authorization 是
// waiting 在 redirect.
func (r *callbackReceiver) current() *authFlight {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flight
}

// bind starts listener 如果 one 是 不 already running. port was
// resolved 和 pinned 在 construction, 所以 这 always targets port
// redirect URI points 在; 如果 it 是 busy 错误 surfaces loudly rather
// than silently binding port nobody will redirect 到.
func (r *callbackReceiver) bind() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bindLocked()
}

// bindLocked 是 bind 用 r.mu already held. listener starts accepting
// before 这 returns, 所以 browser opened immediately after cannot beat
// 服务端 到 port.
func (r *callbackReceiver) bindLocked() error {
	if r.closed {
		return errors.New("OAuth callback listener closed")
	}
	if r.server != nil {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", r.handleCallback)
	server := &http.Server{Handler: mux}

	lc := &net.ListenConfig{}
	listener, err := lc.Listen(context.Background(), "tcp", fmt.Sprintf("localhost:%d", r.fixedPort))
	if err != nil {
		return fmt.Errorf("failed to bind OAuth callback port %d: %w", r.fixedPort, err)
	}
	r.port = r.fixedPort
	go r.serve(server, listener)
	r.server = server
	return nil
}

// serve runs HTTP 服务端 until listener 是 closed. An unexpected
// Serve 错误 settles 在-flight flight 所以 waiting flow fails
// promptly instead 的 hanging until 它的 上下文 expires.
func (r *callbackReceiver) serve(server *http.Server, listener net.Listener) {
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		if flight := r.current(); flight != nil {
			flight.settle(nil, err)
		}
	}
}

// release shuts listener down 和 clears it, 所以 callback port 是
// free 为 next authorization (或 another process). 它 是 mirror
// 的 bind 和 是 called once authorization settles 和 在 close.
//
// callback page 是 rendered before flight settles, 所以 响应
// 是 already 在 它的 way out 通过 time release runs. Shutdown (不 Close)
// lets any 在-flight 写入 finish draining before socket closes.
func (r *callbackReceiver) release() {
	r.mu.Lock()
	server := r.server
	r.server = nil
	r.mu.Unlock()
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}

// handleCallback receives authorization redirect, hands 结果 到
// waiting flow, 和 renders page 用户 sees.
//
// Only redirect 路径 是 treated 作为 callback. Browsers 请求 extras
// such 作为 /favicon.ico against same origin, 和 letting one 的 those
// settle flight would abort authorization 用 empty code.
func (r *callbackReceiver) handleCallback(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != callbackPath {
		http.NotFound(w, req)
		return
	}

	query := req.URL.Query()
	result := callback.Result{
		Subject:          r.serverName,
		ErrorCode:        query.Get("error"),
		ErrorDescription: query.Get("error_description"),
	}

	// Render the page BEFORE settling the flight. settle unblocks await,
	// which releases listener; settling first would close 连接
	// out 从 under 这 写入 和 show 用户 browser 错误 instead
	// 的 success page. Writing first keeps 响应 在 wire.
	if err := callback.Serve(w, result); err != nil {
		slog.Warn("Failed to render OAuth callback page", "error", err)
	}

	// 一个 redirect 用 no flight waiting means tab was reloaded 或
	// revisited after 流程 finished. page above already described
	// outcome accurately, 所以 do 不 disturb any later authorization.
	if flight := r.current(); flight != nil {
		if result.Failed() {
			flight.settle(nil, fmt.Errorf("OAuth error: %s: %s", result.ErrorCode, result.ErrorDescription))
		} else {
			flight.settle(&auth.AuthorizationResult{
				Code:  query.Get("code"),
				State: query.Get("state"),
				// Required 通过 servers 那 implement RFC 9207.
				Iss: query.Get("iss"),
			}, nil)
		}
	}
}

func (r *callbackReceiver) fetchAuthorizationCode(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
	flight, owned, err := r.begin()
	if err != nil {
		return nil, err
	}
	if !owned {
		// Another 请求 already opened browser 为 这 服务端. Wait
		// 为 那 redirect instead 的 opening second tab.
		slog.Debug("Joining in-progress MCP OAuth authorization", "name", r.serverName, "waiting", flight.joined.Load())
		return r.await(ctx, flight, false)
	}
	defer r.end(flight)

	// Some authorization servers reject "resource" 查询 parameter 在
	// authorization URL (RFC 8707) 但 受理 it during token exchange.
	// Strip it 从 browser URL 到 avoid server_error responses.
	authURL := stripResourceParam(args.URL)
	slog.Info("Opening browser for MCP OAuth authorization")

	r.handler.mu.Lock()
	r.handler.authURL = authURL
	open := r.handler.openURL
	suppress := r.handler.suppressBrowser
	r.handler.mu.Unlock()

	if suppress {
		slog.Info("Browser suppressed; remote client must open the authorization URL", "url", authURL)
	} else if err := open(authURL); err != nil {
		// If browser can't 是 opened (headless, remote SSH), keep
		// callback listener running 和 tell 用户 到 open
		// URL manually.
		slog.Warn("Failed to open browser automatically", "error", err)
		slog.Info("Please open the following URL in your browser to authorize", "url", authURL)
	}

	return r.await(ctx, flight, true)
}

// await blocks until flight settles 或 ctx 是 cancelled. owner
// releases callback listener once flow 是 done, 所以 port 是
// free again 作为 soon 作为 authorization completes (或 是 abandoned).
func (r *callbackReceiver) await(ctx context.Context, flight *authFlight, owned bool) (*auth.AuthorizationResult, error) {
	select {
	case <-flight.done:
		if owned {
			r.release()
		}
		if flight.err != nil {
			slog.Error("MCP OAuth authorization failed", "error", flight.err)
			return nil, flight.err
		}
		slog.Info("MCP OAuth authorization completed")
		return flight.result, nil
	case <-ctx.Done():
		slog.Warn("MCP OAuth authorization cancelled")
		if owned {
			// Abandoning tab we opened; make sure nobody 保持
			// waiting 在 redirect 那 是 no longer coming.
			flight.settle(nil, ctx.Err())
			r.release()
		}
		return nil, ctx.Err()
	}
}

// close shuts receiver down permanently 和 fails any authorization
// still waiting 在 redirect, 所以 pending flow ends promptly instead 的
// hanging until 它的 上下文 expires. After close, bind refuses 到 start
// new listener.
func (r *callbackReceiver) close() {
	r.mu.Lock()
	r.closed = true
	server, flight := r.server, r.flight
	r.server = nil
	r.flight = nil
	r.mu.Unlock()

	if server != nil {
		_ = server.Close()
	}
	if flight != nil {
		flight.settle(nil, errors.New("OAuth callback listener closed"))
	}
}

// metadataFixupRoundTripper normalizes trailing-slash issuers 在 OAuth
// metadata responses. Some servers return issuer 用 trailing slash
// 那 doesn't match URL metadata was fetched 从, causing
// SDK's strict RFC 8414 validation 到 reject it. Based 在 Bruno Krugel's
// fix 从 PR #3396.
type metadataFixupRoundTripper struct {
	base http.RoundTripper
}

func newMetadataFixupRoundTripper(base http.RoundTripper) *metadataFixupRoundTripper {
	return &metadataFixupRoundTripper{base: base}
}

func (rt *metadataFixupRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if !isMetadataEndpoint(req.URL.Path) || resp.StatusCode != http.StatusOK || resp.Body == nil {
		return resp, nil
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read metadata response: %w", err)
	}

	var raw map[string]any
	if json.Unmarshal(body, &raw) != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}

	issuer, ok := raw["issuer"].(string)
	if !ok || !strings.HasSuffix(issuer, "/") {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}

	raw["issuer"] = strings.TrimSuffix(issuer, "/")
	fixed, err := json.Marshal(raw)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}

	slog.Debug("Normalized OAuth metadata issuer trailing slash", "url", req.URL.String())
	resp.Body = io.NopCloser(bytes.NewReader(fixed))
	resp.ContentLength = int64(len(fixed))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(fixed)))
	return resp, nil
}

// newOAuthMetadataClient creates HTTP 客户端 为 OAuth flow 那
// smooths over two nonstandard behaviors seen behind corporate proxies:
//
//  1. Trailing-slash issuers 在 metadata responses, normalized 通过
//     metadataFixupRoundTripper 所以 they pass SDK's strict RFC 8414
//     validation.
//  2. Metadata discovery requests 那 get 3xx-redirected 到
//     unreachable internal 主机 (e.g. cluster address behind proxy).
//
// Well-known discovery 是 never supposed 到 hop hosts via redirects
// ( authorization 服务端 location comes 从 metadata body,
// 不 Location header), 所以 为 metadata endpoints we rewrite
// redirect back 到 original MCP host. Token, registration, 和
// authorize requests 是 left untouched, 所以 separately hosted
// identity 提供方 keeps working.
func newOAuthMetadataClient(base http.RoundTripper, serverURL string) *http.Client {
	var originalHost, originalScheme string
	if u, err := url.Parse(serverURL); err == nil {
		originalHost = u.Host
		originalScheme = u.Scheme
	}
	return &http.Client{
		Transport: newMetadataFixupRoundTripper(base),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Supplying CheckRedirect replaces net/http's 默认, 所以
			// re-enforce 它的 10-redirect cap here.
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if originalHost != "" && isMetadataEndpoint(req.URL.Path) && req.URL.Host != originalHost {
				slog.Debug("Rewriting OAuth metadata redirect back to original host",
					"from", req.URL.Host, "to", originalHost)
				req.URL.Host = originalHost
				req.URL.Scheme = originalScheme
				req.Host = originalHost
			}
			return nil
		},
	}
}

func isMetadataEndpoint(path string) bool {
	return strings.Contains(path, "/.well-known/oauth-authorization-server") ||
		strings.Contains(path, "/.well-known/oauth-protected-resource")
}

// stripResourceParam removes "resource" 查询 parameter 从
// authorization URL. Some authorization servers reject it 在
// authorize 请求 但 受理 it during token exchange. Based 在 Bruno
// Krugel's fix 从 PR #3396.
func stripResourceParam(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	if q.Has("resource") {
		q.Del("resource")
		u.RawQuery = q.Encode()
		slog.Debug("Stripped resource parameter from authorization URL")
	}
	return u.String()
}
