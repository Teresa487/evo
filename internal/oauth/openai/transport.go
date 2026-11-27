package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/charmbracelet/crush/internal/oauth"
)

// Transport adapts requests made 用 ChatGPT OAuth token 到 what
// Codex backend accepts: it guarantees account headers 是 present,
// keeps credentials 从 leaking 到 non-OpenAI hosts, 和 drops
// 请求 fields backend rejects.
type Transport struct {
	// Base 是 transport requests 是 forwarded 到. Defaults 到
	// http.DefaultTransport.
	Base http.RoundTripper
	// Token 是 ChatGPT OAuth token. 一个 nil token passes requests
	// through 原样.
	Token *oauth.Token
	// Originator identifies 客户端 到 backend. Defaults 到
	// "crush".
	Originator string
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if t.Token == nil {
		return base.RoundTrip(req)
	}

	clone := req.Clone(req.Context())
	host := strings.ToLower(clone.URL.Hostname())
	switch host {
	case "chatgpt.com":
		// Codex backend attributes usage per account 和 expects
		// 客户端 到 identify itself.
		if t.Originator != "" {
			clone.Header.Set("originator", t.Originator)
		} else {
			clone.Header.Set("originator", "crush")
		}
		if t.Token.AccountID != "" {
			clone.Header.Set("chatgpt-account-id", t.Token.AccountID)
		}
		clone.Body, clone.ContentLength = codexRequestBody(clone.Body, clone.ContentLength)
	case "api.openai.com", "auth.openai.com":
		// 官方 hosts, no 适配 needed.
	default:
		// 一个 custom base URL must never receive ChatGPT credentials:
		// token would leak 到 third party.
		clone.Header.Del("Authorization")
		clone.Header.Del("chatgpt-account-id")
	}

	return base.RoundTrip(clone)
}

// codexRequestBody strips 请求 fields Codex backend rejects.
// max_output_tokens 是 Responses API 字段 backend refuses; crush
// derives it 从 模型 catalog, 所以 it 是 present 在 every 调用 和
// must 是 removed. Bodies 那 是 不 JSON pass through unchanged.
func codexRequestBody(body io.ReadCloser, length int64) (io.ReadCloser, int64) {
	if body == nil || body == http.NoBody || length == 0 {
		return body, length
	}

	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		return io.NopCloser(bytes.NewReader(data)), int64(len(data))
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return io.NopCloser(bytes.NewReader(data)), int64(len(data))
	}
	if _, ok := raw["max_output_tokens"]; !ok {
		return io.NopCloser(bytes.NewReader(data)), int64(len(data))
	}
	delete(raw, "max_output_tokens")

	cleaned, err := json.Marshal(raw)
	if err != nil {
		cleaned = data
	}
	return io.NopCloser(bytes.NewReader(cleaned)), int64(len(cleaned))
}
