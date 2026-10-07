package sessions

import "context"

// Store is the session-owned persistence required by the domain service.
type Store interface {
	Get(context.Context, string) (Session, error)
	UpdateTitle(context.Context, string, string) error
	CompareAndSetTitle(context.Context, string, string, string) (bool, error)
}
