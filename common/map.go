package common

import "sync"

type SimpleSyncMap[K comparable, V any] struct {
	lock sync.Mutex
	m    map[K]V
}

func NewSimpleSyncMap[K comparable, V any]() *SimpleSyncMap[K, V] {
	return &SimpleSyncMap[K, V]{m: make(map[K]V)}
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

func (ssm *SimpleSyncMap[K, V]) PutIfNotPresent(k K, v V) bool {
	ssm.lock.Lock()
	defer ssm.lock.Unlock()
	if _, ok := ssm.m[k]; ok {
		return false
	}
	ssm.m[k] = v
	return true
}

func (ssm *SimpleSyncMap[K, V]) Del(k K) {
	ssm.lock.Lock()
	defer ssm.lock.Unlock()
	delete(ssm.m, k)
}
