// Package storewatch tracks local writes that overlap a first-send authority check.
package storewatch

import (
	"errors"
	"reflect"
	"sync"
)

type key struct {
	store any
	scope string
}

type window struct {
	writers int
	watches int
	starts  uint64
}

var registry = struct {
	sync.Mutex
	active map[key]*window
}{active: make(map[key]*window)}

func entry(store any, scope string) (key, *window) {
	// Value-based custom adapters containing maps or functions have no stable
	// comparable identity. Conservatively share their type and scope.
	if v := reflect.ValueOf(store); v.IsValid() && !v.Comparable() {
		store = v.Type()
	}
	k := key{store, scope}
	w := registry.active[k]
	if w == nil {
		w = new(window)
		registry.active[k] = w
	}
	return k, w
}

func release(k key, w *window) {
	if w.writers == 0 && w.watches == 0 {
		delete(registry.active, k)
	}
}

// Write must cover the whole transaction, including any wait to commit.
func Write(store any, scope string) func() {
	registry.Lock()
	k, w := entry(store, scope)
	w.starts++
	w.writers++
	registry.Unlock()
	return func() {
		registry.Lock()
		defer registry.Unlock()
		w.writers--
		release(k, w)
	}
}

// Observe returns a single-use, synchronous comparison and release. It also
// reports writes already underway at registration, including aborted writes.
func Observe(store any, scope string) func() bool {
	registry.Lock()
	k, w := entry(store, scope)
	w.watches++
	starts, busy := w.starts, w.writers != 0
	registry.Unlock()
	return func() bool {
		registry.Lock()
		defer registry.Unlock()
		w.watches--
		release(k, w)
		return busy || starts != w.starts
	}
}

// Scoped observers let a store coordinate across handles to one backing store.
type Observer interface {
	ObserveScopeWrites(scope string) func() bool
}

func ObserveStore(store any, scope string) func() bool {
	if native, ok := store.(Observer); ok {
		return native.ObserveScopeWrites(scope)
	}
	return Observe(store, scope)
}

// WriteAdapter covers SDK-originated updates to adapters without native observation.
func WriteAdapter(store any, scope string) func() {
	if _, native := store.(Observer); native {
		return func() {}
	}
	return Write(store, scope)
}

// Check releases the observation even if a custom validation callback panics.
// It compares locally after all potentially blocking validations have returned.
func Check(finish func() bool, check func() error) (err error) {
	if finish != nil {
		defer func() {
			if finish() && err == nil {
				err = errors.New("store changed during final submission checks")
			}
		}()
	}
	return check()
}
