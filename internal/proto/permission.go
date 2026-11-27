// permission.go —— 协议里的权限请求与答复类型。

package proto

import (
	"encoding/json"

	"github.com/charmbracelet/crush/internal/permission"
)

// PermissionModeToProto converts internal 权限.PermissionMode 到
// proto wire representation.
func PermissionModeToProto(mode permission.PermissionMode) WorkspacePermissionMode {
	switch mode {
	case permission.PermissionModeSysadmin:
		return WorkspacePermissionModeSysadmin
	case permission.PermissionModeYolo:
		return WorkspacePermissionModeYolo
	default:
		return WorkspacePermissionModeNormal
	}
}

// ProtoModeToPermission converts proto WorkspacePermissionMode 到
// internal 权限.PermissionMode.
func ProtoModeToPermission(mode WorkspacePermissionMode) permission.PermissionMode {
	switch mode {
	case WorkspacePermissionModeSysadmin:
		return permission.PermissionModeSysadmin
	case WorkspacePermissionModeYolo:
		return permission.PermissionModeYolo
	default:
		return permission.PermissionModeNormal
	}
}

// CreatePermissionRequest represents 请求 到 创建 权限.
type CreatePermissionRequest struct {
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
	Danger      string `json:"danger,omitempty"`
}

// PermissionNotification represents 通知 about 权限 change.
type PermissionNotification struct {
	SessionID  string `json:"session_id"`
	ToolCallID string `json:"tool_call_id"`
	Granted    bool   `json:"granted"`
	Denied     bool   `json:"denied"`
	Cancelled  bool   `json:"cancelled"`
}

// PermissionRequest represents pending 权限 请求.
type PermissionRequest struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
	Danger      string `json:"danger,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler interface. 这 是 needed
// because Params 字段 是 的 type any, 所以 we need 到 unmarshal it into
// 它的 appropriate type based 在 [PermissionRequest.ToolName].
func (p *PermissionRequest) UnmarshalJSON(data []byte) error {
	type Alias PermissionRequest
	aux := &struct {
		Params json.RawMessage `json:"params"`
		*Alias
	}{
		Alias: (*Alias)(p),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	params, err := unmarshalToolParams(p.ToolName, aux.Params)
	if err != nil {
		return err
	}
	p.Params = params
	return nil
}

// UnmarshalJSON implements json.Unmarshaler interface. 这 是 needed
// because Params 字段 是 的 type any, 所以 we need 到 unmarshal it into
// 它的 appropriate type based 在 [CreatePermissionRequest.ToolName].
func (p *CreatePermissionRequest) UnmarshalJSON(data []byte) error {
	type Alias CreatePermissionRequest
	aux := &struct {
		Params json.RawMessage `json:"params"`
		*Alias
	}{
		Alias: (*Alias)(p),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	params, err := unmarshalToolParams(p.ToolName, aux.Params)
	if err != nil {
		return err
	}
	p.Params = params
	return nil
}

func unmarshalToolParams(toolName string, raw json.RawMessage) (any, error) {
	switch toolName {
	case BashToolName:
		var params BashPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return params, nil
	case DownloadToolName:
		var params DownloadPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return params, nil
	case EditToolName:
		var params EditPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return params, nil
	case WriteToolName:
		var params WritePermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return params, nil
	case MultiEditToolName:
		var params MultiEditPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return params, nil
	case FetchToolName:
		var params FetchPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return params, nil
	case AgenticFetchToolName:
		var params AgenticFetchPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return params, nil
	case ViewToolName:
		var params ViewPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return params, nil
	case LSToolName:
		var params LSPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return params, nil
	case ReplaceSymbolToolName:
		var params ReplaceSymbolPermissionsParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return params, nil
	default:
		// For unknown tools, keep params 作为 generic JSON 值
		// they hold. That 是 不 always object: MCP tools send their
		// raw JSON input 作为 string. Never fail whole 请求 over
		// params we have no type 为; fall back 到 raw JSON 文本.
		var generic any
		if err := json.Unmarshal(raw, &generic); err != nil {
			return string(raw), nil
		}
		return generic, nil
	}
}
