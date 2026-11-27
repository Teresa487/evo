package skills

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTracker_MarkLoadedAndIsLoaded(t *testing.T) {
	t.Parallel()

	activeSkills := []*Skill{
		{Name: "go-doc"},
		{Name: "bash"},
	}
	tracker := NewTracker(activeSkills)

	// Initially 不 loaded.
	require.False(t, tracker.IsLoaded("go-doc"))
	require.False(t, tracker.IsLoaded("bash"))

	// 标记 作为 loaded.
	tracker.MarkLoaded("go-doc")
	require.True(t, tracker.IsLoaded("go-doc"))
	require.False(t, tracker.IsLoaded("bash"))

	// 标记 another.
	tracker.MarkLoaded("bash")
	require.True(t, tracker.IsLoaded("go-doc"))
	require.True(t, tracker.IsLoaded("bash"))
}

func TestTracker_NonActiveSkillCannotBeMarkedLoaded(t *testing.T) {
	t.Parallel()

	activeSkills := []*Skill{
		{Name: "go-doc"},
	}
	tracker := NewTracker(activeSkills)

	// Cannot 标记 non-活跃 skill 作为 loaded.
	tracker.MarkLoaded("bash")
	require.False(t, tracker.IsLoaded("bash"))

	// Can 标记 活跃 skill 作为 loaded.
	tracker.MarkLoaded("go-doc")
	require.True(t, tracker.IsLoaded("go-doc"))
}

func TestTracker_NilSafety(t *testing.T) {
	t.Parallel()

	var tracker *Tracker

	// Should 不 panic.
	tracker.MarkLoaded("go-doc")
	require.False(t, tracker.IsLoaded("go-doc"))
}

func TestTracker_BuiltinSkillTracking(t *testing.T) {
	t.Parallel()

	// Simulate 活跃 skills including builtin skill (crush-配置).
	activeSkills := []*Skill{
		{Name: "crush-config", Description: "Crush config", Builtin: true},
		{Name: "go-doc", Description: "Go docs", Builtin: false},
	}
	tracker := NewTracker(activeSkills)

	// Initially 不 loaded.
	require.False(t, tracker.IsLoaded("crush-config"))
	require.False(t, tracker.IsLoaded("go-doc"))

	// 标记 builtin skill 作为 loaded (simulating 读取 via crush://...).
	tracker.MarkLoaded("crush-config")
	require.True(t, tracker.IsLoaded("crush-config"))

	// 标记 用户 skill 作为 loaded.
	tracker.MarkLoaded("go-doc")
	require.True(t, tracker.IsLoaded("go-doc"))
}

func TestTracker_OverriddenBuiltinNotTracked(t *testing.T) {
	t.Parallel()

	// Simulate scenario where builtin "bash" 是 overridden 通过 用户 "bash".
	// After dedup, 只 用户 "bash" 是 活跃.
	activeSkills := []*Skill{
		{Name: "bash", Description: "User bash override", Builtin: false},
	}
	tracker := NewTracker(activeSkills)

	// Trying 到 标记 builtin "bash" 作为 loaded should 不 work
	// because 活跃 skill 是 用户 override.
	tracker.MarkLoaded("bash")
	require.True(t, tracker.IsLoaded("bash"))

	// But 如果 we somehow tried 到 标记 different builtin 那's 不 活跃,
	// it wouldn't get marked.
	tracker.MarkLoaded("nonexistent-builtin")
	require.False(t, tracker.IsLoaded("nonexistent-builtin"))
}
