//go:build painalizer_live

package localapp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

// This opt-in comparison runs unchanged on the pre-dispatch revision too.
// GitHub, git publication, application completion, and Codex are all real.
func TestPainalizerLiveDispatch(t *testing.T) {
	for _, scenario := range []struct {
		name string
		hold time.Duration
	}{
		{"natural_1", 0}, {"natural_2", 0}, {"natural_3", 0}, {"held_preparation", 5 * time.Second},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			logger := slog.Default()
			handler := &liveDispatchLog{Handler: slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}), hold: scenario.hold}
			slog.SetDefault(slog.New(handler))
			defer slog.SetDefault(logger)
			app, p, repo, published := liveWorkFixtureWithPublishedHead(t, false)
			liveWorkRequest(t, app, "PUT", "/api/v1/settings/default-agent-harness", map[string]any{"harness_type": "codex"}, 200, nil)
			read := func(id string) pullrequestwork.Work {
				t.Helper()
				works, err := app.PullRequestWork.List(t.Context(), p.ID, pullrequestwork.KindWorker)
				if err != nil {
					t.Fatal(err)
				}
				for _, w := range works {
					if w.ID == id {
						return w
					}
				}
				t.Fatalf("missing work %s", id)
				return pullrequestwork.Work{}
			}
			wait := func(label string, condition func() bool) {
				t.Helper()
				deadline := time.NewTimer(4 * time.Minute)
				defer deadline.Stop()
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for !condition() {
					select {
					case <-deadline.C:
						t.Fatalf("timed out: %s", label)
					case <-t.Context().Done():
						t.Fatal(t.Context().Err())
					case <-tick.C:
					}
				}
			}
			admit := func(prompt string) (pullrequestwork.Work, time.Duration) {
				t.Helper()
				var comment pullrequestcomments.Comment
				liveWorkRequest(t, app, "POST", "/api/v1/pull-requests/"+p.ID+"/comments", map[string]any{"scope": "pull_request", "body": "[AUTH TEST] Disposable queue handoff performance measurement."}, 201, &comment)
				var works []pullrequestwork.Work
				started := time.Now()
				liveWorkRequest(t, app, "POST", "/api/v1/pull-requests/"+p.ID+"/workers", map[string]any{"mode": "auto", "comment_ids": []string{comment.ID}, "prompt": prompt}, 201, &works)
				elapsed := time.Since(started)
				if len(works) != 1 {
					t.Fatalf("admitted %+v", works)
				}
				return works[0], elapsed
			}
			gate := filepath.Join(filepath.Dir(repo), "release-outgoing")
			filename := fmt.Sprintf("holark-dispatch-%d.txt", time.Now().UnixNano())
			prompt := fmt.Sprintf("This is an authorized live Holark performance test in a disposable worktree. First wait for the local barrier by executing: while [ ! -f %q ]; do sleep 0.2; done\nThen create only file %s containing LIVE_DISPATCH_OK and a newline. Stage only that file and commit with git -c user.name='Holark Test' -c user.email='holark-test@users.noreply.github.com' commit -m 'Live queue dispatch test'. Do not push or modify any other source files. After committing, create .holark/comment-reply.json containing {\"reply\":\"Added the disposable dispatch verification file.\"}. This file is uncommitted control data; do not stage it. Finish after writing it.", gate, filename)
			admissionStart := time.Now()
			outgoing, admission := admit(prompt)
			returnedStatus := outgoing.Status
			wait("outgoing startup", func() bool { outgoing = read(outgoing.ID); return outgoing.SessionID != "" })
			startup := time.Since(admissionStart)
			h, err := app.Holons.Get(t.Context(), outgoing.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if len(h.AgentSessions) != 1 {
				t.Fatalf("outgoing agents: %+v", h.AgentSessions)
			}
			agentID := h.AgentSessions[0].ID
			successor, queuedAdmission := admit("This is an authorized live queue startup measurement. Do not modify any files, commit, push, or write reply artifacts. Respond READY and wait for further instructions.")
			if successor.Status != pullrequestwork.StatusQueued {
				t.Fatalf("successor not queued: %+v", successor)
			}
			if err = os.WriteFile(gate, []byte("go\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var retiredAt, acknowledgedAt, successorAt time.Time
			wait("published completion, retirement, successor startup", func() bool {
				outgoing = read(outgoing.ID)
				if outgoing.ResultHeadCommit != "" {
					published(outgoing.ResultHeadCommit)
				}
				if outgoing.Status == pullrequestwork.StatusFailed {
					t.Fatalf("outgoing stalled: %+v", outgoing)
				}
				latest, e := app.Holons.Get(t.Context(), outgoing.SessionID)
				if e != nil {
					t.Fatal(e)
				}
				a := latest.AgentSession(agentID)
				if a.Status == string(holons.StatusExpired) && retiredAt.IsZero() {
					retiredAt = a.UpdatedAt
				}
				if outgoing.CompletedAt != nil && acknowledgedAt.IsZero() {
					if _, e = os.Stat(filepath.Join(latest.WorktreePath, ".holark", "comment-reply.json")); os.IsNotExist(e) {
						acknowledgedAt = time.Now()
					}
				}
				successor = read(successor.ID)
				if successor.SessionID != "" && successorAt.IsZero() {
					successorAt = time.Now()
				}
				return outgoing.Status == pullrequestwork.StatusCompleted && !retiredAt.IsZero() && !acknowledgedAt.IsZero() && !successorAt.IsZero()
			})
			if successor.HeadCommit != outgoing.ResultHeadCommit {
				t.Fatalf("successor did not use published head: %+v", successor)
			}
			if scenario.hold > 0 && !handler.held.Load() {
				t.Fatal("successor preparation hold was not exercised")
			}
			t.Logf("DISPATCH_SAMPLE version=%s scenario=%s admission_ms=%.3f admission_status=%s startup_ms=%.3f queued_admission_ms=%.3f completion_to_artifact_ack_ms=%.3f completion_to_retirement_ms=%.3f completion_to_successor_ms=%.3f hold_ms=%d outgoing=%s successor=%s", os.Getenv("HOLARK_DISPATCH_VERSION"), scenario.name, float64(admission)/float64(time.Millisecond), returnedStatus, float64(startup)/float64(time.Millisecond), float64(queuedAdmission)/float64(time.Millisecond), float64(acknowledgedAt.Sub(*outgoing.CompletedAt))/float64(time.Millisecond), float64(retiredAt.Sub(*outgoing.CompletedAt))/float64(time.Millisecond), float64(successorAt.Sub(*outgoing.CompletedAt))/float64(time.Millisecond), scenario.hold.Milliseconds(), outgoing.ID, successor.ID)
			if err = app.PullRequestWork.CancelPullRequest(t.Context(), p.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Adds a labelled, controlled hold only after real successor preparation.
// It does not replace any network, git, or agent operations.
type liveDispatchLog struct {
	slog.Handler
	preparations atomic.Int32
	held         atomic.Bool
	hold         time.Duration
}

func (h *liveDispatchLog) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "Work preparation completed" && h.preparations.Add(1) == 2 && h.hold > 0 {
		h.held.Store(true)
		timer := time.NewTimer(h.hold)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
	}
	return h.Handler.Handle(ctx, r)
}
