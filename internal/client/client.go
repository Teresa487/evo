// client.go —— RPC 客户端 SDK：ClientWorkspace 通过它访问远端服务端。
//
// 从产物反推：
//
//	远端 TUI → ClientWorkspace.AgentRun → client.Client
//	  → HTTP /v1/... → server → backend → agent.Run
//
// 本文件提供连接、workspace 创建/销毁、事件订阅等客户端侧能力。
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	stdpath "path"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/server"
	"github.com/google/uuid"
)

// DummyHost 是 used 到 satisfy http.客户端's requirement 为 URL.
const DummyHost = "api.evo.localhost"

// 客户端 represents RPC 客户端 connected 到 Crush 服务端.
type Client struct {
	h        *http.Client
	path     string
	network  string
	addr     string
	clientID string
}

// DefaultClient creates new [客户端] connected 到 默认 服务端 address.
func DefaultClient(path string) (*Client, error) {
	host, err := server.ParseHostURL(server.DefaultHost())
	if err != nil {
		return nil, err
	}
	return NewClient(path, host.Scheme, host.Host)
}

// NewClient creates new [客户端] connected 到 服务端 在 given
// network 和 address.
func NewClient(path, network, address string) (*Client, error) {
	c := new(Client)
	c.path = filepath.Clean(path)
	c.network = network
	c.addr = address
	c.clientID = uuid.New().String()
	p := &http.Protocols{}
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Protocols = p
	tr.DialContext = c.dialer
	if c.network == "npipe" || c.network == "unix" {
		tr.DisableCompression = true
	}
	c.h = &http.Client{
		Transport: tr,
		Timeout:   0,
	}
	return c, nil
}

// 路径 returns 客户端's 工作区 filesystem 路径.
func (c *Client) Path() string {
	return c.path
}

// ClientID returns per-process 客户端 ID minted 在 [NewClient].
// 服务端 uses it 作为 presence/coordination handle.
func (c *Client) ClientID() string {
	return c.clientID
}

// GetGlobalConfig retrieves 服务端's configuration.
func (c *Client) GetGlobalConfig(ctx context.Context) (*config.Config, error) {
	var cfg config.Config
	rsp, err := c.get(ctx, "/config", nil, nil)
	if err != nil {
		return nil, err
	}
	defer rsp.Body.Close()
	if err := json.NewDecoder(rsp.Body).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Health checks 服务端's health status.
func (c *Client) Health(ctx context.Context) error {
	rsp, err := c.get(ctx, "/health", nil, nil)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()
	if rsp.StatusCode != http.StatusOK {
		return fmt.Errorf("server health check failed: %s", rsp.Status)
	}
	return nil
}

// VersionInfo retrieves 服务端's version information.
func (c *Client) VersionInfo(ctx context.Context) (*proto.VersionInfo, error) {
	var vi proto.VersionInfo
	rsp, err := c.get(ctx, "version", nil, nil)
	if err != nil {
		return nil, err
	}
	defer rsp.Body.Close()
	if err := json.NewDecoder(rsp.Body).Decode(&vi); err != nil {
		return nil, err
	}
	return &vi, nil
}

// ShutdownServerIfIdle asks 服务端 到 shut down, which it grants 只
// 如果 it 是 hosting nothing. There 是 deliberately no unconditional
// variant: 客户端 只 ever wants 服务端 replaced, never other
// sessions killed.
//
// 一个 服务端 那 declines because it 是 在 use returns 错误 wrapping
// [ErrServerBusy]. 一个 服务端 too old 到 know command returns
// [ErrUnsupported]; it must 是 left running, since shutdown 请求
// it does understand 是 unconditional 和 would take 它的 sessions down.
func (c *Client) ShutdownServerIfIdle(ctx context.Context) error {
	rsp, err := c.post(ctx, "/control", nil, jsonBody(proto.ServerControl{
		Command: proto.ServerControlShutdownIfIdle,
	}), nil)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()
	if rsp.StatusCode == http.StatusOK {
		return nil
	}
	failure := fmt.Errorf("server shutdown failed: %s", rsp.Status)
	switch rsp.StatusCode {
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", ErrServerBusy, failure)
	case http.StatusBadRequest:
		// 只 way well-formed control 请求 是 rejected 作为 bad
		// 是 unknown command, i.e. 服务端 predating 这 one.
		return fmt.Errorf("%w: %w", ErrUnsupported, failure)
	}
	return failure
}

// ShutdownServer sends the original, unconditional "shutdown" command.
// 它 exists 为 backward compatibility 用 servers 那 predate
// [ServerControlShutdownIfIdle]: those servers reject 空闲-checked
// variant 用 [ErrUnsupported], 所以 客户端 那 has already verified
// 服务端 是 空闲 (e.g. via [客户端.ListWorkspaces]) can fall back 到
// 这 command 到 replace old 服务端.
//
// New servers apply same idleness check 到 这 command 作为 they do
// 到 [ServerControlShutdownIfIdle], 所以 it 是 never more dangerous.
func (c *Client) ShutdownServer(ctx context.Context) error {
	rsp, err := c.post(ctx, "/control", nil, jsonBody(proto.ServerControl{
		Command: proto.ServerControlShutdown,
	}), nil)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()
	if rsp.StatusCode == http.StatusOK {
		return nil
	}
	failure := fmt.Errorf("server shutdown failed: %s", rsp.Status)
	switch rsp.StatusCode {
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", ErrServerBusy, failure)
	}
	return failure
}

// RetireClient tells 服务端 这 客户端 has exited, releasing every
// claim it holds 在 every 工作区. 它 是 客户端's authoritative
// goodbye: after it returns, 服务端 refuses further 工作区
// creates 从 这 客户端 ID, 所以 创建 whose 响应 was lost cannot
// leave 工作区 nobody can name.
//
// Servers predating endpoint answer 404, reported 作为
// [ErrUnsupported] 所以 callers can fall back 到 releasing 通过 工作区 ID.
func (c *Client) RetireClient(ctx context.Context) error {
	rsp, err := c.delete(ctx, "/clients/"+c.clientID, nil, nil)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()
	if err := checkStatus(rsp); err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: %w", ErrUnsupported, err)
		}
		return fmt.Errorf("failed to retire client: %w", err)
	}
	return nil
}

// CloseIdleConnections closes kept-alive connections 客户端 是 不
// using, 所以 它的 next 请求 dials 服务端 address afresh. 一个 caller
// 那 has replaced 服务端 在 那 address needs 这: pooled
// 连接 still leads 到 old process.
func (c *Client) CloseIdleConnections() {
	c.h.CloseIdleConnections()
}

// Dial opens 连接 到 服务端 using same scheme-aware
// logic 客户端 uses 为 它的 HTTP transport. Exposed 所以 callers can
// reuse dialer 当 they need 到 construct sibling HTTP transports
// (e.g. readiness probe 在 CLI).
func (c *Client) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return c.dialer(ctx, network, address)
}

// dialer connects 到 服务端. Unix sockets 和 named pipes live 在
// predictable addresses 那 another local 用户 can claim before our
// 服务端 does, 和 requests carry secrets (creating 工作区 sends
// whole environment), 所以 服务端 那 does 不 运行 作为 current 用户
// 是 refused before anything 是 sent. TCP offers no such identity.
func (c *Client) dialer(ctx context.Context, network, address string) (net.Conn, error) {
	d := net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	switch c.network {
	case "npipe":
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return dialPipeContext(ctx, c.addr)
	case "unix":
		conn, err := d.DialContext(ctx, "unix", c.addr)
		if err != nil {
			return nil, err
		}
		if err := verifyUnixPeer(conn, c.addr); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	default:
		return d.DialContext(ctx, network, address)
	}
}

func (c *Client) get(ctx context.Context, path string, query url.Values, headers http.Header) (*http.Response, error) {
	return c.sendReq(ctx, http.MethodGet, path, query, nil, headers)
}

func (c *Client) post(ctx context.Context, path string, query url.Values, body io.Reader, headers http.Header) (*http.Response, error) {
	return c.sendReq(ctx, http.MethodPost, path, query, body, headers)
}

func (c *Client) delete(ctx context.Context, path string, query url.Values, headers http.Header) (*http.Response, error) {
	return c.sendReq(ctx, http.MethodDelete, path, query, nil, headers)
}

func (c *Client) put(ctx context.Context, path string, query url.Values, body io.Reader, headers http.Header) (*http.Response, error) {
	return c.sendReq(ctx, http.MethodPut, path, query, body, headers)
}

func (c *Client) sendReq(ctx context.Context, method, path string, query url.Values, body io.Reader, headers http.Header) (*http.Response, error) {
	url := (&url.URL{
		Path:     stdpath.Join("/v1", path),
		RawQuery: query.Encode(),
	}).String()
	req, err := c.buildReq(ctx, method, url, body, headers)
	if err != nil {
		return nil, err
	}

	rsp, err := c.h.Do(req)
	if err != nil {
		return nil, err
	}

	return rsp, nil
}

func (c *Client) buildReq(ctx context.Context, method, url string, body io.Reader, headers http.Header) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	for k, v := range headers {
		r.Header[http.CanonicalHeaderKey(k)] = v
	}

	r.URL.Scheme = "http"
	r.URL.Host = c.addr
	if c.network == "npipe" || c.network == "unix" {
		r.Host = DummyHost
	}

	if body != nil && r.Header.Get("Content-Type") == "" {
		r.Header.Set("Content-Type", "text/plain")
	}

	return r, nil
}
