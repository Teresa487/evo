// Package openai implements OAuth 认证 against OpenAI's
// authorization 服务端, letting users sign 在 用 ChatGPT account 和
// use their 订阅 through Codex backend.
//
// flow 是 same PKCE-based browser authorization Codex CLI
// uses: loopback HTTP 服务端 在 fixed port receives authorization
// code, which 是 then exchanged 为 access, refresh, 和 ID tokens.
package openai

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
	// ClientID 是 public OAuth 客户端 ID Codex CLI desktop flow
	// registers 用 OpenAI's authorization 服务端.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// Issuer 是 OpenAI's authorization 服务端.
	Issuer = "https://auth.openai.com"

	// CodexBaseURL 是 ChatGPT backend 那 serves Codex requests
	// 为 ChatGPT-plan accounts. 它 speaks Responses API.
	CodexBaseURL = "https://chatgpt.com/backend-api/codex"

	// Scope requests claims needed 到 identify account plus
	// 刷新令牌 via offline_access.
	Scope = "openid profile email offline_access"
)

// tokenEndpoint 和 modelsEndpoint 是 variables 所以 tests can point them
// 在 stub 服务端.
var (
	tokenEndpoint  = Issuer + "/oauth/token"
	modelsEndpoint = CodexBaseURL + "/models"
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

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthorizeURL builds browser authorization URL 为 given
// redirect URI, PKCE pair, 和 state.
func AuthorizeURL(redirectURI string, pkce PKCE, state string) string {
	vals := url.Values{
		"response_type":              {"code"},
		"client_id":                  {ClientID},
		"redirect_uri":               {redirectURI},
		"scope":                      {Scope},
		"code_challenge":             {pkce.Challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {state},
		"originator":                 {"evo"},
	}
	return Issuer + "/oauth/authorize?" + vals.Encode()
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

	// account ID travels inside ID token (falling back 到
	// access token 为 servers 那 omit one). ChatGPT backend
	// requires it 在 every 请求 到 attribute usage 到 right
	// 账户.
	claims, err := ParseJWTClaims(token.IDToken)
	if err != nil || claims == nil {
		claims, _ = ParseJWTClaims(token.AccessToken)
	}
	token.AccountID = ExtractAccountID(claims)

	return token, nil
}

// Claims holds untrusted metadata extracted 从 token's JWT
// payload. Values 是 used 只 为 routing requests, never 为
// 授权 decisions.
type Claims struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	Auth             *struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

// ParseJWTClaims decodes payload segment 的 JWT without verifying
// 它的 signature. claims 是 advisory metadata, 所以 signature checking
// 是 服务端's job, 不 ours.
func ParseJWTClaims(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil, fmt.Errorf("invalid JWT structure")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, fmt.Errorf("decode JWT payload: %w", err)
		}
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("decode JWT claims: %w", err)
	}
	return &claims, nil
}

// ExtractAccountID resolves ChatGPT account ID 从 token claims.
func ExtractAccountID(claims *Claims) string {
	if claims == nil {
		return ""
	}
	if claims.ChatGPTAccountID != "" {
		return claims.ChatGPTAccountID
	}
	if claims.Auth != nil {
		return claims.Auth.ChatGPTAccountID
	}
	return ""
}
