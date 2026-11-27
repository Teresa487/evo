// config.go —— 配置的数据结构与类型定义。
//
// 定义 Config / ProviderConfig / SelectedModel / MCPConfig / LSPConfig /
// Options / Permissions 等，以及它们的方法（如 ToProvider、HasAPIKey）。
// 读取与合并逻辑在 load.go，热更新在 store 相关文件。

package config

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/oauth/copilot"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/invopop/jsonschema"
)

const (
	appName              = "crush"
	defaultDataDirectory = ".crush"
	defaultInitializeAs  = "AGENTS.md"
)

var defaultContextPaths = []string{
	".github/copilot-instructions.md",
	".cursorrules",
	".cursor/rules/",
	"CLAUDE.md",
	"CLAUDE.local.md",
	"GEMINI.md",
	"gemini.md",
	"crush.md",
	"crush.local.md",
	"Crush.md",
	"Crush.local.md",
	"CRUSH.md",
	"CRUSH.local.md",
	"AGENTS.md",
	"agents.md",
	"Agents.md",
}

type SelectedModelType string

// String returns string representation 的 [SelectedModelType].
func (s SelectedModelType) String() string {
	return string(s)
}

const (
	SelectedModelTypeLarge SelectedModelType = "large"
	SelectedModelTypeSmall SelectedModelType = "small"
)

const (
	AgentCoder string = "coder"
	AgentPlan  string = "plan"
	AgentTask  string = "task"
)

type SelectedModel struct {
	// 模型 id 作为 used 通过 提供方 API.
	// Required.
	Model string `json:"model" jsonschema:"required,description=The model ID as used by the provider API,example=gpt-4o"`
	// 模型 提供方, same 作为 key/id used 在 providers 配置.
	// Required.
	Provider string `json:"provider" jsonschema:"required,description=The model provider ID that matches a key in the providers config,example=openai"`

	// Only used 通过 models 那 use openai 提供方 和 need 这 set.
	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema:"description=Reasoning effort level for OpenAI models that support it,enum=low,enum=medium,enum=high"`

	// Used 通过 anthropic models 那 can reason 到 indicate 如果 模型 should think.
	Think bool `json:"think,omitempty" jsonschema:"description=Enable thinking mode for Anthropic models that support reasoning"`

	// Overrides 默认 模型 configuration.
	MaxTokens        int64    `json:"max_tokens,omitempty" jsonschema:"description=Maximum number of tokens for model responses,maximum=200000,example=4096"`
	Temperature      *float64 `json:"temperature,omitempty" jsonschema:"description=Sampling temperature,minimum=0,maximum=1,example=0.7"`
	TopP             *float64 `json:"top_p,omitempty" jsonschema:"description=Top-p (nucleus) sampling parameter,minimum=0,maximum=1,example=0.9"`
	TopK             *int64   `json:"top_k,omitempty" jsonschema:"description=Top-k sampling parameter"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty" jsonschema:"description=Frequency penalty to reduce repetition"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty" jsonschema:"description=Presence penalty to increase topic diversity"`

	// Override 提供方 specific options.
	ProviderOptions map[string]any `json:"provider_options,omitempty" jsonschema:"description=Additional provider-specific options for the model"`
}

type ProviderConfig struct {
	// 提供方's id.
	ID string `json:"id,omitempty" jsonschema:"description=Unique identifier for the provider,example=openai"`
	// 提供方's name, used 为 display purposes.
	Name string `json:"name,omitempty" jsonschema:"description=Human-readable name for the provider,example=OpenAI"`
	// 提供方's API endpoint.
	BaseURL string `json:"base_url,omitempty" jsonschema:"description=Base URL for the provider's API,format=uri,example=https://api.openai.com/v1"`
	// 提供方 type, e.g. "openai", "anthropic", etc. 如果 empty it defaults 到 openai.
	Type catwalk.Type `json:"type,omitempty" jsonschema:"description=Provider type that determines the API format,default=openai"`
	// 提供方's API key.
	APIKey string `json:"api_key,omitempty" jsonschema:"description=API key for authentication with the provider,example=$OPENAI_API_KEY"`
	// original API key template before resolution (为 re-resolution 在 auth errors).
	APIKeyTemplate string `json:"-"`
	// OAuthToken 为 providers 那 use OAuth2 authentication.
	OAuthToken *oauth.Token `json:"oauth,omitempty" jsonschema:"description=OAuth2 token for authentication with the provider"`
	// Marks 提供方 作为 disabled.
	Disable bool `json:"disable,omitempty" jsonschema:"description=Whether this provider is disabled,default=false"`

	// Custom 系统提示词 prefix.
	SystemPromptPrefix string `json:"system_prompt_prefix,omitempty" jsonschema:"description=Custom prefix to add to system prompts for this provider"`

	// Extra headers 到 send 用 each 请求 到 提供方. Values
	// 运行 through shell expansion 在 配置-load time, 所以 $VAR 和
	// $(cmd) work same way they do 在 MCP headers. 一个 header whose
	// 值 resolves 到 empty string (unset bare $VAR under
	// lenient nounset, $(echo), 或 literal "") 是 omitted 从
	// outgoing 请求 rather than sent 作为 "Header:".
	ExtraHeaders map[string]string `json:"extra_headers,omitempty" jsonschema:"description=Additional HTTP headers to send with requests"`
	// ExtraBody 是 merged verbatim into OpenAI-compatible 请求
	// bodies. String values 是 NOT shell-expanded: 这 是 plain
	// JSON passthrough 所以 那 arbitrary 提供方-extension fields
	// (numbers, nested objects, booleans) round-trip without a
	// recursive walker guessing 在 intent. If you need env-var-
	// driven 值 在 请求 time, put it 在 extra_headers, 或 在
	// 提供方's top-level api_key / base_url, all 的 which do
	// expand.
	ExtraBody map[string]any `json:"extra_body,omitempty" jsonschema:"description=Additional fields to include in request bodies\\, only works with openai-compatible providers"`

	ProviderOptions map[string]any `json:"provider_options,omitempty" jsonschema:"description=Additional provider-specific options for this provider"`

	// Used 到 pass extra parameters 到 提供方.
	ExtraParams map[string]string `json:"-"`

	// AWSAuthRefresh 是 shell command 运行 当 Bedrock returns
	// credential 错误. Output 是 discarded 到 avoid corrupting TUI.
	AWSAuthRefresh string `json:"aws_auth_refresh,omitempty" jsonschema:"description=Shell command to run when AWS credentials expire (Bedrock only)."`

	// Skip cost accumulation 为 这 提供方 当 using subscription 或 flat rate billing.
	FlatRate bool `json:"flat_rate,omitempty" jsonschema:"description=Flat-rate mode for this provider"`

	// AutoDiscoverModels controls 模型 discovery via /v1/models endpoint.
	// When Models 是 empty 和 这 是 nil 或 true, Crush auto-discovers
	// models. When true 和 Models 是 non-empty, discovered models 是
	// merged 在 (用户-specified models take precedence). When false,
	// 只 explicitly listed models 是 used.
	AutoDiscoverModels *bool `json:"discover_models,omitempty" jsonschema:"description=Auto-discover models from /v1/models endpoint. When true with existing models they are merged (yours win),default=true"`

	// 提供方 models
	Models []catwalk.Model `json:"models,omitempty" jsonschema:"description=List of models available from this provider"`

	// ChatGPTModels lists the models the ChatGPT (Codex) backend grants
	// 当 提供方 是 authenticated 用 ChatGPT account. 它 是
	// 提供方's whole catalog 在 那 case: API-key models 在
	// Models 是 不 served 通过 subscription.
	ChatGPTModels []catwalk.Model `json:"chatgpt_models,omitempty" jsonschema:"-"`

	// GrokModels lists models Grok plan grants 当 提供方
	// 是 authenticated 用 Grok account. Like ChatGPTModels, it 是
	// 提供方's whole catalog 在 那 case.
	GrokModels []catwalk.Model `json:"grok_models,omitempty" jsonschema:"-"`
}

// ToProvider converts [ProviderConfig] 到 [catwalk.提供方].
func (c *ProviderConfig) ToProvider() catwalk.Provider {
	return catwalk.Provider{
		Name:   c.Name,
		ID:     catwalk.InferenceProvider(c.ID),
		Models: c.Models,
	}
}

func (c *ProviderConfig) SetupGitHubCopilot() {
	maps.Copy(c.ExtraHeaders, copilot.Headers())
}

// HasAPIKey reports whether 提供方's api_key resolves 到 usable
// credential. 存储 值 是 often unresolved template like
// $OPENAI_API_KEY, which 是 不 credential until variable exists.
func (c *ProviderConfig) HasAPIKey(resolver VariableResolver) bool {
	if c.APIKey == "" {
		return false
	}
	v, err := resolver.ResolveValue(c.APIKey)
	return err == nil && v != ""
}

type MCPType string

const (
	MCPStdio MCPType = "stdio"
	MCPSSE   MCPType = "sse"
	MCPHttp  MCPType = "http"
)

type MCPConfig struct {
	Command       string            `json:"command,omitempty" jsonschema:"description=Command to execute for stdio MCP servers,example=npx"`
	Env           map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set for the MCP server"`
	Args          []string          `json:"args,omitempty" jsonschema:"description=Arguments to pass to the MCP server command"`
	Type          MCPType           `json:"type" jsonschema:"required,description=Type of MCP connection,enum=stdio,enum=sse,enum=http,default=stdio"`
	URL           string            `json:"url,omitempty" jsonschema:"description=URL for HTTP or SSE MCP servers,format=uri,example=http://localhost:3000/mcp"`
	Disabled      bool              `json:"disabled,omitempty" jsonschema:"description=Whether this MCP server is disabled,default=false"`
	DisabledTools []string          `json:"disabled_tools,omitempty" jsonschema:"description=List of tools from this MCP server to disable,example=get-library-doc"`
	EnabledTools  []string          `json:"enabled_tools,omitempty" jsonschema:"description=Allow list of tools from this MCP server,example=get-library-doc"`
	Timeout       int               `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for MCP server connections,default=10,example=30,example=60,example=120"`

	// Sessionless marks 服务端 那 does 不 maintain MCP 会话 (it
	// never issues Mcp-会话-Id). When true, Crush omits
	// tools/prompts/resources list-changed handlers: the go-sdk opens a
	// SEP-2575 "subscriptions/listen" 流 whenever any 的 those handlers
	// 是 set, 和 sessionless streamable-HTTP servers (e.g. GitHub MCP)
	// answer 那 POST 用 404 ("会话 不 found"), which SDK treats
	// 作为 fatal. cost 是 no live list-changed notifications 从 这
	// 服务端.
	//
	// When nil, Crush auto-detects set 的 known sessionless servers (see
	// IsSessionless); set it explicitly 到 override 那 detection.
	Sessionless *bool `json:"sessionless,omitempty" jsonschema:"description=Mark a sessionless MCP server (no Mcp-Session-Id) so Crush skips the subscriptions/listen stream it would otherwise reject. Leave unset to auto-detect known sessionless servers (e.g. GitHub MCP),default=false"`

	// Headers 是 HTTP headers 为 HTTP/SSE MCP servers. Values 运行
	// through shell expansion 在 MCP startup, 所以 $VAR 和 $(cmd)
	// work. 一个 header whose 值 resolves 到 empty string (unset
	// bare $VAR under lenient nounset, $(echo), 或 literal "") 是
	// omitted 从 outgoing 请求 rather than sent 作为
	// "请求头:".
	Headers map[string]string `json:"headers,omitempty" jsonschema:"description=HTTP headers for HTTP/SSE MCP servers"`

	// OAuth enables MCP OAuth 2.1 authorization flow 为 HTTP
	// transport servers. When true, 客户端 uses dynamic 客户端
	// registration 和 opens browser 为 用户 到 authorize.
	// Tokens 是 persisted automatically. Only supported 为 type=http.
	OAuth bool `json:"oauth,omitempty" jsonschema:"description=Enable OAuth 2.1 authorization flow for this MCP server (HTTP transport only),default=false"`

	// OAuthClientID 是 optional pre-registered OAuth 客户端 ID. Set
	// it 为 servers 那 do 不 support dynamic 客户端 registration
	// (e.g. GitHub, Slack) 和 instead issue 客户端 credentials 当 you
	// register OAuth app. Values 运行 through shell expansion, 所以
	// $VAR 和 $(cmd) work.
	OAuthClientID string `json:"oauth_client_id,omitempty" jsonschema:"description=Pre-registered OAuth client ID for servers without dynamic client registration"`

	// OAuthClientSecret 是 optional secret paired 用
	// OAuthClientID 为 confidential clients. Values 运行 through shell
	// expansion, 所以 $VAR 和 $(cmd) work.
	OAuthClientSecret string `json:"oauth_client_secret,omitempty" jsonschema:"description=Pre-registered OAuth client secret paired with oauth_client_id"`

	// OAuthCallbackPort pins localhost port used 为 OAuth
	// redirect listener. Set 这 当 OAuth 提供方 requires
	// exact-match callback URL (e.g. GitHub OAuth Apps). When omitted,
	// Crush picks first free port 从 它的 默认 range.
	OAuthCallbackPort int `json:"oauth_callback_port,omitempty" jsonschema:"description=Fixed localhost port for the OAuth callback, required by providers that enforce exact-match redirect URIs"`

	// OAuthToken 是 persisted OAuth token 为 这 服务端. 它 是
	// managed internally 和 存储 在 global data 配置.
	OAuthToken *oauth.Token `json:"oauth_token,omitempty" jsonschema:"-"`

	// ChannelEnabled enables MCP 服务端 作为 渠道 directly 从 配置,
	// equivalent 到 passing 它的 name via --channels 在 CLI. 这 lets
	// channels 是 declared persistently 在 crush.json without needing CLI
	// flag 在 every launch.
	ChannelEnabled bool `json:"channel_enabled,omitempty" jsonschema:"description=Enable this MCP server as a channel (equivalent to --channels),default=false"`

	// ChannelReply, 当 set 在 服务端 enabled 作为 渠道, makes Crush
	// route final 助手 响应 的 every turn 那 originated 从
	// 这 渠道 back through one 的 服务端's own tools, 所以 消息
	// received 在 渠道 gets reply 在 渠道 even 当
	// 模型 只 produced 终态 output.
	ChannelReply *MCPChannelReply `json:"channel_reply,omitempty" jsonschema:"description=Automatically route replies for turns originating from this channel back through one of the server's tools"`
}

// MCPChannelReply configures deterministic reply routing 为 MCP 服务端
// acting 作为 渠道: which 的 服务端's tools 投递 reply 为
// direct 和 group pushes, 和 how reply 文本 和 target 是 mapped
// onto 工具 arguments.
type MCPChannelReply struct {
	// 用户 routes replies 到 direct (person-到-person) 渠道 pushes.
	User *MCPChannelReplyRoute `json:"user,omitempty" jsonschema:"description=Reply route for direct messages"`
	// Group routes replies 到 group 渠道 pushes. 它 是 preferred over
	// 用户 当 push carries group route's meta attribute.
	Group *MCPChannelReplyRoute `json:"group,omitempty" jsonschema:"description=Reply route for group messages"`
	// MessageParam 是 工具 argument 那 receives reply 文本.
	MessageParam string `json:"message_param,omitempty" jsonschema:"description=Tool argument name that receives the reply text,default=message"`
	// SuppressTools lists additional 工具 names (beyond two route
	// tools) 那 count 作为 模型 having already replied 在 渠道
	// during turn, e.g. operator-shortcut send 工具.
	SuppressTools []string `json:"suppress_tools,omitempty" jsonschema:"description=Additional tool names that suppress the automatic reply when the model already called one of them during the turn,example=send"`
}

// MCPChannelReplyRoute maps one kind 的 inbound 渠道 push onto MCP
// 工具调用 那 delivers reply 到 it.
type MCPChannelReplyRoute struct {
	// 工具 是 MCP 工具 (bare name, without mcp_<服务端>_ prefix)
	// invoked 到 投递 reply.
	Tool string `json:"tool" jsonschema:"required,description=MCP tool name that sends the reply,example=send_message_to_user"`
	// TargetParam 是 工具 argument 那 receives reply target
	// (recipient 或 group ID).
	TargetParam string `json:"target_param" jsonschema:"required,description=Tool argument name that receives the reply target,example=user_id"`
	// TargetMeta 是 <渠道> meta attribute whose 值 identifies
	// reply target. Defaults 到 "sender" 为 用户 route 和
	// "group" 为 group route.
	TargetMeta string `json:"target_meta,omitempty" jsonschema:"description=Channel meta attribute carrying the reply target; defaults to sender (user route) or group (group route)"`
}

// isOrphanedToken reports whether 这 条目 是 leftover OAuth token
// 用 no real 服务端 配置.
func (m MCPConfig) isOrphanedToken() bool {
	return m.Type == "" && m.Command == "" && m.URL == "" && m.OAuthToken != nil
}

type LSPConfig struct {
	Disabled    bool              `json:"disabled,omitempty" jsonschema:"description=Whether this LSP server is disabled,default=false"`
	Command     string            `json:"command,omitempty" jsonschema:"description=Command to execute for the LSP server,example=gopls"`
	Args        []string          `json:"args,omitempty" jsonschema:"description=Arguments to pass to the LSP server command"`
	Env         map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set to the LSP server command"`
	FileTypes   []string          `json:"filetypes,omitempty" jsonschema:"description=File types this LSP server handles,example=go,example=mod,example=rs,example=c,example=js,example=ts"`
	RootMarkers []string          `json:"root_markers,omitempty" jsonschema:"description=Files or directories that indicate the project root,example=go.mod,example=package.json,example=Cargo.toml"`
	InitOptions map[string]any    `json:"init_options,omitempty" jsonschema:"description=Initialization options passed to the LSP server during initialize request"`
	Options     map[string]any    `json:"options,omitempty" jsonschema:"description=LSP server-specific settings passed during initialization"`
	Timeout     int               `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for LSP server initialization,default=30,example=60,example=120"`
}

type TUIOptions struct {
	CompactMode bool        `json:"compact_mode,omitempty" jsonschema:"description=Enable compact mode for the TUI interface,default=false"`
	DiffMode    string      `json:"diff_mode,omitempty" jsonschema:"description=Diff mode for the TUI interface,enum=unified,enum=split"`
	ActiveTheme string      `json:"active_theme,omitempty" jsonschema:"description=Name of the currently active theme,default=charmtone-panther,example=charmtone-panther,example=gruvbox-dark,example=rose-pine,example=keanu,example=kitt"`
	Completions Completions `json:"completions,omitzero" jsonschema:"description=Completions UI options"`
	Transparent *bool       `json:"transparent,omitempty" jsonschema:"description=Enable transparent background for the TUI interface,default=false"`
	Scrollbar   string      `json:"scrollbar,omitempty" jsonschema:"description=Chat scrollbar visibility,enum=default,enum=always,enum=never,default=default"`
	Mouse       *bool       `json:"mouse,omitempty" jsonschema:"description=Enable terminal mouse capture for selection\\, clicks\\, and scrolling in the TUI. Disable to let the terminal emulator or tmux handle text selection and copy/paste,default=true"`
	NerdFonts   *bool       `json:"nerd_fonts,omitempty" jsonschema:"description=Override Nerd Font glyph detection for the TUI: true forces glyphs on\\, false forces them off\\, and unset keeps automatic detection"`
	ExitBanner  ExitBanner  `json:"exit_banner,omitempty" jsonschema:"description=Exit banner style after quitting Crush,enum=default,enum=compact,enum=none,default=default"`
}

// IsTransparent reports whether TUI draws transparent 后台.
// nil receiver 和 unset 指针 both mean opaque, 所以 callers can ask
// without unwrapping either.
func (t *TUIOptions) IsTransparent() bool {
	return t != nil && t.Transparent != nil && *t.Transparent
}

// UnmarshalJSON tolerates legacy string form 的 "theme" 字段.
// Older Crush builds 存储 selected theme 作为 `"theme":
// "gruvbox-dark"`. When 那 string 是 encountered it 是 promoted 到
// 活跃 theme name 所以 existing configs keep loading.
func (t *TUIOptions) UnmarshalJSON(data []byte) error {
	type tuiOptionsAlias TUIOptions
	var loose struct {
		tuiOptionsAlias
		Theme json.RawMessage `json:"theme"`
	}
	if err := json.Unmarshal(data, &loose); err != nil {
		return err
	}
	*t = TUIOptions(loose.tuiOptionsAlias)
	if len(loose.Theme) == 0 || string(loose.Theme) == "null" {
		return nil
	}

	var legacyName string
	if err := json.Unmarshal(loose.Theme, &legacyName); err != nil {
		// Legacy inline theme maps 是 ignored now 那 files 是 只
		// palette source, 但 they must remain loadable during migration.
		var legacyThemes map[string]json.RawMessage
		return json.Unmarshal(loose.Theme, &legacyThemes)
	}
	if legacyName != "" && t.ActiveTheme == "" {
		t.ActiveTheme = legacyName
	}
	return nil
}

// Completions defines options 为 completions UI.
type Completions struct {
	MaxDepth *int `json:"max_depth,omitempty" jsonschema:"description=Maximum depth for the ls tool,default=0,example=10"`
	MaxItems *int `json:"max_items,omitempty" jsonschema:"description=Maximum number of items to return for the ls tool,default=1000,example=100"`
}

// Limits returns configured completion limits. Zero means 用户 has 不
// pinned 那 limit, 和 callers fall back 到 their own built-在 cap.
func (c Completions) Limits() (depth, items int) {
	return ptrValOr(c.MaxDepth, 0), ptrValOr(c.MaxItems, 0)
}

// Diff mode options.
const (
	DiffModeUnified = "unified" // Inline unified diffs
	DiffModeSplit   = "split"   // Side-by-side diffs
)

// Scrollbar visibility options.
const (
	ScrollbarDefault = "default" // Auto-hide after 2 seconds
	ScrollbarAlways  = "always"  // Always show when content exceeds viewport
	ScrollbarNever   = "never"   // Never show scrollbar
)

// ExitBanner selects what Crush prints after the TUI exits.
type ExitBanner string

const (
	// ExitBannerDefault renders full ASCII art logo 用 padding. 它 是
	// also what zero 值 和 any unrecognized 值 fall back 到.
	ExitBannerDefault ExitBanner = "default"
	// ExitBannerCompact renders 只 会话 和 resume lines, 用 no
	// logo 和 no padding. With no 活跃 会话 it renders nothing 在 all,
	// 所以 Crush exits silently.
	ExitBannerCompact ExitBanner = "compact"
	// ExitBannerNone 渲染 nothing.
	ExitBannerNone ExitBanner = "none"
)

type Permissions struct {
	AllowedTools []string `json:"allowed_tools,omitempty" jsonschema:"description=List of tools that don't require permission prompts,example=bash,example=view"`
	// BlockedCommands adds 到 built-在 list 的 commands treated 作为
	// dangerous. Dangerous commands 是 prompted 为 在 normal mode, 和
	// blocked 在 exec time 当 they turn up indirectly.
	BlockedCommands []string `json:"blocked_commands,omitempty" jsonschema:"description=Extra shell commands to treat as dangerous (prompted for in normal mode),example=kubectl,example=terraform"`
	// AllowedCommands removes entries 从 effective dangerous list,
	// including built-在 defaults.
	AllowedCommands []string `json:"allowed_commands,omitempty" jsonschema:"description=Shell commands to drop from the dangerous list so they stop prompting,example=curl,example=ssh"`
	// SafeCommands 是 exact command lines 那 skip 权限 提示词
	// entirely, e.g. "go build" 或 "cargo check". An 条目 matches 只 当
	// command 是 written exactly 作为 given, 用 no extra arguments 和
	// no flags, 所以 "go build" never covers "go build -o /usr/local/bin/x".
	//
	// 这 是 narrow form 在 purpose. built-在 list carries flag
	// policy per command, which 是 what lets `git log` through while holding
	// back `git push` 和 `git diff --output=文件`. Expressing 那 在
	// configuration would 是 small security language, 和 thing people
	// actually want 到 say 是 " exact command I 运行 fifty times day".
	SafeCommands []string `json:"safe_commands,omitempty" jsonschema:"description=Exact command lines that skip the permission prompt. Matched literally with no extra arguments,example=go build,example=cargo check"`
}

type TrailerStyle string

const (
	TrailerStyleNone         TrailerStyle = "none"
	TrailerStyleCoAuthoredBy TrailerStyle = "co-authored-by"
	TrailerStyleAssistedBy   TrailerStyle = "assisted-by"
)

type Attribution struct {
	TrailerStyle  TrailerStyle `json:"trailer_style,omitempty" jsonschema:"description=Style of attribution trailer to add to commits,enum=none,enum=co-authored-by,enum=assisted-by,default=assisted-by"`
	CoAuthoredBy  *bool        `json:"co_authored_by,omitempty" jsonschema:"description=Deprecated: use trailer_style instead"`
	GeneratedWith bool         `json:"generated_with,omitempty" jsonschema:"description=Add Generated with Crush line to commit messages and issues and PRs,default=true"`
}

// JSONSchemaExtend marks co_authored_by 字段 作为 deprecated 在 schema.
func (Attribution) JSONSchemaExtend(schema *jsonschema.Schema) {
	if schema.Properties != nil {
		if prop, ok := schema.Properties.Get("co_authored_by"); ok {
			prop.Deprecated = true
		}
	}
}

type Options struct {
	ContextPaths         []string    `json:"context_paths,omitempty" jsonschema:"description=Paths to files containing context information for the AI,example=.cursorrules,example=CRUSH.md"`
	GlobalContextPaths   []string    `json:"global_context_paths,omitempty" jsonschema:"description=Paths to files containing global context information for the AI,default=~/.config/crush/CRUSH.md,default=~/.config/AGENTS.md"`
	SkillsPaths          []string    `json:"skills_paths,omitempty" jsonschema:"description=Paths to directories containing Agent Skills (folders with SKILL.md files),example=~/.config/crush/skills,example=./skills"`
	TUI                  *TUIOptions `json:"tui,omitempty" jsonschema:"description=Terminal user interface options"`
	Debug                bool        `json:"debug,omitempty" jsonschema:"description=Enable debug logging,default=false"`
	DebugLSP             bool        `json:"debug_lsp,omitempty" jsonschema:"description=Enable debug logging for LSP servers,default=false"`
	DisableAutoSummarize bool        `json:"disable_auto_summarize,omitempty" jsonschema:"description=Disable automatic conversation summarization,default=false"`
	// DataDirectory 是 where Crush keeps per-project state such 作为
	// SQLite 数据库 和 工作区 overrides. Relative paths 是
	// resolved against working 目录; absolute paths 是 used
	// verbatim. After defaulting 存储 值 是 always absolute.
	DataDirectory             string       `json:"data_directory,omitempty" jsonschema:"description=Directory for storing application data. Relative paths are resolved against the working directory; absolute paths are used as-is.,default=.crush,example=.crush"`
	DisabledTools             []string     `json:"disabled_tools,omitempty" jsonschema:"description=List of built-in tools to disable and hide from the agent,example=bash,example=sourcegraph"`
	DisableProviderAutoUpdate bool         `json:"disable_provider_auto_update,omitempty" jsonschema:"description=Disable providers auto-update,default=false"`
	DisableDefaultProviders   bool         `json:"disable_default_providers,omitempty" jsonschema:"description=Ignore all default/embedded providers. When enabled\\, providers must be fully specified in the config file with base_url\\, models\\, and api_key - no merging with defaults occurs,default=false"`
	Attribution               *Attribution `json:"attribution,omitempty" jsonschema:"description=Attribution settings for generated content"`
	DisableMetrics            bool         `json:"disable_metrics,omitempty" jsonschema:"description=Disable sending metrics,default=false"`
	InitializeAs              string       `json:"initialize_as,omitempty" jsonschema:"description=Name of the context file to create/update during project initialization,default=AGENTS.md,example=AGENTS.md,example=CRUSH.md,example=CLAUDE.md,example=docs/LLMs.md"`
	AutoLSP                   *bool        `json:"auto_lsp,omitempty" jsonschema:"description=Automatically setup LSPs based on root markers,default=true"`
	Progress                  *bool        `json:"progress,omitempty" jsonschema:"description=Show indeterminate progress updates during long operations,default=true"`
	Notifications             string       `json:"notifications,omitempty" jsonschema:"description=Notification style to use. Options: auto (default)\\, native\\, osc\\, bell\\, disabled. Auto selects based on environment: native for local sessions\\, osc for SSH (with automatic OSC 99/777 detection).,enum=auto,enum=native,enum=osc,enum=bell,enum=disabled,default=auto"`
	DisabledSkills            []string     `json:"disabled_skills,omitempty" jsonschema:"description=List of skill names to disable and hide from the agent,example=crush-config"`
	RequestTimeout            *int         `json:"request_timeout,omitempty" jsonschema:"description=Timeout in seconds for each LLM API request. Streaming responses are aborted only after this much inactivity between tokens\\, so slow but active streams are never killed; the wait for the first token gets at least five minutes for prompt prefill. 0 disables it\\, negative values are invalid.,default=60,example=120,example=300,example=0"`
	MaxBackgroundJobs         *int         `json:"max_background_jobs,omitempty" jsonschema:"description=How many background shell jobs may run at once. Jobs that have finished never count against it. 0 or negative restores the default.,default=50,example=10,example=200"`
	// PinentryIntegrated renders GPG passphrase/PIN prompts 作为 native
	// Crush dialog (loopback pinentry) instead 的 handing 终态
	// over 到 external pinentry program. No GPG 或 git configuration
	// 是 modified: redirect 只 applies 到 processes Crush spawns.
	PinentryIntegrated   *bool                `json:"pinentry_integrated,omitempty" jsonschema:"description=Render GPG passphrase prompts as a native Crush dialog instead of an external pinentry program. Works with file-based keys and security keys and never modifies GPG or git configuration.,default=true"`
	PinentryCacheTimeout *int                 `json:"pinentry_cache_timeout,omitempty" jsonschema:"description=Seconds Crush keeps an entered GPG passphrase in memory so repeated signing operations do not re-prompt. 0 (the default) disables it; gpg-agent's own cache applies regardless.,default=0,example=300"`
	Prompts              PromptPaths          `json:"prompts,omitempty" jsonschema:"description=Files that replace Crush's built-in prompts\\, keyed by prompt name. A replacement is used verbatim\\, so anything the built-in template injected (skills\\, context files\\, LSP status) is lost unless the replacement asks for it too."`
	Experimental         *ExperimentalOptions `json:"experimental,omitempty" jsonschema:"description=Experimental features. They are unfinished and may change or be removed in any release."`
	Coreutils            CoreutilsMode        `json:"coreutils,omitempty" jsonschema:"description=How core utilities run in Crush shells.,enum=turtle,enum=system,enum=auto,enum=off,default=auto"`
}

// CoreutilsMode 是 shell package's coreutils mode, aliased 所以 configs
// 可以 hold it directly.
type CoreutilsMode = shell.CoreutilsMode

// Coreutils mode values.
const (
	CoreutilsTurtle = shell.CoreutilsTurtle
	CoreutilsSystem = shell.CoreutilsSystem
	CoreutilsAuto   = shell.CoreutilsAuto
	CoreutilsOff    = shell.CoreutilsOff
)

// DefaultCoreutils 是 coreutils mode used 当 configuration does
// 不 specify one.
const DefaultCoreutils = CoreutilsAuto

// GetCoreutils returns configured coreutils mode, 或 DefaultCoreutils
// 当 it 是 不 set. Callers pass 结果 到 shell surfaces.
func (o *Options) GetCoreutils() CoreutilsMode {
	if o == nil || o.Coreutils == "" {
		return DefaultCoreutils
	}
	return o.Coreutils
}

// ExperimentalOptions holds unfinished features. Each mirrors an environment
// variable, which wins over 配置 值 当 set.
type ExperimentalOptions struct {
	ClientServer       *bool `json:"client_server,omitempty" jsonschema:"description=Run the agent in a background server that the TUI attaches to. Overridden by CRUSH_CLIENT_SERVER.,default=false"`
	ServerReadyTimeout *int  `json:"server_ready_timeout,omitempty" jsonschema:"description=Seconds a client waits for the server to become ready. Overridden by CRUSH_SERVER_READY_TIMEOUT.,example=30"`
	ServerIdleTimeout  *int  `json:"server_idle_timeout,omitempty" jsonschema:"description=Seconds the server stays alive after its last workspace closes. 0 shuts it down at once. Overridden by CRUSH_SERVER_IDLE_TIMEOUT.,default=60"`
	ServerDetachGrace  *int  `json:"server_detach_grace,omitempty" jsonschema:"description=Seconds a workspace survives after a client's connection drops. 0 tears it down at once. Overridden by CRUSH_SERVER_DETACH_GRACE.,default=10"`
}

// DefaultMaxBackgroundJobs 是 how many background shell jobs may 运行 在 once
// 当 用户 has 不 set max_background_jobs.
const DefaultMaxBackgroundJobs = 50

// GetMaxBackgroundJobs returns the running-job ceiling, always positive.
func (o *Options) GetMaxBackgroundJobs() int {
	if o == nil || o.MaxBackgroundJobs == nil || *o.MaxBackgroundJobs <= 0 {
		return DefaultMaxBackgroundJobs
	}
	return *o.MaxBackgroundJobs
}

// PromptPaths maps built-在 提示词 name 到 文件 那 replaces it.
type PromptPaths map[string]string

// PromptNames 是 built-在 prompts 那 options.prompts can replace.
// agent package owns templates themselves 和 asserts it covers 这 list.
var PromptNames = []string{"coder", "task", "plan", "summary", "title", "initialize"}

// DefaultRequestTimeout bounds each LLM API 请求 当 用户 has 不
// configured 超时. Slow 或 unreachable providers fail after it instead
// 的 blocking 会话 forever; streamed responses 是 只 aborted after
// 这 much inactivity between tokens, 和 users running slow local models
// can raise 或 disable it via options.request_timeout.
const DefaultRequestTimeout = 2 * time.Minute

// GetRequestTimeout returns per-请求 超时 为 LLM API calls (
// hard 期限 为 non-streaming requests 和 空闲 超时 为
// streams, 用 more generous budget 为 first streamed token), 或
// zero 当 disabled. nil receiver 和 unset 字段 both mean
// DefaultRequestTimeout, 所以 callers can ask without unwrapping either.
func (o *Options) GetRequestTimeout() time.Duration {
	if o == nil || o.RequestTimeout == nil {
		return DefaultRequestTimeout
	}
	if *o.RequestTimeout <= 0 {
		return 0
	}
	return time.Duration(*o.RequestTimeout) * time.Second
}

// GetPinentryIntegrated reports whether integrated pinentry 是
// enabled. Enabled 通过 默认; callers use it 到 decide whether 到
// 提示词 在-app 或 let external pinentry handle credentials.
func (o *Options) GetPinentryIntegrated() bool {
	if o == nil || o.PinentryIntegrated == nil {
		return true
	}
	return *o.PinentryIntegrated
}

// GetPinentryCacheTimeout returns how long an entered GPG passphrase may
// stay 在 Crush's memory, 或 0 当 caching 是 disabled.
func (o *Options) GetPinentryCacheTimeout() time.Duration {
	if o == nil || o.PinentryCacheTimeout == nil {
		return 0
	}
	if *o.PinentryCacheTimeout <= 0 {
		return 0
	}
	return time.Duration(*o.PinentryCacheTimeout) * time.Second
}

type MCPs map[string]MCPConfig

type MCP struct {
	Name string    `json:"name"`
	MCP  MCPConfig `json:"mcp"`
}

func (m MCPs) Sorted() []MCP {
	sorted := make([]MCP, 0, len(m))
	for k, v := range m {
		sorted = append(sorted, MCP{
			Name: k,
			MCP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b MCP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

type LSPs map[string]LSPConfig

type LSP struct {
	Name string    `json:"name"`
	LSP  LSPConfig `json:"lsp"`
}

func (l LSPs) Sorted() []LSP {
	sorted := make([]LSP, 0, len(l))
	for k, v := range l {
		sorted = append(sorted, LSP{
			Name: k,
			LSP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b LSP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

// ResolvedEnv returns m.Env 用 every 值 expanded through
// given resolver. returned slice 是 的 form "KEY=值" sorted
// 通过 key 所以 callers get deterministic output; receiver's Env map 是
// 不 mutated. On first resolution failure it returns nil 和
// 错误 那 identifies offending key; inner resolver 错误 是
// already sanitized 通过 ResolveValue 和 是 wrapped 用 %w 所以
// errors.Is/As continues 到 work. Callers 是 expected 到 surface it
// (为 MCP, via StateError 在 status card) rather than silently
// spawn 服务端 用 empty credential.
//
// resolver choice matters: 在 服务端 mode pass shell resolver
// 所以 $VAR / $(cmd) expand; 在 客户端 mode pass IdentityResolver 所以
// template 是 forwarded verbatim 和 expansion happens 在 服务端.
func (m MCPConfig) ResolvedEnv(r VariableResolver) ([]string, error) {
	return resolveEnvs(m.Env, r)
}

// ResolvedArgs returns m.Args 用 every element expanded through
// given resolver. 一个 fresh slice 是 allocated; m.Args 是 never mutated.
// On first resolution failure it returns nil 和 错误
// identifying offending positional index; inner resolver 错误
// 是 already sanitized 通过 ResolveValue 和 是 wrapped 用 %w 所以
// errors.Is/As continues 到 work.
//
// See ResolvedEnv 为 guidance 在 picking resolver.
func (m MCPConfig) ResolvedArgs(r VariableResolver) ([]string, error) {
	if len(m.Args) == 0 {
		return nil, nil
	}
	out := make([]string, len(m.Args))
	for i, a := range m.Args {
		v, err := r.ResolveValue(a)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// ResolvedURL returns m.URL expanded through the given resolver. The
// receiver 是 不 mutated. Errors 从 resolver 是 already
// sanitized 通过 ResolveValue 和 是 wrapped 用 %w 为 errors.Is/As.
//
// URLs 运行 through same shell-expansion pipeline 作为 other
// fields, 所以 literal '$' (e.g. OData 查询 strings containing
// $filter/$select) must 是 escaped 作为 '\$' 或 '${DOLLAR:-$}' 到 avoid
// being interpreted 作为 variable reference. Same constraint already
// applies 到 command, args, env, 和 headers.
//
// See ResolvedEnv 为 guidance 在 picking resolver.
func (m MCPConfig) ResolvedURL(r VariableResolver) (string, error) {
	if m.URL == "" {
		return "", nil
	}
	v, err := r.ResolveValue(m.URL)
	if err != nil {
		return "", fmt.Errorf("url: %w", err)
	}
	return v, nil
}

// knownSessionlessMCPs 是 set 的 MCP endpoint URLs (normalized, no
// trailing slash) 那 是 known 不 到 maintain MCP 会话 — they
// never issue Mcp-会话-Id 和 reject SEP-2575
// "subscriptions/listen" 流. Add 条目 当 服务端 是 confirmed 到
// behave 这 way.
var knownSessionlessMCPs = map[string]struct{}{
	"https://api.github.com/mcp":        {},
	"https://api.githubcopilot.com/mcp": {},
}

// IsSessionless reports whether 服务端 should 是 treated 作为 sessionless.
// An explicit Sessionless 值 wins; 当 unset, resolved URL 是 matched
// against knownSessionlessMCPs (trailing slash ignored). URL 是 resolved
// through r 所以 $VAR-expanded endpoints 是 detected too; 在 resolution
// 错误 explicit 值 (或 false) 是 used.
func (m MCPConfig) IsSessionless(r VariableResolver) bool {
	if m.Sessionless != nil {
		return *m.Sessionless
	}
	url, err := m.ResolvedURL(r)
	if err != nil {
		return false
	}
	_, ok := knownSessionlessMCPs[strings.TrimSuffix(url, "/")]
	return ok
}

// ResolvedHeaders returns m.Headers 用 every 值 expanded through
// given resolver. 一个 fresh map 是 allocated; m.Headers 是 never
// mutated. On first resolution failure it returns nil 和 错误
// identifying offending header name; inner resolver 错误 是
// already sanitized 通过 ResolveValue 和 是 wrapped 用 %w 所以
// errors.Is/As continues 到 work.
//
// 一个 header whose 值 resolves 到 empty string (unset bare $VAR
// under lenient nounset, $(echo), 或 literal "") 是 omitted 从
// returned map — sending "X-Auth:" 用 empty 值 是 rejected 通过
// some providers 和 用户's intent 在 "optional, env-gated
// header" 是 clearly "absent 当 var isn't set."
//
// See ResolvedEnv 为 guidance 在 picking resolver.
func (m MCPConfig) ResolvedHeaders(r VariableResolver) (map[string]string, error) {
	if len(m.Headers) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(m.Headers))
	// Sort keys 所以 failures 是 reported deterministically 当 more
	// than one 请求头 would fail.
	keys := make([]string, 0, len(m.Headers))
	for k := range m.Headers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v, err := r.ResolveValue(m.Headers[k])
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", k, err)
		}
		if v == "" {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// ResolvedArgs returns l.Args 用 every element expanded through
// given resolver. 一个 fresh slice 是 allocated; l.Args 是 never mutated.
// On first resolution failure it returns nil 和 错误
// identifying offending positional index; inner resolver 错误
// 是 already sanitized 通过 ResolveValue 和 是 wrapped 用 %w 所以
// errors.Is/As continues 到 work.
//
// Empty resolved values 是 kept ( deliberate "empty positional arg"
// like --flag "" 是 sometimes valid), matching MCPConfig.ResolvedArgs.
//
// resolver choice matters: 在 服务端 mode pass shell resolver
// 所以 $VAR / $(cmd) expand; 在 客户端 mode pass IdentityResolver 所以
// template 是 forwarded verbatim.
func (l LSPConfig) ResolvedArgs(r VariableResolver) ([]string, error) {
	if len(l.Args) == 0 {
		return nil, nil
	}
	out := make([]string, len(l.Args))
	for i, a := range l.Args {
		v, err := r.ResolveValue(a)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// ResolvedEnv returns l.Env 用 every 值 expanded through
// given resolver. 一个 fresh map 是 allocated; l.Env 是 never mutated.
// On first resolution failure it returns nil 和 错误 那
// identifies offending key; inner resolver 错误 是 already
// sanitized 通过 ResolveValue 和 是 wrapped 用 %w 所以 errors.Is/As
// continues 到 work.
//
// Empty resolved values 是 kept ("FOO=" 是 legitimate 请求;
// opt out via ${VAR:+...}), matching MCPConfig.ResolvedEnv.
//
// Shape note: 这 returns map[string]string rather than []string
// shape MCPConfig.ResolvedEnv uses because the consumer
// (powernap.ClientConfig.Environment 在 internal/lsp/客户端.go) takes
// map directly — returning []string here would 只 force
// round-trip back 到 map 在 调用 site.
//
// See ResolvedArgs 为 guidance 在 picking resolver.
func (l LSPConfig) ResolvedEnv(r VariableResolver) (map[string]string, error) {
	if len(l.Env) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(l.Env))
	// Sort keys 所以 failures 是 reported deterministically 当 more
	// than one 值 would fail.
	keys := make([]string, 0, len(l.Env))
	for k := range l.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v, err := r.ResolveValue(l.Env[k])
		if err != nil {
			return nil, fmt.Errorf("env %q: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}

type Agent struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// 这 是 id 的 系统提示词 used 通过 agent
	Disabled bool `json:"disabled,omitempty"`

	Model SelectedModelType `json:"model" jsonschema:"required,description=The model type to use for this agent,enum=large,enum=small,default=large"`

	// available tools 为 agent
	// 如果 这 是 nil, all tools 是 available
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// 这 tells us which MCPs 是 available 为 这 agent
	// 如果 这 是 empty all mcps 是 available
	// string array 是 list 的 tools 从 AllowedMCP agent has available
	// 如果 string array 是 nil, all tools 从 AllowedMCP 是 available
	AllowedMCP map[string][]string `json:"allowed_mcp,omitempty"`

	// Overrides 上下文 paths 为 这 agent
	ContextPaths []string `json:"context_paths,omitempty"`
}

type Tools struct {
	Ls   ToolLs   `json:"ls,omitzero"`
	Grep ToolGrep `json:"grep,omitzero"`
	Glob ToolGlob `json:"glob,omitzero"`
}

type ToolLs struct {
	MaxDepth *int `json:"max_depth,omitempty" jsonschema:"description=Maximum depth for the ls tool,default=0,example=10"`
	MaxItems *int `json:"max_items,omitempty" jsonschema:"description=Maximum number of items to return for the ls tool,default=1000,example=100"`
}

// Limits returns 用户-defined max-depth 和 max-items, 或 their defaults.
func (t ToolLs) Limits() (depth, items int) {
	return ptrValOr(t.MaxDepth, 0), ptrValOr(t.MaxItems, 0)
}

type ToolGrep struct {
	Timeout *time.Duration `json:"timeout,omitempty" jsonschema:"description=Timeout for the grep tool call,default=5s,example=10s"`
}

// GetTimeout returns 用户-defined 超时 或 默认.
func (t ToolGrep) GetTimeout() time.Duration {
	return ptrValOr(t.Timeout, 5*time.Second)
}

type ToolGlob struct {
	Timeout *time.Duration `json:"timeout,omitempty" jsonschema:"description=Timeout for the glob tool call,default=30s,example=10s"`
}

// GetTimeout returns 用户-defined 超时 或 默认.
func (t ToolGlob) GetTimeout() time.Duration {
	return ptrValOr(t.Timeout, 30*time.Second)
}

// HookConfig 是 用户-configured 钩子 command. hooks package owns
// type because it owns 事件 list it 是 validated against.
type HookConfig = hooks.HookConfig

// 配置 holds configuration 为 crush.
type Config struct {
	Schema string `json:"$schema,omitempty"`

	// We currently 只 support large/small 作为 values here.
	Models map[SelectedModelType]SelectedModel `json:"models,omitempty" jsonschema:"description=Model configurations for different model types,example={\"large\":{\"model\":\"gpt-4o\",\"provider\":\"openai\"}}"`

	// Recently used models 存储 在 data 目录 配置.
	RecentModels map[SelectedModelType][]SelectedModel `json:"recent_models,omitempty" jsonschema:"-"`

	// providers 那 是 configured
	Providers *csync.Map[string, ProviderConfig] `json:"providers,omitempty" jsonschema:"description=AI provider configurations"`

	MCP MCPs `json:"mcp,omitempty" jsonschema:"description=Model Context Protocol server configurations"`

	LSP LSPs `json:"lsp,omitempty" jsonschema:"description=Language Server Protocol configurations"`

	Options *Options `json:"options,omitempty" jsonschema:"description=General application options"`

	Permissions *Permissions `json:"permissions,omitempty" jsonschema:"description=Permission settings for tool usage"`

	Tools Tools `json:"tools,omitzero" jsonschema:"description=Tool configurations"`

	Hooks map[string][]HookConfig `json:"hooks,omitempty" jsonschema:"description=User-defined shell commands that fire on hook events (e.g. PreToolUse)"`

	// Env 是 map 的 environment variables set 在 startup.
	Env map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set on startup"`

	Agents map[string]Agent `json:"-"`
}

// cloneForWrite returns copy 的 c 那 存储's typed 字段 mutators
// may modify without racing readers 的 currently published 配置.
//
// Reads 的 published 配置 take no 锁 beyond 指针 load, 所以
// mutator must never 写入 through live 指针. Instead it clones,
// mutates clone, 和 atomically swaps it 在. clone gives fresh
// copies 的 every 字段 typed mutator touches 在 place — Models,
// RecentModels, MCP, 和 Options (用 它的 nested TUI 指针). Providers
// 是 *csync.Map (internally synchronized) 和 是 shared 通过 reference;
// remaining fields 是 immutable after load 从 mutators'
// standpoint 和 是 likewise shared.
func (c *Config) cloneForWrite() *Config {
	nc := *c
	nc.Models = maps.Clone(c.Models)
	nc.RecentModels = maps.Clone(c.RecentModels)
	nc.MCP = maps.Clone(c.MCP)
	if c.Options != nil {
		opts := *c.Options
		if c.Options.TUI != nil {
			tui := *c.Options.TUI
			opts.TUI = &tui
		}
		nc.Options = &opts
	}
	return &nc
}

// ensureTUI returns c.Options.TUI, allocating Options 和 TUI 作为 needed 所以
// callers 可以 assign TUI fields without nil checks.
func (c *Config) ensureTUI() *TUIOptions {
	if c.Options == nil {
		c.Options = &Options{}
	}
	if c.Options.TUI == nil {
		c.Options.TUI = &TUIOptions{}
	}
	return c.Options.TUI
}

func (c *Config) EnabledProviders() []ProviderConfig {
	var enabled []ProviderConfig
	for p := range c.Providers.Seq() {
		if !p.Disable {
			enabled = append(enabled, p)
		}
	}
	return enabled
}

// IsConfigured return true 如果 在 least one 提供方 是 configured
func (c *Config) IsConfigured() bool {
	return len(c.EnabledProviders()) > 0
}

func (c *Config) GetModel(provider, model string) *catwalk.Model {
	if providerConfig, ok := c.Providers.Get(provider); ok {
		for _, m := range providerConfig.Models {
			if m.ID == model {
				return &m
			}
		}
		for _, m := range providerConfig.ChatGPTModels {
			if m.ID == model {
				return &m
			}
		}
		for _, m := range providerConfig.GrokModels {
			if m.ID == model {
				return &m
			}
		}
	}
	return nil
}

// ValidateReasoningEffort checks 那 effort 是 reasoning level
// given 提供方/模型 supports. 它 returns 错误 listing accepted
// levels 当 模型 cannot use it.
func (c *Config) ValidateReasoningEffort(provider, modelID, effort string) error {
	model := c.GetModel(provider, modelID)
	if model == nil {
		return fmt.Errorf("model %q not found for provider %q", modelID, provider)
	}
	levels := model.ReasoningEffortLevels()
	if len(levels) == 0 {
		return fmt.Errorf("model %q does not support reasoning effort", modelID)
	}
	if slices.Contains(levels, effort) {
		return nil
	}
	return fmt.Errorf(
		"model %q does not support reasoning effort %q, accepted values: %s",
		modelID, effort, strings.Join(levels, ", "),
	)
}

// IsModelAvailable returns true 如果 提供方 是 enabled 和 模型
// exists 在 它的 catalog. Unlike GetModel, it rejects disabled providers.
func (c *Config) IsModelAvailable(provider, model string) bool {
	providerConfig, ok := c.Providers.Get(provider)
	if !ok || providerConfig.Disable {
		return false
	}
	// 一个 模型 是 available 在 any 的 提供方's catalogs:
	// API-key list, 或 ChatGPT/Grok subscription list fetched 在
	// login. Subscription models live outside Models, 所以 checking 只
	// there would reject 模型 那 是 selectable 和 usable.
	for _, m := range providerConfig.Models {
		if m.ID == model {
			return true
		}
	}
	for _, m := range providerConfig.ChatGPTModels {
		if m.ID == model {
			return true
		}
	}
	for _, m := range providerConfig.GrokModels {
		if m.ID == model {
			return true
		}
	}
	return false
}

// UsableModels returns models current credential 可以 serve:
// ChatGPT 或 Grok login 只 reaches 它的 subscription's models.
func (p ProviderConfig) UsableModels() []catwalk.Model {
	if models, ok := p.SubscriptionModels(); ok {
		return models
	}
	return p.Models
}

// SubscriptionModels returns models ChatGPT 或 Grok login grants, 和
// whether 提供方 是 signed 在 用 one 在 all.
func (p ProviderConfig) SubscriptionModels() ([]catwalk.Model, bool) {
	if p.OAuthToken == nil {
		return nil, false
	}
	switch catwalk.InferenceProvider(p.ID) {
	case catwalk.InferenceProviderOpenAI:
		return p.ChatGPTModels, true
	case catwalk.InferenceProviderXAI:
		return p.GrokModels, true
	}
	return nil, false
}

func (c *Config) GetProviderForModel(modelType SelectedModelType) *ProviderConfig {
	model, ok := c.Models[modelType]
	if !ok {
		return nil
	}
	if providerConfig, ok := c.Providers.Get(model.Provider); ok {
		return &providerConfig
	}
	return nil
}

func (c *Config) GetModelByType(modelType SelectedModelType) *catwalk.Model {
	model, ok := c.Models[modelType]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

func (c *Config) LargeModel() *catwalk.Model {
	model, ok := c.Models[SelectedModelTypeLarge]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

func (c *Config) SmallModel() *catwalk.Model {
	model, ok := c.Models[SelectedModelTypeSmall]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

const maxRecentModelsPerType = 5

func allToolNames() []string {
	return []string{
		"agent",
		"agent_dispatch",
		"agent_send",
		"agent_status",
		"agent_stop",
		"bash",
		"crush_info",
		"crush_logs",
		"job_output",
		"job_kill",
		"download",
		"edit",
		"multiedit",
		"lsp_diagnostics",
		"lsp_references",
		"lsp_restart",
		"lsp_symbols",
		"lsp_definition",
		"lsp_call_hierarchy",
		"lsp_rename",
		"lsp_replace_symbol",
		"fetch",
		"agentic_fetch",
		"glob",
		"grep",
		"ls",
		"question",
		"sourcegraph",
		"todos",
		"view",
		"write",
		"list_mcp_resources",
		"read_mcp_resource",
	}
}

func resolveAllowedTools(allTools []string, disabledTools []string) []string {
	if disabledTools == nil {
		return allTools
	}
	// filter out disabled tools (exclude mode)
	return filterSlice(allTools, disabledTools, false)
}

func resolveReadOnlyTools(tools []string) []string {
	readOnlyTools := []string{"glob", "grep", "ls", "lsp_call_hierarchy", "lsp_definition", "lsp_symbols", "sourcegraph", "view"}
	// filter 到 只 include tools 那 是 在 allowedtools (include mode)
	return filterSlice(tools, readOnlyTools, true)
}

func resolvePlanTools(tools []string) []string {
	// 读取-只 LSP lookups mirror task agent's 工具 set: planning
	// needs symbol navigation just 作为 much 作为 research does. Both
	// delegation styles 是 listed; coordinator keeps whichever one
	// suits 运行 mode.
	planTools := []string{
		"agent",
		"agent_dispatch",
		"agent_send",
		"agent_status",
		"agent_stop",
		"glob",
		"grep",
		"ls",
		"lsp_call_hierarchy",
		"lsp_definition",
		"lsp_symbols",
		"question",
		"sourcegraph",
		"view",
	}
	return filterSlice(tools, planTools, true)
}

// resolveTaskTools returns tools available 到 task 子代理:
// 读取-只 set plus bash, 所以 it can 运行 git diff, tests, 和 rg while
// reviewing. 它 still cannot edit 或 写入 files.
func resolveTaskTools(tools []string) []string {
	return filterSlice(tools, append(resolveReadOnlyTools(tools), "bash"), true)
}

func filterSlice(data []string, mask []string, include bool) []string {
	var filtered []string
	for _, s := range data {
		// 如果 include 是 true, we include items 那 ARE 在 mask
		// 如果 include 是 false, we include items 那 是 NOT 在 mask
		if include == slices.Contains(mask, s) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func (c *Config) SetupAgents() {
	allowedTools := resolveAllowedTools(allToolNames(), c.Options.DisabledTools)

	agents := map[string]Agent{
		AgentCoder: {
			ID:           AgentCoder,
			Name:         "Coder",
			Description:  "An agent that helps with executing coding tasks.",
			Model:        SelectedModelTypeLarge,
			ContextPaths: c.Options.ContextPaths,
			AllowedTools: allowedTools,
		},

		AgentTask: {
			ID:           AgentTask,
			Name:         "Task",
			Description:  "An agent that helps with searching for context and finding implementation details.",
			Model:        SelectedModelTypeLarge,
			ContextPaths: c.Options.ContextPaths,
			AllowedTools: resolveTaskTools(allowedTools),
			// NO MCPs 或 LSPs 通过 默认
			AllowedMCP: map[string][]string{},
		},

		AgentPlan: {
			ID:           AgentPlan,
			Name:         "Plan",
			Description:  "An agent that performs deep analysis and prepares implementation plans without modifying files.",
			Model:        SelectedModelTypeLarge,
			ContextPaths: c.Options.ContextPaths,
			AllowedTools: resolvePlanTools(allowedTools),
			// NO MCPs 或 LSPs 通过 默认
			AllowedMCP: map[string][]string{},
		},
	}
	c.Agents = agents
}

func (c *ProviderConfig) TestConnection(resolver VariableResolver) error {
	var (
		providerID = catwalk.InferenceProvider(c.ID)
		testURL    = ""
		headers    = make(map[string]string)
		apiKey, _  = resolver.ResolveValue(c.APIKey)
	)

	switch providerID {
	case catwalk.InferenceProviderMiniMax, catwalk.InferenceProviderMiniMaxChina:
		// NOTE: MiniMax has no good endpoint we can use 到 validate API key.
		return nil
	case catwalk.InferenceProviderAlibabaSingapore:
		// NOTE: Alibaba has no good endpoint we can use 到 validate API key.
		// Let's 在 least check pattern.
		if !strings.HasPrefix(apiKey, "sk-") {
			return fmt.Errorf("invalid API key format for provider %s", c.ID)
		}
		return nil
	case catwalk.InferenceProviderBedrock, catwalk.InferenceProviderBedrockEurope:
		// NOTE: Bedrock has `/foundation-models` endpoint 那 we could 在
		// theory use, 但 apparently authorization 是 region-specific,
		// 所以 it's 不 所以 trivial.
		if strings.HasPrefix(apiKey, "ABSK") { // Bedrock API keys
			return nil
		}
		return errors.New("not a valid bedrock api key")
	case catwalk.InferenceProviderVercel:
		// NOTE: Vercel does 不 validate API keys 在 `/models` endpoint.
		if strings.HasPrefix(apiKey, "vck_") { // Vercel API keys
			return nil
		}
		return errors.New("not a valid vercel api key")
	case catwalk.InferenceProviderGemini:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		baseURL = cmp.Or(baseURL, "https://generativelanguage.googleapis.com")
		testURL = baseURL + "/v1beta/models?key=" + url.QueryEscape(apiKey)
	}

	if testURL == "" {
		// models API endpoint 是 determined 通过 提供方 type, which
		// describes API format 提供方 speaks.
		switch c.Type {
		case catwalk.TypeMessages:
			baseURL, _ := resolver.ResolveValue(c.BaseURL)
			baseURL = cmp.Or(baseURL, "https://api.anthropic.com")

			testURL = baseURL + "/v1/models"

			headers["x-api-key"] = apiKey
			headers["anthropic-version"] = "2023-06-01"
		default:
			// Completions, responses, 和 custom 提供方 types all expose
			// OpenAI-style /models 端点.
			baseURL, _ := resolver.ResolveValue(c.BaseURL)
			baseURL = cmp.Or(baseURL, "https://api.openai.com/v1")

			if providerID == catwalk.InferenceProviderOpenRouter {
				testURL = baseURL + "/credits"
			} else {
				testURL = baseURL + "/models"
			}

			headers["Authorization"] = "Bearer " + apiKey
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}
	defer resp.Body.Close()

	switch providerID {
	case catwalk.InferenceProviderZAI:
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("failed to connect to provider %s: %s", c.ID, resp.Status)
		}
	default:
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to connect to provider %s: %s", c.ID, resp.Status)
		}
	}
	return nil
}

// resolveEnvs expands every 值 在 envs through given resolver
// 和 returns fresh "KEY=值" slice sorted 通过 key. input map 是
// 不 mutated. On first resolution failure it returns nil 和
// 错误 identifying offending variable; inner resolver 错误 是
// already sanitized 通过 ResolveValue 和 是 wrapped 用 %w.
func resolveEnvs(envs map[string]string, r VariableResolver) ([]string, error) {
	if len(envs) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	res := make([]string, 0, len(envs))
	for _, k := range keys {
		v, err := r.ResolveValue(envs[k])
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		res = append(res, fmt.Sprintf("%s=%s", k, v))
	}
	return res, nil
}

func ptrValOr[T any](t *T, el T) T {
	if t == nil {
		return el
	}
	return *t
}
