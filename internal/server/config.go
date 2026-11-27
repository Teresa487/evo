package server

import (
	"encoding/json"
	"net/http"

	"github.com/charmbracelet/crush/internal/proto"
)

// handlePostWorkspaceConfigSet sets configuration 字段.
func (c *controllerV1) handlePostWorkspaceConfigSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.ConfigSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if err := c.backend.SetConfigField(id, req.Scope, req.Key, req.Value); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceConfigRemove removes configuration 字段.
func (c *controllerV1) handlePostWorkspaceConfigRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.ConfigRemoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if err := c.backend.RemoveConfigField(id, req.Scope, req.Key); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceConfigModel updates preferred 模型.
func (c *controllerV1) handlePostWorkspaceConfigModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.ConfigModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if err := c.backend.UpdatePreferredModel(id, req.Scope, req.ModelType, req.Model); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceConfigCompact sets compact mode.
func (c *controllerV1) handlePostWorkspaceConfigCompact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.ConfigCompactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if err := c.backend.SetCompactMode(id, req.Scope, req.Enabled); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceConfigProviderKey sets 提供方 API key.
func (c *controllerV1) handlePostWorkspaceConfigProviderKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.ConfigProviderKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	apiKey, err := req.DecodeAPIKey()
	if err != nil {
		c.server.logError(r, "Failed to decode api key", "error", err, "kind", req.Kind)
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := c.backend.SetProviderAPIKey(id, req.Scope, req.ProviderID, apiKey); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceConfigImportCopilot imports Copilot credentials.
func (c *controllerV1) handlePostWorkspaceConfigImportCopilot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	token, ok, err := c.backend.ImportCopilot(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.ImportCopilotResponse{Token: token, Success: ok})
}

// handlePostWorkspaceConfigRefreshOAuth refreshes OAuth token 为 提供方.
func (c *controllerV1) handlePostWorkspaceConfigRefreshOAuth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.ConfigRefreshOAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if err := c.backend.RefreshOAuthToken(r.Context(), id, req.Scope, req.ProviderID); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceProjectNeedsInit reports whether a project needs initialization.
func (c *controllerV1) handleGetWorkspaceProjectNeedsInit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	needs, err := c.backend.ProjectNeedsInitialization(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.ProjectNeedsInitResponse{NeedsInit: needs})
}

// handlePostWorkspaceProjectInit marks project 作为 initialized.
func (c *controllerV1) handlePostWorkspaceProjectInit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := c.backend.MarkProjectInitialized(id); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceProjectInitPrompt returns project initialization 提示词.
func (c *controllerV1) handleGetWorkspaceProjectInitPrompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	prompt, err := c.backend.InitializePrompt(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.ProjectInitPromptResponse{Prompt: prompt})
}

// handleGetWorkspaceSkills returns effective visible skills 为 工作区.
func (c *controllerV1) handleGetWorkspaceSkills(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	skills, err := c.backend.ListSkills(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, skills)
}

// handlePostWorkspaceSkillRead reads skill's content 通过 ID.
func (c *controllerV1) handlePostWorkspaceSkillRead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.ReadSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	content, result, err := c.backend.ReadSkill(r.Context(), id, req.SkillID)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.ReadSkillResponse{Content: content, Result: result})
}

// handlePostWorkspaceMCPEnableDocker enables Docker MCP 服务端.
func (c *controllerV1) handlePostWorkspaceMCPEnableDocker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := c.backend.EnableDockerMCP(r.Context(), id); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceMCPDisableDocker disables Docker MCP 服务端.
func (c *controllerV1) handlePostWorkspaceMCPDisableDocker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := c.backend.DisableDockerMCP(id); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceMCPServersDisabled returns the MCP servers disabled
// 为 工作区's repository.
//
// @摘要 List disabled MCP servers
//	@Tags			mcp
// @Param id 路径 string true "工作区 ID"
//	@Success		200	{array}	string
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
//	@Router			/workspaces/{id}/mcp/disabled [get]
func (c *controllerV1) handleGetWorkspaceMCPServersDisabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	servers, err := c.backend.MCPServersDisabled(r.Context(), id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, servers)
}

// handlePostWorkspaceMCPSetServerDisabled toggles a repository-scoped MCP
// override.
//
// @摘要 Toggle MCP 服务端 为 repository
//	@Tags			mcp
// @受理 json
// @Param id 路径 string true "工作区 ID"
// @Param 请求 body proto.MCPSetServerDisabledRequest true "Toggle 请求"
//	@Success		200
// @Failure 400 {object} proto.错误
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
//	@Router			/workspaces/{id}/mcp/disabled [post]
func (c *controllerV1) handlePostWorkspaceMCPSetServerDisabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.MCPSetServerDisabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := c.backend.SetMCPServerDisabled(r.Context(), id, req.Name, req.Disabled); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceMCPSetServerConfigDisabled toggles MCP 服务端's
// disabled flag 在 global 配置.
//
// @摘要 Toggle MCP 服务端 在 配置
//	@Tags			mcp
// @受理 json
// @Param id 路径 string true "工作区 ID"
// @Param 请求 body proto.MCPSetServerDisabledRequest true "Toggle 请求"
//	@Success		200
// @Failure 400 {object} proto.错误
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
// @Router /workspaces/{id}/mcp/配置-disabled [post]
func (c *controllerV1) handlePostWorkspaceMCPSetServerConfigDisabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.MCPSetServerDisabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := c.backend.SetMCPServerConfigDisabled(r.Context(), id, req.Name, req.Disabled); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceMCPServersEnabled returns MCP servers 用
// repository-scoped enabled override 为 工作区's repository.
//
// @摘要 List enabled-override MCP servers
//	@Tags			mcp
// @Param id 路径 string true "工作区 ID"
//	@Success		200	{array}	string
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
//	@Router			/workspaces/{id}/mcp/enabled [get]
func (c *controllerV1) handleGetWorkspaceMCPServersEnabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	servers, err := c.backend.MCPServersEnabled(r.Context(), id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, servers)
}

// handlePostWorkspaceMCPStartServer starts named MCP 服务端 even 当
// 它的 配置 条目 是 disabled.
//
// @摘要 Start MCP 服务端
//	@Tags			mcp
// @受理 json
// @Param id 路径 string true "工作区 ID"
// @Param 请求 body proto.MCPNameRequest true "MCP name 请求"
//	@Success		200
// @Failure 400 {object} proto.错误
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
//	@Router			/workspaces/{id}/mcp/start [post]
func (c *controllerV1) handlePostWorkspaceMCPStartServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.MCPNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := c.backend.StartMCPServer(r.Context(), id, req.Name); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceMCPRefreshTools refreshes tools 为 named MCP 服务端.
func (c *controllerV1) handlePostWorkspaceMCPRefreshTools(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.MCPNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if err := c.backend.RefreshMCPTools(r.Context(), id, req.Name); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceMCPReadResource reads resource 从 MCP 服务端.
func (c *controllerV1) handlePostWorkspaceMCPReadResource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.MCPReadResourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	contents, err := c.backend.ReadMCPResource(r.Context(), id, req.Name, req.URI)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, contents)
}

// handleGetWorkspaceMCPPrompts returns available MCP prompts 为 工作区.
func (c *controllerV1) handleGetWorkspaceMCPPrompts(w http.ResponseWriter, r *http.Request) {
	prompts, err := c.backend.ListMCPPrompts(r.PathValue("id"))
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, prompts)
}

// handlePostWorkspaceMCPGetPrompt retrieves 提示词 从 MCP 服务端.
func (c *controllerV1) handlePostWorkspaceMCPGetPrompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.MCPGetPromptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	prompt, err := c.backend.GetMCPPrompt(id, req.ClientID, req.PromptID, req.Args)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, proto.MCPGetPromptResponse{Prompt: prompt})
}

// handleGetWorkspaceMCPStates returns state 的 all MCP clients.
func (c *controllerV1) handleGetWorkspaceMCPStates(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	states := c.backend.MCPGetStates(id)
	result := make(map[string]proto.MCPClientInfo, len(states))
	for k, v := range states {
		result[k] = proto.MCPClientInfo{
			Name:           v.Name,
			State:          proto.MCPState(v.State),
			Error:          v.Error,
			ToolCount:      v.Counts.Tools,
			PromptCount:    v.Counts.Prompts,
			ResourceCount:  v.Counts.Resources,
			ConnectedAt:    v.ConnectedAt,
			Channel:        v.Channel,
			ChannelOptIn:   v.ChannelOptIn,
			ChannelCapable: v.ChannelCapable,
		}
	}
	jsonEncode(w, result)
}

// handleGetWorkspaceMCPPendingAuth returns the MCP servers awaiting OAuth
// authentication 为 工作区.
func (c *controllerV1) handleGetWorkspaceMCPPendingAuth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pending, err := c.backend.MCPPendingAuth(id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	result := make([]proto.MCPPendingAuthServer, len(pending))
	for i, p := range pending {
		result[i] = proto.MCPPendingAuthServer{Name: p.Name, URL: p.URL}
	}
	jsonEncode(w, result)
}

// handleGetWorkspaceMCPAuthURL returns current OAuth 授权 URL
// 为 named MCP 服务端, 如果 flow 是 在 progress.
func (c *controllerV1) handleGetWorkspaceMCPAuthURL(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		jsonError(w, http.StatusBadRequest, "name is required")
		return
	}
	jsonEncode(w, proto.MCPAuthResponse{AuthURL: c.backend.MCPAuthURL(name)})
}

// handlePostWorkspaceMCPAuth runs OAuth flow 为 named MCP 服务端.
// local browser 是 suppressed 在 服务端; 客户端 polls
// pending-auth / auth-url 到 surface authorization URL 在 用户's
// machine. 调用 blocks until flow completes 或 请求 上下文
// 是 cancelled.
func (c *controllerV1) handlePostWorkspaceMCPAuth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.MCPNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if err := c.backend.MCPAuthenticate(r.Context(), id, req.Name); err != nil {
		// If 客户端 went away 请求 上下文 was cancelled;
		// 错误 是 still surfaced 为 logging 但 no 响应 can
		// 是 written.
		c.handleError(w, r, err)
		return
	}
	// flow has finished 通过 time 这 returns, 所以 there 是 no
	// 在-progress authorization URL 到 报告; 客户端 polls
	// /mcp/auth-url 为 那 while flow runs.
	jsonEncode(w, proto.MCPAuthResponse{})
}

// handlePostWorkspaceMCPRefreshPrompts refreshes prompts 为 named MCP 服务端.
func (c *controllerV1) handlePostWorkspaceMCPRefreshPrompts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.MCPNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	c.backend.MCPRefreshPrompts(r.Context(), id, req.Name)
	w.WriteHeader(http.StatusOK)
}

// handlePostWorkspaceMCPRefreshResources refreshes resources 为 named MCP 服务端.
func (c *controllerV1) handlePostWorkspaceMCPRefreshResources(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.MCPNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	c.backend.MCPRefreshResources(r.Context(), id, req.Name)
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceSkillsDisabled returns skills disabled 为
// 工作区's repository.
//
// @摘要 List disabled skills
//	@Tags			skills
// @Param id 路径 string true "工作区 ID"
//	@Success		200	{array}	string
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
//	@Router			/workspaces/{id}/skills/disabled [get]
func (c *controllerV1) handleGetWorkspaceSkillsDisabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	names, err := c.backend.SkillsDisabled(r.Context(), id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, names)
}

// handlePostWorkspaceSetSkillDisabled toggles a repository-scoped skill
// override.
//
// @摘要 Toggle skill 为 repository
//	@Tags			skills
// @受理 json
// @Param id 路径 string true "工作区 ID"
// @Param 请求 body proto.SkillSetDisabledRequest true "Toggle 请求"
//	@Success		200
// @Failure 400 {object} proto.错误
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
//	@Router			/workspaces/{id}/skills/disabled [post]
func (c *controllerV1) handlePostWorkspaceSetSkillDisabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.SkillSetDisabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := c.backend.SetSkillDisabled(r.Context(), id, req.Name, req.Disabled); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetWorkspaceSkillsEnabled returns skills 用
// repository-scoped enabled override 为 工作区's repository.
//
// @摘要 List enabled-override skills
//	@Tags			skills
// @Param id 路径 string true "工作区 ID"
//	@Success		200	{array}	string
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
//	@Router			/workspaces/{id}/skills/enabled [get]
func (c *controllerV1) handleGetWorkspaceSkillsEnabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	names, err := c.backend.SkillsEnabled(r.Context(), id)
	if err != nil {
		c.handleError(w, r, err)
		return
	}
	jsonEncode(w, names)
}

// handlePostWorkspaceSetSkillConfigDisabled toggles a skill's disabled
// flag 在 global 配置.
//
// @摘要 Toggle skill 在 配置
//	@Tags			skills
// @受理 json
// @Param id 路径 string true "工作区 ID"
// @Param 请求 body proto.SkillSetDisabledRequest true "Toggle 请求"
//	@Success		200
// @Failure 400 {object} proto.错误
// @Failure 404 {object} proto.错误
// @Failure 500 {object} proto.错误
// @Router /workspaces/{id}/skills/配置-disabled [post]
func (c *controllerV1) handlePostWorkspaceSetSkillConfigDisabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req proto.SkillSetDisabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.server.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := c.backend.SetSkillConfigDisabled(r.Context(), id, req.Name, req.Disabled); err != nil {
		c.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
