package pullrequestlifecycle

import (
	"context"
	"errors"
	"github.com/holark-ai/holark/internal/repository"
)

type ComparisonState string

const (
	ComparisonReady       ComparisonState = "ready"
	ComparisonStale       ComparisonState = "stale"
	ComparisonUnavailable ComparisonState = "unavailable"
)

var ErrComparisonUnavailable = errors.New("pull request comparison is unavailable or stale")

type ComparisonInputs struct {
	RelationshipGeneration int64                      `json:"relationship_generation"`
	Base                   repository.PublishedBranch `json:"base"`
	Head                   repository.PublishedBranch `json:"head"`
	LifecycleGeneration    int64                      `json:"lifecycle_generation"`
}
type ComparisonSnapshot struct {
	Inputs    ComparisonInputs `json:"inputs"`
	MergeBase string           `json:"merge_base"`
}
type ComparisonCatalog interface {
	InvalidateComparison(context.Context, string) error
	BeginBranchObservation(context.Context, []repository.BranchIdentity) (repository.BranchRead, error)
	AcceptBranchObservation(context.Context, repository.BranchRead, map[string]repository.BranchObservation) error
	CaptureComparison(context.Context, string) (ComparisonInputs, error)
	AcceptComparison(context.Context, string, ComparisonInputs, string) error
}

func (p PullRequest) HasCurrentComparison() bool {
	return p.ComparisonState == ComparisonReady && p.Comparison != nil
}

// SameComparisonVersion ignores observation evidence for identical content.
func SameComparisonVersion(a, b ComparisonInputs) bool {
	a.Base.Observation, b.Base.Observation = 0, 0
	a.Head.Observation, b.Head.Observation = 0, 0
	return a == b
}
