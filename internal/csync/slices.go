package csync

import (
	"iter"
	"sync"
)

// LazySlice 是 thread-safe lazy-loaded slice.
type LazySlice[K any] struct {
	inner []K
	wg    sync.WaitGroup
}

// NewLazySlice creates new slice 和 runs [load] function 在 goroutine
// 到 populate it.
func NewLazySlice[K any](load func() []K) *LazySlice[K] {
	s := &LazySlice[K]{}
	s.wg.Go(func() {
		s.inner = load()
	})
	return s
}

// Seq returns iterator 那 yields elements 从 slice.
func (s *LazySlice[K]) Seq() iter.Seq[K] {
	s.wg.Wait()
	return func(yield func(K) bool) {
		for _, v := range s.inner {
			if !yield(v) {
				return
			}
		}
	}
}

// Slice 是 thread-safe slice implementation 那 provides concurrent access.
type Slice[T any] struct {
	inner []T
	mu    sync.RWMutex
}

// NewSlice creates a new thread-safe slice.
func NewSlice[T any]() *Slice[T] {
	return &Slice[T]{
		inner: make([]T, 0),
	}
}

// NewSliceFrom creates new thread-safe slice 从 existing slice.
func NewSliceFrom[T any](s []T) *Slice[T] {
	inner := make([]T, len(s))
	copy(inner, s)
	return &Slice[T]{
		inner: inner,
	}
}

// Append adds element 到 end 的 slice.
func (s *Slice[T]) Append(items ...T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner = append(s.inner, items...)
}

// Get returns element 在 specified index.
func (s *Slice[T]) Get(index int) (T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var zero T
	if index < 0 || index >= len(s.inner) {
		return zero, false
	}
	return s.inner[index], true
}

// Len returns number 的 elements 在 slice.
func (s *Slice[T]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.inner)
}

// SetSlice replaces entire slice 用 new one.
func (s *Slice[T]) SetSlice(items []T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner = make([]T, len(items))
	copy(s.inner, items)
}

// Seq returns iterator 那 yields elements 从 slice.
func (s *Slice[T]) Seq() iter.Seq[T] {
	return func(yield func(T) bool) {
		for _, v := range s.Seq2() {
			if !yield(v) {
				return
			}
		}
	}
}

// Seq2 returns iterator 那 yields index-值 pairs 从 slice.
func (s *Slice[T]) Seq2() iter.Seq2[int, T] {
	items := s.Copy()
	return func(yield func(int, T) bool) {
		for i, v := range items {
			if !yield(i, v) {
				return
			}
		}
	}
}

// Copy returns copy 的 inner slice.
func (s *Slice[T]) Copy() []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]T, len(s.inner))
	copy(items, s.inner)
	return items
}
