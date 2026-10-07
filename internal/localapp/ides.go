package localapp

import (
	"context"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/ide"
)

// localIDEService coordinates IDE closure with deferred finite-Holon archival.
type localIDEService struct {
	*ide.Service
	holons *holons.Service
}

func (s *localIDEService) Close(ctx context.Context, holonID, id string) (ide.IDE, error) {
	editor, err := s.Service.Close(ctx, holonID, id)
	if err != nil || editor.DesiredOpen {
		return editor, err
	}
	_, err = s.holons.ArchiveFinite(ctx, holonID)
	return editor, err
}
