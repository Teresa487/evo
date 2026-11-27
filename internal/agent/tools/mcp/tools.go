// tools.go —— MCP 工具的发现与适配成 agent 工具。

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Tool = mcp.Tool

// ToolResult represents 结果 的 running MCP 工具.
type ToolResult struct {
	Type      string
	Content   string
	Data      []byte
	MediaType string
}

var allTools = csync.NewMap[string, []*Tool]()

// Tools returns all available MCP tools.
func Tools() iter.Seq2[string, []*Tool] {
	return allTools.Seq2()
}

// GetTools returns 工具 list 为 single MCP 服务端 通过 name, 或
// ok=false 如果 服务端 是 不 registered.
func GetTools(name string) ([]*Tool, bool) {
	return allTools.Get(name)
}

// RunTool runs MCP 工具 用 given input parameters.
func RunTool(ctx context.Context, cfg *config.ConfigStore, name, toolName string, input string) (ToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return ToolResult{}, fmt.Errorf("error parsing parameters: %s", err)
	}

	c, err := getOrRenewClient(ctx, cfg, name)
	if err != nil {
		return ToolResult{}, err
	}
	result, err := c.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return ToolResult{}, err
	}

	tr := toolResultFromCall(result)
	// cap bounds what 服务端 may place 在 conversation, 所以 it 是
	// applied here rather than inside the fold: translating a reply's content
	// blocks stays pure function, 和 只 这 路径 knows data
	// 目录 那 oversized 文本 spills 到.
	tr.Content = capResult(tr.Content, cfg.Config().Options.DataDirectory)
	return tr, nil
}

// toolResultFromCall folds content blocks 的 MCP reply into one
// 结果 Crush carries, keeping 在 most one 图片 和 one 音频 payload.
func toolResultFromCall(result *mcp.CallToolResult) ToolResult {
	if len(result.Content) == 0 {
		return ToolResult{Type: "text", Content: ""}
	}

	var textParts []string
	var image, audio mediaPayload
	for _, v := range result.Content {
		switch content := v.(type) {
		case *mcp.TextContent:
			textParts = append(textParts, content.Text)
		case *mcp.ImageContent:
			image.offer(content.Data, content.MIMEType)
		case *mcp.AudioContent:
			audio.offer(content.Data, content.MIMEType)
		default:
			textParts = append(textParts, fmt.Sprintf("%v", v))
		}
	}
	textContent := strings.Join(textParts, "\n")

	switch {
	case image.seen && (len(image.data) > 0 || !audio.seen):
		return ToolResult{Type: "image", Content: textContent, Data: image.data, MediaType: image.mimeType}
	case audio.seen:
		return ToolResult{Type: "media", Content: textContent, Data: audio.data, MediaType: audio.mimeType}
	default:
		return ToolResult{Type: "text", Content: textContent}
	}
}

// mediaPayload keeps first non-empty payload offered, 或 first one
// 如果 they 是 all empty, decoded 到 raw bytes.
type mediaPayload struct {
	data     []byte
	mimeType string
	seen     bool
}

func (m *mediaPayload) offer(data []byte, mimeType string) {
	if m.seen && len(m.data) > 0 {
		return
	}
	*m = mediaPayload{data: ensureRawBytes(data), mimeType: mimeType, seen: true}
}

// RefreshTools gets updated list 的 tools 从 MCP 和 updates
// global state.
func RefreshTools(ctx context.Context, cfg *config.ConfigStore, name string) {
	// Serialize 用 会话 renewal 所以 registered 会话 can't 是
	// swapped between Get 和 state 更新 below — stale 错误
	// transition would otherwise tear down the healthy replacement.
	mu := renewLock(name)
	mu.Lock()
	defer mu.Unlock()

	session, ok := sessions.Get(name)
	if !ok {
		slog.Warn("Refresh tools: no session", "name", name)
		return
	}

	tools, err := getTools(ctx, session)
	if err != nil {
		updateState(name, StateError, err, session, Counts{})
		return
	}

	toolCount := updateTools(cfg, name, tools)

	prev, _ := states.Get(name)
	prev.Counts.Tools = toolCount
	updateState(name, StateConnected, nil, session, prev.Counts)
}

// registerSessionTools lists tools live 会话 exposes 和 writes them
// into the shared registry, returning the number registered after any
// configured allow/deny filtering. 它 是 single seam through which
// (re)connected 会话's tools enter registry, 所以 both initial
// connect 和 lazy renew repopulate 工具 list agent sends 到 LLM
// instead 的 leaving it empty.
func registerSessionTools(ctx context.Context, cfg *config.ConfigStore, name string, sess *ClientSession) (int, error) {
	tools, err := getTools(ctx, sess)
	if err != nil {
		return 0, err
	}
	return updateTools(cfg, name, tools), nil
}

func getTools(ctx context.Context, session *ClientSession) ([]*Tool, error) {
	// Always 调用 ListTools 到 get actual available tools.
	// InitializeResult Capabilities.Tools 字段 may 是 empty object {},
	// which 是 valid per MCP spec, 但 we still need 到 调用 ListTools 到 discover tools.
	result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func updateTools(cfg *config.ConfigStore, name string, tools []*Tool) int {
	mcpCfg, ok := cfg.Config().MCP[name]
	if ok {
		tools = filterTools(mcpCfg, tools)
	}
	if len(tools) == 0 {
		allTools.Del(name)
		return 0
	}
	allTools.Set(name, tools)
	return len(tools)
}

// filterTools filters tools based 在 enabled_tools (allow list) 和
// disabled_tools (deny list) 从 MCP 配置.
func filterTools(mcpCfg config.MCPConfig, tools []*Tool) []*Tool {
	if len(mcpCfg.EnabledTools) > 0 {
		filtered := make([]*Tool, 0, len(mcpCfg.EnabledTools))
		for _, tool := range tools {
			if slices.Contains(mcpCfg.EnabledTools, tool.Name) {
				filtered = append(filtered, tool)
			}
		}
		tools = filtered
	}

	if len(mcpCfg.DisabledTools) > 0 {
		filtered := make([]*Tool, 0, len(tools))
		for _, tool := range tools {
			if !slices.Contains(mcpCfg.DisabledTools, tool.Name) {
				filtered = append(filtered, tool)
			}
		}
		tools = filtered
	}

	return tools
}

// ensureRawBytes normalizes MCP media data into raw 二进制 bytes.
//
// The MCP Go SDK's json.Unmarshal normally base64-decodes
// ImageContent.Data into raw bytes automatically. However, some MCP
// transports (notably Docker over stdio) can 投递 data 在
// unexpected formats. 这 function handles both cases:
//
// - If data looks like valid base64 string (ASCII-只, decodable)
// it 是 decoded 和 raw bytes 是 returned.
// - If data 是 already raw 二进制 (contains bytes > 127) it 是
// returned 作为-是.
func ensureRawBytes(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	normalized := normalizeBase64Input(data)
	if decoded, ok := decodeBase64(normalized); ok {
		return decoded
	}

	// Already raw 二进制 — return unchanged.
	return data
}

func normalizeBase64Input(data []byte) []byte {
	normalized := strings.Join(strings.Fields(string(data)), "")
	return []byte(normalized)
}

func decodeBase64(data []byte) ([]byte, bool) {
	if len(data) == 0 {
		return data, true
	}

	for _, b := range data {
		if b > 127 {
			return nil, false
		}
	}

	s := string(data)
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return decoded, true
	}
	decoded, err = base64.RawStdEncoding.DecodeString(s)
	if err == nil {
		return decoded, true
	}
	return nil, false
}

// capResult bounds 文本 服务端 是 allowed 到 place 在 conversation.
//
// MCP servers 是 third-party, 和 nothing about their output size 是 ours 到
// assume: one 结果 can 是 hundreds 的 kilobytes, 和 because 工具 results 是
// never rewritten it then rides along 在 every later 请求. full 文本 是
// spilled into data 目录 用 指针 left 在 它的 place, which 是
// same bargain command output already strikes, 所以 data 是 deferred
// rather than destroyed.
func capResult(text, spillDir string) string {
	if text == "" {
		return text
	}
	return shell.TruncateForPersist(text, spillDir)
}
