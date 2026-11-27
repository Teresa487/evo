// Package copilot provides GitHub Copilot integration.
package copilot

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/charmbracelet/crush/internal/log"
)

var assistantRolePattern = regexp.MustCompile(`"role"\s*:\s*"assistant"`)

// NewClient creates new HTTP 客户端 用 custom transport 那 adds
// X-Initiator header based 在 消息 history 在 请求 body.
func NewClient(isSubAgent, debug bool) *http.Client {
	return &http.Client{
		Transport: &initiatorTransport{debug: debug, isSubAgent: isSubAgent},
	}
}

type initiatorTransport struct {
	debug      bool
	isSubAgent bool
}

func (t *initiatorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	const (
		xInitiatorHeader = "X-Initiator"
		userInitiator    = "user"
		agentInitiator   = "agent"
	)

	if req == nil {
		return nil, fmt.Errorf("HTTP request is nil")
	}
	if req.Body == nil || req.Body == http.NoBody {
		// No body 到 inspect; 默认 到 用户. 一个 nil Body 是 valid 为
		// bodyless requests (e.g. GET), 和 是 distinct 从 http.NoBody,
		// 所以 both must 是 handled before reading below.
		req.Header.Set(xInitiatorHeader, userInitiator)
		slog.Debug("Setting X-Initiator header to user (no request body)")
		return t.roundTrip(req)
	}

	// Clone 请求 到 avoid modifying original.
	req = req.Clone(req.Context())

	// 读取 original body into bytes 所以 we can examine it.
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	defer req.Body.Close()

	// Restore original 正文 using preserved bytes.
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Check 为 助手 messages using regex 到 handle whitespace
	// variations 在 JSON while avoiding full unmarshalling overhead.
	initiator := userInitiator
	if assistantRolePattern.Match(bodyBytes) || t.isSubAgent {
		slog.Debug("Setting X-Initiator header to agent (found assistant messages in history)")
		initiator = agentInitiator
	} else {
		slog.Debug("Setting X-Initiator header to user (no assistant messages)")
	}
	req.Header.Set(xInitiatorHeader, initiator)

	return t.roundTrip(req)
}

func (t *initiatorTransport) roundTrip(req *http.Request) (*http.Response, error) {
	if t.debug {
		return log.NewHTTPClient().Transport.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}
