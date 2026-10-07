package pullrequestcomments

import (
	"context"
	"sync"
)

type keyedLocks struct {
	mu      sync.Mutex
	entries map[string]*keyedLock
}
type keyedLock struct {
	token      chan struct{}
	references int
}

func newKeyedLocks() *keyedLocks { return &keyedLocks{entries: make(map[string]*keyedLock)} }
func (locks *keyedLocks) lock(key string) func() {
	unlock, _ := locks.lockContext(context.Background(), key)
	return unlock
}
func (locks *keyedLocks) lockContext(ctx context.Context, key string) (func(), error) {
	locks.mu.Lock()
	entry := locks.entries[key]
	if entry == nil {
		entry = &keyedLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		locks.entries[key] = entry
	}
	entry.references++
	locks.mu.Unlock()
	release := func() {
		locks.mu.Lock()
		entry.references--
		if entry.references == 0 {
			delete(locks.entries, key)
		}
		locks.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	case <-entry.token:
		return func() { entry.token <- struct{}{}; release() }, nil
	}
}
