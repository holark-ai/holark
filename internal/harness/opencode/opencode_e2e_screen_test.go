//go:build opencode_e2e

package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
)

// waitOpenCodeE2EScreenText reconstructs the visible character grid using the
// existing xterm worker. Neither scrollback nor overwritten output can match.
// Each wait owns a fresh renderer, restored from a checkpoint and sequenced tail.
func waitOpenCodeE2EScreenText(t *testing.T, manager *terminalhost.Manager, id terminals.TerminalID, expected string, wholeLine bool, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	worker := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "..", "terminal-worker", "worker.mjs"))
	worker.WaitDelay = time.Second
	var stderr bytes.Buffer
	worker.Stderr = &stderr
	stdin, err := worker.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := worker.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	var screen struct {
		OK         bool     `json:"ok"`
		Error      string   `json:"error"`
		Lines      []string `json:"lines"`
		CursorX    int      `json:"cursorX"`
		CursorY    int      `json:"cursorY"`
		BufferType string   `json:"bufferType"`
	}
	cursor := terminalhost.Cursor{TerminalID: id}
	defer func() {
		cancel()
		_ = worker.Wait()
		if t.Failed() {
			t.Logf("waiting up to %s for screen text %q (whole line: %t); terminal=%s sequence=%d checkpoint=%d buffer=%s cursor=(%d,%d)\nlast screen:\n%s\nxterm stderr: %s",
				timeout, expected, wholeLine, id, cursor.Sequence, cursor.CheckpointSequence,
				screen.BufferType, screen.CursorX, screen.CursorY, strings.Join(screen.Lines, "\n"), stderr.String())
		}
	}()

	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(stdout)
	call := func(request map[string]any) {
		t.Helper()
		if err := encoder.Encode(request); err != nil {
			t.Fatalf("write xterm request: %v (wait context: %v)", err, ctx.Err())
		}
		// Non-inspection responses leave the last rendered screen intact for
		// diagnostics if a subsequent write or inspection times out.
		if err := decoder.Decode(&screen); err != nil {
			t.Fatalf("read xterm response: %v (wait context: %v)", err, ctx.Err())
		}
		if !screen.OK {
			t.Fatalf("xterm %v failed: %s", request["command"], screen.Error)
		}
	}
	// Worker sequence numbers also cover replay writes, including an initial
	// checkpoint at host sequence zero. Keep them separate from the host cursor.
	var workerSequence uint64
	write := func(data []byte) {
		if len(data) > 0 {
			workerSequence++
			call(map[string]any{"command": "write", "sequence": workerSequence, "data": data})
		}
	}
	restore := func(checkpoint terminals.TerminalCheckpoint) {
		call(map[string]any{"command": "destroy", "model": "__legacy"})
		call(map[string]any{
			"command": "init", "columns": checkpoint.Dimensions.Columns, "rows": checkpoint.Dimensions.Rows,
			"scrollback": terminals.DefaultScrollbackLines, "maximum_replay_bytes": terminals.DefaultCheckpointReplayBytes,
		})
		write(checkpoint.ReplayPayload)
		cursor.Sequence = checkpoint.Sequence
		cursor.CheckpointSequence = checkpoint.Sequence
	}
	apply := func(notifications []terminals.ProcessNotification) {
		t.Helper()
		for _, notification := range notifications {
			if notification.Sequence != cursor.Sequence+1 {
				t.Fatalf("terminal sequence gap: have %d, received %d", cursor.Sequence, notification.Sequence)
			}
			switch notification.Kind {
			case terminals.ProcessOutput:
				write(notification.Data)
			case terminals.ProcessResize:
				workerSequence++
				call(map[string]any{
					"command": "resize", "sequence": workerSequence,
					"columns": notification.Dimensions.Columns, "rows": notification.Dimensions.Rows,
				})
			case terminals.ProcessExit:
				t.Fatalf("OpenCode exited while waiting for screen text %q", expected)
			}
			cursor.Sequence = notification.Sequence
		}
	}

	attachment, err := manager.Attach(id)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Detach(id, attachment.Attachment)
	restore(attachment.Checkpoint)
	apply(attachment.Tail)
	for {
		if err := ctx.Err(); err != nil {
			t.Fatalf("waiting for screen text %q: %v", expected, err)
		}
		call(map[string]any{"command": "inspect"})
		for _, line := range screen.Lines {
			if wholeLine && strings.TrimSpace(line) == expected || !wholeLine && strings.Contains(line, expected) {
				return
			}
		}
		update, err := manager.AttachmentUpdates(ctx, id, attachment.Attachment, cursor, 256*1024)
		if err != nil {
			t.Fatalf("waiting for screen text %q: %v", expected, err)
		}
		if checkpoint := update.Checkpoint; checkpoint != nil {
			// A compaction acknowledgment can be behind our already-rendered
			// cursor. Only replace the model when the checkpoint catches it up.
			if checkpoint.Sequence >= cursor.Sequence {
				restore(*checkpoint)
			}
			cursor.CheckpointSequence = checkpoint.Sequence
		}
		apply(update.Notifications)
	}
}
