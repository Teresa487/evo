// Package grok implements OAuth 认证 against xAI's
// authorization 服务端, letting users sign 在 用 Grok account their
// subscription 是 attached 到 和 use it through xAI API.
//
// flow 是 same PKCE-based browser authorization Grok CLI
// uses: loopback HTTP 服务端 在 random port receives
// authorization code, which 是 then exchanged 为 access, refresh, 和
// ID tokens. access token 是 valid Bearer credential 为
// api.x.ai/v1.
package grok

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/oauth"
)

const (
	// ClientID 是 public OAuth 客户端 ID xAI's own clients (
	// Grok CLI) register 用 authorization 服务端. xAI does 不
	// offer self-service 客户端 registration, 所以 agent CLIs share it.
	ClientID = "b1a00492-073a-47ea-816f-4c329264a828"

	// Issuer 是 xAI's authorization 服务端.
	Issuer = "https://auth.x.ai"

	// Scope requests claims needed 到 identify account,
	// refresh token via offline_access, 和 access 到 xAI API.
	// conversation 和 工作区 scopes 是 片段 的 shared
	// 客户端's registered set, 所以 服务端 expects all 的 them.
	Scope = "openid profile email offline_access grok-cli:access " +
		"api:access conversations:read conversations:write " +
		"workspaces:read workspaces:write"
)

// authorizeEndpoint 和 tokenEndpoint 是 variables 所以 tests can point
// them 在 stub 服务端.
var (
	authorizeEndpoint = Issuer + "/oauth2/authorize"
	tokenEndpoint     = Issuer + "/oauth2/token"
)

// HTTPClient allows tests 到 stub out token endpoint. Production
// leaves it 作为 plain 客户端 用 generous 超时.
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

// PKCE holds proof key 为 code exchange.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE generates PKCE 配对 using S256 方法.
func NewPKCE() (PKCE, error) {
	verifier, err := randomToken(64)
	if err != nil {
		return PKCE{}, fmt.Errorf("generate code verifier: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

// State returns fresh random state parameter 为 CSRF protection.
func State() (string, error) {
	return randomToken(32)
}

// Nonce returns fresh random nonce binding ID token 到 这
// 授权.
func Nonce() (string, error) {
	return randomToken(32)
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthorizeURL builds browser authorization URL 为 given
// redirect URI, PKCE pair, state, 和 nonce.
func AuthorizeURL(redirectURI string, pkce PKCE, state, nonce string) string {
	vals := url.Values{
		"response_type":         {"code"},
		"client_id":             {ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {Scope},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"nonce":                 {nonce},
		"referrer":              {"evo"},
	}
	return authorizeEndpoint + "?" + vals.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// ExchangeCode trades authorization code captured 通过 callback
// 服务端 为 OAuth token.
func ExchangeCode(ctx context.Context, code, redirectURI string, pkce PKCE) (*oauth.Token, error) {
	vals := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {ClientID},
		"code_verifier": {pkce.Verifier},
	}
	token, err := requestToken(ctx, vals)
	if err != nil {
		return nil, fmt.Errorf("exchange authorization code: %w", err)
	}
	return token, nil
}

// RefreshToken exchanges refresh token 为 fresh token pair.
// authorization 服务端 does 不 always rotate refresh tokens, 所以
// previous one 是 kept 当 响应 omits it.
func RefreshToken(ctx context.Context, refreshToken string) (*oauth.Token, error) {
	vals := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {ClientID},
	}
	token, err := requestToken(ctx, vals)
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	if token.RefreshToken == "" {
		token.RefreshToken = refreshToken
	}
	return token, nil
}

func requestToken(ctx context.Context, vals url.Values) (*oauth.Token, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		tokenEndpoint,
		strings.NewReader(vals.Encode()),
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &oauth.TokenExchangeError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token response contained no access token")
	}

	token := &oauth.Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		IDToken:      tr.IDToken,
		ExpiresIn:    tr.ExpiresIn,
	}
	token.SetExpiresAt()

	return token, nil
}
