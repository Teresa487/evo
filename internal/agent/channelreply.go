package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
)

// channelReplyTimeout bounds auto-reply 工具调用 所以 wedged MCP
// 服务端 cannot hold finished turn (和 会话's busy state)
// 无限期保持打开。
const channelReplyTimeout = 30 * time.Second

// 默认 meta attributes identifying reply target 为 each route.
// They match 渠道 contract used 通过 messaging servers (e.g. Signal
// MCP): "sender" carries author 的 direct push, "group" 是 present
// 只 在 group pushes.
const (
	defaultUserTargetMeta  = "sender"
	defaultGroupTargetMeta = "group"
	defaultMessageParam    = "message"
)

// targetMetaParams maps 渠道 meta attributes 到 工具 parameter
// names 那 typically carry reply target. During auto-discovery,
// each candidate 工具's InputSchema 是 checked against these 在 priority
// 顺序。
var targetMetaParams = map[string][]string{
	"sender": {"user_id", "to", "recipient", "target", "phone", "number"},
	"group":  {"group_id", "room", "channel", "conversation_id"},
}

// parseChannelMeta extracts attributes 的 <渠道> element
// 渠道-originated turn was started 用. 提示词 的 such turn 是
// exactly element rendered 通过 MCP layer (renderChannel), 所以 这
// 是 inverse 的 那 rendering. Returns ok=false 当 提示词 does
// 不 start 用 <渠道> element.
func parseChannelMeta(prompt string) (map[string]string, bool) {
	dec := xml.NewDecoder(strings.NewReader(prompt))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		switch t := tok.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return nil, false
			}
		case xml.StartElement:
			if t.Name.Local != "channel" {
				return nil, false
			}
			meta := make(map[string]string, len(t.Attr))
			for _, attr := range t.Attr {
				meta[attr.Name.Local] = attr.Value
			}
			return meta, true
		default:
			return nil, false
		}
	}
}

// resolveChannelReply picks 工具调用 那 delivers reply 为 push
// 用 given meta. Group routes win over 用户 routes 当 push
// carries group target attribute, 所以 消息 received 在 group 是
// answered 在 那 group rather than 作为 DM 到 它的 author.
func resolveChannelReply(reply *config.MCPChannelReply, meta map[string]string, text string) (tool string, args map[string]any, ok bool) {
	msgParam := cmp.Or(reply.MessageParam, defaultMessageParam)
	route := func(r *config.MCPChannelReplyRoute, defaultMeta string) (string, map[string]any, bool) {
		if r == nil || r.Tool == "" || r.TargetParam == "" {
			return "", nil, false
		}
		target := meta[cmp.Or(r.TargetMeta, defaultMeta)]
		if target == "" {
			return "", nil, false
		}
		return r.Tool, map[string]any{r.TargetParam: target, msgParam: text}, true
	}
	if tool, args, ok := route(reply.Group, defaultGroupTargetMeta); ok {
		return tool, args, ok
	}
	return route(reply.User, defaultUserTargetMeta)
}

// channelReplyDelivered reports whether 模型 already delivered reply
// through 渠道 itself during turn: completedTools holds full
// names (mcp_<服务端>_<工具>) 的 工具 calls 那 finished without 错误, 和
// 调用 到 either route 工具 或 any configured suppress 工具 counts.
func channelReplyDelivered(reply *config.MCPChannelReply, channel string, completedTools map[string]struct{}) bool {
	names := make([]string, 0, 2+len(reply.SuppressTools))
	if reply.User != nil {
		names = append(names, reply.User.Tool)
	}
	if reply.Group != nil {
		names = append(names, reply.Group.Tool)
	}
	names = append(names, reply.SuppressTools...)
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := completedTools[fmt.Sprintf("mcp_%s_%s", channel, name)]; ok {
			return true
		}
	}
	return false
}

// channelErrorReplyWanted 判断失败的轮次是否应当通知
// 渠道 sender 那 something went wrong. Only 渠道-originated turns
// qualify, 和 cancellation 是 excluded: it 是 operator stopping
// turn (Esc, 或 CancelAll 在 shutdown), 不 failure, 和 answering it
// would 消息 every 在-flight sender each time Crush quits.
func channelErrorReplyWanted(channel string, err error) bool {
	return channel != "" && !errors.Is(err, context.Canceled)
}

// discoverChannelReply scans 工具 list 的 MCP 服务端 named 渠道
// 和 attempts 到 find tools 那 can serve 作为 reply routes. 它 looks 为
// tools 那:
// - 受理 "消息" (string) parameter
// - AND 受理 target parameter 那 matches 渠道 meta attribute
// (e.g. "user_id" 为 "sender", "group_id" 为 "group")
//
// Returns nil 当 no suitable 工具 是 found, which 是 no-op fallback
// 那 preserves current behaviour 当 服务端 has no obvious
// 具备发送能力的工具。
func discoverChannelReply(channel string) *config.MCPChannelReply {
	tools, ok := mcp.GetTools(channel)
	if !ok {
		return nil
	}
	return discoverReplyFromTools(tools)
}

// discoverReplyFromTools scans slice 的 MCP tools 和 builds reply
// 配置 从 any 那 look like 消息-sending tools. 它 是 extracted
// 所以 tests can 调用 it without setting up global MCP state.
func discoverReplyFromTools(tools []*mcp.Tool) *config.MCPChannelReply {
	const msgParam = "message"
	var userTool, groupTool string
	var userTargetParam, groupTargetParam string

	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			continue
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		// Check 为 "消息" parameter (string type).
		msgProp, hasMsg := props[msgParam]
		if !hasMsg {
			continue
		}
		if msgMap, ok := msgProp.(map[string]any); !ok || msgMap["type"] != "string" {
			continue
		}

		// Check 为 target parameter matching any known meta attribute.
		for meta, candidates := range targetMetaParams {
			for _, candidate := range candidates {
				prop, hasTarget := props[candidate]
				if !hasTarget {
					continue
				}
				if propMap, ok := prop.(map[string]any); ok && propMap["type"] == "string" {
					switch meta {
					case "sender":
						if userTool == "" {
							userTool = tool.Name
							userTargetParam = candidate
						}
					case "group":
						if groupTool == "" {
							groupTool = tool.Name
							groupTargetParam = candidate
						}
					}
				}
			}
		}
	}

	// Build reply 配置 从 what we found.
	reply := &config.MCPChannelReply{
		MessageParam: msgParam,
	}
	if userTool != "" {
		reply.User = &config.MCPChannelReplyRoute{
			Tool:        userTool,
			TargetParam: userTargetParam,
		}
	}
	if groupTool != "" {
		reply.Group = &config.MCPChannelReplyRoute{
			Tool:        groupTool,
			TargetParam: groupTargetParam,
		}
	}
	if reply.User == nil && reply.Group == nil {
		return nil
	}
	return reply
}

// autoReplyDelivered 是 channelReplyDelivered 用 nil guard, 所以
// 渠道 用 no reply routes (neither configured nor discovered) never
// counts 作为 already replied. 它 serves explicit 和 auto-discovered
// 同样路由。
func autoReplyDelivered(reply *config.MCPChannelReply, channel string, completedTools map[string]struct{}) bool {
	if reply == nil {
		return false
	}
	return channelReplyDelivered(reply, channel, completedTools)
}

// skipsPermissionPrompts reports whether 工具 calls 是 being auto-approved
// rather than put 在 front 的 用户, which 是 what makes
// auto-discovered reply route safe 到 use unasked.
func (a *sessionAgent) skipsPermissionPrompts() bool {
	return a.permissions != nil && a.permissions.PermissionMode().SkipsPrompts()
}

// channelReplyText 是 助手 文本 渠道 recipient gets back 为
// finished turn. 一个 响应 那 stopped before it was 完成 — cut
// off 通过 output token limit, refused, 或 abandoned 通过 提供方 —
// closes 用 note, because recipient sees 只 这 文本 和 would
// otherwise 读取 half answer 作为 whole one.
func channelReplyText(m *message.Message) string {
	var note string
	switch m.FinishReason() {
	case message.FinishReasonMaxTokens:
		note = "response cut off: the model reached its output token limit"
	case message.FinishReasonContentFilter:
		note = "response stopped: the provider's safety classifier refused to continue"
	case message.FinishReasonError:
		note = "the response ended before it was complete"
		if finish := m.FinishPart(); finish != nil && finish.Message != "" {
			note = finish.Message
		}
	default:
		return m.Content().String()
	}
	return strings.TrimSpace(m.Content().String()) + "\n\n(" + note + ")"
}

// sendChannelReply routes final 助手 文本 的 渠道-originated
// turn back through 渠道's configured reply 工具. 它 是 no-op 为
// local turns, channels without channel_reply 配置, empty responses,
// 和 turns where 模型 already replied 在 渠道 itself. Failures
// 是 logged 和 dropped: turn has finished 和 there 是 no caller 到
// return 错误 到.
//
// When 渠道 has no explicit channel_reply 配置, sendChannelReply
// falls back 到 auto-discovering reply tools 从 MCP 服务端's 工具
// list. 这 allows channels like Signal MCP 到 work without manual
// channel_reply 配置。
func (a *sessionAgent) sendChannelReply(ctx context.Context, call SessionAgentCall, text string, completedTools map[string]struct{}) {
	if call.Channel == "" || a.cfg == nil {
		return
	}
	mcpCfg, ok := a.cfg.Config().MCP[call.Channel]
	if !ok {
		return
	}

	// Use explicit 配置 当 available, otherwise auto-discover.
	reply := mcpCfg.ChannelReply
	autoDiscovered := false
	if reply == nil {
		reply = discoverChannelReply(call.Channel)
		if reply == nil {
			return
		}
		autoDiscovered = true
	}

	if autoReplyDelivered(reply, call.Channel, completedTools) {
		slog.Debug("Channel reply already delivered by the model", "channel", call.Channel)
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if call.channelMeta == nil {
		slog.Warn("Channel reply skipped: prompt has no channel metadata", "channel", call.Channel)
		return
	}
	tool, args, ok := resolveChannelReply(reply, call.channelMeta, text)
	if !ok {
		slog.Warn("Channel reply skipped: no reply route matches the push metadata", "channel", call.Channel)
		return
	}
	// An explicit channel_reply 配置 是 itself consent 到 use
	// 工具 为 replies. An auto-discovered route, however, was 不
	// explicitly opted into — 只 proceed 当 用户 has turned 工具
	// prompts off altogether (yolo 或 sysadmin mode), 所以 someone who has
	// declined 工具 在 normal turn does 不 get 消息 sent 在
	// 未经同意就代表他们行事。
	if autoDiscovered && !a.skipsPermissionPrompts() {
		slog.Info("Channel reply skipped: auto-discovered route requires yolo or sysadmin mode, or explicit channel_reply config",
			"channel", call.Channel, "tool", tool)
		return
	}
	input, err := json.Marshal(args)
	if err != nil {
		slog.Error("Channel reply skipped: failed to encode tool arguments", "channel", call.Channel, "tool", tool, "error", err)
		return
	}
	// Detach 从 运行 上下文: turn 是 完成, 和 取消
	// racing 这 send must 不 lose reply.
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), channelReplyTimeout)
	defer cancel()
	if _, err := mcp.RunTool(sendCtx, a.cfg, call.Channel, tool, string(input)); err != nil {
		slog.Error("Channel reply failed", "channel", call.Channel, "tool", tool, "error", err)
		return
	}
	slog.Info("Routed reply to originating channel", "channel", call.Channel, "tool", tool)
}
