package mcp

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/charmbracelet/crush/internal/config"
)

// reinitAction describes how 到 reconcile one MCP 服务端 against
// current 配置.
type reinitAction int

const (
	reinitDisable reinitAction = iota + 1
	reinitRemove
	reinitStart
)

// reconcile diffs running MCP state against current 配置 和 returns
// action 到 take 为 each 服务端. 它 是 pure function: all state 是
// passed 在, nothing global 是 读取 或 mutated, 所以 reconciliation
// decision can 是 tested 用 plain maps.
//
// 配置 服务端 last connected 用 lives 在 它的 ClientInfo (配置),
// 和 配置 在-flight attempt 是 connecting 用 lives 在
// PendingConfig. Reconcile compares live 配置 against whichever 是
// relevant 为 服务端's state:
//
// - 一个 starting 服务端 是 left alone 只 while it 是 connecting 用
// current 配置. If 配置 changed since it started, it restarts 所以
// new 配置 takes effect instead 的 being lost 到 在-flight
//     attempt.
// - 一个 connected 服务端 restarts 当 它的 配置 differs 从 one it
// connected 用.
//   - Every other state (new, errored, needs-auth, disabled) restarts: new
// 和 disabled servers carry no 配置, 和 retrying failed 服务端 在
// each 配置 写入 是 desired recovery 路径.
//
// Servers gone 从 配置 是 removed entirely; enabled-在-配置 servers
// marked disabled 是 disabled.
func reconcile(current config.MCPs, running map[string]ClientInfo) map[string]reinitAction {
	actions := map[string]reinitAction{}

	// Servers no longer 在 配置 是 removed entirely.
	for name := range running {
		if _, exists := current[name]; !exists {
			actions[name] = reinitRemove
		}
	}

	for name, m := range current {
		info, exists := running[name]
		if m.Disabled {
			if exists && info.State != StateDisabled {
				actions[name] = reinitDisable
			}
			continue
		}

		if exists {
			switch info.State {
			case StateStarting:
				// Restart 只 如果 配置 changed since 这 attempt
				// started; otherwise let 在-flight attempt settle 所以
				// rapid writes don't pile up overlapping init goroutines.
				if info.PendingConfig != nil && mcpConfigEqual(*info.PendingConfig, m) {
					continue
				}
			case StateConnected:
				if mcpConfigEqual(info.Config, m) {
					continue
				}
			}
		}
		actions[name] = reinitStart
	}

	return actions
}

// reinitMu guards reinitRunning 和 reinitDirty.
var (
	reinitMu      sync.Mutex
	reinitRunning bool
	reinitDirty   bool
)

// Reinitialize reconciles running MCP servers against current 配置.
// Servers added since last 调用 是 started, servers removed 是 torn
// down, 和 servers whose 配置 changed 是 restarted. Unchanged servers
// keep their existing sessions.
//
// MCP state 是 process-global, 所以 reconciliation 是 single-flighted: 在
// most one runs 在 time. 一个 配置 写入 那 arrives mid-运行 just sets
// dirty flag 和 returns; running reconciliation loops once more 到
// pick up newer state. 这 coalesces burst 的 rapid writes into 在
// most two reconciles instead 的 queueing redundant no-op pass per 写入,
// while still guaranteeing final state reflects latest 配置.
func Reinitialize(ctx context.Context, cfg *config.ConfigStore) {
	reinitMu.Lock()
	if reinitRunning {
		reinitDirty = true
		reinitMu.Unlock()
		return
	}
	reinitRunning = true
	reinitMu.Unlock()

	for {
		reconcileOnce(ctx, cfg)

		reinitMu.Lock()
		if !reinitDirty {
			reinitRunning = false
			reinitMu.Unlock()
			return
		}
		reinitDirty = false
		reinitMu.Unlock()
	}
}

// reconcileOnce applies one reconciliation pass against current 配置.
func reconcileOnce(ctx context.Context, cfg *config.ConfigStore) {
	current := cfg.Config().MCP
	actions := reconcile(current, states.Copy())
	for name, action := range actions {
		switch action {
		case reinitRemove:
			slog.Info("Removing MCP server no longer in config", "name", name)
			removeServer(name)
		case reinitDisable:
			slog.Info("Disabling MCP server", "name", name)
			DisableSingle(cfg, name)
		case reinitStart:
			m := current[name]
			if _, exists := states.Get(name); exists {
				slog.Info("Re-initializing MCP server after config change", "name", name)
			} else {
				slog.Info("Initializing new MCP server after config change", "name", name)
			}
			// teardown bumps generation, invalidating any 在-flight
			// attempt 为 这 服务端. StateStarting transition records
			// m 作为 PendingConfig 所以 subsequent reconcile can tell whether
			// attempt now 在 flight matches latest 配置.
			teardown(name)
			updateState(name, StateStarting, nil, nil, Counts{}, withPending(m), withChannelOptIn(cfg, name, m))
			goInitClient(ctx, cfg, name, m, nil)
		}
	}
}

// removeServer fully tears down MCP 服务端 和 deletes 它的 state
// 条目. Unlike DisableSingle (which keeps 条目 作为 StateDisabled),
// 这 是 为 servers 那 no longer exist 在 配置 在 all.
func removeServer(name string) {
	teardown(name)
	states.Del(name)
	gens.Del(name)
}

// mcpConfigEqual reports whether two MCPConfig values 是 equal, ignoring
// internally-managed OAuthToken 字段. 字段-通过-字段 rather than
// reflect.DeepEqual 所以 comparison 是 explicit about what matters.
// TestMCPConfigEqualExhaustive guards against drift: it fails 在 测试
// time 如果 new 字段 是 added 到 MCPConfig without decision about
// whether it participates here.
func mcpConfigEqual(a, b config.MCPConfig) bool {
	return a.Command == b.Command &&
		maps.Equal(a.Env, b.Env) &&
		slices.Equal(a.Args, b.Args) &&
		a.Type == b.Type &&
		a.URL == b.URL &&
		a.Disabled == b.Disabled &&
		slices.Equal(a.DisabledTools, b.DisabledTools) &&
		slices.Equal(a.EnabledTools, b.EnabledTools) &&
		a.Timeout == b.Timeout &&
		boolPtrEqual(a.Sessionless, b.Sessionless) &&
		maps.Equal(a.Headers, b.Headers) &&
		a.OAuth == b.OAuth &&
		a.OAuthClientID == b.OAuthClientID &&
		a.OAuthClientSecret == b.OAuthClientSecret &&
		a.OAuthCallbackPort == b.OAuthCallbackPort &&
		a.ChannelEnabled == b.ChannelEnabled
}

// boolPtrEqual compares two *bool 通过 值, treating two nils 作为 equal.
func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
