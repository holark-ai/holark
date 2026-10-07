package pullrequestlifecycle

import (
	"encoding/json"
	"github.com/holark-ai/holark/internal/repository"
)

// A local WIP publishes into its repository. A fork may reuse both branch
// names and even the same commit, so those fields cannot identify its source.
func localPublicationSourceMatches(data json.RawMessage, headURL, repositoryURL string) bool {
	observation := DecodeGitHubObservation(data)
	if headURL == "" {
		headURL = observation.HeadRepositoryURL
	}
	if headURL == "" {
		return false
	}
	if repositoryURL == "" {
		repositoryURL = observation.BaseRepositoryURL
	}
	if repositoryURL == "" {
		return false
	}
	return repositoryIdentity(headURL) == repositoryIdentity(repositoryURL)
}
func repositoryIdentity(raw string) string { return repository.RepositoryIdentity(raw) }
