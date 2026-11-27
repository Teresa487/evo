// fetch_types.go —— fetch 相关工具的参数与结果类型。

package tools

// AgenticFetchToolName 是 name 的 agentic fetch 工具.
const AgenticFetchToolName = "agentic_fetch"

// WebFetchToolName 是 name 的 web_fetch 工具.
const WebFetchToolName = "web_fetch"

// WebSearchToolName 是 name 的 web_search 工具 为 subagents.
const WebSearchToolName = "web_search"

// LargeContentThreshold 是 size threshold 为 saving content 到 文件.
const LargeContentThreshold = 50000 // 50KB

// AgenticFetchParams defines parameters 为 agentic fetch 工具.
type AgenticFetchParams struct {
	URL    string `json:"url,omitempty" description:"The URL to fetch content from (optional - if not provided, the agent will search the web)"`
	Prompt string `json:"prompt" description:"The prompt describing what information to find or extract"`
}

// AgenticFetchPermissionsParams defines 权限 parameters 为 agentic fetch 工具.
type AgenticFetchPermissionsParams struct {
	URL    string `json:"url,omitempty"`
	Prompt string `json:"prompt"`
}

// WebFetchParams defines parameters 为 web_fetch 工具.
type WebFetchParams struct {
	URL string `json:"url" description:"The URL to fetch content from"`
}

// WebSearchParams defines parameters 为 web_search 工具.
type WebSearchParams struct {
	Query      string `json:"query" description:"The search query to find information on the web"`
	MaxResults int    `json:"max_results,omitempty" description:"Maximum number of results to return (default: 10, max: 20)"`
}

// FetchParams defines parameters 为 simple fetch 工具.
type FetchParams struct {
	URL     string `json:"url" description:"The URL to fetch content from"`
	Format  string `json:"format" description:"The format to return the content in (text, markdown, or html)"`
	Timeout int    `json:"timeout,omitempty" description:"Optional timeout in seconds (max 120)"`
}

// FetchPermissionsParams defines 权限 parameters 为 simple fetch 工具.
type FetchPermissionsParams struct {
	URL     string `json:"url"`
	Format  string `json:"format"`
	Timeout int    `json:"timeout,omitempty"`
}
