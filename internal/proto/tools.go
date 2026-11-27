package proto

// wire schema 为 per-工具 权限 parameters 是 owned 通过
// 工具 itself, 不 duplicated here. We alias canonical types 所以
// there 是 exactly one source 的 truth 和 所以 values survive
// round-trip across 客户端/服务端 boundary 作为 same Go type
// UI asserts 在.
import "github.com/charmbracelet/crush/internal/agent/tools"

// ToolResponseType represents type 的 工具 响应.
type ToolResponseType string

const (
	ToolResponseTypeText  ToolResponseType = "text"
	ToolResponseTypeImage ToolResponseType = "image"
)

// ToolResponse represents 响应 从 工具.
type ToolResponse struct {
	Type     ToolResponseType `json:"type"`
	Content  string           `json:"content"`
	Metadata string           `json:"metadata,omitempty"`
	IsError  bool             `json:"is_error"`
}

const BashToolName = "bash"

// BashParams represents parameters 为 bash 工具.
type BashParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// BashPermissionsParams represents 权限 parameters 为 bash 工具.
type BashPermissionsParams = tools.BashPermissionsParams

// BashResponseMetadata represents metadata 为 bash 工具 响应.
type BashResponseMetadata struct {
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	Output           string `json:"output"`
	WorkingDirectory string `json:"working_directory"`
}

// DiagnosticsParams represents parameters 为 diagnostics 工具.
type DiagnosticsParams struct {
	FilePath string `json:"file_path"`
}

const DownloadToolName = "download"

// DownloadParams represents parameters 为 download 工具.
type DownloadParams struct {
	URL      string `json:"url"`
	FilePath string `json:"file_path"`
	Timeout  int    `json:"timeout,omitempty"`
}

// DownloadPermissionsParams represents 权限 parameters 为 download 工具.
type DownloadPermissionsParams = tools.DownloadPermissionsParams

const EditToolName = "edit"

// EditParams represents parameters 为 edit 工具.
type EditParams struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// EditPermissionsParams represents 权限 parameters 为 edit 工具.
type EditPermissionsParams = tools.EditPermissionsParams

// EditResponseMetadata represents metadata 为 edit 工具 响应.
type EditResponseMetadata struct {
	Additions  int    `json:"additions"`
	Removals   int    `json:"removals"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}

const FetchToolName = "fetch"

// FetchParams represents parameters 为 fetch 工具.
type FetchParams struct {
	URL     string `json:"url"`
	Format  string `json:"format"`
	Timeout int    `json:"timeout,omitempty"`
}

// FetchPermissionsParams represents 权限 parameters 为 fetch 工具.
type FetchPermissionsParams = tools.FetchPermissionsParams

// AgenticFetchToolName 是 name 的 agentic_fetch 工具.
const AgenticFetchToolName = tools.AgenticFetchToolName

// AgenticFetchPermissionsParams represents 权限 parameters 为
// agentic_fetch 工具.
type AgenticFetchPermissionsParams = tools.AgenticFetchPermissionsParams

const GlobToolName = "glob"

// GlobParams represents parameters 为 glob 工具.
type GlobParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

// GlobResponseMetadata represents metadata 为 glob 工具 响应.
type GlobResponseMetadata struct {
	NumberOfFiles int  `json:"number_of_files"`
	Truncated     bool `json:"truncated"`
}

const GrepToolName = "grep"

// GrepParams represents parameters 为 grep 工具.
type GrepParams struct {
	Pattern     string `json:"pattern"`
	Path        string `json:"path"`
	Include     string `json:"include"`
	LiteralText bool   `json:"literal_text"`
}

// GrepResponseMetadata represents metadata 为 grep 工具 响应.
type GrepResponseMetadata struct {
	NumberOfMatches int  `json:"number_of_matches"`
	Truncated       bool `json:"truncated"`
}

const LSToolName = "ls"

// LSParams represents parameters 为 ls 工具.
type LSParams struct {
	Path   string   `json:"path"`
	Ignore []string `json:"ignore"`
}

// LSPermissionsParams represents 权限 parameters 为 ls 工具.
type LSPermissionsParams = tools.LSPermissionsParams

// TreeNode represents node 在 目录 tree.
type TreeNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	Type     string      `json:"type"`
	Children []*TreeNode `json:"children,omitempty"`
}

// LSResponseMetadata represents metadata 为 ls 工具 响应.
type LSResponseMetadata struct {
	NumberOfFiles int  `json:"number_of_files"`
	Truncated     bool `json:"truncated"`
}

const MultiEditToolName = "multiedit"

// MultiEditOperation represents single edit operation 在 multi-edit.
type MultiEditOperation struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// MultiEditParams represents parameters 为 multi-edit 工具.
type MultiEditParams struct {
	FilePath string               `json:"file_path"`
	Edits    []MultiEditOperation `json:"edits"`
}

// MultiEditPermissionsParams represents 权限 parameters 为 multi-edit 工具.
type MultiEditPermissionsParams = tools.MultiEditPermissionsParams

// MultiEditResponseMetadata represents metadata 为 multi-edit 工具 响应.
type MultiEditResponseMetadata struct {
	Additions    int    `json:"additions"`
	Removals     int    `json:"removals"`
	OldContent   string `json:"old_content,omitempty"`
	NewContent   string `json:"new_content,omitempty"`
	EditsApplied int    `json:"edits_applied"`
}

// ReplaceSymbolToolName 是 name 的 lsp_replace_symbol 工具.
const ReplaceSymbolToolName = tools.ReplaceSymbolToolName

// ReplaceSymbolPermissionsParams represents 权限 parameters 为
// lsp_replace_symbol 工具.
type ReplaceSymbolPermissionsParams = tools.ReplaceSymbolPermissionsParams

const SourcegraphToolName = "sourcegraph"

// SourcegraphParams represents parameters 为 sourcegraph 工具.
type SourcegraphParams struct {
	Query         string `json:"query"`
	Count         int    `json:"count,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	Timeout       int    `json:"timeout,omitempty"`
}

// SourcegraphResponseMetadata represents metadata 为 sourcegraph 工具 响应.
type SourcegraphResponseMetadata struct {
	NumberOfMatches int  `json:"number_of_matches"`
	Truncated       bool `json:"truncated"`
}

const ViewToolName = "view"

// ViewParams represents parameters 为 view 工具.
type ViewParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

// ViewPermissionsParams represents 权限 parameters 为 view 工具.
type ViewPermissionsParams = tools.ViewPermissionsParams

// ViewResponseMetadata represents metadata 为 view 工具 响应.
type ViewResponseMetadata struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

const WriteToolName = "write"

// WriteParams represents parameters 为 写入 工具.
type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// WritePermissionsParams represents 权限 parameters 为 写入 工具.
type WritePermissionsParams = tools.WritePermissionsParams

// WriteResponseMetadata represents metadata 为 写入 工具 响应.
type WriteResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}
