package localapp

import (
	"context"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

func bindWorkFinalization(service *holons.Service, work localCommitWorkReader) {
	service.SetPendingFinalization(func(ctx context.Context, id string) bool {
		w, err := work.WorkForSession(ctx, id)
		return err == nil && w.IsAddress() && w.Active() && w.Status != pullrequestwork.StatusCancelling && w.PendingCompletion != nil
	})
}

// ProjectHolon includes the durable retry reason without changing agent state.
func (s *terminalHolonService) ProjectHolon(ctx context.Context, h holons.Holon) holons.Holon {
	h = s.Service.ProjectHolon(ctx, h)
	if h.ApplicationPhase == "finalizing" && h.Reason == "" && s.work != nil {
		if w, err := s.work.WorkForSession(ctx, h.ID); err == nil && w.Active() && w.PendingCompletion != nil {
			h.Reason = w.Error
		}
	}
	return h
}
