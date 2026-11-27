package backend

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/charmbracelet/crush/internal/agent"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/gitutil"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/skills"
)

// publishConfigChanged publishes ConfigChanged 事件 在 工作区's
// 事件 总线 所以 all subscribers (e.g. remote clients) refresh their
// cached 配置 snapshot. 它 also re-initializes any MCP servers whose
// configuration changed 作为 结果 的 写入.
func publishConfigChanged(ws *Workspace) {
	if ws == nil || ws.App == nil {
		return
	}

	// Re-init MCP servers whose 配置 changed. MCP state 是 process-global,
	// 所以 这 只 needs 到 happen once regardless 的 which 工作区
	// triggered 写入. 运行 async 所以 unrelated 配置 writes (模型
	// switches, API keys) don't block 在 MCP reconciliation. Bound 到
	// 工作区 ctx 所以 teardown cancels any 在-flight init.
	go mcptools.Reinitialize(ws.ctx, ws.Cfg)

	announceConfigChanged(ws)
}

// announceConfigChanged publishes 只 ConfigChanged 事件, 为
// writes 那 must leave MCP servers alone. 一个 reconcile ignores
// repository-scoped toggles, 所以 it would stop 配置-disabled 服务端
// enabled 为 这 repository 和 start one disabled here.
func announceConfigChanged(ws *Workspace) {
	ws.SendEvent(pubsub.Event[proto.ConfigChanged]{
		Type:    pubsub.UpdatedEvent,
		Payload: proto.ConfigChanged{WorkspaceID: ws.ID},
	})
}

// ReloadConfig re-reads 服务端's 和 every hosted 工作区's 配置
// 从 disk, 所以 edits take effect without restart. 一个 配置 那 fails
// 到 load keeps 它的 previous state 和 does 不 hold back others.
func (b *Backend) ReloadConfig(ctx context.Context) error {
	workspaces := slices.Collect(b.workspaces.Seq())

	errs := make([]error, len(workspaces)+1)
	if b.cfg != nil {
		errs[0] = b.cfg.ReloadFromDisk(ctx)
	}
	var wg sync.WaitGroup
	for i, ws := range workspaces {
		wg.Go(func() {
			if err := ws.reloadConfig(ctx); err != nil {
				errs[i+1] = fmt.Errorf("workspace %s: %w", ws.ID, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// reloadConfig re-reads 工作区 配置 和 rebuilds agent 从 it.
func (w *Workspace) reloadConfig(ctx context.Context) error {
	if err := w.Cfg.ReloadFromDisk(ctx); err != nil {
		return err
	}
	if coord := w.Coordinator(); coord != nil {
		if err := coord.UpdateModels(ctx); err != nil {
			return err
		}
	}
	publishConfigChanged(w)
	return nil
}

// MCPResourceContents holds contents 的 MCP resource returned
// 通过 backend.
type MCPResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     []byte `json:"blob,omitempty"`
}

// SetConfigField sets key/值 pair 在 配置 文件 为
// given scope.
func (b *Backend) SetConfigField(workspaceID string, scope config.Scope, key string, value any) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.SetConfigField(scope, key, value); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// RemoveConfigField removes key 从 配置 文件 为 given
// scope.
func (b *Backend) RemoveConfigField(workspaceID string, scope config.Scope, key string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.RemoveConfigField(scope, key); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// UpdatePreferredModel updates preferred 模型 为 given type
// 和 persists it 到 配置 文件 在 given scope.
func (b *Backend) UpdatePreferredModel(workspaceID string, scope config.Scope, modelType config.SelectedModelType, model config.SelectedModel) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.UpdatePreferredModel(scope, modelType, model); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// SetCompactMode sets compact mode setting 和 persists it.
func (b *Backend) SetCompactMode(workspaceID string, scope config.Scope, enabled bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.SetCompactMode(scope, enabled); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// SetProviderAPIKey sets API key 为 提供方 和 persists it.
func (b *Backend) SetProviderAPIKey(workspaceID string, scope config.Scope, providerID string, apiKey any) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.SetProviderAPIKey(scope, providerID, apiKey); err != nil {
		return err
	}
	ws.Cfg.SignalAuthComplete(providerID)
	publishConfigChanged(ws)
	return nil
}

// ImportCopilot attempts 到 import GitHub Copilot token 从 disk.
func (b *Backend) ImportCopilot(workspaceID string) (*oauth.Token, bool, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, false, err
	}
	token, ok := ws.Cfg.ImportCopilot()
	if ok {
		publishConfigChanged(ws)
	}
	return token, ok, nil
}

// RefreshOAuthToken refreshes OAuth token 为 提供方.
func (b *Backend) RefreshOAuthToken(ctx context.Context, workspaceID string, scope config.Scope, providerID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := ws.Cfg.RefreshOAuthToken(ctx, scope, providerID); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// ProjectNeedsInitialization checks whether project 在 这
// 工作区 needs initialization.
func (b *Backend) ProjectNeedsInitialization(workspaceID string) (bool, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return false, err
	}
	return config.ProjectNeedsInitialization(ws.Cfg)
}

// MarkProjectInitialized marks project 作为 initialized.
func (b *Backend) MarkProjectInitialized(workspaceID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if err := config.MarkProjectInitialized(ws.Cfg); err != nil {
		return err
	}
	publishConfigChanged(ws)
	return nil
}

// InitializePrompt builds initialization 提示词 为 工作区.
func (b *Backend) InitializePrompt(workspaceID string) (string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return "", err
	}
	return agent.InitializePrompt(ws.Cfg)
}

// GitBranch returns current Git branch 的 工作区's working
// 目录, 或 empty string 当 it 是 不 Git repository 或 HEAD
// 是 detached.
func (b *Backend) GitBranch(workspaceID string) (string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return "", err
	}
	return gitutil.CurrentBranch(ws.Cfg.WorkingDir()), nil
}

// ReadSkill reads skill's content 通过 ID.
func (b *Backend) ReadSkill(ctx context.Context, workspaceID, skillID string) ([]byte, proto.SkillReadResult, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, proto.SkillReadResult{}, err
	}

	mgr := ws.Skills
	content, result, err := skills.ReadContent(
		mgr.ActiveSkills(), mgr.ResolvedPaths(), mgr.WorkingDir(), skillID,
	)
	if err != nil {
		return nil, proto.SkillReadResult{}, err
	}
	return content, proto.SkillReadResult{
		Name:        result.Name,
		Description: result.Description,
		Source:      string(result.Source),
		Builtin:     result.Builtin,
	}, nil
}

// ListSkills returns effective visible skills 为 工作区.
func (b *Backend) ListSkills(workspaceID string) ([]proto.SkillInfo, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	mgr := ws.Skills
	entries := skills.Catalog(mgr.ActiveSkills(), mgr.ResolvedPaths(), mgr.WorkingDir())
	result := make([]proto.SkillInfo, len(entries))
	for i, entry := range entries {
		result[i] = proto.SkillInfo{
			ID:            entry.ID,
			Name:          entry.Name,
			Description:   entry.Description,
			Label:         entry.Label,
			Source:        string(entry.Source),
			UserInvocable: entry.UserInvocable,
		}
	}
	return result, nil
}

// EnableDockerMCP validates Docker MCP availability, stages the
// configuration, starts MCP 客户端, 和 persists 配置.
func (b *Backend) EnableDockerMCP(ctx context.Context, workspaceID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	mcpConfig, err := ws.Cfg.PrepareDockerMCPConfig()
	if err != nil {
		return err
	}

	if err := mcptools.InitializeSingle(ctx, config.DockerMCPName, ws.Cfg); err != nil {
		disableErr := mcptools.DisableSingle(ws.Cfg, config.DockerMCPName)
		ws.Cfg.RemoveDockerMCPInMemory()
		return fmt.Errorf("failed to start docker MCP: %w", errors.Join(err, disableErr))
	}

	if err := ws.Cfg.PersistDockerMCPConfig(mcpConfig); err != nil {
		disableErr := mcptools.DisableSingle(ws.Cfg, config.DockerMCPName)
		ws.Cfg.RemoveDockerMCPInMemory()
		return fmt.Errorf("docker MCP started but failed to persist configuration: %w", errors.Join(err, disableErr))
	}

	publishConfigChanged(ws)
	return nil
}

// DisableDockerMCP closes Docker MCP 客户端, removes
// configuration, 和 persists change.
func (b *Backend) DisableDockerMCP(workspaceID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if err := mcptools.DisableSingle(ws.Cfg, config.DockerMCPName); err != nil {
		return fmt.Errorf("failed to disable docker MCP: %w", err)
	}

	if err := ws.Cfg.DisableDockerMCP(); err != nil {
		return err
	}

	publishConfigChanged(ws)
	return nil
}

// RefreshMCPTools refreshes tools 为 named MCP 服务端.
func (b *Backend) RefreshMCPTools(ctx context.Context, workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}
	mcptools.RefreshTools(ctx, ws.Cfg, name)
	return nil
}

// ReadMCPResource reads resource 从 named MCP 服务端.
func (b *Backend) ReadMCPResource(ctx context.Context, workspaceID, name, uri string) ([]MCPResourceContents, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	contents, err := mcptools.ReadResource(ctx, ws.Cfg, name, uri)
	if err != nil {
		return nil, err
	}
	result := make([]MCPResourceContents, len(contents))
	for i, c := range contents {
		result[i] = MCPResourceContents{
			URI:      c.URI,
			MIMEType: c.MIMEType,
			Text:     c.Text,
			Blob:     c.Blob,
		}
	}
	return result, nil
}

// GetMCPPrompt retrieves 提示词 从 named MCP 服务端.
func (b *Backend) GetMCPPrompt(workspaceID, clientID, promptID string, args map[string]string) (string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return "", err
	}
	return commands.GetMCPPrompt(ws.Cfg, clientID, promptID, args)
}

func (b *Backend) ListMCPPrompts(workspaceID string) ([]proto.MCPPrompt, error) {
	if _, err := b.GetWorkspace(workspaceID); err != nil {
		return nil, err
	}
	prompts, err := commands.LoadMCPPrompts()
	if err != nil {
		return nil, err
	}
	result := make([]proto.MCPPrompt, len(prompts))
	for i, prompt := range prompts {
		arguments := make([]proto.MCPPromptArgument, len(prompt.Arguments))
		for j, argument := range prompt.Arguments {
			arguments[j] = proto.MCPPromptArgument{
				ID:          argument.ID,
				Title:       argument.Title,
				Description: argument.Description,
				Required:    argument.Required,
			}
		}
		result[i] = proto.MCPPrompt{
			ID:          prompt.ID,
			Title:       prompt.Title,
			Description: prompt.Description,
			PromptID:    prompt.PromptID,
			ClientID:    prompt.ClientID,
			Arguments:   arguments,
		}
	}
	return result, nil
}

// GetWorkingDir returns working 目录 为 工作区.
func (b *Backend) GetWorkingDir(workspaceID string) (string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return "", err
	}
	return ws.Cfg.WorkingDir(), nil
}
