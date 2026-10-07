package holons

import (
	"context"
	"sync"
)

// Serialize mutations of one Holon without making unrelated Holons wait on
// workspace I/O. Entries disappear when neither an owner nor waiters remain.
type holonLocks struct {
	mu      sync.Mutex
	entries map[string]*holonLock
}
type holonLock struct {
	token chan struct{}
	users int
}

func (s *Service) lock(ctx context.Context, id string) (func(), error) {
	locks := &s.locks
	locks.mu.Lock()
	if locks.entries == nil {
		locks.entries = make(map[string]*holonLock)
	}
	entry := locks.entries[id]
	if entry == nil {
		entry = &holonLock{token: make(chan struct{}, 1)}
		locks.entries[id] = entry
	}
	entry.users++
	locks.mu.Unlock()
	release := func() {
		locks.mu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(locks.entries, id)
		}
		locks.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	select {
	case entry.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-entry.token
			release()
			return nil, err
		}
		return func() { <-entry.token; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
