package holons

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/protocol"
)

// ErrSetupPersistence distinguishes an uncertain durable transition from a
// preparation failure. Callers must stop before performing more side effects.
var ErrSetupPersistence = errors.New("reserved holon persistence failed")
var ErrReservationEnded = errors.New("reserved holon has ended")

type reservationStore interface {
	CompleteReservation(context.Context, Holon) error
}

// Reserve creates only the durable identity. Retrying the same ID never resets
// an existing holon or creates a workspace or agent.
func (s *Service) Reserve(ctx context.Context, id string, in Create) (Holon, error) {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return Holon{}, err
	}
	defer unlock()
	if id == "" || !((in.PullRequestID != "" && (in.Kind == KindPullWorker || in.Kind == KindRebase)) || (in.Kind == KindNormal && in.PullRequestID == "")) {
		return Holon{}, ErrInvalid
	}
	h, err := s.store.Get(ctx, id)
	if err == nil {
		if h.Kind != in.Kind || h.PullRequestID != in.PullRequestID {
			return Holon{}, ErrInvalid
		}
		return h, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Holon{}, err
	}
	h = Holon{ID: id, Title: NormalizeTitle(in.Title, in.Prompt), Prompt: in.Prompt, Kind: in.Kind, PullRequestID: in.PullRequestID, Status: StatusPreparing, BaseBranch: CanonicalBaseBranch(in.BaseBranch), CreatedAt: s.now().UTC()}
	if err := s.store.Create(ctx, h); err != nil {
		return Holon{}, err
	}
	return h, nil
}

// PrepareReserved fills the reservation from a refreshed repository snapshot and
// persists the complete workspace metadata before reserving an agent session.
func (s *Service) PrepareReserved(ctx context.Context, id string, in Create) (Holon, error) {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return Holon{}, errors.Join(ErrSetupPersistence, err)
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Holon{}, errors.Join(ErrSetupPersistence, err)
	}
	if h.EndRequested || IsTerminal(h.Status) || h.Status == StatusCancelling {
		return h, ErrReservationEnded
	}
	if h.Status != StatusPreparing || h.WorktreePath != "" || len(h.AgentSessions) != 0 || h.Kind != in.Kind || h.PullRequestID != in.PullRequestID {
		return h, ErrInvalid
	}
	store, ok := s.store.(reservationStore)
	if !ok || (in.AgentType == "" && in.StartupMode != "terminal") || strings.TrimSpace(in.BaseCommit) == "" || !utf8.ValidString(in.Prompt) {
		return h, ErrInvalid
	}
	h.Title, h.Prompt = NormalizeTitle(in.Title, in.Prompt), in.Prompt
	h.BaseBranch, h.BaseCommit = CanonicalBaseBranch(in.BaseBranch), in.BaseCommit
	h.WorkSessionStartCommit = strings.TrimSpace(in.WorkSessionStartCommit)
	if h.WorkSessionStartCommit == "" {
		h.WorkSessionStartCommit = in.BaseCommit
	}
	h.UpstreamBranch, h.UpstreamHeadCommit = in.UpstreamBranch, in.UpstreamHeadCommit
	if err = ctx.Err(); err != nil {
		return h, err
	}
	if err = s.prepareWorkspace(ctx, &h, in.RebaseTargetCommit); err != nil {
		return h, err
	}
	if err = store.CompleteReservation(ctx, h); err != nil {
		return h, errors.Join(ErrSetupPersistence, err)
	}
	if err = ctx.Err(); err != nil {
		return h, err
	}
	if in.StartupMode == "terminal" {
		return h, nil
	}
	now := s.now().UTC()
	a := AgentSession{Model: in.Model, Permissions: in.Permissions, ID: newID("agent_"), HolonID: id, AgentType: in.AgentType, Title: NormalizeTitle(in.AgentTitle, "Agent"), Prompt: in.Prompt, Status: string(StatusQueued), Activity: protocol.ActivityStarting, CreatedAt: now, UpdatedAt: now}
	h, err = s.store.AddAgentSession(ctx, id, a)
	if err != nil {
		return h, errors.Join(ErrSetupPersistence, err)
	}
	return h, nil
}

// SettleReserved records a setup outcome even when no agent or workspace exists.
// End intent wins over setup failure and can never be reactivated by a retry.
func (s *Service) SettleReserved(ctx context.Context, id string, status Status, reason string) error {
	if status != StatusFailed && status != StatusCompleted && status != StatusCancelled {
		return ErrInvalid
	}
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if h.EndRequested || h.Status == StatusCancelled || h.Status == StatusCancelling {
		status, reason = StatusCancelled, ""
	}
	now := s.now().UTC()
	for _, a := range h.AgentSessions {
		if !IsTerminal(Status(a.Status)) {
			a.Status, a.Reason, a.FinishedAt, a.UpdatedAt = string(status), reason, &now, now
			if _, err = s.store.UpdateAgentSession(ctx, id, a, false); err != nil {
				return err
			}
		}
	}
	h.Status, h.Reason, h.FinishedAt = status, reason, &now
	return s.store.Update(ctx, h)
}

// ReserveManual persists manual setup before any repository or runtime work.
func (s *Service) ReserveManual(ctx context.Context, in Create) (Holon, error) {
	if in.Kind != KindNormal || in.PullRequestID != "" || !utf8.ValidString(in.Prompt) {
		return Holon{}, ErrInvalid
	}
	return s.Reserve(ctx, newID("holon_"), in)
}
