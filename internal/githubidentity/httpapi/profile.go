package httpapi

import (
	"context"
	"errors"
	"net/http"

	githubratelimit "github.com/holark-ai/holark/internal/codehost/github/ratelimit"
	"github.com/holark-ai/holark/internal/githubidentity"
)

type ProfileService interface {
	AuthenticatedProfile(context.Context) (githubidentity.Profile, error)
}

func NewProfileHandler(service ProfileService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		profile, err := service.AuthenticatedProfile(r.Context())
		if err != nil {
			switch {
			case githubratelimit.Exceeded(err):
				writeError(w, http.StatusTooManyRequests, "github_rate_limit_exceeded", "GitHub API rate limit exceeded. Try again after the limit resets.")
			case errors.Is(err, githubidentity.ErrGitHubUnavailable):
				writeError(w, http.StatusServiceUnavailable, "gh_unavailable", "GitHub profile is unavailable.")
			case errors.Is(err, githubidentity.ErrGitHubFailed), errors.Is(err, githubidentity.ErrMalformedSnapshot):
				writeError(w, http.StatusBadGateway, "github_profile_failed", "GitHub profile could not be synchronized.")
			default:
				writeError(w, http.StatusInternalServerError, "github_profile_failed", "GitHub profile is unavailable.")
			}
			return
		}
		writeJSON(w, http.StatusOK, profile)
	}
}
