package localadapter

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/terminalenv"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
)

func TestGatewayLaunchAttachResizeFenceReconnectAndClose(t *testing.T) {
	manager, err := terminalhost.NewManager(terminalhost.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	gateway := New(manager, terminalenv.Context{})
	id, err := terminals.NewID()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = gateway.Launch(ctx, "ignored", terminals.LaunchSpec{
		TerminalID: id, Kind: terminals.LaunchCommand, Command: "/bin/sh",
		Arguments:   []string{"-c", "printf local-ready; trap 'exit 0' TERM; while :; do sleep 1; done"},
		Environment: os.Environ(), Dimensions: terminals.Dimensions{Columns: 80, Rows: 24},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := gateway.Attach(ctx, "ignored", id)
	if err != nil {
		t.Fatal(err)
	}
	if first.Restore.Checkpoint.FormatVersion != terminals.CheckpointFormatANSI {
		t.Fatalf("checkpoint format = %q", first.Restore.Checkpoint.FormatVersion)
	}
	if err := gateway.Resize(ctx, "ignored", id, first.Restore.Attachment, 1, terminals.Dimensions{Columns: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	second, err := gateway.Attach(ctx, "ignored", id)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.Input(ctx, "ignored", id, first.Restore.Attachment, []byte("stale")); !errors.Is(err, terminals.ErrStaleAttachment) {
		t.Fatalf("stale input error = %v", err)
	}
	if second.Restore.LastSequence < first.Restore.LastSequence {
		t.Fatalf("reconnect regressed sequence: %d < %d", second.Restore.LastSequence, first.Restore.LastSequence)
	}
	if err := gateway.Close(ctx, "ignored", id, second.Restore.Attachment); err != nil {
		t.Fatal(err)
	}
}
