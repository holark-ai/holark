package sessions

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestServiceRenameNormalizesAndPersistsSessionTitle(t *testing.T) {
	stored := Session{ID: "session-one", Title: "Old title", Prompt: "Prompt fallback\nDetails"}
	store := &sessionStoreFake{session: stored}
	service := NewService(store)

	renamed, err := service.Rename(t.Context(), stored.ID, "  Renamed session  ")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "Renamed session" || store.session.Title != "Renamed session" || store.updates != 1 {
		t.Fatalf("renamed = %+v, store = %+v, updates = %d", renamed, store.session, store.updates)
	}

	renamed, err = service.Rename(t.Context(), stored.ID, "   ")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "Prompt fallback" || store.updates != 2 {
		t.Fatalf("fallback rename = %+v, updates = %d", renamed, store.updates)
	}

	renamed, err = service.Rename(t.Context(), stored.ID, strings.Repeat("x", MaxTitleCharacters+1))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != strings.Repeat("x", MaxTitleCharacters) || store.updates != 3 {
		t.Fatalf("truncated rename = %+v, updates = %d", renamed, store.updates)
	}
}

func TestServiceRenameSkipsUnchangedTitleAndReturnsStoreErrors(t *testing.T) {
	store := &sessionStoreFake{session: Session{ID: "session-one", Title: "Same", Prompt: "Prompt"}}
	service := NewService(store)
	if _, err := service.Rename(t.Context(), "session-one", " Same "); err != nil {
		t.Fatal(err)
	}
	if store.updates != 0 {
		t.Fatalf("updates = %d, want 0", store.updates)
	}

	readErr := errors.New("read failed")
	store.getErr = readErr
	if _, err := service.Rename(t.Context(), "session-one", "New"); !errors.Is(err, readErr) {
		t.Fatalf("read error = %v, want %v", err, readErr)
	}

	store.getErr = nil
	store.updateErr = errors.New("write failed")
	if _, err := service.Rename(t.Context(), "session-one", "New"); !errors.Is(err, store.updateErr) {
		t.Fatalf("write error = %v, want %v", err, store.updateErr)
	}
}

func TestServiceAppliesGeneratedTitleOnlyToExpectedFallback(t *testing.T) {
	store := &sessionStoreFake{session: Session{ID: "session-one", Title: "Fix login flow", Prompt: "Fix login flow"}}
	service := NewService(store)

	updated, applied, err := service.ApplyGeneratedTitle(t.Context(), "session-one", "Fix login flow", "Repair login flow")
	if err != nil {
		t.Fatal(err)
	}
	if !applied || updated.Title != "Repair login flow" || store.updates != 1 {
		t.Fatalf("updated = %+v, applied = %v, updates = %d", updated, applied, store.updates)
	}

	for _, test := range []struct {
		name, expected, proposed string
	}{
		{name: "manual rename wins", expected: "Fix login flow", proposed: "Generated again"},
		{name: "blank proposal", expected: "Repair login flow", proposed: "   "},
		{name: "long proposal", expected: "Repair login flow", proposed: strings.Repeat("界", MaxTitleCharacters+1)},
		{name: "stale expected", expected: "Different fallback", proposed: "Generated again"},
	} {
		t.Run(test.name, func(t *testing.T) {
			current, changed, err := service.ApplyGeneratedTitle(t.Context(), "session-one", test.expected, test.proposed)
			if err != nil {
				t.Fatal(err)
			}
			if changed || current.Title != "Repair login flow" || store.updates != 1 {
				t.Fatalf("current = %+v, changed = %v, updates = %d", current, changed, store.updates)
			}
		})
	}
}

type sessionStoreFake struct {
	session   Session
	getErr    error
	updateErr error
	updates   int
}

func (store *sessionStoreFake) Get(context.Context, string) (Session, error) {
	return store.session, store.getErr
}

func (store *sessionStoreFake) UpdateTitle(_ context.Context, id, title string) error {
	if store.updateErr != nil {
		return store.updateErr
	}
	store.updates++
	store.session.ID, store.session.Title = id, title
	return nil
}

func (store *sessionStoreFake) CompareAndSetTitle(_ context.Context, id, expected, title string) (bool, error) {
	if store.updateErr != nil {
		return false, store.updateErr
	}
	if store.session.ID != id || store.session.Title != expected {
		return false, nil
	}
	store.updates++
	store.session.Title = title
	return true, nil
}
