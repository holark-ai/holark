package manualterminals

import (
	"context"
	"sync"
)

// Store is owned by the manual-terminal domain and implemented by persistence
// adapters. It deliberately exposes no session or canonical terminal storage.
type Store interface {
	Insert(context.Context, Record) error
	Update(context.Context, Record) error
	Delete(context.Context, string) error
	List(context.Context) ([]Record, error)
	ListBySession(context.Context, string) ([]Record, error)
}

type MemoryStore struct {
	mu        sync.RWMutex
	terminals map[string]Record
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{terminals: make(map[string]Record)}
}

func (store *MemoryStore) Insert(_ context.Context, terminal Record) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.terminals[terminal.ID]; exists {
		return ErrLimit
	}
	store.terminals[terminal.ID] = cloneRecord(terminal)
	return nil
}

func (store *MemoryStore) Update(_ context.Context, terminal Record) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.terminals[terminal.ID]; !exists {
		return ErrMissing
	}
	store.terminals[terminal.ID] = cloneRecord(terminal)
	return nil
}

func (store *MemoryStore) Delete(_ context.Context, id string) error {
	store.mu.Lock()
	delete(store.terminals, id)
	store.mu.Unlock()
	return nil
}

func (store *MemoryStore) List(_ context.Context) ([]Record, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]Record, 0, len(store.terminals))
	for _, terminal := range store.terminals {
		result = append(result, cloneRecord(terminal))
	}
	return result, nil
}

func (store *MemoryStore) ListBySession(ctx context.Context, sessionID string) ([]Record, error) {
	all, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(all))
	for _, terminal := range all {
		if terminal.SessionID == sessionID {
			result = append(result, terminal)
		}
	}
	return result, nil
}
