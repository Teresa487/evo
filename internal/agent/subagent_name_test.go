package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Names cycle through one shuffle: each comes up once per lap, 所以 no name
// repeats until every other one has been used.
func TestSubagentNamerCyclesThroughShuffle(t *testing.T) {
	t.Parallel()
	var n subagentNamer
	firstName := func() string {
		first, _, ok := splitSubagentName(n.name("task"))
		require.True(t, ok)
		return first.key
	}
	lap := make([]string, len(subagentFirstNames))
	seen := map[string]bool{}
	for i := range lap {
		lap[i] = firstName()
		require.False(t, seen[lap[i]], "%s came up twice in one lap", lap[i])
		seen[lap[i]] = true
	}
	for i := range lap {
		require.Equal(t, lap[i], firstName(), "the second lap repeats the first")
	}
}

// 一个 label 那 happens 到 start 用 first name, like "jan-报告", 是
// still label 和 gets first name 的 它的 own.
func TestSubagentNameAlwaysAddsAFirstName(t *testing.T) {
	t.Parallel()
	var n subagentNamer
	first, label, ok := splitSubagentName(n.name(" jan-report "))
	require.True(t, ok)
	require.Equal(t, "jan-report", label)
	require.NotEmpty(t, first.key)
	require.Empty(t, n.name("  "))
}

func TestSubagentDisplayName(t *testing.T) {
	t.Parallel()
	first, label := SubagentDisplayName("bjorn-explore-subagents-code")
	require.Equal(t, "Björn", first)
	require.Equal(t, "explore-subagents-code", label)

	first, label = SubagentDisplayName("audit")
	require.Empty(t, first)
	require.Equal(t, "audit", label)
}
