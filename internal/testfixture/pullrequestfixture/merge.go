package pullrequestfixture

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	githubpullrequests "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
)

func (github *GitHub) Readiness(target pullrequestmerge.GitHubTarget) (pullrequestmerge.GitHubReadiness, bool) {
	return githubpullrequests.New(nil).Readiness(target)
}

// Squash replaces the external GitHub merge with a commit in the scenario's
// local remote. The application still validates readiness and records the merge.
func (github *GitHub) Squash(ctx context.Context, request pullrequestmerge.GitHubMergeRequest) (pullrequestmerge.GitHubMergeResult, error) {
	github.mu.Lock()
	defer github.mu.Unlock()
	if github.pullRequest == nil || github.remotePath == "" || github.pullRequest.Status != pullrequestlifecycle.StatusOpen {
		return pullrequestmerge.GitHubMergeResult{}, &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubBlocked, Message: "Scenario pull request is not open for merging."}
	}
	if err := github.refreshCommits(); err != nil {
		return pullrequestmerge.GitHubMergeResult{}, err
	}
	if request.ExpectedHeadSHA != github.headCommit {
		return pullrequestmerge.GitHubMergeResult{}, &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubHeadChanged}
	}
	if _, err := gitOutputContext(ctx, github.remotePath, "merge-base", "--is-ancestor", github.baseCommit, github.headCommit); err != nil {
		return pullrequestmerge.GitHubMergeResult{}, &pullrequestmerge.GitHubError{Kind: pullrequestmerge.GitHubBlocked, Message: "Rebase before merging."}
	}
	tree, err := gitOutputContext(ctx, github.remotePath, "rev-parse", github.headCommit+"^{tree}")
	if err != nil {
		return pullrequestmerge.GitHubMergeResult{}, err
	}
	commit, err := gitOutputContext(ctx, github.remotePath, "-c", "user.name=Holark Scenario", "-c", "user.email=scenario@invalid", "commit-tree", strings.TrimSpace(tree), "-p", github.baseCommit, "-m", request.Title, "-m", request.CommitMessage)
	if err != nil {
		return pullrequestmerge.GitHubMergeResult{}, err
	}
	commit = strings.TrimSpace(commit)
	if _, err := gitOutputContext(ctx, github.remotePath, "update-ref", "refs/heads/"+github.pullRequest.BaseBranch, commit, github.baseCommit); err != nil {
		return pullrequestmerge.GitHubMergeResult{}, err
	}
	now := time.Now().UTC()
	github.pullRequest.Status = pullrequestlifecycle.StatusMerged
	github.pullRequest.MergedAt, github.pullRequest.ClosedAt = &now, &now
	github.pullRequest.MergedCommit, github.pullRequest.MergeStrategy = commit, "squash"
	github.pullRequest.UpdatedAt = now
	var data map[string]map[string]any
	if json.Unmarshal(github.pullRequest.SyncData, &data) == nil && data["github"] != nil {
		data["github"]["state"], data["github"]["merged"] = "closed", true
		data["github"]["merged_at"], data["github"]["merge_commit_sha"] = now, commit
		github.pullRequest.SyncData, _ = json.Marshal(data)
	}
	return pullrequestmerge.GitHubMergeResult{MergedCommit: commit}, nil
}
