package localapp

import (
	"context"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/protocol"
)

// PreflightSelection snapshots the harness, model, and permissions before workflow preparation.
func (s *terminalHolonService) PreflightSelection(ctx context.Context, in *holons.Create) error {
	if in.StartupMode == "terminal" || in.SelectionResolved || s.harnesses == nil {
		return nil
	}
	selection, err := s.harnesses.Resolve(ctx, workflowForKind(in.Kind), protocol.HarnessType(in.AgentType))
	if err != nil {
		return err
	}
	in.AgentType, in.Model, in.Permissions, in.SelectionResolved = string(selection.HarnessType), selection.Model, selection.Permissions, true
	return nil
}
