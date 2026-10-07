package workflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/issues"
	"github.com/holark-ai/holark/internal/issues/comments"
)

type commentStoreStub struct {
	mu     sync.Mutex
	d      comments.Discussion
	err    error
	writes int
}

func (s *commentStoreStub) ListComments(context.Context, string) (comments.Discussion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d, s.err
}
func (s *commentStoreStub) GetComment(_ context.Context, id string) (comments.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.d.Comments {
		if c.ID == id {
			return c, nil
		}
	}
	return comments.Comment{}, comments.ErrNotFound
}
func (s *commentStoreStub) UpsertComment(_ context.Context, c comments.Comment) (comments.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return c, s.err
	}
	s.writes++
	c.ID = "local"
	s.d.Comments = []comments.Comment{c}
	return c, nil
}
func (s *commentStoreStub) DeleteComment(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.writes++
	s.d.Comments = nil
	return nil
}
func (s *commentStoreStub) ReconcileComments(_ context.Context, _ string, c []comments.Comment, can *bool, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.writes++
	s.d = comments.Discussion{Comments: c, CanComment: can, SyncedAt: &at}
	return nil
}

type commentTransportStub struct {
	*fakeTransport
	remote  comments.Comment
	err     error
	calls   int
	entered chan struct{}
	release chan struct{}
	listed  chan struct{}
}

func (s *commentTransportStub) ListComments(context.Context, issues.Issue, string) ([]comments.Comment, *bool, error) {
	if s.listed != nil {
		close(s.listed)
	}
	can := true
	return []comments.Comment{s.remote}, &can, s.err
}
func (s *commentTransportStub) CreateComment(context.Context, issues.Issue, string, string) (comments.Comment, error) {
	s.calls++
	if s.entered != nil {
		close(s.entered)
		<-s.release
	}
	return s.remote, s.err
}
func (s *commentTransportStub) UpdateComment(context.Context, issues.Issue, string, comments.Comment, string) (comments.Comment, error) {
	s.calls++
	return s.remote, s.err
}
func (s *commentTransportStub) DeleteComment(context.Context, issues.Issue, string, comments.Comment) error {
	s.calls++
	return s.err
}
func commentService() (*Service, *commentTransportStub, *commentStoreStub) {
	p := newFakeProjection(nil)
	p.issues["stored"] = issue(7, "Stored")
	t := &commentTransportStub{fakeTransport: &fakeTransport{}, remote: comments.Comment{ID: "local", GitHubID: "123", URL: "https://github.com/o/r/issues/7#issuecomment-123", Body: "Remote", Author: comments.Author{Login: "outsider"}}}
	store := &commentStoreStub{d: comments.Discussion{Comments: []comments.Comment{{ID: "local", IssueID: "stored", Body: "Before"}}}}
	s := testService(t.fakeTransport, p, &fakeIdentities{})
	s.transport = t
	s.WithComments(store)
	return s, t, store
}
func TestCommentMutationsAreRemoteFirstAndNeverRetry(t *testing.T) {
	for _, op := range []string{"create", "edit", "delete"} {
		t.Run(op, func(t *testing.T) {
			for _, outcome := range []string{"rejected", "uncertain", "pending", "local failure", "success"} {
				t.Run(outcome, func(t *testing.T) {
					s, transport, store := commentService()
					switch outcome {
					case "rejected":
						transport.err = &CommentRejectedError{Status: 403, Message: "permission changed"}
					case "uncertain":
						transport.err = &CommentMutationError{Outcome: CommentUncertain, Err: errors.New("connection lost")}
					case "pending":
						transport.err = &CommentMutationError{Outcome: CommentAccepted, Err: errors.New("accepted")}
					case "local failure":
						store.err = errors.New("disk failed")
					}
					var err error
					switch op {
					case "create":
						_, err = s.CreateComment(t.Context(), "stored", "Body")
					case "edit":
						_, err = s.UpdateComment(t.Context(), "local", "Body")
					case "delete":
						err = s.DeleteComment(t.Context(), "local")
					}
					if transport.calls != 1 {
						t.Fatalf("provider calls=%d", transport.calls)
					}
					if outcome == "success" {
						if err != nil || store.writes != 1 {
							t.Fatalf("success: %v writes=%d", err, store.writes)
						}
						return
					}
					if err == nil || store.writes != 0 || store.d.Comments[0].Body != "Before" {
						t.Fatalf("failed mutation changed local data: %v %+v", err, store)
					}
					if outcome == "local failure" {
						var e *CommentMutationError
						if !errors.As(err, &e) || e.Outcome != CommentAccepted || e.GitHubID != "123" && op != "delete" {
							t.Fatalf("pending error: %#v", err)
						}
					}
				})
			}
		})
	}
}
func TestCommentValidationAndClosedIssues(t *testing.T) {
	s, transport, _ := commentService()
	if _, err := s.CreateComment(t.Context(), "stored", " \n"); !errors.Is(err, comments.ErrInvalidBody) || transport.calls != 0 {
		t.Fatal("invalid body sent")
	}
	projection := s.projection.(*fakeProjection)
	c := projection.issues["stored"]
	c.Status = issues.IssueClosed
	projection.issues["stored"] = c
	if _, err := s.CreateComment(t.Context(), "stored", "> quote"); err != nil {
		t.Fatal(err)
	}
}
func TestCommentSyncFailureKeepsCacheAndSnapshotIsLocal(t *testing.T) {
	s, transport, store := commentService()
	transport.err = errors.New("page two failed")
	if _, err := s.SyncComments(t.Context(), "stored"); err == nil || store.writes != 0 {
		t.Fatal("partial snapshot committed")
	}
	snapshot, err := s.Snapshot(t.Context(), "stored")
	if err != nil || snapshot.Discussion.TotalComments != 1 {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	store.err = errors.New("local read failed")
	if _, err = s.Snapshot(t.Context(), "stored"); err == nil {
		t.Fatal("snapshot ignored local failure")
	}
}
func TestCommentSyncSerializesWithPosting(t *testing.T) {
	s, transport, _ := commentService()
	transport.entered = make(chan struct{})
	transport.release = make(chan struct{})
	transport.listed = make(chan struct{})
	posted := make(chan error, 1)
	synced := make(chan error, 1)
	go func() { _, err := s.CreateComment(t.Context(), "stored", "Body"); posted <- err }()
	<-transport.entered
	go func() { _, err := s.SyncComments(t.Context(), "stored"); synced <- err }()
	select {
	case <-transport.listed:
		t.Fatal("sync raced with post")
	case <-time.After(25 * time.Millisecond):
	}
	close(transport.release)
	if err := <-posted; err != nil {
		t.Fatal(err)
	}
	if err := <-synced; err != nil {
		t.Fatal(err)
	}
}
func TestCommentWriteHoldsProjectLockAgainstIssueDeletion(t *testing.T) {
	s, transport, _ := commentService()
	transport.entered = make(chan struct{})
	transport.release = make(chan struct{})
	posted := make(chan error, 1)
	synced := make(chan error, 1)
	go func() { _, err := s.CreateComment(t.Context(), "stored", "Body"); posted <- err }()
	<-transport.entered
	go func() { _, err := s.Sync(t.Context(), "project"); synced <- err }()
	select {
	case err := <-synced:
		t.Fatalf("issue sync raced with comment: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(transport.release)
	if err := <-posted; err != nil {
		t.Fatal(err)
	}
	if err := <-synced; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(t.Context(), "stored"); !errors.Is(err, issues.ErrIssueNotFound) {
		t.Fatalf("issue not removed after release: %v", err)
	}
}
