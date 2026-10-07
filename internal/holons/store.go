package holons

import (
	"context"
	"time"
)

type Store interface {
	Create(context.Context, Holon) error
	Get(context.Context, string) (Holon, error)
	List(context.Context) ([]Holon, error)
	Update(context.Context, Holon) error
	SetSelectedTab(context.Context, string, string) error
	SetStatus(context.Context, string, Status, time.Time) (Holon, error)
	AddAgentSession(context.Context, string, AgentSession) (Holon, error)
	UpdateAgentSession(context.Context, string, AgentSession, bool) (Holon, error)
	AddManualTerminal(context.Context, string, ManualTerminal) (Holon, error)
	UpdateManualTerminal(context.Context, string, ManualTerminal) (Holon, error)
	ReorderTabs(context.Context, string, []TabRef) (Holon, error)
	ListByPullRequest(context.Context, string) ([]Holon, error)
}
