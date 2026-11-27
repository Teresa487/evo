package mcp

import (
	"context"
	"iter"
	"log/slog"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Prompt = mcp.Prompt

var allPrompts = csync.NewMap[string, []*Prompt]()

// Prompts returns all available MCP prompts.
func Prompts() iter.Seq2[string, []*Prompt] {
	return allPrompts.Seq2()
}

// GetPromptMessages retrieves content 的 MCP 提示词 用 given arguments.
func GetPromptMessages(ctx context.Context, cfg *config.ConfigStore, clientName, promptName string, args map[string]string) ([]string, error) {
	c, err := getOrRenewClient(ctx, cfg, clientName)
	if err != nil {
		return nil, err
	}
	result, err := c.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      promptName,
		Arguments: args,
	})
	if err != nil {
		return nil, err
	}

	var messages []string
	for _, msg := range result.Messages {
		if msg.Role != "user" {
			continue
		}
		if textContent, ok := msg.Content.(*mcp.TextContent); ok {
			messages = append(messages, textContent.Text)
		}
	}
	return messages, nil
}

// RefreshPrompts gets updated list 的 prompts 从 MCP 和 updates
// global state.
func RefreshPrompts(ctx context.Context, name string) {
	// Serialize 用 会话 renewal 所以 registered 会话 can't 是
	// swapped between Get 和 state 更新 below — stale 错误
	// transition would otherwise tear down the healthy replacement.
	mu := renewLock(name)
	mu.Lock()
	defer mu.Unlock()

	session, ok := sessions.Get(name)
	if !ok {
		slog.Warn("Refresh prompts: no session", "name", name)
		return
	}

	prompts, err := getPrompts(ctx, session)
	if err != nil {
		updateState(name, StateError, err, session, Counts{})
		return
	}

	updatePrompts(name, prompts)

	prev, _ := states.Get(name)
	prev.Counts.Prompts = len(prompts)
	updateState(name, StateConnected, nil, session, prev.Counts)
}

func getPrompts(ctx context.Context, c *ClientSession) ([]*Prompt, error) {
	if c.InitializeResult().Capabilities.Prompts == nil {
		return nil, nil
	}
	result, err := c.ListPrompts(ctx, &mcp.ListPromptsParams{})
	if err != nil {
		return nil, err
	}
	return result.Prompts, nil
}

// updatePrompts updates global mcpPrompts 和 mcpClient2Prompts maps
func updatePrompts(mcpName string, prompts []*Prompt) {
	if len(prompts) == 0 {
		allPrompts.Del(mcpName)
		return
	}
	allPrompts.Set(mcpName, prompts)
}
