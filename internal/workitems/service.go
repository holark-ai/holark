// Package workitems provides repository-local browsing over canonical records.
package workitems

import (
	"context"
	"fmt"

	"github.com/holark-ai/holark/internal/issues"
)

type Store interface {
	Labels(context.Context, string) ([]issues.Label, error)
	Search(context.Context, string, Query, int, int) (Result, error)
	MyWork(context.Context, string, string, int, int) (Result, error)
}
type Service struct {
	store        Store
	repositoryID string
}

func New(store Store, repositoryID string) *Service { return &Service{store, repositoryID} }
func (s *Service) Search(ctx context.Context, raw string, page, size int) (Result, error) {
	return s.SearchTitle(ctx, raw, "", page, size)
}

func (s *Service) SearchTitle(ctx context.Context, raw, title string, page, size int) (Result, error) {
	q, err := Parse(raw)
	if err != nil {
		return Result{}, err
	}
	q.Title = title
	return s.store.Search(ctx, s.repositoryID, q, page, size)
}
func (s *Service) SearchIssues(ctx context.Context, raw string, page, size int) (Result, error) {
	return s.SearchIssuesTitle(ctx, raw, "", page, size)
}
func (s *Service) SearchIssuesTitle(ctx context.Context, raw, title string, page, size int) (Result, error) {
	q, err := ParseIssues(raw)
	if err != nil {
		return Result{}, err
	}
	q.Title = title
	return s.store.Search(ctx, s.repositoryID, q, page, size)
}
func (s *Service) Labels(ctx context.Context) ([]issues.Label, error) {
	return s.store.Labels(ctx, s.repositoryID)
}
func (s *Service) MyWork(ctx context.Context, view string, page, size int) (Result, error) {
	switch view {
	case "all", "review_requested", "assigned_issues", "assigned_pull_requests":
	default:
		return Result{}, fmt.Errorf("invalid My work view")
	}
	return s.store.MyWork(ctx, s.repositoryID, view, page, size)
}
