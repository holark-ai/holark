package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Use live ref targets: the PR's baseRefOid/headRefOid can outlive a branch.
const workStateFields = `id number title body url state isDraft createdAt updatedAt closedAt mergedAt mergeCommit{oid} baseRefName headRefName baseRepository{nameWithOwner url} headRepository{nameWithOwner url} baseRef{target{__typename oid}} headRef{target{__typename oid}}`

func (client *CLIClient) PullRequestWorkState(ctx context.Context, repository Repository, number int) (PullRequest, error) {
	invalid := func() (PullRequest, error) {
		return PullRequest{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub returned incomplete or mismatched work state")}
	}
	if repository.Owner == "" || repository.Name == "" || number <= 0 {
		return invalid()
	}
	query := `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){nameWithOwner pullRequest(number:$number){` + workStateFields + `}}}`
	var response struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Repository *struct {
				Name        string          `json:"nameWithOwner"`
				PullRequest json.RawMessage `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	// Deliberately one invocation, without lifecycle follow-up reads or retries.
	if err := client.GraphQL(ctx, query, map[string]any{"owner": repository.Owner, "name": repository.Name, "number": number}, &response); err != nil {
		return PullRequest{}, err
	}
	if len(response.Errors) != 0 || response.Data.Repository == nil || !strings.EqualFold(response.Data.Repository.Name, repository.Owner+"/"+repository.Name) {
		return invalid()
	}
	raw := response.Data.Repository.PullRequest
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return invalid()
	}
	for _, name := range []string{"closedAt", "mergedAt", "mergeCommit"} {
		if _, present := fields[name]; !present {
			return invalid()
		}
	}
	// These may legitimately be empty/false, but cannot be absent or null.
	for _, name := range []string{"title", "body", "isDraft"} {
		if value := fields[name]; len(value) == 0 || string(value) == "null" {
			return invalid()
		}
	}
	type liveRef struct {
		Target *struct {
			Type string `json:"__typename"`
			OID  string `json:"oid"`
		} `json:"target"`
	}
	var node struct {
		pullRequestObservation
		BaseRef *liveRef `json:"baseRef"`
		HeadRef *liveRef `json:"headRef"`
	}
	if json.Unmarshal(raw, &node) != nil {
		return invalid()
	}
	p := node.pullRequestObservation
	if p.ID == "" || p.Number != number || strings.TrimSpace(p.Title) == "" || p.URL == "" || p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() || p.BaseRefName == "" || p.HeadRefName == "" {
		return invalid()
	}
	if p.State != "OPEN" && p.State != "CLOSED" && p.State != "MERGED" {
		return invalid()
	}
	if p.MergeCommit != nil && p.MergeCommit.OID == "" {
		return invalid()
	}
	for _, repo := range []*struct {
		NameWithOwner string `json:"nameWithOwner"`
		URL           string `json:"url"`
	}{p.BaseRepository, p.HeadRepository} {
		if repo == nil || repo.NameWithOwner == "" || repo.URL == "" {
			return invalid()
		}
		parsed, err := ParseRepositoryURL(repo.URL)
		if err != nil || !strings.EqualFold(parsed.Owner+"/"+parsed.Name, repo.NameWithOwner) {
			return invalid()
		}
	}
	if !strings.EqualFold(p.BaseRepository.NameWithOwner, response.Data.Repository.Name) || !strings.EqualFold(p.URL, p.BaseRepository.URL+"/pull/"+strconv.Itoa(number)) {
		return invalid()
	}
	for _, ref := range []*liveRef{node.BaseRef, node.HeadRef} {
		if ref == nil || ref.Target == nil || ref.Target.Type != "Commit" || len(ref.Target.OID) != 40 {
			return invalid()
		}
		if _, err := hex.DecodeString(ref.Target.OID); err != nil {
			return invalid()
		}
	}
	p.BaseRefOID, p.HeadRefOID = node.BaseRef.Target.OID, node.HeadRef.Target.OID
	return p.pullRequest(), nil
}
