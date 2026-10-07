package repositoryhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/repository"
)

type fakeService struct{ refreshErr error }

func (*fakeService) Descriptor() repository.Descriptor {
	return repository.Descriptor{ID: "stable", DefaultBranch: "main"}
}
func (*fakeService) Refs(context.Context) ([]repository.Ref, error) {
	return []repository.Ref{{Name: "refs/heads/main", ShortName: "main", Kind: "branch", Target: "abc"}}, nil
}
func (f *fakeService) Refresh(context.Context) ([]repository.Ref, error) { return nil, f.refreshErr }
func (*fakeService) Tree(context.Context, string, string) (repository.Tree, error) {
	return repository.Tree{Ref: "abc", Entries: []repository.TreeEntry{}}, nil
}
func (*fakeService) Blob(context.Context, string, string) (repository.Blob, error) {
	return repository.Blob{}, nil
}
func (*fakeService) Commits(context.Context, string, int) ([]repository.Commit, error) {
	return nil, nil
}
func (*fakeService) CommitPage(context.Context, string, int, string) (repository.CommitPage, error) {
	return repository.CommitPage{}, nil
}
func (*fakeService) Changes(context.Context, string, string) ([]repository.Change, error) {
	return nil, nil
}
func (*fakeService) Dirty(context.Context) (repository.DirtyState, error) {
	return repository.DirtyState{Dirty: true, Fingerprint: "state-one"}, nil
}
func (*fakeService) Resolve(context.Context, string) (string, error) { return "abcdef", nil }
func (*fakeService) PrepareBranch(_ context.Context, branch string) (repository.Preparation, error) {
	return repository.Preparation{Branch: repository.CanonicalProviderBranch(branch), Commit: "abcdef"}, nil
}

func TestRefreshFailureDegradesToCachedRefs(t *testing.T) {
	handler := New(&fakeService{refreshErr: errors.New("offline")})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/repository/refs/refresh", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, want := range []string{`"short_name":"main"`, `"refresh_error":"offline"`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("body missing %s: %s", want, response.Body.String())
		}
	}
}

func TestPreparePinsSelectedRefToCommit(t *testing.T) {
	handler := New(&fakeService{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/repository/prepare", strings.NewReader(`{"ref":"origin/feature"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"commit":"abcdef"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"ref":"feature"`) {
		t.Fatalf("body=%s", response.Body.String())
	}
}
