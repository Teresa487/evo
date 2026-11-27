package skills

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// Manager owns per-工作区 skill discovery state: latest discovery
// snapshot, full skill metadata (用 Instructions) 为
// coordinator, 和 pubsub 总线 为 change events. There 是 exactly
// one Manager per 工作区.
//
// Package-level helpers (GetLatestStates, SetLatestStates,
// PublishStates, SubscribeEvents) 是 preserved 为 callers 那 share
// process 用 TUI. To bridge Manager 到 those globals, construct
// it 用 WithGlobalMirror. Only do 这 当 process hosts single
// 工作区 (local mode 或 客户端 process); backend 服务端 hosts
// multiple workspaces concurrently 和 must 不 enable mirroring.
type Manager struct {
	mu           sync.RWMutex
	allSkills    []*Skill
	activeSkills []*Skill
	states       []*SkillState

	// resolvedPaths 是 expanded SkillsPaths used during discovery.
	// 存储 所以 Catalog/ReadContent can label skills without
	// re-resolving.
	resolvedPaths []string
	workingDir    string

	broker       *pubsub.Broker[Event]
	globalMirror bool
}

// ManagerOption configures Manager 在 construction time.
type ManagerOption func(*Manager)

// WithGlobalMirror causes manager 到 forward SetLatestStates 和
// PublishStates calls 到 package-level 缓存 和 总线. Only safe
// 当 process hosts 在 most one Manager (e.g. local mode 或
// 客户端 process).
func WithGlobalMirror() ManagerOption {
	return func(m *Manager) {
		m.globalMirror = true
	}
}

// WithResolvedPaths stores expanded skills 目录 paths 那
// were used during discovery. Catalog 和 ReadContent use these 为
// source labelling.
func WithResolvedPaths(paths []string) ManagerOption {
	return func(m *Manager) {
		m.resolvedPaths = paths
	}
}

// WithWorkingDir stores 工作区 working 目录. Catalog 和
// ReadContent use it 到 distinguish project skills 从 用户 skills.
func WithWorkingDir(dir string) ManagerOption {
	return func(m *Manager) {
		m.workingDir = dir
	}
}

// NewManager constructs 工作区-scoped Manager 用 given
// pre-computed discovery results. slices 是 存储 作为-是; callers
// should 不 mutate them afterwards.
func NewManager(allSkills, activeSkills []*Skill, states []*SkillState, opts ...ManagerOption) *Manager {
	m := &Manager{
		allSkills:    allSkills,
		activeSkills: activeSkills,
		states:       states,
		broker:       pubsub.NewBroker[Event](),
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.globalMirror {
		SetLatestStates(states)
	}
	return m
}

// AllSkills returns deduplicated list 的 all discovered skills.
func (m *Manager) AllSkills() []*Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.allSkills
}

// ActiveSkills returns post-filter list 的 活跃 skills (after
// removing disabled entries).
func (m *Manager) ActiveSkills() []*Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeSkills
}

// ResolvedPaths returns expanded skills 目录 paths 存储 在
// construction time.
func (m *Manager) ResolvedPaths() []string {
	return m.resolvedPaths
}

// WorkingDir returns 工作区 working 目录 存储 在
// construction time.
func (m *Manager) WorkingDir() string {
	return m.workingDir
}

// States returns clone 的 latest discovery state snapshot.
func (m *Manager) States() []*SkillState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneStates(m.states)
}

// SetLatestStates updates the manager's cached discovery snapshot.
func (m *Manager) SetLatestStates(states []*SkillState) {
	m.mu.Lock()
	m.states = cloneStates(states)
	m.mu.Unlock()
	if m.globalMirror {
		SetLatestStates(states)
	}
}

// PublishStates updates manager's cached snapshot 和 publishes
// discovery 事件 到 subscribers. Callers should 不 调用
// SetLatestStates separately — PublishStates 是 single mutation
// point, keeping Manager.States(), workspaceToProto, 和 (当
// WithGlobalMirror 是 set) skills.GetLatestStates consistent 用 what
// subscribers observe.
func (m *Manager) PublishStates(states []*SkillState) {
	m.mu.Lock()
	m.states = cloneStates(states)
	m.mu.Unlock()
	if m.globalMirror {
		SetLatestStates(states)
	}
	m.broker.Publish(pubsub.UpdatedEvent, Event{States: cloneStates(states)})
	if m.globalMirror {
		PublishStates(states)
	}
}

// SubscribeEvents returns 渠道 的 discovery events 为
// manager's 工作区.
func (m *Manager) SubscribeEvents(ctx context.Context) <-chan pubsub.Event[Event] {
	return m.broker.Subscribe(ctx)
}

// Shutdown releases 总线 resources.
func (m *Manager) Shutdown() {
	if m.broker != nil {
		m.broker.Shutdown()
	}
}

// DiscoverFromConfig walks embedded builtin FS 和 every 路径 在
// cfg.Options.SkillsPaths (after home / env expansion), then dedups 和
// filters 通过 cfg.Options.DisabledSkills. 它 returns three slices
// rest 的 system needs:
//
//   - allSkills:    deduplicated, pre-filter (includes disabled).
//   - activeSkills: post-filter (DisabledSkills removed).
// - states: per-文件 discovery outcome 为 diagnostics/UI.
func DiscoverFromConfig(cfg DiscoveryConfig) (allSkills, activeSkills []*Skill, states []*SkillState) {
	builtin, builtinStates := DiscoverBuiltinWithStates()
	discovered := append([]*Skill(nil), builtin...)

	var userStates []*SkillState
	userPaths := cfg.ResolvePaths()
	if len(userPaths) > 0 {
		var userSkills []*Skill
		userSkills, userStates = DiscoverWithStates(userPaths)
		discovered = append(discovered, userSkills...)
	}

	allSkills = Deduplicate(discovered)
	activeSkills = Filter(allSkills, cfg.DisabledSkills)

	allStates := append([]*SkillState(nil), builtinStates...)
	allStates = append(allStates, userStates...)
	allStates = DeduplicateStates(allStates)
	slices.SortStableFunc(allStates, func(a, b *SkillState) int {
		return strings.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path))
	})
	return allSkills, activeSkills, allStates
}

// DiscoveryConfig contains the inputs DiscoverFromConfig needs. Using a
// dedicated struct (rather than importing internal/配置) keeps
// skills package's dependency graph small.
type DiscoveryConfig struct {
	SkillsPaths    []string
	DisabledSkills []string
	WorkingDir     string
	// Resolver expands $VAR-style references 在 paths. May 是 nil.
	Resolver func(string) (string, error)
}

// ResolvePaths expands home-目录 和 $VAR references 在
// SkillsPaths. 这 是 canonical 路径-resolution logic used 通过
// DiscoverFromConfig; callers 那 need resolved list (e.g. 为
// Catalog labels) can 调用 这 directly.
func (c DiscoveryConfig) ResolvePaths() []string {
	if len(c.SkillsPaths) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.SkillsPaths))
	for _, pth := range c.SkillsPaths {
		expanded := home.Long(pth)
		if strings.HasPrefix(expanded, "$") && c.Resolver != nil {
			if resolved, err := c.Resolver(expanded); err == nil {
				expanded = resolved
			}
		}
		out = append(out, expanded)
	}
	return out
}
