// shared_models.go —— coordinator 共享的模型集合与覆盖模型管理。

package agent

import (
	"context"
	"log/slog"
	"sync"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
)

// ModelSet 是 large 和 small 模型 agent sends requests through.
type ModelSet struct {
	Large, Small Model
}

// coordinatorModels 是 every 模型 coordinator's agents share. On Copilot,
// subagents get their own large 模型 所以 it bills them 作为 agent-initiated;
// elsewhere subLarge 是 large. small 模型 是 always built 那 way.
type coordinatorModels struct {
	large, subLarge, small Model
}

// rebuildModels rebuilds every shared 模型 从 live 配置 和
// publishes them together. Rebuilds 是 serialized, 所以 one 那 读取 older
// 配置 can never 发布 over one 那 读取 newer credential.
func (c *coordinator) rebuildModels(ctx context.Context) error {
	c.rebuildMu.Lock()
	defer c.rebuildMu.Unlock()

	large, err := c.buildModel(ctx, config.SelectedModelTypeLarge, false)
	if err != nil {
		return err
	}
	subLarge := large
	if large.ModelCfg.Provider == string(catwalk.InferenceProviderCopilot) {
		subLarge, err = c.buildModel(ctx, config.SelectedModelTypeLarge, true)
		if err != nil {
			return err
		}
	}
	small, err := c.buildModel(ctx, config.SelectedModelTypeSmall, true)
	if err != nil {
		return err
	}
	c.models.Set(coordinatorModels{large: large, subLarge: subLarge, small: small})

	// Override models rebuild lazily 在 their next use.
	c.modelsGen.Add(1)
	return nil
}

// overrideModelKey identifies per-派发 override selection.
type overrideModelKey struct {
	provider string
	model    string
	think    bool
	effort   string
}

// overrideModel 是 shared build 的 one override selection. 它 是 built
// 作为 子代理 模型, 所以 Copilot bills it 作为 agent-initiated.
type overrideModel struct {
	sel config.SelectedModel

	mu    sync.Mutex
	model Model
	gen   uint64 // c.modelsGen the build was made under.
}

// current returns build, rebuilding it first 如果 shared models were
// rebuilt since. 一个 failed rebuild keeps old build rather than moving
// running 子代理 onto 会话 默认.
func (o *overrideModel) current(c *coordinator) Model {
	model, _ := o.refresh(c)
	return model
}

func (o *overrideModel) refresh(c *coordinator) (Model, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	gen := c.modelsGen.Load()
	if o.gen == gen {
		return o.model, nil
	}
	model, err := c.buildModelFromSelection(context.Background(), o.sel, true)
	if err != nil {
		if o.model.Model != nil {
			slog.Warn("Failed to refresh an overridden subagent model",
				"model", o.sel.Model, "provider", o.sel.Provider, "error", err)
		}
		return o.model, err
	}
	o.model, o.gen = model, gen
	return o.model, nil
}

// overrideModel returns shared, built 模型 为 selection. 一个 selection
// 那 fails 它的 first build 是 不 cached, 所以 later 派发 tries afresh.
func (c *coordinator) overrideModel(sel config.SelectedModel) (*overrideModel, error) {
	key := overrideModelKey{provider: sel.Provider, model: sel.Model, think: sel.Think, effort: sel.ReasoningEffort}
	c.overrideModelsMu.Lock()
	if c.overrideModels == nil {
		c.overrideModels = make(map[overrideModelKey]*overrideModel)
	}
	entry, ok := c.overrideModels[key]
	if !ok {
		entry = &overrideModel{sel: sel, gen: ^uint64(0)}
		c.overrideModels[key] = entry
	}
	c.overrideModelsMu.Unlock()

	if model, err := entry.refresh(c); model.Model == nil {
		c.overrideModelsMu.Lock()
		if c.overrideModels[key] == entry {
			delete(c.overrideModels, key)
		}
		c.overrideModelsMu.Unlock()
		return nil, err
	}
	return entry, nil
}

func (c *coordinator) mainModels() ModelSet {
	m := c.models.Get()
	return ModelSet{Large: m.large, Small: m.small}
}

func (c *coordinator) subAgentModels() ModelSet {
	m := c.models.Get()
	return ModelSet{Large: m.subLarge, Small: m.small}
}

// smallOnlyModels serves agents 那 use small 模型 为 everything.
func (c *coordinator) smallOnlyModels() ModelSet {
	m := c.models.Get()
	return ModelSet{Large: m.small, Small: m.small}
}

// currentBuildOf returns latest build 的 模型 turn started 用, 所以
// 重试 gets refreshed credential 但 模型 switch waits 为 next
// turn.
func (a *sessionAgent) currentBuildOf(started Model) fantasy.LanguageModel {
	m := a.models().Large
	if m.ModelCfg.Provider != started.ModelCfg.Provider || m.ModelCfg.Model != started.ModelCfg.Model {
		return started.Model
	}
	return m.Model
}
