package pullrequestfixture

import (
	"errors"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

func TestQueuedFixtureOutcomesAreOneShot(t *testing.T) {
	agents := newMetadataAgents(0)
	agents.queue(
		metadataAgentOutcome{failure: "fails once"},
		metadataAgentOutcome{title: "Queued title", description: "Queued description"},
	)
	if outcome := agents.nextOutcome(); outcome.failure != "fails once" {
		t.Fatalf("first metadata outcome = %+v", outcome)
	}
	if outcome := agents.nextOutcome(); outcome.title != "Queued title" || outcome.description != "Queued description" || outcome.failure != "" {
		t.Fatalf("second metadata outcome = %+v", outcome)
	}
	if outcome := agents.nextOutcome(); outcome.title != GeneratedTitle || outcome.description != GeneratedDescription || outcome.failure != "" {
		t.Fatalf("default metadata outcome = %+v", outcome)
	}

	github := &GitHub{}
	wantErr := errors.New("fails once")
	github.QueueCreateError(wantErr)
	request := pullrequestlifecycle.GitHubCreateRequest{Title: "Title", Body: "Description", Base: "main", Head: "feature"}
	if _, err := github.Create(t.Context(), request); !errors.Is(err, wantErr) {
		t.Fatalf("first GitHub creation error = %v, want %v", err, wantErr)
	}
	if _, err := github.Create(t.Context(), request); err != nil {
		t.Fatalf("second GitHub creation error = %v", err)
	}
	if requests := github.CreateRequests(); len(requests) != 2 {
		t.Fatalf("GitHub creation requests = %d, want 2", len(requests))
	}
}
