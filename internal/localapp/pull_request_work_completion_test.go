package localapp

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

type completionWorkStub struct {
	work                                            pullrequestwork.Work
	lookupErr                                       error
	completeErr                                     error
	completeDespiteError                            bool
	completeCalls, failCalls, waitCalls, staleCalls int
}

func (s *completionWorkStub) WorkForSession(context.Context, string) (pullrequestwork.Work, error) {
	return s.work, s.lookupErr
}
func (s *completionWorkStub) Complete(context.Context, string, string, pullrequestwork.Completion) (pullrequestwork.Work, error) {
	s.completeCalls++
	if s.completeErr != nil && !s.completeDespiteError {
		return pullrequestwork.Work{}, s.completeErr
	}
	s.work.Status = pullrequestwork.StatusCompleted
	if s.completeErr != nil {
		return s.work, s.completeErr
	}
	return s.work, nil
}
func (s *completionWorkStub) Fail(context.Context, string, string) error {
	s.failCalls++
	s.work.Status = pullrequestwork.StatusFailed
	return nil
}
func (s *completionWorkStub) Wait(context.Context, string, string) error {
	s.waitCalls++
	s.work.Status = pullrequestwork.StatusWaiting
	return nil
}
func (s *completionWorkStub) Stale(context.Context, string, string) error {
	s.staleCalls++
	s.work.Status = pullrequestwork.StatusStale
	return nil
}

func workArtifactHolon(t *testing.T, kind pullrequestwork.Kind, data string) holons.Holon {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".holark"), 0o700); err != nil {
		t.Fatal(err)
	}
	if data != "" {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(pullRequestWorkArtifactPath(kind))), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return holons.Holon{ID: "holon-1", PullRequestID: "pr-1", WorktreePath: root}
}

func TestReviewCompletionConsumesArtifactExactlyOnce(t *testing.T) {
	h := workArtifactHolon(t, pullrequestwork.KindReview, `{"comments":[]}`)
	partPath := filepath.Join(h.WorktreePath, ".holark", "review.part-1.json")
	if err := os.WriteFile(partPath, []byte(`{"comments":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	work := &completionWorkStub{work: pullrequestwork.Work{ID: "work-1", SessionID: h.ID, PullRequestID: h.PullRequestID, Kind: pullrequestwork.KindReview, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusRunning, HeadCommit: "head"}}
	coordinator := localPullRequestWorkCompletionCoordinator{work: work}
	disposition, err := coordinator.Complete(t.Context(), h)
	if err != nil || !disposition.Terminal || work.completeCalls != 1 {
		t.Fatalf("disposition=%+v complete=%d err=%v", disposition, work.completeCalls, err)
	}
	if _, err = os.Stat(filepath.Join(h.WorktreePath, ".holark", "review.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("artifact was not consumed: %v", err)
	}
	if _, err = os.Stat(partPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("part artifact was not consumed: %v", err)
	}
	disposition, err = coordinator.Complete(t.Context(), h)
	if err != nil || !disposition.AlreadyHandled || work.completeCalls != 1 {
		t.Fatalf("duplicate disposition=%+v complete=%d err=%v", disposition, work.completeCalls, err)
	}
}

func TestAssistedMissingWaitsButInvalidFailsAndIsRemoved(t *testing.T) {
	missing := workArtifactHolon(t, pullrequestwork.KindWorker, "")
	missingWork := &completionWorkStub{work: pullrequestwork.Work{ID: "missing", SessionID: missing.ID, PullRequestID: missing.PullRequestID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAssisted, Status: pullrequestwork.StatusRunning, HeadCommit: "head"}}
	disposition, err := (localPullRequestWorkCompletionCoordinator{work: missingWork}).Complete(t.Context(), missing)
	if err != nil || disposition.Terminal || missingWork.waitCalls != 1 || missingWork.failCalls != 0 {
		t.Fatalf("missing disposition=%+v work=%+v err=%v", disposition, missingWork, err)
	}

	invalid := workArtifactHolon(t, pullrequestwork.KindWorker, `{"reply":""}`)
	invalidWork := &completionWorkStub{work: pullrequestwork.Work{ID: "invalid", SessionID: invalid.ID, PullRequestID: invalid.PullRequestID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAssisted, Status: pullrequestwork.StatusRunning, HeadCommit: "head"}}
	disposition, err = (localPullRequestWorkCompletionCoordinator{work: invalidWork}).Complete(t.Context(), invalid)
	if !errors.Is(err, pullrequestwork.ErrInvalid) || !disposition.Terminal || invalidWork.failCalls != 1 {
		t.Fatalf("invalid disposition=%+v work=%+v err=%v", disposition, invalidWork, err)
	}
	if _, statErr := os.Stat(filepath.Join(invalid.WorktreePath, ".holark", "comment-reply.json")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("invalid artifact was not removed: %v", statErr)
	}
}

func TestInvalidReviewArtifactIsLoggedBeforeRemoval(t *testing.T) {
	const malformed = `{"comments":[{"body":"local source context",]}`
	h := workArtifactHolon(t, pullrequestwork.KindReview, malformed)
	work := &completionWorkStub{work: pullrequestwork.Work{ID: "review-1", SessionID: h.ID, PullRequestID: h.PullRequestID, Kind: pullrequestwork.KindReview, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusRunning, HeadCommit: "head"}}
	logPath := filepath.Join(t.TempDir(), "holark.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := localPullRequestWorkCompletionCoordinator{work: work, logger: log.New(logFile, "", 0)}
	disposition, completionErr := coordinator.Complete(t.Context(), h)
	if closeErr := logFile.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if !errors.Is(completionErr, pullrequestwork.ErrInvalid) || !disposition.Terminal || work.failCalls != 1 {
		t.Fatalf("disposition=%+v work=%+v err=%v", disposition, work, completionErr)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"review-1", h.PullRequestID, ".holark/review.json", "invalid JSON", "artifact_bytes=46"} {
		if !strings.Contains(string(logged), value) {
			t.Fatalf("log does not contain %q: %s", value, logged)
		}
	}
	if strings.Contains(string(logged), "local source context") {
		t.Fatal("diagnostic log exposed artifact content")
	}
	if _, err = os.Stat(filepath.Join(h.WorktreePath, ".holark", "review.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invalid artifact was not removed: %v", err)
	}
}

type artifactRepository struct {
	onInspect    func()
	inspection   holons.WorkspaceInspection
	inspectErr   error
	inspectCalls *int
}

func (r artifactRepository) CreateWorkspace(context.Context, string, string) (holons.Workspace, error) {
	return holons.Workspace{}, nil
}
func (r artifactRepository) InspectWorkspace(context.Context, string) (holons.WorkspaceInspection, error) {
	if r.onInspect != nil {
		r.onInspect()
	}
	if r.inspectCalls != nil {
		(*r.inspectCalls)++
	}
	return r.inspection, r.inspectErr
}
func (r artifactRepository) PublishWorkspace(context.Context, string, holons.Publish) (holons.PublishedWorkspace, error) {
	return holons.PublishedWorkspace{}, nil
}
func (r artifactRepository) RemoveWorkspace(context.Context, string) error { return nil }
func completionHolonService(t *testing.T, h holons.Holon, head string) *holons.Service {
	t.Helper()
	_, store := terminalTestService(t)
	if err := store.Create(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	return holons.NewServiceWithRepository(store, artifactRepository{inspection: holons.WorkspaceInspection{HeadCommit: head, Clean: true}})
}

func TestAssistedPublishFailureRetainsArtifactAndRetries(t *testing.T) {
	h := workArtifactHolon(t, pullrequestwork.KindWorker, `{"reply":"Fixed"}`)
	work := &completionWorkStub{work: pullrequestwork.Work{ID: "work-1", SessionID: h.ID, PullRequestID: h.PullRequestID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAssisted, Status: pullrequestwork.StatusRunning, HeadCommit: "head"}, completeErr: pullrequestwork.ErrPublish}
	coordinator := localPullRequestWorkCompletionCoordinator{holons: completionHolonService(t, h, "new-head"), work: work}
	disposition, err := coordinator.Complete(t.Context(), h)
	if err != nil || disposition.Terminal || work.waitCalls != 1 {
		t.Fatalf("first disposition=%+v work=%+v err=%v", disposition, work, err)
	}
	path := filepath.Join(h.WorktreePath, ".holark", "comment-reply.json")
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("recoverable artifact was removed: %v", err)
	}
	work.completeErr = nil
	disposition, err = coordinator.Complete(t.Context(), h)
	if err != nil || !disposition.Terminal || work.completeCalls != 2 {
		t.Fatalf("retry disposition=%+v work=%+v err=%v", disposition, work, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("successful retry did not consume artifact: %v", err)
	}
}

func TestStaleWorkerAndRebaseUseDifferentTerminalStates(t *testing.T) {
	for _, test := range []struct {
		name string
		kind pullrequestwork.Kind
		data string
	}{
		{name: "worker", kind: pullrequestwork.KindWorker, data: `{"reply":"Fixed"}`},
		{name: "rebase", kind: pullrequestwork.KindRebase, data: `{"reply":"Rebased"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := workArtifactHolon(t, test.kind, test.data)
			work := &completionWorkStub{work: pullrequestwork.Work{ID: "work-1", SessionID: h.ID, PullRequestID: h.PullRequestID, Kind: test.kind, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusRunning, HeadCommit: "head"}, completeErr: pullrequestwork.ErrStaleHead}
			disposition, err := (localPullRequestWorkCompletionCoordinator{holons: completionHolonService(t, h, "new-head"), work: work}).Complete(t.Context(), h)
			if !errors.Is(err, pullrequestwork.ErrStaleHead) || !disposition.Terminal {
				t.Fatalf("disposition=%+v err=%v", disposition, err)
			}
			if test.kind == pullrequestwork.KindWorker && (work.staleCalls != 1 || work.failCalls != 0) {
				t.Fatalf("worker transitions=%+v", work)
			}
			if test.kind == pullrequestwork.KindRebase && (work.failCalls != 1 || work.staleCalls != 0) {
				t.Fatalf("rebase transitions=%+v", work)
			}
		})
	}
}

func TestAutoMissingArtifactFailsTerminally(t *testing.T) {
	h := workArtifactHolon(t, pullrequestwork.KindWorker, "")
	work := &completionWorkStub{work: pullrequestwork.Work{ID: "work-1", SessionID: h.ID, PullRequestID: h.PullRequestID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusRunning, HeadCommit: "head"}}
	disposition, err := (localPullRequestWorkCompletionCoordinator{work: work}).Complete(t.Context(), h)
	if !errors.Is(err, fs.ErrNotExist) || !disposition.Terminal || work.failCalls != 1 || work.waitCalls != 0 {
		t.Fatalf("disposition=%+v work=%+v err=%v", disposition, work, err)
	}
}

func TestWorkerAndRebaseSuccessfulCompletionConsumeTheirArtifacts(t *testing.T) {
	for _, test := range []struct {
		name string
		kind pullrequestwork.Kind
		data string
	}{
		{name: "worker", kind: pullrequestwork.KindWorker, data: `{"reply":"Fixed"}`},
		{name: "rebase", kind: pullrequestwork.KindRebase, data: `{"reply":"Rebased"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := workArtifactHolon(t, test.kind, test.data)
			work := &completionWorkStub{work: pullrequestwork.Work{ID: "work-1", SessionID: h.ID, PullRequestID: h.PullRequestID, Kind: test.kind, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusRunning, HeadCommit: "head"}}
			disposition, err := (localPullRequestWorkCompletionCoordinator{holons: completionHolonService(t, h, "new-head"), work: work}).Complete(t.Context(), h)
			if err != nil || !disposition.Terminal || work.completeCalls != 1 {
				t.Fatalf("disposition=%+v work=%+v err=%v", disposition, work, err)
			}
			if _, err := os.Stat(filepath.Join(h.WorktreePath, filepath.FromSlash(pullRequestWorkArtifactPath(test.kind)))); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("artifact remains: %v", err)
			}
		})
	}
}

func TestPostCompletionReplyFailureStillConsumesArtifact(t *testing.T) {
	h := workArtifactHolon(t, pullrequestwork.KindWorker, `{"reply":"Fixed"}`)
	work := &completionWorkStub{
		work:                 pullrequestwork.Work{ID: "work-1", SessionID: h.ID, PullRequestID: h.PullRequestID, Kind: pullrequestwork.KindWorker, Mode: pullrequestwork.ModeAuto, Status: pullrequestwork.StatusRunning, HeadCommit: "head"},
		completeErr:          errors.New("reply unavailable"),
		completeDespiteError: true,
	}
	disposition, err := (localPullRequestWorkCompletionCoordinator{holons: completionHolonService(t, h, "new-head"), work: work}).Complete(t.Context(), h)
	if err == nil || !disposition.Terminal || work.failCalls != 0 || work.work.Status != pullrequestwork.StatusCompleted {
		t.Fatalf("disposition=%+v work=%+v err=%v", disposition, work, err)
	}
	if _, statErr := os.Stat(filepath.Join(h.WorktreePath, ".holark", "comment-reply.json")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("durably completed artifact remains: %v", statErr)
	}
}
