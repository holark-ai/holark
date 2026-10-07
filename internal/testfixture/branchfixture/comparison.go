// Package branchfixture supplies accepted branch evidence for integration fixtures.
package branchfixture

import (
	"context"
	"encoding/json"

	pr "github.com/holark-ai/holark/internal/pullrequestlifecycle"
	"github.com/holark-ai/holark/internal/repository"
)

type Catalog interface {
	pr.ComparisonCatalog
	CreatePullRequest(pr.PullRequest) (pr.PullRequest, error)
	GetPullRequest(string) (pr.PullRequest, bool)
}

func Create(ctx context.Context, catalog Catalog, p pr.PullRequest) (pr.PullRequest, error) {
	if p.BaseBranch == "" {
		p.BaseBranch = "main"
	}
	if p.HeadBranch == "" {
		p.HeadBranch = "topic"
	}
	source := pr.DecodeGitHubObservation(p.SyncData)
	if !p.BaseRef.Valid() {
		p.BaseRef = repository.PublishedBranchIdentity(source.BaseRepositoryURL, p.BaseBranch)
	}
	if !p.HeadRef.Valid() {
		p.HeadRef = repository.PublishedBranchIdentity(source.HeadRepositoryURL, p.HeadBranch)
	}
	if !p.BaseRef.Valid() {
		p.BaseRef = repository.PublishedBranchIdentity("https://github.com/fixture/repo.git", p.BaseBranch)
	}
	if !p.HeadRef.Valid() {
		p.HeadRef = repository.PublishedBranchIdentity("https://github.com/fixture/repo.git", p.HeadBranch)
	}
	if p.SyncExternalID != "" {
		var data map[string]any
		_ = json.Unmarshal(p.SyncData, &data)
		if data == nil {
			data = map[string]any{}
		}
		github, _ := data["github"].(map[string]any)
		if github == nil {
			github = map[string]any{}
		}
		if source.BaseRepositoryURL == "" {
			github["base_repository_url"] = "https://" + p.BaseRef.Repository
		}
		if source.HeadRepositoryURL == "" {
			github["head_repository_url"] = "https://" + p.HeadRef.Repository
		}
		data["github"] = github
		p.SyncData, _ = json.Marshal(data)
	}
	base, head, diff := p.BaseCommit, p.HeadCommit, p.DiffBaseCommit
	if base == "" {
		base = "base"
	}
	if head == "" {
		head = "head"
	}
	if diff == "" {
		diff = base
	}
	status := p.Status
	if !status.Active() {
		p.Status = pr.StatusOpen
	}
	saved, err := catalog.CreatePullRequest(p)
	if err != nil {
		return saved, err
	}
	saved, err = Accept(ctx, catalog, saved.ID, base, head, diff)
	if err == nil && !status.Active() {
		saved.Status = status
		return catalog.CreatePullRequest(saved)
	}
	return saved, err
}

func Accept(ctx context.Context, catalog Catalog, id, base, head, mergeBase string) (pr.PullRequest, error) {
	p, ok := catalog.GetPullRequest(id)
	if !ok {
		return p, pr.ErrNotFound
	}
	read, err := catalog.BeginBranchObservation(ctx, []repository.BranchIdentity{p.BaseRef, p.HeadRef})
	if err != nil {
		return p, err
	}
	if err = catalog.AcceptBranchObservation(ctx, read, map[string]repository.BranchObservation{p.BaseRef.Ref: {Commit: base, Exists: true}, p.HeadRef.Ref: {Commit: head, Exists: true}}); err != nil {
		return p, err
	}
	inputs, err := catalog.CaptureComparison(ctx, id)
	if err != nil {
		return p, err
	}
	if err = catalog.AcceptComparison(ctx, id, inputs, mergeBase); err != nil {
		return p, err
	}
	p, _ = catalog.GetPullRequest(id)
	return p, nil
}
