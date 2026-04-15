/***
	design stand-alone kv store pay attention to the order of WAL and read-write locks fine grained lock, good
**/

package distributedkvstore

import "sync"

type Storage struct {
	store    sync.Map // concurrent map to store key-value pairs
	capacity int32
}

func NewStorage(capacity int32) *Storage {
	return &Storage{
		store:    sync.Map{}, // initialize the sync.Map with a given capacity
		capacity: capacity,
	}
}

func (s *Storage) Put(key, value string) {
	s.store.Store(key, value)
}

func (s *Storage) Get(key string) (string, bool) {
	value, ok := s.store.Load(key)
	if !ok {
		return "", false
	}
	return value.(string), true
}

func (s *Storage) Update(key, value string) bool {
	_, ok := s.store.Load(key)
	if !ok {
		return false
	}
	s.store.Store(key, value)
	return true
}

func (s *Storage) Delete(key string) {
	s.store.Delete(key)
}

func (s *Storage) Clear() {
	s.store.Range(func(key, value any) bool {
		s.store.Delete(key)
		return true
	})
}

func (s *Storage) LoadOrStore(key, value string) (actual string, loaded bool) {
	actualValue, loaded := s.store.LoadOrStore(key, value)
	return actualValue.(string), loaded
}

func (s *Storage) LoadAndDelete(key string) (value string, loaded bool) {
	actualValue, loaded := s.store.LoadAndDelete(key)
	if !loaded {
		return "", false
	}
	return actualValue.(string), true
}

func (s *Storage) Swap(key, value string) (previous string, loaded bool) {
	actualValue, loaded := s.store.Swap(key, value)
	if !loaded {
		return "", false
	}
	return actualValue.(string), true
}

func (s *Storage) CompareAndSwap(key, old, new string) (swapped bool) {
	return s.store.CompareAndSwap(key, old, new)
}

func (s *Storage) CompareAndDelete(key, old string) (deleted bool) {
	return s.store.CompareAndDelete(key, old)
}
