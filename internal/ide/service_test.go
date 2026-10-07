package ide

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu    sync.Mutex
	items []IDE
}

func (m *memoryStore) List(_ context.Context, h string) ([]IDE, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []IDE
	for _, v := range m.items {
		if v.HolonID == h {
			out = append(out, v)
		}
	}
	return out, nil
}
func (m *memoryStore) Get(_ context.Context, h, id string) (IDE, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.items {
		if v.HolonID == h && v.ID == id {
			return v, nil
		}
	}
	return IDE{}, ErrNotFound
}
func (m *memoryStore) Create(_ context.Context, v IDE) (IDE, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v.TabOrder = len(m.items)
	m.items = append(m.items, v)
	return v, nil
}
func (m *memoryStore) Update(_ context.Context, v IDE) (IDE, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.items {
		if m.items[i].ID == v.ID {
			m.items[i] = v
			return v, nil
		}
	}
	return IDE{}, ErrNotFound
}
func (m *memoryStore) OpenAtStartup(context.Context) ([]IDE, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []IDE
	for _, v := range m.items {
		if v.DesiredOpen {
			out = append(out, v)
		}
	}
	return out, nil
}

type fakeRuntime struct {
	started      chan Start
	startRelease <-chan struct{}
	stopped      chan string
	startErr     error
	stopErr      error
	stops        int
}

func (f *fakeRuntime) Start(_ context.Context, s Start) error {
	f.started <- s
	if f.startRelease != nil {
		<-f.startRelease
	}
	return f.startErr
}
func (f *fakeRuntime) Stop(_ context.Context, id string) error {
	f.stops++
	if f.stopped != nil {
		f.stopped <- id
	}
	return f.stopErr
}
func (f *fakeRuntime) Target(string) (string, bool) { return "http://127.0.0.1:1", true }
func (f *fakeRuntime) LogPath(string) string        { return "/logs/serve-web.log" }
func (f *fakeRuntime) Close() error                 { return nil }

type fakeWorkspace struct{}

func (fakeWorkspace) Workspace(context.Context, string) (Workspace, error) {
	return Workspace{Path: "/worktree"}, nil
}
func TestOpenIsExplicitDurableAndReportsReadiness(t *testing.T) {
	store := &memoryStore{}
	runtime := &fakeRuntime{started: make(chan Start, 1)}
	s := New(store, fakeWorkspace{}, runtime)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	v, e := s.Open(t.Context(), "holon-one")
	if e != nil || v.State != Starting || !v.DesiredOpen {
		t.Fatalf("open=%+v %v", v, e)
	}
	start := <-runtime.started
	if start.WorktreePath != "/worktree" || start.BasePath != BasePath("holon-one", v.ID) {
		t.Fatalf("start=%+v", start)
	}
	for i := 0; i < 100; i++ {
		current, _ := store.Get(t.Context(), "holon-one", v.ID)
		if current.State == Ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("IDE never became ready")
}
func TestFailedLaunchPersistsBoundedDiagnostic(t *testing.T) {
	store := &memoryStore{}
	runtime := &fakeRuntime{started: make(chan Start, 1), startErr: errors.New(string(make([]byte, 2000)))}
	s := New(store, fakeWorkspace{}, runtime)
	v, e := s.Open(t.Context(), "h")
	if e != nil {
		t.Fatal(e)
	}
	<-runtime.started
	for i := 0; i < 100; i++ {
		current, _ := store.Get(t.Context(), "h", v.ID)
		if current.State == Failed {
			if !current.DesiredOpen || current.ClosedAt != nil || len(current.Reason) > 1024 || !strings.Contains(current.Reason, "/logs/serve-web.log") {
				t.Fatalf("failed=%+v", current)
			}
			if err := s.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			current, _ = store.Get(t.Context(), "h", v.ID)
			if current.State != Failed || !current.DesiredOpen {
				t.Fatalf("restart hid failure: %+v", current)
			}
			closed, err := s.Close(t.Context(), "h", v.ID)
			if err != nil || closed.DesiredOpen || closed.State != Closed {
				t.Fatalf("close=%+v err=%v", closed, err)
			}
			if _, err := s.Open(t.Context(), "h"); err != nil {
				t.Fatalf("retry: %v", err)
			}

			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("failure not persisted")
}
func TestRecoverRequiresExplicitReopen(t *testing.T) {
	closed := time.Now().UTC()
	store := &memoryStore{items: []IDE{
		{ID: "ide", HolonID: "h", State: Ready, DesiredOpen: true},
		{ID: "suspended", HolonID: "h", State: Suspended, DesiredOpen: true, ClosedAt: &closed},
	}}
	s := New(store, fakeWorkspace{}, &fakeRuntime{started: make(chan Start, 1)})
	if e := s.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	v, _ := store.Get(t.Context(), "h", "ide")
	if v.State != Lost || v.DesiredOpen {
		t.Fatalf("recovered=%+v", v)
	}
	suspended, _ := store.Get(t.Context(), "h", "suspended")
	if suspended.State != Suspended || !suspended.DesiredOpen || suspended.ClosedAt == nil {
		t.Fatalf("suspended intent was not preserved: %+v", suspended)
	}
}

func TestClosePersistsClosedStateAndReturnsRuntimeStopError(t *testing.T) {
	store := &memoryStore{items: []IDE{{ID: "ide", HolonID: "h", State: Ready, DesiredOpen: true}}}
	runtime := &fakeRuntime{started: make(chan Start, 1), stopErr: errors.New("stop failed")}
	s := New(store, fakeWorkspace{}, runtime)
	v, err := s.Close(t.Context(), "h", "ide")
	if err == nil || v.State != Stopping || !v.DesiredOpen || v.ClosedAt != nil || runtime.stops != 1 {
		t.Fatalf("close=%+v stops=%d err=%v", v, runtime.stops, err)
	}
}

func TestSuspendIsRetryableAndResumeReusesRecord(t *testing.T) {
	store := &memoryStore{items: []IDE{{ID: "ide", HolonID: "h", State: Ready, DesiredOpen: true}}}
	runtime := &fakeRuntime{started: make(chan Start, 1), stopErr: errors.New("stop failed")}
	s := New(store, fakeWorkspace{}, runtime)
	v, err := s.Suspend(t.Context(), "h", "ide")
	if err == nil || v.State != Stopping || !v.DesiredOpen || v.ClosedAt != nil {
		t.Fatalf("failed suspend=%+v err=%v", v, err)
	}
	runtime.stopErr = nil
	v, err = s.Suspend(t.Context(), "h", "ide")
	if err != nil || v.State != Suspended || !v.DesiredOpen || v.ClosedAt == nil || runtime.stops != 2 {
		t.Fatalf("retried suspend=%+v stops=%d err=%v", v, runtime.stops, err)
	}
	v, err = s.Resume(t.Context(), "h", "ide")
	if err != nil || v.ID != "ide" || v.State != Starting || !v.DesiredOpen || v.ReadyAt != nil || v.ClosedAt != nil || v.Reason != "" {
		t.Fatalf("resume=%+v err=%v", v, err)
	}
	started := <-runtime.started
	if started.IDEID != "ide" || started.HolonID != "h" {
		t.Fatalf("resume start=%+v", started)
	}
	for i := 0; i < 100; i++ {
		current, _ := store.Get(t.Context(), "h", "ide")
		if current.State == Ready {
			if len(store.items) != 1 {
				t.Fatalf("resume created duplicate IDEs: %+v", store.items)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("resumed IDE never became ready")
}

func TestSuspendWinsRaceWithInFlightLaunch(t *testing.T) {
	store := &memoryStore{}
	release := make(chan struct{})
	runtime := &fakeRuntime{started: make(chan Start, 1), startRelease: release, stopped: make(chan string, 2)}
	s := New(store, fakeWorkspace{}, runtime)
	v, err := s.Open(t.Context(), "h")
	if err != nil {
		t.Fatal(err)
	}
	<-runtime.started
	if _, err = s.Suspend(t.Context(), "h", v.ID); err != nil {
		t.Fatal(err)
	}
	<-runtime.stopped
	close(release)
	<-runtime.stopped
	current, err := store.Get(t.Context(), "h", v.ID)
	if err != nil || current.State != Suspended || !current.DesiredOpen || current.ClosedAt == nil {
		t.Fatalf("in-flight launch defeated suspension: IDE=%+v err=%v", current, err)
	}
}
