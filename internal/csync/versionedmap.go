package csync

import (
	"iter"
	"sync/atomic"
)

// NewVersionedMap creates a new versioned, thread-safe map.
func NewVersionedMap[K comparable, V any]() *VersionedMap[K, V] {
	return &VersionedMap[K, V]{
		m: NewMap[K, V](),
	}
}

// VersionedMap 是 thread-safe map 那 keeps track 的 它的 version.
type VersionedMap[K comparable, V any] struct {
	m *Map[K, V]
	v atomic.Uint64
}

// Get gets 值 为 specified key 从 map.
func (m *VersionedMap[K, V]) Get(key K) (V, bool) {
	return m.m.Get(key)
}

// Set sets 值 为 specified key 在 map 和 increments version.
func (m *VersionedMap[K, V]) Set(key K, value V) {
	m.m.Set(key, value)
	m.v.Add(1)
}

// Del deletes specified key 从 map 和 increments version.
func (m *VersionedMap[K, V]) Del(key K) {
	m.m.Del(key)
	m.v.Add(1)
}

// Seq2 returns iter.Seq2 那 yields key-值 pairs 从 map.
func (m *VersionedMap[K, V]) Seq2() iter.Seq2[K, V] {
	return m.m.Seq2()
}

// Copy returns copy 的 inner map.
func (m *VersionedMap[K, V]) Copy() map[K]V {
	return m.m.Copy()
}

// Len returns number 的 items 在 map.
func (m *VersionedMap[K, V]) Len() int {
	return m.m.Len()
}

// Version returns current version 的 map.
func (m *VersionedMap[K, V]) Version() uint64 {
	return m.v.Load()
}
