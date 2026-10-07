package localapp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	commentsqlite "github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	worksqlite "github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
)

type deliveryTarget struct{}

func (deliveryTarget) GetCommentTarget(context.Context, string) (pullrequestcomments.PullRequestTarget, error) {
	return pullrequestcomments.PullRequestTarget{ID: "pr", HeadCommit: "head", Status: pullrequestcomments.PullRequestOpen, SyncProvider: "github", RepositoryURL: "https://github.com/fixture/repo", ProviderPullRequest: 1}, nil
}

type blockedDeliveryProvider struct {
	pullrequestcomments.ProviderGateway
	entered chan struct{}
	release chan struct{}
}

func (g blockedDeliveryProvider) ListComments(ctx context.Context, _ pullrequestcomments.ProviderTarget) ([]pullrequestcomments.RemoteComment, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (g blockedDeliveryProvider) CreateComment(context.Context, pullrequestcomments.ProviderTarget, pullrequestcomments.RemoteMutation) (pullrequestcomments.ProviderIdentity, error) {
	return pullrequestcomments.ProviderIdentity{}, pullrequestcomments.PermanentProviderError(errors.New("rejected"))
}

func TestPendingCommentAdmissionAndCompletionAcknowledgementIgnoreBlockedGitHub(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "comments.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`create table repositories(id text primary key); insert into repositories(id) values('repo')`); err != nil {
		t.Fatal(err)
	}
	prs, err := prsqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prs.CreatePullRequest(pullrequestlifecycle.PullRequest{ID: "pr", RepositoryID: "repo", Status: pullrequestlifecycle.StatusOpen, HeadCommit: "head"}); err != nil {
		t.Fatal(err)
	}
	store, err := commentsqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	gateway := blockedDeliveryProvider{entered: make(chan struct{}, 1), release: make(chan struct{})}
	comments := pullrequestcomments.NewService(store, deliveryTarget{}, pullrequestcomments.WithProviderGateway(gateway, nil))
	root, err := comments.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr", Body: "Address me", Scope: pullrequestcomments.ScopeFile, Path: "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	works, err := worksqlite.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	running := pullrequestwork.Work{ID: "running", PullRequestID: "pr", CommentID: root.ID, SessionID: "worker-holon", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, HeadCommit: "head", Status: pullrequestwork.StatusRunning, CreatedAt: time.Now().UTC()}
	if err := works.Create(t.Context(), running); err != nil {
		t.Fatal(err)
	}
	work := pullrequestwork.New(works, addressCompletionCatalog{pullrequestwork.PullRequest{ID: "pr", Active: true, HeadCommit: "head"}}, nil, localReviewComments{comments: comments})
	work.SetComments(localWorkComments{comments: comments})
	admitted, err := work.Start(t.Context(), pullrequestwork.Start{PullRequestID: "pr", Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, CommentIDs: []string{root.ID}})
	if err != nil || len(admitted) != 1 || admitted[0].Status != pullrequestwork.StatusQueued {
		t.Fatalf("pending Address admission: %+v %v", admitted, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() { defer close(stopped); comments.RunPublisher(ctx, nil) }()
	t.Cleanup(func() { cancel(); <-stopped })
	select {
	case <-gateway.entered:
	case <-time.After(time.Second):
		t.Fatal("publisher did not block")
	}
	completed := make(chan error, 1)
	go func() {
		result, err := work.Complete(t.Context(), running.ID, "head", pullrequestwork.Completion{ResultHeadCommit: "result", ReplyBody: "Fixed"})
		if err == nil && (result.Status != pullrequestwork.StatusCompleted || !result.ArtifactImported) {
			err = errors.New("comment delivery not acknowledged")
		}
		completed <- err
	}()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Holark completion waited for GitHub")
	}
	parent, err := comments.Get(t.Context(), root.ID)
	if err != nil || parent.Status != pullrequestcomments.Resolved {
		t.Fatalf("automatic local resolution: %+v %v", parent, err)
	}
	close(gateway.release)
	deadline := time.Now().Add(time.Second)
	for {
		parent, _ = comments.Get(t.Context(), root.ID)
		if parent.PublicationState == pullrequestcomments.PublicationFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("publisher did not record permanent failure")
		}
		time.Sleep(time.Millisecond)
	}
	stored, err := works.Get(t.Context(), running.ID)
	if err != nil || stored.Status != pullrequestwork.StatusCompleted {
		t.Fatalf("late GitHub failure changed completed work: %+v %v", stored, err)
	}
}
