// requests.go —— 协议里的请求体定义（各端点的入参）。

package proto

import (
	"encoding/json"
	"fmt"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/oauth"
)

// ConfigSetRequest represents 请求 到 set 配置 字段.
type ConfigSetRequest struct {
	Scope config.Scope `json:"scope"`
	Key   string       `json:"key"`
	Value any          `json:"value"`
}

// ConfigRemoveRequest represents 请求 到 remove 配置 字段.
type ConfigRemoveRequest struct {
	Scope config.Scope `json:"scope"`
	Key   string       `json:"key"`
}

// ConfigModelRequest represents 请求 到 更新 preferred 模型.
type ConfigModelRequest struct {
	Scope     config.Scope             `json:"scope"`
	ModelType config.SelectedModelType `json:"model_type"`
	Model     config.SelectedModel     `json:"model"`
}

// ConfigCompactRequest represents 请求 到 set compact mode.
type ConfigCompactRequest struct {
	Scope   config.Scope `json:"scope"`
	Enabled bool         `json:"enabled"`
}

// APIKeyKind discriminates kind 的 credential carried 在
// ConfigProviderKeyRequest. JSON's `any` loses Go type information, 所以
// wire format names kind explicitly 和 服务端 decodes
// APIKey accordingly.
type APIKeyKind string

const (
	// APIKeyKindString 是 plain string API key.
	APIKeyKindString APIKeyKind = "string"
	// APIKeyKindOAuth 是 oauth.Token credential.
	APIKeyKindOAuth APIKeyKind = "oauth"
)

// ConfigProviderKeyRequest represents 请求 到 set 提供方 API
// key. APIKey 是 raw JSON 为 credential; Kind selects
// concrete Go type APIKey should 是 decoded into via DecodeAPIKey.
type ConfigProviderKeyRequest struct {
	Scope      config.Scope    `json:"scope"`
	ProviderID string          `json:"provider_id"`
	Kind       APIKeyKind      `json:"kind"`
	APIKey     json.RawMessage `json:"api_key"`
}

// DecodeAPIKey decodes APIKey into Go type indicated 通过 Kind. 它
// returns string 为 APIKeyKindString 和 *oauth.Token 为
// APIKeyKindOAuth. An unknown kind 或 malformed payload 是 reported
// 作为 错误.
func (r ConfigProviderKeyRequest) DecodeAPIKey() (any, error) {
	switch r.Kind {
	case APIKeyKindString:
		var s string
		if err := json.Unmarshal(r.APIKey, &s); err != nil {
			return nil, fmt.Errorf("decode api key string: %w", err)
		}
		return s, nil
	case APIKeyKindOAuth:
		var tok oauth.Token
		if err := json.Unmarshal(r.APIKey, &tok); err != nil {
			return nil, fmt.Errorf("decode api key oauth token: %w", err)
		}
		return &tok, nil
	default:
		return nil, fmt.Errorf("unsupported api key kind %q", r.Kind)
	}
}

// ConfigRefreshOAuthRequest represents 请求 到 refresh OAuth token.
type ConfigRefreshOAuthRequest struct {
	Scope      config.Scope `json:"scope"`
	ProviderID string       `json:"provider_id"`
}

// ImportCopilotResponse represents 响应 从 importing Copilot credentials.
type ImportCopilotResponse struct {
	Token   any  `json:"token"`
	Success bool `json:"success"`
}

// ProjectNeedsInitResponse represents whether a project needs initialization.
type ProjectNeedsInitResponse struct {
	NeedsInit bool `json:"needs_init"`
}

// ProjectInitPromptResponse represents project initialization 提示词.
type ProjectInitPromptResponse struct {
	Prompt string `json:"prompt"`
}

// GitBranchResponse carries current Git branch 的 工作区's
// working 目录. Branch 是 empty 当 目录 是 不 Git
// repository 或 HEAD 是 detached.
type GitBranchResponse struct {
	Branch string `json:"branch"`
}

// AgentInitRequest represents 请求 到 initialize agent.
type AgentInitRequest struct {
	Interactive bool `json:"interactive"`
}

// AgentSetMainRequest requests switching 工作区's 活跃 agent
// (e.g. "coder" 或 "plan").
type AgentSetMainRequest struct {
	AgentID string `json:"agent_id"`
}

// LSPStartRequest represents 请求 到 start LSP 为 路径.
type LSPStartRequest struct {
	Path string `json:"path"`
}

// FileTrackerReadRequest represents 请求 到 record 文件 读取.
type FileTrackerReadRequest struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
}

// MCPNameRequest represents 请求 targeting named MCP 服务端.
type MCPNameRequest struct {
	Name string `json:"name"`
}

// MCPSetServerDisabledRequest toggles repository-scoped MCP 服务端
// override 为 工作区's Toggle MCPs dialog.
type MCPSetServerDisabledRequest struct {
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
}

// SkillSetDisabledRequest toggles a skill override: a repository-scoped
// disable, 或 配置's options.disabled_skills flag.
type SkillSetDisabledRequest struct {
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
}

// MCPPendingAuthServer describes MCP 服务端 awaiting OAuth
// authentication, returned 到 clients 所以 they can 提示词 用户.
type MCPPendingAuthServer struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// MCPAuthResponse describes 结果 的 MCP authentication 请求.
type MCPAuthResponse struct {
	// AuthURL 是 OAuth authorization URL 用户 must visit, 当
	// flow 是 still 在 progress.
	AuthURL string `json:"auth_url,omitempty"`
}

// MCPReadResourceRequest represents 请求 到 读取 MCP resource.
type MCPReadResourceRequest struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

// MCPGetPromptRequest represents 请求 到 get MCP 提示词.
type MCPGetPromptRequest struct {
	ClientID string            `json:"client_id"`
	PromptID string            `json:"prompt_id"`
	Args     map[string]string `json:"args"`
}

// MCPGetPromptResponse represents 响应 从 getting MCP 提示词.
type MCPGetPromptResponse struct {
	Prompt string `json:"prompt"`
}
