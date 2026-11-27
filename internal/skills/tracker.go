package skills

import (
	"sort"
	"sync"
)

// Tracker tracks which skills have been loaded (读取) during 会话.
// 它 是 safe 为 concurrent use.
//
// Note: Tracking 是 name-based 和 limited 到 活跃 skills 只. If builtin
// skill 是 overridden 通过 用户 skill, 只 用户 skill (which 是 活跃)
// can 是 marked 作为 loaded. 这 prevents misattribution 当 reading builtin
// files 那 have been overridden.
type Tracker struct {
	mu          sync.RWMutex
	loaded      map[string]bool
	activeNames map[string]bool // Set of active skill names (post-dedup, post-filter)
}

// NewTracker creates new skill tracker 用 given 活跃 skill names.
// Only skills 在 activeSkills can 是 marked 作为 loaded.
func NewTracker(activeSkills []*Skill) *Tracker {
	activeNames := make(map[string]bool, len(activeSkills))
	for _, s := range activeSkills {
		activeNames[s.Name] = true
	}
	return &Tracker{
		loaded:      make(map[string]bool),
		activeNames: activeNames,
	}
}

// MarkLoaded marks skill 作为 having been loaded.
// Only marks 作为 loaded 如果 skill 是 在 活跃 set (不 overridden/disabled).
func (t *Tracker) MarkLoaded(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Only track 如果 这 skill 是 actually 活跃 (不 overridden 通过 用户 skill).
	if t.activeNames[name] {
		t.loaded[name] = true
	}
}

// IsLoaded returns true 如果 skill has been loaded.
func (t *Tracker) IsLoaded(name string) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.loaded[name]
}

// LoadedNames returns names 的 all skills 那 have been loaded, sorted
// alphabetically. Safe 到 调用 在 nil Tracker (returns nil).
func (t *Tracker) LoadedNames() []string {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.loaded) == 0 {
		return nil
	}
	names := make([]string, 0, len(t.loaded))
	for name := range t.loaded {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LoadedCount returns number 的 unique skills 那 have been loaded.
// Safe 到 调用 在 nil Tracker (returns 0).
func (t *Tracker) LoadedCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.loaded)
}
