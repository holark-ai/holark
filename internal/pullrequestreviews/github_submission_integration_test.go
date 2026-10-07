//go:build integration

package pullrequestreviews_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubreviews "github.com/holark-ai/holark/internal/codehost/github/pullrequestreviews"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/pullrequestreviews"
	"github.com/holark-ai/holark/internal/pullrequestreviews/sqliteadapter"
)

type reviewTarget struct{ pullrequestreviews.Target }

func (target *reviewTarget) GetReviewTarget(context.Context, string) (pullrequestreviews.Target, error) {
	return target.Target, nil
}

type remoteReview struct {
	ID       int    `json:"id"`
	Body     string `json:"body"`
	State    string `json:"state"`
	CommitID string `json:"commit_id"`
	URL      string `json:"html_url"`
}

type reviewClient struct {
	head         string
	mutationErr  error
	listErr      error
	visible      bool
	omitResponse bool
	posts        int
	reviews      []remoteReview
	beforePost   func()
	afterPost    func()
}

func reviewResponse(output, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, output)
}

func (client *reviewClient) Request(_ context.Context, method, path string, _ any, output any) error {
	if method != "GET" {
		return fmt.Errorf("unexpected GitHub request: %s %s", method, path)
	}
	if strings.Contains(path, "/reviews?") {
		if client.listErr != nil {
			return client.listErr
		}
		if client.visible {
			return reviewResponse(output, client.reviews)
		}
		return reviewResponse(output, []remoteReview{})
	}
	return reviewResponse(output, map[string]any{"head": map[string]string{"sha": client.head}})
}

func (client *reviewClient) RequestMutation(_ context.Context, method, path string, input, output any) error {
	if method != "POST" || !strings.HasSuffix(path, "/reviews") {
		return fmt.Errorf("unexpected GitHub mutation: %s %s", method, path)
	}
	if client.beforePost != nil {
		client.beforePost()
	}
	client.posts++
	var rejection *githubapi.RejectionError
	if errors.As(client.mutationErr, &rejection) {
		if client.afterPost != nil {
			client.afterPost()
		}
		return client.mutationErr
	}
	var request struct {
		Event    string `json:"event"`
		Body     string `json:"body"`
		CommitID string `json:"commit_id"`
	}
	if err := reviewResponse(&request, input); err != nil {
		return err
	}
	state := "COMMENTED"
	if request.Event == "APPROVE" {
		state = "APPROVED"
	}
	remote := remoteReview{ID: 42, Body: request.Body, State: state, CommitID: request.CommitID, URL: "https://github.com/o/r/pull/7#pullrequestreview-42"}
	client.reviews = append(client.reviews, remote)
	if client.afterPost != nil {
		client.afterPost()
	}
	if client.mutationErr != nil {
		return client.mutationErr
	}
	if client.omitResponse {
		return reviewResponse(output, map[string]any{})
	}
	return reviewResponse(output, remote)
}

type failingReceiptRepository struct{ pullrequestreviews.Repository }

func (repository failingReceiptRepository) Save(ctx context.Context, review pullrequestreviews.Review) error {
	if review.WriteOutcome == pullrequestreviews.WriteConfirmed {
		return errors.New("receipt could not be saved")
	}
	return repository.Repository.Save(ctx, review)
}

func TestGitHubReviewSubmissionRecoversUnconfirmedWritesAcrossRestart(t *testing.T) {
	for _, test := range []struct {
		name            string
		mutationErr     error
		omitResponse    bool
		cancel          bool
		failReceiptSave bool
	}{
		{name: "interrupted POST", mutationErr: &githubapi.Error{Code: githubapi.ErrorCodeMutationUncertain, Err: errors.New("connection lost")}},
		{name: "accepted without receipt", mutationErr: &githubapi.Error{Code: githubapi.ErrorCodeMutationAccepted, Err: errors.New("unreadable response")}},
		{name: "missing confirmation", omitResponse: true},
		{name: "unclassified transport error", mutationErr: errors.New("connection lost")},
		{name: "cancelled request", mutationErr: context.Canceled, cancel: true},
		{name: "confirmed POST without local receipt", failReceiptSave: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "reviews.sqlite")
			db, err := database.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.ExecContext(ctx, "create table pull_requests(id text primary key); insert into pull_requests values('pr')"); err != nil {
				t.Fatal(err)
			}
			store, err := sqliteadapter.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			target := &reviewTarget{pullrequestreviews.Target{ID: "pr", Status: "open", HeadCommit: "head", ComparisonCurrent: true, Provider: "github", RepositoryURL: "https://github.com/o/r", Number: 7}}
			client := &reviewClient{head: "head", mutationErr: test.mutationErr, omitResponse: test.omitResponse}
			input := pullrequestreviews.Submission{ID: "manual-review-1234", Event: pullrequestreviews.Approve, Body: "Looks good", HeadCommit: "head"}
			client.beforePost = func() {
				checkpoint, err := store.Get(ctx, input.ID)
				if err != nil || checkpoint.WriteOutcome != pullrequestreviews.WriteUnconfirmed || checkpoint.State != "pending" {
					t.Fatalf("missing durable checkpoint before POST: %+v, %v", checkpoint, err)
				}
			}
			submitCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			if test.cancel {
				client.afterPost = cancel
			}
			var repository pullrequestreviews.Repository = store
			if test.failReceiptSave {
				repository = failingReceiptRepository{store}
			}
			service := pullrequestreviews.New(repository, target, githubreviews.New(client))
			review, err := service.Submit(submitCtx, "pr", input)
			if test.failReceiptSave {
				if err == nil {
					t.Fatal("expected receipt persistence failure")
				}
			} else if err != nil || review.State != "pending" || review.WriteOutcome != pullrequestreviews.WriteUnconfirmed || client.posts != 1 {
				t.Fatalf("unconfirmed submission: %+v, %v, posts=%d", review, err, client.posts)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = database.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store, err = sqliteadapter.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			service = pullrequestreviews.New(store, target, githubreviews.New(client))
			for range 2 {
				review, err = service.Retry(ctx, "pr", input.ID)
				if err != nil || review.State != "pending" || review.WriteOutcome != pullrequestreviews.WriteUnconfirmed || client.posts != 1 {
					t.Fatalf("empty receipt lookup resubmitted review: %+v, %v, posts=%d", review, err, client.posts)
				}
			}
			client.listErr = errors.New("GitHub unavailable")
			review, err = service.Retry(ctx, "pr", input.ID)
			if err != nil || review.State != "pending" || client.posts != 1 {
				t.Fatalf("failed receipt lookup lost uncertainty: %+v, %v", review, err)
			}
			client.listErr, client.visible = nil, true
			target.HeadCommit, target.ComparisonCurrent, client.head = "new-head", false, "new-head"
			review, err = service.Retry(ctx, "pr", input.ID)
			if err != nil || review.State != "published" || review.WriteOutcome != pullrequestreviews.WriteConfirmed || review.ExternalID != "42" || review.URL == "" || client.posts != 1 {
				t.Fatalf("late receipt recovery: %+v, %v, posts=%d", review, err, client.posts)
			}
		})
	}
}

func TestGitHubReviewSubmissionRetriesOnlyKnownUnsentWrites(t *testing.T) {
	for _, test := range []struct {
		name     string
		rejected bool
		cancel   bool
	}{
		{name: "head changed before POST"},
		{name: "rejected POST", rejected: true},
		{name: "rejected POST with cancelled request", rejected: true, cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			db, err := database.Open(filepath.Join(t.TempDir(), "reviews.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.ExecContext(ctx, "create table pull_requests(id text primary key); insert into pull_requests values('pr')"); err != nil {
				t.Fatal(err)
			}
			store, err := sqliteadapter.New(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			target := &reviewTarget{pullrequestreviews.Target{ID: "pr", Status: "open", HeadCommit: "head", ComparisonCurrent: true, Provider: "github", RepositoryURL: "https://github.com/o/r", Number: 7}}
			client := &reviewClient{head: "new-head"}
			outcome, posts := pullrequestreviews.WriteNotAttempted, 0
			if test.rejected {
				client.head = "head"
				client.mutationErr = &githubapi.RejectionError{Status: 403, Message: "Permission denied"}
				outcome, posts = pullrequestreviews.WriteRejected, 1
			}
			service := pullrequestreviews.New(store, target, githubreviews.New(client))
			input := pullrequestreviews.Submission{ID: "manual-review-1234", Event: pullrequestreviews.Comment, Body: "Please check", HeadCommit: "head"}
			submitCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			if test.cancel {
				client.afterPost = cancel
			}
			review, err := service.Submit(submitCtx, "pr", input)
			if err != nil || review.State != "failed" || review.WriteOutcome != outcome || client.posts != posts {
				t.Fatalf("known unsent submission: %+v, %v, posts=%d", review, err, client.posts)
			}
			client.head, client.mutationErr = "head", nil
			service = pullrequestreviews.New(store, target, githubreviews.New(client))
			review, err = service.Retry(ctx, "pr", input.ID)
			if err != nil || review.State != "published" || review.WriteOutcome != pullrequestreviews.WriteConfirmed || client.posts != posts+1 {
				t.Fatalf("known unsent retry: %+v, %v, posts=%d", review, err, client.posts)
			}
		})
	}
}
