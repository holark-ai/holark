package pullrequestmetadata

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
)

type Client interface {
	UpdatePullRequest(context.Context, githubapi.Repository, int, githubapi.UpdatePullRequestRequest) (githubapi.PullRequest, error)
}

type Provider struct {
	client Client
}

func New(client Client) *Provider {
	return &Provider{client: client}
}

func (provider *Provider) Update(ctx context.Context, snapshot pullrequestmetadata.Snapshot, title, description string) (time.Time, error) {
	if provider == nil || provider.client == nil {
		return time.Time{}, pullrequestmetadata.ErrProviderUnavailable
	}
	repository, err := githubapi.ParseRepositoryURL(snapshot.RepositoryURL)
	if err != nil {
		return time.Time{}, errors.Join(pullrequestmetadata.ErrUnsupportedProvider, err)
	}
	number, err := externalNumber(snapshot.SyncExternalID, repository)
	if err != nil {
		return time.Time{}, errors.Join(pullrequestmetadata.ErrUnsupportedProvider, err)
	}
	remote, err := provider.client.UpdatePullRequest(ctx, repository, number, githubapi.UpdatePullRequestRequest{Title: title, Body: description})
	if err == nil {
		if remote.Number != number || remote.Title != title || remote.Body != description || remote.UpdatedAt.IsZero() {
			return time.Time{}, errors.Join(pullrequestmetadata.ErrProviderFailed, &githubapi.Error{Code: githubapi.ErrorCodeMutationUncertain, Err: errors.New("GitHub metadata mutation returned no matching confirmed revision")})
		}
		return remote.UpdatedAt, nil
	}
	var githubErr *githubapi.Error
	if !errors.As(err, &githubErr) {
		var outcome interface{ Uncertain() bool }
		if !errors.As(err, &outcome) {
			err = &githubapi.Error{Code: githubapi.ErrorCodeMutationUncertain, Err: err}
		}
	} else if githubErr.Code == githubapi.ErrorCodeGHUnavailable {
		return time.Time{}, errors.Join(pullrequestmetadata.ErrProviderUnavailable, err)
	}
	return time.Time{}, errors.Join(pullrequestmetadata.ErrProviderFailed, err)
}

func externalNumber(externalID string, repository githubapi.Repository) (int, error) {
	prefix := "github:" + repository.Owner + "/" + repository.Name + "#"
	if !strings.HasPrefix(externalID, prefix) {
		return 0, fmt.Errorf("invalid GitHub pull request external ID %q", externalID)
	}
	number, err := strconv.Atoi(strings.TrimPrefix(externalID, prefix))
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("invalid GitHub pull request external ID %q", externalID)
	}
	return number, nil
}

var _ pullrequestmetadata.Provider = (*Provider)(nil)
