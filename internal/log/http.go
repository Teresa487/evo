package log

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// NewHTTPClient creates HTTP 客户端 用 debug logging enabled 当 debug mode 是 在.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &HTTPRoundTripLogger{
			Transport: http.DefaultTransport,
		},
	}
}

// HTTPRoundTripLogger 是 http.RoundTripper 那 logs requests 和 responses.
type HTTPRoundTripLogger struct {
	Transport http.RoundTripper
}

// RoundTrip implements http.RoundTripper interface 用 logging.
func (h *HTTPRoundTripLogger) RoundTrip(req *http.Request) (*http.Response, error) {
	var err error
	var save io.ReadCloser
	save, req.Body, err = drainBody(req.Body)
	if err != nil {
		slog.Error(
			"HTTP request failed",
			"method", req.Method,
			"url", req.URL,
			"error", err,
		)
		return nil, err
	}

	if slog.Default().Enabled(req.Context(), slog.LevelDebug) {
		slog.Debug(
			"HTTP Request",
			"method", req.Method,
			"url", req.URL,
			"body", bodyToString(save),
		)
	}

	start := time.Now()
	resp, err := h.Transport.RoundTrip(req)
	duration := time.Since(start)
	if err != nil {
		slog.Error(
			"HTTP request failed",
			"method", req.Method,
			"url", req.URL,
			"duration_ms", duration.Milliseconds(),
			"error", err,
		)
		return resp, err
	}

	if !slog.Default().Enabled(req.Context(), slog.LevelDebug) {
		return resp, nil
	}

	// Recorded 作为 body 是 读取, 不 drained up front: draining waits 为
	// EOF, which 在 流 只 arrives 当 turn ends.
	logResponse := func(body string, truncated bool) {
		slog.Debug(
			"HTTP Response",
			"status_code", resp.StatusCode,
			"status", resp.Status,
			"headers", formatHeaders(resp.Header),
			"body", body,
			"body_truncated", truncated,
			"content_length", resp.ContentLength,
			"headers_ms", duration.Milliseconds(),
			"body_ms", time.Since(start).Milliseconds(),
		)
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		logResponse("", false)
		return resp, nil
	}
	resp.Body = &recordingBody{inner: resp.Body, log: logResponse}
	return resp, nil
}

// maxRecordedBodyBytes caps how much 的 logged body 是 held 在 memory.
// useful 片段 的 stall 是 last events before it, 所以 tail 是 kept.
const maxRecordedBodyBytes = 1 << 20

// recordingBody keeps tail 的 what 是 读取 through 响应 body 和
// logs it once body ends. Reads pass straight through, 所以 streams 流.
//
// 一个 body may 是 closed 从 one goroutine while another 是 blocked 在 读取,
// which 是 how 流 gets aborted, 所以 缓冲 是 guarded.
type recordingBody struct {
	inner     io.ReadCloser
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
	logged    bool
	log       func(body string, truncated bool)
}

func (r *recordingBody) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 {
		r.record(p[:n])
	}
	if err != nil {
		r.flush()
	}
	return n, err
}

func (r *recordingBody) Close() error {
	r.flush()
	return r.inner.Close()
}

// record appends 到 tail 缓冲. head 是 discarded 只 once
// 缓冲 reaches twice cap, 所以 long 流 pays one discard per cap's
// worth 的 bytes rather than one per 读取.
func (r *recordingBody) record(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf.Write(p)
	if r.buf.Len() > 2*maxRecordedBodyBytes {
		r.dropHead()
	}
}

// dropHead discards everything 但 last maxRecordedBodyBytes. Callers
// hold mu.
func (r *recordingBody) dropHead() {
	r.truncated = true
	r.buf.Next(r.buf.Len() - maxRecordedBodyBytes)
}

// 刷盘 writes log line once, whether body ended 在 它的 own 或 was
// closed early.
func (r *recordingBody) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.logged {
		return
	}
	r.logged = true
	if r.buf.Len() > maxRecordedBodyBytes {
		r.dropHead()
	}
	r.log(prettyJSON(r.buf.Bytes()), r.truncated)
}

func bodyToString(body io.ReadCloser) string {
	if body == nil {
		return ""
	}
	src, err := io.ReadAll(body)
	if err != nil {
		slog.Error("Failed to read body", "error", err)
		return ""
	}
	return prettyJSON(src)
}

// prettyJSON indents src 当 it 是 JSON 和 returns it unchanged otherwise.
func prettyJSON(src []byte) string {
	var b bytes.Buffer
	if json.Indent(&b, bytes.TrimSpace(src), "", "  ") != nil {
		return string(src)
	}
	return b.String()
}

// formatHeaders formats HTTP headers 为 logging, filtering out sensitive information.
func formatHeaders(headers http.Header) map[string][]string {
	filtered := make(map[string][]string)
	for key, values := range headers {
		lowerKey := strings.ToLower(key)
		// Filter out sensitive headers
		if strings.Contains(lowerKey, "authorization") ||
			strings.Contains(lowerKey, "api-key") ||
			strings.Contains(lowerKey, "token") ||
			strings.Contains(lowerKey, "secret") {
			filtered[key] = []string{"[REDACTED]"}
		} else {
			filtered[key] = values
		}
	}
	return filtered
}

func drainBody(b io.ReadCloser) (r1, r2 io.ReadCloser, err error) {
	if b == nil || b == http.NoBody {
		return http.NoBody, http.NoBody, nil
	}
	var buf bytes.Buffer
	if _, err = buf.ReadFrom(b); err != nil {
		return nil, b, err
	}
	if err = b.Close(); err != nil {
		return nil, b, err
	}
	return io.NopCloser(&buf), io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}
