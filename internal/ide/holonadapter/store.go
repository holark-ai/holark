// Package holonadapter adapts durable Holon records to the IDE domain ports.
package holonadapter

import (
	"context"
	"errors"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/ide"
)

type Holons interface {
	Get(context.Context, string) (holons.Holon, error)
}
type Store struct {
	read Holons
}

func New(r Holons) *Store { return &Store{read: r} }
func (s *Store) Workspace(ctx context.Context, h string) (ide.Workspace, error) {
	v, e := s.read.Get(ctx, h)
	if e != nil {
		return ide.Workspace{}, e
	}
	if v.ArchivedAt != nil || v.WorktreePath == "" {
		return ide.Workspace{}, errors.New("holon has no worktree")
	}
	return ide.Workspace{Path: v.WorktreePath}, nil
}
