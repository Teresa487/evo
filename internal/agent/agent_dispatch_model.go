package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/fantasy"
	"golang.org/x/sync/errgroup"

	"github.com/charmbracelet/crush/internal/config"
)

// mainModel returns 模型 main agent runs 在, 或 zero 模型
// before one 是 set.
func (c *coordinator) mainModel() Model {
	if agent, _ := c.activeAgent(); agent != nil {
		return agent.Model()
	}
	return Model{}
}

// resolveModelOverride turns 派发 工具's 模型-override params into
// 模型 selection 子代理 should 运行 在. ok 是 false 当 no
// override was requested. 一个 non-empty 拒绝 means one was requested 但
// cannot 是 served; it goes back 到 模型 所以 调用 can 是 retried.
func (c *coordinator) resolveModelOverride(params AgentDispatchParams, parentModel Model) (sel config.SelectedModel, ok bool, refusal string) {
	provider := strings.TrimSpace(params.Provider)
	model := strings.TrimSpace(params.Model)
	reasoning := strings.TrimSpace(params.Reasoning)
	switch {
	case provider == "" && model == "" && reasoning == "":
		return config.SelectedModel{}, false, ""
	case provider != "" && model == "":
		return config.SelectedModel{}, false, "provider is given without model; pass model as well"
	case model == "":
		// Reasoning alone rides 子代理's 默认 selection.
		sub := c.subAgentModels().Large
		if sub.Model == nil {
			return config.SelectedModel{}, false, "no subagent model is configured"
		}
		sel = sub.ModelCfg
	default:
		if provider == "" {
			if parentModel.Model == nil {
				return config.SelectedModel{}, false, "the parent agent has no model to take a provider from"
			}
			provider = parentModel.ModelCfg.Provider
		}
		// 一个 fresh selection: everything else 是 模型's own 默认.
		sel = config.SelectedModel{Provider: provider, Model: model}
	}
	catwalkModel, refusal := c.availableModel(sel)
	if refusal == "" {
		refusal = applyReasoning(&sel, catwalkModel, reasoning)
	}
	return sel, true, refusal
}

// availableModel finds selection's 模型 among what catalog would list.
func (c *coordinator) availableModel(sel config.SelectedModel) (catwalk.Model, string) {
	cfg := c.cfg.Config()
	provider, ok := cfg.Providers.Get(sel.Provider)
	if !ok || provider.Disable {
		ids := make([]string, 0)
		for _, p := range usableProviders(cfg) {
			ids = append(ids, p.ID)
		}
		return catwalk.Model{}, fmt.Sprintf("provider %q is not available; use one of: %s", sel.Provider, strings.Join(ids, ", "))
	}
	models := provider.UsableModels()
	idx := slices.IndexFunc(models, func(m catwalk.Model) bool { return m.ID == sel.Model })
	if idx < 0 {
		return catwalk.Model{}, fmt.Sprintf("model %q is not available from provider %q; use one of: %s", sel.Model, sel.Provider, modelIDList(models))
	}
	return models[idx], ""
}

// reasoningOptions lists values 模型 takes 为 reasoning: 它的 effort
// levels, 或 在 和 off 为 模型 那 can 只 toggle thinking.
func reasoningOptions(m catwalk.Model) []string {
	if levels := m.ReasoningEffortLevels(); len(levels) > 0 {
		return levels
	}
	if m.Reasoning.Thinking == catwalk.ThinkingToggleable {
		return []string{"on", "off"}
	}
	return nil
}

// applyReasoning sets selection's effort level 或 thinking toggle,
// whichever 模型 reasons 用.
func applyReasoning(sel *config.SelectedModel, m catwalk.Model, reasoning string) string {
	if reasoning == "" {
		return ""
	}
	options := reasoningOptions(m)
	if !slices.Contains(options, reasoning) {
		if len(options) == 0 {
			return fmt.Sprintf("model %q does not support reasoning", m.ID)
		}
		return fmt.Sprintf("model %q takes reasoning %s", m.ID, strings.Join(options, ", "))
	}
	if len(m.ReasoningEffortLevels()) > 0 {
		sel.ReasoningEffort = reasoning
	} else {
		sel.Think = reasoning == "on"
	}
	return ""
}

// catalogModelsPerProvider keeps one huge catalog 从 crowding out 提示词.
const catalogModelsPerProvider = 25

// usableProviders lists enabled providers 通过 ID, 所以 catalog 文本 是 stable across turns.
func usableProviders(cfg *config.Config) []config.ProviderConfig {
	providers := cfg.EnabledProviders()
	slices.SortFunc(providers, func(a, b config.ProviderConfig) int { return strings.Compare(a.ID, b.ID) })
	return providers
}

func modelIDList(models []catwalk.Model) string {
	listed := models[:min(len(models), catalogModelsPerProvider)]
	ids := make([]string, 0, len(listed))
	for _, m := range listed {
		if options := reasoningOptions(m); len(options) > 0 {
			ids = append(ids, m.ID+" ["+strings.Join(options, "|")+"]")
		} else {
			ids = append(ids, m.ID)
		}
	}
	list := strings.Join(ids, ", ")
	if extra := len(models) - len(listed); extra > 0 {
		list += fmt.Sprintf(", and %d more", extra)
	}
	return list
}

// dispatchModelCatalog 是 appended 到 派发 工具's description; empty 当 nothing 是 usable.
func dispatchModelCatalog(cfg *config.Config, parent Model) string {
	var b strings.Builder
	for _, p := range usableProviders(cfg) {
		if models := p.UsableModels(); len(models) > 0 {
			fmt.Fprintf(&b, "\n- `%s`: %s", p.ID, modelIDList(models))
		}
	}
	if b.Len() == 0 {
		return ""
	}
	header := "\n\nModels available to `model`, by `provider`"
	if parent.Model != nil {
		header += fmt.Sprintf(" (you are running on `%s`)", modelDisplayName(parent))
	}
	return header + ":\n" + b.String()
}

// modelDisplayName names 模型 作为 提供方/模型.
func modelDisplayName(m Model) string {
	return m.ModelCfg.Provider + "/" + m.ModelCfg.Model
}

// overrideDispatchAgent resolves 派发's 模型 override 和, 当 one
// was requested, builds SessionAgent it should 运行 在. Without
// override caller keeps shared agent taskAgents built; 用 one,
// 条目 keeps dedicated agent 所以 reopens 运行 在 same 模型.
func (c *coordinator) overrideDispatchAgent(ctx context.Context, access string, params AgentDispatchParams, parentModel Model) (SessionAgent, config.SelectedModel, bool, string) {
	sel, ok, refusal := c.resolveModelOverride(params, parentModel)
	if refusal != "" || !ok {
		return nil, config.SelectedModel{}, false, refusal
	}
	agent, refusal := c.overrideTaskAgent(ctx, access, sel)
	if refusal != "" {
		return nil, sel, true, refusal
	}
	return agent, sel, true, ""
}

// overrideTaskAgent builds dedicated task 子代理 variant running 在
// overridden 模型 selection. 模型 function serves selection's
// current build, 所以 模型 refresh reaches running 子代理 在 它的 next
// turn.
func (c *coordinator) overrideTaskAgent(ctx context.Context, access string, sel config.SelectedModel) (SessionAgent, string) {
	entry, err := c.overrideModel(sel)
	if err != nil {
		return nil, "failed to set up the requested model: " + err.Error()
	}
	agentCfg, canWrite := c.taskAgentConfig(access)
	// 一个 group 的 它的 own: 这 build joins no one, 和 是 fully set up
	// before 派发 registers 子代理.
	var ready errgroup.Group
	agent, err := c.buildTaskAgent(ctx, &ready, agentCfg, canWrite, func() ModelSet {
		return ModelSet{Large: entry.current(c), Small: c.subAgentModels().Small}
	})
	if err != nil {
		return nil, "failed to start a subagent on the requested model: " + err.Error()
	}
	if err := ready.Wait(); err != nil {
		return nil, "failed to start a subagent on the requested model: " + err.Error()
	}
	return agent, ""
}

// overrideRecord 是 what transcript records 的 override: 只
// settings 调用 asked 为, 用 提供方 it resolved 到.
func overrideRecord(params AgentDispatchParams, sel config.SelectedModel) (model, provider, reasoning string) {
	if strings.TrimSpace(params.Model) != "" {
		model, provider = sel.Model, sel.Provider
	}
	return model, provider, strings.TrimSpace(params.Reasoning)
}

// sendOnNewModel moves 空闲 子代理 到 another 模型 和 reopens it 用
// 消息, keeping 它的 会话.
func (c *coordinator) sendOnNewModel(ctx context.Context, entry *dispatchedAgent, params AgentSendParams, parentModel Model) fantasy.ToolResponse {
	if entry.stopping() {
		entry.awaitSettled(10 * time.Second)
	}
	running := fmt.Sprintf("subagent %q is running; stop it with agent_stop before moving it to another model", entry.Label)
	if entry.running() {
		return fantasy.NewTextErrorResponse(running)
	}
	override := AgentDispatchParams{Model: params.Model, Provider: params.Provider, Reasoning: params.Reasoning}
	agent, sel, _, refusal := c.overrideDispatchAgent(ctx, entry.Access, override, parentModel)
	if refusal != "" {
		return fantasy.NewTextErrorResponse(refusal)
	}
	if !c.reopen(entry, params.Message, agent) {
		return fantasy.NewTextErrorResponse(running)
	}
	meta := AgentSendResponseMetadata{Label: entry.Label, Reopened: true, RunModel: modelDisplayName(agent.Model())}
	meta.Model, meta.Provider, meta.Reasoning = overrideRecord(override, sel)
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(fmt.Sprintf(
		"Moved %q to %s and delivered the message. It will answer in a new report.",
		entry.Label, meta.RunModel,
	)), meta)
}
