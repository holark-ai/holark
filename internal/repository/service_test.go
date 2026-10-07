package repository

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
)

type fakeStore struct {
	mu               sync.Mutex
	calls            int
	started, release chan struct{}
	result           []Ref
	err              error
}

func (*fakeStore) Descriptor() Descriptor              { return Descriptor{ID: "one"} }
func (*fakeStore) Refs(context.Context) ([]Ref, error) { return []Ref{{ShortName: "cached"}}, nil }
func (f *fakeStore) Refresh(context.Context) ([]Ref, error) {
	f.mu.Lock()
	f.calls++
	if f.calls == 1 {
		close(f.started)
	}
	f.mu.Unlock()
	<-f.release
	return append([]Ref(nil), f.result...), f.err
}
func (*fakeStore) Tree(context.Context, string, string) (Tree, error)             { return Tree{}, nil }
func (*fakeStore) Blob(context.Context, string, string) (Blob, error)             { return Blob{}, nil }
func (*fakeStore) Commits(context.Context, string, int) ([]Commit, error)         { return nil, nil }
func (*fakeStore) Changes(context.Context, string, string) ([]Change, error)      { return nil, nil }
func (*fakeStore) Resolve(context.Context, string) (string, error)                { return "", nil }
func (*fakeStore) CommitPage(context.Context, string, int, int) ([]Commit, error) { return nil, nil }
func (*fakeStore) Dirty(context.Context) (DirtyState, error)                      { return DirtyState{}, nil }

func TestRefreshCoalescesConcurrentSuccess(t *testing.T) {
	store := &fakeStore{started: make(chan struct{}), release: make(chan struct{}), result: []Ref{{ShortName: "fresh", Target: "abc"}}}
	service := NewService(store)
	const clients = 8
	outcomes := make(chan []Ref, clients)
	for range clients {
		go func() {
			refs, err := service.Refresh(t.Context())
			if err != nil {
				t.Errorf("Refresh: %v", err)
			}
			outcomes <- refs
		}()
	}
	<-store.started
	waitForWaiters(t, service, clients-1)
	close(store.release)
	for range clients {
		if got := <-outcomes; !reflect.DeepEqual(got, store.result) {
			t.Fatalf("refs=%+v, want %+v", got, store.result)
		}
	}
	if store.calls != 1 {
		t.Fatalf("calls=%d", store.calls)
	}
}
func TestRefreshCoalescesConcurrentFailure(t *testing.T) {
	wantErr := errors.New("network unavailable")
	store := &fakeStore{started: make(chan struct{}), release: make(chan struct{}), result: []Ref{{ShortName: "partial"}}, err: wantErr}
	service := NewService(store)
	type outcome struct {
		refs []Ref
		err  error
	}
	outcomes := make(chan outcome, 4)
	for range 4 {
		go func() { refs, err := service.Refresh(t.Context()); outcomes <- outcome{refs, err} }()
	}
	<-store.started
	waitForWaiters(t, service, 3)
	close(store.release)
	for range 4 {
		got := <-outcomes
		if !errors.Is(got.err, wantErr) || !reflect.DeepEqual(got.refs, store.result) {
			t.Fatalf("outcome=%+v", got)
		}
	}
	if store.calls != 1 {
		t.Fatalf("calls=%d", store.calls)
	}
}
func TestRefreshWaiterHonorsCancellation(t *testing.T) {
	store := &fakeStore{started: make(chan struct{}), release: make(chan struct{})}
	service := NewService(store)
	go service.Refresh(context.Background())
	<-store.started
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := service.Refresh(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	close(store.release)
}
func waitForWaiters(t *testing.T, s *Service, want int) {
	t.Helper()
	for attempts := 0; attempts < 100000; attempts++ {
		s.mu.Lock()
		got := s.refresh.waiters
		s.mu.Unlock()
		if got == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("waiters did not reach %d", want)
}
