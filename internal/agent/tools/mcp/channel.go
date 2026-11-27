package mcp

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 渠道 contract lets MCP 服务端 push events straight into
// 会话 作为 <渠道> element 那 模型 reads 在 它的 next turn. See
// https://code.claude.com/docs/en/channels-reference 为 authoritative
// spec. Crush plays 客户端 角色: it detects 能力 服务端
// declares, listens 为 服务端-initiated 通知, 和 injects
// (validated, escaped) payload into 活跃 会话.
const (
	// channelCapability 是 experimental 能力 key 服务端 declares
	// (capabilities.experimental["claude/渠道"] = {}) 到 become 渠道.
	// Presence 的 key 是 what turns 在 通知 listener.
	channelCapability = "claude/channel"

	// channelNotificationMethod 是 JSON-RPC 通知 渠道 服务端
	// emits 到 push 事件. go-sdk 客户端 cannot 派发 这 custom
	// method itself, 所以 it 是 intercepted 在 transport layer (see
	// channelConn).
	channelNotificationMethod = "notifications/claude/channel"

	// maxChannelContentBytes caps single 渠道 payload body. Payloads 是
	// untrusted, 服务端-initiated input landing 在 模型 上下文, 所以
	// oversized body 是 rejected outright rather than truncated.
	maxChannelContentBytes = 64 * 1024

	// maxChannelMetaEntries caps how many meta attributes a single payload may
	// carry. Extra entries 是 dropped.
	maxChannelMetaEntries = 32

	// maxChannelMetaValueBytes caps single meta attribute 值. Longer
	// values cause 条目 到 是 dropped.
	maxChannelMetaValueBytes = 1024
)

// metaKeyPattern restricts meta attribute keys 到 valid XML names: letter
// 或 underscore followed 通过 letters, digits, 和 underscores. Keys starting
// 用 digit (e.g. "1chat") 是 不 valid XML names 和 是 dropped 所以
// they cannot produce structurally altered output. Hyphens 和 other
// characters 是 also rejected, preventing forged structural attributes.
var metaKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedMetaKeys 是 attribute names 客户端 controls; 服务端 must 不
// 是 able 到 override them via meta. 这 includes XML namespace family
// (xmlns, xml) which encoding/xml would emit 作为 namespace declarations.
var reservedMetaKeys = map[string]struct{}{
	"source": {},
	"xmlns":  {},
	"xml":    {},
}

// channelParams 是 wire shape 的 notifications/claude/渠道 params.
type channelParams struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta"`
}

// parseChannelParams validates 和 sanitises raw notifications/claude/渠道
// params object. 它 fails closed: any structural problem returns ok=false 和
// caller drops 通知. On success it returns params 值 whose
// content 是 size-bounded 和 whose meta contains 只 well-formed,
// non-reserved, size-bounded entries.
func parseChannelParams(raw json.RawMessage) (channelParams, bool) {
	if len(raw) == 0 {
		return channelParams{}, false
	}

	// Reject unknown fields 所以 malformed 或 hostile payload cannot smuggle
	// unexpected structure past validation.
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var p channelParams
	if err := dec.Decode(&p); err != nil {
		return channelParams{}, false
	}

	if p.Content == "" || len(p.Content) > maxChannelContentBytes {
		return channelParams{}, false
	}

	clean := channelParams{Content: p.Content}
	if len(p.Meta) > 0 {
		clean.Meta = make(map[string]string, len(p.Meta))
		for k, v := range p.Meta {
			if len(clean.Meta) >= maxChannelMetaEntries {
				break
			}
			if !metaKeyPattern.MatchString(k) {
				continue
			}
			if _, reserved := reservedMetaKeys[k]; reserved {
				continue
			}
			if len(v) > maxChannelMetaValueBytes {
				continue
			}
			clean.Meta[k] = v
		}
	}
	return clean, true
}

// renderChannel builds safe <渠道> element injected into 会话.
// source attribute 是 set 从 (trusted) 服务端 name; (untrusted)
// body 和 meta values 是 escaped 通过 encoding/xml, 所以 content cannot break out
// 的 element 或 forge attributes. Meta keys 是 emitted 在 sorted order 所以
// output 是 deterministic.
func renderChannel(source string, p channelParams) string {
	start := xml.StartElement{
		Name: xml.Name{Local: "channel"},
		Attr: make([]xml.Attr, 0, 1+len(p.Meta)),
	}
	start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "source"}, Value: source})

	keys := make([]string, 0, len(p.Meta))
	for k := range p.Meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// k 是 already validated against metaKeyPattern.
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: k}, Value: p.Meta[k]})
	}

	var b strings.Builder
	enc := xml.NewEncoder(&b)
	// EncodeToken 和 刷盘 只 fail 在 malformed tokens 或 failing writer;
	// tokens here 是 well-formed 和 strings.Builder never errors.
	_ = enc.EncodeToken(start)
	_ = enc.EncodeToken(xml.CharData(p.Content))
	_ = enc.EncodeToken(start.End())
	_ = enc.Flush()
	return b.String()
}

// hasChannelCapability reports whether 服务端's initialize 结果 advertises
// claude/渠道 experimental 能力.
func hasChannelCapability(res *mcp.InitializeResult) bool {
	if res == nil || res.Capabilities == nil {
		return false
	}
	_, ok := res.Capabilities.Experimental[channelCapability]
	return ok
}

// ChannelEnabled reports whether given 服务端 name was opted 在 via
// --channels flag. 一个 服务端 present 在 MCP 配置 是 不 渠道 until it
// 是 explicitly enabled, matching "listed 是 不 enabled" 模型. Entries
// may 是 written 作为 "服务端:<name>" 或 作为 bare "<name>". Exported 为
// 服务端 backend, which routes 渠道 events per 工作区 和 needs
// same opt-在 semantics.
func ChannelEnabled(enabled []string, name string) bool {
	for _, e := range enabled {
		e = strings.TrimSpace(e)
		if e == name {
			return true
		}
		if strings.EqualFold(e, "server:"+name) {
			return true
		}
	}
	return false
}

// ChannelOptIn reports whether 服务端 `name` should 是 treated 作为 渠道,
// considering both enablement sources: persistently via crush.json
// (m.ChannelEnabled) 或 per-launch via --channels overrides list. Every
// site 那 gates 渠道 behaviour — 会话 creation, 会话 renewal, 和
// 消息 routing — must use 这 所以 two sources 是 always ORed together
// 和 no site drifts out 的 sync.
func ChannelOptIn(m config.MCPConfig, enabled []string, name string) bool {
	return m.ChannelEnabled || ChannelEnabled(enabled, name)
}

// publishChannelMessage validates 和 renders payload, then publishes it 作为
// EventChannelMessage via must-投递 semantics. 渠道 notifications
// represent ordered inbound messages (chat, alerts) rather than disposable UI
// updates, 所以 stalled 订阅者 must 不 permanently lose them way
// lossy 发布 would. Malformed payloads 是 dropped (fail closed).
func publishChannelMessage(ctx context.Context, name string, raw json.RawMessage) {
	p, ok := parseChannelParams(raw)
	if !ok {
		slog.Warn("Dropping malformed channel notification", "server", name)
		return
	}
	broker.PublishMustDeliver(ctx, pubsub.CreatedEvent, Event{
		Type:           EventChannelMessage,
		Name:           name,
		ChannelMessage: renderChannel(name, p),
	})
}

// channelGateState 是 lifecycle state 的 渠道 gate.
//
// gate starts 在 stateGateUndecided: notifications 那 arrive during
// MCP 能力 negotiation (between Connect 和 gate resolution) 是
// buffered, because capable, opted-在 服务端 may push events immediately
// after initialize. Once negotiation completes, gate transitions 到
// stateGateOpen (发布 + drain 缓冲) 或 stateGateClosed (discard
// 缓冲 + drop).
type channelGateState int32

const (
	stateGateUndecided channelGateState = iota
	stateGateOpen
	stateGateClosed
)

// channelGate controls whether 渠道 notifications 是 published, dropped,
// 或 buffered. During 能力 negotiation gate 是 undecided 和
// messages 是 buffered; once resolved, 缓冲 是 drained 或 discarded.
type channelGate struct {
	state   atomic.Int32 // channelGateState
	mu      sync.Mutex
	pending []json.RawMessage
}

func newChannelGate() *channelGate {
	g := &channelGate{}
	g.state.Store(int32(stateGateUndecided))
	return g
}

// isOpen reports whether gate has been resolved 到 open.
func (g *channelGate) isOpen() bool {
	return channelGateState(g.state.Load()) == stateGateOpen
}

// resolve transitions gate 从 undecided 到 它的 final state. If open,
// buffered messages 是 returned 为 caller 到 发布; 如果 closed,
// 缓冲 是 discarded. Calling resolve 在 already-resolved gate 是
// no-op.
func (g *channelGate) resolve(open bool) []json.RawMessage {
	g.mu.Lock()
	defer g.mu.Unlock()
	if channelGateState(g.state.Load()) != stateGateUndecided {
		return nil
	}
	buffered := g.pending
	g.pending = nil
	if open {
		g.state.Store(int32(stateGateOpen))
		return buffered // drain the buffer for the caller to publish
	}
	g.state.Store(int32(stateGateClosed))
	return nil // discard the buffer
}

// 受理 handles 渠道 通知 according 到 gate state.
// Returns params 如果 消息 should 是 published now, 或 nil 如果 it
// was buffered 或 dropped.
func (g *channelGate) accept(raw json.RawMessage) json.RawMessage {
	switch channelGateState(g.state.Load()) {
	case stateGateOpen:
		return raw
	case stateGateClosed:
		return nil
	default: // undecided
		g.mu.Lock()
		defer g.mu.Unlock()
		// Re-check under 锁 在 case resolve ran between load 和
		// 锁 acquisition.
		switch channelGateState(g.state.Load()) {
		case stateGateOpen:
			return raw
		case stateGateClosed:
			return nil
		}
		g.pending = append(g.pending, raw)
		return nil
	}
}

// channelTransport wraps mcp.Transport 所以 客户端 can intercept
// notifications/claude/渠道 messages. go-sdk rejects unknown JSON-RPC
// methods before any 客户端-side 处理器 或 middleware runs, 所以 只 place
// 到 observe custom 通知 是 transport's own 连接.
type channelTransport struct {
	inner mcp.Transport
	name  string
	gate  *channelGate
}

// Connect implements mcp.传输.
// unwrapTransport implements [transportWrapper].
func (t *channelTransport) unwrapTransport() mcp.Transport { return t.inner }

func (t *channelTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &channelConn{Connection: conn, name: t.name, gate: t.gate}, nil
}

// channelConn wraps mcp.连接 和 filters 渠道 notifications out 的
// 流 SDK sees, dispatching them 到 渠道 处理器 instead.
type channelConn struct {
	mcp.Connection
	name string
	gate *channelGate
}

// 读取 intercepts notifications/claude/渠道. Such messages 是 always
// removed 从 流 handed 到 SDK (which would otherwise reject
// unknown method). During 能力 negotiation (gate undecided) they 是
// buffered; once gate 是 resolved they 是 published (open) 或 dropped
// (closed, fail closed).
func (c *channelConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		msg, err := c.Connection.Read(ctx)
		if err != nil {
			return msg, err
		}
		req, ok := msg.(*jsonrpc.Request)
		if !ok || req.IsCall() || req.Method != channelNotificationMethod {
			return msg, nil
		}
		if raw := c.gate.accept(req.Params); raw != nil {
			publishChannelMessage(ctx, c.name, raw)
		}
	}
}
