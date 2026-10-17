// Package operations contains concurrency primitives shared by host
// operation adapters. The lock registry serializes mutations for related
// sites while allowing unrelated sites to proceed concurrently.
package operations

import (
	"sort"
	"sync"
)

// Locks serializes operations that share a site-level staging, environment,
// database, route, or release path. References keep a lock alive for waiters
// without leaking registry entries.
type Locks struct {
	mu    sync.Mutex
	locks map[string]*operationLock
}

type operationLock struct {
	mu   sync.Mutex
	refs int
}

// Acquire obtains the lock for one operation key.
func (l *Locks) Acquire(key string) func() {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*operationLock)
	}
	lock := l.locks[key]
	if lock == nil {
		lock = &operationLock{}
		l.locks[key] = lock
	}
	lock.refs++
	l.mu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		l.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}

// TryAcquire obtains the lock for one operation key only if no other
// operation holds it. It reports false, holding nothing, when the key is busy.
func (l *Locks) TryAcquire(key string) (func(), bool) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*operationLock)
	}
	lock := l.locks[key]
	if lock == nil {
		lock = &operationLock{}
		l.locks[key] = lock
	}
	lock.refs++
	l.mu.Unlock()

	release := func() {
		l.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
	if !lock.mu.TryLock() {
		release()
		return nil, false
	}
	return func() {
		lock.mu.Unlock()
		release()
	}, true
}

// AcquireMany obtains a stable set of related operation locks in lexical
// order, preventing lock-order deadlocks between concurrent requests.
func (l *Locks) AcquireMany(keys ...string) func() {
	unique := make(map[string]struct{}, len(keys))
	ordered := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = struct{}{}
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	unlockers := make([]func(), 0, len(ordered))
	for _, key := range ordered {
		unlockers = append(unlockers, l.Acquire(key))
	}
	return func() {
		for i := len(unlockers) - 1; i >= 0; i-- {
			unlockers[i]()
		}
	}
}

// Len reports the number of currently referenced operation keys. It is
// intended for diagnostics and contract tests.
func (l *Locks) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.locks)
}
