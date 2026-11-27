package mcpoauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// fakeASOpts configures fake authorization 服务端 所以 each 测试 can
// exercise specific branch 的 discovery + registration + token flow.
type fakeASOpts struct {
	clientID       string // client_id returned by /register
	accessToken    string // access_token returned by /token for a code exchange
	refreshedToken string // access_token returned by /token for a refresh grant
	refreshToken   string // refresh_token returned by /token
	tokenExpiresIn int    // expires_in returned by /token (0 => 3600)
	failRegister   bool   // make /register return 500 (server has no DCR)
	// issSupported advertises RFC 9207: 服务端 promises 到 name itself
	// 在 authorization 响应, 和 SDK rejects authorization
	// 如果 no issuer comes back.
	issSupported bool
}

// newFakeAS starts httptest 服务端 speaking enough 的 OAuth
// discovery, dynamic-registration, 和 token protocol 为 go-sdk
// AuthorizationCodeHandler 到 运行 end 到 end. 它 returns base URL 和
// MCP 服务端 URL (base + /mcp).
func newFakeAS(t *testing.T, opts fakeASOpts) (base, mcpURL string) {
	t.Helper()
	var baseURL string
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	expiresIn := opts.tokenExpiresIn
	if expiresIn == 0 {
		expiresIn = 3600
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"resource":              baseURL + "/mcp",
			"authorization_servers": []string{baseURL},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{
			"issuer":                           baseURL,
			"authorization_endpoint":           baseURL + "/authorize",
			"token_endpoint":                   baseURL + "/token",
			"registration_endpoint":            baseURL + "/register",
			"code_challenge_methods_supported": []string{"S256"},
			"scopes_supported":                 []string{"offline_access"},
		}
		if opts.issSupported {
			meta["authorization_response_iss_parameter_supported"] = true
		}
		writeJSON(w, meta)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		if opts.failRegister {
			http.Error(w, "registration not supported", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"client_id":                  opts.clientID,
			"token_endpoint_auth_method": "none",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		access := opts.accessToken
		if r.Form.Get("grant_type") == "refresh_token" && opts.refreshedToken != "" {
			access = opts.refreshedToken
		}
		writeJSON(w, map[string]any{
			"access_token":  access,
			"refresh_token": opts.refreshToken,
			"token_type":    "Bearer",
			"expires_in":    expiresIn,
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	baseURL = srv.URL
	return srv.URL, srv.URL + "/mcp"
}

// browserRedirect simulates 用户's browser: it extracts
// redirect_uri 和 state 从 authorization URL 和 calls local
// callback 用 fixed code, driving flow forward without real
// 浏览器.
func browserRedirect(code string) func(string) error {
	return browserRedirectIss(code, "")
}

// browserRedirectIss 是 browserRedirect 为 服务端 那 implements RFC
// 9207: redirect also names issuer. Pass empty iss 为 服务端
// 那 does 不.
func browserRedirectIss(code, iss string) func(string) error {
	return func(rawAuthURL string) error {
		u, err := url.Parse(rawAuthURL)
		if err != nil {
			return err
		}
		q := u.Query()
		cb, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			return err
		}
		cbq := cb.Query()
		cbq.Set("code", code)
		cbq.Set("state", q.Get("state"))
		if iss != "" {
			cbq.Set("iss", iss)
		}
		cb.RawQuery = cbq.Encode()
		go func() {
			resp, err := http.Get(cb.String()) //nolint:noctx
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

// authorizeWith401 creates 401 响应 用 appropriate
// WWW-Authenticate header 和 passes it 到 处理器's Authorize
// method. 响应 body 是 consumed 和 closed within 这 function
// 所以 callers don't need 到 worry about bodyclose.
func authorizeWith401(t *testing.T, h *Handler, base, mcpURL string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, mcpURL, nil)
	require.NoError(t, err)
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header: http.Header{
			"Www-Authenticate": []string{
				`Bearer resource_metadata="` + base + `/.well-known/oauth-protected-resource/mcp"`,
			},
		},
		Body: io.NopCloser(bytes.NewReader(nil)),
	}
	defer resp.Body.Close()
	return h.Authorize(t.Context(), req, resp)
}

// TestHandler_FreshAuthorize drives whole authorization-code flow 和
// asserts token 是 captured 和 persisted together 用 registered
// 客户端 ID 和 endpoints, 所以 later start can refresh without browser.
func TestHandler_FreshAuthorize(t *testing.T) {
	base, mcpURL := newFakeAS(t, fakeASOpts{
		clientID:     "fresh-client",
		accessToken:  "fresh-access",
		refreshToken: "fresh-refresh",
	})

	var (
		mu    sync.Mutex
		saved *oauth.Token
	)
	h, err := NewHandler("test", mcpURL, nil, nil, func(tok *oauth.Token) {
		mu.Lock()
		saved = tok
		mu.Unlock()
	}, true, 0)
	require.NoError(t, err)
	t.Cleanup(h.Close)
	h.openURL = browserRedirect("fresh-code")

	require.NoError(t, authorizeWith401(t, h, base, mcpURL))

	ts, err := h.TokenSource(t.Context())
	require.NoError(t, err)
	require.NotNil(t, ts)
	tok, err := ts.Token()
	require.NoError(t, err)
	require.Equal(t, "fresh-access", tok.AccessToken)

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, saved, "token must be persisted via the saver")
	require.Equal(t, "fresh-access", saved.AccessToken)
	require.Equal(t, "fresh-refresh", saved.RefreshToken)
	require.NotNil(t, saved.Client)
	require.Equal(t, "fresh-client", saved.Client.ClientID)
	require.Equal(t, base+"/token", saved.Client.TokenURL)
}

// TestHandler_PreregisteredClientSkipsDCR proves 那 configured 客户端 是
// used even 当 服务端 does 不 support dynamic 客户端 registration
// (作为 用 GitHub 或 Slack): flow authorizes without ever calling
// /register successfully.
func TestHandler_PreregisteredClientSkipsDCR(t *testing.T) {
	base, mcpURL := newFakeAS(t, fakeASOpts{
		accessToken:  "prereg-access",
		refreshToken: "prereg-refresh",
		failRegister: true, // server rejects DCR
	})

	preregistered := &oauth.OAuthClient{ClientID: "configured-client"}
	var saved *oauth.Token
	h, err := NewHandler("test", mcpURL, nil, preregistered, func(tok *oauth.Token) {
		saved = tok
	}, true, 0)
	require.NoError(t, err)
	t.Cleanup(h.Close)
	h.openURL = browserRedirect("prereg-code")

	require.NoError(t, authorizeWith401(t, h, base, mcpURL))

	ts, err := h.TokenSource(t.Context())
	require.NoError(t, err)
	tok, err := ts.Token()
	require.NoError(t, err)
	require.Equal(t, "prereg-access", tok.AccessToken)
	require.NotNil(t, saved)
	require.Equal(t, "configured-client", saved.Client.ClientID)
}

// TestHandler_RestoreSkipsBrowser proves restored, unexpired token 是 used
// directly: TokenSource returns it 和 browser 是 never opened.
func TestHandler_RestoreSkipsBrowser(t *testing.T) {
	base, mcpURL := newFakeAS(t, fakeASOpts{clientID: "saved-client"})

	saved := &oauth.Token{
		AccessToken:  "restored-access",
		RefreshToken: "restored-refresh",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		Client: &oauth.OAuthClient{
			ClientID: "saved-client",
			AuthURL:  base + "/authorize",
			TokenURL: base + "/token",
		},
	}

	h, err := NewHandler("test", mcpURL, saved, nil, func(*oauth.Token) {}, false, 0)
	require.NoError(t, err)
	t.Cleanup(h.Close)
	h.openURL = func(string) error {
		t.Error("browser must not open when a valid token is restored")
		return nil
	}

	ts, err := h.TokenSource(t.Context())
	require.NoError(t, err)
	require.NotNil(t, ts)
	tok, err := ts.Token()
	require.NoError(t, err)
	require.Equal(t, "restored-access", tok.AccessToken)
}

// TestHandler_RefreshPersists proves expired restored token 是 refreshed
// via 存储 token endpoint 和 new token 是 persisted, all without
// 浏览器.
func TestHandler_RefreshPersists(t *testing.T) {
	base, mcpURL := newFakeAS(t, fakeASOpts{
		clientID:       "saved-client",
		refreshedToken: "refreshed-access",
		refreshToken:   "next-refresh",
	})

	saved := &oauth.Token{
		AccessToken:  "stale-access",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-time.Hour).Unix(), // expired
		Client: &oauth.OAuthClient{
			ClientID: "saved-client",
			AuthURL:  base + "/authorize",
			TokenURL: base + "/token",
		},
	}

	var (
		mu    sync.Mutex
		saver *oauth.Token
	)
	h, err := NewHandler("test", mcpURL, saved, nil, func(tok *oauth.Token) {
		mu.Lock()
		saver = tok
		mu.Unlock()
	}, false, 0)
	require.NoError(t, err)
	t.Cleanup(h.Close)
	h.openURL = func(string) error {
		t.Error("browser must not open when refreshing a token")
		return nil
	}

	ts, err := h.TokenSource(t.Context())
	require.NoError(t, err)
	tok, err := ts.Token()
	require.NoError(t, err)
	require.Equal(t, "refreshed-access", tok.AccessToken)

	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, saver, "refreshed token must be persisted")
	require.Equal(t, "refreshed-access", saver.AccessToken)
}

func TestHasRefreshableToken(t *testing.T) {
	t.Parallel()
	full := &oauth.Token{AccessToken: "a", Client: &oauth.OAuthClient{TokenURL: "https://x/token"}}
	tests := []struct {
		name string
		tok  *oauth.Token
		want bool
	}{
		{"nil", nil, false},
		{"no access token", &oauth.Token{Client: &oauth.OAuthClient{TokenURL: "x"}}, false},
		{"no client", &oauth.Token{AccessToken: "a"}, false},
		{"no token url", &oauth.Token{AccessToken: "a", Client: &oauth.OAuthClient{}}, false},
		{"complete", full, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, hasRefreshableToken(tt.tok))
		})
	}
}

// staticSource returns same token every 调用, letting us assert
// saver fires 只 当 access token actually changes.
type staticSource struct{ tok *oauth2.Token }

func (s staticSource) Token() (*oauth2.Token, error) { return s.tok, nil }

func TestSavingTokenSource_FiresOnChangeOnly(t *testing.T) {
	t.Parallel()

	tok := &oauth2.Token{AccessToken: "same"}
	var calls int
	ts := NewSavingTokenSource(staticSource{tok}, nil, tok, func(*oauth2.Config, *oauth2.Token) {
		calls++
	})

	_, err := ts.Token()
	require.NoError(t, err)
	_, err = ts.Token()
	require.NoError(t, err)
	require.Zero(t, calls, "unchanged token must not trigger the saver")

	changing := &oauth2.Token{AccessToken: "new"}
	ts2 := NewSavingTokenSource(staticSource{changing}, nil, tok, func(*oauth2.Config, *oauth2.Token) {
		calls++
	})
	_, err = ts2.Token()
	require.NoError(t, err)
	require.Equal(t, 1, calls, "changed token must trigger the saver once")
}

func TestSavingTokenSource_NilInputs(t *testing.T) {
	t.Parallel()
	require.Nil(t, NewSavingTokenSource(nil, nil, nil, func(*oauth2.Config, *oauth2.Token) {}))
	src := staticSource{&oauth2.Token{AccessToken: "x"}}
	require.Equal(t, oauth2.TokenSource(src), NewSavingTokenSource(src, nil, nil, nil))
}

// TestHandler_AuthorizeError proves OAuth 错误 在 callback surfaces
// 作为 authorization failure rather than captured token.
func TestHandler_AuthorizeError(t *testing.T) {
	base, mcpURL := newFakeAS(t, fakeASOpts{clientID: "c", accessToken: "a"})
	h, err := NewHandler("test", mcpURL, nil, nil, func(*oauth.Token) {}, true, 0)
	require.NoError(t, err)
	t.Cleanup(h.Close)

	// Simulate 用户 denying consent: redirect back 用 错误.
	h.openURL = func(rawAuthURL string) error {
		u, _ := url.Parse(rawAuthURL)
		cb, _ := url.Parse(u.Query().Get("redirect_uri"))
		q := cb.Query()
		q.Set("error", "access_denied")
		q.Set("error_description", "user said no")
		cb.RawQuery = q.Encode()
		go func() {
			resp, gerr := http.Get(cb.String()) //nolint:noctx
			if gerr == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}

	authErr := authorizeWith401(t, h, base, mcpURL)
	require.Error(t, authErr)
	require.Contains(t, authErr.Error(), "access_denied")
}

// TestHandler_BackgroundAuthorizeRefused 证明 后台 (非交互)
// 连接 never opens browser: Authorize fails fast 用
// ErrInteractiveAuthRequired 所以 caller can surface needs-auth state.
func TestHandler_BackgroundAuthorizeRefused(t *testing.T) {
	base, mcpURL := newFakeAS(t, fakeASOpts{clientID: "c", accessToken: "a"})
	h, err := NewHandler("test", mcpURL, nil, nil, func(*oauth.Token) {}, false, 0)
	require.NoError(t, err)
	t.Cleanup(h.Close)
	h.openURL = func(string) error {
		t.Error("browser must not open for a background connection")
		return nil
	}

	err = authorizeWith401(t, h, base, mcpURL)
	require.ErrorIs(t, err, ErrInteractiveAuthRequired)
}

// TestHandler_BrowserSuppressed 证明 SetBrowserSuppress prevents
// browser 从 opening while still recording authorization URL, which
// 是 how remote 客户端 surfaces flow 在 用户's machine.
// returned restore function re-enables 浏览器.
func TestHandler_BrowserSuppressed(t *testing.T) {
	base, mcpURL := newFakeAS(t, fakeASOpts{clientID: "c", accessToken: "a"})
	h, err := NewHandler("test", mcpURL, nil, nil, func(*oauth.Token) {}, true, 0)
	require.NoError(t, err)
	t.Cleanup(h.Close)

	var opens atomic.Int64
	h.openURL = func(string) error {
		opens.Add(1)
		return browserRedirect("code")(h.AuthURL())
	}

	restore := h.SetBrowserSuppress(true)

	// Suppressed: flow generates URL 但 never opens browser.
	// Drive the callback manually using the recorded URL.
	done := make(chan error, 1)
	go func() {
		done <- authorizeWith401(t, h, base, mcpURL)
	}()

	require.Eventually(t, func() bool { return h.AuthURL() != "" },
		2*time.Second, 10*time.Millisecond, "auth URL should be recorded")
	require.Equal(t, int64(0), opens.Load(), "browser must not open while suppressed")

	// 完成 flow 通过 simulating browser redirect.
	require.NoError(t, browserRedirect("code")(h.AuthURL()))
	require.NoError(t, <-done)
	require.Equal(t, int64(0), opens.Load())

	// Restoring re-enables 浏览器.
	restore()
	h.mu.Lock()
	suppressed := h.suppressBrowser
	h.mu.Unlock()
	require.False(t, suppressed)
}

// TestCallbackReceiver_IgnoresNonCallbackPaths 是 regression 测试 为
// browser incidentally aborting flow. listener answered every 路径,
// 所以 请求 为 something like /favicon.ico could win one-time
// handoff 和 hand flow empty authorization code.
func TestCallbackReceiver_IgnoresNonCallbackPaths(t *testing.T) {
	t.Parallel()

	r := &callbackReceiver{}
	t.Cleanup(r.close)

	base := serveReceiver(t, r)

	flight, owned, err := r.begin()
	require.NoError(t, err)
	require.True(t, owned)

	// 一个 stray 请求 must 不 是 mistaken 为 redirect.
	resp, err := http.Get(base + "/favicon.ico") //nolint:noctx
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Empty(t, flight.done, "a stray request must not complete the flow")

	// The real redirect still lands.
	resp, err = http.Get(base + callbackPath + "?code=abc&state=xyz") //nolint:noctx
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "You’re all set")

	<-flight.done
	require.NoError(t, flight.err)
	require.Equal(t, "abc", flight.result.Code)
	require.Equal(t, "xyz", flight.result.State)
}

// TestCallbackReceiver_RendersFailurePage 证明 denied 授权
// reaches 用户 作为 readable page naming 服务端, 不 just status
// line 在 终态.
func TestCallbackReceiver_RendersFailurePage(t *testing.T) {
	t.Parallel()

	r := &callbackReceiver{serverName: "linear"}
	t.Cleanup(r.close)

	base := serveReceiver(t, r)

	flight, owned, err := r.begin()
	require.NoError(t, err)
	require.True(t, owned)

	url := base + callbackPath +
		"?error=access_denied&error_description=user+said+no"
	resp, err := http.Get(url) //nolint:noctx
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(body), "access_denied")
	require.Contains(t, string(body), "user said no")
	require.Contains(t, string(body), "linear")

	<-flight.done
	require.ErrorContains(t, flight.err, "access_denied")
}

// TestCallbackReceiver_ConcurrentAuthorizeOpensOneTab 是 regression 测试
// 为 browser opening twice 为 single login. Connecting 到 服务端
// can put several requests 在 flight, 和 each one 那 meets 401 asks 到
// authorize. Every ask used 到 open 它的 own tab 和 then contend 为
// single redirect, 所以 用户 saw two tabs 和 one 的 two requests
// waited 在 redirect 那 had already been consumed, hanging until 它的
// 上下文 expired.
func TestCallbackReceiver_ConcurrentAuthorizeOpensOneTab(t *testing.T) {
	t.Parallel()

	r := &callbackReceiver{serverName: "linear"}
	t.Cleanup(r.close)

	base := serveReceiver(t, r)

	const callers = 4

	// Stand 在 为 browser: record open, then redirect back once
	// every caller has joined, 作为 they would while real 用户 consents.
	var opens atomic.Int64
	r.handler = &Handler{openURL: func(string) error {
		opens.Add(1)
		go func() {
			assert.Eventually(t, func() bool {
				flight := r.current()
				return flight != nil && flight.joined.Load() == callers-1
			}, 10*time.Second, time.Millisecond)
			resp, gerr := http.Get(base + callbackPath + "?code=abc&state=xyz") //nolint:noctx
			if gerr == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}}

	var wg sync.WaitGroup
	results := make(chan *auth.AuthorizationResult, callers)
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			result, ferr := r.fetchAuthorizationCode(t.Context(), &auth.AuthorizationArgs{URL: base + "/authorize"})
			results <- result
			errs <- ferr
		})
	}
	wg.Wait()
	close(results)
	close(errs)

	require.Equal(t, int64(1), opens.Load(), "one login must open exactly one tab")

	// Every caller 是 served same authorization code, 和 none 是 left
	// waiting 在 redirect it will never see.
	for err := range errs {
		require.NoError(t, err)
	}
	for result := range results {
		require.NotNil(t, result)
		require.Equal(t, "abc", result.Code)
	}
}

// TestCallbackReceiver_AuthorizeTwiceInSequence 证明 later
// authorization still works. 处理器 outlives single login (tokens
// get revoked, servers reconnect), 和 redirect handoff used 到 是
// once-per-处理器, 所以 any second attempt hung forever.
func TestCallbackReceiver_AuthorizeTwiceInSequence(t *testing.T) {
	t.Parallel()

	r := &callbackReceiver{serverName: "linear"}
	t.Cleanup(r.close)

	base := serveReceiver(t, r)

	var opens atomic.Int64
	code := "first"
	r.handler = &Handler{openURL: func(string) error {
		opens.Add(1)
		go func() {
			resp, gerr := http.Get(base + callbackPath + "?code=" + code) //nolint:noctx
			if gerr == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}}

	args := &auth.AuthorizationArgs{URL: base + "/authorize"}

	result, err := r.fetchAuthorizationCode(t.Context(), args)
	require.NoError(t, err)
	require.Equal(t, "first", result.Code)

	code = "second"
	result, err = r.fetchAuthorizationCode(t.Context(), args)
	require.NoError(t, err)
	require.Equal(t, "second", result.Code)

	require.Equal(t, int64(2), opens.Load(), "each login opens its own tab")
}

// serveReceiver binds receiver's listener 和 returns base URL
// authorization 服务端 would redirect 到. tests construct
// receiver directly (fixedPort 0) 和 运行 在 parallel, 所以 pin
// ephemeral port rather than shared callbackPorts 条目 — otherwise
// parallel runs collide 在 same candidate.
func serveReceiver(t *testing.T, r *callbackReceiver) string {
	t.Helper()
	if r.fixedPort == 0 {
		lc := &net.ListenConfig{}
		probe, err := lc.Listen(t.Context(), "tcp", "localhost:0")
		require.NoError(t, err)
		r.fixedPort = probe.Addr().(*net.TCPAddr).Port
		_ = probe.Close()
	}
	require.NoError(t, r.bind())
	return fmt.Sprintf("http://localhost:%d", r.port)
}

// TestHandler_PassesIssuerThrough 是 regression 测试 为 logins failing
// against servers 那 implement RFC 9207. Such 服务端 names itself 在
// redirect 和 SDK rejects authorization 当 那 name does
// 不 come back, 所以 dropping it broke 连接 outright 和 sent
// 用户 round browser flow again.
func TestHandler_PassesIssuerThrough(t *testing.T) {
	base, mcpURL := newFakeAS(t, fakeASOpts{
		clientID:     "c",
		accessToken:  "a",
		issSupported: true,
	})
	h, err := NewHandler("test", mcpURL, nil, nil, func(*oauth.Token) {}, true, 0)
	require.NoError(t, err)
	t.Cleanup(h.Close)

	var opens atomic.Int64
	redirect := browserRedirectIss("code123", base)
	h.openURL = func(u string) error {
		opens.Add(1)
		return redirect(u)
	}

	require.NoError(t, authorizeWith401(t, h, base, mcpURL))
	require.Equal(t, int64(1), opens.Load())
	require.NotNil(t, h.Token())
	require.Equal(t, "a", h.Token().AccessToken)
}

// TestHandler_RejectsWrongIssuer confirms issuer 是 passed through 为
// checking rather than merely echoed: redirect naming different 服务端
// 必须 fail login.
func TestHandler_RejectsWrongIssuer(t *testing.T) {
	base, mcpURL := newFakeAS(t, fakeASOpts{
		clientID:     "c",
		accessToken:  "a",
		issSupported: true,
	})
	h, err := NewHandler("test", mcpURL, nil, nil, func(*oauth.Token) {}, true, 0)
	require.NoError(t, err)
	t.Cleanup(h.Close)

	h.openURL = browserRedirectIss("code123", "https://attacker.example.com")

	require.ErrorContains(t, authorizeWith401(t, h, base, mcpURL), "issuer")
}

// newFakeMCP starts 服务端 那 refuses requests until bearer token 是
// present, pointing 在 given authorization 服务端. 它 returns MCP
// 端点 URL.
func newFakeMCP(t *testing.T, authServer string) string {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              srv.URL + "/mcp",
			"authorization_servers": []string{authServer},
		})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.Header().Set("WWW-Authenticate",
				`Bearer resource_metadata="`+srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Authorization 是 all 这 fake 是 为; MCP handshake itself
		// 是 left 到 fail 所以 测试 stays focused.
		w.WriteHeader(http.StatusNotFound)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/mcp"
}

// TestConnect_OneLoginOpensOneTab drives real 客户端 连接 against
// 服务端 那 requires authorization, 和 是 regression 测试 为
// single login opening two 浏览器 tabs.
//
// Connecting makes more than one 请求, 和 each 拒绝 asks 到 log 在.
// first tab's redirect was being accepted 但 then rejected during
// validation, 所以 login failed just 作为 用户 finished it 和 next
// 请求 opened another tab.
func TestConnect_OneLoginOpensOneTab(t *testing.T) {
	authServer, _ := newFakeAS(t, fakeASOpts{
		clientID:     "c",
		accessToken:  "tok",
		issSupported: true,
	})
	endpoint := newFakeMCP(t, authServer)

	h, err := NewHandler("test", endpoint, nil, nil, func(*oauth.Token) {}, true, 0)
	require.NoError(t, err)
	t.Cleanup(h.Close)

	var opens atomic.Int64
	redirect := browserRedirectIss("code123", authServer)
	h.openURL = func(u string) error {
		opens.Add(1)
		return redirect(u)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "crush", Version: "test"}, nil)
	// handshake itself fails 通过 design; tab count 是 subject.
	_, _ = client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:     endpoint,
		OAuthHandler: h,
	}, nil)

	require.Equal(t, int64(1), opens.Load(), "one login must open exactly one browser tab")
	require.NotNil(t, h.Token(), "the login must yield a usable token")
}
