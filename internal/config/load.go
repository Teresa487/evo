// load.go —— 配置的发现、读取、合并与 provider 解析，是启动链的第一站。
//
// 从产物反推（谁最终让模型能说话）：
//
//	Process 启动 → config.Load(workingDir, dataDir, debug)
//	  → lookupConfigs / loadFromConfigPaths    找到全局 + 项目的配置文件
//	  → loadFromBytes                          按优先级合并多份配置
//	  → setDefaults / NormalizeOptions         补默认值与规范化
//	  → configureProviders                     解析 provider、API key、base_url、OAuth
//	  → defaultModelSelection / resolveSelectedModels  选定 large/small 模型
//	  → 得到 *ConfigStore，交由 app.App 构造 coordinator 和 agent
//
// 本文件决定「用哪个 provider、哪个 model、从哪读 key」。

package config

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/discover"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/shellconfig"
	powernapConfig "github.com/charmbracelet/x/powernap/pkg/config"
	"github.com/qjebbs/go-jsons"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const defaultCatwalkURL = "https://catwalk.charm.land"

// legacyProviderTypes maps 提供方 type values 从 catwalk v2
// schema onto v3 endpoint types 那 describe same API format, 所以
// configs written before migration keep working. Custom 提供方
// types 用 registered enrichers (ollama, litellm, ...) 是 不 listed
// here 和 pass through unchanged.
var legacyProviderTypes = map[catwalk.Type]catwalk.Type{
	"openai":        catwalk.TypeResponses,
	"openai-compat": catwalk.TypeCompletions,
	"openrouter":    catwalk.TypeCompletions,
	"vercel":        catwalk.TypeCompletions,
	"anthropic":     catwalk.TypeMessages,
	"azure":         catwalk.TypeResponses,
	"bedrock":       catwalk.TypeMessages,
	"google":        catwalk.TypeCompletions,
	"google-vertex": catwalk.TypeCompletions,
}

// NormalizeProviderType maps catwalk v2 提供方 type onto v3
// endpoint type describing same API format. Types 那 是 already
// v3 或 是 不 legacy names pass through unchanged. catwalk's v3 data
// still carries v2 names 为 some providers (OpenAI, MiniMax, ...).
func NormalizeProviderType(t catwalk.Type) catwalk.Type {
	if mapped, ok := legacyProviderTypes[t]; ok {
		return mapped
	}
	return t
}

// legacyModelPricingFields maps catwalk v2 模型 cost fields onto their
// v3 pricing counterparts.
var legacyModelPricingFields = map[string]string{
	"cost_per_1m_in":         "input",
	"cost_per_1m_out":        "output",
	"cost_per_1m_in_cached":  "cache_create",
	"cost_per_1m_out_cached": "cache_hit",
}

// migrateCatwalkV2 rewrites merged 用户 配置 从 catwalk v2
// schema onto v3 schema 所以 pre-migration configs decode correctly.
// In v3, flat 模型 fields moved into nested structures: cost
// fields became pricing, supports_attachments became
// capabilities.vision, 和 can_reason/reasoning_levels/
// default_reasoning_effort trio became the reasoning object. The
// rewrite 是 pure 和 是 no-op 为 configs already written 用 v3
// fields; use migrateCatwalkV2File 到 persist 结果.
func migrateCatwalkV2(data []byte) ([]byte, bool) {
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return data, false
	}
	providers, ok := root["providers"].(map[string]any)
	if !ok {
		return data, false
	}

	changed := false
	for _, raw := range providers {
		provider, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"models", "chatgpt_models"} {
			models, ok := provider[key].([]any)
			if !ok {
				continue
			}
			for i, rawModel := range models {
				model, ok := rawModel.(map[string]any)
				if !ok {
					continue
				}
				if migrateCatwalkV2Model(model) {
					models[i] = model
					changed = true
				}
			}
		}
	}
	if !changed {
		return data, false
	}

	out, err := json.Marshal(root)
	if err != nil {
		return data, false
	}
	return out, true
}

// migrateCatwalkV2File migrates JSON 配置 文件 从 catwalk v2
// 模型 schema onto v3 和 persists rewrite atomically, preserving
// 文件's permissions. returned bytes 是 always migrated
// form, 所以 loading continues even 当 写入 fails.
func migrateCatwalkV2File(path string, data []byte) []byte {
	migrated, changed := migrateCatwalkV2(data)
	if !changed {
		return data
	}
	perm := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	if err := atomicWriteFile(path, migrated, perm); err != nil {
		slog.Warn("Failed to write migrated config", "path", path, "error", err)
	} else {
		slog.Info("Migrated config file to the catwalk v3 model schema", "path", path)
	}
	return migrated
}

// migrateCatwalkV2Model migrates single 模型 object 在 place 和
// reports whether anything changed. Legacy fields 是 removed after
// being folded into their v3 counterparts 所以 they never leak back into
// decoded 配置.
func migrateCatwalkV2Model(model map[string]any) bool {
	changed := false

	if _, ok := model["pricing"]; !ok {
		pricing := map[string]any{}
		for legacy, field := range legacyModelPricingFields {
			value, ok := model[legacy]
			if !ok {
				continue
			}
			pricing[field] = value
			delete(model, legacy)
			changed = true
		}
		if len(pricing) > 0 {
			model["pricing"] = pricing
		}
	} else {
		for legacy := range legacyModelPricingFields {
			if _, ok := model[legacy]; ok {
				delete(model, legacy)
				changed = true
			}
		}
	}

	if value, ok := model["supports_attachments"]; ok {
		delete(model, "supports_attachments")
		capabilities, ok := model["capabilities"].(map[string]any)
		if !ok {
			capabilities = map[string]any{}
			model["capabilities"] = capabilities
		}
		if _, ok := capabilities["vision"]; !ok {
			capabilities["vision"] = value
		}
		changed = true
	}

	canReason, hasCanReason := model["can_reason"]
	levels, hasLevels := model["reasoning_levels"]
	defaultEffort, hasDefaultEffort := model["default_reasoning_effort"]
	for _, legacy := range []string{"can_reason", "reasoning_levels", "default_reasoning_effort"} {
		if _, ok := model[legacy]; ok {
			delete(model, legacy)
			changed = true
		}
	}
	if _, ok := model["reasoning"]; !ok && (hasCanReason || hasLevels || hasDefaultEffort) {
		reasoning := map[string]any{}
		if can, ok := canReason.(bool); ok && !can {
			reasoning["thinking"] = catwalk.ThinkingNever
		} else {
			reasoning["thinking"] = catwalk.ThinkingToggleable
			if raw, ok := levels.([]any); ok && len(raw) > 0 {
				values := make([]string, 0, len(raw))
				for _, level := range raw {
					if value, ok := level.(string); ok {
						values = append(values, value)
					}
				}
				if len(values) > 0 {
					effortLevels := make([]any, 0, len(values))
					for _, level := range catwalk.NewEffortLevels(values...) {
						effortLevels = append(effortLevels, map[string]any{
							"value":   level.Value,
							"display": level.Display,
						})
					}
					reasoning["effort_levels"] = effortLevels
					if effort, ok := defaultEffort.(string); ok && effort != "" {
						reasoning["default_effort_level"] = effort
					}
				}
			}
		}
		model["reasoning"] = reasoning
	}

	return changed
}

// Load loads configuration 从 默认 paths 和 returns
// ConfigStore 那 owns both pure-data 配置 和 all runtime state.
func Load(ctx context.Context, workingDir, dataDir string, debug bool) (*ConfigStore, error) {
	// CRUSH_DATA_DIR env var overrides per-project data 目录
	// 当 no --data-dir flag 是 given. Relative paths resolve against
	// working 目录, like data_directory 配置 option.
	dataDir = cmp.Or(dataDir, os.Getenv("CRUSH_DATA_DIR"))

	// Migrate deprecated disable_notifications before loading 配置.
	migrateDisableNotifications()

	configPaths := lookupConfigs(workingDir)

	cfg, loadedPaths, err := loadFromConfigPaths(ctx, configPaths)
	if err != nil {
		return nil, fmt.Errorf("failed to load config from paths %v: %w", configPaths, err)
	}

	cfg.setDefaults(workingDir, dataDir)

	store := &ConfigStore{
		config:         cfg,
		workingDir:     workingDir,
		globalDataPath: GlobalConfigData(),
		workspacePath:  filepath.Join(cfg.Options.DataDirectory, fmt.Sprintf("%s.json", appName)),
		loadedPaths:    loadedPaths,
	}

	if debug {
		cfg.Options.Debug = true
	}

	// Load 工作区 配置 last 所以 it has highest priority.
	if wsData, err := readFile(store.workspacePath); err == nil && len(wsData) > 0 {
		if !json.Valid(wsData) {
			return nil, fmt.Errorf("invalid JSON in config file %s", store.workspacePath)
		}
		wsData = migrateCatwalkV2File(store.workspacePath, wsData)
		merged, mergeErr := loadFromBytes(append([][]byte{mustMarshalConfig(cfg)}, wsData))
		if mergeErr == nil {
			// Preserve defaults 那 setDefaults already applied.
			dataDir := cfg.Options.DataDirectory
			*cfg = *merged
			cfg.setDefaults(workingDir, dataDir)
			store.config = cfg
			store.loadedPaths = append(store.loadedPaths, store.workspacePath)
		}
	}

	// Validate hooks after all 配置 merging 是 完成 所以 工作区
	// hooks also get their matcher regexes compiled.
	if err := cfg.ValidateHooks(); err != nil {
		return nil, fmt.Errorf("invalid hook configuration: %w", err)
	}

	if err := cfg.ValidatePrompts(); err != nil {
		return nil, fmt.Errorf("invalid prompt configuration: %w", err)
	}

	if err := cfg.ValidateCoreutils(); err != nil {
		return nil, fmt.Errorf("invalid coreutils configuration: %w", err)
	}

	if !isInsideWorktree(workingDir) {
		const depth = 2
		const items = 100
		slog.Warn("No git repository detected in working directory, will limit file walk operations", "depth", depth, "items", items)
		assignIfNil(&cfg.Tools.Ls.MaxDepth, depth)
		assignIfNil(&cfg.Tools.Ls.MaxItems, items)
		assignIfNil(&cfg.Options.TUI.Completions.MaxDepth, depth)
		assignIfNil(&cfg.Options.TUI.Completions.MaxItems, items)
	}

	if isAppleTerminal() {
		slog.Warn("Detected Apple Terminal, enabling transparent mode")
		assignIfNil(&cfg.Options.TUI.Transparent, true)
	}

	// 不使用内置/远程 provider 目录，加载结果恒为空；provider 一律来自
	// 用户在配置文件里的手写定义。
	providers, err := LoadProviders(ctx, cfg, func(ctx context.Context) error {
		return store.RefreshOAuthToken(ctx, ScopeGlobal, "hyper")
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, err
	}
	store.knownProviders = providers

	// When Catwalk refreshed 它的 catalog 这 运行, give 模型
	// catalogs 那 shadow Catwalk's same treatment: ChatGPT 和
	// Grok catalogs 是 otherwise 只 fetched 在 login 和 would freeze
	// there while Catwalk 保持 moving. Best effort; failed fetch 保持
	// catalog loaded 从 配置.
	//
	// refetches 发布 copy-在-写入 配置, 所以 re-读取 it
	// afterwards: mutations below must land 在 live 配置 rather
	// than snapshot 存储 has already replaced.
	if CatwalkUpdated() {
		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		store.refetchOpenAIModels(fetchCtx, ScopeGlobal)
		store.refetchGrokModels(fetchCtx, ScopeGlobal)
		cancel()
		cfg = store.Config()
	}

	env := env.New()
	// Configure providers
	valueResolver := NewShellVariableResolver(env, WithWorkingDir(workingDir))
	store.resolver = valueResolver

	// Hold writeMu during initial load 到 prevent configureProviders
	// 从 triggering auto-reload via RemoveConfigField.
	store.writeMu.Lock()
	defer store.writeMu.Unlock()

	// Apply top-level env vars before configuring providers 所以 variables
	// like AWS_PROFILE 是 visible 到 AWS SDK credential chain.
	cfg.applyEnv(valueResolver)

	if err := cfg.configureProviders(ctx, store, env, valueResolver, store.knownProviders); err != nil {
		return nil, fmt.Errorf("failed to configure providers: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !cfg.IsConfigured() {
		slog.Warn("No providers configured")
		return store, nil
	}

	resolved, err := resolveSelectedModels(cfg, store.knownProviders)
	if err != nil {
		return nil, fmt.Errorf("failed to configure selected models: %w", err)
	}
	cfg.Models[SelectedModelTypeLarge] = resolved.Large
	cfg.Models[SelectedModelTypeSmall] = resolved.Small

	// Persist any fallback corrections while we still hold writeMu.
	if resolved.LargeFallback {
		if err := store.updateLocked(ScopeGlobal, func(c *Config) map[string]any {
			return store.updatePreferredModelFields(c, SelectedModelTypeLarge, resolved.Large)
		}); err != nil {
			return nil, fmt.Errorf("failed to update preferred large model: %w", err)
		}
	}
	if resolved.SmallFallback {
		if err := store.updateLocked(ScopeGlobal, func(c *Config) map[string]any {
			return store.updatePreferredModelFields(c, SelectedModelTypeSmall, resolved.Small)
		}); err != nil {
			return nil, fmt.Errorf("failed to update preferred small model: %w", err)
		}
	}
	store.SetupAgents()

	// Capture initial staleness snapshot. Track every discovered 配置 路径,
	// 不 just ones 那 loaded, 所以 配置 文件 created after startup
	// (e.g. crushrc added mid-会话) 是 detected 作为 change.
	store.captureStalenessSnapshot(append(slices.Clone(configPaths), loadedPaths...))

	return store, nil
}

// mustMarshalConfig marshals 配置 到 JSON bytes, returning empty JSON 在
// 错误.
func mustMarshalConfig(cfg *Config) []byte {
	data, err := json.Marshal(cfg)
	if err != nil {
		return []byte("{}")
	}
	return data
}

func PushPopCrushEnv() func() {
	var found []string
	for _, ev := range os.Environ() {
		if strings.HasPrefix(ev, "CRUSH_") {
			pair := strings.SplitN(ev, "=", 2)
			if len(pair) != 2 {
				continue
			}
			found = append(found, strings.TrimPrefix(pair[0], "CRUSH_"))
		}
	}
	backups := make(map[string]string)
	for _, ev := range found {
		backups[ev] = os.Getenv(ev)
	}

	for _, ev := range found {
		os.Setenv(ev, os.Getenv("CRUSH_"+ev))
	}

	restore := func() {
		for k, v := range backups {
			os.Setenv(k, v)
		}
	}
	return restore
}

func (c *Config) configureProviders(ctx context.Context, store *ConfigStore, env env.Env, resolver VariableResolver, knownProviders []catwalk.Provider) error {
	knownProviderNames := make(map[string]bool)
	restore := PushPopCrushEnv()
	defer restore()

	// 本仓库不使用任何内置/远程 provider 目录，只认用户在配置里手写的
	// provider。knownProviders 恒为空，下面的默认 provider 合并循环不会执行，
	// 所有 provider 都走后面的自定义校验分支。
	knownProviders = nil

	for _, p := range knownProviders {
		knownProviderNames[string(p.ID)] = true
		config, configExists := c.Providers.Get(string(p.ID))
		// 如果 用户 configured known 提供方 we need 到 allow it 到 override couple 的 parameters
		if configExists {
			if config.BaseURL != "" {
				p.APIEndpoint = config.BaseURL
			}
			if config.APIKey != "" {
				p.APIKey = config.APIKey
			}
			if len(config.Models) > 0 {
				models := []catwalk.Model{}
				seen := make(map[string]bool)

				for _, model := range config.Models {
					if seen[model.ID] {
						continue
					}
					seen[model.ID] = true
					if model.Name == "" {
						model.Name = model.ID
					}
					models = append(models, model)
				}
				for _, model := range p.Models {
					if seen[model.ID] {
						continue
					}
					seen[model.ID] = true
					if model.Name == "" {
						model.Name = model.ID
					}
					models = append(models, model)
				}

				p.Models = models
			}
		}

		headers := map[string]string{}
		if len(p.DefaultHeaders) > 0 {
			maps.Copy(headers, p.DefaultHeaders)
		}
		if len(config.ExtraHeaders) > 0 {
			maps.Copy(headers, config.ExtraHeaders)
		}
		// 提供方 headers use same 错误 contract 作为 MCP headers:
		// failing $(...) aborts 提供方 load 用 clear
		// 消息, 和 header 那 resolves 到 empty string
		// (unset bare $VAR under lenient nounset, $(echo), 或 literal
		// "") 是 dropped 从 outgoing 请求.
		for k, v := range headers {
			resolved, err := resolver.ResolveValue(v)
			if err != nil {
				return fmt.Errorf("resolving provider %s header %q: %w", p.ID, k, err)
			}
			if resolved == "" {
				delete(headers, k)
				continue
			}
			headers[k] = resolved
		}
		// Start 从 用户 配置 所以 all 用户 fields survive without
		// explicit copying. Overlay catwalk identity/端点 fields
		// (already merged 用 用户 overrides above).
		prepared := config
		prepared.ID = string(p.ID)
		prepared.Name = p.Name
		prepared.BaseURL = p.APIEndpoint
		prepared.APIKey = p.APIKey
		prepared.APIKeyTemplate = p.APIKey // Store original template for re-resolution
		prepared.Type = p.Type
		prepared.Models = p.Models
		prepared.ExtraHeaders = headers
		if prepared.ExtraParams == nil {
			prepared.ExtraParams = make(map[string]string)
		}

		switch {
		case p.ID == catwalk.InferenceProviderAnthropic && config.OAuthToken != nil:
			// Claude Code subscription 是 不 supported anymore. Remove 到 show onboarding.
			// RemoveConfigField persists deletion 到 disk. 在-memory
			// state 是 kept consistent 通过 Providers.Del 调用 below; any
			// concurrent reload 那 races 用 这 写入 will also see
			// removal because it re-reads 从 disk.
			store.RemoveConfigField(ScopeGlobal, "providers.anthropic")
			c.Providers.Del(string(p.ID))
			continue
		case p.ID == catwalk.InferenceProviderCopilot && config.OAuthToken != nil:
			prepared.SetupGitHubCopilot()
		}

		switch p.ID {
		// Handle specific providers 那 require additional configuration
		case catwalk.InferenceProviderVertexAI:
			var (
				project  = env.Get("VERTEXAI_PROJECT")
				location = env.Get("VERTEXAI_LOCATION")
			)
			if project == "" || location == "" {
				if configExists {
					slog.Warn("Skipping Vertex AI provider due to missing credentials")
					c.Providers.Del(string(p.ID))
				}
				continue
			}
			prepared.ExtraParams["project"] = project
			prepared.ExtraParams["location"] = location
		case catwalk.InferenceProviderAzure:
			endpoint, err := resolver.ResolveValue(p.APIEndpoint)
			if err != nil || endpoint == "" {
				if configExists {
					slog.Warn("Skipping Azure provider due to missing API endpoint", "provider", p.ID, "error", err)
					c.Providers.Del(string(p.ID))
				}
				continue
			}
			prepared.BaseURL = endpoint
			prepared.ExtraParams["apiVersion"] = env.Get("AZURE_OPENAI_API_VERSION")
		case catwalk.InferenceProviderBedrock, catwalk.InferenceProviderBedrockEurope:
			if p.APIKey == "" && !hasAWSCredentials(env) {
				if configExists {
					slog.Warn("Skipping Bedrock provider due to missing AWS credentials")
					c.Providers.Del(string(p.ID))
				}
				continue
			}
		case catwalk.InferenceProvider("hyper"):
			if apiKey := env.Get("HYPER_API_KEY"); apiKey != "" {
				prepared.APIKey = apiKey
				prepared.APIKeyTemplate = apiKey
			} else {
				v, err := resolver.ResolveValue(p.APIKey)
				if v == "" || err != nil {
					if configExists {
						slog.Warn("Skipping Hyper provider due to missing API key", "provider", p.ID)
						c.Providers.Del(string(p.ID))
					}
					continue
				}
			}
		default:
			// An OAuth login 是 credential too: providers signed 在
			// through OAuth (e.g. OpenAI 用 ChatGPT account) 是
			// configured even 当 no API key 是 present.
			if config.OAuthToken != nil {
				break
			}
			// 如果 提供方 api 或 endpoint 是 missing we skip them
			v, err := resolver.ResolveValue(p.APIKey)
			if v == "" || err != nil {
				if configExists {
					slog.Warn("Skipping provider due to missing API key", "provider", p.ID)
					c.Providers.Del(string(p.ID))
				}
				continue
			}
		}
		c.Providers.Set(string(p.ID), prepared)
	}

	discovered := store.resolveDiscoveredModels(ctx, discoveryJobs(c, knownProviderNames), resolver)

	// Validate the custom providers.
	for id, providerConfig := range c.Providers.Seq2() {
		if knownProviderNames[id] {
			continue
		}

		// Make sure 提供方 ID 是 set.
		providerConfig.ID = id
		providerConfig.Name = cmp.Or(providerConfig.Name, id) // Use ID as name if not set
		// Migrate legacy 提供方 types onto catwalk v3 endpoint types.
		providerConfig.Type = NormalizeProviderType(providerConfig.Type)
		// 默认 到 OpenAI chat completions 如果 不 set.
		providerConfig.Type = cmp.Or(providerConfig.Type, catwalk.TypeCompletions)
		if !slices.Contains(catwalk.KnownProviderTypes(), providerConfig.Type) &&
			providerConfig.Type != hyper.Name &&
			!discover.IsKnownCustomProvider(string(providerConfig.Type)) {
			slog.Warn("Skipping custom provider due to unsupported provider type", "provider", id)
			c.Providers.Del(id)
			continue
		}

		if providerConfig.Disable {
			slog.Debug("Skipping custom provider due to disable flag", "provider", id)
			c.Providers.Del(id)
			continue
		}
		apiKey, err := resolver.ResolveValue(providerConfig.APIKey)
		if apiKey == "" || err != nil {
			slog.Warn("Provider is missing API key, this might be OK for local providers", "provider", id)
		}
		baseURL, err := resolver.ResolveValue(providerConfig.BaseURL)
		if baseURL == "" || err != nil {
			slog.Warn("Skipping custom provider due to missing API endpoint", "provider", id, "error", err)
			c.Providers.Del(id)
			continue
		}

		if models, ok := discovered[id]; ok {
			providerConfig.Models = mergeModels(providerConfig.Models, models)
		}

		if len(providerConfig.Models) == 0 {
			slog.Warn("Skipping custom provider because the provider has no models", "provider", id)
			c.Providers.Del(id)
			continue
		}

		// Custom-提供方 headers share MCP 错误 contract; see
		// known-提供方 loop above.
		for k, v := range providerConfig.ExtraHeaders {
			resolved, err := resolver.ResolveValue(v)
			if err != nil {
				return fmt.Errorf("resolving provider %s header %q: %w", id, k, err)
			}
			if resolved == "" {
				delete(providerConfig.ExtraHeaders, k)
				continue
			}
			providerConfig.ExtraHeaders[k] = resolved
		}

		c.Providers.Set(id, providerConfig)
	}

	if c.Providers.Len() == 0 {
		return fmt.Errorf("no providers configured: add at least one provider under providers in the config file")
	}

	return nil
}

// applyEnv sets top-level env vars 从 配置. Keys 是 sorted 为
// deterministic ordering 所以 那 vars referencing other vars via
// 值 resolver produce consistent results.
func (c *Config) applyEnv(resolver VariableResolver) {
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		resolved, err := resolver.ResolveValue(c.Env[k])
		if err != nil {
			slog.Warn("Skipping env var due to resolution failure.", "key", k, "value", c.Env[k], "error", err)
			continue
		}
		os.Setenv(k, resolved)
	}
}

// NormalizeOptions allocates Options 和 Options.TUI 和 fills 在 option
// defaults UI relies 在, 所以 readers can dereference them without guarding.
// Configs loaded 从 disk get 这 via setDefaults; configs arriving over
// wire 从 Crush 服务端 need same treatment before UI reads them.
//
// DiffMode 是 deliberately left alone: permissions dialog reads 它的 zero
// 值 作为 "choose split 或 unified 从 终态 width".
func (c *Config) NormalizeOptions() {
	if c.Options == nil {
		c.Options = &Options{}
	}
	if c.Options.TUI == nil {
		c.Options.TUI = &TUIOptions{}
	}
	if c.Options.TUI.Scrollbar == "" {
		c.Options.TUI.Scrollbar = ScrollbarDefault
	}
	if c.Options.TUI.ExitBanner == "" {
		c.Options.TUI.ExitBanner = ExitBannerDefault
	}
}

func (c *Config) setDefaults(workingDir, dataDir string) {
	c.NormalizeOptions()
	if len(c.Options.GlobalContextPaths) == 0 {
		crushConfigDir := filepath.Dir(GlobalConfig())
		c.Options.GlobalContextPaths = []string{
			filepath.Join(crushConfigDir, "CRUSH.md"),
			filepath.Join(filepath.Dir(crushConfigDir), "AGENTS.md"),
		}
	}
	slices.Sort(c.Options.GlobalContextPaths)
	c.Options.GlobalContextPaths = slices.Compact(c.Options.GlobalContextPaths)

	if dataDir != "" {
		c.Options.DataDirectory = dataDir
	} else if c.Options.DataDirectory == "" {
		if path, ok := fsext.LookupClosestBounded(workingDir, projectBoundary(workingDir), defaultDataDirectory); ok {
			c.Options.DataDirectory = path
		} else {
			c.Options.DataDirectory = filepath.Join(workingDir, defaultDataDirectory)
		}
	}
	c.Options.DataDirectory = filepath.Clean(filepathext.SmartJoin(workingDir, c.Options.DataDirectory))
	if c.Providers == nil {
		c.Providers = csync.NewMap[string, ProviderConfig]()
	}
	if c.Models == nil {
		c.Models = make(map[SelectedModelType]SelectedModel)
	}
	if c.RecentModels == nil {
		c.RecentModels = make(map[SelectedModelType][]SelectedModel)
	}
	if c.MCP == nil {
		c.MCP = make(map[string]MCPConfig)
	}
	// Drop orphaned OAuth token entries left behind 当 用户 removes
	// MCP 从 crush.json. See MCPConfig.isOrphanedToken.
	for name, m := range c.MCP {
		if m.isOrphanedToken() {
			delete(c.MCP, name)
		}
	}
	if c.LSP == nil {
		c.LSP = make(map[string]LSPConfig)
	}

	// Apply defaults 到 LSP configurations
	c.applyLSPDefaults()

	// Add 默认 上下文 paths 如果 they 是 不 already present
	c.Options.ContextPaths = append(slices.Clone(defaultContextPaths), c.Options.ContextPaths...)

	slices.Sort(c.Options.ContextPaths)
	c.Options.ContextPaths = slices.Compact(c.Options.ContextPaths)

	// Add 默认 skills directories 如果 不 already present.
	for _, dir := range GlobalSkillsDirs() {
		if !slices.Contains(c.Options.SkillsPaths, dir) {
			c.Options.SkillsPaths = append(c.Options.SkillsPaths, dir)
		}
	}

	// Project specific skills dirs.
	c.Options.SkillsPaths = append(c.Options.SkillsPaths, ProjectSkillsDir(workingDir)...)

	if c.Options.Attribution == nil {
		c.Options.Attribution = &Attribution{
			TrailerStyle:  TrailerStyleAssistedBy,
			GeneratedWith: true,
		}
	} else if c.Options.Attribution.TrailerStyle == "" {
		// Migrate deprecated co_authored_by 或 apply 默认
		if c.Options.Attribution.CoAuthoredBy != nil {
			if *c.Options.Attribution.CoAuthoredBy {
				c.Options.Attribution.TrailerStyle = TrailerStyleCoAuthoredBy
			} else {
				c.Options.Attribution.TrailerStyle = TrailerStyleNone
			}
		} else {
			c.Options.Attribution.TrailerStyle = TrailerStyleAssistedBy
		}
	}

	c.Options.InitializeAs = cmp.Or(c.Options.InitializeAs, defaultInitializeAs)
}

// powernapDefaults caches powernap 默认 LSP 服务端 catalog.
// catalog 是 static 和 immutable 为 life 的 process, 但
// building it (NewManager + LoadDefaults) 是 expensive 和 was previously
// repeated 在 every 配置 reload. We load it once 和 只 ever 读取 从
// it via GetServer, 所以 shared instance 是 safe.
var (
	powernapDefaultsOnce sync.Once
	powernapDefaults     *powernapConfig.Manager
)

func lspDefaultsManager() *powernapConfig.Manager {
	powernapDefaultsOnce.Do(func() {
		m := powernapConfig.NewManager()
		// LoadDefaults 只 fails 在 malformed embedded defaults, which
		// would 是 build-time bug; treat manager 作为 usable either
		// way 所以 transient 错误 never wedges 配置 loading.
		_ = m.LoadDefaults()
		powernapDefaults = m
	})
	return powernapDefaults
}

// applyLSPDefaults applies 默认 values 从 powernap 到 LSP configurations
func (c *Config) applyLSPDefaults() {
	// Reuse process-wide 默认 catalog; building it per reload was
	// significant chunk 的 reload latency.
	configManager := lspDefaultsManager()

	// Apply defaults 到 each LSP configuration
	for name, cfg := range c.LSP {
		// Try 到 get defaults 从 powernap based 在 name 或 command name.
		base, ok := configManager.GetServer(name)
		if !ok {
			base, ok = configManager.GetServer(cfg.Command)
			if !ok {
				continue
			}
		}
		if cfg.Options == nil {
			cfg.Options = base.Settings
		}
		if cfg.InitOptions == nil {
			cfg.InitOptions = base.InitOptions
		}
		if len(cfg.FileTypes) == 0 {
			cfg.FileTypes = base.FileTypes
		}
		if len(cfg.RootMarkers) == 0 {
			cfg.RootMarkers = base.RootMarkers
		}
		cfg.Command = cmp.Or(cfg.Command, base.Command)
		if len(cfg.Args) == 0 {
			cfg.Args = base.Args
		}
		if len(cfg.Env) == 0 {
			cfg.Env = base.Environment
		}
		// 更新 配置 在 map
		c.LSP[name] = cfg
	}
}

func (c *Config) defaultModelSelection(knownProviders []catwalk.Provider) (largeModel SelectedModel, smallModel SelectedModel, err error) {
	if len(knownProviders) == 0 && c.Providers.Len() == 0 {
		err = fmt.Errorf("no providers configured, please configure at least one provider")
		return largeModel, smallModel, err
	}

	// Use first 提供方 enabled based 在 known providers order
	// 如果 no 提供方 found 那 是 known use first 提供方 configured
	for _, p := range knownProviders {
		providerConfig, ok := c.Providers.Get(string(p.ID))
		if !ok || providerConfig.Disable {
			continue
		}
		defaultLargeModel := c.GetModel(string(p.ID), p.DefaultLargeModelID)
		if defaultLargeModel == nil {
			slog.Warn("Default large model %s not found for provider %s", p.DefaultLargeModelID, p.ID)
			if len(providerConfig.Models) == 0 {
				return largeModel, smallModel, fmt.Errorf("default large model %s not found for provider %s", p.DefaultLargeModelID, p.ID)
			}
			defaultLargeModel = &providerConfig.Models[0]
		}
		largeModel = SelectedModel{
			Provider:        string(p.ID),
			Model:           defaultLargeModel.ID,
			MaxTokens:       defaultLargeModel.DefaultMaxTokens,
			ReasoningEffort: defaultLargeModel.Reasoning.DefaultEffortLevel,
		}

		defaultSmallModel := c.GetModel(string(p.ID), p.DefaultSmallModelID)
		if defaultSmallModel == nil {
			slog.Warn("Default small model %s not found for provider %s", p.DefaultSmallModelID, p.ID)
			if len(providerConfig.Models) == 0 {
				return largeModel, smallModel, fmt.Errorf("default small model %s not found for provider %s", p.DefaultSmallModelID, p.ID)
			}
			defaultSmallModel = &providerConfig.Models[0]
		}
		smallModel = SelectedModel{
			Provider:        string(p.ID),
			Model:           defaultSmallModel.ID,
			MaxTokens:       defaultSmallModel.DefaultMaxTokens,
			ReasoningEffort: defaultSmallModel.Reasoning.DefaultEffortLevel,
		}
		return largeModel, smallModel, err
	}

	enabledProviders := c.EnabledProviders()
	slices.SortFunc(enabledProviders, func(a, b ProviderConfig) int {
		return strings.Compare(a.ID, b.ID)
	})

	if len(enabledProviders) == 0 {
		err = fmt.Errorf("no providers configured, please configure at least one provider")
		return largeModel, smallModel, err
	}

	providerConfig := enabledProviders[0]
	if len(providerConfig.Models) == 0 {
		err = fmt.Errorf("provider %s has no models configured", providerConfig.ID)
		return largeModel, smallModel, err
	}
	defaultLargeModel := c.GetModel(providerConfig.ID, providerConfig.Models[0].ID)
	largeModel = SelectedModel{
		Provider:  providerConfig.ID,
		Model:     defaultLargeModel.ID,
		MaxTokens: defaultLargeModel.DefaultMaxTokens,
	}
	defaultSmallModel := c.GetModel(providerConfig.ID, providerConfig.Models[0].ID)
	smallModel = SelectedModel{
		Provider:  providerConfig.ID,
		Model:     defaultSmallModel.ID,
		MaxTokens: defaultSmallModel.DefaultMaxTokens,
	}
	return largeModel, smallModel, err
}

// resolvedModels holds 结果 的 resolving 用户-configured 模型
// selections against 提供方 catalog.
type resolvedModels struct {
	Large         SelectedModel
	Small         SelectedModel
	LargeFallback bool // true if Large was corrected to a default
	SmallFallback bool // true if Small was corrected to a default
}

// resolveSelectedModels validates 用户's configured 模型 selections
// against 提供方 catalog, falling back 到 defaults 当 模型 ID 是
// invalid. 它 是 pure resolution logic: it does 不 mutate 存储 或
// touch disk. caller assigns results 到 c.Models 和 persists any
// fallback corrections 作为 appropriate.
func resolveSelectedModels(cfg *Config, knownProviders []catwalk.Provider) (resolvedModels, error) {
	var result resolvedModels
	defaultLarge, defaultSmall, err := cfg.defaultModelSelection(knownProviders)
	if err != nil {
		return result, fmt.Errorf("failed to select default models: %w", err)
	}
	large, small := defaultLarge, defaultSmall

	largeModelSelected, largeModelConfigured := cfg.Models[SelectedModelTypeLarge]
	if largeModelConfigured {
		large, result.LargeFallback = applySelection(cfg, defaultLarge, largeModelSelected)
	}
	_, smallModelConfigured := cfg.Models[SelectedModelTypeSmall]
	if smallModelConfigured {
		small, result.SmallFallback = applySelection(cfg, defaultSmall, cfg.Models[SelectedModelTypeSmall])
	}

	// When small isn't explicitly configured 和 提供方 isn't
	// known built-在, use large 模型 作为 small 模型. 这
	// prevents two different models 从 being requested concurrently
	// 为 local/openai-compat providers.
	if !smallModelConfigured {
		isKnownProvider := false
		for _, kp := range knownProviders {
			if string(kp.ID) == small.Provider {
				isKnownProvider = true
				break
			}
		}
		if !isKnownProvider {
			slog.Warn("Using large model as small model for unknown provider", "provider", large.Provider, "model", large.Model)
			small = large
		}
	}

	result.Large = large
	result.Small = small
	return result, nil
}

// applySelection overlays configured selection onto def. 它 reports
// fallback, returning def unchanged, 当 selection names no known 模型.
func applySelection(cfg *Config, def, sel SelectedModel) (SelectedModel, bool) {
	out := def
	out.Model = cmp.Or(sel.Model, def.Model)
	out.Provider = cmp.Or(sel.Provider, def.Provider)
	model := cfg.GetModel(out.Provider, out.Model)
	if model == nil {
		return def, true
	}

	out.MaxTokens = model.DefaultMaxTokens
	if sel.MaxTokens > 0 {
		out.MaxTokens = sel.MaxTokens
	}

	// 一个 selection layered over another 模型's can hold reasoning options
	// 这 one rejects, 所以 they 只 apply 到 模型 那 reasons.
	out.ReasoningEffort, out.Think = "", false
	if model.CanReason() {
		out.ReasoningEffort = cmp.Or(sel.ReasoningEffort, model.Reasoning.DefaultEffortLevel)
		out.Think = sel.Think
	} else if sel.Think || sel.ReasoningEffort != "" {
		slog.Warn(
			"Ignoring reasoning options for a model that does not declare reasoning support",
			"provider", out.Provider,
			"model", out.Model,
			"hint", `set "reasoning": {"thinking": "toggleable"} on the model if it reasons`,
		)
	}

	out.Temperature = cmp.Or(sel.Temperature, out.Temperature)
	out.TopP = cmp.Or(sel.TopP, out.TopP)
	out.TopK = cmp.Or(sel.TopK, out.TopK)
	out.FrequencyPenalty = cmp.Or(sel.FrequencyPenalty, out.FrequencyPenalty)
	out.PresencePenalty = cmp.Or(sel.PresencePenalty, out.PresencePenalty)
	if sel.ProviderOptions != nil {
		out.ProviderOptions = maps.Clone(sel.ProviderOptions)
	}
	return out, false
}

// lookupConfigs searches 配置 files starting 在 cwd 和 walking up
// through current project. upward walk stops 在 git
// working tree root 当 one can 是 detected, otherwise 在 cwd itself,
// 所以 unrelated crush.json placed above project 是 never picked
// up. Global 用户-level 配置 locations 是 always included
// regardless 的 boundary.
func lookupConfigs(cwd string) []string {
	// Prepend global 用户 配置 和 machine-owned data JSON. Only 用户
	// 配置 目录 contributes crushrc; data 目录 是 writable
	// machine state 和 must never 是 executed 作为 Bash. Missing files 是
	// skipped 当 loaded.
	configPaths := []string{
		systemConfigPath,
		GlobalConfig(),
		shellConfigSibling(GlobalConfig()),
		GlobalConfigData(),
	}

	// Ordered high-到-low priority within 目录. LookupBounded returns
	// matches 在 这 order, 和 later reverse + merge make earliest
	// listed name win 在 conflict. So: .crushrc beats crushrc, both beat
	// JSON configs, 和 .crush.json beats crush.json.
	configNames := []string{
		"." + appName + "rc",
		appName + "rc",
		"." + appName + ".json",
		appName + ".json",
	}

	foundConfigs, err := fsext.LookupBounded(cwd, projectBoundary(cwd), configNames...)
	if err != nil {
		// returns 在 least 默认 configs
		return configPaths
	}

	// reverse order 所以 last 配置 has more priority
	slices.Reverse(foundConfigs)

	return append(configPaths, foundConfigs...)
}

func loadFromConfigPaths(ctx context.Context, configPaths []string) (*Config, []string, error) {
	var configs [][]byte
	var loaded []string

	// Track directories 那 have both crush.json 和 crushrc 到 warn
	// about potential confusion, along 用 top-level keys each
	// defines 所以 we can 报告 conflicts.
	jsonDirKeys := make(map[string]map[string]bool)
	shDirKeys := make(map[string]map[string]bool)

	for _, path := range configPaths {
		if path == "" {
			continue
		}
		data, err := readFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, fmt.Errorf("failed to open config file %s: %w", path, err)
		}
		if len(data) == 0 {
			continue
		}

		dir := filepath.Dir(path)
		if isShellConfig(path) {
			jsonBytes, err := shellconfig.LoadShellConfig(ctx, path, data)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to load shell config %s: %w", path, err)
			}
			if len(jsonBytes) > 0 {
				if !json.Valid(jsonBytes) {
					return nil, nil, fmt.Errorf("shell config %s produced invalid JSON", path)
				}
				jsonBytes = anchorPromptPaths(jsonBytes, dir)
				addTopLevelKeys(shDirKeys, dir, jsonBytes)
				configs = append(configs, jsonBytes)
				loaded = append(loaded, path)
			}
		} else {
			if !json.Valid(data) {
				return nil, nil, fmt.Errorf("invalid JSON in config file %s", path)
			}
			data = migrateCatwalkV2File(path, data)
			data = anchorPromptPaths(data, dir)
			addTopLevelKeys(jsonDirKeys, dir, data)
			configs = append(configs, data)
			loaded = append(loaded, path)
		}
	}

	// Warn 如果 both JSON 配置 和 crushrc exist 在 same 目录
	// 和 define overlapping top-level keys. Disjoint coexistence 是
	// intentional 和 不 worth warning about.
	for dir, jKeys := range jsonDirKeys {
		sKeys, ok := shDirKeys[dir]
		if !ok {
			continue
		}
		var conflicts []string
		for k := range jKeys {
			if sKeys[k] {
				conflicts = append(conflicts, k)
			}
		}
		if len(conflicts) > 0 {
			slices.Sort(conflicts)
			slog.Warn("Found both a JSON config and a crushrc in the same directory; merging with crushrc taking precedence",
				"dir", dir, "conflicting_keys", strings.Join(conflicts, ", "))
		}
	}

	cfg, err := loadFromBytes(configs)
	if err != nil {
		return nil, nil, err
	}
	return cfg, loaded, nil
}

// anchorPromptPaths makes relative options.prompts paths 在 one 配置
// 文件 absolute against 那 文件's 目录, before files merge 和
// lose track 的 where each 路径 came 从. Otherwise relative 路径 在
// global 配置 would point 在 different 文件 在 every project. Paths
// starting 用 ~ 或 $ 是 left 为 expansion 当 提示词 是 读取.
func anchorPromptPaths(data []byte, dir string) []byte {
	gjson.GetBytes(data, "options.prompts").ForEach(func(name, path gjson.Result) bool {
		p := path.String()
		if p == "" || strings.HasPrefix(p, "~") || strings.HasPrefix(p, "$") || filepathext.SmartIsAbs(p) {
			return true
		}
		abs, err := filepath.Abs(filepath.Join(dir, p))
		if err != nil {
			return true
		}
		if out, err := sjson.SetBytes(data, "options.prompts."+gjson.Escape(name.String()), abs); err == nil {
			data = out
		}
		return true
	})
	return data
}

// addTopLevelKeys records top-level JSON keys present 在 data into
// set 为 dir.
func addTopLevelKeys(m map[string]map[string]bool, dir string, data []byte) {
	keys := m[dir]
	if keys == nil {
		keys = make(map[string]bool)
		m[dir] = keys
	}
	gjson.ParseBytes(data).ForEach(func(key, _ gjson.Result) bool {
		keys[key.String()] = true
		return true
	})
}

func loadFromBytes(configs [][]byte) (*Config, error) {
	if len(configs) == 0 {
		return &Config{}, nil
	}

	data, err := jsons.Merge(configs)
	if err != nil {
		return nil, err
	}
	// Safety net 为 sources 那 cannot 是 rewritten 在 disk (e.g.
	// crushrc builtins producing JSON): migrate merged 结果 在
	// memory 所以 decoding always sees v3 schema.
	data, _ = migrateCatwalkV2(data)
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func hasAWSCredentials(env env.Env) bool {
	if env.Get("AWS_BEARER_TOKEN_BEDROCK") != "" {
		return true
	}

	if env.Get("AWS_ACCESS_KEY_ID") != "" && env.Get("AWS_SECRET_ACCESS_KEY") != "" {
		return true
	}

	if env.Get("AWS_PROFILE") != "" || env.Get("AWS_DEFAULT_PROFILE") != "" {
		return true
	}

	if env.Get("AWS_REGION") != "" || env.Get("AWS_DEFAULT_REGION") != "" {
		return true
	}

	if env.Get("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
		env.Get("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" {
		return true
	}

	// 文件-based credential discovery requires filesystem stats, 所以 do it
	// last 和 skip it under 测试. Checking testing.Testing() before
	// os.Stat 调用 (rather than after, 在 && tail) ensures syscall
	// 是 never issued during tests, where it otherwise ran unconditionally
	// 和 只 had 它的 结果 discarded.
	if testing.Testing() {
		return false
	}
	if _, err := os.Stat(filepath.Join(home.Dir(), ".aws/credentials")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(home.Dir(), ".aws/login")); err == nil {
		return true
	}

	return false
}

// migrateDisableNotifications migrates the deprecated disable_notifications
// 和 notification_style fields 到 unified notifications 字段. 它 checks
// both 用户 配置 (~/.配置) 和 data 配置 (~/.local) files. If
// disable_notifications 是 true, it sets notifications 到 "disabled" 在
// data 文件. If notification_style 是 set, it moves 值 到 notifications.
// Regardless 的 值, it removes deprecated fields 从 any 文件 那
// contains them.
func migrateDisableNotifications() {
	globalConfig := GlobalConfig()
	dataConfig := GlobalConfigData()

	var wasDisabled bool
	var styleValue string
	filesToClean := []string{}

	for _, path := range []string{globalConfig, dataConfig} {
		data, err := readFile(path)
		if err != nil {
			continue
		}
		needsClean := false
		if gjson.Get(string(data), "options.disable_notifications").Exists() {
			needsClean = true
			if gjson.Get(string(data), "options.disable_notifications").Bool() {
				wasDisabled = true
			}
		}
		if v := gjson.Get(string(data), "options.notification_style"); v.Exists() {
			needsClean = true
			if styleValue == "" {
				styleValue = v.String()
			}
		}
		if needsClean {
			filesToClean = append(filesToClean, path)
		}
	}

	if len(filesToClean) == 0 {
		return
	}

	// Determine 值 到 persist: notification_style takes precedence,
	// then disable_notifications: true maps 到 "disabled".
	migratedValue := styleValue
	if migratedValue == "" && wasDisabled {
		migratedValue = "disabled"
	}

	if migratedValue != "" {
		data, err := readFile(dataConfig)
		if err == nil {
			if !gjson.Get(string(data), "options.notifications").Exists() {
				updated, err := sjson.Set(string(data), "options.notifications", migratedValue)
				if err == nil {
					if err := atomicWriteFile(dataConfig, []byte(updated), 0o600); err != nil {
						slog.Warn("Failed to migrate to notifications field", "error", err)
					} else {
						slog.Info("Migrated notification settings to notifications field", "value", migratedValue)
					}
				}
			}
		}
	}

	// Remove deprecated fields 从 all files 那 contain them.
	for _, path := range filesToClean {
		data, err := readFile(path)
		if err != nil {
			continue
		}
		updated := string(data)
		updated, _ = sjson.Delete(updated, "options.disable_notifications")
		updated, _ = sjson.Delete(updated, "options.notification_style")
		if updated == string(data) {
			continue
		}
		if err := atomicWriteFile(path, []byte(updated), 0o600); err != nil {
			slog.Warn("Failed to write migrated config", "path", path, "error", err)
		}
	}
}

// GlobalConfig returns global configuration 文件 路径 为 application.
func GlobalConfig() string {
	if crushGlobal := os.Getenv("CRUSH_GLOBAL_CONFIG"); crushGlobal != "" {
		return filepath.Join(crushGlobal, fmt.Sprintf("%s.json", appName))
	}
	return filepath.Join(home.Config(), appName, fmt.Sprintf("%s.json", appName))
}

// shellConfigSibling returns crushrc 路径 那 sits alongside given
// crush.json 路径 (same 目录). Used 所以 global 配置 locations pick up
// shell 配置, 不 just JSON.
func shellConfigSibling(jsonPath string) string {
	return filepath.Join(filepath.Dir(jsonPath), appName+"rc")
}

// isShellConfig reports whether 配置 路径 是 shell 配置 (crushrc 或
// hidden .crushrc), 作为 opposed 到 JSON 配置.
func isShellConfig(path string) bool {
	base := filepath.Base(path)
	return base == appName+"rc" || base == "."+appName+"rc"
}

// GlobalCacheDir returns 路径 到 global 缓存 目录 为
// application.
func GlobalCacheDir() string {
	if crushCache := os.Getenv("CRUSH_CACHE_DIR"); crushCache != "" {
		return crushCache
	}
	if xdgCacheHome := os.Getenv("XDG_CACHE_HOME"); xdgCacheHome != "" {
		return filepath.Join(xdgCacheHome, appName)
	}
	if runtime.GOOS == "windows" {
		localAppData := cmp.Or(
			os.Getenv("LOCALAPPDATA"),
			filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local"),
		)
		return filepath.Join(localAppData, appName, "cache")
	}
	return filepath.Join(home.Dir(), ".cache", appName)
}

// ProjectConfigs returns list 的 current project configs paths.
func ProjectConfigs(cwd string) []string {
	return lookupConfigs(cwd)
}

// GlobalConfigData returns 路径 到 main data 目录 为 application.
// 这 配置 是 used 当 app overrides configurations instead 的 updating global 配置.
func GlobalConfigData() string {
	if crushData := os.Getenv("CRUSH_GLOBAL_DATA"); crushData != "" {
		return filepath.Join(crushData, fmt.Sprintf("%s.json", appName))
	}
	if xdgDataHome := os.Getenv("XDG_DATA_HOME"); xdgDataHome != "" {
		return filepath.Join(xdgDataHome, appName, fmt.Sprintf("%s.json", appName))
	}

	// return 路径 到 main data 目录
	// 为 windows, it should 是 在 `%LOCALAPPDATA%/crush/`
	// 为 linux 和 macOS, it should 是 在 `$HOME/.local/share/crush/`
	if runtime.GOOS == "windows" {
		localAppData := cmp.Or(
			os.Getenv("LOCALAPPDATA"),
			filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local"),
		)
		return filepath.Join(localAppData, appName, fmt.Sprintf("%s.json", appName))
	}

	return filepath.Join(home.Dir(), ".local", "share", appName, fmt.Sprintf("%s.json", appName))
}

// GlobalWorkspaceDir returns 路径 到 global 服务端 工作区
// 目录. 这 目录 acts 作为 meta-工作区 为 服务端
// process, giving it real workingDir 所以 那 配置 loading, scoped
// writes, 和 提供方 resolution behave identically 到 project
// workspaces.
func GlobalWorkspaceDir() string {
	return filepath.Dir(GlobalConfigData())
}

func assignIfNil[T any](ptr **T, val T) {
	if *ptr == nil {
		*ptr = &val
	}
}

// isInsideWorktree reports whether dir 是 inside git working tree. 它
// checks dir rather than process working 目录, which 服务端
// shares across every 工作区 it hosts.
func isInsideWorktree(dir string) bool {
	cmd := exec.CommandContext(
		context.Background(),
		"git", "rev-parse",
		"--is-inside-work-tree",
	)
	cmd.Dir = dir
	bts, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(bts)) == "true"
}

// worktreeRoot returns absolute 路径 的 git working tree root 为
// dir, 或 empty string 如果 dir 是 不 inside working tree (bare
// repositories, missing git 二进制, plain directories, 或 any other
// failure mode). Linked worktrees 和 submodules each 报告 their own
// top-level, which 是 what callers want 当 bounding lookups.
// worktreeRootCache memoizes git worktree root per 目录. root
// 是 stable 为 life 的 process, 所以 we avoid re-shelling out 到
// "git rev-parse" 在 every 配置 reload. Keyed 通过 requested dir;
// 值 是 resolved root ("" 当 dir 是 不 在 git worktree).
var worktreeRootCache sync.Map // map[string]string

func worktreeRoot(dir string) string {
	if cached, ok := worktreeRootCache.Load(dir); ok {
		return cached.(string)
	}
	root := computeWorktreeRoot(dir)
	worktreeRootCache.Store(dir, root)
	return root
}

func computeWorktreeRoot(dir string) string {
	cmd := exec.CommandContext(
		context.Background(),
		"git", "rev-parse", "--show-toplevel",
	)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	return abs
}

// projectBoundary returns 目录 在 which upward configuration
// search rooted 在 dir should stop. 它 是 git working tree root 当
// one can 是 detected, otherwise dir itself. Returning dir 作为
// fallback keeps Crush 从 silently adopting state files placed above
// the current project.
func projectBoundary(dir string) string {
	if root := worktreeRoot(dir); root != "" {
		return root
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// GlobalSkillsDirs returns 默认 directories 为 Agent Skills.
// Skills 在 these directories 是 auto-discovered 和 their files can 是 读取
// without 权限 prompts.
func GlobalSkillsDirs() []string {
	if crushSkills := os.Getenv("CRUSH_SKILLS_DIR"); crushSkills != "" {
		return []string{crushSkills}
	}

	paths := []string{
		filepath.Join(home.Config(), appName, "skills"),
		filepath.Join(home.Config(), "agents", "skills"),
		// Per the Agent Skills spec, scan ~/.agents/skills
		filepath.Join(home.Dir(), ".agents", "skills"),
		filepath.Join(home.Dir(), ".claude", "skills"),
	}

	// On Windows, also load 从 app data 在 top 的 `$HOME/.配置/crush`.
	// 这 是 here mostly 为 backwards compatibility.
	if runtime.GOOS == "windows" {
		appData := cmp.Or(
			os.Getenv("LOCALAPPDATA"),
			filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local"),
		)
		paths = append(
			paths,
			filepath.Join(appData, appName, "skills"),
			filepath.Join(appData, "agents", "skills"),
		)
	}

	return paths
}

// projectSkillSubdirs lists the conventional subdirectories where
// project-level skills 是 discovered. Shared across working-dir 和
// git-root lookups 到 prevent drift 当 new convention 是 added.
var projectSkillSubdirs = []string{
	".agents/skills",
	".crush/skills",
	".claude/skills",
	".cursor/skills",
}

// ProjectSkillsDir returns 默认 project directories 为 which Crush
// will look 为 skills. In addition 到 working 目录, it also
// checks git working tree root 所以 那 monorepo-level skills 是
// discovered 当 用户 是 inside subdirectory.
// Working-目录 paths come first 所以 local skills take precedence
// over monorepo-level ones.
func ProjectSkillsDir(workingDir string) []string {
	dirs := make([]string, 0, len(projectSkillSubdirs)*2)
	for _, sub := range projectSkillSubdirs {
		dirs = append(dirs, filepath.Join(workingDir, sub))
	}

	// When working 目录 是 inside git repository, also look 在
	// repository root 所以 monorepo-level .agents/skills 是 found.
	if root := worktreeRoot(workingDir); root != "" && root != workingDir {
		for _, sub := range projectSkillSubdirs {
			dirs = append(dirs, filepath.Join(root, sub))
		}
	}

	return dirs
}

func isAppleTerminal() bool { return os.Getenv("TERM_PROGRAM") == "Apple_Terminal" }

// ValidatePrompts rejects 提示词 names 那 do 不 name built-在 提示词, 所以
// typo fails 在 startup instead 的 silently leaving built-在 在 place.
func (c *Config) ValidatePrompts() error {
	if c.Options == nil {
		return nil
	}
	return ValidatePromptNames(c.Options.Prompts)
}

// ValidateCoreutils rejects coreutils modes shell does 不 know, 所以
// typo fails 在 startup instead 的 silently falling back 到 默认.
func (c *Config) ValidateCoreutils() error {
	if c.Options == nil {
		return nil
	}
	switch c.Options.Coreutils {
	case "", CoreutilsTurtle, CoreutilsSystem, CoreutilsAuto, CoreutilsOff:
		return nil
	default:
		return fmt.Errorf("unknown coreutils mode %q, expected turtle, system, auto, or off", string(c.Options.Coreutils))
	}
}

// ValidatePromptNames rejects any key 在 prompts 那 是 不 built-在
// 提示词 name. 配置 files 和 --提示词 flag both go through it.
func ValidatePromptNames(prompts PromptPaths) error {
	for _, name := range slices.Sorted(maps.Keys(prompts)) {
		if !slices.Contains(PromptNames, name) {
			return fmt.Errorf("unknown prompt %q, expected one of %s", name, strings.Join(PromptNames, ", "))
		}
	}
	return nil
}
