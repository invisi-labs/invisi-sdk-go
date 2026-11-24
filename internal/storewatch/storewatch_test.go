package storewatch

import "testing"

func TestConcurrentScopeWrites(t *testing.T) {
	store := new(int)
	complete := Write(store, "a")
	observe := Observe(store, "a")
	complete()
	if !observe() {
		t.Fatal("missed already running write")
	}
	observe = Observe(store, "a")
	Write(store, "b")()
	Write(new(int), "a")()
	if observe() {
		t.Fatal("reported unrelated scope or store")
	}
	observe = Observe(store, "a")
	Write(store, "a")()
	if !observe() {
		t.Fatal("missed completed overlapping write")
	}
	maps := struct{ Options map[string]string }{map[string]string{}}
	observe = Observe(maps, "a")
	Write(maps, "a")()
	if !observe() {
		t.Fatal("uncomparable adapter type lost overlapping write")
	}
	if len(registry.active) != 0 {
		t.Fatal("completed windows retain store identities")
	}
}
