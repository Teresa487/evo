package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// injectTransport wraps transport 和 hands established 连接 back 到
// 测试, 所以 测试 can push raw 服务端-initiated 通知 onto it (
// go-sdk 服务端 has no public API 为 custom 通知 method).
type injectTransport struct {
	inner  mcp.Transport
	connCh chan mcp.Connection
}

func (t *injectTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	c, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.connCh <- c
	return c, nil
}

// TestChannelEndToEnd exercises whole 客户端-side 渠道 路径 against
// real go-sdk 服务端 over 在-memory transport: 服务端 那 declares
// claude/渠道 能力 和 reply 工具, 客户端 wrapped 用 渠道
// interceptor, real 能力 detection + gate opt-在 (作为 createSession does),
// two-way reply-工具 discovery, 和 real 服务端-pushed 通知 flowing
// through 传输 wrapper into EventChannelMessage.
func TestChannelEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sub := broker.Subscribe(ctx)

	serverT, clientT := mcp.NewInMemoryTransports()

	server := mcp.NewServer(
		&mcp.Implementation{Name: "chan", Version: "0.0.1"},
		&mcp.ServerOptions{
			Instructions: "reply with the reply tool",
			Capabilities: &mcp.ServerCapabilities{
				Experimental: map[string]any{channelCapability: map[string]any{}},
			},
		},
	)
	type replyIn struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	mcp.AddTool(
		server,
		&mcp.Tool{Name: "reply", Description: "send a message back over this channel"},
		func(context.Context, *mcp.CallToolRequest, replyIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "sent"}}}, nil, nil
		},
	)

	inject := &injectTransport{inner: serverT, connCh: make(chan mcp.Connection, 1)}
	serverSession, err := server.Connect(ctx, inject, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Close()

	gate := newChannelGate()
	client := mcp.NewClient(&mcp.Implementation{Name: "crush", Version: "test"}, nil)
	session, err := client.Connect(ctx, &channelTransport{inner: clientT, name: "chan", gate: gate}, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	// 能力 detection + opt-在 gate flip, mirroring createSession.
	if !hasChannelCapability(session.InitializeResult()) {
		t.Fatal("expected claude/channel capability to be detected from the handshake")
	}
	if ChannelEnabled([]string{"chan"}, "chan") && hasChannelCapability(session.InitializeResult()) {
		buffered := gate.resolve(true)
		for _, raw := range buffered {
			publishChannelMessage(ctx, "chan", raw)
		}
	} else {
		gate.resolve(false)
	}

	// Two-way reply: reply 工具 是 exposed 作为 ordinary MCP 工具.
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	hasReply := false
	for _, tl := range tools.Tools {
		if tl.Name == "reply" {
			hasReply = true
		}
	}
	if !hasReply {
		t.Error("expected the reply tool to be exposed to the agent")
	}

	// Push real 渠道 通知 服务端 -> 客户端 through transport.
	serverConn := <-inject.connCh
	params, err := json.Marshal(channelParams{
		Content: "build failed",
		Meta:    map[string]string{"severity": "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := serverConn.Write(ctx, &jsonrpc.Request{
		Method: channelNotificationMethod,
		Params: params,
	}); err != nil {
		t.Fatalf("write notification: %v", err)
	}

	select {
	case ev := <-sub:
		if ev.Payload.Type != EventChannelMessage {
			t.Fatalf("event type = %v, want EventChannelMessage", ev.Payload.Type)
		}
		msg := ev.Payload.ChannelMessage
		if !strings.Contains(msg, `source="chan"`) ||
			!strings.Contains(msg, "build failed") ||
			!strings.Contains(msg, `severity="high"`) {
			t.Errorf("unexpected channel message: %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the channel event")
	}
}
