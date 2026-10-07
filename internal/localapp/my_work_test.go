package localapp

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubissues "github.com/holark-ai/holark/internal/codehost/github/issues"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/githubidentity"
	membersql "github.com/holark-ai/holark/internal/githubidentity/storeadapter"
	"github.com/holark-ai/holark/internal/issues"
	issuesql "github.com/holark-ai/holark/internal/issues/storeadapter"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	prsql "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	participantsql "github.com/holark-ai/holark/internal/pullrequestparticipants/sqliteadapter"
	worksql "github.com/holark-ai/holark/internal/workitems/sqliteadapter"
)

type scheduledIssueRead struct {
	open bool
	done chan error
}
type scheduledIssueSource struct{ reads chan scheduledIssueRead }

func (s *scheduledIssueSource) list(ctx context.Context, open bool) ([]githubapi.Issue, error) {
	request := scheduledIssueRead{open: open, done: make(chan error)}
	select {
	case s.reads <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case err := <-request.done:
		return []githubapi.Issue{}, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *scheduledIssueSource) ListIssues(ctx context.Context, _ githubapi.Repository) ([]githubapi.Issue, error) {
	return s.list(ctx, false)
}
func (s *scheduledIssueSource) ListOpenIssues(ctx context.Context, _ githubapi.Repository) ([]githubapi.Issue, error) {
	return s.list(ctx, true)
}
func (s *scheduledIssueSource) GetIssue(context.Context, githubapi.Repository, int) (githubapi.Issue, error) {
	panic("no cached issues to fetch")
}

func newScheduledIssueSync(t *testing.T) (*personalWorkSync, *scheduledIssueSource, *sql.DB) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "issues.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec("create table repositories(id text primary key);insert into repositories values('repo')"); err != nil {
		t.Fatal(err)
	}
	if _, err := prsql.New(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	issueStore, err := issuesql.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	members, err := membersql.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	source := &scheduledIssueSource{reads: make(chan scheduledIssueRead)}
	workflow := issueworkflow.New(func(string) (issueworkflow.Project, bool) {
		return issueworkflow.Project{ID: "repo", RepositoryURL: "https://github.com/owner/repo"}, true
	}, githubissues.New(source), issues.NewService(issueStore), githubidentity.NewService(members, nil))
	if _, err := participantsql.New(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	state, err := worksql.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	workflow.WithSyncRecorder(state)
	scheduler := pr.NewRefreshCoordinator()
	t.Cleanup(scheduler.Close)
	syncer := &personalWorkSync{repositoryID: "repo", issues: workflow, scheduler: scheduler}
	return syncer, source, db
}

func TestScheduledIssuesImportHistoryAtStartupAndOpenIssuesAfterThirtySeconds(t *testing.T) {
	syncer, source, _ := newScheduledIssueSync(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); syncer.run(ctx, "issues", openIssueRefreshInterval) }()
	read := func() scheduledIssueRead {
		t.Helper()
		select {
		case r := <-source.reads:
			return r
		case <-time.After(35 * time.Second):
			t.Fatal("issue refresh did not run")
			return scheduledIssueRead{}
		}
	}
	first := read()
	if first.open {
		t.Fatal("startup did not import history")
	}
	started := time.Now()
	close(first.done)
	next := read()
	if !next.open {
		t.Fatal("recurring refresh imported history")
	}
	if elapsed := time.Since(started); elapsed < 30*time.Second {
		t.Fatalf("refresh interval=%s", elapsed)
	}
	close(next.done)
	// Explicit Sync still uses a full import through the shared scheduler.
	manualDone := make(chan error, 1)
	go func() {
		manualDone <- syncer.scheduler.Do(ctx, pr.RefreshKey{RepositoryID: "repo", Section: "issues"}, syncer.section("issues"))
	}()
	manual := read()
	if manual.open {
		t.Fatal("manual sync did not import history")
	}
	close(manual.done)
	if err := <-manualDone; err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}

func TestScheduledIssuesRetryFailedHistoryBeforeOpenRefresh(t *testing.T) {
	syncer, source, db := newScheduledIssueSync(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	const interval = 10 * time.Millisecond
	go func() { defer close(done); syncer.run(ctx, "issues", interval) }()
	read := func() scheduledIssueRead {
		t.Helper()
		select {
		case r := <-source.reads:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("issue refresh did not run")
			return scheduledIssueRead{}
		}
	}
	assertState := func(wantError string) {
		t.Helper()
		var message, syncedAt string
		if err := db.QueryRowContext(ctx, "select error, synced_at from work_sync_state where repository_id='repo' and section='issues'").Scan(&message, &syncedAt); err != nil {
			t.Fatal(err)
		}
		if wantError != "" {
			if !strings.Contains(message, wantError) || syncedAt != "" {
				t.Fatalf("failed history status: error=%q synced_at=%q", message, syncedAt)
			}
		} else if message != "" || syncedAt == "" {
			t.Fatalf("successful history status: error=%q synced_at=%q", message, syncedAt)
		}
	}
	first := read()
	if first.open {
		t.Fatal("startup did not import history")
	}
	failure := errors.New("temporary history import failure")
	started := time.Now()
	first.done <- failure
	retry := read()
	if retry.open {
		t.Fatal("failed startup switched to open-only refresh")
	}
	if elapsed := time.Since(started); elapsed < 2*interval {
		t.Fatalf("retry skipped failure backoff: %s", elapsed)
	}
	assertState(failure.Error())
	close(retry.done)
	open := read()
	if !open.open {
		t.Fatal("successful history retry did not switch to open-only refresh")
	}
	assertState("")
}
