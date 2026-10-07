//go:build integration

package terminalhost

import (
	"context"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/terminals"
)

func TestSameDimensionResizeAdvancesOrderingWithoutPTYSideEffects(t *testing.T) {
	manager, err := NewManager(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	id := terminals.TerminalID("same-dimension-resize")
	initial := terminals.Dimensions{Columns: 80, Rows: 24}
	command := `
winches=0
trap 'winches=$((winches + 1))' WINCH
printf 'RESIZE_READY\n'
while :; do
	if IFS= read -r line; then
		set -- $(stty size)
		printf 'RESIZE_REPORT_%s:%s:%sx%s\n' "$line" "$winches" "$1" "$2"
	fi
done
`
	if _, err := manager.Launch(t.Context(), terminals.LaunchSpec{
		TerminalID: id, Kind: terminals.LaunchCommand,
		Command: "/bin/bash", Arguments: []string{"-c", command}, Environment: os.Environ(), CWD: t.TempDir(),
		Dimensions: initial,
	}); err != nil {
		t.Fatal(err)
	}

	observer := newResizeAttachmentObserver(t, manager, id)
	observer.waitFor(t, "RESIZE_READY")

	if err := manager.Resize(t.Context(), id, 1, initial); err != nil {
		t.Fatal(err)
	}
	firstNotifications, first := observer.report(t, "one")
	assertResizeReport(t, first, 0, initial)
	assertResizeNotifications(t, firstNotifications, resizeNotification{revision: 1, dimensions: initial})

	changed := terminals.Dimensions{Columns: 100, Rows: 30}
	if err := manager.Resize(t.Context(), id, 2, changed); err != nil {
		t.Fatal(err)
	}
	secondNotifications, second := observer.report(t, "two")
	assertResizeReport(t, second, 1, changed)
	assertResizeNotifications(t, secondNotifications, resizeNotification{revision: 2, dimensions: changed})

	if err := manager.Resize(t.Context(), id, 3, changed); err != nil {
		t.Fatal(err)
	}
	thirdNotifications, third := observer.report(t, "three")
	assertResizeReport(t, third, 1, changed)
	assertResizeNotifications(t, thirdNotifications, resizeNotification{revision: 3, dimensions: changed})

	if err := manager.Resize(t.Context(), id, 2, terminals.Dimensions{Columns: 90, Rows: 25}); err != nil {
		t.Fatal(err)
	}
	staleNotifications, stale := observer.report(t, "stale")
	assertResizeReport(t, stale, 1, changed)
	assertResizeNotifications(t, staleNotifications)
}

type resizeAttachmentObserver struct {
	manager    *Manager
	id         terminals.TerminalID
	attachment terminals.TerminalAttachment
	cursor     Cursor
	text       strings.Builder
}

func newResizeAttachmentObserver(t *testing.T, manager *Manager, id terminals.TerminalID) *resizeAttachmentObserver {
	t.Helper()
	restore, err := manager.Attach(id)
	if err != nil {
		t.Fatal(err)
	}
	observer := &resizeAttachmentObserver{
		manager: manager, id: id, attachment: restore.Attachment,
		cursor: Cursor{TerminalID: id},
	}
	observer.consume(restore.Checkpoint.Sequence, restore.Tail)
	return observer
}

func (observer *resizeAttachmentObserver) waitFor(t *testing.T, marker string) []terminals.ProcessNotification {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var observed []terminals.ProcessNotification
	for !strings.Contains(observer.text.String(), marker) {
		update, err := observer.manager.AttachmentUpdates(
			ctx, observer.id, observer.attachment, observer.cursor, 64*1024,
		)
		if err != nil {
			t.Fatalf("wait for %q: %v; output=%q", marker, err, observer.text.String())
		}
		if update.Checkpoint != nil {
			observer.consume(update.Checkpoint.Sequence, nil)
		}
		observer.consume(0, update.Notifications)
		observed = append(observed, update.Notifications...)
	}
	return observed
}

func (observer *resizeAttachmentObserver) consume(checkpointSequence uint64, notifications []terminals.ProcessNotification) {
	if checkpointSequence > observer.cursor.CheckpointSequence {
		observer.cursor.CheckpointSequence = checkpointSequence
	}
	if checkpointSequence > observer.cursor.Sequence {
		observer.cursor.Sequence = checkpointSequence
	}
	for _, notification := range notifications {
		if notification.Kind == terminals.ProcessOutput {
			observer.text.Write(notification.Data)
		}
		if notification.Sequence > observer.cursor.Sequence {
			observer.cursor.Sequence = notification.Sequence
		}
	}
}

func (observer *resizeAttachmentObserver) report(t *testing.T, marker string) ([]terminals.ProcessNotification, resizeReport) {
	t.Helper()
	// A pending SIGWINCH may interrupt a shell read after consuming its final
	// byte. A disposable suffix keeps the unique report marker intact without
	// adding a timing sleep between the resize and its ordering barrier.
	if err := observer.manager.Input(t.Context(), observer.id, []byte(marker+"XXXXXXXX\n")); err != nil {
		t.Fatal(err)
	}
	notifications := observer.waitFor(t, "RESIZE_REPORT_"+marker)
	pattern := regexp.MustCompile(`RESIZE_REPORT_` + regexp.QuoteMeta(marker) + `X*:([0-9]+):([0-9]+)x([0-9]+)`)
	match := pattern.FindStringSubmatch(observer.text.String())
	if match == nil {
		t.Fatalf("resize report %q was not parseable in %q", marker, observer.text.String())
	}
	return notifications, resizeReport{
		winches:    mustResizeInteger(t, match[1]),
		dimensions: terminals.Dimensions{Rows: mustResizeInteger(t, match[2]), Columns: mustResizeInteger(t, match[3])},
	}
}

type resizeReport struct {
	winches    int
	dimensions terminals.Dimensions
}

type resizeNotification struct {
	revision   uint64
	dimensions terminals.Dimensions
}

func assertResizeReport(t *testing.T, report resizeReport, winches int, dimensions terminals.Dimensions) {
	t.Helper()
	if report.winches != winches || report.dimensions != dimensions {
		t.Fatalf("shell report = signals:%d dimensions:%+v, want signals:%d dimensions:%+v",
			report.winches, report.dimensions, winches, dimensions)
	}
}

func assertResizeNotifications(t *testing.T, notifications []terminals.ProcessNotification, expected ...resizeNotification) {
	t.Helper()
	actual := make([]resizeNotification, 0)
	for _, notification := range notifications {
		if notification.Kind == terminals.ProcessResize {
			actual = append(actual, resizeNotification{revision: notification.ResizeRevision, dimensions: notification.Dimensions})
		}
	}
	if !slices.Equal(actual, expected) {
		t.Fatalf("resize notifications = %+v, want %+v", actual, expected)
	}
}

func mustResizeInteger(t *testing.T, value string) int {
	t.Helper()
	parsed, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
