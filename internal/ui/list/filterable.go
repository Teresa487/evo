package list

import (
	"github.com/sahilm/fuzzy"
)

// FilterableItem 是 item 那 can 是 filtered via 查询.
type FilterableItem interface {
	Item
	// Filter returns 值 到 是 used 为 filtering.
	Filter() string
}

// MatchSettable 是 interface 为 items 那 can have their match indexes
// 和 match score set.
type MatchSettable interface {
	SetMatch(fuzzy.Match)
}

// FilterableList 是 list 那 takes filterable items 那 can 是 filtered
// via settable 查询.
type FilterableList struct {
	*List
	items []FilterableItem
	query string
}

// NewFilterableList creates a new filterable list.
func NewFilterableList(items ...FilterableItem) *FilterableList {
	f := &FilterableList{
		List:  NewList(),
		items: items,
	}
	f.RegisterRenderCallback(FocusedRenderCallback(f.List))
	f.SetItems(items...)
	return f
}

// SetItems sets list items 和 updates filtered items.
func (f *FilterableList) SetItems(items ...FilterableItem) {
	f.items = items
	fitems := make([]Item, len(items))
	for i, item := range items {
		fitems[i] = item
	}
	f.List.SetItems(fitems...)
}

// AppendItems appends items 到 list 和 updates filtered items.
func (f *FilterableList) AppendItems(items ...FilterableItem) {
	f.items = append(f.items, items...)
	itms := make([]Item, len(f.items))
	for i, item := range f.items {
		itms[i] = item
	}
	f.List.SetItems(itms...)
}

// PrependItems prepends items 到 list 和 updates filtered items.
func (f *FilterableList) PrependItems(items ...FilterableItem) {
	f.items = append(items, f.items...)
	itms := make([]Item, len(f.items))
	for i, item := range f.items {
		itms[i] = item
	}
	f.List.SetItems(itms...)
}

// SetFilter sets filter 查询 和 updates list items.
func (f *FilterableList) SetFilter(q string) {
	f.query = q
	f.List.SetItems(f.FilteredItems()...)
	f.ScrollToTop()
}

// FilterableItemsSource 是 type 那 implements [fuzzy.Source] 为 filtering
// [FilterableItem]s.
type FilterableItemsSource []FilterableItem

// Len returns length 的 source.
func (f FilterableItemsSource) Len() int {
	return len(f)
}

// String returns string representation 的 item 在 index i.
func (f FilterableItemsSource) String(i int) string {
	return f[i].Filter()
}

// Items returns every item 的 list, including ones current
// 查询 filters out.
func (f *FilterableList) Items() []FilterableItem {
	return f.items
}

// FilteredItems returns the visible items after filtering.
func (f *FilterableList) FilteredItems() []Item {
	if f.query == "" {
		items := make([]Item, len(f.items))
		for i, item := range f.items {
			if ms, ok := item.(MatchSettable); ok {
				ms.SetMatch(fuzzy.Match{})
				item = ms.(FilterableItem)
			}
			items[i] = item
		}
		return items
	}

	items := FilterableItemsSource(f.items)
	matches := fuzzy.FindFrom(f.query, items)
	matchedItems := []Item{}
	resultSize := len(matches)
	for i := range resultSize {
		match := matches[i]
		item := items[match.Index]
		if ms, ok := item.(MatchSettable); ok {
			ms.SetMatch(match)
			item = ms.(FilterableItem)
		}
		matchedItems = append(matchedItems, item)
	}

	return matchedItems
}

// Render 渲染 filterable list.
func (f *FilterableList) Render() string {
	f.List.SetItems(f.FilteredItems()...)
	return f.List.Render()
}
