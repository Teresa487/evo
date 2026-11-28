// store.go —— 配置的运行时存储与热更新（ConfigStore）。
//
// 从产物反推：UI 改设置 → Workspace.SetConfigField/SetProviderAPIKey
//   → ConfigStore 落盘并广播变更 → 下次 UpdateAgentModel 生效。
//
// 它让配置在运行中可改，而不必重启进程。

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"charm.land/catwalk/v3/pkg/catwalk"
	hyperp "github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/charmbracelet/crush/internal/lock"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/oauth/copilot"
	"github.com/charmbracelet/crush/internal/oauth/grok"
	"github.com/charmbracelet/crush/internal/oauth/hyper"
	"github.com/charmbracelet/crush/internal/oauth/openai"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/sync/singleflight"
)

// configLockDeadline bounds how long lockConfig waits 为
// cross-process flock before giving up. 一个 few seconds 是 plenty 为
// honest contention; longer suggests something 是 wedged.
const configLockDeadline = 5 * time.Second

// refreshLockDeadline bounds how long RefreshOAuthToken waits 为
// per-提供方 cross-process refresh 锁. 它 must exceed token
// exchange HTTP 超时 (30s) 所以 那 peer mid-exchange 是 given time
// 到 finish 和 发布 它的 结果, which we then adopt instead 的
// running our own exchange. Running our own would reuse an
// already-rotated refresh token 和 trip 提供方's reuse detection,
// revoking whole 令牌 family.
const refreshLockDeadline = 45 * time.Second

// credentialWriteLockDeadline bounds how long credential 写入 (e.g.
// storing token 从 fresh interactive login) waits 为
// per-提供方 refresh 锁. 它 是 deliberately shorter than
// refreshLockDeadline because 用户 是 watching: 如果 peer 是 wedged we
// would rather 写入 和 risk rare clobber than hang UI.
const credentialWriteLockDeadline = 10 * time.Second

// fileSnapshot captures metadata about 配置 文件 在 point 在 time.
type fileSnapshot struct {
	Path    string
	Exists  bool
	Size    int64
	ModTime int64 // UnixNano
}

// RuntimeOverrides holds per-会话 settings 那 是 never persisted 到
// disk. They 是 applied 在 top 的 loaded 配置 和 survive 只 为
// lifetime 的 process (或 工作区).
type RuntimeOverrides struct {
	PermissionMode permission.PermissionMode
	// EnabledChannels lists MCP servers opted 在 作为 channels 为 这
	// 会话 (via --channels flag). 一个 服务端 present 在 MCP 配置 只
	// pushes 渠道 events 当 it also appears here. Entries may 是 written
	// 作为 "服务端:<name>" 或 作为 bare "<name>".
	EnabledChannels []string
	// Models records 模型 choices made 在 这 instance, whether
	// persisted 或 不. They 是 reapplied after 配置 reload 所以 那
	// selection made here always outranks whatever shared 配置 文件
	// happens 到 hold — see pinPreferredModelLocked.
	Models map[SelectedModelType]SelectedModel
	// Prompts replaces built-在 prompts 为 这 会话 只 (via
	// --提示词 flag). Entries win over options.prompts 和 hold absolute
	// paths, since they were resolved against the invoking shell's cwd.
	Prompts PromptPaths
}

// ConfigStore 是 single 条目 point 为 all 配置 access. 它 owns
// pure-data 配置, runtime state (working 目录, resolver, known
// providers), 和 persistence 到 both global 和 工作区 配置 files.
//
// mu serialises all 配置 文件 mutations (SetConfigFields,
// RemoveConfigField, RefreshOAuthToken) 到 prevent both 在-process
// goroutine races 和, together 用 shared 锁.文件, cross-process
// races 在 配置 文件.
//
// writeMu serialises every operation 那 produces new 在-memory 配置:
// typed copy-在-写入 mutators (SetCompactMode, UpdatePreferredModel,
// ...) 和 ReloadFromDisk. Typed mutators take 锁; autoReload takes
// TryLock 所以 写入 triggered re-entrantly during reload (e.g.
// configureProviders calling RemoveConfigField) skips the nested reload
// instead 的 deadlocking. 这 是 what lets published Configs 是 treated
// 作为 immutable: mutator clones, mutates clone, 和 swaps it 在 under
// writeMu rather than mutating live 配置 在 place.
type ConfigStore struct {
	config             *Config
	workingDir         string
	resolver           VariableResolver
	globalDataPath     string   // ~/.local/share/crush/crush.json
	workspacePath      string   // .evo/crush.json
	loadedPaths        []string // config files that were successfully loaded
	knownProviders     []catwalk.Provider
	overrides          RuntimeOverrides
	trackedConfigPaths []string                // unique, normalized config file paths
	snapshots          map[string]fileSnapshot // path -> snapshot at last capture
	cachedDiscovery    []discoveryJob          // providers whose models came from the discovery cache; guarded by writeMu

	// configMu guards 配置 指针 字段 against concurrent
	// readers (配置) 和 writeMu-serialised swap (setConfig). 它
	// protects 指针 word 只; pointed-到 配置 是 treated
	// 作为 immutable once published, since both reloads 和 typed mutators
	// build fresh 配置 rather than mutating live one.
	configMu sync.RWMutex

	mu      sync.Mutex   // serialises config file writes
	writeMu sync.RWMutex // serialises in-memory config production (mutators + reload); RLock for readers

	// refreshSF collapses concurrent 在-process OAuth refreshes 为
	// same 提供方 into single attempt. Combined 用 per-提供方
	// cross-process refresh 锁, it ensures 只 one token exchange runs
	// 在 time. See RefreshOAuthToken.
	refreshSF singleflight.Group

	// rejectedRefreshTokens remembers refresh tokens 提供方 has refused
	// 为 rejectedRefreshTokenTTL, 所以 dead credential costs one exchange
	// per 窗口 rather than one per caller.
	rejectedMu            sync.Mutex
	rejectedRefreshTokens map[string]rejectedRefresh
	// now 是 clock 为 rejectedRefreshTokens; nil means time.Now.
	now func() time.Time

	// exchangeToken performs 提供方-specific OAuth token exchange.
	// 它 是 字段 所以 tests can substitute fake exchange without making
	// real network calls. Production code leaves it nil, 和 exchange falls
	// back 到 real 提供方 clients.
	exchangeToken func(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error)

	// authSignalMu guards authSignals, which maps 提供方 IDs 到
	// channels 那 WaitForTokenChange blocks 在. SignalAuthComplete
	// closes 渠道 到 unblock waiters; new 渠道 是 created
	// 在 next wait.
	authSignalMu sync.Mutex
	authSignals  map[string]chan struct{}
}

// 配置 returns pure-data 配置 struct (读取-只 after load).
//
// 指针 读取 是 guarded 通过 configMu 所以 it can never tear against
// reload swap 在 reloadFromDiskLocked. Reloads build brand-new
// 配置 和 swap it 在 rather than mutating live one, 所以 holding
// returned 指针 stays safe even across concurrent reload — reader
// keeps reading 它的 (now immutable) snapshot.
func (s *ConfigStore) Config() *Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
}

// setConfig atomically swaps 活跃 配置 指针 under configMu.
// Used 通过 reload 路径; 在-place 字段 mutators leave 指针
// untouched 和 运行 under mu instead.
func (s *ConfigStore) setConfig(cfg *Config) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.config = cfg
}

// WorkingDir returns current working 目录.
func (s *ConfigStore) WorkingDir() string {
	return s.workingDir
}

// Resolver returns the variable resolver.
func (s *ConfigStore) Resolver() VariableResolver {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return s.resolver
}

// Resolve resolves a variable reference using the configured resolver.
func (s *ConfigStore) Resolve(key string) (string, error) {
	s.writeMu.RLock()
	r := s.resolver
	s.writeMu.RUnlock()
	if r == nil {
		return "", fmt.Errorf("no variable resolver configured")
	}
	return r.ResolveValue(key)
}

// KnownProviders returns list 的 known providers.
func (s *ConfigStore) KnownProviders() []catwalk.Provider {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return s.knownProviders
}

// RefetchHyperProvider re-fetches Hyper 提供方 catalog 从
// remote API 和 updates 在-memory known providers list 和 配置.
// 这 是 called after OAuth authentication completes 所以 latest
// models 是 available without restarting.
func (s *ConfigStore) RefetchHyperProvider(ctx context.Context) error {
	// Build fresh 客户端 那 reads API key 从 live 配置,
	// 不 stale snapshot captured 在 startup. syncer's original
	// 客户端 closes over startup 配置 和 would send expired
	// 令牌 after OAuth re-认证.
	freshClient := realHyperClient{
		baseURL:    hyperp.BaseURL(),
		resolveKey: func() string { return ResolveHyperAPIKey(s.Config()) },
	}
	// 启动时不再拉取整份目录，hyper 同步器可能尚未初始化；这里按需初始化，
	// 否则登录后刷新会 panic 在 "called Refetch before Init"。
	hyperSyncer.Init(freshClient, cachePathFor("hyper"), true)

	hyperProvider, err := hyperSyncer.Refetch(ctx)
	if err != nil {
		return fmt.Errorf("failed to refetch Hyper provider: %w", err)
	}
	if hyperProvider.ID == "" {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Replace 或 insert Hyper 条目 在 knownProviders.
	found := false
	for i, p := range s.knownProviders {
		if string(p.ID) == string(hyperProvider.ID) {
			s.knownProviders[i] = hyperProvider
			found = true
			break
		}
	}
	if !found {
		s.knownProviders = append([]catwalk.Provider{hyperProvider}, s.knownProviders...)
	}

	// 更新 Hyper 提供方 配置 用 refreshed 模型 list
	// 和 endpoint. Use cloneForWrite 所以 readers always see consistent
	// snapshot ( 存储's contract forbids 在-place 配置 mutation).
	nc := s.config.cloneForWrite()
	if pc, ok := nc.Providers.Get(string(hyperProvider.ID)); ok {
		pc.Models = hyperProvider.Models
		if hyperProvider.APIEndpoint != "" {
			pc.BaseURL = hyperProvider.APIEndpoint
		}
		nc.Providers.Set(string(hyperProvider.ID), pc)
	}
	s.setConfig(nc)

	// Also 更新 memoized 提供方 list 所以 callers 的
	// 配置.Providers() (e.g. models dialog) see fresh data.
	UpdateProviderInList(hyperProvider)

	s.SetupAgents()
	return nil
}

// SetupAgents configures coder 和 task agents 在 配置.
func (s *ConfigStore) SetupAgents() {
	s.Config().SetupAgents()
}

// Overrides returns runtime overrides 为 这 存储.
func (s *ConfigStore) Overrides() *RuntimeOverrides {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return &s.overrides
}

// LoadedPaths returns 配置 文件 paths 那 were successfully loaded.
func (s *ConfigStore) LoadedPaths() []string {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return slices.Clone(s.loadedPaths)
}

// lockConfig acquires both 在-process 互斥锁 和 cross-process flock
// 在 配置 文件 为 given scope. Callers 那 need 到 do I/O
// between reading 和 writing (e.g. HTTP token exchange) must use
// lockConfig explicitly rather than atomicWrite.
//
// returned release function drops both locks. Callers must 调用 it
// 作为 soon 作为 文件 access 是 完成 — no I/O should 是 performed
// while 锁 是 held.
func (s *ConfigStore) lockConfig(scope Scope) (func(), error) {
	s.mu.Lock()
	path, err := s.configPath(scope)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), configLockDeadline)
	defer cancel()
	release, err := lock.File(ctx, path+".lock")
	if err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("acquire config lock: %w", err)
	}
	return func() {
		release()
		s.mu.Unlock()
	}, nil
}

// atomicWrite handles 锁-读取-transform-写入-unlock cycle 为
// 配置 文件 mutations. fn callback receives current 文件
// contents (raw bytes, 或 {} 如果 文件 是 missing) 和 must return
// new contents. fn must 是 pure — no I/O, no network calls.
func (s *ConfigStore) atomicWrite(scope Scope, fn func(current []byte) ([]byte, error)) error {
	unlock, err := s.lockConfig(scope)
	if err != nil {
		return err
	}
	defer unlock()

	path, err := s.configPath(scope)
	if err != nil {
		return err
	}

	data, err := readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			data = []byte("{}")
		} else {
			return fmt.Errorf("read config file: %w", err)
		}
	}

	newData, err := fn(data)
	if err != nil {
		return err
	}

	return atomicWriteFile(path, newData, 0o600)
}

// configPath returns 文件 路径 为 given scope.
func (s *ConfigStore) configPath(scope Scope) (string, error) {
	switch scope {
	case ScopeWorkspace:
		if s.workspacePath == "" {
			return "", ErrNoWorkspaceConfig
		}
		return s.workspacePath, nil
	default:
		return s.globalDataPath, nil
	}
}

// HasConfigField checks whether key exists 在 配置 文件 为 given
// scope.
func (s *ConfigStore) HasConfigField(scope Scope, key string) bool {
	path, err := s.configPath(scope)
	if err != nil {
		return false
	}
	data, err := readFile(path)
	if err != nil {
		return false
	}
	return gjson.Get(string(data), key).Exists()
}

// SetConfigField sets key/值 pair 在 配置 文件 为 given scope.
// After successful 写入, it automatically reloads 配置 到 keep 在-memory
// state fresh.
func (s *ConfigStore) SetConfigField(scope Scope, key string, value any) error {
	return s.SetConfigFields(scope, map[string]any{key: value})
}

// SetConfigFields sets multiple key/值 pairs 在 配置 文件 为
// given scope 在 single 写入, then reloads 在-memory state 从 disk.
//
// Use 这 为 arbitrary external edits where 在-memory effect 的
// change 是 不 known ahead 的 time. typed mutators (which know exactly
// what changed) go through 更新 instead 和 skip reload.
//
// 写入 是 protected 通过 在-process 互斥锁 和 cross-process flock
// 到 prevent races between concurrent writers 在 different processes.
func (s *ConfigStore) SetConfigFields(scope Scope, kv map[string]any) error {
	if err := s.writeConfigFields(scope, kv); err != nil {
		return err
	}
	// Auto-reload 到 keep 在-memory state fresh after 配置 edits.
	// We use 上下文.Background() since 这 是 internal operation 那
	// shouldn't 是 cancelled 通过 用户 上下文.
	if err := s.autoReload(context.Background()); err != nil {
		// Log warning 但 don't fail 写入 - disk 是 already updated.
		slog.Warn("Config file updated but failed to reload in-memory state", "error", err)
	}
	return nil
}

// writeConfigFields persists key/值 pairs 到 配置 文件. 它 does 不
// touch 在-memory 配置 state 或 staleness snapshot: callers either
// reload (SetConfigFields, whose reload recaptures snapshot) 或 have
// already published updated clone 和 capture snapshot themselves
// (更新). Both 的 those 运行 under writeMu, which 是 what keeps
// snapshot map free 的 concurrent writers.
func (s *ConfigStore) writeConfigFields(scope Scope, kv map[string]any) error {
	// Sort keys 为 deterministic output regardless 的 map iteration
	// order. 这 also ensures consistent results 当 callers pass
	// overlapping JSONPath keys (e.g. "" 和 ".b").
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	return s.atomicWrite(scope, func(data []byte) ([]byte, error) {
		v := string(data)
		for _, key := range keys {
			var sErr error
			if v, sErr = sjson.Set(v, key, kv[key]); sErr != nil {
				return nil, fmt.Errorf("failed to set config field %s: %w", key, sErr)
			}
		}
		return []byte(v), nil
	})
}

// mutateInMemory applies copy-在-写入 change 到 配置 without
// persisting. Under writeMu it clones live 配置, lets mutate edit
// clone, 和 publishes it. 这 是 single primitive every 在-memory
// 配置 change goes through, 所以 published 配置 是 never mutated 在
// place 和 readers always see consistent snapshot.
func (s *ConfigStore) mutateInMemory(mutate func(*Config)) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nc := s.Config().cloneForWrite()
	mutate(nc)
	s.setConfig(nc)
}

// 更新 applies copy-在-写入 change 和 persists reported fields.
// mutate edits clone 和 returns JSON-路径 fields 到 写入 到 disk;
// because clone already reflects change, no reload 是 needed.
// Returning empty map publishes clone without disk 写入.
func (s *ConfigStore) update(scope Scope, mutate func(*Config) map[string]any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.updateLocked(scope, mutate)
}

// updateLocked 是 锁-free core 的 更新. Caller must hold writeMu.
func (s *ConfigStore) updateLocked(scope Scope, mutate func(*Config) map[string]any) error {
	nc := s.Config().cloneForWrite()
	fields := mutate(nc)
	s.setConfig(nc)
	if len(fields) == 0 {
		return nil
	}
	if err := s.writeConfigFields(scope, fields); err != nil {
		return err
	}
	// Refresh staleness snapshot 所以 文件 watcher does 不 treat
	// our own 写入 作为 external change. Safe 到 touch snapshot map
	// here because we hold writeMu.
	if path, err := s.configPath(scope); err == nil {
		s.captureStalenessSnapshot(append(slices.Clone(s.loadedPaths), path))
	}
	return nil
}

// OverridePreferredModel sets preferred 模型 为 given type 在
// memory 只, without persisting. 它 是 为 per-运行 overrides (such 作为
// non-interactive --模型 flags) 那 must 不 是 written 到 用户's
// 配置 文件.
func (s *ConfigStore) OverridePreferredModel(modelType SelectedModelType, model SelectedModel) {
	s.mutateInMemory(func(c *Config) {
		if c.Models == nil {
			c.Models = make(map[SelectedModelType]SelectedModel)
		}
		c.Models[modelType] = model
		s.pinPreferredModelLocked(modelType, model)
	})
}

// pinPreferredModelLocked records 模型 choice made 在 这 instance 所以
// 那 later 配置 reload cannot replace it 用 choice made
// somewhere else. Several Crush instances share one global 配置 文件, 所以
// reload triggered 通过 unrelated 写入 ( token refresh, say) would
// otherwise import whichever 模型 sibling instance last selected 和
// switch models out 从 under 用户 mid-会话.
//
// Caller 必须 hold writeMu.
func (s *ConfigStore) pinPreferredModelLocked(modelType SelectedModelType, model SelectedModel) {
	if s.overrides.Models == nil {
		s.overrides.Models = make(map[SelectedModelType]SelectedModel)
	}
	s.overrides.Models[modelType] = model
}

// RemoveConfigField removes key 从 配置 文件 为 given scope.
// After successful 写入, it automatically reloads 配置 到 keep 在-memory
// state fresh.
//
// 写入 是 protected 通过 在-process 互斥锁 和 cross-process flock.
func (s *ConfigStore) RemoveConfigField(scope Scope, key string) error {
	err := s.atomicWrite(scope, func(data []byte) ([]byte, error) {
		v, sErr := sjson.Delete(string(data), key)
		if sErr != nil {
			return nil, fmt.Errorf("failed to delete config field %s: %w", key, sErr)
		}
		return []byte(v), nil
	})
	if err != nil {
		return err
	}

	if err := s.autoReload(context.Background()); err != nil {
		slog.Warn("Config file updated but failed to reload in-memory state", "error", err)
	}

	return nil
}

// UpdatePreferredModel updates preferred 模型 为 given type 和
// persists it 到 配置 文件 在 given scope. selected 模型 和
// recent-models list 是 written together 在 single 配置 写入.
//
// 写入 skips full disk reparse/reload (which would rebuild
// 提供方 catalog 和 agents 在 every 模型 switch 和 dominate selection
// latency); agents 是 refreshed separately 通过 caller (see
// UpdateAgentModel).
func (s *ConfigStore) UpdatePreferredModel(scope Scope, modelType SelectedModelType, model SelectedModel) error {
	return s.update(scope, func(c *Config) map[string]any {
		return s.updatePreferredModelFields(c, modelType, model)
	})
}

// updatePreferredModelFields builds fields map 为 persisting preferred
// 模型 change. Shared between UpdatePreferredModel 和 direct updateLocked
// callers (e.g. Load). Caller 必须 hold writeMu.
func (s *ConfigStore) updatePreferredModelFields(c *Config, modelType SelectedModelType, model SelectedModel) map[string]any {
	if c.Models == nil {
		c.Models = make(map[SelectedModelType]SelectedModel)
	}
	c.Models[modelType] = model
	s.pinPreferredModelLocked(modelType, model)

	fields := map[string]any{
		fmt.Sprintf("models.%s", modelType): model,
	}
	if updated, changed := nextRecentModels(c, modelType, model); changed {
		if c.RecentModels == nil {
			c.RecentModels = make(map[SelectedModelType][]SelectedModel)
		}
		c.RecentModels[modelType] = updated
		fields[fmt.Sprintf("recent_models.%s", modelType)] = updated
	}
	return fields
}

// SetCompactMode sets compact mode setting 和 persists it.
func (s *ConfigStore) SetCompactMode(scope Scope, enabled bool) error {
	return s.update(scope, func(c *Config) map[string]any {
		c.ensureTUI().CompactMode = enabled
		return map[string]any{"options.tui.compact_mode": enabled}
	})
}

// SetTransparentBackground sets transparent background setting 和 persists it.
func (s *ConfigStore) SetTransparentBackground(scope Scope, enabled bool) error {
	return s.update(scope, func(c *Config) map[string]any {
		c.ensureTUI().Transparent = &enabled
		return map[string]any{"options.tui.transparent": enabled}
	})
}

// SetProviderAPIKey sets API key 为 提供方 和 persists it.
// OpenAI 提供方 holds exactly one credential: storing ChatGPT
// token removes previously entered API key, 和 storing API key
// removes a previous ChatGPT login.
func (s *ConfigStore) SetProviderAPIKey(scope Scope, providerID string, apiKey any) error {
	var providerConfig ProviderConfig
	var exists bool
	var setKeyOrToken func()
	isToken := false

	switch v := apiKey.(type) {
	case string:
		if err := s.SetConfigField(scope, fmt.Sprintf("providers.%s.api_key", providerID), v); err != nil {
			return fmt.Errorf("failed to save api key to config file: %w", err)
		}
		setKeyOrToken = func() {
			providerConfig.APIKey = v
			if providerID == string(catwalk.InferenceProviderOpenAI) {
				// Either OAuth 或 API key, never both: login
				// leaves nothing usable 在 API-key side behind.
				providerConfig.OAuthToken = nil
				providerConfig.ChatGPTModels = nil
			}
			if providerID == string(catwalk.InferenceProviderXAI) {
				// Either OAuth 或 API key, never both: key
				// replaces a previous Grok login, whose refreshes would
				// otherwise overwrite the key again.
				providerConfig.OAuthToken = nil
				providerConfig.GrokModels = nil
			}
		}
		if providerID == string(catwalk.InferenceProviderOpenAI) {
			// Either OAuth 或 API key, never both: new key
			// leaves nothing usable 在 ChatGPT side behind.
			if err := s.RemoveConfigField(scope, fmt.Sprintf("providers.%s.oauth", providerID)); err != nil {
				return err
			}
			if err := s.RemoveConfigField(scope, fmt.Sprintf("providers.%s.chatgpt_models", providerID)); err != nil {
				return err
			}
		}
		if providerID == string(catwalk.InferenceProviderXAI) {
			// Either OAuth 或 API key, never both: new key
			// leaves nothing usable 在 Grok side behind.
			if err := s.RemoveConfigField(scope, fmt.Sprintf("providers.%s.oauth", providerID)); err != nil {
				return err
			}
			if err := s.RemoveConfigField(scope, fmt.Sprintf("providers.%s.grok_models", providerID)); err != nil {
				return err
			}
		}
	case *oauth.Token:
		isToken = true
		// Hold refresh 锁 across 写入 所以 peer's 在-flight
		// token exchange cannot land 在 top 的 credential 用户 just
		// obtained interactively — which would silently invalidate the
		// login they 只 just completed.
		fields := map[string]any{
			fmt.Sprintf("providers.%s.oauth", providerID): v,
		}
		if providerID != string(catwalk.InferenceProviderOpenAI) {
			fields[fmt.Sprintf("providers.%s.api_key", providerID)] = v.AccessToken
		}
		if err := s.withRefreshLock(providerID, func() error {
			return s.SetConfigFields(scope, fields)
		}); err != nil {
			return err
		}
		if providerID == string(catwalk.InferenceProviderOpenAI) {
			// Either OAuth 或 API key, never both: login retires
			// any key 那 came before it.
			if err := s.RemoveConfigField(scope, fmt.Sprintf("providers.%s.api_key", providerID)); err != nil {
				return err
			}
		}
		setKeyOrToken = func() {
			providerConfig.OAuthToken = v
			if providerID == string(catwalk.InferenceProviderOpenAI) {
				providerConfig.APIKey = ""
				return
			}
			providerConfig.APIKey = v.AccessToken
			if providerID == string(catwalk.InferenceProviderCopilot) {
				providerConfig.SetupGitHubCopilot()
			}
		}
	}

	cfg := s.Config()
	providerConfig, exists = cfg.Providers.Get(providerID)
	if exists {
		setKeyOrToken()
		cfg.Providers.Set(providerID, providerConfig)
	} else {
		var foundProvider *catwalk.Provider
		for _, p := range s.knownProviders {
			if string(p.ID) == providerID {
				foundProvider = &p
				break
			}
		}

		if foundProvider != nil {
			providerConfig = ProviderConfig{
				ID:           providerID,
				Name:         foundProvider.Name,
				BaseURL:      foundProvider.APIEndpoint,
				Type:         foundProvider.Type,
				Disable:      false,
				ExtraHeaders: make(map[string]string),
				ExtraParams:  make(map[string]string),
				Models:       foundProvider.Models,
			}
			setKeyOrToken()
		} else {
			return fmt.Errorf("provider with ID %s not found in known providers", providerID)
		}
		cfg.Providers.Set(providerID, providerConfig)
	}

	// After authenticating 用 Hyper, re-fetch 提供方 catalog 所以
	// latest models 是 available without restarting.
	if providerID == "hyper" {
		if refetchErr := s.RefetchHyperProvider(context.Background()); refetchErr != nil {
			slog.Warn("Failed to refetch Hyper provider after auth", "error", refetchErr)
		}
	}
	// After authenticating 用 ChatGPT account, fetch Codex 模型
	// catalog subscription grants 和 persist it 所以 models
	// dialog 可以 offer it beside API-key 目录.
	if providerID == string(catwalk.InferenceProviderOpenAI) && isToken {
		s.refetchOpenAIModels(context.Background(), scope)
	}
	// Same 为 Grok account: fetch 模型 catalog Grok plan
	// grants 和 persist it.
	if providerID == string(catwalk.InferenceProviderXAI) && isToken {
		s.refetchGrokModels(context.Background(), scope)
	}
	return nil
}

// fetchOpenAIModels fetches ChatGPT 模型 catalog 从 Codex
// backend. 一个 package variable 所以 tests can stub network 调用,
// matching how catwalk 和 hyper syncers 是 swappable globals.
var fetchOpenAIModels = openai.Models

// refetchOpenAIModels stores Codex 模型 catalog ChatGPT plan
// grants next 到 提供方's API-key models. Best effort: failure
// leaves existing catalog 在 place 和 login still succeeds.
func (s *ConfigStore) refetchOpenAIModels(ctx context.Context, scope Scope) {
	const providerID = string(catwalk.InferenceProviderOpenAI)
	pc, ok := s.Config().Providers.Get(providerID)
	if !ok || pc.OAuthToken == nil {
		return
	}
	// access token may have expired since login, 所以 renew it before
	// asking 为 catalog: models endpoint rejects stale tokens
	// 用 401. 一个 failed refresh falls through 和 lets fetch 运行
	// 在 old token, which keeps existing catalog 在 place.
	if pc.OAuthToken.IsExpired() {
		if err := s.RefreshOAuthToken(ctx, scope, providerID); err != nil {
			slog.Warn("Failed to refresh the ChatGPT token before fetching the model catalog", "error", err)
		}
		if refreshed, ok := s.Config().Providers.Get(providerID); ok && refreshed.OAuthToken != nil {
			pc = refreshed
		}
	}
	models, err := fetchOpenAIModels(ctx, pc.OAuthToken)
	if err != nil {
		slog.Warn("Failed to fetch ChatGPT model catalog after auth", "error", err)
		return
	}
	if err := s.update(scope, func(c *Config) map[string]any {
		p, ok := c.Providers.Get(providerID)
		if !ok {
			return nil
		}
		p.ChatGPTModels = models
		c.Providers.Set(providerID, p)
		return map[string]any{
			"providers.openai.chatgpt_models": models,
		}
	}); err != nil {
		slog.Warn("Failed to persist ChatGPT model catalog", "error", err)
	}
}

// RefetchOpenAIChatGPTModels fills 在 ChatGPT 模型 catalog 当
// OpenAI 提供方 是 signed 在 但 has none — because fetch 在 login
// time failed, 或 credentials predate catalog. 一个 no-op once
// catalog exists, 所以 callers can invoke it freely 在 模型 updates:
// existing catalog 是 refreshed 在 startup instead, 当 Catwalk delivers
// a new one (see Load).
func (s *ConfigStore) RefetchOpenAIChatGPTModels(ctx context.Context) {
	cfg := s.Config()
	pc, ok := cfg.Providers.Get(string(catwalk.InferenceProviderOpenAI))
	if !ok || pc.OAuthToken == nil || len(pc.ChatGPTModels) > 0 {
		return
	}
	s.refetchOpenAIModels(ctx, ScopeGlobal)
}

// fetchGrokModels fetches Grok 模型 catalog 从 xAI API. 一个
// package variable 所以 tests can stub network 调用, matching how
// catwalk 和 hyper syncers 是 swappable globals.
var fetchGrokModels = grok.Models

// refetchGrokModels stores 模型 catalog Grok plan grants next
// 到 提供方's API-key models. Best effort: failure leaves
// existing catalog 在 place 和 login still succeeds.
func (s *ConfigStore) refetchGrokModels(ctx context.Context, scope Scope) {
	const providerID = string(catwalk.InferenceProviderXAI)
	pc, ok := s.Config().Providers.Get(providerID)
	if !ok || pc.OAuthToken == nil {
		return
	}
	// access token may have expired since login, 所以 renew it before
	// asking 为 catalog: models endpoint rejects stale tokens
	// 用 401. 一个 failed refresh falls through 和 lets fetch 运行
	// 在 old token, which keeps existing catalog 在 place.
	if pc.OAuthToken.IsExpired() {
		if err := s.RefreshOAuthToken(ctx, scope, providerID); err != nil {
			slog.Warn("Failed to refresh the Grok token before fetching the model catalog", "error", err)
		}
		if refreshed, ok := s.Config().Providers.Get(providerID); ok && refreshed.OAuthToken != nil {
			pc = refreshed
		}
	}
	models, err := fetchGrokModels(ctx, pc.OAuthToken)
	if err != nil {
		slog.Warn("Failed to fetch Grok model catalog after auth", "error", err)
		return
	}
	if err := s.update(scope, func(c *Config) map[string]any {
		p, ok := c.Providers.Get(providerID)
		if !ok {
			return nil
		}
		p.GrokModels = models
		c.Providers.Set(providerID, p)
		return map[string]any{
			"providers.xai.grok_models": models,
		}
	}); err != nil {
		slog.Warn("Failed to persist Grok model catalog", "error", err)
	}
}

// RefetchGrokModels fills 在 Grok 模型 catalog 当 xAI
// 提供方 是 signed 在 但 has none — because fetch 在 login time
// failed, 或 credentials predate catalog. 一个 no-op once
// catalog exists, 所以 callers can invoke it freely 在 模型 updates:
// existing catalog 是 refreshed 在 startup instead, 当 Catwalk
// delivers a new one (see Load).
func (s *ConfigStore) RefetchGrokModels(ctx context.Context) {
	cfg := s.Config()
	pc, ok := cfg.Providers.Get(string(catwalk.InferenceProviderXAI))
	if !ok || pc.OAuthToken == nil || len(pc.GrokModels) > 0 {
		return
	}
	s.refetchGrokModels(ctx, ScopeGlobal)
}

// RefreshOAuthToken refreshes OAuth token 为 given 提供方.
//
// Providers like Hyper rotate 刷新 tokens: each exchange consumes
// caller's refresh token, issues new pair, 和 revokes old one. If
// two evo instances (或 two goroutines) refresh concurrently 用
// same 存储 refresh token, second exchange reuses already-revoked
// token, trips 提供方's reuse detection, 和 revokes entire
// token family — leaving both 用 dead tokens even though each refresh
// "succeeded".
//
// To prevent 那, refreshes 是 single-flighted 在 two levels:
//
// - In-process: refreshSF collapses concurrent goroutines 为 same
// 提供方 into one attempt.
// - Cross-process: per-提供方 advisory 锁 是 held across whole
// 读取-decide-exchange-写入 cycle, 所以 只 one process exchanges 在
// time. 一个 process 那 acquires 锁 after peer rotated finds
// peer's fresh token 在 disk 和 adopts it instead 的 exchanging.
func (s *ConfigStore) RefreshOAuthToken(ctx context.Context, scope Scope, providerID string) error {
	key := fmt.Sprintf("%d\x00%s", scope, providerID)
	_, err, _ := s.refreshSF.Do(key, func() (any, error) {
		return nil, s.refreshOAuthTokenLocked(ctx, scope, providerID)
	})
	return err
}

// refreshOAuthTokenLocked performs the cross-process single-flighted
// refresh. 它 是 invoked through refreshSF, 所以 在 most one goroutine per
// 提供方 runs it 在 time within 这 process.
func (s *ConfigStore) refreshOAuthTokenLocked(ctx context.Context, scope Scope, providerID string) error {
	cfg := s.Config()
	providerConfig, exists := cfg.Providers.Get(providerID)
	if !exists {
		return fmt.Errorf("provider %s not found", providerID)
	}
	if providerConfig.OAuthToken == nil {
		return fmt.Errorf("provider %s does not have an OAuth token", providerID)
	}
	entryToken := providerConfig.OAuthToken

	// Acquire per-提供方 cross-process refresh 锁. 这 是
	// dedicated 锁 文件, 不 配置-写入 锁, 和 it does 不 take
	// s.mu — 所以 network exchange below cannot stall unrelated 配置
	// operations. 期限 exceeds exchange 超时 所以 那 peer
	// mid-exchange has time 到 发布 token we can adopt. 锁 ordering:
	// refresh 锁 是 always taken before 配置-写入 锁 (via
	// SetConfigFields), never reverse, 所以 no deadlock 是 possible.
	lockCtx, cancel := context.WithTimeout(ctx, refreshLockDeadline)
	defer cancel()
	release, lockErr := lock.File(lockCtx, s.refreshLockPath(providerID))
	if lockErr != nil {
		// Could 不 acquire 锁 (peer wedged 或 期限 hit). Prefer
		// usable token already 在 disk over forcing our own exchange, which
		// would risk reusing rotated 刷新令牌.
		if diskToken := s.usableDiskToken(scope, providerID, entryToken); diskToken != nil {
			slog.Warn("Refresh lock unavailable; adopting token from disk", "provider", providerID, "error", lockErr)
			return s.applyToken(providerConfig, diskToken, providerID)
		}
		return fmt.Errorf("acquire refresh lock for provider %s: %w", providerID, lockErr)
	}
	defer release()

	// Now 那 we hold 锁, disk 是 authority 在 which credential
	// 是 current: peer may have rotated ours away while we waited. Adopt
	// newer token outright 当 it 是 still usable, 和 otherwise switch
	// 到 它的 refresh token 为 exchange below. Presenting our own
	// already-rotated refresh token would trip 提供方's reuse
	// detection 和 revoke whole family, forcing interactive login.
	if diskToken := s.newerDiskToken(scope, providerID, entryToken); diskToken != nil {
		if !diskToken.IsExpired() {
			slog.Info("Adopting token refreshed by another session", "provider", providerID)
			return s.applyToken(providerConfig, diskToken, providerID)
		}
		slog.Info("Exchanging with refresh token rotated by another session", "provider", providerID)
		entryToken = diskToken
	}

	// Disk still holds our token (或 no newer peer token exists) 和 we hold
	// 锁, 所以 we 是 sole exchanger. Perform exchange.
	refreshedToken, refreshErr := s.exchange(ctx, providerID, entryToken.RefreshToken)
	if refreshErr != nil {
		// exchange may have failed because peer rotated 刷新
		// token 在 窗口 we did 不 cover. Re-check disk: adopt usable
		// token, 或 重试 once 用 peer's newer refresh token.
		if diskToken := s.newerDiskToken(scope, providerID, entryToken); diskToken != nil {
			if !diskToken.IsExpired() {
				slog.Info("Adopting token refreshed by another session after exchange failure", "provider", providerID)
				return s.applyToken(providerConfig, diskToken, providerID)
			}
			slog.Info("Retrying exchange with refresh token rotated by another session", "provider", providerID)
			refreshedToken, refreshErr = s.exchange(ctx, providerID, diskToken.RefreshToken)
		}
	}
	if refreshErr != nil {
		return fmt.Errorf("failed to refresh OAuth token for provider %s: %w", providerID, refreshErr)
	}

	slog.Info("Successfully refreshed OAuth token", "provider", providerID)
	if err := s.applyToken(providerConfig, refreshedToken, providerID); err != nil {
		return err
	}

	if err := s.SetConfigFields(scope, tokenFields(providerID, refreshedToken)); err != nil {
		return fmt.Errorf("failed to persist refreshed token: %w", err)
	}
	return nil
}

// tokenFields builds 配置 fields 那 persist OAuth token.
// OpenAI 提供方 does 不 mirror access token into api_key 所以
// manually entered API key can coexist 用 ChatGPT login.
func tokenFields(providerID string, token *oauth.Token) map[string]any {
	fields := map[string]any{
		fmt.Sprintf("providers.%s.oauth", providerID): token,
	}
	if providerID != string(catwalk.InferenceProviderOpenAI) {
		fields[fmt.Sprintf("providers.%s.api_key", providerID)] = token.AccessToken
	}
	return fields
}

// WaitForTokenChange blocks until SignalAuthComplete 是 called 为
// given 提供方 或 上下文 是 cancelled. 它 是 used 通过 OnAuthRefresh
// callbacks 到 wait 为 interactive re-authentication 到 完成 before
// retrying failed 请求. 渠道 是 created atomically 用
// wait registration 所以 concurrent SignalAuthComplete cannot miss it.
func (s *ConfigStore) WaitForTokenChange(ctx context.Context, providerID string) error {
	s.authSignalMu.Lock()
	ch, ok := s.authSignals[providerID]
	if !ok {
		ch = make(chan struct{})
		if s.authSignals == nil {
			s.authSignals = make(map[string]chan struct{})
		}
		s.authSignals[providerID] = ch
	}
	s.authSignalMu.Unlock()

	select {
	case <-ch:
		// Remove consumed signal 所以 subsequent
		// SignalAuthComplete does 不 close already-closed
		// 渠道.
		s.authSignalMu.Lock()
		if s.authSignals[providerID] == ch {
			delete(s.authSignals, providerID)
		}
		s.authSignalMu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SignalAuthComplete unblocks any goroutine waiting 在 WaitForTokenChange
// 为 given 提供方. If no waiter exists yet, it pre-creates 和
// immediately closes 渠道 所以 subsequent WaitForTokenChange
// returns without blocking. 这 eliminates race where signal
// fires before the waiter registers.
func (s *ConfigStore) SignalAuthComplete(providerID string) {
	s.authSignalMu.Lock()
	defer s.authSignalMu.Unlock()
	if ch, ok := s.authSignals[providerID]; ok {
		delete(s.authSignals, providerID)
		select {
		case <-ch:
			// Already closed 通过 previous signal; nothing 到 do.
		default:
			close(ch)
		}
	} else {
		// No waiter yet. Pre-创建 closed 渠道 所以 next
		// WaitForTokenChange returns immediately.
		if s.authSignals == nil {
			s.authSignals = make(map[string]chan struct{})
		}
		ch := make(chan struct{})
		close(ch)
		s.authSignals[providerID] = ch
	}
}

// newerDiskToken returns 在-disk token 为 提供方 当 it 是
// newer than entryToken — i.e. another 会话 (possibly 在 another
// process) has already rotated credential. 它 returns nil 当 disk
// holds nothing newer than what we started 用.
//
// Newness 是 judged 通过 expiry 作为 well 作为 identity, 所以 配置 文件 那
// somehow holds older token cannot drag us backwards. 结果 may
// itself 是 expired: providers 那 rotate refresh tokens invalidate ours
// moment peer refreshes, 所以 peer's refresh token 是 只 one
// 提供方 will still 受理 even after 它的 access token ages out.
// Callers decide whether 到 adopt token wholesale 或 merely borrow 它的
// 刷新令牌.
func (s *ConfigStore) newerDiskToken(scope Scope, providerID string, entryToken *oauth.Token) *oauth.Token {
	diskToken, err := s.loadTokenFromDisk(scope, providerID)
	if err != nil {
		slog.Warn("Failed to read token from config file", "provider", providerID, "error", err)
		return nil
	}
	if diskToken == nil {
		return nil
	}
	if diskToken.AccessToken == entryToken.AccessToken {
		// Same token we started 用; nobody rotated since.
		return nil
	}
	if diskToken.RefreshToken == "" && entryToken.RefreshToken != "" {
		// Adopting would strand us 用 no way 到 refresh later, 和
		// there 是 nothing 到 borrow 为 exchange.
		return nil
	}
	if diskToken.ExpiresAt < entryToken.ExpiresAt {
		// Older than ours; nothing 到 gain 从 adopting it.
		return nil
	}
	return diskToken
}

// usableDiskToken returns 在-disk token 只 当 it 是 both newer
// than entryToken 和 still valid, meaning it can 是 adopted 作为-是 用
// no exchange 在 all.
func (s *ConfigStore) usableDiskToken(scope Scope, providerID string, entryToken *oauth.Token) *oauth.Token {
	diskToken := s.newerDiskToken(scope, providerID, entryToken)
	if diskToken == nil || diskToken.IsExpired() {
		return nil
	}
	return diskToken
}

// rejectedRefreshTokenTTL 是 how long refused refresh token 是 answered
// 从 memory. 它 是 short 所以 one mistaken 拒绝 cannot outlast outage.
const rejectedRefreshTokenTTL = 5 * time.Minute

type rejectedRefresh struct {
	err   error
	until time.Time
}

// exchange performs 提供方-specific OAuth token exchange, answering
// 从 memory 为 refresh token 提供方 recently refused.
func (s *ConfigStore) exchange(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error) {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	s.rejectedMu.Lock()
	rejected, ok := s.rejectedRefreshTokens[refreshToken]
	if ok && !now().Before(rejected.until) {
		delete(s.rejectedRefreshTokens, refreshToken)
		ok = false
	}
	s.rejectedMu.Unlock()
	if ok {
		return nil, rejected.err
	}
	token, err := s.exchangeWithProvider(ctx, providerID, refreshToken)
	var exchangeErr *oauth.TokenExchangeError
	if errors.As(err, &exchangeErr) && exchangeErr.IsRefreshTokenRevoked() {
		s.rejectedMu.Lock()
		if s.rejectedRefreshTokens == nil {
			s.rejectedRefreshTokens = make(map[string]rejectedRefresh)
		}
		s.rejectedRefreshTokens[refreshToken] = rejectedRefresh{err: err, until: now().Add(rejectedRefreshTokenTTL)}
		s.rejectedMu.Unlock()
	}
	return token, err
}

// exchangeWithProvider calls 提供方's token endpoint. Tests may
// override it via exchangeToken 字段.
func (s *ConfigStore) exchangeWithProvider(ctx context.Context, providerID, refreshToken string) (*oauth.Token, error) {
	if s.exchangeToken != nil {
		return s.exchangeToken(ctx, providerID, refreshToken)
	}
	switch providerID {
	case string(catwalk.InferenceProviderCopilot):
		return copilot.RefreshToken(ctx, refreshToken)
	case string(catwalk.InferenceProviderOpenAI):
		return openai.RefreshToken(ctx, refreshToken)
	case string(catwalk.InferenceProviderXAI):
		return grok.RefreshToken(ctx, refreshToken)
	case hyperp.Name:
		return hyper.ExchangeToken(ctx, refreshToken)
	default:
		return nil, fmt.Errorf("OAuth refresh not supported for provider %s", providerID)
	}
}

// withRefreshLock runs fn while holding per-提供方 cross-process
// refresh 锁, 所以 credential 写入 cannot interleave 用 peer's
// token exchange. Acquisition 是 best effort: 当 锁 cannot 是
// taken 在 time, fn runs anyway rather than blocking 写入 用户 是
// waiting 在.
func (s *ConfigStore) withRefreshLock(providerID string, fn func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), credentialWriteLockDeadline)
	defer cancel()
	release, err := lock.File(ctx, s.refreshLockPath(providerID))
	if err != nil {
		slog.Warn("Writing credentials without the refresh lock", "provider", providerID, "error", err)
		return fn()
	}
	defer release()
	return fn()
}

// refreshLockPath returns 路径 到 per-提供方 cross-process refresh
// 锁 文件. 锁 files live under dedicated locks/ subdirectory 的
// data dir 所以 they do 不 clutter 配置 目录. 文件 是 created
// 在 demand 通过 锁.文件 和 是 never removed (flock keys 在 inode, 不
// 路径).
func (s *ConfigStore) refreshLockPath(providerID string) string {
	dir := filepath.Join(filepath.Dir(s.globalDataPath), "locks")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, fmt.Sprintf("%s.refresh.lock", providerID))
}

// applyToken updates 在-memory 提供方 配置 用 given token.
func (s *ConfigStore) applyToken(providerConfig ProviderConfig, token *oauth.Token, providerID string) error {
	providerConfig.OAuthToken = token
	// OpenAI 提供方 holds exactly one credential, 所以 ChatGPT
	// token means there 是 no API key side 通过 side 用 it.
	if providerID != string(catwalk.InferenceProviderOpenAI) {
		providerConfig.APIKey = token.AccessToken
	}
	if providerID == string(catwalk.InferenceProviderCopilot) {
		providerConfig.SetupGitHubCopilot()
	}
	s.Config().Providers.Set(providerID, providerConfig)
	return nil
}

// loadTokenFromDisk reads OAuth token 为 given 提供方 从
// 配置 文件 在 disk. Returns nil 如果 token 是 不 found 或 matches
// current 在-memory token.
func (s *ConfigStore) loadTokenFromDisk(scope Scope, providerID string) (*oauth.Token, error) {
	path, err := s.configPath(scope)
	if err != nil {
		return nil, err
	}

	data, err := readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	oauthKey := fmt.Sprintf("providers.%s.oauth", providerID)
	oauthResult := gjson.Get(string(data), oauthKey)
	if !oauthResult.Exists() {
		return nil, nil
	}

	var token oauth.Token
	if err := json.Unmarshal([]byte(oauthResult.Raw), &token); err != nil {
		return nil, err
	}

	if token.AccessToken == "" {
		return nil, nil
	}

	return &token, nil
}

// nextRecentModels computes recent-models list 为 given type
// after recording supplied 模型 在 front, operating 在
// provided 配置 without persisting anything. 它 returns new slice
// 和 whether it differs 从 cfg's current list. Callers fold 结果
// into clone they 是 about 到 发布.
func nextRecentModels(cfg *Config, modelType SelectedModelType, model SelectedModel) ([]SelectedModel, bool) {
	if model.Provider == "" || model.Model == "" {
		return nil, false
	}

	eq := func(a, b SelectedModel) bool {
		return a.Provider == b.Provider && a.Model == b.Model
	}

	entry := SelectedModel{
		Provider: model.Provider,
		Model:    model.Model,
	}

	current := cfg.RecentModels[modelType]
	withoutCurrent := slices.DeleteFunc(slices.Clone(current), func(existing SelectedModel) bool {
		return eq(existing, entry)
	})

	updated := append([]SelectedModel{entry}, withoutCurrent...)
	if len(updated) > maxRecentModelsPerType {
		updated = updated[:maxRecentModelsPerType]
	}

	if slices.EqualFunc(current, updated, eq) {
		return current, false
	}

	return updated, true
}

// NewTestStore creates ConfigStore 为 testing purposes.
func NewTestStore(cfg *Config, loadedPaths ...string) *ConfigStore {
	return &ConfigStore{
		config:      cfg,
		loadedPaths: loadedPaths,
		resolver:    NewShellVariableResolver(env.New()),
	}
}

// ImportCopilot attempts 到 import GitHub Copilot token 从 disk.
func (s *ConfigStore) ImportCopilot() (*oauth.Token, bool) {
	if s.HasConfigField(ScopeGlobal, "providers.copilot.api_key") || s.HasConfigField(ScopeGlobal, "providers.copilot.oauth") {
		return nil, false
	}

	diskToken, hasDiskToken := copilot.RefreshTokenFromDisk()
	if !hasDiskToken {
		return nil, false
	}

	slog.Info("Found existing GitHub Copilot token on disk. Authenticating...")
	token, err := copilot.RefreshToken(context.TODO(), diskToken)
	if err != nil {
		slog.Error("Unable to import GitHub Copilot token", "error", err)
		return nil, false
	}

	if err := s.SetProviderAPIKey(ScopeGlobal, string(catwalk.InferenceProviderCopilot), token); err != nil {
		return token, false
	}

	if err := s.SetConfigFields(ScopeGlobal, map[string]any{
		"providers.copilot.api_key": token.AccessToken,
		"providers.copilot.oauth":   token,
	}); err != nil {
		slog.Error("Unable to save GitHub Copilot token to disk", "error", err)
	}

	slog.Info("GitHub Copilot successfully imported")
	return token, true
}

// StalenessResult contains 结果 的 staleness check.
type StalenessResult struct {
	Dirty   bool
	Changed []string
	Missing []string
	Errors  map[string]error // stat errors by path
}

// ConfigStaleness checks whether any tracked 配置 files have changed 在 disk
// since last snapshot. Returns dirty=true 如果 any files changed 或 went
// missing, along 用 sorted lists 的 affected paths. Stat errors 是
// captured 在 Errors map 但 still treated 作为 non-existence 为 dirty detection.
func (s *ConfigStore) ConfigStaleness() StalenessResult {
	var result StalenessResult
	result.Errors = make(map[string]error)

	for _, path := range s.trackedConfigPaths {
		snapshot, hadSnapshot := s.snapshots[path]

		info, err := os.Stat(path)
		exists := err == nil && !info.IsDir()

		if err != nil && !os.IsNotExist(err) {
			// Capture 权限/IO errors separately 从 non-existence
			result.Errors[path] = err
			result.Dirty = true
		}

		if !exists {
			if hadSnapshot && snapshot.Exists {
				// 文件 existed before 但 now missing
				result.Missing = append(result.Missing, path)
				result.Dirty = true
			}
			continue
		}

		// 文件 exists now
		if !hadSnapshot || !snapshot.Exists {
			// 文件 didn't exist before 但 does now
			result.Changed = append(result.Changed, path)
			result.Dirty = true
			continue
		}

		// Check 为 content 或 metadata changes
		if snapshot.Size != info.Size() || snapshot.ModTime != info.ModTime().UnixNano() {
			result.Changed = append(result.Changed, path)
			result.Dirty = true
		}
	}

	// Sort 为 deterministic output
	slices.Sort(result.Changed)
	slices.Sort(result.Missing)

	return result
}

// RefreshStalenessSnapshot captures fresh snapshots 的 all tracked 配置 files.
// 调用 这 after reloading 配置 到 clear dirty state.
func (s *ConfigStore) RefreshStalenessSnapshot() error {
	if s.snapshots == nil {
		s.snapshots = make(map[string]fileSnapshot)
	}

	for _, path := range s.trackedConfigPaths {
		info, err := os.Stat(path)
		exists := err == nil && !info.IsDir()

		snapshot := fileSnapshot{
			Path:   path,
			Exists: exists,
		}

		if exists {
			snapshot.Size = info.Size()
			snapshot.ModTime = info.ModTime().UnixNano()
		}

		s.snapshots[path] = snapshot
	}

	return nil
}

// CaptureStalenessSnapshot captures snapshots 为 given paths, building
// tracked 配置 paths list. Paths 是 deduplicated 和 normalized.
func (s *ConfigStore) CaptureStalenessSnapshot(paths []string) {
	// Build unique set 的 normalized paths
	seen := make(map[string]struct{})
	for _, p := range paths {
		if p == "" {
			continue
		}
		// Normalize 路径
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		seen[abs] = struct{}{}
	}

	// Also track 工作区 和 global 配置 paths 如果 set
	if s.workspacePath != "" {
		abs, err := filepath.Abs(s.workspacePath)
		if err == nil {
			seen[abs] = struct{}{}
		}
	}
	if s.globalDataPath != "" {
		abs, err := filepath.Abs(s.globalDataPath)
		if err == nil {
			seen[abs] = struct{}{}
		}
	}

	// Build sorted list 为 deterministic ordering
	s.trackedConfigPaths = make([]string, 0, len(seen))
	for p := range seen {
		s.trackedConfigPaths = append(s.trackedConfigPaths, p)
	}
	slices.Sort(s.trackedConfigPaths)

	// Capture initial snapshots
	s.RefreshStalenessSnapshot()
}

// captureStalenessSnapshot 是 alias 为 CaptureStalenessSnapshot 为 internal use.
func (s *ConfigStore) captureStalenessSnapshot(paths []string) {
	s.CaptureStalenessSnapshot(paths)
}

// ReloadFromDisk re-runs 配置 load/merge flow 和 updates 在-memory
// 配置 atomically. 它 rebuilds staleness snapshot after successful reload.
// On failure, 存储 state 是 rolled back 到 它的 previous state.
// Concurrent calls 是 serialised via writeMu.
func (s *ConfigStore) ReloadFromDisk(ctx context.Context) error {
	if s.workingDir == "" {
		return fmt.Errorf("cannot reload: working directory not set")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.reloadFromDiskLocked(ctx)
}

// reloadFromDiskLocked performs actual reload. Caller 必须 hold writeMu.
func (s *ConfigStore) reloadFromDiskLocked(ctx context.Context) error {
	// Migrate deprecated disable_notifications before reloading 配置.
	migrateDisableNotifications()

	configPaths := lookupConfigs(s.workingDir)
	cfg, loadedPaths, err := loadFromConfigPaths(ctx, configPaths)
	if err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}

	// Apply defaults (using existing data 目录 如果 set)
	var dataDir string
	if cur := s.Config(); cur != nil && cur.Options != nil {
		dataDir = cur.Options.DataDirectory
	}
	cfg.setDefaults(s.workingDir, dataDir)

	// Merge 工作区 配置 如果 present
	workspacePath := filepath.Join(cfg.Options.DataDirectory, fmt.Sprintf("%s.json", appName))
	if wsData, err := readFile(workspacePath); err == nil && len(wsData) > 0 {
		if !json.Valid(wsData) {
			return fmt.Errorf("invalid JSON in config file %s", workspacePath)
		}
		wsData = migrateCatwalkV2File(workspacePath, wsData)
		merged, mergeErr := loadFromBytes(append([][]byte{mustMarshalConfig(cfg)}, wsData))
		if mergeErr == nil {
			dataDir := cfg.Options.DataDirectory
			*cfg = *merged
			cfg.setDefaults(s.workingDir, dataDir)
			loadedPaths = append(loadedPaths, workspacePath)
		}
	}

	// Validate hooks after all 配置 merging 是 完成 所以 matcher
	// regexes 是 recompiled 在 reloaded 配置 (mirrors Load).
	if err := cfg.ValidateHooks(); err != nil {
		return fmt.Errorf("invalid hook configuration on reload: %w", err)
	}

	// Save current state 为 potential rollback BEFORE configureProviders,
	// which may 写入 到 disk via RemoveConfigField (e.g. removing stale
	// OAuth providers). Capturing after would snapshot 配置 那 has
	// already been mutated, 和 rollback would restore corrupted state.
	oldConfig := s.Config()
	oldLoadedPaths := s.loadedPaths
	oldResolver := s.resolver
	oldKnownProviders := s.knownProviders
	oldOverrides := s.overrides
	oldWorkspacePath := s.workspacePath

	// Preserve runtime overrides
	overrides := s.overrides

	// Reapply 模型 choices made 在 这 instance. global 配置 文件 是
	// shared, 所以 it may now name 模型 sibling instance selected;
	// reload triggered 通过 unrelated 写入 must 不 swap 用户's 模型
	// mid-会话. An external edit 到 配置 still takes effect 为 any
	// 模型 type 这 instance never chose.
	maps.Copy(cfg.Models, overrides.Models)

	// Reconfigure providers
	env := env.New()
	resolver := NewShellVariableResolver(env, WithWorkingDir(s.workingDir))

	// Apply top-level env vars before configuring providers 所以 variables
	// like AWS_PROFILE 是 visible 到 AWS SDK credential chain.
	cfg.applyEnv(resolver)

	providers, err := Providers(cfg)
	if err != nil {
		if len(providers) == 0 {
			return fmt.Errorf("failed to load providers during reload: %w", err)
		}
		slog.Warn("Reload continuing with the previously known providers", "error", err)
	}

	if err := cfg.configureProviders(ctx, s, env, resolver, providers); err != nil {
		return fmt.Errorf("failed to configure providers during reload: %w", err)
	}

	// 更新 存储 state BEFORE running 模型/agent setup (所以 they see new 配置)
	s.setConfig(cfg)
	s.loadedPaths = loadedPaths
	s.resolver = resolver
	s.knownProviders = providers
	s.overrides = overrides
	s.workspacePath = workspacePath

	// Mirror startup flow: setup models 和 agents against NEW 配置.
	var setupErr error
	if !cfg.IsConfigured() {
		slog.Warn("No providers configured after reload")
	} else {
		resolved, resolveErr := resolveSelectedModels(cfg, providers)
		if resolveErr != nil {
			setupErr = fmt.Errorf("failed to configure selected models during reload: %w", resolveErr)
		} else {
			cfg.Models[SelectedModelTypeLarge] = resolved.Large
			cfg.Models[SelectedModelTypeSmall] = resolved.Small
			s.SetupAgents()
		}
	}

	// Rollback 在 setup failure
	if setupErr != nil {
		s.setConfig(oldConfig)
		s.loadedPaths = oldLoadedPaths
		s.resolver = oldResolver
		s.knownProviders = oldKnownProviders
		s.overrides = oldOverrides
		s.workspacePath = oldWorkspacePath
		return setupErr
	}

	// Rebuild staleness tracking. Track every discovered 配置 路径, 不
	// just ones 那 loaded, 所以 配置 文件 created after 这 reload
	// 是 detected 作为 change 在 next staleness check.
	s.captureStalenessSnapshot(append(slices.Clone(configPaths), loadedPaths...))

	return nil
}

// autoReload conditionally reloads 配置 从 disk after writes.
// 它 returns nil (no 错误) 为 expected skip cases: 当 auto-reload 是
// disabled during load/reload flows, 或 当 working 目录 是 不 set
// (e.g., during testing). Only actual reload failures return 错误.
func (s *ConfigStore) autoReload(ctx context.Context) error {
	if s.workingDir == "" {
		return nil // Expected skip: working directory not set
	}
	// Skip 如果 reload 是 already 在 progress. 这 handles both
	// concurrent auto-reloads after parallel writes 和 re-entrant
	// calls 从 configureProviders during reload.
	//
	// Note: 如果 写入 completes after 在-progress reload has
	// already 读取 配置 文件, 那 写入 won't 是 reflected 在
	// memory until next reload. 这 是 acceptable because writes
	// 是 rare 和 next 用户 action 或 文件-watch tick will pick
	// up change. Callers 那 need guaranteed fresh state after
	// 写入 should 调用 ReloadFromDisk explicitly.
	if !s.writeMu.TryLock() {
		return nil
	}
	defer s.writeMu.Unlock()
	return s.reloadFromDiskLocked(ctx)
}
