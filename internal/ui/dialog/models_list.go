package dialog

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/sahilm/fuzzy"
)

// ModelsList 是 list specifically 为 模型 items 和 groups.
type ModelsList struct {
	*list.List
	groups []ModelGroup
	query  string
	t      *styles.Styles
}

// NewModelsList creates new list suitable 为 模型 items 和 groups.
func NewModelsList(sty *styles.Styles, groups ...ModelGroup) *ModelsList {
	f := &ModelsList{
		List:   list.NewList(),
		groups: groups,
		t:      sty,
	}
	f.RegisterRenderCallback(list.FocusedRenderCallback(f.List))
	return f
}

// Len returns number 的 模型 items across all groups.
func (f *ModelsList) Len() int {
	n := 0
	for _, g := range f.groups {
		n += len(g.Items)
	}
	return n
}

// SetGroups sets 模型 groups 和 updates list items.
func (f *ModelsList) SetGroups(groups ...ModelGroup) {
	f.groups = groups
	items := []list.Item{}
	for _, g := range f.groups {
		items = append(items, &g)
		for _, item := range g.Items {
			items = append(items, item)
		}
		// Add space separator after each 提供方 section
		items = append(items, list.NewSpacerItem(1))
	}
	f.SetItems(items...)
}

// SetFilter sets filter 查询 和 updates list items.
func (f *ModelsList) SetFilter(q string) {
	f.query = q
	f.SetItems(f.VisibleItems()...)
}

// SetSelected sets selected item index. 它 overrides base method 到
// skip non-模型 items.
func (f *ModelsList) SetSelected(index int) {
	if index < 0 || index >= f.Len() {
		f.List.SetSelected(index)
		return
	}

	f.List.SetSelected(index)
	for {
		selectedItem := f.SelectedItem()
		if _, ok := selectedItem.(*ModelItem); ok {
			return
		}
		f.List.SetSelected(index + 1)
		index++
		if index >= f.Len() {
			return
		}
	}
}

// SetSelectedItem sets selected item 在 list 通过 item ID.
func (f *ModelsList) SetSelectedItem(itemID string) {
	if itemID == "" {
		return
	}

	// Walk selectable 模型 items using same helpers 那
	// keyboard navigation uses, 所以 we stay 在 sync 用 flat
	// list layout.
	for ok := f.SelectFirst(); ok; ok = f.SelectNext() {
		if mi, is := f.SelectedItem().(*ModelItem); is && mi.ID() == itemID {
			return
		}
	}
}

// SelectNext selects next 模型 item, skipping any non-focusable items
// like group headers 和 spacers.
func (f *ModelsList) SelectNext() (v bool) {
	v = f.List.SelectNext()
	for v {
		selectedItem := f.SelectedItem()
		if _, ok := selectedItem.(*ModelItem); ok {
			return v
		}
		v = f.List.SelectNext()
	}
	return v
}

// SelectPrev selects previous 模型 item, skipping any non-focusable items
// like group headers 和 spacers.
func (f *ModelsList) SelectPrev() (v bool) {
	v = f.List.SelectPrev()
	for v {
		selectedItem := f.SelectedItem()
		if _, ok := selectedItem.(*ModelItem); ok {
			return v
		}
		v = f.List.SelectPrev()
	}
	return v
}

// SelectFirst selects first 模型 item 在 list.
func (f *ModelsList) SelectFirst() (v bool) {
	v = f.List.SelectFirst()
	for v {
		selectedItem := f.SelectedItem()
		_, ok := selectedItem.(*ModelItem)
		if ok {
			return v
		}
		v = f.List.SelectNext()
	}
	return v
}

// SelectLast selects last 模型 item 在 list.
func (f *ModelsList) SelectLast() (v bool) {
	v = f.List.SelectLast()
	for v {
		selectedItem := f.SelectedItem()
		if _, ok := selectedItem.(*ModelItem); ok {
			return v
		}
		v = f.List.SelectPrev()
	}
	return v
}

// IsSelectedFirst checks 如果 selected item 是 first 模型 item.
func (f *ModelsList) IsSelectedFirst() bool {
	originalIndex := f.Selected()
	f.SelectFirst()
	isFirst := f.Selected() == originalIndex
	f.List.SetSelected(originalIndex)
	return isFirst
}

// IsSelectedLast checks 如果 selected item 是 last 模型 item.
func (f *ModelsList) IsSelectedLast() bool {
	originalIndex := f.Selected()
	f.SelectLast()
	isLast := f.Selected() == originalIndex
	f.List.SetSelected(originalIndex)
	return isLast
}

// VisibleItems returns the visible items after filtering.
func (f *ModelsList) VisibleItems() []list.Item {
	query := strings.ToLower(strings.ReplaceAll(f.query, " ", ""))

	if query == "" {
		// No filter, return all items 用 group headers
		items := []list.Item{}
		for _, g := range f.groups {
			items = append(items, &g)
			for _, item := range g.Items {
				item.SetMatch(fuzzy.Match{})
				items = append(items, item)
			}
			// Add space separator after each 提供方 section
			items = append(items, list.NewSpacerItem(1))
		}
		return items
	}

	filterableItems := make([]list.FilterableItem, 0, f.Len())
	for _, g := range f.groups {
		for _, item := range g.Items {
			filterableItems = append(filterableItems, item)
		}
	}

	items := []list.Item{}
	visitedGroups := map[int]bool{}

	// Reconstruct groups 用 matched items
	// Find which group 这 item belongs 到
	for gi, g := range f.groups {
		addedCount := 0
		name := strings.ToLower(g.Title) + " "

		names := make([]string, len(filterableItems))
		for i, item := range filterableItems {
			ms := item.(*ModelItem)
			names[i] = fmt.Sprintf("%s%s", name, ms.Filter())
		}

		matches := fuzzy.Find(query, names)

		// Sort 通过 original index 到 preserve order within group
		sort.SliceStable(matches, func(i, j int) bool {
			return matches[i].Index < matches[j].Index
		})

		for _, match := range matches {
			item := filterableItems[match.Index].(*ModelItem)
			idxs := []int{}
			for _, idx := range match.MatchedIndexes {
				// Adjusts removing 提供方 name highlights
				if idx < len(name) {
					continue
				}
				idxs = append(idxs, idx-len(name))
			}

			match.MatchedIndexes = idxs
			if slices.Contains(g.Items, item) {
				if !visitedGroups[gi] {
					// Add section 请求头
					items = append(items, &g)
					visitedGroups[gi] = true
				}
				// Add the matched item
				item.SetMatch(match)
				items = append(items, item)
				addedCount++
			}
		}
		if addedCount > 0 {
			// Add space separator after each 提供方 section
			items = append(items, list.NewSpacerItem(1))
		}
	}

	return items
}

// Render 渲染 filterable list.
func (f *ModelsList) Render() string {
	return f.List.Render()
}

type modelGroups []ModelGroup

func (m modelGroups) Len() int {
	n := 0
	for _, g := range m {
		n += len(g.Items)
	}
	return n
}

func (m modelGroups) String(i int) string {
	count := 0
	for _, g := range m {
		if i < count+len(g.Items) {
			return g.Items[i-count].Filter()
		}
		count += len(g.Items)
	}
	return ""
}
