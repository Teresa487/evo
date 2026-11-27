package csync

import (
	"encoding/json"
	"iter"
	"maps"
	"sync"
)

// Map 是 concurrent map implementation 那 provides thread-safe access.
type Map[K comparable, V any] struct {
	inner map[K]V
	mu    sync.RWMutex
}

// NewMap creates new thread-safe map 用 specified key 和 值 types.
func NewMap[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		inner: make(map[K]V),
	}
}

// NewMapFrom creates new thread-safe map 从 existing map.
func NewMapFrom[K comparable, V any](m map[K]V) *Map[K, V] {
	return &Map[K, V]{
		inner: m,
	}
}

// NewLazyMap creates new lazy-loaded map. provided load function 是
// executed 在 separate goroutine 到 populate map.
func NewLazyMap[K comparable, V any](load func() map[K]V) *Map[K, V] {
	m := &Map[K, V]{}
	m.mu.Lock()
	go func() {
		defer m.mu.Unlock()
		m.inner = load()
	}()
	return m
}

// Reset replaces inner map 用 new one.
func (m *Map[K, V]) Reset(input map[K]V) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inner = input
}

// Set sets 值 为 specified key 在 map.
func (m *Map[K, V]) Set(key K, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inner[key] = value
}

// Swap sets 值 为 specified key 和 returns 值 it
// replaced, 如果 any, 在 one atomic step.
func (m *Map[K, V]) Swap(key K, value V) (previous V, loaded bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, loaded = m.inner[key]
	m.inner[key] = value
	return previous, loaded
}

// Del deletes specified key 从 map.
func (m *Map[K, V]) Del(key K) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.inner, key)
}

// CompareAndDelete deletes key 只 如果 current 值 matches
// expected 指针. Returns true 如果 deletion occurred. 这 是
// ABA-safe 清理 primitive: it prevents deferred 清理 从 removing
// 值 那 was replaced 通过 newer writer 在 窗口 between
// explicit Del 和 deferred Del.
func (m *Map[K, V]) CompareAndDelete(key K, expected any) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.inner[key]
	if !ok {
		return false
	}
	if any(current) != expected {
		return false
	}
	delete(m.inner, key)
	return true
}

// Get gets 值 为 specified key 从 map.
func (m *Map[K, V]) Get(key K) (V, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.inner[key]
	return v, ok
}

// Len returns number 的 items 在 map.
func (m *Map[K, V]) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.inner)
}

// GetOrSet gets 和 returns key 如果 it exists, otherwise, it executes
// given function, set 它的 return 值 为 given key, 和 returns it.
func (m *Map[K, V]) GetOrSet(key K, fn func() V) V {
	got, ok := m.Get(key)
	if ok {
		return got
	}
	value := fn()
	m.Set(key, value)
	return value
}

// Take gets item 和 then deletes it.
func (m *Map[K, V]) Take(key K) (V, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.inner[key]
	delete(m.inner, key)
	return v, ok
}

// Copy returns copy 的 inner map.
func (m *Map[K, V]) Copy() map[K]V {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return maps.Clone(m.inner)
}

// Seq2 returns iter.Seq2 那 yields key-值 pairs 从 map.
func (m *Map[K, V]) Seq2() iter.Seq2[K, V] {
	dst := m.Copy()
	return func(yield func(K, V) bool) {
		for k, v := range dst {
			if !yield(k, v) {
				return
			}
		}
	}
}

// Seq returns iter.Seq 那 yields values 从 map.
func (m *Map[K, V]) Seq() iter.Seq[V] {
	return func(yield func(V) bool) {
		for _, v := range m.Seq2() {
			if !yield(v) {
				return
			}
		}
	}
}

var (
	_ json.Unmarshaler = &Map[string, any]{}
	_ json.Marshaler   = &Map[string, any]{}
)

// JSONSchemaAlias returns underlying map type 为 JSON schema generation.
// 值 receiver 是 required because github.com/invopop/jsonschema checks
// interface satisfaction 在 non-指针 type after stripping pointers.
func (Map[K, V]) JSONSchemaAlias() any { //nolint
	m := map[K]V{}
	return m
}

// UnmarshalJSON implements json.Unmarshaler.
func (m *Map[K, V]) UnmarshalJSON(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inner = make(map[K]V)
	return json.Unmarshal(data, &m.inner)
}

// MarshalJSON implements json.Marshaler.
func (m *Map[K, V]) MarshalJSON() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return json.Marshal(m.inner)
}
