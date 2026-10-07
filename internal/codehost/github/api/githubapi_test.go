package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRepositoryURL(t *testing.T) {
	for _, test := range []struct {
		value string
		owner string
		name  string
	}{
		{value: "git@github.com:owner/repo.git", owner: "owner", name: "repo"},
		{value: "https://github.com/owner/repo.git", owner: "owner", name: "repo"},
		{value: "ssh://git@github.com/owner/repo.git", owner: "owner", name: "repo"},
	} {
		repository, err := ParseRepositoryURL(test.value)
		if err != nil {
			t.Fatalf("ParseRepositoryURL(%q): %v", test.value, err)
		}
		if repository.Owner != test.owner || repository.Name != test.name {
			t.Fatalf("ParseRepositoryURL(%q) = %+v", test.value, repository)
		}
	}
	if _, err := ParseRepositoryURL("git@example.com:owner/repo.git"); err != ErrUnsupportedRepository {
		t.Fatalf("unsupported repository error = %v", err)
	}
}

func TestCLIClientCurrentUserReturnsGitHubProfile(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh-args.log")
	t.Setenv("GH_ARGS_LOG", logPath)
	command := fakeGH(t, `#!/bin/sh
printf "%s\n" "$*" >> "$GH_ARGS_LOG"
case "$*" in
"api user")
  printf "%s\n" "{\"node_id\":\"U_kw\",\"login\":\"mona\",\"name\":\"Mona Lisa\",\"avatar_url\":\"https://avatars.githubusercontent.com/u/1?v=4\",\"html_url\":\"https://github.com/mona\",\"type\":\"User\",\"site_admin\":true}"
  ;;
*)
  echo "unexpected args: $*" >&2
  exit 1
  ;;
esac
`)
	client := &CLIClient{Command: command}

	user, err := client.CurrentUser(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if user.NodeID != "U_kw" || user.Login != "mona" || user.Name != "Mona Lisa" || user.AvatarURL != "https://avatars.githubusercontent.com/u/1?v=4" || user.HTMLURL != "https://github.com/mona" {
		t.Fatalf("current user = %+v", user)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(logBytes)), "api user"; got != want {
		t.Fatalf("gh args = %q, want %q", got, want)
	}
}

func TestCLIClientCurrentUserWrapsDecodeErrors(t *testing.T) {
	t.Setenv("GH_STDOUT", "{bad")
	command := fakeGH(t, `#!/bin/sh
printf "%s" "$GH_STDOUT"
`)
	client := &CLIClient{Command: command}

	_, err := client.CurrentUser(t.Context())
	var githubErr *Error
	if !errors.As(err, &githubErr) || githubErr.Code != ErrorCodeSyncFailed {
		t.Fatalf("error = %v", err)
	}
}

func TestCLIClientListsEveryCollaboratorPage(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh-args.log")
	t.Setenv("GH_ARGS_LOG", logPath)
	command := fakeGH(t, `#!/bin/sh
printf '%s\n' "$*" >> "$GH_ARGS_LOG"
case "$*" in
"api --paginate /repos/owner/repo/collaborators?affiliation=all&per_page=100")
  printf '%s\n' '[{"node_id":"U_one","login":"Alpha","avatar_url":"https://avatars/one","html_url":"https://github.com/Alpha","role_name":"custom-reviewer"}]'
  printf '%s\n' '[{"node_id":"U_two","login":"beta","avatar_url":"https://avatars/two","html_url":"https://github.com/beta","role_name":"maintain"}]'
  ;;
*) exit 9 ;;
esac
`)
	client := &CLIClient{Command: command}
	members, err := client.ListCollaborators(t.Context(), Repository{Owner: "owner", Name: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].NodeID != "U_one" || members[0].Login != "Alpha" || members[0].AvatarURL != "https://avatars/one" || members[0].HTMLURL != "https://github.com/Alpha" || members[0].Permission != "custom-reviewer" || members[1].NodeID != "U_two" {
		t.Fatalf("collaborators = %#v", members)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(logged)), "api --paginate /repos/owner/repo/collaborators?affiliation=all&per_page=100"; got != want {
		t.Fatalf("gh args = %q, want %q", got, want)
	}
}

func TestCLIClientUsesGitHubAPIEndpoints(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh-args.log")
	inputPath := filepath.Join(t.TempDir(), "gh-input.json")
	t.Setenv("GH_ARGS_LOG", logPath)
	t.Setenv("GH_INPUT_LOG", inputPath)
	command := fakeGH(t, `#!/bin/sh
printf '%s\n' "$*" >> "$GH_ARGS_LOG"
case "$*" in
"api --include --method POST /repos/owner/repo/pulls --input -")
  cat > "$GH_INPUT_LOG"
  printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'
  printf '%s\n' '{"number":13,"node_id":"PR_kw","title":"Create","body":"Created body","html_url":"https://github.com/owner/repo/pull/13","state":"open","draft":false,"created_at":"2026-01-03T00:00:00Z","updated_at":"2026-01-03T00:01:00Z","base":{"ref":"main","sha":"base"},"head":{"ref":"holark/session-1","sha":"head"},"user":{"node_id":"U_created","login":"mona"}}'
  ;;
"api --paginate /repos/owner/repo/pulls?state=all&per_page=100")
  printf '%s\n' '[{"number":12,"title":"Ship","body":"Body","html_url":"https://github.com/owner/repo/pull/12","state":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","base":{"ref":"main","sha":"base"},"head":{"ref":"feature","sha":"head"},"user":{"node_id":"U_listed","login":"hubot"}}]'
  ;;
*)
  echo "unexpected args: $*" >&2
  exit 1
  ;;
esac
`)
	client := &CLIClient{Command: command}
	repository := Repository{Owner: "owner", Name: "repo"}

	created, err := client.CreatePullRequest(t.Context(), repository, CreatePullRequestRequest{
		Title: "Create", Body: "Created body", Head: "holark/session-1", Base: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Number != 13 || created.Head.Ref != "holark/session-1" {
		t.Fatalf("created pull request = %+v", created)
	}
	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inputBytes), `"title":"Create"`) || !strings.Contains(string(inputBytes), `"head":"holark/session-1"`) || !strings.Contains(string(inputBytes), `"base":"main"`) {
		t.Fatalf("create input = %s", inputBytes)
	}

	pullRequests, err := client.ListPullRequests(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(pullRequests) != 1 || pullRequests[0].Number != 12 || pullRequests[0].Base.SHA != "base" || pullRequests[0].Head.SHA != "head" {
		t.Fatalf("pull requests = %+v", pullRequests)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	want := []string{
		"api --include --method POST /repos/owner/repo/pulls --input -",
		"api --paginate /repos/owner/repo/pulls?state=all&per_page=100",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("gh args = %#v, want %#v", got, want)
	}
}

func TestCLIClientReadsSinglePullRequest(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh-args.log")
	t.Setenv("GH_ARGS_LOG", logPath)
	command := fakeGH(t, `#!/bin/sh
printf '%s\n' "$*" >> "$GH_ARGS_LOG"
case "$*" in
"api /repos/owner/repo/pulls/12")
  printf '%s\n' '{"number":12,"node_id":"PR_kw","title":"Ship","body":"Body","html_url":"https://github.com/owner/repo/pull/12","state":"closed","merged_at":"2026-01-03T00:00:00Z","merge_commit_sha":"merged","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-03T00:00:00Z","base":{"ref":"main","sha":"base"},"head":{"ref":"feature","sha":"head"},"user":{"node_id":"U_author","login":"hubot"}}'
  ;;
*) exit 9 ;;
esac
`)
	client := &CLIClient{Command: command}

	pullRequest, err := client.PullRequest(t.Context(), Repository{Owner: "owner", Name: "repo"}, 12)
	if err != nil {
		t.Fatal(err)
	}
	if pullRequest.Number != 12 || pullRequest.State != "closed" || pullRequest.MergedAt == nil || pullRequest.MergeCommitSHA != "merged" || pullRequest.Base.SHA != "base" || pullRequest.Head.SHA != "head" {
		t.Fatalf("pull request = %+v", pullRequest)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(logged)), "api /repos/owner/repo/pulls/12"; got != want {
		t.Fatalf("gh args = %q, want %q", got, want)
	}
}

func TestCLIClientUsesGitHubIssueAPIEndpoints(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh-args.log")
	inputPath := filepath.Join(t.TempDir(), "gh-input.json")
	t.Setenv("GH_ARGS_LOG", logPath)
	t.Setenv("GH_INPUT_LOG", inputPath)
	command := fakeGH(t, `#!/bin/sh
printf '%s\n' "$*" >> "$GH_ARGS_LOG"
case "$*" in
"api --method POST /repos/owner/repo/issues --input -")
  cat > "$GH_INPUT_LOG"
  printf '%s\n' '{"number":13,"node_id":"I_kw","title":"Create","body":"Created body","html_url":"https://github.com/owner/repo/issues/13","state":"open","created_at":"2026-01-03T00:00:00Z","updated_at":"2026-01-03T00:01:00Z","user":{"node_id":"U_created","login":"mona"}}'
  ;;
"api --method PATCH /repos/owner/repo/issues/13 --input -")
  cat > "$GH_INPUT_LOG"
  printf '%s\n' '{"number":13,"node_id":"I_kw","title":"Updated","body":"Updated body","html_url":"https://github.com/owner/repo/issues/13","state":"closed","created_at":"2026-01-03T00:00:00Z","updated_at":"2026-01-04T00:00:00Z","closed_at":"2026-01-04T00:00:00Z","user":{"node_id":"U_updated","login":"mona"}}'
  ;;
"api --paginate /repos/owner/repo/issues?state=all&per_page=100")
  printf '%s\n' '[{"number":12,"title":"Issue","body":"Body","html_url":"https://github.com/owner/repo/issues/12","state":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","user":{"node_id":"U_listed","login":"hubot"}},{"number":14,"title":"PR shaped","pull_request":{},"state":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}]'
  ;;
*)
  echo "unexpected args: $*" >&2
  exit 1
  ;;
esac
`)
	client := &CLIClient{Command: command}
	repository := Repository{Owner: "owner", Name: "repo"}

	created, err := client.CreateIssue(t.Context(), repository, CreateIssueRequest{Title: "Create", Body: "Created body"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Number != 13 || created.NodeID != "I_kw" {
		t.Fatalf("created issue = %+v", created)
	}
	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inputBytes), `"title":"Create"`) || !strings.Contains(string(inputBytes), `"body":"Created body"`) {
		t.Fatalf("create input = %s", inputBytes)
	}

	updateTitle := "Updated"
	updateBody := "Updated body"
	updateState := "closed"
	updated, err := client.UpdateIssue(t.Context(), repository, 13, UpdateIssueRequest{Title: &updateTitle, Body: &updateBody, State: &updateState})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Number != 13 || updated.State != "closed" || updated.ClosedAt == nil {
		t.Fatalf("updated issue = %+v", updated)
	}
	inputBytes, err = os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inputBytes), `"title":"Updated"`) || !strings.Contains(string(inputBytes), `"body":"Updated body"`) || !strings.Contains(string(inputBytes), `"state":"closed"`) {
		t.Fatalf("update input = %s", inputBytes)
	}

	issues, err := client.ListIssues(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 12 || issues[0].Title != "Issue" {
		t.Fatalf("issues = %+v", issues)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	want := []string{
		"api --method POST /repos/owner/repo/issues --input -",
		"api --method PATCH /repos/owner/repo/issues/13 --input -",
		"api --paginate /repos/owner/repo/issues?state=all&per_page=100",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("gh args = %#v, want %#v", got, want)
	}
}

func TestCLIClientClassifiesMalformedIssueMutationResponsesAsAccepted(t *testing.T) {
	command := fakeGH(t, `#!/bin/sh
cat >/dev/null
printf '%s' '{bad'
`)
	client := &CLIClient{Command: command}
	repository := Repository{Owner: "owner", Name: "repo"}
	title := "Updated"

	for _, test := range []struct {
		name   string
		mutate func() error
	}{
		{name: "create", mutate: func() error {
			_, err := client.CreateIssue(t.Context(), repository, CreateIssueRequest{Title: "Created"})
			return err
		}},
		{name: "update", mutate: func() error {
			_, err := client.UpdateIssue(t.Context(), repository, 12, UpdateIssueRequest{Title: &title})
			return err
		}},
		{name: "replace assignees", mutate: func() error {
			_, err := client.ReplaceIssueAssignees(t.Context(), repository, 12, []string{})
			return err
		}},
		{name: "add assignee", mutate: func() error {
			_, err := client.AddIssueAssignee(t.Context(), repository, 12, "mona")
			return err
		}},
		{name: "remove assignee", mutate: func() error {
			_, err := client.RemoveIssueAssignee(t.Context(), repository, 12, "mona")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.mutate()
			var githubErr *Error
			if !errors.As(err, &githubErr) || githubErr.Code != ErrorCodeMutationAccepted {
				t.Fatalf("error = %v, want mutation-accepted classification", err)
			}
			var syntaxErr *json.SyntaxError
			if !errors.As(err, &syntaxErr) {
				t.Fatalf("error = %v, want preserved JSON decoding cause", err)
			}
		})
	}
}

func TestCLIClientUsesGitHubMergePullRequestEndpoint(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh-args.log")
	inputPath := filepath.Join(t.TempDir(), "gh-input.json")
	t.Setenv("GH_ARGS_LOG", logPath)
	t.Setenv("GH_INPUT_LOG", inputPath)
	command := fakeGH(t, `#!/bin/sh
printf '%s\n' "$*" >> "$GH_ARGS_LOG"
case "$*" in
"api --include --method PUT /repos/owner/repo/pulls/12/merge --input -")
  cat > "$GH_INPUT_LOG"
  printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'
  printf '%s\n' '{"sha":"abc123","merged":true,"message":"Pull Request successfully merged"}'
  ;;
*)
  echo "unexpected args: $*" >&2
  exit 1
  ;;
esac
`)
	client := &CLIClient{Command: command}
	response, err := client.MergePullRequest(t.Context(), Repository{Owner: "owner", Name: "repo"}, 12, MergePullRequestRequest{
		CommitTitle: "Title", CommitMessage: "Body\n\n/projects/holark/pulls/pr-1", MergeMethod: "squash", ExpectedHeadSHA: "def456",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Merged || response.SHA != "abc123" {
		t.Fatalf("merge response = %+v", response)
	}
	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	input := string(inputBytes)
	if !strings.Contains(input, `"commit_title":"Title"`) || !strings.Contains(input, `"commit_message":"Body\n\n/projects/holark/pulls/pr-1"`) ||
		!strings.Contains(input, `"merge_method":"squash"`) || !strings.Contains(input, `"sha":"def456"`) {
		t.Fatalf("merge input = %s", inputBytes)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(logBytes)), "api --include --method PUT /repos/owner/repo/pulls/12/merge --input -"; got != want {
		t.Fatalf("gh args = %q, want %q", got, want)
	}
}

func TestCLIClientReadsPullRequestReadiness(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh-args.log")
	t.Setenv("GH_ARGS_LOG", logPath)
	command := fakeGH(t, `#!/bin/sh
printf '%s\n' "$*" >> "$GH_ARGS_LOG"
printf '%s\n' '{"headRefOid":"head123","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","statusCheckRollup":[{"__typename":"CheckRun","name":"test","bucket":"pass","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"StatusContext","context":"lint","state":"SUCCESS"}],"url":"https://github.com/owner/repo/pull/12"}'
`)
	client := &CLIClient{Command: command}

	readiness, err := client.PullRequestReadiness(t.Context(), Repository{Owner: "owner", Name: "repo"}, 12)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.HeadCommit != "head123" || readiness.ChecksState != ChecksPassing || readiness.MergeabilityState != MergeabilityMergeable || readiness.DetailsURL != "https://github.com/owner/repo/pull/12" {
		t.Fatalf("readiness = %+v", readiness)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "pr view 12 --repo owner/repo --json headRefOid,mergeable,mergeStateStatus,statusCheckRollup,url"
	if got := strings.TrimSpace(string(logged)); got != want {
		t.Fatalf("gh args = %q, want %q", got, want)
	}
}

func TestCLIClientMapsPullRequestReadinessStates(t *testing.T) {
	for _, test := range []struct {
		name             string
		mergeable        string
		mergeStateStatus string
		rollup           string
		wantChecks       ChecksState
		wantMergeability MergeabilityState
	}{
		{name: "pending checks block", mergeable: "MERGEABLE", mergeStateStatus: "BLOCKED", rollup: `[{"bucket":"pending","state":"PENDING"}]`, wantChecks: ChecksPending, wantMergeability: MergeabilityBlocked},
		{name: "failed check", mergeable: "MERGEABLE", mergeStateStatus: "UNSTABLE", rollup: `[{"bucket":"fail","conclusion":"FAILURE"}]`, wantChecks: ChecksFailing, wantMergeability: MergeabilityBlocked},
		{name: "errored status", mergeable: "MERGEABLE", mergeStateStatus: "CLEAN", rollup: `[{"state":"ERROR"}]`, wantChecks: ChecksError, wantMergeability: MergeabilityMergeable},
		{name: "conflict", mergeable: "CONFLICTING", mergeStateStatus: "DIRTY", rollup: `[{"bucket":"pass"}]`, wantChecks: ChecksPassing, wantMergeability: MergeabilityConflicting},
		{name: "mixed passing and unknown checks", mergeable: "MERGEABLE", mergeStateStatus: "CLEAN", rollup: `[{"conclusion":"SUCCESS"},{"status":"FUTURE_STATUS"}]`, wantChecks: ChecksUnknown, wantMergeability: MergeabilityMergeable},
		{name: "unknown", mergeable: "UNKNOWN", mergeStateStatus: "UNKNOWN", rollup: `[]`, wantChecks: ChecksUnknown, wantMergeability: MergeabilityUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GH_MERGEABLE", test.mergeable)
			t.Setenv("GH_MERGE_STATE", test.mergeStateStatus)
			t.Setenv("GH_ROLLUP", test.rollup)
			command := fakeGH(t, `#!/bin/sh
printf '{"headRefOid":"head","mergeable":"%s","mergeStateStatus":"%s","statusCheckRollup":%s,"url":"url"}\n' "$GH_MERGEABLE" "$GH_MERGE_STATE" "$GH_ROLLUP"
`)
			readiness, err := (&CLIClient{Command: command}).PullRequestReadiness(t.Context(), Repository{Owner: "owner", Name: "repo"}, 12)
			if err != nil {
				t.Fatal(err)
			}
			if readiness.ChecksState != test.wantChecks || readiness.MergeabilityState != test.wantMergeability {
				t.Fatalf("readiness = %+v, want checks=%q mergeability=%q", readiness, test.wantChecks, test.wantMergeability)
			}
		})
	}
}

func TestCLIClientUsesGitHubPullRequestMetadataEndpoint(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh-args.log")
	inputPath := filepath.Join(t.TempDir(), "gh-input.json")
	t.Setenv("GH_ARGS_LOG", logPath)
	t.Setenv("GH_INPUT_LOG", inputPath)
	command := fakeGH(t, `#!/bin/sh
printf '%s\n' "$*" >> "$GH_ARGS_LOG"
case "$*" in
"api --include --method PATCH /repos/owner/repo/pulls/12 --input -")
  cat > "$GH_INPUT_LOG"
  printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'
  printf '%s\n' '{"number":12,"title":"Updated title","body":"Updated body"}'
  ;;
*)
  echo "unexpected args: $*" >&2
  exit 1
  ;;
esac
`)
	client := &CLIClient{Command: command}

	updated, err := client.UpdatePullRequest(t.Context(), Repository{Owner: "owner", Name: "repo"}, 12, UpdatePullRequestRequest{
		Title: "Updated title", Body: "Updated body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Number != 12 || updated.Title != "Updated title" || updated.Body != "Updated body" {
		t.Fatalf("updated pull request = %+v", updated)
	}
	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(inputBytes)), `{"title":"Updated title","body":"Updated body"}`; got != want {
		t.Fatalf("update input = %q, want %q", got, want)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(logBytes)), "api --include --method PATCH /repos/owner/repo/pulls/12 --input -"; got != want {
		t.Fatalf("gh args = %q, want %q", got, want)
	}
}

func TestCLIClientUsesGitHubPullRequestTransitionEndpoints(t *testing.T) {
	for _, mutation := range []string{"markPullRequestReadyForReview", "convertPullRequestToDraft"} {
		t.Run(mutation, func(t *testing.T) {
			inputPath := filepath.Join(t.TempDir(), "input.json")
			t.Setenv("GH_INPUT_LOG", inputPath)
			t.Setenv("GH_MUTATION", mutation)
			client := &CLIClient{Command: fakeGH(t, `#!/bin/sh
[ "$*" = "api --include --method POST graphql --input -" ] || exit 1
cat > "$GH_INPUT_LOG"
printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'
printf '{"data":{"%s":{"pullRequest":{"id":"PR_kw","number":12,"title":"PR","state":"OPEN","headRefOid":"head","baseRefOid":"base"}}}}' "$GH_MUTATION"
`)}
			var result PullRequest
			var err error
			if mutation == "markPullRequestReadyForReview" {
				result, err = client.MarkPullRequestReadyForReview(t.Context(), "PR_kw")
			} else {
				result, err = client.ConvertPullRequestToDraft(t.Context(), "PR_kw")
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.NodeID != "PR_kw" || result.Number != 12 {
				t.Fatalf("result=%+v", result)
			}
			raw, err := os.ReadFile(inputPath)
			if err != nil {
				t.Fatal(err)
			}
			var input struct {
				Query     string            `json:"query"`
				Variables map[string]string `json:"variables"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				t.Fatal(err)
			}
			if input.Variables["pullRequestId"] != "PR_kw" || !strings.Contains(input.Query, mutation+"(input:{pullRequestId:$pullRequestId})") || !strings.Contains(input.Query, "headRefOid") {
				t.Fatalf("mutation input=%s", raw)
			}
		})
	}
}

func TestCLIClientUpdatesGitHubPullRequestState(t *testing.T) {
	for _, state := range []string{"open", "closed"} {
		t.Run(state, func(t *testing.T) {
			inputPath := filepath.Join(t.TempDir(), "gh-input.json")
			t.Setenv("GH_INPUT_LOG", inputPath)
			t.Setenv("GH_RESPONSE_STATE", state)
			command := fakeGH(t, `#!/bin/sh
case "$*" in
"api --include --method PATCH /repos/owner/repo/pulls/12 --input -")
  cat > "$GH_INPUT_LOG"
  printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'
  printf '{"number":12,"node_id":"PR_kw","title":"PR","body":"Body","state":"%s","draft":true,"created_at":"2026-01-03T00:00:00Z","updated_at":"2026-01-04T00:00:00Z","base":{"ref":"main","sha":"base"},"head":{"ref":"feature","sha":"head"}}\n' "$GH_RESPONSE_STATE"
  ;;
*)
  echo "unexpected args: $*" >&2
  exit 1
  ;;
esac
`)
			client := &CLIClient{Command: command}
			updated, err := client.UpdatePullRequestState(t.Context(), Repository{Owner: "owner", Name: "repo"}, 12, state)
			if err != nil {
				t.Fatal(err)
			}
			if updated.State != state || updated.Number != 12 {
				t.Fatalf("updated pull request = %+v", updated)
			}
			input, err := os.ReadFile(inputPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(input)) != `{"state":"`+state+`"}` {
				t.Fatalf("state input = %s", input)
			}
		})
	}
}

func TestCLIClientHandlesPaginatedJSONEdgeCases(t *testing.T) {
	for _, test := range []struct {
		name       string
		stdout     string
		wantLength int
		wantError  bool
	}{
		{name: "invalid JSON", stdout: "[{bad", wantError: true},
		{name: "empty output", stdout: "", wantLength: 0},
		{name: "non-array JSON", stdout: `{"message":"unexpected shape"}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GH_STDOUT", test.stdout)
			command := fakeGH(t, `#!/bin/sh
printf '%s' "$GH_STDOUT"
`)
			client := &CLIClient{Command: command}
			pullRequests, err := client.ListPullRequests(t.Context(), Repository{Owner: "owner", Name: "repo"})
			if test.wantError {
				var githubErr *Error
				if !errors.As(err, &githubErr) || githubErr.Code != ErrorCodeSyncFailed {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(pullRequests) != test.wantLength {
				t.Fatalf("pull requests length = %d, want %d", len(pullRequests), test.wantLength)
			}
		})
	}
}

func TestCLIClientReturnsGitHubCLIErrors(t *testing.T) {
	command := fakeGH(t, `#!/bin/sh
echo 'auth failed' >&2
exit 2
`)
	client := &CLIClient{Command: command}
	_, err := client.ListPullRequests(t.Context(), Repository{Owner: "owner", Name: "repo"})
	var githubErr *Error
	if !errors.As(err, &githubErr) || githubErr.Code != ErrorCodeSyncFailed || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestCLIClientClassifiesMissingExecutableAsUnavailable(t *testing.T) {
	client := &CLIClient{Command: filepath.Join(t.TempDir(), "missing-gh")}
	_, err := client.CurrentUser(t.Context())
	var githubErr *Error
	if !errors.As(err, &githubErr) || githubErr.Code != ErrorCodeGHUnavailable {
		t.Fatalf("error = %v", err)
	}
}

func fakeGH(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIClientUsesGitHubLabelEndpointsAndClassifiesAcceptedMutations(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh-args.log")
	inputPath := filepath.Join(t.TempDir(), "gh-input.log")
	t.Setenv("GH_ARGS_LOG", logPath)
	t.Setenv("GH_INPUT_LOG", inputPath)
	command := fakeGH(t, `#!/bin/sh
printf '%s\n' "$*" >> "$GH_ARGS_LOG"
case "$*" in
"api --paginate /repos/owner/repo/labels?per_page=100")
  printf '%s\n' '[{"id":1,"node_id":"LA_one","name":"bug","color":"d73a4a","description":"Broken"}]'
  printf '%s\n' '[{"id":2,"node_id":"LA_two","name":"docs","color":"0075ca","description":"Documentation"}]'
  ;;
"api --method POST /repos/owner/repo/labels --input -")
  cat >> "$GH_INPUT_LOG"
  printf '\n' >> "$GH_INPUT_LOG"
  printf '%s\n' '{"id":3,"node_id":"LA_three","name":"priority: high","color":"b60205","description":"Urgent"}'
  ;;
"api --paginate /repos/owner/repo/issues/7/labels?per_page=100")
  printf '%s\n' '[{"id":1,"node_id":"LA_one","name":"bug","color":"d73a4a","description":"Broken"}]'
  printf '%s\n' '[]'
  ;;
"api --method POST /repos/owner/repo/issues/7/labels --input -")
  cat >> "$GH_INPUT_LOG"
  printf '\n' >> "$GH_INPUT_LOG"
  printf '%s\n' '[{"id":1,"node_id":"LA_one","name":"bug","color":"d73a4a","description":"Broken"},{"id":2,"node_id":"LA_two","name":"docs","color":"0075ca","description":"Documentation"}]'
  ;;
"api --method PUT /repos/owner/repo/issues/7/labels --input -")
  cat >> "$GH_INPUT_LOG"
  printf '\n' >> "$GH_INPUT_LOG"
  printf '%s\n' '[{"id":2,"node_id":"LA_two","name":"docs","color":"0075ca","description":"Documentation"}]'
  ;;
*) echo "unexpected args: $*" >&2; exit 9 ;;
esac
`)
	client := &CLIClient{Command: command}
	repository := Repository{Owner: "owner", Name: "repo"}

	catalog, err := client.ListLabels(t.Context(), repository)
	if err != nil || len(catalog) != 2 || catalog[1].NodeID != "LA_two" {
		t.Fatalf("catalog = %#v, err = %v", catalog, err)
	}
	created, err := client.CreateLabel(t.Context(), repository, CreateLabelRequest{Name: "priority: high", Color: "b60205", Description: "Urgent"})
	if err != nil || created.NodeID != "LA_three" {
		t.Fatalf("created = %#v, err = %v", created, err)
	}
	current, err := client.ListIssueLabels(t.Context(), repository, 7)
	if err != nil || len(current) != 1 || current[0].Name != "bug" {
		t.Fatalf("current = %#v, err = %v", current, err)
	}
	added, err := client.AddIssueLabels(t.Context(), repository, 7, []string{"bug", "docs"})
	if err != nil || len(added) != 2 {
		t.Fatalf("added = %#v, err = %v", added, err)
	}
	remaining, err := client.SetIssueLabels(t.Context(), repository, 7, []string{"docs"})
	if err != nil || len(remaining) != 1 || remaining[0].Name != "docs" {
		t.Fatalf("remaining = %#v, err = %v", remaining, err)
	}

	inputs, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	wantInputs := "{\"name\":\"priority: high\",\"color\":\"b60205\",\"description\":\"Urgent\"}\n" +
		"{\"labels\":[\"bug\",\"docs\"]}\n{\"labels\":[\"docs\"]}"
	if got := strings.TrimSpace(string(inputs)); got != wantInputs {
		t.Fatalf("inputs = %q, want %q", got, wantInputs)
	}

	bad := fakeGH(t, `#!/bin/sh
printf '%s' '{bad'
`)
	badClient := &CLIClient{Command: bad}
	for _, invoke := range []func() error{
		func() error {
			_, err := badClient.CreateLabel(t.Context(), repository, CreateLabelRequest{Name: "x", Color: "abcdef"})
			return err
		},
		func() error {
			_, err := badClient.AddIssueLabels(t.Context(), repository, 7, []string{"x"})
			return err
		},
		func() error { _, err := badClient.SetIssueLabels(t.Context(), repository, 7, nil); return err },
	} {
		var githubErr *Error
		if err := invoke(); !errors.As(err, &githubErr) || githubErr.Code != ErrorCodeMutationAccepted {
			t.Fatalf("mutation decode error = %v, want %s", err, ErrorCodeMutationAccepted)
		}
	}
}

func TestCLIClientListsPaginatedOpenIssuesAndReadsClosures(t *testing.T) {
	command := fakeGH(t, `#!/bin/sh
case "$*" in
"api --paginate /repos/owner/repo/issues?state=open&per_page=100")
 printf '%s\n' '[{"number":1,"state":"open"},{"number":2,"pull_request":{}}]' '[{"number":3,"state":"open"}]'
 ;;
"api /repos/owner/repo/issues/1")
 printf '%s\n' '{"number":1,"state":"closed"}'
 ;;
"api /repos/owner/repo/issues/2")
 printf '%s\n' '{"number":2,"pull_request":{}}'
 ;;
"api /repos/owner/repo/issues/3")
 echo 'not found' >&2; exit 1
 ;;
*) echo "unexpected args: $*" >&2; exit 1;;
esac
`)
	client := &CLIClient{Command: command}
	repository := Repository{Owner: "owner", Name: "repo"}
	items, err := client.ListOpenIssues(t.Context(), repository)
	if err != nil || len(items) != 2 || items[0].Number != 1 || items[1].Number != 3 {
		t.Fatalf("open issues=%+v error=%v", items, err)
	}
	item, err := client.GetIssue(t.Context(), repository, 1)
	if err != nil || item.State != "closed" {
		t.Fatalf("closure=%+v error=%v", item, err)
	}
	for _, number := range []int{2, 3} {
		if _, err := client.GetIssue(t.Context(), repository, number); err == nil {
			t.Fatalf("invalid issue %d accepted", number)
		}
	}
}
