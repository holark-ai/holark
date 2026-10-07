// Package localadapter runs terminal processes directly in the Holark process.
package localadapter

import (
	"context"
	"errors"
	"os"
	"syscall"

	"github.com/holark-ai/holark/internal/manualterminals"
	"github.com/holark-ai/holark/internal/terminalenv"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
)

// Gateway adapts the mature PTY manager to the transport-neutral terminal port.
// The node argument is deliberately ignored: local Holark has one process host.
type Gateway struct {
	manager *terminalhost.Manager
	policy  manualterminals.ShellLaunchPolicy

	// InputDelivered observes successful writes. Configure it before serving requests.
	InputDelivered func(terminals.TerminalID, []byte)
}

func New(manager *terminalhost.Manager, context terminalenv.Context) *Gateway {
	return &Gateway{manager: manager, policy: manualterminals.ShellLaunchPolicy{TerminalContext: context}}
}

func (g *Gateway) Launch(ctx context.Context, _ string, spec terminals.LaunchSpec) error {
	if spec.Kind == terminals.LaunchShell {
		base := append(terminalenv.StripHolark(os.Environ()), spec.Environment...)
		spec.Command, spec.Environment = g.policy.Resolve(base, os.Getenv("SHELL"))
	}
	_, err := g.manager.Launch(ctx, spec)
	return translate(err)
}

func (g *Gateway) Attach(ctx context.Context, _ string, id terminals.TerminalID) (terminals.LiveAttachment, error) {
	restore, err := g.manager.Attach(id)
	if err != nil {
		return terminals.LiveAttachment{}, translate(err)
	}
	updates := make(chan terminals.TerminalUpdateStream, 16)
	go func() {
		defer close(updates)
		cursor := terminalhost.Cursor{TerminalID: id, Sequence: restore.LastSequence, CheckpointSequence: restore.Checkpoint.Sequence}
		for {
			update, updateErr := g.manager.AttachmentUpdates(ctx, id, restore.Attachment, cursor, 64*1024)
			if updateErr != nil {
				return
			}
			select {
			case updates <- update:
			case <-ctx.Done():
				return
			}
			cursor.Sequence = update.LastSequence
			if update.Checkpoint != nil {
				cursor.CheckpointSequence = update.Checkpoint.Sequence
			}
		}
	}()
	return terminals.LiveAttachment{Restore: restore, Updates: updates}, nil
}

func (g *Gateway) Detach(_ context.Context, _ string, id terminals.TerminalID, a terminals.TerminalAttachment) error {
	return translate(g.manager.Detach(id, a))
}
func (g *Gateway) Input(ctx context.Context, _ string, id terminals.TerminalID, a terminals.TerminalAttachment, data []byte) error {
	if err := g.manager.InputAttached(ctx, id, a, data); err != nil {
		return translate(err)
	}
	if g.InputDelivered != nil {
		g.InputDelivered(id, data)
	}
	return nil
}
func (g *Gateway) Resize(ctx context.Context, _ string, id terminals.TerminalID, a terminals.TerminalAttachment, revision uint64, dimensions terminals.Dimensions) error {
	return translate(g.manager.ResizeAttached(ctx, id, a, revision, dimensions))
}
func (g *Gateway) Signal(ctx context.Context, _ string, id terminals.TerminalID, signal string) error {
	value := syscall.SIGTERM
	if signal == "kill" {
		value = syscall.SIGKILL
	}
	return translate(g.manager.Signal(ctx, id, value))
}
func (g *Gateway) Close(_ context.Context, _ string, id terminals.TerminalID, a terminals.TerminalAttachment) error {
	if a.Valid() {
		return translate(g.manager.CloseAttached(id, a, 0))
	}
	return translate(g.manager.CloseTerminal(id, 0))
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, terminalhost.ErrNotFound):
		return terminals.ErrProcessLost
	case errors.Is(err, terminalhost.ErrStaleAttachment):
		return terminals.ErrStaleAttachment
	case errors.Is(err, terminalhost.ErrClosed):
		return terminals.ErrConnectionUnavailable
	default:
		return err
	}
}
