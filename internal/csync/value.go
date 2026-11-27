package csync

import (
	"reflect"
	"sync"
)

// 值 是 generic thread-safe wrapper 为 any 值 type.
//
// For slices, use [Slice]. For maps, use [Map]. Pointers 是 不 supported.
type Value[T any] struct {
	v  T
	mu sync.RWMutex
}

// NewValue creates new 值 用 given initial 值.
//
// Panics 如果 t 是 指针, slice, 或 map. Use dedicated types 为 those.
func NewValue[T any](t T) *Value[T] {
	v := reflect.ValueOf(t)
	switch v.Kind() {
	case reflect.Pointer:
		panic("csync.Value does not support pointer types")
	case reflect.Slice:
		panic("csync.Value does not support slice types; use csync.Slice")
	case reflect.Map:
		panic("csync.Value does not support map types; use csync.Map")
	}
	return &Value[T]{v: t}
}

// Get returns current 值.
func (v *Value[T]) Get() T {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.v
}

// Set updates 值.
func (v *Value[T]) Set(t T) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.v = t
}
