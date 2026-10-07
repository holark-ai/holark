package localapp

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/pullrequestcomments"
	commentsqlite "github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/repository"
)

func TestApplicationJoinsDispatcherBeforeClosingDatabase(t *testing.T) {
	root := t.TempDir()
	applicationLockGit(t, root, "init", "-b", "main")
	applicationLockGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	applicationLockGit(t, root, "branch", "feature")
	remote := filepath.Join(t.TempDir(), "remote.git")
	applicationLockGit(t, root, "clone", "--bare", root, remote)
	applicationLockGit(t, root, "remote", "add", "origin", remote)
	localOnly := ""
	head := strings.TrimSpace(applicationLockGit(t, root, "rev-parse", "HEAD"))
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	launcher := reviewLaunchFunc(func(ctx context.Context, _ pullrequestwork.PullRequest, _ pullrequestwork.Work, _ string) (string, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return "", ctx.Err()
	})
	app, err := New(t.Context(), Options{RepositoryPath: root, HomeDirectory: t.TempDir(), PullRequestWorkLauncher: launcher, PullRequestRepositoryURL: remote, GitHubRepositoryURL: &localOnly})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	var closeOnce sync.Once
	closeApp := func() { closeOnce.Do(func() { go func() { closed <- app.Close() }() }) }
	t.Cleanup(func() {
		unblock()
		closeApp()
		select {
		case err := <-closed:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("application shutdown did not finish")
		}
	})
	prs, err := prsqlite.New(t.Context(), app.Database)
	if err != nil {
		t.Fatal(err)
	}
	_, err = prs.CreatePullRequest(pullrequestlifecycle.PullRequest{ID: "pr", RepositoryID: app.RepositoryID, Status: pullrequestlifecycle.StatusWIP, BaseBranch: "main", HeadBranch: "feature", BaseCommit: head, HeadCommit: head, DiffBaseCommit: head, BaseRef: repository.PublishedBranchIdentity(remote, "main"), HeadRef: repository.PublishedBranchIdentity(remote, "feature")})
	if err != nil {
		t.Fatal(err)
	}
	comments, err := commentsqlite.New(t.Context(), app.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := comments.Insert(t.Context(), pullrequestcomments.Comment{ID: "comment", PullRequestID: "pr", Body: "Address this", Status: pullrequestcomments.Unresolved, Scope: pullrequestcomments.ScopePullRequest, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	jobs, err := app.PullRequestWork.Start(requestCtx, pullrequestwork.Start{PullRequestID: "pr", Kind: pullrequestwork.KindWorker, CommentIDs: []string{"comment"}})
	cancelRequest()
	if err != nil || len(jobs) != 1 || jobs[0].Status != pullrequestwork.StatusQueued {
		t.Fatalf("admission=%+v err=%v", jobs, err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("application dispatcher did not launch admitted work")
	}
	closeApp()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel dispatch")
	}
	// The launcher deliberately holds shutdown after observing cancellation. Its
	// dependencies must remain usable until it returns and the dispatcher exits.
	if err := app.Database.PingContext(t.Context()); err != nil {
		t.Fatalf("database closed before dispatch finished: %v", err)
	}
	select {
	case <-app.dispatcherDone:
		t.Fatal("dispatcher finished before launch returned")
	default:
	}
	unblock()
}
