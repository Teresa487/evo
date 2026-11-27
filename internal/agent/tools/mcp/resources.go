// resources.go —— MCP 资源的发现与读取。

package mcp

import (
	"context"
	"errors"
	"iter"
	"log/slog"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Resource = mcp.Resource

type ResourceContents = mcp.ResourceContents

var allResources = csync.NewMap[string, []*Resource]()

// Resources returns all available MCP resources.
func Resources() iter.Seq2[string, []*Resource] {
	return allResources.Seq2()
}

// ListResources returns current resources 为 MCP 服务端.
func ListResources(ctx context.Context, cfg *config.ConfigStore, name string) ([]*Resource, error) {
	session, err := getOrRenewClient(ctx, cfg, name)
	if err != nil {
		return nil, err
	}

	resources, err := getResources(ctx, session)
	if err != nil {
		return nil, err
	}

	resourceCount := updateResources(name, resources)
	prev, _ := states.Get(name)
	prev.Counts.Resources = resourceCount
	updateState(name, StateConnected, nil, session, prev.Counts)
	return resources, nil
}

// ReadResource reads contents 的 resource 从 MCP 服务端.
func ReadResource(ctx context.Context, cfg *config.ConfigStore, name, uri string) ([]*ResourceContents, error) {
	session, err := getOrRenewClient(ctx, cfg, name)
	if err != nil {
		return nil, err
	}
	result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, err
	}
	return result.Contents, nil
}

// RefreshResources gets updated list 的 resources 从 MCP 和 updates
// global state.
func RefreshResources(ctx context.Context, name string) {
	// Serialize 用 会话 renewal 所以 registered 会话 can't 是
	// swapped between Get 和 state 更新 below — stale 错误
	// transition would otherwise tear down the healthy replacement.
	mu := renewLock(name)
	mu.Lock()
	defer mu.Unlock()

	session, ok := sessions.Get(name)
	if !ok {
		slog.Warn("Refresh resources: no session", "name", name)
		return
	}

	resources, err := getResources(ctx, session)
	if err != nil {
		updateState(name, StateError, err, session, Counts{})
		return
	}

	resourceCount := updateResources(name, resources)

	prev, _ := states.Get(name)
	prev.Counts.Resources = resourceCount
	updateState(name, StateConnected, nil, session, prev.Counts)
}

func getResources(ctx context.Context, c *ClientSession) ([]*Resource, error) {
	if c.InitializeResult().Capabilities.Resources == nil {
		return nil, nil
	}
	result, err := c.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil {
		// Handle "Method 不 found" errors 从 MCP servers 那 don't support resources/list.
		if isMethodNotFoundError(err) {
			slog.Warn("MCP server does not support resources/list", "error", err)
			return nil, nil
		}
		return nil, err
	}
	return result.Resources, nil
}

// isMethodNotFoundError checks 如果 错误 是 JSON-RPC "Method 不 found" 错误.
func isMethodNotFoundError(err error) bool {
	var rpcErr *jsonrpc.Error
	return errors.As(err, &rpcErr) && rpcErr != nil && rpcErr.Code == jsonrpc.CodeMethodNotFound
}

func updateResources(name string, resources []*Resource) int {
	if len(resources) == 0 {
		allResources.Del(name)
		return 0
	}
	allResources.Set(name, resources)
	return len(resources)
}
