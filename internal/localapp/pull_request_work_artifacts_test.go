package localapp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestwork"
)

func TestReadPullRequestWorkArtifactValidatesOwnershipAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".holark"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "artifact.json")
	if err := os.WriteFile(outside, []byte(`{"pull_request_id":"pr-1","head_commit":"head","comments":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".holark", "review.json")
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	h := holons.Holon{WorktreePath: root, PullRequestID: "pr-1"}
	if _, err := readPullRequestWorkArtifact(h, pullrequestwork.KindReview); err == nil {
		t.Fatal("symlink artifact accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"pull_request_id":"wrong","head_commit":"head","comments":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPullRequestWorkArtifact(h, pullrequestwork.KindReview); err == nil {
		t.Fatal("wrong owner accepted")
	}
}

func TestReadPullRequestWorkArtifactAcceptsEditableTemplateSchemas(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".holark"), 0o700); err != nil {
		t.Fatal(err)
	}
	holon := holons.Holon{WorktreePath: root, PullRequestID: "pr-1"}
	work := pullrequestwork.Work{PullRequestID: "pr-1", HeadCommit: "head"}

	if err := os.WriteFile(filepath.Join(root, ".holark", "review.json"), []byte(`{"findings":["Legacy general"],"comments":[{"body":"General"},{"body":"File","scope":"file","path":"internal/example.go"},{"body":"Removed","scope":"line","path":"internal/example.go","side":"LEFT","line":3},{"body":"Added","scope":"line","path":"internal/example.go","side":"RIGHT","line":4}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	review, err := readPullRequestWorkArtifact(holon, pullrequestwork.KindReview, work)
	left, right := 3, 4
	wantComments := []pullrequestwork.ReviewComment{
		{Body: "Legacy general", Scope: "pull_request"},
		{Body: "General", Scope: "pull_request"},
		{Body: "File", Scope: "file", Path: "internal/example.go"},
		{Body: "Removed", Scope: "line", Path: "internal/example.go", Side: "LEFT", Line: &left},
		{Body: "Added", Scope: "line", Path: "internal/example.go", Side: "RIGHT", Line: &right},
	}
	if err != nil || review.PullRequestID != "pr-1" || review.HeadCommit != "head" || !reflect.DeepEqual(review.Comments, wantComments) {
		t.Fatalf("review=%+v err=%v", review, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".holark", "review.json"), []byte(`{"comments":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyReview, err := readPullRequestWorkArtifact(holon, pullrequestwork.KindReview, work)
	if err != nil || len(emptyReview.Comments) != 0 || emptyReview.PullRequestID != "pr-1" || emptyReview.HeadCommit != "head" {
		t.Fatalf("empty review=%+v err=%v", emptyReview, err)
	}

	if err := os.WriteFile(filepath.Join(root, ".holark", "comment-reply.json"), []byte(`{"reply":"Fixed it"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	worker, err := readPullRequestWorkArtifact(holon, pullrequestwork.KindWorker, work)
	if err != nil || worker.ReplyBody != "Fixed it" || worker.PullRequestID != "pr-1" || worker.HeadCommit != "head" {
		t.Fatalf("worker=%+v err=%v", worker, err)
	}

	if err := os.WriteFile(filepath.Join(root, ".holark", "rebase_complete"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rebase, err := readPullRequestWorkArtifact(holon, pullrequestwork.KindRebase, work)
	if err != nil || rebase.ReplyBody != "" || rebase.ResultHeadCommit != "" {
		t.Fatalf("rebase=%+v err=%v", rebase, err)
	}
}

func TestReadPullRequestWorkArtifactEnforcesLimitsAndSingleJSONValue(t *testing.T) {
	tests := []struct {
		name string
		kind pullrequestwork.Kind
		data string
	}{
		{name: "oversized reply", kind: pullrequestwork.KindWorker, data: fmt.Sprintf(`{"reply":%q}`, strings.Repeat("r", maxPullRequestWorkTextCharacters+1))},
		{name: "empty worker reply", kind: pullrequestwork.KindWorker, data: `{"reply":"  "}`},
		{name: "trailing JSON", kind: pullrequestwork.KindReview, data: `{"comments":[]} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".holark"), 0o700); err != nil {
				t.Fatal(err)
			}
			h := holons.Holon{WorktreePath: root, PullRequestID: "pr-1"}
			w := pullrequestwork.Work{PullRequestID: "pr-1", HeadCommit: "head"}
			path := filepath.Join(root, filepath.FromSlash(pullRequestWorkArtifactPath(test.kind)))
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readPullRequestWorkArtifact(h, test.kind, w); !errors.Is(err, pullrequestwork.ErrInvalid) {
				t.Fatalf("error=%v, want invalid", err)
			}
		})
	}
}

func TestReadPullRequestWorkArtifactFiltersTruncatesAndLimitsReviewComments(t *testing.T) {
	comments := []string{
		`{"body":" "}`,
		`{"body":"bad location","scope":"line","path":"file.go","side":"RIGHT"}`,
		fmt.Sprintf(`{"body":%q}`, strings.Repeat("é", maxPullRequestWorkTextCharacters+1)),
	}
	for index := range maxPullRequestReviewComments {
		comments = append(comments, fmt.Sprintf(`{"body":"comment %d"}`, index))
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".holark"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".holark", "review.json"), []byte(`{"comments":[`+strings.Join(comments, ",")+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	h := holons.Holon{WorktreePath: root, PullRequestID: "pr-1"}
	review, err := readPullRequestWorkArtifact(h, pullrequestwork.KindReview, pullrequestwork.Work{HeadCommit: "head"})
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Comments) != maxPullRequestReviewComments {
		t.Fatalf("comments=%d, want %d", len(review.Comments), maxPullRequestReviewComments)
	}
	if utf8.RuneCountInString(review.Comments[0].Body) != maxPullRequestWorkTextCharacters {
		t.Fatalf("truncated comment length=%d", utf8.RuneCountInString(review.Comments[0].Body))
	}
	if review.Comments[len(review.Comments)-1].Body != "comment 28" {
		t.Fatalf("last comment=%q", review.Comments[len(review.Comments)-1].Body)
	}
}
