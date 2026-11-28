package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// overrideTestConfig 是 hermetic 配置 用 two providers: mock carries
// plain 模型, one 用 effort levels, 和 one 那 只 toggles thinking;
// other carries one 模型 的 它的 own.
const overrideTestConfig = `{
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true},
  "providers": {
    "mock": {"id": "mock", "name": "Mock", "type": "openai",
      "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
      "models": [
        {"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128},
        {"id": "mock-reasoner", "name": "Mock Reasoner", "context_window": 8192, "default_max_tokens": 128,
         "can_reason": true, "reasoning_levels": ["low", "high"], "default_reasoning_effort": "low"},
        {"id": "mock-thinker", "name": "Mock Thinker", "context_window": 8192, "default_max_tokens": 128, "can_reason": true}
      ]},
    "other": {"id": "other", "name": "Other", "type": "openai",
      "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
      "models": [{"id": "other-model", "name": "Other", "context_window": 8192, "default_max_tokens": 128}]}
  },
  "models": {"large": {"provider": "mock", "model": "mock-reasoner"},
             "small": {"provider": "mock", "model": "mock-model"}}
}`

// newOverrideTestCoordinator returns a coordinator wired the way the
// readiness tests wire one, against overrideTestConfig, 用 它的 shared
// models already built. parent 模型 是 large one.
func newOverrideTestCoordinator(t *testing.T) (*coordinator, fakeEnv) {
	t.Helper()
	env := testEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "evo.json"), []byte(overrideTestConfig), 0o644))

	cfg, err := config.Init(t.Context(), env.workingDir, "", false)
	require.NoError(t, err)
	cfg.SetupAgents()

	c := &coordinator{
		cfg:         cfg,
		sessions:    env.sessions,
		messages:    env.messages,
		permissions: env.permissions,
		history:     env.history,
		filetracker: *env.filetracker,
		dispatched:  csync.NewMap[string, *dispatchedAgent](),
	}
	require.NoError(t, c.rebuildModels(context.Background()))
	return c, env
}

func TestResolveModelOverride(t *testing.T) {
	t.Parallel()
	c, _ := newOverrideTestCoordinator(t)
	parent := c.mainModels().Large

	tests := []struct {
		name    string
		params  AgentDispatchParams
		parent  Model
		wantOK  bool
		wantSel config.SelectedModel
		// wantDynamic expects 子代理 默认 模型's selection 用
		// the override applied, rather than a fixed selection.
		wantDynamic bool
		refusal     string
	}{
		{
			name:   "no override requested",
			params: AgentDispatchParams{},
			wantOK: false,
		},
		{
			name:    "provider without model is refused",
			params:  AgentDispatchParams{Provider: "mock"},
			refusal: "provider is given without model",
		},
		{
			name:    "model only inherits the parent provider",
			params:  AgentDispatchParams{Model: "mock-model"},
			parent:  parent,
			wantOK:  true,
			wantSel: config.SelectedModel{Provider: "mock", Model: "mock-model"},
		},
		{
			name:    "model only with no parent model is refused",
			params:  AgentDispatchParams{Model: "mock-model"},
			refusal: "no model to take a provider from",
		},
		{
			name:    "model and provider are explicit",
			params:  AgentDispatchParams{Model: "other-model", Provider: "other"},
			wantOK:  true,
			wantSel: config.SelectedModel{Provider: "other", Model: "other-model"},
		},
		{
			name:    "unknown provider is refused",
			params:  AgentDispatchParams{Model: "m", Provider: "nope"},
			refusal: `provider "nope" is not available; use one of: mock, other`,
		},
		{
			name:    "unknown model is refused",
			params:  AgentDispatchParams{Model: "gone-model", Provider: "mock"},
			refusal: `model "gone-model" is not available from provider "mock"`,
		},
		{
			name:    "unsupported reasoning level is refused",
			params:  AgentDispatchParams{Model: "mock-reasoner", Reasoning: "medium"},
			parent:  parent,
			refusal: `model "mock-reasoner" takes reasoning low, high`,
		},
		{
			name:    "supported reasoning level is applied",
			params:  AgentDispatchParams{Model: "mock-reasoner", Reasoning: "high"},
			parent:  parent,
			wantOK:  true,
			wantSel: config.SelectedModel{Provider: "mock", Model: "mock-reasoner", ReasoningEffort: "high"},
		},
		{
			name:    "reasoning on a model that cannot reason is refused",
			params:  AgentDispatchParams{Model: "mock-model", Reasoning: "on"},
			parent:  parent,
			refusal: `model "mock-model" does not support reasoning`,
		},
		{
			name:    "on for a model that only toggles thinking turns it on",
			params:  AgentDispatchParams{Model: "mock-thinker", Reasoning: "on"},
			parent:  parent,
			wantOK:  true,
			wantSel: config.SelectedModel{Provider: "mock", Model: "mock-thinker", Think: true},
		},
		{
			name:    "an effort level for a model that only toggles is refused",
			params:  AgentDispatchParams{Model: "mock-thinker", Reasoning: "high"},
			parent:  parent,
			refusal: `model "mock-thinker" takes reasoning on, off`,
		},
		{
			name:    "on for a model with effort levels is refused",
			params:  AgentDispatchParams{Model: "mock-reasoner", Reasoning: "on"},
			parent:  parent,
			refusal: `model "mock-reasoner" takes reasoning low, high`,
		},
		{
			name:    "off for a model that only toggles thinking turns it off",
			params:  AgentDispatchParams{Model: "mock-thinker", Reasoning: "off"},
			parent:  parent,
			wantOK:  true,
			wantSel: config.SelectedModel{Provider: "mock", Model: "mock-thinker"},
		},
		{
			name:        "reasoning settings alone ride the subagent default model",
			params:      AgentDispatchParams{Reasoning: "high"},
			wantOK:      true,
			wantDynamic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sel, ok, refusal := c.resolveModelOverride(tt.params, tt.parent)
			if tt.refusal != "" {
				require.NotEmpty(t, refusal)
				require.Contains(t, refusal, tt.refusal)
				return
			}
			require.Empty(t, refusal)
			require.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			if tt.wantDynamic {
				// sub-默认 case keeps 默认 模型's other settings.
				want := c.subAgentModels().Large.ModelCfg
				want.ReasoningEffort = "high"
				require.Equal(t, want, sel)
				return
			}
			require.Equal(t, tt.wantSel, sel)
		})
	}
}

// Reasoning settings alone ride 子代理's 默认 模型, 所以 they 是
// refused 当 那 模型 cannot reason.
func TestResolveModelOverrideRidesNonReasoningSubDefault(t *testing.T) {
	t.Parallel()
	c, _ := newOverrideTestCoordinator(t)

	small := c.subAgentModels().Small
	c.models.Set(coordinatorModels{large: small, subLarge: small, small: small})

	_, ok, refusal := c.resolveModelOverride(AgentDispatchParams{Reasoning: "on"}, Model{})
	require.True(t, ok)
	require.Contains(t, refusal, "does not support reasoning")
}

func TestOverrideDispatchAgent(t *testing.T) {
	t.Parallel()
	c, _ := newOverrideTestCoordinator(t)
	ctx := t.Context()
	parent := c.mainModels().Large

	agents, err := c.taskAgents(ctx, false)
	require.NoError(t, err)
	require.Len(t, agents, 2, "the read and write variants are built")

	agent, sel, ok, refusal := c.overrideDispatchAgent(ctx, accessRead, AgentDispatchParams{
		Model:    "other-model",
		Provider: "other",
	}, parent)
	require.Empty(t, refusal)
	require.True(t, ok)
	require.Equal(t, config.SelectedModel{Provider: "other", Model: "other-model"}, sel)
	require.NotNil(t, agent)
	require.Equal(t, "other-model", agent.Model().ModelCfg.Model)
	require.Equal(t, "other", agent.Model().ModelCfg.Provider)

	// override 模型 是 shared, 所以 second 派发 在 same
	// selection reuses build instead 的 rebuilding 提供方.
	_, _, _, refusal = c.overrideDispatchAgent(ctx, accessRead, AgentDispatchParams{
		Model:    "other-model",
		Provider: "other",
	}, parent)
	require.Empty(t, refusal)
	require.Len(t, c.overrideModels, 1)

	_, _, _, refusal = c.overrideDispatchAgent(ctx, accessRead, AgentDispatchParams{
		Provider: "other",
	}, parent)
	require.Contains(t, refusal, "provider is given without model")
}

// 一个 rebuild 的 shared models reaches override 在 它的 next use.
func TestOverrideModelRefreshedOnRebuild(t *testing.T) {
	t.Parallel()
	c, _ := newOverrideTestCoordinator(t)
	ctx := t.Context()

	entry, err := c.overrideModel(config.SelectedModel{Provider: "other", Model: "other-model"})
	require.NoError(t, err)
	first := entry.current(c)
	require.Equal(t, "other-model", first.ModelCfg.Model)
	require.Equal(t, fmt.Sprintf("%p", first.Model), fmt.Sprintf("%p", entry.current(c).Model), "no rebuild without a refresh")

	require.NoError(t, c.rebuildModels(ctx))
	second := entry.current(c)
	require.Equal(t, "other-model", second.ModelCfg.Model)
	require.NotEqual(t, fmt.Sprintf("%p", first.Model), fmt.Sprintf("%p", second.Model),
		"a rebuild must produce a fresh build, not the pre-rebuild one")
}

// 一个 selection 那 fails 到 build 是 不 cached.
func TestOverrideModelFailureIsNotCached(t *testing.T) {
	t.Parallel()
	c, _ := newOverrideTestCoordinator(t)

	_, err := c.overrideModel(config.SelectedModel{Provider: "nope", Model: "x"})
	require.Error(t, err)
	require.Empty(t, c.overrideModels)
}

func dispatchOverrideCall(t *testing.T, id, label, access, model, provider string) message.ToolCall {
	t.Helper()
	input, err := json.Marshal(AgentDispatchParams{
		Prompt:   "do " + label,
		Label:    label,
		Access:   access,
		Model:    model,
		Provider: provider,
	})
	require.NoError(t, err)
	return message.ToolCall{ID: id, Name: AgentDispatchToolName, Input: string(input), Finished: true}
}

func TestDispatchHistoryRecordsModelOverride(t *testing.T) {
	t.Parallel()

	// c1 派发 asked 为 模型 只; 它的 结果 carries
	// 提供方 那 was resolved 为 it.
	startedMeta, err := json.Marshal(AgentDispatchResponseMetadata{
		Label:    "a",
		Model:    "other-model",
		Provider: "mock",
	})
	require.NoError(t, err)

	msgs := []message.Message{
		{ID: "m1", Role: message.Assistant, Parts: []message.ContentPart{
			dispatchOverrideCall(t, "c1", "a", "", "other-model", ""),
			dispatchCall(t, "c2", "b", ""),
		}},
		{ID: "t1", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "c1", Name: AgentDispatchToolName, Content: "started", Metadata: string(startedMeta)},
			message.ToolResult{ToolCallID: "c2", Name: AgentDispatchToolName, Content: "started"},
		}},
	}

	records := dispatchHistory(msgs)
	a := records.byLabel["a"]
	require.Equal(t, "other-model", a.Model)
	require.Equal(t, "mock", a.Provider, "the resolved provider is recorded, not re-derived")
	b := records.byLabel["b"]
	require.Empty(t, b.Model)
	require.Empty(t, b.Provider)
	require.Empty(t, b.Reasoning)
}

func TestDispatchToolRunsOverrideModel(t *testing.T) {
	t.Parallel()
	c, env := newOverrideTestCoordinator(t)
	ctx := t.Context()

	// 一个 parent 模型 gives 模型-只 override 提供方 到 inherit.
	agents, err := c.taskAgents(ctx, false)
	require.NoError(t, err)

	tool, err := c.agentDispatchTool(ctx, config.AgentCoder, agents[accessRead].Model)
	require.NoError(t, err)

	parent, err := env.sessions.Create(ctx, "parent")
	require.NoError(t, err)

	newCall := func(id, input string) (context.Context, fantasy.ToolCall) {
		assistant, err := env.messages.Create(ctx, parent.ID, message.CreateMessageParams{
			Role:  message.Assistant,
			Parts: []message.ContentPart{message.TextContent{Text: "dispatching"}},
		})
		require.NoError(t, err)
		callCtx := context.WithValue(ctx, tools.SessionIDContextKey, parent.ID)
		callCtx = context.WithValue(callCtx, tools.MessageIDContextKey, assistant.ID)
		return callCtx, fantasy.ToolCall{ID: id, Name: AgentDispatchToolName, Input: input}
	}

	// 一个 refused override leaves label free 为 重试.
	callCtx, call := newCall("c1", `{"prompt":"do","label":"ov","provider":"other"}`)
	resp, err := tool.Run(callCtx, call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "provider is given without model")

	callCtx, call = newCall("c2", `{"prompt":"do","label":"ov","model":"gone-model"}`)
	resp, err = tool.Run(callCtx, call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, `model "gone-model" is not available from provider "mock"`)

	callCtx, call = newCall("c3", `{"prompt":"do","label":"ov","model":"mock-model"}`)
	resp, err = tool.Run(callCtx, call)
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)

	// 结果 records resolved override, 所以 restart rehydrates 在
	// 提供方 派发 actually used.
	var meta AgentDispatchResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	ovName := meta.Label
	require.True(t, strings.HasSuffix(ovName, "-ov"), "a dispatch is given a first name: %s", ovName)
	require.Equal(t, "mock-model", meta.Model)
	require.Equal(t, "mock", meta.Provider, "a model-only dispatch records the inherited provider")
	require.Empty(t, meta.Reasoning)

	callCtx, call = newCall("c4", `{"prompt":"do","label":"thunk","reasoning":"high"}`)
	resp, err = tool.Run(callCtx, call)
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	meta = AgentDispatchResponseMetadata{}
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	thunkName := meta.Label
	require.NotEqual(t, strings.Split(ovName, "-")[0], strings.Split(thunkName, "-")[0], "two dispatches in a row get different names")
	require.Empty(t, meta.Model, "reasoning alone records no model, so a resume rides the default")
	require.Equal(t, "high", meta.Reasoning, "only the settings the call asked for are recorded")
	entry, ok := c.dispatched.Get(dispatchKey(parent.ID, thunkName))
	require.True(t, ok)
	require.Equal(t, "high", entry.agent.Model().ModelCfg.ReasoningEffort)
	entry.stop("test done")
	require.True(t, entry.awaitSettled(10*time.Second))

	entry, ok = c.dispatched.Get(dispatchKey(parent.ID, ovName))
	require.True(t, ok)
	require.Equal(t, "mock-model", entry.agent.Model().ModelCfg.Model)
	require.Equal(t, "mock", entry.agent.Model().ModelCfg.Provider)

	entry.stop("test done")
	require.True(t, entry.awaitSettled(10*time.Second))
}

func TestRehydrateDispatchRestoresModelOverride(t *testing.T) {
	t.Parallel()
	c, env := newOverrideTestCoordinator(t)
	ctx := t.Context()

	parent, err := env.sessions.Create(ctx, "parent")
	require.NoError(t, err)

	// c3 asked 为 模型 只; 它的 recorded 结果 carries 提供方
	// 派发 resolved, 所以 rehydration needs no current parent 模型.
	monoMeta, err := json.Marshal(AgentDispatchResponseMetadata{
		Label:    "mono",
		Model:    "mock-model",
		Provider: "mock",
	})
	require.NoError(t, err)

	assistant, err := env.messages.Create(ctx, parent.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			dispatchOverrideCall(t, "c1", "ov", "", "other-model", "other"),
			dispatchOverrideCall(t, "c2", "gone", "", "gone-model", "other"),
			dispatchOverrideCall(t, "c3", "mono", "", "mock-model", ""),
		},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, parent.ID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "c1", Name: AgentDispatchToolName, Content: "started"},
			message.ToolResult{ToolCallID: "c3", Name: AgentDispatchToolName, Content: "started", Metadata: string(monoMeta)},
		},
	})
	require.NoError(t, err)

	// c4 "crash" was 模型-只 派发 whose 结果 never landed;
	// child transcript records what it ran 在. c5 "orphan" never got
	// 结果 或 first turn, 所以 只 parent 会话 has now can
	// supply 提供方. c6 "thunk" gave reasoning settings 只, 所以 it
	// rides 默认 selection rather than explicit 模型.
	thunkInput, err := json.Marshal(AgentDispatchParams{Prompt: "do thunk", Label: "thunk", Reasoning: "high"})
	require.NoError(t, err)
	thunkMeta, err := json.Marshal(AgentDispatchResponseMetadata{Label: "thunk", Reasoning: "high"})
	require.NoError(t, err)

	firstAssistant := assistant
	assistant, err = env.messages.Create(ctx, parent.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			dispatchOverrideCall(t, "c4", "crash", "", "mock-model", ""),
			dispatchOverrideCall(t, "c5", "orphan", "", "other-model", ""),
			message.ToolCall{ID: "c6", Name: AgentDispatchToolName, Input: string(thunkInput), Finished: true},
		},
	})
	require.NoError(t, err)

	// parent 会话 has now runs 在 "other", 所以 parent fallback
	// would resolve "crash" onto wrong 提供方 和 fail.
	c.mainAgent = &mockSessionAgent{model: Model{
		Model:    c.mainModels().Large.Model,
		ModelCfg: config.SelectedModel{Provider: "other", Model: "other-model"},
	}}

	for _, callID := range []string{"c1", "c2", "c3"} {
		childID := env.sessions.CreateAgentToolSessionID(firstAssistant.ID, callID)
		_, err := env.sessions.CreateTaskSession(ctx, childID, parent.ID, "Subagent")
		require.NoError(t, err)
	}
	for _, callID := range []string{"c4", "c5", "c6"} {
		childID := env.sessions.CreateAgentToolSessionID(assistant.ID, callID)
		_, err := env.sessions.CreateTaskSession(ctx, childID, parent.ID, "Subagent")
		require.NoError(t, err)
	}
	_, err = env.messages.Create(ctx, parent.ID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "c6", Name: AgentDispatchToolName, Content: "started", Metadata: string(thunkMeta)},
		},
	})
	require.NoError(t, err)

	crashChild := env.sessions.CreateAgentToolSessionID(assistant.ID, "c4")
	_, err = env.messages.Create(ctx, crashChild, message.CreateMessageParams{
		Role:     message.Assistant,
		Parts:    []message.ContentPart{message.TextContent{Text: "working"}},
		Model:    "mock-model",
		Provider: "mock",
	})
	require.NoError(t, err)

	entry, err := c.rehydrateDispatch(ctx, parent.ID, "ov")
	require.NoError(t, err)
	require.Equal(t, "other-model", entry.agent.Model().ModelCfg.Model)
	require.Equal(t, "other", entry.agent.Model().ModelCfg.Provider)

	// metadata records resolved 提供方, 所以 parent running 在
	// "other" 通过 now does 不 matter.
	entry, err = c.rehydrateDispatch(ctx, parent.ID, "mono")
	require.NoError(t, err)
	require.Equal(t, "mock-model", entry.agent.Model().ModelCfg.Model)
	require.Equal(t, "mock", entry.agent.Model().ModelCfg.Provider)

	// 一个 派发 whose 结果 never landed recovers 模型 和 提供方
	// 它的 child 会话 actually ran 在, 不 提供方 parent has
	// now.
	entry, err = c.rehydrateDispatch(ctx, parent.ID, "crash")
	require.NoError(t, err)
	require.Equal(t, "mock-model", entry.agent.Model().ModelCfg.Model)
	require.Equal(t, "mock", entry.agent.Model().ModelCfg.Provider)

	// 一个 派发 那 never ran turn falls back 到 parent's 提供方.
	entry, err = c.rehydrateDispatch(ctx, parent.ID, "orphan")
	require.NoError(t, err)
	require.Equal(t, "other-model", entry.agent.Model().ModelCfg.Model)
	require.Equal(t, "other", entry.agent.Model().ModelCfg.Provider)

	// 一个 reasoning override rides 默认 selection, keeping 它的
	// configured settings, instead 的 rebuilding bare selection 从
	// recorded 模型.
	entry, err = c.rehydrateDispatch(ctx, parent.ID, "thunk")
	require.NoError(t, err)
	want := c.subAgentModels().Large.ModelCfg
	want.ReasoningEffort = "high"
	require.Equal(t, want, entry.agent.Model().ModelCfg, "a reasoning override keeps the default selection's settings")

	// 一个 派发 whose 模型 no longer resolves 是 不 silently resumed 在
	// 会话 默认.
	_, err = c.rehydrateDispatch(ctx, parent.ID, "gone")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be resumed")
}

// listFailingMessages fails every 会话 listing except parent's, 到
// simulate transient 读取 failure while child transcript 是 consulted.
type listFailingMessages struct {
	message.Service
	except string
}

func (f listFailingMessages) List(ctx context.Context, sessionID string) ([]message.Message, error) {
	if sessionID != f.except {
		return nil, errors.New("transient read failure")
	}
	return f.Service.List(ctx, sessionID)
}

// 一个 failing transcript 读取 must 不 是 mistaken 为 child 那 never
// ran, which would let rehydration fall back 到 wrong 提供方.
func TestRehydrateDispatchPropagatesTranscriptReadError(t *testing.T) {
	t.Parallel()
	c, env := newOverrideTestCoordinator(t)
	ctx := t.Context()

	parent, err := env.sessions.Create(ctx, "parent")
	require.NoError(t, err)

	assistant, err := env.messages.Create(ctx, parent.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			dispatchOverrideCall(t, "c1", "ov", "", "other-model", ""),
		},
	})
	require.NoError(t, err)
	childID := env.sessions.CreateAgentToolSessionID(assistant.ID, "c1")
	_, err = env.sessions.CreateTaskSession(ctx, childID, parent.ID, "Subagent")
	require.NoError(t, err)

	c.messages = listFailingMessages{Service: env.messages, except: parent.ID}
	_, err = c.rehydrateDispatch(ctx, parent.ID, "ov")
	require.Error(t, err)
	require.Contains(t, err.Error(), "transient read failure")
}

// 一个 disabled 提供方 是 refused even 当 模型 it names exists, 和
// it drops out 的 catalog 工具 description lists.
func TestModelOverrideSkipsDisabledProvider(t *testing.T) {
	t.Parallel()
	c, _ := newOverrideTestCoordinator(t)
	cfg := c.cfg.Config()

	other, ok := cfg.Providers.Get("other")
	require.True(t, ok)
	other.Disable = true
	cfg.Providers.Set("other", other)

	_, _, refusal := c.resolveModelOverride(AgentDispatchParams{Model: "other-model", Provider: "other"}, Model{})
	require.Equal(t, `provider "other" is not available; use one of: mock`, refusal)

	catalog := dispatchModelCatalog(cfg, Model{})
	require.NotContains(t, catalog, "other")
	require.Contains(t, catalog, "- `mock`: mock-model, mock-reasoner [low|high], mock-thinker [on|off]")
}

// catalog names parent's 模型, lists every usable 提供方 在
// stable order, 和 是 what unknown 模型's 拒绝 points back 到.
func TestDispatchModelCatalog(t *testing.T) {
	t.Parallel()
	c, _ := newOverrideTestCoordinator(t)
	cfg := c.cfg.Config()

	catalog := dispatchModelCatalog(cfg, c.mainModels().Large)
	require.Equal(t, "\n\nModels available to `model`, by `provider` (you are running on `mock/mock-reasoner`):\n"+
		"\n- `mock`: mock-model, mock-reasoner [low|high], mock-thinker [on|off]"+
		"\n- `other`: other-model", catalog)

	_, _, refusal := c.resolveModelOverride(AgentDispatchParams{Model: "gone-model", Provider: "mock"}, Model{})
	require.Equal(t, `model "gone-model" is not available from provider "mock"; use one of: mock-model, mock-reasoner [low|high], mock-thinker [on|off]`, refusal)
}

// Nothing usable means no catalog 在 all, rather than empty heading.
func TestDispatchModelCatalogEmpty(t *testing.T) {
	t.Parallel()
	c, _ := newOverrideTestCoordinator(t)
	cfg := c.cfg.Config()
	for _, id := range []string{"mock", "other"} {
		p, ok := cfg.Providers.Get(id)
		require.True(t, ok)
		p.Disable = true
		cfg.Providers.Set(id, p)
	}
	require.Empty(t, dispatchModelCatalog(cfg, Model{}))
}

func TestModelIDListCapsLongCatalogs(t *testing.T) {
	t.Parallel()
	models := make([]catwalk.Model, catalogModelsPerProvider+3)
	for i := range models {
		models[i].ID = fmt.Sprintf("m%d", i)
	}
	list := modelIDList(models)
	require.Contains(t, list, "m0, m1")
	require.NotContains(t, list, fmt.Sprintf("m%d", catalogModelsPerProvider))
	require.True(t, strings.HasSuffix(list, ", and 3 more"), list)
}

// 一个 refused 子代理 can 是 moved 到 another 模型 通过 agent_send, keeping
// 它的 会话, 和 restart resumes it there.
func TestAgentSendMovesSubagentToAnotherModel(t *testing.T) {
	t.Parallel()
	c, env := newOverrideTestCoordinator(t)
	ctx := t.Context()
	agents, err := c.taskAgents(ctx, false)
	require.NoError(t, err)
	dispatch, err := c.agentDispatchTool(ctx, config.AgentCoder, agents[accessRead].Model)
	require.NoError(t, err)
	send := c.agentSendTool(agents[accessRead].Model)

	parent, err := env.sessions.Create(ctx, "parent")
	require.NoError(t, err)
	assistant, err := env.messages.Create(ctx, parent.ID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "dispatching"}},
	})
	require.NoError(t, err)
	callCtx := context.WithValue(ctx, tools.SessionIDContextKey, parent.ID)
	callCtx = context.WithValue(callCtx, tools.MessageIDContextKey, assistant.ID)

	resp, err := dispatch.Run(callCtx, fantasy.ToolCall{ID: "d1", Name: AgentDispatchToolName, Input: `{"prompt":"do","label":"ir","model":"mock-model"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	var dmeta AgentDispatchResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &dmeta))
	entry, ok := c.dispatched.Get(dispatchKey(parent.ID, dmeta.Label))
	require.True(t, ok)
	sessionID := entry.SessionID

	input := func(extra string) string {
		return `{"label":"` + dmeta.Label + `","message":"carry on"` + extra + `}`
	}
	sendCall := func(extra string) fantasy.ToolResponse {
		resp, err := send.Run(callCtx, fantasy.ToolCall{ID: "s", Name: AgentSendToolName, Input: input(extra)})
		require.NoError(t, err)
		return resp
	}

	// Not switched while it 是 running.
	require.True(t, entry.running())
	resp = sendCall(`,"model":"other-model","provider":"other"`)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "agent_stop")

	entry.stop("refused")
	require.True(t, entry.awaitSettled(10*time.Second))

	// 一个 模型 那 cannot 是 served 是 refused 和 changes nothing.
	resp = sendCall(`,"model":"gone-model"`)
	require.True(t, resp.IsError)
	require.Equal(t, "mock-model", entry.runner().Model().ModelCfg.Model)

	resp = sendCall(`,"model":"other-model","provider":"other"`)
	require.False(t, resp.IsError, resp.Content)
	var smeta AgentSendResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &smeta))
	require.True(t, smeta.Reopened)
	require.Equal(t, "other-model", smeta.Model)
	require.Equal(t, "other", smeta.Provider)

	entry, ok = c.dispatched.Get(dispatchKey(parent.ID, dmeta.Label))
	require.True(t, ok)
	require.Equal(t, sessionID, entry.SessionID, "the subagent keeps its session")
	require.Equal(t, "other-model", entry.runner().Model().ModelCfg.Model)
	require.Equal(t, "other", entry.runner().Model().ModelCfg.Provider)
	entry.stop("test done")
	require.True(t, entry.awaitSettled(10*time.Second))

	// transcript records switch, 所以 restart resumes 在 it.
	records := dispatchHistory([]message.Message{
		{ID: assistant.ID, Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "d1", Name: AgentDispatchToolName, Input: `{"prompt":"do","label":"ir","model":"mock-model"}`, Finished: true},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "d1", Name: AgentDispatchToolName, Metadata: mustJSON(t, dmeta)},
			message.ToolResult{ToolCallID: "s", Name: AgentSendToolName, Metadata: mustJSON(t, smeta)},
		}},
	})
	record := records.byLabel[dmeta.Label]
	require.NotNil(t, record)
	require.Equal(t, "other-model", record.Model)
	require.Equal(t, "other", record.Provider)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
