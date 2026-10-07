package repository

import (
	"context"
	"errors"
	"strings"
)

var ErrInvalidSearch = errors.New("search must be a single line of at most 500 characters")

type SearchMatch struct {
	Path         string `json:"path"`
	NameMatch    bool   `json:"name_match"`
	ContentMatch bool   `json:"content_match"`
}
type SearchResults struct {
	Ref       string        `json:"ref"`
	Matches   []SearchMatch `json:"matches"`
	Truncated bool          `json:"truncated"`
}
type SearchStore interface {
	Search(context.Context, string, string) (SearchResults, error)
}

func (s *Service) Search(ctx context.Context, ref, query string) (SearchResults, error) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) > 500 || strings.ContainsAny(query, "\x00\r\n") {
		return SearchResults{}, ErrInvalidSearch
	}
	if query == "" {
		return SearchResults{Ref: ref, Matches: []SearchMatch{}}, nil
	}
	store, ok := s.store.(SearchStore)
	if !ok {
		return SearchResults{}, ErrRepositoryUnavailable
	}
	return store.Search(ctx, ref, query)
}
