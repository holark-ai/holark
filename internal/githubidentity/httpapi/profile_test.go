package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubmembers "github.com/holark-ai/holark/internal/codehost/github/members"
	"github.com/holark-ai/holark/internal/githubidentity"
	githubhttp "github.com/holark-ai/holark/internal/githubidentity/httpapi"
)

type profileClient struct {
	user githubapi.AuthenticatedUser
	err  error
}

func (client profileClient) CurrentUser(context.Context) (githubapi.AuthenticatedUser, error) {
	return client.user, client.err
}

func (profileClient) ListCollaborators(context.Context, githubapi.Repository) ([]githubapi.Collaborator, error) {
	return nil, errors.New("collaborator access unavailable")
}

func TestAuthenticatedProfile(t *testing.T) {
	for _, test := range []struct {
		name   string
		client profileClient
		status int
		want   githubidentity.Profile
	}{
		{
			name: "name and avatar without collaborator access",
			client: profileClient{user: githubapi.AuthenticatedUser{
				NodeID: "U_me", Login: " octocat ", Name: " Mona Lisa ",
				AvatarURL: "https://avatars.githubusercontent.com/u/1", HTMLURL: "https://github.com/octocat",
			}},
			status: http.StatusOK,
			want:   githubidentity.Profile{Login: "octocat", Name: "Mona Lisa", AvatarURL: "https://avatars.githubusercontent.com/u/1", ProfileURL: "https://github.com/octocat"},
		},
		{
			name:   "account without a display name",
			client: profileClient{user: githubapi.AuthenticatedUser{NodeID: "U_me", Login: "octocat"}},
			status: http.StatusOK,
			want:   githubidentity.Profile{Login: "octocat"},
		},
		{
			name:   "GitHub unavailable",
			client: profileClient{err: &githubapi.Error{Code: githubapi.ErrorCodeGHUnavailable, Err: errors.New("unavailable")}},
			status: http.StatusServiceUnavailable,
		},
		{
			name:   "malformed identity",
			client: profileClient{user: githubapi.AuthenticatedUser{Name: "Mona Lisa"}},
			status: http.StatusBadGateway,
		},
		{
			name:   "rate limited",
			client: profileClient{err: testRateLimitError{errors.New("limited")}},
			status: http.StatusTooManyRequests,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := githubidentity.NewService(nil, githubmembers.New(test.client))
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/v1/github-profile", githubhttp.NewProfileHandler(service))
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/github-profile", nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if test.status != http.StatusOK {
				return
			}
			var profile githubidentity.Profile
			if err := json.Unmarshal(response.Body.Bytes(), &profile); err != nil {
				t.Fatal(err)
			}
			if profile != test.want {
				t.Fatalf("profile = %+v, want %+v", profile, test.want)
			}
		})
	}
}
