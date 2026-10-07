//go:build integration

package pullrequestcomments_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/pullrequestcomments"
	commentshttp "github.com/holark-ai/holark/internal/pullrequestcomments/httpapi"
	"github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
)

func TestLocalHTTPAndWorkerCompletionFinishWhilePublicationIsBlocked(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &blockingGateway{blocked: make(chan struct{}), release: make(chan struct{})}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	mux := http.NewServeMux()
	commentshttp.RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service, providerTargets())
	server := httptest.NewServer(mux)
	defer server.Close()
	client := &http.Client{Timeout: time.Second}
	post := func(body string) pullrequestcomments.Comment {
		t.Helper()
		response, err := client.Post(server.URL+"/api/v1/pull-requests/pr-one/comments", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("local creation waited for GitHub: %v", err)
		}
		defer response.Body.Close()
		var comment pullrequestcomments.Comment
		if err := json.NewDecoder(response.Body).Decode(&comment); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 201 || comment.PublicationState != pullrequestcomments.PublicationPending {
			t.Fatalf("local response: %d %+v", response.StatusCode, comment)
		}
		return comment
	}
	parent := post(`{"body":"Fix this","scope":"line","path":"main.go","side":"RIGHT","line":8}`)
	stop := startCommentPublisher(t, service)
	select {
	case <-gateway.blocked:
	case <-time.After(time.Second):
		t.Fatal("publisher did not run without a page refresh")
	}
	post(`{"body":"Save during publication"}`)
	response, err := client.Get(server.URL + "/api/v1/pull-requests/pr-one/comments")
	if err != nil {
		t.Fatalf("cached GET waited for GitHub: %v", err)
	}
	var cached []pullrequestcomments.Comment
	if err := json.NewDecoder(response.Body).Decode(&cached); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(cached) != 2 {
		t.Fatalf("cached comments: %+v", cached)
	}
	request := pullrequestcomments.CreateComment{PullRequestID: "pr-one", ParentCommentID: parent.ID, Body: "Fixed", Origin: pullrequestcomments.WorkerOrigin("session", "worker", "published-head")}
	delivered := make(chan error, 1)
	go func() { _, err := service.CompleteWorkerReply(t.Context(), request); delivered <- err }()
	select {
	case err := <-delivered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("completion acknowledgement waited for GitHub")
	}
	resolved, err := service.Get(t.Context(), parent.ID)
	if err != nil || resolved.Status != pullrequestcomments.Resolved {
		t.Fatalf("local resolution: %+v %v", resolved, err)
	}
	if _, err := service.CompleteWorkerReply(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.PendingPublications(t.Context(), "pr-one")
	if err != nil || len(jobs) != 4 {
		t.Fatalf("durable replay jobs: %+v %v", jobs, err)
	}
	stop()
	for _, job := range jobs {
		if job.PermanentError != "" {
			t.Fatalf("cancellation permanently failed a job: %+v", job)
		}
	}
	close(gateway.release)
	// A fresh application service discovers committed jobs without notification or an open page.
	restarted := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	publishComments(t, restarted)
	jobs, err = store.PendingPublications(t.Context(), "pr-one")
	if err != nil || len(jobs) != 0 {
		t.Fatalf("restart did not drain jobs: %+v %v", jobs, err)
	}
	published, _ := restarted.Get(t.Context(), parent.ID)
	if published.PublicationState != pullrequestcomments.PublicationPublished || !published.UpdatedAt.Equal(resolved.UpdatedAt) {
		t.Fatalf("publication changed content timestamp: %+v", published)
	}
	if _, err := restarted.Reopen(t.Context(), parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.CompleteWorkerReply(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	reopened, _ := restarted.Get(t.Context(), parent.ID)
	if reopened.Status != pullrequestcomments.Unresolved {
		t.Fatal("completion replay overrode the user's reopening")
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.creates) != 3 || len(gateway.resolves) != 1 {
		t.Fatalf("duplicate publications: creates=%d resolves=%d", len(gateway.creates), len(gateway.resolves))
	}
}

func TestAtomicCreationReviewRecoveryAndCompletionReplay(t *testing.T) {
	db := openCommentsDB(t)
	store, err := sqliteadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(&lifecycleGateway{}, nil))
	exec := func(sql string) {
		t.Helper()
		if _, err := db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`create trigger fail_job before insert on pull_request_comment_publications begin select raise(abort, 'disk failure'); end`)
	if _, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "rollback"}); err == nil {
		t.Fatal("creation acknowledged a failed enqueue")
	}
	comments, _ := store.ListByPullRequest(t.Context(), "pr-one")
	jobs, _ := store.PendingPublications(t.Context(), "pr-one")
	if len(comments) != 0 || len(jobs) != 0 {
		t.Fatalf("partial creation: %+v %+v", comments, jobs)
	}
	exec(`drop trigger fail_job`)
	exec(`create trigger fail_second before insert on pull_request_comment_publications when new.payload like '%Second finding%' begin select raise(abort, 'disk failure'); end`)
	review := pullrequestcomments.CreateReviewComments{PullRequestID: "pr-one", Origin: pullrequestcomments.ReviewOrigin("session", "review", "review-head"), Comments: []pullrequestcomments.ReviewComment{{Body: "First finding", Scope: pullrequestcomments.ScopeFile, Path: "main.go"}, {Body: "Second finding", Scope: pullrequestcomments.ScopePullRequest}}}
	if _, err := service.CreateReviewComments(t.Context(), review); err == nil {
		t.Fatal("review acknowledged partial delivery")
	}
	comments, _ = store.ListByPullRequest(t.Context(), "pr-one")
	jobs, _ = store.PendingPublications(t.Context(), "pr-one")
	if len(comments) != 1 || len(jobs) != 1 {
		t.Fatalf("per-comment recovery: %+v %+v", comments, jobs)
	}
	first := comments[0]
	exec(`drop trigger fail_second`)
	imported, err := service.CreateReviewComments(t.Context(), review)
	if err != nil || len(imported) != 2 || imported[0].ID != first.ID || !imported[0].CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("review replay: %+v %v", imported, err)
	}
	exec(`create trigger fail_resolution before insert on pull_request_comment_publications when new.operation = 'resolve' begin select raise(abort, 'disk failure'); end`)
	reply := pullrequestcomments.CreateComment{PullRequestID: "pr-one", ParentCommentID: first.ID, Body: "Fixed", Origin: pullrequestcomments.WorkerOrigin("worker-session", "worker", "result-head")}
	if _, err := service.CompleteWorkerReply(t.Context(), reply); err == nil {
		t.Fatal("completion acknowledged missing resolution job")
	}
	parent, _ := store.Get(t.Context(), first.ID)
	if parent.Status != pullrequestcomments.Unresolved {
		t.Fatal("resolution did not roll back with its failed job")
	}
	exec(`drop trigger fail_resolution`)
	for range 2 {
		if _, err := service.CompleteWorkerReply(t.Context(), reply); err != nil {
			t.Fatal(err)
		}
	}
	comments, _ = store.ListByPullRequest(t.Context(), "pr-one")
	jobs, _ = store.PendingPublications(t.Context(), "pr-one")
	if len(comments) != 3 || len(jobs) != 4 {
		t.Fatalf("replay duplicated comments or jobs: %+v %+v", comments, jobs)
	}
}

func TestConcurrentPublisherMutationAndRefreshSerializeRemoteJobs(t *testing.T) {
	for _, operation := range []string{"edit", "delete"} {
		t.Run(operation, func(t *testing.T) {
			store, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
			if err != nil {
				t.Fatal(err)
			}
			gateway := &blockingGateway{blocked: make(chan struct{}), release: make(chan struct{})}
			service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
			comment, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Original"})
			if err != nil {
				t.Fatal(err)
			}
			stop := startCommentPublisher(t, service)
			select {
			case <-gateway.blocked:
			case <-time.After(time.Second):
				t.Fatal("publication did not start")
			}
			mutationDone := make(chan error, 1)
			go func() {
				if operation == "delete" {
					mutationDone <- service.Delete(t.Context(), comment.ID)
				} else {
					_, err := service.UpdateBody(t.Context(), comment.ID, "Edited")
					mutationDone <- err
				}
			}()
			// Cancellation must also interrupt a waiter for the remote lock.
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			if _, err := service.UpdateBody(ctx, comment.ID, "Cancelled"); err == nil {
				t.Fatal("cancelled remote-lock waiter succeeded")
			}
			refreshDone := make(chan error, 1)
			go func() { _, err := service.Sync(t.Context(), "pr-one"); refreshDone <- err }()
			close(gateway.release)
			if err := <-mutationDone; err != nil {
				t.Fatal(err)
			}
			if err := <-refreshDone; err != nil {
				t.Fatal(err)
			}
			stop()
			gateway.mu.Lock()
			defer gateway.mu.Unlock()
			if len(gateway.creates) != 1 {
				t.Fatalf("same creation executed %d times", len(gateway.creates))
			}
			if operation == "edit" && (len(gateway.updates) != 1 || gateway.updates[0].Body != "Edited" || gateway.updates[0].ProviderIdentity.ExternalID == "") {
				t.Fatalf("edit missed in-flight identity: %+v", gateway.updates)
			}
			if operation == "delete" && (len(gateway.deletes) != 1 || gateway.deletes[0].ProviderIdentity.ExternalID == "") {
				t.Fatalf("delete missed in-flight identity: %+v", gateway.deletes)
			}
		})
	}
}

func TestQueuedReplyQuotationUsesPrecedingPublishedReply(t *testing.T) {
	store, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
	if err != nil {
		t.Fatal(err)
	}
	gateway := &lifecycleGateway{}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	parent, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Root"})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"First", "Second", "Third"} {
		if _, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", ParentCommentID: parent.ID, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	publishComments(t, service)
	for index, want := range []string{"", "Root", "First", "Second"} {
		if got := gateway.creates[index].ReplyContextBody; got != want {
			t.Fatalf("reply %d quotation=%q, want %q", index, got, want)
		}
	}
}

func TestCreationDuringFullRefreshRemainsLocallyVisible(t *testing.T) {
	gateway := &refreshGateway{entered: make(chan struct{}, 1), release: make(chan struct{})}
	service, _ := refreshService(t, gateway)
	done := make(chan error, 1)
	go func() { _, err := service.Refresh(t.Context(), "pr-one", true); done <- err }()
	select {
	case <-gateway.entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not enter provider")
	}
	created := make(chan pullrequestcomments.Comment, 1)
	go func() {
		comment, _ := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "During fetch"})
		created <- comment
	}()
	var saved pullrequestcomments.Comment
	select {
	case saved = <-created:
	case <-time.After(time.Second):
		t.Fatal("creation waited for remote refresh")
	}
	if saved.ID == "" {
		t.Fatal("creation failed")
	}
	cached, err := service.ListByPullRequest(t.Context(), "pr-one")
	if err != nil || len(cached) != 1 || cached[0].ID != saved.ID {
		t.Fatalf("cached comments during fetch: %+v %v", cached, err)
	}
	close(gateway.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cached, err = service.ListByPullRequest(t.Context(), "pr-one")
	if err != nil || len(cached) != 1 || cached[0].ID != saved.ID || cached[0].PublicationState != pullrequestcomments.PublicationPending {
		t.Fatalf("reconciliation lost local intent: %+v %v", cached, err)
	}
}

func TestEditAndDeleteBeforePublisherStarts(t *testing.T) {
	store, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
	if err != nil {
		t.Fatal(err)
	}
	gateway := &lifecycleGateway{}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	deleted, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Delete before sending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(t.Context(), deleted.ID); err != nil {
		t.Fatal(err)
	}
	if len(gateway.creates) != 0 || len(gateway.deletes) != 0 {
		t.Fatal("unstarted deletion called provider")
	}
	edited, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	edited, err = service.UpdateBody(t.Context(), edited.ID, "Current text")
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.creates) != 1 || gateway.creates[0].Body != "Current text" || edited.PublicationState != pullrequestcomments.PublicationPublished {
		t.Fatalf("edit restored old creation payload: %+v %+v", gateway.creates, edited)
	}
	jobs, _ := store.PendingPublications(t.Context(), "pr-one")
	if len(jobs) != 0 {
		t.Fatalf("edit left duplicate creations: %+v", jobs)
	}
}

func TestPublisherScansSynchronousRetriesWithoutPageOrNotification(t *testing.T) {
	store, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
	if err != nil {
		t.Fatal(err)
	}
	gateway := &lifecycleGateway{}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	comment, err := createAndPublish(t, service, pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	gateway.updateError = pullrequestcomments.RetryableProviderError(context.DeadlineExceeded, 200*time.Millisecond)
	if _, err := service.UpdateBody(t.Context(), comment.ID, "Retry this edit"); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.PendingPublications(t.Context(), "pr-one")
	if err != nil || len(jobs) != 1 || jobs[0].Attempts != 1 {
		t.Fatalf("synchronous retry not durable: %+v %v", jobs, err)
	}
	gateway.updateError = nil
	// A new service has no notification; its startup scan must respect the deadline,
	// then the one-second database scan must discover the now-due mutation.
	restarted := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	stop := startCommentPublisher(t, restarted)
	time.Sleep(40 * time.Millisecond)
	gateway.mu.Lock()
	attempts := len(gateway.updates)
	gateway.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("retried before provider deadline: %d", attempts)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		updated, err := restarted.Get(t.Context(), comment.ID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.PublicationState == pullrequestcomments.PublicationPublished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("database scan did not publish due retry")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.updates) != 2 {
		t.Fatalf("retry count: %d", len(gateway.updates))
	}
}

func TestCancelledPublicationPassLeavesWorkRecoverableAndVisitsNextPR(t *testing.T) {
	store, err := sqliteadapter.New(t.Context(), openCommentsDB(t))
	if err != nil {
		t.Fatal(err)
	}
	gateway := &blockingGateway{blocked: make(chan struct{}), release: make(chan struct{})}
	service := pullrequestcomments.NewService(store, providerTargets(), pullrequestcomments.WithProviderGateway(gateway, nil))
	first, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-one", Body: "Slow PR"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(t.Context(), pullrequestcomments.CreateComment{PullRequestID: "pr-two", Body: "Next PR"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var visited []string
	service.RunPublisher(ctx, pullrequestcomments.RefreshSchedulerFunc(func(ctx context.Context, _, id string, work func(context.Context) error) error {
		visited = append(visited, id)
		pass, stop := context.WithTimeout(ctx, 20*time.Millisecond)
		defer stop()
		err := work(pass)
		if id == "pr-two" {
			cancel()
		}
		return err
	}))
	if len(visited) != 2 || visited[0] != "pr-one" || visited[1] != "pr-two" {
		t.Fatalf("sweep fairness: %v", visited)
	}
	pending, _ := service.Get(t.Context(), first.ID)
	published, _ := service.Get(t.Context(), second.ID)
	jobs, _ := store.PendingPublications(t.Context(), "pr-one")
	if pending.PublicationState != pullrequestcomments.PublicationPending || published.PublicationState != pullrequestcomments.PublicationPublished || len(jobs) != 1 || jobs[0].PermanentError != "" {
		t.Fatalf("cancelled pass lost work: pending=%+v published=%+v jobs=%+v", pending, published, jobs)
	}
}
