package oauth

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// minRefreshBuffer 是 minimum number 的 seconds before actual
// expiry 在 which IsExpired returns true. Prevents very short-lived
// tokens 从 having meaningless refresh 窗口.
const minRefreshBuffer = 30

// OAuthClient stores 客户端 registration 和 authorization-服务端
// endpoints captured 在 first successful authorization. Persisting
// them lets later start rebuild oauth2 配置 和 refresh saved
// token without re-running discovery 或 browser flow.
type OAuthClient struct {
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	AuthURL      string `json:"auth_url,omitempty"`
	TokenURL     string `json:"token_url,omitempty"`
	AuthStyle    int    `json:"auth_style,omitempty"`
}

// 令牌 represents OAuth2 令牌.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	// AccountID 是 提供方 account token belongs 到, extracted
	// 从 ID token 当 提供方 embeds one. ChatGPT
	// backend requires it 作为 header 在 every 请求.
	AccountID string       `json:"account_id,omitempty"`
	ExpiresIn int          `json:"expires_in"`
	ExpiresAt int64        `json:"expires_at"`
	Client    *OAuthClient `json:"client,omitempty"`
}

// SetExpiresAt calculates 和 sets ExpiresAt 字段 based 在
// current time 和 ExpiresIn. If ExpiresIn 是 zero 或 negative 和
// ExpiresAt 是 already set (e.g. 从 提供方 响应), it 是
// left unchanged. If neither 是 usable, ExpiresAt 是 set 到 zero 所以
// IsExpired treats token 作为 immediately expired, forcing refresh
// rather than 猜测 有效期.
func (t *Token) SetExpiresAt() {
	if t.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second).Unix()
		return
	}
	// ExpiresIn 是 missing 或 invalid. If ExpiresAt was already
	// populated 通过 提供方 (some return exp directly), trust it.
	if t.ExpiresAt > 0 {
		slog.Warn("OAuth token has invalid expires_in but valid expires_at, using expires_at",
			"expires_in", t.ExpiresIn, "expires_at", t.ExpiresAt)
		return
	}
	// Neither 字段 是 usable. 标记 作为 expired 所以 caller 是 forced
	// 到 refresh rather than operating 用 fabricated lifetime.
	slog.Warn("OAuth token has no valid expiry information, marking as expired",
		"expires_in", t.ExpiresIn, "expires_at", t.ExpiresAt)
	t.ExpiresAt = 0
}

// IsExpired checks 如果 token 是 expired 或 about 到 expire. 它
// uses 缓冲 的 max(expires_in/10, minRefreshBuffer) seconds 到
// trigger 主动 刷新 before 令牌 actually 过期.
func (t *Token) IsExpired() bool {
	buffer := max(int64(t.ExpiresIn)/10, minRefreshBuffer)
	return time.Now().Unix() >= (t.ExpiresAt - buffer)
}

// SetExpiresIn calculates 和 sets ExpiresIn 字段 based 在 ExpiresAt 字段.
func (t *Token) SetExpiresIn() {
	t.ExpiresIn = int(time.Until(time.Unix(t.ExpiresAt, 0)).Seconds())
}

// TokenExchangeError represents failed OAuth token exchange. 它 carries
// HTTP status code 和 响应 body 所以 callers can distinguish between
// recoverable failures (e.g. temporary 服务端 错误) 和 终态 ones
// (e.g. 已撤销 刷新令牌).
type TokenExchangeError struct {
	StatusCode int
	Body       string
}

func (e *TokenExchangeError) Error() string {
	return fmt.Sprintf("token exchange failed: status %d body %q", e.StatusCode, e.Body)
}

// IsRefreshTokenRevoked reports whether 提供方 rejected credential
// itself, 所以 只 new sign-在 can help: 401, 或 OAuth invalid_grant
// 错误 (RFC 6749 section 5.2).
func (e *TokenExchangeError) IsRefreshTokenRevoked() bool {
	if e.StatusCode == http.StatusUnauthorized {
		return true
	}
	var body struct {
		Error string `json:"error"`
	}
	return json.Unmarshal([]byte(e.Body), &body) == nil && body.Error == "invalid_grant"
}
