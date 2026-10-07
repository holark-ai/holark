package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubissues "github.com/holark-ai/holark/internal/codehost/github/issues"
	"github.com/holark-ai/holark/internal/githubidentity"
	"github.com/holark-ai/holark/internal/issues"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
)

type openIssueSource struct {
	all, open  []githubapi.Issue
	individual map[int]githubapi.Issue
	failures   map[int]error
	listingErr error
	reads      []int
}

func (s *openIssueSource) ListIssues(context.Context, githubapi.Repository) ([]githubapi.Issue, error) {
	return s.all, s.listingErr
}
func (s *openIssueSource) ListOpenIssues(context.Context, githubapi.Repository) ([]githubapi.Issue, error) {
	return s.open, s.listingErr
}
func (s *openIssueSource) GetIssue(_ context.Context, _ githubapi.Repository, number int) (githubapi.Issue, error) {
	s.reads = append(s.reads, number)
	return s.individual[number], s.failures[number]
}
func remoteIssue(number int, state string) githubapi.Issue {
	return githubapi.Issue{Number: number, NodeID: fmt.Sprint("I_", number), Title: fmt.Sprint("Issue ", number), State: state, User: githubapi.User{NodeID: "U_me", Login: "Alice"}, Assignees: []githubapi.User{}}
}
func TestOpenIssueSyncReconcilesClosuresWithoutDeletingHistory(t *testing.T) {
	f := setup(t)
	source := &openIssueSource{all: []githubapi.Issue{remoteIssue(1, "open"), remoteIssue(2, "open"), remoteIssue(3, "closed"), remoteIssue(4, "closed"), remoteIssue(5, "open")}, individual: map[int]githubapi.Issue{}, failures: map[int]error{}}
	service := issueworkflow.New(func(string) (issueworkflow.Project, bool) {
		return issueworkflow.Project{ID: "repo", RepositoryURL: "https://github.com/owner/repo"}, true
	}, githubissues.New(source), f.issues, githubidentity.NewService(f.members, nil))
	service.WithSyncRecorder(f.work)
	if _, err := service.Sync(t.Context(), "repo"); err != nil {
		t.Fatal(err)
	}
	local, err := f.issues.Create(t.Context(), "repo", "Local issue", "")
	if err != nil {
		t.Fatal(err)
	}
	// A reopened historical issue is rediscovered; a missing open issue closes,
	// while failed and mismatched lookups retain their cached records.
	source.open = []githubapi.Issue{remoteIssue(3, "open"), remoteIssue(6, "open")}
	source.individual[1] = remoteIssue(1, "closed")
	source.individual[5] = remoteIssue(99, "closed")
	source.failures[2] = errors.New("not found or inaccessible")
	result, err := service.SyncOpen(t.Context(), "repo")
	if err == nil || !strings.Contains(err.Error(), "not found or inaccessible") || !strings.Contains(err.Error(), "different issue") || result.Imported != 1 || result.Updated != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	check := func(want map[int]issues.IssueStatus) {
		t.Helper()
		list, e := f.issues.List(t.Context(), "repo")
		if e != nil {
			t.Fatal(e)
		}
		got := map[int]issues.IssueStatus{}
		localFound := false
		for _, i := range list {
			if i.ID == local.ID {
				localFound = true
				continue
			}
			var n int
			fmt.Sscanf(i.SyncExternalID, "github:owner/repo#%d", &n)
			got[n] = i.Status
		}
		if !localFound || !reflect.DeepEqual(got, want) {
			t.Fatalf("issues=%v local=%t want=%v", got, localFound, want)
		}
	}
	check(map[int]issues.IssueStatus{1: issues.IssueClosed, 2: issues.IssueOpen, 3: issues.IssueOpen, 4: issues.IssueClosed, 5: issues.IssueOpen, 6: issues.IssueOpen})
	for _, n := range source.reads {
		if n == 3 || n == 4 {
			t.Fatalf("cached closed issue fetched individually: %d", n)
		}
	}
	foundError := false
	for _, s := range f.get(t, "/api/v1/my-work").Sync {
		if s.Section == "issues" && s.Error != "" {
			foundError = true
		}
	}
	if !foundError {
		t.Fatal("lookup failure not observable")
	}
	// Failed listing must never trigger disappearance reads.
	source.reads = nil
	source.listingErr = errors.New("offline")
	if _, err := service.SyncOpen(t.Context(), "repo"); err == nil {
		t.Fatal("listing failure lost")
	}
	if len(source.reads) != 0 {
		t.Fatal(source.reads)
	}
	source.listingErr = nil
	// Empty open snapshots check all formerly open records and preserve history.
	source.open = nil
	source.failures = map[int]error{}
	for _, n := range []int{2, 3, 5, 6} {
		source.individual[n] = remoteIssue(n, "closed")
	}
	if _, err := service.SyncOpen(t.Context(), "repo"); err != nil {
		t.Fatal(err)
	}
	check(map[int]issues.IssueStatus{1: issues.IssueClosed, 2: issues.IssueClosed, 3: issues.IssueClosed, 4: issues.IssueClosed, 5: issues.IssueClosed, 6: issues.IssueClosed})
	// Full reconciliation alone removes provider issues no longer visible.
	source.all = []githubapi.Issue{remoteIssue(4, "closed")}
	if _, err := service.Sync(t.Context(), "repo"); err != nil {
		t.Fatal(err)
	}
	check(map[int]issues.IssueStatus{4: issues.IssueClosed})
}

func TestOpenIssueLookupRejectsChangedNodeIdentity(t *testing.T) {
	f := setup(t)
	source := &openIssueSource{all: []githubapi.Issue{remoteIssue(1, "open")}, individual: map[int]githubapi.Issue{1: remoteIssue(1, "closed")}}
	service := issueworkflow.New(func(string) (issueworkflow.Project, bool) {
		return issueworkflow.Project{ID: "repo", RepositoryURL: "https://github.com/owner/repo"}, true
	}, githubissues.New(source), f.issues, githubidentity.NewService(f.members, nil))
	if _, err := service.Sync(t.Context(), "repo"); err != nil {
		t.Fatal(err)
	}
	changed := source.individual[1]
	changed.NodeID = "different-node"
	source.individual[1] = changed
	if _, err := service.SyncOpen(t.Context(), "repo"); err == nil {
		t.Fatal("changed identity accepted")
	}
	list, err := f.issues.List(t.Context(), "repo")
	if err != nil || len(list) != 1 || list[0].Status != issues.IssueOpen {
		t.Fatalf("cached issue changed: %+v %v", list, err)
	}
}
