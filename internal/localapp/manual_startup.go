package localapp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/repository"
)

// manualStartupJobs owns startup after HTTP acceptance and joins it before the
// application's runtime and database close. A reservation has one launch owner.
type manualStartupJobs struct {
	ctx     context.Context
	prepare func(context.Context, string) (repository.Preparation, error)
	mu      sync.Mutex
	closed  bool
	jobs    sync.WaitGroup
}

func (j *manualStartupJobs) Close() {
	j.mu.Lock()
	j.closed = true
	j.mu.Unlock()
	j.jobs.Wait()
}

func (s *terminalHolonService) CreateAsync(ctx context.Context, in holons.Create) (holons.Holon, error) {
	if err := in.ValidateStartup(); err != nil {
		return holons.Holon{}, err
	}
	if (in.Kind != "" && in.Kind != holons.KindNormal) || strings.TrimSpace(in.BaseBranch) == "" || s.startups == nil {
		return holons.Holon{}, holons.ErrInvalid
	}
	in.Kind = holons.KindNormal
	selectionErr := s.PreflightSelection(ctx, &in)
	j := s.startups
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.ctx.Err() != nil {
		return holons.Holon{}, context.Canceled
	}
	h, err := s.Service.ReserveManual(ctx, in)
	if err != nil {
		return h, err
	}
	j.jobs.Add(1)
	go func() {
		defer j.jobs.Done()
		if err := s.startManualReservation(j.ctx, h.ID, in, selectionErr); err != nil {
			// A completed launch belongs to ordinary recovery even if shutdown
			// cancelled the final read after the runtime accepted it.
			if j.ctx.Err() != nil {
				latest, readErr := s.Get(context.WithoutCancel(j.ctx), h.ID)
				if readErr == nil && latest.StartedAt != nil && latest.Status == holons.StatusRunning {
					return
				}
			}
			reason := "Could not prepare Holon: " + err.Error()
			if settleErr := s.Service.SettleReserved(context.WithoutCancel(j.ctx), h.ID, holons.StatusFailed, reason); settleErr != nil {
				slog.Error("Persist manual startup failure", "holon_id", h.ID, "error", errors.Join(err, settleErr))
			}
		}
	}()
	return h, nil
}

func (s *terminalHolonService) startManualReservation(ctx context.Context, id string, in holons.Create, selectionErr error) error {
	prepared, err := s.startups.prepare(ctx, in.BaseBranch)
	if err != nil {
		return err
	}
	// Preserve asynchronous failure reporting while fixing the pair at acceptance.
	if selectionErr != nil {
		return selectionErr
	}
	in.BaseBranch, in.BaseCommit = prepared.Branch, prepared.Commit
	options := agentsessions.LaunchOptions{GenerateIdentity: true, GenerateTitle: strings.TrimSpace(in.Title) == ""}
	if in.StartupMode != "terminal" {
		if in.AgentType == "" {
			in.AgentType, err = s.PreflightAgent(ctx, in.Kind)
		} else if s.harnesses != nil {
			err = s.harnesses.Validate(ctx, protocol.HarnessType(in.AgentType), workflowForKind(in.Kind))
		}
		if err != nil {
			return err
		}
	}
	// PrepareReserved checks cancellation before creating the workspace.
	h, err := s.Service.PrepareReserved(ctx, id, in)
	if err != nil {
		return err
	}
	unlock := s.lockQueuedLaunch(id)
	defer unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	h, err = s.Get(ctx, id)
	if err != nil {
		return err
	}
	if h.EndRequested || holons.IsTerminal(h.Status) || h.Status == holons.StatusCancelling {
		return holons.ErrReservationEnded
	}
	if in.StartupMode != "terminal" {
		return s.launchAgentRuntime(ctx, h, h.AgentSessions[0].ID, options)
	}
	started, startupErr := s.AddManualTerminal(ctx, id, "", "", "")
	if startupErr == nil {
		startupErr = s.SelectTab(context.WithoutCancel(ctx), id, started.ManualTerminals[0].ID)
	}
	h, err = s.Service.FinalizeTerminalStartup(context.WithoutCancel(ctx), id, startupErr)
	if startupErr != nil || err != nil {
		return errors.Join(startupErr, err)
	}
	if strings.TrimSpace(h.Prompt) != "" && s.naming != nil {
		s.naming.GenerateIdentity(agentsessions.Session{HolonID: h.ID, Title: h.Title, Prompt: h.Prompt, Worktree: h.WorktreePath}, options)
	}
	return nil
}

// Manual setup is never replayed after restart. Existing execution evidence is
// left to ordinary agent/terminal recovery; queued PR recovery is independent.
func settleInterruptedManualPreparations(ctx context.Context, service *holons.Service) error {
	all, err := service.List(ctx)
	if err != nil {
		return err
	}
	for _, h := range all {
		if h.Kind != holons.KindNormal || h.StartedAt != nil || (h.Status != holons.StatusPreparing && h.Status != holons.StatusQueued && h.Status != holons.StatusNaming) {
			continue
		}
		// Shell tabs are persisted before launch. Only FinalizeTerminalStartup's
		// StartedAt (checked above) proves their startup completed.
		launched := false
		for _, a := range h.AgentSessions {
			launched = launched || a.StartedAt != nil || a.TerminalID != "" || a.ResumeTarget != ""
		}
		if !launched {
			if err := service.SettleReserved(ctx, h.ID, holons.StatusFailed, "Holark stopped before preparation finished. Create a new Holon to try again."); err != nil {
				return err
			}
		}
	}
	return nil
}
