package repository

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrInvalidRepository     = errors.New("invalid repository")
	ErrRepositoryUnavailable = errors.New("repository unavailable")
	ErrRefNotFound           = errors.New("ref not found")
	ErrPathNotFound          = errors.New("path not found")
	ErrPathNotDirectory      = errors.New("path is not a directory")
	ErrPathNotFile           = errors.New("path is not a file")
	ErrInvalidPath           = errors.New("invalid path")
	ErrBinaryFile            = errors.New("binary file")
	ErrFileTooLarge          = errors.New("file too large")
)

type Descriptor struct {
	Root          string `json:"root"`
	CommonDir     string `json:"common_dir"`
	ID            string `json:"id"`
	DefaultBranch string `json:"default_branch"`
	RepositoryURL string `json:"repository_url,omitempty"`
}

type Ref struct {
	Name        string    `json:"name"`
	ShortName   string    `json:"short_name"`
	Kind        string    `json:"kind"`
	Target      string    `json:"target"`
	CommittedAt time.Time `json:"committed_at"`
	AuthorName  string    `json:"author_name,omitempty"`
	Subject     string    `json:"subject,omitempty"`
}
type TreeEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Mode     string `json:"mode"`
	Language string `json:"language,omitempty"`
	Size     int64  `json:"size,omitempty"`
}
type Tree struct {
	Commit  string      `json:"commit"`
	Ref     string      `json:"ref"`
	Path    string      `json:"path"`
	Entries []TreeEntry `json:"entries"`
}
type Blob struct {
	Commit    string `json:"commit"`
	Ref       string `json:"ref"`
	Path      string `json:"path"`
	Language  string `json:"language"`
	Content   string `json:"content"`
	Encoding  string `json:"encoding"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}
type Commit struct {
	SHA         string    `json:"sha"`
	Message     string    `json:"message"`
	AuthorName  string    `json:"author_name,omitempty"`
	AuthorEmail string    `json:"author_email,omitempty"`
	AuthoredAt  time.Time `json:"authored_at"`
}
type Change struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Patch     string `json:"patch"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}
type DirtyState struct {
	Dirty       bool   `json:"dirty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Staged      int    `json:"staged,omitempty"`
	Unstaged    int    `json:"unstaged,omitempty"`
	Untracked   int    `json:"untracked,omitempty"`
}

// Preparation is the immutable provider branch and commit selected for a new
// workspace. Branch is always the canonical provider name (for example main).
type Preparation struct {
	Branch string `json:"ref"`
	Commit string `json:"commit"`
}

// CanonicalProviderBranch keeps old origin/<branch> records compatible while
// ensuring newly persisted provider branch names do not include the remote.
func CanonicalProviderBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	return strings.TrimPrefix(branch, "origin/")
}

// Store is the repository domain's narrow port. Implementations own all Git details.
type Store interface {
	Descriptor() Descriptor
	Refs(context.Context) ([]Ref, error)
	Refresh(context.Context) ([]Ref, error)
	Tree(context.Context, string, string) (Tree, error)
	Blob(context.Context, string, string) (Blob, error)
	Commits(context.Context, string, int) ([]Commit, error)
	CommitPage(context.Context, string, int, int) ([]Commit, error)
	Changes(context.Context, string, string) ([]Change, error)
	Resolve(context.Context, string) (string, error)
	Dirty(context.Context) (DirtyState, error)
}
type Publisher interface {
	Publish(context.Context, string, string) error
}

type BranchPreparer interface {
	PrepareBranch(context.Context, string) (Preparation, error)
}

type ProviderBranchResolver interface {
	ResolveProviderBranch(context.Context, string) (string, error)
}

type DefaultRefProvider interface {
	DefaultRef() string
}

type Service struct {
	store    Store
	mu       sync.Mutex
	refresh  *refreshCall
	lifetime context.Context
	branches map[string]*branchCall
}

type refreshCall struct {
	done    chan struct{}
	refs    []Ref
	err     error
	waiters int
	users   int
	cancel  context.CancelFunc
	id      string
}

func NewService(store Store) *Service { return NewServiceWithContext(context.Background(), store) }

// NewServiceWithContext ties shared work to application shutdown, not its first caller.
func NewServiceWithContext(ctx context.Context, store Store) *Service {
	return &Service{store: store, lifetime: ctx, branches: make(map[string]*branchCall)}
}

func (s *Service) Descriptor() Descriptor { return s.store.Descriptor() }
func (s *Service) Refs(ctx context.Context) ([]Ref, error) {
	refs, err := s.store.Refs(ctx)
	if refs == nil {
		refs = []Ref{}
	}
	return refs, err
}
func (s *Service) Tree(ctx context.Context, ref, path string) (Tree, error) {
	return s.store.Tree(ctx, ref, path)
}
func (s *Service) Blob(ctx context.Context, ref, path string) (Blob, error) {
	return s.store.Blob(ctx, ref, path)
}
func (s *Service) Commits(ctx context.Context, ref string, limit int) ([]Commit, error) {
	commits, err := s.store.Commits(ctx, ref, limit)
	if commits == nil {
		commits = []Commit{}
	}
	return commits, err
}

// CommitRange returns only commits reachable from head after base, oldest first.
// It is a separate narrow capability so simple repository stores remain valid.
type MergeBaseStore interface {
	MergeBase(context.Context, string, string) (string, error)
}

func (s *Service) MergeBase(ctx context.Context, base, head string) (string, error) {
	store, ok := s.store.(MergeBaseStore)
	if !ok {
		return "", ErrRepositoryUnavailable
	}
	return store.MergeBase(ctx, base, head)
}

type CommitRangeStore interface {
	CommitRange(context.Context, string, string) ([]Commit, error)
}

func (s *Service) CommitRange(ctx context.Context, base, head string) ([]Commit, error) {
	store, ok := s.store.(CommitRangeStore)
	if !ok {
		return nil, ErrRepositoryUnavailable
	}
	commits, err := store.CommitRange(ctx, base, head)
	if commits == nil {
		commits = []Commit{}
	}
	return commits, err
}
func (s *Service) Changes(ctx context.Context, base, target string) ([]Change, error) {
	changes, err := s.store.Changes(ctx, base, target)
	if changes == nil {
		changes = []Change{}
	}
	return changes, err
}
func (s *Service) Resolve(ctx context.Context, ref string) (string, error) {
	return s.store.Resolve(ctx, ref)
}
func (s *Service) PrepareBranch(ctx context.Context, branch string) (Preparation, error) {
	store, ok := s.store.(BranchPreparer)
	if !ok {
		return Preparation{}, ErrRepositoryUnavailable
	}
	return store.PrepareBranch(ctx, branch)
}
func (s *Service) ResolveProviderBranch(ctx context.Context, branch string) (string, error) {
	store, ok := s.store.(ProviderBranchResolver)
	if !ok {
		return "", ErrRepositoryUnavailable
	}
	return store.ResolveProviderBranch(ctx, branch)
}
func (s *Service) DefaultRef() string {
	if store, ok := s.store.(DefaultRefProvider); ok {
		return store.DefaultRef()
	}
	branch := CanonicalProviderBranch(s.Descriptor().DefaultBranch)
	if branch == "" || branch == "HEAD" {
		return "HEAD"
	}
	return "refs/heads/" + branch
}
func (s *Service) Dirty(ctx context.Context) (DirtyState, error) { return s.store.Dirty(ctx) }
func (s *Service) Publish(ctx context.Context, commit, branch string) error {
	p, ok := s.store.(Publisher)
	if !ok {
		return errors.New("repository publication is unavailable")
	}
	return p.Publish(ctx, commit, branch)
}

// Refresh shares only in-progress work. Every caller owns a cancellable wait.
func (s *Service) Refresh(ctx context.Context) ([]Ref, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	call := s.refresh
	if call == nil {
		work, cancel := s.sharedContext(ctx)
		call = &refreshCall{done: make(chan struct{}), cancel: cancel, id: fmt.Sprintf("repository-%d-%d", os.Getpid(), sharedSequence.Add(1))}
		s.refresh = call
		slog.DebugContext(work, "shared refresh started", "operation_id", call.id, "repository_id", s.Descriptor().ID)
		go func() {
			defer cancel()
			refs, err := s.store.Refresh(work)
			if refs == nil {
				refs = []Ref{}
			}
			s.mu.Lock()
			call.refs, call.err = refs, err
			slog.DebugContext(work, "shared refresh completed", "operation_id", call.id, "failed", err != nil, "cancellation", work.Err())
			if s.refresh == call {
				s.refresh = nil
			}
			close(call.done)
			s.mu.Unlock()
		}()
	} else {
		call.waiters++
	}
	call.users++
	slog.DebugContext(ctx, "shared repository operation joined", "operation_id", call.id, "waiters", call.users)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		call.users--
		slog.DebugContext(ctx, "shared repository operation left", "operation_id", call.id, "waiters", call.users, "cancellation", ctx.Err())
		if call.users == 0 {
			call.cancel()
			if s.refresh == call {
				s.refresh = nil
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-call.done:
		return append([]Ref{}, call.refs...), call.err
	}
}

type branchCall struct {
	done   chan struct{}
	result Preparation
	err    error
	users  int
	cancel context.CancelFunc
	id     string
}

// PrepareBranchShared is for polling and preparation previews. Mutating actions
// use PrepareBranch directly so their freshness check starts after the action.
func (s *Service) PrepareBranchShared(ctx context.Context, branch string) (Preparation, error) {
	if err := ctx.Err(); err != nil {
		return Preparation{}, err
	}
	branch = CanonicalProviderBranch(branch)
	s.mu.Lock()
	call := s.branches[branch]
	if call == nil {
		work, cancel := s.sharedContext(ctx)
		call = &branchCall{done: make(chan struct{}), cancel: cancel, id: fmt.Sprintf("repository-%d-%d", os.Getpid(), sharedSequence.Add(1))}
		s.branches[branch] = call
		slog.DebugContext(work, "shared branch preparation started", "operation_id", call.id, "repository_id", s.Descriptor().ID)
		go func() {
			defer cancel()
			result, err := s.PrepareBranch(work, branch)
			s.mu.Lock()
			call.result, call.err = result, err
			slog.DebugContext(work, "shared branch preparation completed", "operation_id", call.id, "failed", err != nil, "cancellation", work.Err())
			if s.branches[branch] == call {
				delete(s.branches, branch)
			}
			close(call.done)
			s.mu.Unlock()
		}()
	}
	call.users++
	slog.DebugContext(ctx, "shared repository operation joined", "operation_id", call.id, "waiters", call.users)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		call.users--
		slog.DebugContext(ctx, "shared repository operation left", "operation_id", call.id, "waiters", call.users, "cancellation", ctx.Err())
		if call.users == 0 {
			call.cancel()
			if s.branches[branch] == call {
				delete(s.branches, branch)
			}
		}
	}()
	select {
	case <-ctx.Done():
		return Preparation{}, ctx.Err()
	case <-call.done:
		return call.result, call.err
	}
}

var sharedSequence atomic.Uint64

// Preserve correlation values while allowing every waiter to own its own lifetime.
func (s *Service) sharedContext(caller context.Context) (context.Context, context.CancelFunc) {
	work, cancel := context.WithTimeout(context.WithoutCancel(caller), 60*time.Second)
	stop := context.AfterFunc(s.lifetime, cancel)
	if s.lifetime.Err() != nil {
		cancel()
	}
	return work, func() { stop(); cancel() }
}
