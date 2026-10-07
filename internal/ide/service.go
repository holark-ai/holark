// Package ide owns Holark's durable IDE lifecycle.
package ide

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("IDE not found")
var ErrExists = errors.New("IDE already open")
var ErrNotReady = errors.New("IDE is not ready")
var ErrInvalid = errors.New("invalid IDE request")

type State string

const (
	Starting  State = "starting"
	Ready     State = "ready"
	Stopping  State = "stopping"
	Suspended State = "suspended"
	Failed    State = "failed"
	Lost      State = "lost"
	Closed    State = "closed"
)

type IDE struct {
	ID          string     `json:"id"`
	HolonID     string     `json:"holon_id"`
	Provider    string     `json:"provider"`
	State       State      `json:"state"`
	TabOrder    int        `json:"tab_order"`
	DesiredOpen bool       `json:"desired_open"`
	Reason      string     `json:"reason,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ReadyAt     *time.Time `json:"ready_at,omitempty"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
}
type Workspace struct{ Path string }
type Store interface {
	List(context.Context, string) ([]IDE, error)
	Get(context.Context, string, string) (IDE, error)
	Create(context.Context, IDE) (IDE, error)
	Update(context.Context, IDE) (IDE, error)
	OpenAtStartup(context.Context) ([]IDE, error)
}
type Workspaces interface {
	Workspace(context.Context, string) (Workspace, error)
}
type Start struct{ IDEID, HolonID, WorktreePath, BasePath string }
type Runtime interface {
	Start(context.Context, Start) error
	Stop(context.Context, string) error
	Target(string) (string, bool)
	LogPath(string) string
	Close() error
}

type Service struct {
	store      Store
	workspaces Workspaces
	runtime    Runtime
	now        func() time.Time
	mu         sync.Mutex
}

func New(store Store, workspaces Workspaces, runtime Runtime) *Service {
	return &Service{store: store, workspaces: workspaces, runtime: runtime, now: time.Now}
}
func (s *Service) List(ctx context.Context, h string) ([]IDE, error)  { return s.store.List(ctx, h) }
func (s *Service) Get(ctx context.Context, h, id string) (IDE, error) { return s.store.Get(ctx, h, id) }
func (s *Service) Open(ctx context.Context, holonID string) (IDE, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(holonID) == "" {
		return IDE{}, ErrInvalid
	}
	current, e := s.store.List(ctx, holonID)
	if e != nil {
		return IDE{}, e
	}
	for _, v := range current {
		if v.DesiredOpen {
			return IDE{}, ErrExists
		}
	}
	ws, e := s.workspaces.Workspace(ctx, holonID)
	if e != nil || strings.TrimSpace(ws.Path) == "" {
		return IDE{}, ErrInvalid
	}
	now := s.now().UTC()
	v := IDE{ID: newID(), HolonID: holonID, Provider: "vscode", State: Starting, DesiredOpen: true, CreatedAt: now, UpdatedAt: now}
	v, e = s.store.Create(ctx, v)
	if e != nil {
		return IDE{}, e
	}
	go s.launch(v, ws.Path)
	return v, nil
}
func (s *Service) launch(v IDE, path string) {
	err := s.runtime.Start(context.Background(), Start{IDEID: v.ID, HolonID: v.HolonID, WorktreePath: path, BasePath: BasePath(v.HolonID, v.ID)})
	current, getErr := s.store.Get(context.Background(), v.HolonID, v.ID)
	if getErr != nil || !current.DesiredOpen || current.State != Starting {
		if err == nil {
			_ = s.runtime.Stop(context.Background(), v.ID)
		}
		return
	}
	v = current
	now := s.now().UTC()
	v.UpdatedAt = now
	if err != nil {
		v.State = Failed
		v.Reason = s.failureReason(v.ID, err.Error())
	} else {
		v.State = Ready
		v.ReadyAt = &now
	}
	updated, _ := s.store.Update(context.Background(), v)
	if err == nil {
		go s.monitor(updated)
	}
}
func (s *Service) monitor(v IDE) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		current, e := s.store.Get(context.Background(), v.HolonID, v.ID)
		if e != nil || !current.DesiredOpen || current.State != Ready {
			return
		}
		if _, ok := s.runtime.Target(v.ID); ok {
			continue
		}
		now := s.now().UTC()
		current.State = Failed
		current.Reason = s.failureReason(v.ID, "VS Code exited unexpectedly.")
		current.UpdatedAt = now
		_, _ = s.store.Update(context.Background(), current)
		return
	}
}
func (s *Service) Close(ctx context.Context, h, id string) (IDE, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.store.Get(ctx, h, id)
	if e != nil {
		return IDE{}, e
	}
	if !v.DesiredOpen {
		return v, nil
	}
	if v.State == Suspended {
		now := s.now().UTC()
		v.State = Closed
		v.DesiredOpen = false
		v.Reason = ""
		v.UpdatedAt = now
		v.ClosedAt = &now
		return s.store.Update(ctx, v)
	}
	v.State = Stopping
	v.Reason = ""
	v.UpdatedAt = s.now().UTC()
	v, e = s.store.Update(ctx, v)
	if e != nil {
		return v, e
	}
	stopErr := s.runtime.Stop(ctx, id)
	now := s.now().UTC()
	v.UpdatedAt = now
	if stopErr != nil {
		v.Reason = bounded(stopErr.Error())
		v, updateErr := s.store.Update(ctx, v)
		return v, errors.Join(stopErr, updateErr)
	}
	v.State = Closed
	v.DesiredOpen = false
	v.Reason = ""
	v.ClosedAt = &now
	v, updateErr := s.store.Update(ctx, v)
	return v, updateErr
}

// Suspend stops an IDE runtime while retaining user intent to reopen it
// when the owning Holon resumes. Failed stops remain retryable in Stopping.
func (s *Service) Suspend(ctx context.Context, h, id string) (IDE, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.store.Get(ctx, h, id)
	if err != nil {
		return IDE{}, err
	}
	if !v.DesiredOpen || v.State == Suspended {
		return v, nil
	}
	v.State = Stopping
	v.Reason = ""
	v.UpdatedAt = s.now().UTC()
	v, err = s.store.Update(ctx, v)
	if err != nil {
		return v, err
	}
	stopErr := s.runtime.Stop(ctx, id)
	now := s.now().UTC()
	v.UpdatedAt = now
	if stopErr != nil {
		v.Reason = bounded(stopErr.Error())
		v, updateErr := s.store.Update(ctx, v)
		return v, errors.Join(stopErr, updateErr)
	}
	v.State = Suspended
	v.Reason = ""
	v.ClosedAt = &now
	v, err = s.store.Update(ctx, v)
	return v, err
}

// Resume reuses a suspended IDE record and starts a new runtime for it.
func (s *Service) Resume(ctx context.Context, h, id string) (IDE, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.store.Get(ctx, h, id)
	if err != nil {
		return IDE{}, err
	}
	if !v.DesiredOpen || v.State != Suspended {
		return IDE{}, ErrInvalid
	}
	ws, err := s.workspaces.Workspace(ctx, h)
	if err != nil || strings.TrimSpace(ws.Path) == "" {
		return IDE{}, ErrInvalid
	}
	now := s.now().UTC()
	v.State = Starting
	v.Reason = ""
	v.ReadyAt = nil
	v.ClosedAt = nil
	v.UpdatedAt = now
	v, err = s.store.Update(ctx, v)
	if err != nil {
		return v, err
	}
	go s.launch(v, ws.Path)
	return v, nil
}
func (s *Service) Target(ctx context.Context, h, id string) (string, error) {
	v, e := s.store.Get(ctx, h, id)
	if e != nil {
		return "", e
	}
	if v.State != Ready || !v.DesiredOpen {
		return "", ErrNotReady
	}
	target, ok := s.runtime.Target(id)
	if !ok {
		return "", ErrNotReady
	}
	return target, nil
}
func (s *Service) Recover(ctx context.Context) error {
	list, e := s.store.OpenAtStartup(ctx)
	if e != nil {
		return e
	}
	for _, v := range list {
		if v.State == Suspended || v.State == Failed {
			continue
		}
		now := s.now().UTC()
		v.State = Lost
		v.DesiredOpen = false
		v.Reason = "Holark restarted; reopen the IDE explicitly."
		v.UpdatedAt = now
		v.ClosedAt = &now
		if _, e = s.store.Update(ctx, v); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) Shutdown() error { return s.runtime.Close() }
func BasePath(h, id string) string { return "/api/v1/holons/" + h + "/ides/" + id + "/proxy" }
func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "ide_" + hex.EncodeToString(b)
}
func (s *Service) failureReason(id, message string) string {
	// Reserve space for the log location when upstream diagnostics are lengthy.
	if len(message) > 700 {
		message = message[:700] + "…"
	}
	if path := s.runtime.LogPath(id); path != "" {
		message += " Log: " + path
	}
	return bounded(message)
}

func bounded(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 1024 {
		v = v[:1024]
	}
	return v
}
