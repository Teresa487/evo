package dialog

import (
	"testing"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/sahilm/fuzzy"
	"github.com/stretchr/testify/require"
)

// versionedItem 是 cross-cutting interface every dialog list
// item 必须 satisfy under F6: every documented mutator 必须 bump
// shared version 计数器 所以 list-level memo invalidates 它的
// entries.
type versionedItem interface {
	list.Item
	Version() uint64
}

// requireBump asserts 那 running mutate() advances item's
// Version().
func requireBump(t *testing.T, name string, item versionedItem, mutate func()) {
	t.Helper()
	before := item.Version()
	mutate()
	after := item.Version()
	require.Greaterf(t, after, before, "%s must bump Version() (before=%d, after=%d)", name, before, after)
}

// requireNoBump asserts 那 running mutate() leaves item's
// Version() unchanged. Used 到 锁 在 dedupe contract:
// mutator called 用 值 identical 到 current state must
// 不 gratuitously invalidate list 缓存.
func requireNoBump(t *testing.T, name string, item versionedItem, mutate func()) {
	t.Helper()
	before := item.Version()
	mutate()
	after := item.Version()
	require.Equalf(t, before, after, "%s must NOT bump Version() when state is unchanged (before=%d, after=%d)", name, before, after)
}

// equivMatch returns fuzzy.Match whose fields 和 indexes 是
// equivalent 到 supplied seed 但 allocated 作为 fresh struct
// 所以 callers exercise 值-equality dedupe 路径 rather than
// referential equality.
func equivMatch(seed fuzzy.Match) fuzzy.Match {
	return fuzzy.Match{
		Str:            seed.Str,
		Index:          seed.Index,
		Score:          seed.Score,
		MatchedIndexes: append([]int(nil), seed.MatchedIndexes...),
	}
}

// TestCommandItem_MutatorsBumpVersion covers F6 §4.5 为
// commands palette items: SetFocused 和 SetMatch bump Version()
// 在 observable change 和 dedupe otherwise.
func TestCommandItem_MutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	item := NewCommandItem(&sty, "id", "Title", "ctrl+t", nil)

	requireBump(t, "SetFocused[true]", item, func() {
		item.SetFocused(true)
	})
	requireNoBump(t, "SetFocused[true again]", item, func() {
		item.SetFocused(true)
	})
	requireBump(t, "SetFocused[false]", item, func() {
		item.SetFocused(false)
	})

	match := fuzzy.Match{
		Str:            "Title",
		Index:          0,
		Score:          5,
		MatchedIndexes: []int{0, 1, 2},
	}
	requireBump(t, "SetMatch[new]", item, func() {
		item.SetMatch(match)
	})
	requireNoBump(t, "SetMatch[same]", item, func() {
		item.SetMatch(equivMatch(match))
	})
	requireBump(t, "SetMatch[different]", item, func() {
		item.SetMatch(fuzzy.Match{
			Str:            "Title",
			Index:          0,
			Score:          5,
			MatchedIndexes: []int{0, 2},
		})
	})
}

// TestModelItem_MutatorsBumpVersion covers F6 §4.5 为 模型
// picker items.
func TestModelItem_MutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	prov := catwalk.Provider{ID: "openai", Name: "OpenAI"}
	model := catwalk.Model{ID: "gpt-4", Name: "GPT-4"}
	item := NewModelItem(&sty, prov, model, ModelTypeLarge, true)

	requireBump(t, "SetFocused[true]", item, func() {
		item.SetFocused(true)
	})
	requireNoBump(t, "SetFocused[true again]", item, func() {
		item.SetFocused(true)
	})

	match := fuzzy.Match{
		Str:            "GPT-4",
		Index:          0,
		Score:          5,
		MatchedIndexes: []int{0, 1, 2},
	}
	requireBump(t, "SetMatch[new]", item, func() {
		item.SetMatch(match)
	})
	requireNoBump(t, "SetMatch[same]", item, func() {
		item.SetMatch(equivMatch(match))
	})
	requireBump(t, "SetMatch[different]", item, func() {
		item.SetMatch(fuzzy.Match{
			Str:            "GPT-4",
			Index:          0,
			Score:          5,
			MatchedIndexes: []int{1},
		})
	})
}

// TestSessionItem_MutatorsBumpVersion covers F6 §4.5 为
// sessions dialog items.
func TestSessionItem_MutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	item := &SessionItem{
		Versioned: list.NewVersioned(),
		Session:   session.Session{ID: "sess-1", Title: "My Session"},
		t:         &sty,
	}

	requireBump(t, "SetFocused[true]", item, func() {
		item.SetFocused(true)
	})
	requireNoBump(t, "SetFocused[true again]", item, func() {
		item.SetFocused(true)
	})

	match := fuzzy.Match{
		Str:            "My Session",
		Index:          0,
		Score:          5,
		MatchedIndexes: []int{0, 1, 2},
	}
	requireBump(t, "SetMatch[new]", item, func() {
		item.SetMatch(match)
	})
	requireNoBump(t, "SetMatch[same]", item, func() {
		item.SetMatch(equivMatch(match))
	})
	requireBump(t, "SetMatch[different]", item, func() {
		item.SetMatch(fuzzy.Match{
			Str:            "My Session",
			Index:          0,
			Score:          5,
			MatchedIndexes: []int{3, 4},
		})
	})
}

// TestReasoningItem_MutatorsBumpVersion covers F6 §4.5 为
// reasoning effort dialog items.
func TestReasoningItem_MutatorsBumpVersion(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	item := &ReasoningItem{
		Versioned: list.NewVersioned(),
		effort:    "medium",
		title:     "Medium",
		t:         &sty,
	}

	requireBump(t, "SetFocused[true]", item, func() {
		item.SetFocused(true)
	})
	requireNoBump(t, "SetFocused[true again]", item, func() {
		item.SetFocused(true)
	})

	match := fuzzy.Match{
		Str:            "Medium",
		Index:          0,
		Score:          5,
		MatchedIndexes: []int{0, 1, 2},
	}
	requireBump(t, "SetMatch[new]", item, func() {
		item.SetMatch(match)
	})
	requireNoBump(t, "SetMatch[same]", item, func() {
		item.SetMatch(equivMatch(match))
	})
	requireBump(t, "SetMatch[different]", item, func() {
		item.SetMatch(fuzzy.Match{
			Str:            "Medium",
			Index:          0,
			Score:          5,
			MatchedIndexes: []int{2, 3},
		})
	})
}
