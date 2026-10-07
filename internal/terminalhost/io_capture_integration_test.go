//go:build integration

package terminalhost

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/terminals"
)

func TestTerminalIOCaptureMatchesHostInputAndSequencedOutput(t *testing.T) {
	capture := &fakeTerminalIOCapture{changed: make(chan struct{}, 1)}
	manager, err := NewManager(Options{TerminalIOCapture: capture})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	id := terminals.TerminalID("io-capture")
	if _, err := manager.Launch(t.Context(), terminals.LaunchSpec{
		TerminalID: id, Kind: terminals.LaunchCommand,
		Command: "/bin/sh", Arguments: []string{"-c", "stty -echo; printf 'CAPTURE_READY\\n'; IFS= read -r line; printf 'CAPTURE_OUTPUT:%s\\n' \"$line\"; sleep 30"},
		Environment: os.Environ(), CWD: t.TempDir(), Dimensions: terminals.Dimensions{Columns: 80, Rows: 24},
	}); err != nil {
		t.Fatal(err)
	}
	restore, err := manager.Attach(id)
	if err != nil {
		t.Fatal(err)
	}
	cursor := Cursor{TerminalID: id, CheckpointSequence: restore.Checkpoint.Sequence, Sequence: restore.Checkpoint.Sequence}
	notifications := append([]terminals.ProcessNotification(nil), restore.Tail...)
	for _, notification := range restore.Tail {
		cursor.Sequence = max(cursor.Sequence, notification.Sequence)
	}
	if err := manager.Input(t.Context(), id, []byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for !notificationsContain(notifications, []byte("CAPTURE_OUTPUT:hello")) {
		update, err := manager.AttachmentUpdates(ctx, id, restore.Attachment, cursor, 64*1024)
		if err != nil {
			t.Fatal(err)
		}
		if update.Checkpoint != nil {
			cursor.CheckpointSequence = update.Checkpoint.Sequence
			cursor.Sequence = max(cursor.Sequence, update.Checkpoint.Sequence)
		}
		for _, notification := range update.Notifications {
			notifications = append(notifications, notification)
			cursor.Sequence = max(cursor.Sequence, notification.Sequence)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		inputs, outputs := capture.snapshot()
		if len(inputs) > 0 && len(outputs) > 0 {
			if !bytes.Equal(inputs[0].data, []byte("hello\n")) || inputs[0].writtenBytes != len("hello\n") || inputs[0].result != "written" {
				t.Fatalf("unexpected captured input: %#v", inputs[0])
			}
			for _, output := range outputs {
				if output.result != "accepted" {
					continue
				}
				matched := false
				for _, notification := range notifications {
					if notification.Kind == terminals.ProcessOutput && notification.Sequence == output.sequence &&
						bytes.Equal(notification.Data, output.data) {
						matched = true
						break
					}
				}
				if !matched {
					t.Fatalf("captured output sequence %d bytes %q did not match retained notifications %#v",
						output.sequence, output.data, notifications)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("capture did not observe input and output: inputs=%#v outputs=%#v", inputs, outputs)
		}
		select {
		case <-capture.changed:
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type fakeTerminalIOCapture struct {
	mu      sync.Mutex
	inputs  []fakeInputCapture
	outputs []fakeOutputCapture
	changed chan struct{}
}

type fakeInputCapture struct {
	data         []byte
	writtenBytes int
	result       string
}

type fakeOutputCapture struct {
	data     []byte
	sequence uint64
	result   string
}

func (capture *fakeTerminalIOCapture) CaptureOutput(
	_ terminals.TerminalID,
	data []byte,
	_, _ time.Time,
	sequence uint64,
	result string,
) {
	capture.mu.Lock()
	capture.outputs = append(capture.outputs, fakeOutputCapture{data: append([]byte(nil), data...), sequence: sequence, result: result})
	capture.mu.Unlock()
	capture.notify()
}

func (capture *fakeTerminalIOCapture) CaptureInput(
	_ terminals.TerminalID,
	data []byte,
	_, _ time.Time,
	writtenBytes int,
	result string,
) {
	capture.mu.Lock()
	capture.inputs = append(capture.inputs, fakeInputCapture{data: append([]byte(nil), data...), writtenBytes: writtenBytes, result: result})
	capture.mu.Unlock()
	capture.notify()
}

func (*fakeTerminalIOCapture) CaptureCheckpoint(terminals.TerminalCheckpoint) {}

func (capture *fakeTerminalIOCapture) snapshot() ([]fakeInputCapture, []fakeOutputCapture) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]fakeInputCapture(nil), capture.inputs...), append([]fakeOutputCapture(nil), capture.outputs...)
}

func (capture *fakeTerminalIOCapture) notify() {
	select {
	case capture.changed <- struct{}{}:
	default:
	}
}

func notificationsContain(notifications []terminals.ProcessNotification, marker []byte) bool {
	var output []byte
	for _, notification := range notifications {
		if notification.Kind == terminals.ProcessOutput {
			output = append(output, notification.Data...)
		}
	}
	return bytes.Contains(output, marker)
}
