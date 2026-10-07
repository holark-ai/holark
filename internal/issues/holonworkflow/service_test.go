package holonworkflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/issues/comments"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
	"github.com/holark-ai/holark/internal/repository"
)

type issueStub struct{ snapshot issueworkflow.Snapshot }

func (s issueStub) Snapshot(context.Context, string) (issueworkflow.Snapshot, error) {
	return s.snapshot, nil
}

type repoStub struct{}

func (repoStub) PrepareBranch(context.Context, string) (repository.Preparation, error) {
	return repository.Preparation{Branch: "main", Commit: "abc123"}, nil
}
func (repoStub) DefaultBranch() string { return "main" }

type templateReader string

func (reader templateReader) Read(context.Context, string) string { return string(reader) }

type holonStub struct{ input holons.Create }

func (s *holonStub) Create(_ context.Context, in holons.Create) (holons.Holon, error) {
	s.input = in
	return holons.Holon{ID: "holon"}, nil
}

type preflightHolonStub struct {
	holonStub
	harness string
	err     error
}

func (s *preflightHolonStub) PreflightSelection(_ context.Context, in *holons.Create) error {
	in.AgentType, in.SelectionResolved = s.harness, true
	return s.err
}

type countingIssueStub struct{ calls int }

func (s *countingIssueStub) Snapshot(context.Context, string) (issueworkflow.Snapshot, error) {
	s.calls++
	return issueworkflow.Snapshot{IssueID: "issue", Title: "Fix it"}, nil
}

type countingRepoStub struct{ calls int }

func (s *countingRepoStub) PrepareBranch(context.Context, string) (repository.Preparation, error) {
	s.calls++
	return repository.Preparation{Branch: "main", Commit: "abc123"}, nil
}
func (*countingRepoStub) DefaultBranch() string { return "main" }

func TestStartPreflightsHarnessBeforeIssueOrRepositorySideEffects(t *testing.T) {
	want := errors.New("saved default unavailable")
	issues := &countingIssueStub{}
	repository := &countingRepoStub{}
	holons := &preflightHolonStub{harness: "opencode", err: want}
	if _, err := New(issues, repository, holons).Start(t.Context(), "issue"); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if issues.calls != 0 || repository.calls != 0 || holons.input.AgentType != "" {
		t.Fatalf("side effects: issues=%d repository=%d create=%+v", issues.calls, repository.calls, holons.input)
	}
}

func TestStartUsesResolvedIssueHarness(t *testing.T) {
	holons := &preflightHolonStub{harness: "opencode"}
	if _, err := New(issueStub{issueworkflow.Snapshot{IssueID: "issue", Title: "Fix it"}}, repoStub{}, holons).Start(t.Context(), "issue"); err != nil {
		t.Fatal(err)
	}
	if holons.input.AgentType != "opencode" {
		t.Fatalf("agent type = %q", holons.input.AgentType)
	}
}

func TestStartCreatesDurableIssueHolonWithRenderedFallback(t *testing.T) {
	h := &holonStub{}
	s := New(issueStub{issueworkflow.Snapshot{IssueID: "issue", Title: "Fix it", Body: "  "}}, repoStub{}, h)
	result, e := s.Start(t.Context(), "issue")
	if e != nil || result.ID != "holon" {
		t.Fatalf("result=%+v %v", result, e)
	}
	if h.input.Kind != holons.KindIssue || h.input.IssueID != "issue" || h.input.AgentType != "codex" || h.input.BaseCommit != "abc123" || !strings.Contains(h.input.Prompt, "No description provided.") {
		t.Fatalf("create=%+v", h.input)
	}
}

func TestStartUsesSavedIssuePromptTemplate(t *testing.T) {
	holons := &holonStub{}
	service := New(issueStub{issueworkflow.Snapshot{IssueID: "issue", Title: "Fix it", Body: "Details"}}, repoStub{}, holons, templateReader("CUSTOM {{issue_title}} -- {{issue_body}}"))
	if _, err := service.Start(t.Context(), "issue"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(holons.input.Prompt, "CUSTOM Fix it -- Details\n\n") || !strings.Contains(holons.input.Prompt, "No cached comments.") {
		t.Fatalf("prompt=%q", holons.input.Prompt)
	}
}

type discussionIssueStub struct {
	events      *[]string
	syncErr     error
	snapshotErr error
}

func (s discussionIssueStub) SyncComments(context.Context, string) (comments.Discussion, error) {
	*s.events = append(*s.events, "sync")
	return comments.Discussion{}, s.syncErr
}
func (s discussionIssueStub) Snapshot(context.Context, string) (issueworkflow.Snapshot, error) {
	*s.events = append(*s.events, "snapshot")
	return issueworkflow.Snapshot{IssueID: "issue", Title: "Title", Discussion: comments.Context{Text: "Discussion body"}}, s.snapshotErr
}
func TestStartSyncsDiscussionAndPreservesCustomTemplates(t *testing.T) {
	for _, template := range []string{"Custom {{issue_title}}", "Custom {{ issue_comments }}"} {
		for _, stale := range []bool{false, true} {
			events := []string{}
			source := discussionIssueStub{events: &events}
			if stale {
				source.syncErr = errors.New("GitHub unavailable")
			}
			h := &holonStub{}
			if _, err := New(source, repoStub{}, h, templateReader(template)).Start(t.Context(), "issue"); err != nil {
				t.Fatal(err)
			}
			if strings.Join(events, ",") != "sync,snapshot" || strings.Count(h.input.Prompt, "Discussion body") != 1 || !strings.HasPrefix(h.input.Prompt, "Custom ") {
				t.Fatalf("events=%v prompt=%s", events, h.input.Prompt)
			}
			if stale != strings.Contains(h.input.Prompt, "may be stale") {
				t.Fatalf("freshness warning: %s", h.input.Prompt)
			}
		}
	}
	events := []string{}
	h := &holonStub{}
	source := discussionIssueStub{events: &events, syncErr: errors.New("remote failed"), snapshotErr: errors.New("local failed")}
	if _, err := New(source, repoStub{}, h).Start(t.Context(), "issue"); err == nil || h.input.Title != "" {
		t.Fatal("started without local snapshot")
	}
}
