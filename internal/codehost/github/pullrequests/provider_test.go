package pullrequests

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
)

func TestSupportsRepositoryUsesGitHubReferenceParsing(t *testing.T) {
	provider := New(nil)
	for _, test := range []struct {
		name       string
		repository string
		want       bool
	}{
		{name: "scp GitHub URL", repository: "git@github.com:owner/repo.git", want: true},
		{name: "HTTPS GitHub URL", repository: "https://github.com/owner/repo.git", want: true},
		{name: "SSH GitHub URL", repository: "ssh://git@github.com/owner/repo.git", want: true},
		{name: "different host", repository: "git@example.com:owner/repo.git"},
		{name: "malformed GitHub path", repository: "https://github.com/owner"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := provider.SupportsRepository(test.repository); got != test.want {
				t.Fatalf("SupportsRepository(%q) = %t, want %t", test.repository, got, test.want)
			}
		})
	}
}

func TestConvertMapsGitHubLifecycleAndRejectsMalformedResponses(t *testing.T) {
	updatedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	mergedAt := updatedAt.Add(-time.Minute)
	converted, err := convert(githubapi.Repository{Owner: "owner", Name: "repo"}, githubapi.PullRequest{
		Number: 12, Title: "  Merged change  ", State: "closed", MergedAt: &mergedAt, MergeCommitSHA: "merged",
		UpdatedAt: updatedAt, Base: githubapi.Ref{Ref: "main", SHA: "base"}, Head: githubapi.Ref{Ref: "feature", SHA: "head"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if converted.Title != "Merged change" || converted.Status != pullrequestlifecycle.StatusMerged || converted.ExternalID != "github:owner/repo#12" {
		t.Fatalf("converted pull request = %+v", converted)
	}
	if converted.MergedAt == nil || !converted.MergedAt.Equal(mergedAt) || converted.MergedCommit != "merged" || converted.MergeStrategy != "squash" {
		t.Fatalf("merged lifecycle = %+v", converted)
	}
	var syncData syncDataEnvelope
	if err := json.Unmarshal(converted.SyncData, &syncData); err != nil {
		t.Fatal(err)
	}
	if syncData.GitHub.Owner != "owner" || syncData.GitHub.Repo != "repo" || syncData.GitHub.Number != 12 || !syncData.GitHub.Merged || syncData.GitHub.MergeCommitSHA != "merged" {
		t.Fatalf("sync data = %+v", syncData)
	}

	_, err = convert(githubapi.Repository{Owner: "owner", Name: "repo"}, githubapi.PullRequest{Number: 12})
	var providerErr *pullrequestlifecycle.GitHubError
	if !errors.As(err, &providerErr) || providerErr.Kind != pullrequestlifecycle.GitHubSyncFailure {
		t.Fatalf("malformed response error = %v", err)
	}
}

func TestNumberUsesCanonicalIdentityAndLegacyFallback(t *testing.T) {
	provider := New(nil)
	tests := []struct {
		name       string
		syncData   json.RawMessage
		externalID string
		want       int
	}{
		{name: "canonical sync data wins", syncData: json.RawMessage(`{"github":{"number":12}}`), externalID: "github:owner/repo#44", want: 12},
		{name: "missing sync identity falls back", syncData: json.RawMessage(`{"github":{}}`), externalID: "github:owner/repo#44", want: 44},
		{name: "malformed sync data falls back", syncData: json.RawMessage(`{"github":`), externalID: "github:owner/repo#44", want: 44},
		{name: "malformed legacy identity is rejected", syncData: json.RawMessage(`{}`), externalID: "github:owner/repo#not-a-number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := provider.Number(pullrequestlifecycle.GitHubPullRequestTarget{SyncData: tt.syncData, ExternalID: tt.externalID})
			if got != tt.want {
				t.Fatalf("number = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLifecycleSyncDataCodecs(t *testing.T) {
	provider := New(nil)
	raw := json.RawMessage(`{"github":{"number":12,"url":"https://github.com/owner/repo/pull/12","node_id":"PR_node","draft":true,"state":"closed","readiness":{"head_commit":"old","checks_state":"pending","mergeability_state":"unknown","synced_at":"2026-01-02T03:04:05Z"}}}`)
	draft, state := false, "open"
	updated, err := provider.UpdateLifecycleFields(raw, pullrequestlifecycle.GitHubLifecycleFields{
		Draft: &draft, State: &state,
	})
	if err != nil {
		t.Fatal(err)
	}
	var data syncDataEnvelope
	if err := json.Unmarshal(updated, &data); err != nil {
		t.Fatal(err)
	}
	if data.GitHub.Number != 12 || data.GitHub.NodeID != "PR_node" || data.GitHub.Draft || data.GitHub.State != "open" || data.GitHub.Readiness == nil {
		t.Fatalf("updated lifecycle data = %+v", data.GitHub)
	}
	if _, err := provider.UpdateLifecycleFields(json.RawMessage(`{"github":`), pullrequestlifecycle.GitHubLifecycleFields{Draft: &draft}); err == nil {
		t.Fatal("expected malformed sync data to be rejected")
	}
}

func TestStoreReadinessPreservesProviderAndForeignSyncData(t *testing.T) {
	provider := New(nil)
	raw := json.RawMessage(`{"other":{"kept":true},"github":{"number":12,"custom":"kept"}}`)
	readiness := pullrequestlifecycle.GitHubReadiness{
		HeadCommit: "head", ChecksState: pullrequestlifecycle.GitHubChecksPassing,
		MergeabilityState: pullrequestlifecycle.GitHubMergeabilityMergeable,
		SyncedAt:          time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	updated, err := provider.StoreReadiness(raw, readiness)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(updated, &stored); err != nil {
		t.Fatal(err)
	}
	github := stored["github"].(map[string]any)
	if github["custom"] != "kept" || stored["other"].(map[string]any)["kept"] != true {
		t.Fatalf("unrelated sync data was not preserved: %s", updated)
	}
	decoded, ok := provider.DecodeReadiness(updated)
	if !ok || decoded.HeadCommit != readiness.HeadCommit || decoded.ChecksState != readiness.ChecksState || decoded.MergeabilityState != readiness.MergeabilityState || !decoded.SyncedAt.Equal(readiness.SyncedAt) {
		t.Fatalf("decoded readiness = %+v, ok = %v", decoded, ok)
	}
}

func TestMergeErrorClassifiesProviderFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want pullrequestmerge.GitHubErrorKind
	}{
		{name: "missing executable is unavailable", err: &githubapi.Error{Code: githubapi.ErrorCodeGHUnavailable, Err: errors.New("gh executable was not found")}, want: pullrequestmerge.GitHubUnavailable},
		{name: "authentication is unavailable", err: &githubapi.Error{Code: githubapi.ErrorCodeGHUnavailable, Err: errors.New("authentication failed")}, want: pullrequestmerge.GitHubUnavailable},
		{name: "sync authentication is unavailable", err: &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("authentication failed")}, want: pullrequestmerge.GitHubUnavailable},
		{name: "network is unavailable", err: &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("network connection failed")}, want: pullrequestmerge.GitHubUnavailable},
		{name: "stale head is head changed", err: &githubapi.Error{Code: githubapi.ErrorCodeGHUnavailable, Err: errors.New("head SHA does not match")}, want: pullrequestmerge.GitHubHeadChanged},
		{name: "sync stale head is head changed", err: &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("head SHA does not match")}, want: pullrequestmerge.GitHubHeadChanged},
		{name: "other provider failure is blocked", err: &githubapi.Error{Code: githubapi.ErrorCodeGHUnavailable, Err: errors.New("merge failed")}, want: pullrequestmerge.GitHubBlocked},
		{name: "sync rejection is blocked", err: &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("branch policy rejected merge")}, want: pullrequestmerge.GitHubBlocked},
		{name: "ordinary merge rejection is blocked", err: errors.New("checks blocked the merge"), want: pullrequestmerge.GitHubBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mergeError(tt.err)
			var providerErr *pullrequestmerge.GitHubError
			if !errors.As(err, &providerErr) || providerErr.Kind != tt.want {
				t.Fatalf("merge error = %#v, want kind %q", err, tt.want)
			}
			if providerErr.Message != tt.err.Error() {
				t.Fatalf("message = %q, want %q", providerErr.Message, tt.err.Error())
			}
		})
	}
}

func TestLifecyclePatchPreservesForkIdentityAndUnrelatedProviderData(t *testing.T) {
	provider := New(nil)
	draft := false
	patched, err := provider.UpdateLifecycleFields(json.RawMessage(`{"github":{"head_repository_url":"https://github.com/contributor/fork.git","base_repository_url":"https://github.com/owner/repo.git","draft":true,"future":{"retained":true}},"description":{"fingerprint":"pinned"}}`), pullrequestlifecycle.GitHubLifecycleFields{Draft: &draft})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(patched, &envelope); err != nil {
		t.Fatal(err)
	}
	var github map[string]json.RawMessage
	if err := json.Unmarshal(envelope["github"], &github); err != nil {
		t.Fatal(err)
	}
	if string(github["head_repository_url"]) != `"https://github.com/contributor/fork.git"` || string(github["future"]) != `{"retained":true}` || string(envelope["description"]) != `{"fingerprint":"pinned"}` || string(github["draft"]) != "false" {
		t.Fatalf("patch lost unrelated state: %s", patched)
	}
}

func TestConvertPreservesOwningRepositoriesForForkBranches(t *testing.T) {
	converted, err := convert(githubapi.Repository{Owner: "owner", Name: "repo"}, githubapi.PullRequest{Number: 9, Base: githubapi.Ref{Ref: "main", SHA: "base", Repository: &githubapi.RefRepository{CloneURL: "https://github.com/owner/repo.git"}}, Head: githubapi.Ref{Ref: "main", SHA: "fork-head", Repository: &githubapi.RefRepository{CloneURL: "https://github.com/contributor/fork.git"}}})
	if err != nil {
		t.Fatal(err)
	}
	if converted.HeadCommit != "fork-head" || converted.HeadRepositoryURL != "https://github.com/contributor/fork.git" || converted.BaseRepositoryURL != "https://github.com/owner/repo.git" {
		t.Fatalf("converted fork = %+v", converted)
	}
	var data syncDataEnvelope
	if err := json.Unmarshal(converted.SyncData, &data); err != nil {
		t.Fatal(err)
	}
	if data.GitHub.HeadRepositoryURL != converted.HeadRepositoryURL || data.GitHub.BaseRepositoryURL != converted.BaseRepositoryURL {
		t.Fatalf("persisted repositories = %+v", data.GitHub)
	}
}

func TestMergeErrorsPreserveUncertainRemoteOutcomes(t *testing.T) {
	for _, test := range []struct {
		name      string
		cause     error
		uncertain bool
	}{
		{name: "lost success", cause: &githubapi.Error{Code: githubapi.ErrorCodeMutationAccepted, Err: errors.New("decode merge response")}, uncertain: true},
		{name: "server failure", cause: &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("HTTP 502 Bad Gateway")}, uncertain: true},
		{name: "head rejected", cause: &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("HTTP 409 Head changed")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := mergeError(test.cause)
			var outcome interface{ Uncertain() bool }
			if !errors.As(err, &outcome) || outcome.Uncertain() != test.uncertain {
				t.Fatalf("uncertainty for %v = %v, want %t", err, outcome, test.uncertain)
			}
		})
	}
}

func TestProviderRetryAfterPreservesServerDelay(t *testing.T) {
	err := lifecycleError(&githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("HTTP 429\nRetry-After: 90\n")})
	var retry interface{ RetryAfter() time.Duration }
	if !errors.As(err, &retry) || retry.RetryAfter() != 90*time.Second {
		t.Fatalf("provider retry delay = %v", err)
	}
}

func TestProviderRetryAfterPrefersStructuredDelay(t *testing.T) {
	cause := &githubapi.RetryError{
		RetryAt: time.Now().Add(2 * time.Minute),
		Err:     &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("HTTP 429\nRetry-After: 90\n")},
	}
	err := lifecycleError(cause)
	var retry interface{ RetryAfter() time.Duration }
	if !errors.As(err, &retry) || retry.RetryAfter() < 110*time.Second {
		t.Fatalf("provider retry delay = %v", err)
	}
	var limited interface{ RateLimited() bool }
	if !errors.As(err, &limited) || !limited.RateLimited() {
		t.Fatalf("provider rate-limit classification = %v", err)
	}
}

func TestMissingHeadRepositoryDoesNotAssumeBaseRepository(t *testing.T) {
	value, err := convert(githubapi.Repository{Owner: "owner", Name: "repo"}, githubapi.PullRequest{Number: 1, Base: githubapi.Ref{Ref: "main", SHA: "base"}, Head: githubapi.Ref{Ref: "topic", SHA: "head"}})
	if err != nil {
		t.Fatal(err)
	}
	if value.HeadRepositoryURL != "" || value.BaseRepositoryURL != "https://github.com/owner/repo.git" {
		t.Fatalf("missing fork identity: %+v", value)
	}
}
