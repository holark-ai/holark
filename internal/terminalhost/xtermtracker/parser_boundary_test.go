package xtermtracker

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/terminals"
)

func TestTopAnchoredScrollUpPreservesOutgoingRowsInScrollback(t *testing.T) {
	tracker := newParserBoundaryTracker(t, "terminal-scroll-up", terminals.Dimensions{Columns: 20, Rows: 5})
	if err := tracker.Write(1, []byte("A\r\nB\r\nC\r\nD\r\nE\x1b[1;5r\x1b[2S\x1b[r")); err != nil {
		t.Fatal(err)
	}

	response, err := tracker.call(map[string]any{"command": "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	if got := integerInspectionField(t, response, "baseY"); got != 2 {
		t.Errorf("scrollback base = %d, want 2", got)
	}
	wantLines := []string{"A", "B", "C", "D", "E", "", ""}
	if got := stringSliceInspectionField(t, response, "allLines"); !reflect.DeepEqual(got, wantLines) {
		t.Errorf("terminal lines after top-anchored scroll up = %#v, want %#v", got, wantLines)
	}
}

func TestMinimumCheckpointStateReportsUnavailableWithoutFailingWorker(t *testing.T) {
	dimensions := terminals.Dimensions{Columns: 20, Rows: 5}
	tests := []struct {
		name string
		new  func(t *testing.T) terminals.ScreenTracker
	}{
		{
			name: "legacy",
			new: func(t *testing.T) terminals.ScreenTracker {
				tracker, err := New("terminal-minimum-legacy", dimensions, Options{
					ScrollbackLines: 10, MaximumReplayBytes: 1,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = tracker.Close() })
				return tracker
			},
		},
		{
			name: "pooled",
			new: func(t *testing.T) terminals.ScreenTracker {
				pool, err := NewPool(PoolOptions{
					ScrollbackLines: 10, MaximumReplayBytes: 1, MaximumWorkers: 1,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = pool.Close() })
				tracker, err := pool.New("terminal-minimum-pooled", dimensions)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if closer, ok := tracker.(interface{ Close() error }); ok {
						_ = closer.Close()
					}
				})
				return tracker
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := test.new(t)
			if err := tracker.Write(1, []byte("current viewport")); err != nil {
				t.Fatal(err)
			}
			if _, err := tracker.Snapshot(1); !errors.Is(err, terminals.ErrCheckpointUnavailable) {
				t.Fatalf("snapshot error = %v, want checkpoint unavailable", err)
			}
			if err := tracker.Write(2, []byte(" still live")); err != nil {
				t.Fatalf("write after unavailable snapshot: %v", err)
			}
		})
	}
}

func TestCheckpointRestorationPreservesUnfinishedParserInput(t *testing.T) {
	tests := []struct {
		name   string
		prefix []byte
		tail   []byte
	}{
		{
			name:   "CSI mode",
			prefix: []byte("ready>\x1b[?2\n00"),
			tail:   []byte("4hrestored"),
		},
		{
			name:   "OSC title",
			prefix: []byte("ready>\x1b]2;parser-boundary"),
			tail:   []byte("\x07restored"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dimensions := terminals.Dimensions{Columns: 40, Rows: 5}
			uninterrupted := newParserBoundaryTracker(t, "terminal-uninterrupted", dimensions)
			if err := uninterrupted.Write(1, test.prefix); err != nil {
				t.Fatal(err)
			}
			checkpoint, err := uninterrupted.Snapshot(1)
			if err != nil {
				t.Fatal(err)
			}
			if err := uninterrupted.Write(2, test.tail); err != nil {
				t.Fatal(err)
			}

			restored := newParserBoundaryTracker(t, "terminal-restored", dimensions)
			if err := restored.Write(1, checkpoint.ReplayPayload); err != nil {
				t.Fatal(err)
			}
			if err := restored.Write(2, test.tail); err != nil {
				t.Fatal(err)
			}

			want := inspectParserBoundaryTracker(t, uninterrupted)
			got := inspectParserBoundaryTracker(t, restored)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("restored terminal diverged from uninterrupted delivery\n got: %#v\nwant: %#v\ncheckpoint: %q", got, want, checkpoint.ReplayPayload)
			}
		})
	}
}

func TestOversizedCheckpointTrimsOnlyOldestScrollback(t *testing.T) {
	dimensions := terminals.Dimensions{Columns: 480, Rows: 31}
	uninterrupted, err := New("terminal-oversized-uninterrupted", dimensions, Options{
		ScrollbackLines: 50_000, OperationTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = uninterrupted.Close() })

	var input bytes.Buffer
	for line := 0; line < 45_000; line++ {
		fmt.Fprintf(&input, "\x1b[38;5;%dm%06d:%s\x1b[0m\r\n", line%256, line, bytes.Repeat([]byte{'x'}, 470))
	}
	sequence := uint64(0)
	for input.Len() > 0 {
		sequence++
		chunk := input.Next(min(input.Len(), maximumWriteBytes))
		if err := uninterrupted.Write(sequence, chunk); err != nil {
			t.Fatal(err)
		}
	}
	sequence++
	prefix := []byte("\x1b[?1h\x1b[?1049h\x1b[2J\x1b[HALTERNATE_PRESERVED\x1b]2;pending-title-\xe2\x82")
	if err := uninterrupted.Write(sequence, prefix); err != nil {
		t.Fatal(err)
	}

	checkpoint, err := uninterrupted.Snapshot(sequence)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.ReplayPayload) > terminals.DefaultCheckpointReplayBytes {
		t.Fatalf("trimmed checkpoint has %d bytes", len(checkpoint.ReplayPayload))
	}
	tail := []byte("\xac\x07\r\nAFTER_BOUNDARY")
	if err := uninterrupted.Write(sequence+1, tail); err != nil {
		t.Fatal(err)
	}

	restored, err := New("terminal-oversized-restored", checkpoint.Dimensions, Options{
		ScrollbackLines: 50_000, OperationTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if err := restored.Write(sequence, checkpoint.ReplayPayload); err != nil {
		t.Fatal(err)
	}
	if err := restored.Write(sequence+1, tail); err != nil {
		t.Fatal(err)
	}

	want := inspectParserBoundaryTracker(t, uninterrupted)
	got := inspectParserBoundaryTracker(t, restored)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trimmed restore diverged from uninterrupted delivery\n got: %#v\nwant: %#v", got, want)
	}
}

type parserBoundaryInspection struct {
	Columns    int
	Rows       int
	CursorX    int
	CursorY    int
	BaseY      int
	Title      string
	BufferType string
	Modes      map[string]any
	Lines      []string
}

func newParserBoundaryTracker(t *testing.T, id terminals.TerminalID, dimensions terminals.Dimensions) *Tracker {
	t.Helper()
	tracker, err := New(id, dimensions, Options{ScrollbackLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	return tracker
}

func inspectParserBoundaryTracker(t *testing.T, tracker *Tracker) parserBoundaryInspection {
	t.Helper()
	response, err := tracker.call(map[string]any{"command": "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	return parserBoundaryInspection{
		Columns:    integerInspectionField(t, response, "columns"),
		Rows:       integerInspectionField(t, response, "rows"),
		CursorX:    integerInspectionField(t, response, "cursorX"),
		CursorY:    integerInspectionField(t, response, "cursorY"),
		BaseY:      integerInspectionField(t, response, "baseY"),
		Title:      stringInspectionField(t, response, "title"),
		BufferType: stringInspectionField(t, response, "bufferType"),
		Modes:      mapInspectionField(t, response, "modes"),
		Lines:      stringSliceInspectionField(t, response, "lines"),
	}
}

func integerInspectionField(t *testing.T, fields map[string]any, name string) int {
	t.Helper()
	value, ok := fields[name].(float64)
	if !ok {
		t.Fatalf("%s = %#v", name, fields[name])
	}
	return int(value)
}

func stringInspectionField(t *testing.T, fields map[string]any, name string) string {
	t.Helper()
	value, ok := fields[name].(string)
	if !ok {
		t.Fatalf("%s = %#v", name, fields[name])
	}
	return value
}

func mapInspectionField(t *testing.T, fields map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := fields[name].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v", name, fields[name])
	}
	return value
}

func stringSliceInspectionField(t *testing.T, fields map[string]any, name string) []string {
	t.Helper()
	values, ok := fields[name].([]any)
	if !ok {
		t.Fatalf("%s = %#v", name, fields[name])
	}
	result := make([]string, len(values))
	for index, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("%s[%d] = %#v", name, index, value)
		}
		result[index] = text
	}
	return result
}
