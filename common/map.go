package common

import (
	"maps"
	"sync"
)

type SimpleSyncMap[K comparable, V any] struct {
	lock sync.Mutex
	m    map[K]V
}

func NewSimpleSyncMap[K comparable, V any]() *SimpleSyncMap[K, V] {
	return &SimpleSyncMap[K, V]{m: make(map[K]V)}
}

func (ssm *SimpleSyncMap[K, V]) Has(k K) bool {
	ssm.lock.Lock()
	defer ssm.lock.Unlock()
	_, ok := ssm.m[k]
	return ok
}

func (ssm *SimpleSyncMap[K, V]) Get(k K) (V, bool) {
	ssm.lock.Lock()
	defer ssm.lock.Unlock()
	v, ok := ssm.m[k]
	return v, ok
}

func (ssm *SimpleSyncMap[K, V]) Put(k K, v V) {
	ssm.lock.Lock()
	defer ssm.lock.Unlock()
	ssm.m[k] = v
}

// If k is present, returns its value and true, otherwise sets k to v and returns v and false.
func (ssm *SimpleSyncMap[K, V]) GetOrPut(k K, v V) (V, bool) {
	ssm.lock.Lock()
	defer ssm.lock.Unlock()
	existing, ok := ssm.m[k]
	if ok {
		return existing, true
	}
	ssm.m[k] = v
	return v, false
}

func (ssm *SimpleSyncMap[K, V]) Delete(k K) {
	ssm.lock.Lock()
	defer ssm.lock.Unlock()
	delete(ssm.m, k)
}

// Deletes entries where f(k, v) returns true.
func (ssm *SimpleSyncMap[K, V]) DeleteFunc(f func(K, V) bool) {
	ssm.lock.Lock()
	defer ssm.lock.Unlock()
	maps.DeleteFunc(ssm.m, f)
}

// Calls f on the value of key k, while holding the lock.
func (ssm *SimpleSyncMap[K, V]) WithValue(k K, f func(V)) {
	ssm.lock.Lock()
	defer ssm.lock.Unlock()
	if v, ok := ssm.m[k]; ok {
		f(v)
	}
}

// Atomically calls f with the result of Get(k) and then either sets k to a new value or
// deletes k.
func (ssm *SimpleSyncMap[K, V]) Modify(k K, f func(V, bool) (V, bool)) {
	ssm.lock.Lock()
	defer ssm.lock.Unlock()
	v, ok := ssm.m[k]
	if nv, nok := f(v, ok); nok {
		ssm.m[k] = nv
	} else {
		delete(ssm.m, k)
	}
}
