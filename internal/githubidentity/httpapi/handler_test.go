package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/holark-ai/holark/internal/githubidentity"
	githubhttp "github.com/holark-ai/holark/internal/githubidentity/httpapi"
)

type testRateLimitError struct{ error }

func (testRateLimitError) RateLimited() bool { return true }

type identityService struct{ syncErr error }

func (identityService) ListProjectMembers(context.Context, string) ([]githubidentity.Member, error) {
	return nil, nil
}
func (identityService) SearchProjectMembers(context.Context, string, string, int) ([]githubidentity.Member, error) {
	return nil, nil
}
func (identityService) GetProjectMembers(context.Context, string, []string) (githubidentity.MemberResolution, error) {
	return githubidentity.MemberResolution{}, nil
}
func (service identityService) SyncProjectMembers(context.Context, string, string) ([]githubidentity.Member, error) {
	return nil, service.syncErr
}

func TestMemberSyncReportsGitHubRateLimit(t *testing.T) {
	mux := http.NewServeMux()
	githubhttp.RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, githubhttp.Options{
		Service: identityService{syncErr: testRateLimitError{errors.New("limited")}},
		Project: &githubhttp.Project{ID: "project", RepositoryURL: "https://github.com/owner/repo"},
	})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/github-members/sync", nil))
	const want = "{\"code\":\"github_rate_limit_exceeded\",\"message\":\"GitHub API rate limit exceeded. Try again after the limit resets.\"}\n"
	if response.Code != http.StatusTooManyRequests || response.Body.String() != want {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}
