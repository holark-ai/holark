package sessions

import (
	"context"
	"strings"
	"sync"
	"unicode/utf8"
)

// Service owns session-domain operations.
type Service struct {
	mu    sync.Mutex
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// Rename changes only the session title, leaving harness-session titles intact.
func (service *Service) Rename(ctx context.Context, id, title string) (Session, error) {
	service.mu.Lock()
	defer service.mu.Unlock()

	session, err := service.store.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	title = NormalizeTitle(title, session.Prompt)
	if title == session.Title {
		return session, nil
	}
	session.Title = title
	if err := service.store.UpdateTitle(ctx, session.ID, session.Title); err != nil {
		return Session{}, err
	}
	return session, nil
}

// ApplyGeneratedTitle changes the title only while it still matches the
// fallback observed by the node that generated the proposal.
func (service *Service) ApplyGeneratedTitle(ctx context.Context, id, expected, proposed string) (Session, bool, error) {
	service.mu.Lock()
	defer service.mu.Unlock()

	session, err := service.store.Get(ctx, id)
	if err != nil {
		return Session{}, false, err
	}
	expected = strings.TrimSpace(expected)
	proposed = strings.TrimSpace(proposed)
	if expected == "" || proposed == "" || !utf8.ValidString(expected) || !utf8.ValidString(proposed) || !TitleWithinLimit(expected) || !TitleWithinLimit(proposed) || session.Title != expected {
		return session, false, nil
	}
	applied, err := service.store.CompareAndSetTitle(ctx, session.ID, expected, proposed)
	if err != nil {
		return Session{}, false, err
	}
	if !applied {
		current, getErr := service.store.Get(ctx, id)
		if getErr != nil {
			return Session{}, false, getErr
		}
		return current, false, nil
	}
	session.Title = proposed
	return session, true, nil
}
