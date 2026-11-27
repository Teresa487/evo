package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/oauth"
)

// deviceCodeEndpoint 是 variable 所以 tests can point it 在 stub
// 服务端.
var deviceCodeEndpoint = Issuer + "/oauth2/device/code"

// DeviceCode 是 RFC 8628 device authorization grant issued 通过
// authorization 服务端.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// RequestDeviceCode starts device authorization flow. 它 是
// sign-在 路径 为 machines where browser cannot reach loopback
// callback — 或 当 用户 declines browser 权限 和 enters
// code 在 accounts.x.ai instead.
func RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	vals := url.Values{
		"client_id": {ClientID},
		"scope":     {Scope},
		"referrer":  {"crush"},
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		deviceCodeEndpoint,
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
		return nil, fmt.Errorf("read device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request device code: status %d body %q", resp.StatusCode, body)
	}

	var dc DeviceCode
	if err := json.Unmarshal(body, &dc); err != nil {
		return nil, fmt.Errorf("decode device code response: %w", err)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, fmt.Errorf("device code response contained no device or user code")
	}
	return &dc, nil
}

// pollInterval 是 RFC 8628 polling interval used 当 服务端
// does 不 advertise one. 一个 variable 所以 tests can shorten sleeps.
var pollInterval = 5 * time.Second

// PollForToken polls token endpoint until 用户 approves
// device code 在 verification page, it expires, 或 authorization
// 是 denied. ctx cancellation aborts poll.
func PollForToken(ctx context.Context, deviceCode string, expiresIn int) (*oauth.Token, error) {
	interval := pollInterval
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	if expiresIn <= 0 {
		deadline = time.Now().Add(15 * time.Minute)
	}

	for {
		token, err := requestToken(ctx, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {deviceCode},
			"client_id":   {ClientID},
		})
		if err == nil {
			return token, nil
		}

		var exchangeErr *oauth.TokenExchangeError
		if !errors.As(err, &exchangeErr) {
			return nil, err
		}
		switch oauthErrorCode(exchangeErr.Body) {
		case "authorization_pending":
			// Keep waiting 为 用户.
		case "slow_down":
			interval += pollInterval
		case "expired_token":
			return nil, fmt.Errorf("device code expired, please try again")
		case "access_denied":
			return nil, fmt.Errorf("authorization was denied")
		default:
			return nil, err
		}

		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("device code expired, please try again")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// oauthErrorCode extracts OAuth 错误 code 从 token endpoint
// 错误 body, which requestToken reports verbatim.
func oauthErrorCode(body string) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	return e.Error
}
