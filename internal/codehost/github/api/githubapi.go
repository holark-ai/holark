package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var ErrUnsupportedRepository = errors.New("repository is not a github.com repository")

type ErrorCode string

const (
	ErrorCodeGHUnavailable    ErrorCode = "gh_unavailable"
	ErrorCodeSyncFailed       ErrorCode = "github_sync_failed"
	ErrorCodeMergeBlocked     ErrorCode = "github_merge_blocked"
	ErrorCodeMutationAccepted ErrorCode = "github_mutation_accepted"
)

type Error struct {
	Code ErrorCode
	Err  error
}

func (err *Error) Error() string {
	if err == nil || err.Err == nil {
		return string(err.Code)
	}
	return err.Err.Error()
}

func (err *Error) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// Uncertain reports whether an attempted mutation could have reached GitHub.
func (err *Error) Uncertain() bool {
	if err == nil {
		return false
	}
	if err.Code == ErrorCodeMutationAccepted || err.Code == ErrorCodeMutationUncertain {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, definite := range []string{"executable was not found", "http 400", "http 401", "http 403", "http 404", "http 405", "http 409", "http 422", "http 429", "not found", "forbidden", "unprocessable", "must be", "is required", "sha does not match", "sha mismatch", "head changed", "head branch was modified", "branch protection", "review is required", "blocked the merge", "rejected merge", "not mergeable"} {
		if strings.Contains(message, definite) {
			return false
		}
	}
	return true
}

type Repository struct {
	Owner string
	Name  string
}

type RefRepository struct {
	CloneURL string `json:"clone_url"`
	FullName string `json:"full_name"`
}

type Ref struct {
	Ref        string         `json:"ref"`
	SHA        string         `json:"sha"`
	Repository *RefRepository `json:"repo,omitempty"`
}

type AuthenticatedUser struct {
	NodeID    string `json:"node_id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
}

type Collaborator struct {
	NodeID     string `json:"node_id"`
	Login      string `json:"login"`
	AvatarURL  string `json:"avatar_url"`
	HTMLURL    string `json:"html_url"`
	Permission string `json:"role_name"`
}

type Label struct {
	ID          int64  `json:"id"`
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type User struct {
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
	NodeID    string `json:"node_id"`
	Login     string `json:"login"`
}

type PullRequest struct {
	User               User       `json:"user"`
	Assignees          []User     `json:"assignees"`
	RequestedReviewers []User     `json:"requested_reviewers"`
	Number             int        `json:"number"`
	NodeID             string     `json:"node_id"`
	Title              string     `json:"title"`
	Body               string     `json:"body"`
	HTMLURL            string     `json:"html_url"`
	State              string     `json:"state"`
	Draft              bool       `json:"draft"`
	Mergeable          *bool      `json:"mergeable"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ClosedAt           *time.Time `json:"closed_at"`
	Merged             bool       `json:"merged"`
	MergedAt           *time.Time `json:"merged_at"`
	MergeCommitSHA     string     `json:"merge_commit_sha"`
	Base               Ref        `json:"base"`
	Head               Ref        `json:"head"`
}

type ChecksState string

const (
	ChecksPassing ChecksState = "passing"
	ChecksPending ChecksState = "pending"
	ChecksFailing ChecksState = "failing"
	ChecksError   ChecksState = "error"
	ChecksUnknown ChecksState = "unknown"
)

type MergeabilityState string

const (
	MergeabilityMergeable   MergeabilityState = "mergeable"
	MergeabilityBlocked     MergeabilityState = "blocked"
	MergeabilityConflicting MergeabilityState = "conflicting"
	MergeabilityUnknown     MergeabilityState = "unknown"
)

type PullRequestReadiness struct {
	HeadCommit        string
	ChecksState       ChecksState
	MergeabilityState MergeabilityState
	DetailsURL        string
}

type pullRequestCheck struct {
	Bucket     string `json:"bucket"`
	State      string `json:"state"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type Issue struct {
	Number      int        `json:"number"`
	NodeID      string     `json:"node_id"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	HTMLURL     string     `json:"html_url"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	PullRequest *struct{}  `json:"pull_request,omitempty"`
	Labels      []Label    `json:"labels"`
	User        User       `json:"user"`
	Assignees   []User     `json:"assignees"`
}

type CreatePullRequestRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Draft bool   `json:"draft"`
}

type MergePullRequestRequest struct {
	CommitTitle     string `json:"commit_title,omitempty"`
	CommitMessage   string `json:"commit_message,omitempty"`
	MergeMethod     string `json:"merge_method,omitempty"`
	ExpectedHeadSHA string `json:"sha,omitempty"`
}

type MergePullRequestResponse struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

type UpdatePullRequestRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type updatePullRequestStateRequest struct {
	State string `json:"state"`
}

type CreateIssueRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type UpdateIssueRequest struct {
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	State *string `json:"state,omitempty"`
}

type CreateLabelRequest struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description,omitempty"`
}

type IssueLabelsRequest struct {
	Labels []string `json:"labels"`
}
type issueAssigneesRequest struct {
	Assignees []string `json:"assignees"`
}

type CLIClient struct {
	Command string
}

func NewCLIClient() *CLIClient {
	return &CLIClient{Command: "gh"}
}

func (client *CLIClient) CurrentUser(ctx context.Context) (AuthenticatedUser, error) {
	var user AuthenticatedUser
	if err := client.Request(ctx, "GET", "user", nil, &user); err != nil {
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != ErrorCodeSyncFailed || !strings.Contains(err.Error(), "decode gh api") {
			return AuthenticatedUser{}, err
		}
		return AuthenticatedUser{}, &Error{
			Code: ErrorCodeSyncFailed,
			Err:  fmt.Errorf("decode gh api user: %w", err),
		}
	}
	return user, nil
}

func (client *CLIClient) ListCollaborators(ctx context.Context, repository Repository) ([]Collaborator, error) {
	return runPagedJSON[Collaborator](ctx, client, fmt.Sprintf("/repos/%s/%s/collaborators?affiliation=all&per_page=100", url.PathEscape(repository.Owner), url.PathEscape(repository.Name)))
}

func (client *CLIClient) ListPullRequests(ctx context.Context, repository Repository) ([]PullRequest, error) {
	return runPagedJSON[PullRequest](ctx, client, fmt.Sprintf("/repos/%s/%s/pulls?state=all&per_page=100", url.PathEscape(repository.Owner), url.PathEscape(repository.Name)))
}

func (client *CLIClient) PullRequest(ctx context.Context, repository Repository, number int) (PullRequest, error) {
	if number <= 0 {
		return PullRequest{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub pull request number is required")}
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number)
	var pullRequest PullRequest
	if err := client.Request(ctx, "GET", endpoint, nil, &pullRequest); err != nil {
		return PullRequest{}, err
	}
	return pullRequest, nil
}

func (client *CLIClient) PullRequestReadiness(ctx context.Context, repository Repository, number int) (PullRequestReadiness, error) {
	if number <= 0 {
		return PullRequestReadiness{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub pull request number is required")}
	}
	query := `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){headRefOid mergeable mergeStateStatus url commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100){nodes{... on CheckRun{status conclusion} ... on StatusContext{state}}}}}}}}}}`
	var response struct {
		Data struct {
			Repository *struct {
				PullRequest *struct {
					HeadRefOID       string `json:"headRefOid"`
					Mergeable        string `json:"mergeable"`
					MergeStateStatus string `json:"mergeStateStatus"`
					URL              string `json:"url"`
					Commits          struct {
						Nodes []struct {
							Commit struct {
								StatusCheckRollup *struct {
									Contexts struct {
										Nodes []pullRequestCheck `json:"nodes"`
									} `json:"contexts"`
								} `json:"statusCheckRollup"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := client.GraphQL(ctx, query, map[string]any{"owner": repository.Owner, "name": repository.Name, "number": number}, &response); err != nil {
		return PullRequestReadiness{}, err
	}
	if response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
		return PullRequestReadiness{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub pull request readiness was not returned")}
	}
	remote := response.Data.Repository.PullRequest
	var checks []pullRequestCheck
	if len(remote.Commits.Nodes) > 0 && remote.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		checks = remote.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes
	}
	return PullRequestReadiness{
		HeadCommit: strings.TrimSpace(remote.HeadRefOID), ChecksState: mapChecksState(checks),
		MergeabilityState: mapMergeabilityState(remote.Mergeable, remote.MergeStateStatus), DetailsURL: strings.TrimSpace(remote.URL),
	}, nil
}

func mapChecksState(rollup []pullRequestCheck) ChecksState {
	if len(rollup) == 0 {
		return ChecksUnknown
	}
	state := ChecksPassing
	hasUnrecognized := false
	for _, item := range rollup {
		itemRecognized := false
		values := []string{strings.ToUpper(item.Bucket), strings.ToUpper(item.State), strings.ToUpper(item.Status), strings.ToUpper(item.Conclusion)}
		for _, value := range values {
			switch value {
			case "ERROR":
				return ChecksError
			case "FAIL", "FAILING", "FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
				itemRecognized = true
				state = ChecksFailing
			case "PENDING", "EXPECTED", "QUEUED", "IN_PROGRESS", "REQUESTED", "WAITING":
				itemRecognized = true
				if state == ChecksPassing {
					state = ChecksPending
				}
			case "PASS", "PASSING", "SUCCESS", "SKIPPING", "SKIPPED", "NEUTRAL":
				itemRecognized = true
			}
		}
		if !itemRecognized {
			hasUnrecognized = true
		}
	}
	if state == ChecksPassing && hasUnrecognized {
		return ChecksUnknown
	}
	return state
}

func mapMergeabilityState(mergeable, mergeStateStatus string) MergeabilityState {
	mergeable = strings.ToUpper(strings.TrimSpace(mergeable))
	mergeStateStatus = strings.ToUpper(strings.TrimSpace(mergeStateStatus))
	if mergeable == "CONFLICTING" || mergeStateStatus == "DIRTY" {
		return MergeabilityConflicting
	}
	switch mergeStateStatus {
	case "BLOCKED", "BEHIND", "DRAFT", "UNSTABLE":
		return MergeabilityBlocked
	case "CLEAN", "HAS_HOOKS":
		if mergeable == "MERGEABLE" {
			return MergeabilityMergeable
		}
	}
	return MergeabilityUnknown
}

func (client *CLIClient) ListIssues(ctx context.Context, repository Repository) ([]Issue, error) {
	return client.listIssues(ctx, repository, "all")
}

func (client *CLIClient) ListOpenIssues(ctx context.Context, repository Repository) ([]Issue, error) {
	return client.listIssues(ctx, repository, "open")
}

func (client *CLIClient) listIssues(ctx context.Context, repository Repository, state string) ([]Issue, error) {
	issues, err := runPagedJSON[Issue](ctx, client, fmt.Sprintf("/repos/%s/%s/issues?state=%s&per_page=100", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), state))
	if err != nil {
		return nil, err
	}
	result := issues[:0]
	for _, issue := range issues {
		if issue.PullRequest == nil {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (client *CLIClient) GetIssue(ctx context.Context, repository Repository, number int) (Issue, error) {
	var issue Issue
	if err := client.Request(ctx, "GET", fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number), nil, &issue); err != nil {
		return Issue{}, err
	}
	if issue.PullRequest != nil {
		return Issue{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub returned a pull request instead of an issue")}
	}
	return issue, nil
}

func (client *CLIClient) CreateIssue(ctx context.Context, repository Repository, request CreateIssueRequest) (Issue, error) {
	input, err := json.Marshal(request)
	if err != nil {
		return Issue{}, err
	}
	output, err := client.runWithInput(ctx, input, "api", "--method", "POST", fmt.Sprintf("/repos/%s/%s/issues", url.PathEscape(repository.Owner), url.PathEscape(repository.Name)), "--input", "-")
	if err != nil {
		return Issue{}, err
	}
	var issue Issue
	if err := json.Unmarshal(output, &issue); err != nil {
		return Issue{}, &Error{
			Code: ErrorCodeMutationAccepted,
			Err:  fmt.Errorf("decode gh api create issue: %w", err),
		}
	}
	return issue, nil
}

func (client *CLIClient) UpdateIssue(ctx context.Context, repository Repository, number int, request UpdateIssueRequest) (Issue, error) {
	input, err := json.Marshal(request)
	if err != nil {
		return Issue{}, err
	}
	output, err := client.runWithInput(ctx, input, "api", "--method", "PATCH", fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number), "--input", "-")
	if err != nil {
		return Issue{}, err
	}
	var issue Issue
	if err := json.Unmarshal(output, &issue); err != nil {
		return Issue{}, &Error{
			Code: ErrorCodeMutationAccepted,
			Err:  fmt.Errorf("decode gh api update issue: %w", err),
		}
	}
	return issue, nil
}

func (client *CLIClient) ListLabels(ctx context.Context, repository Repository) ([]Label, error) {
	return runPagedJSON[Label](ctx, client, fmt.Sprintf("/repos/%s/%s/labels?per_page=100", url.PathEscape(repository.Owner), url.PathEscape(repository.Name)))
}

func (client *CLIClient) CreateLabel(ctx context.Context, repository Repository, request CreateLabelRequest) (Label, error) {
	input, err := json.Marshal(request)
	if err != nil {
		return Label{}, err
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/labels", url.PathEscape(repository.Owner), url.PathEscape(repository.Name))
	output, err := client.runWithInput(ctx, input, "api", "--method", "POST", endpoint, "--input", "-")
	if err != nil {
		return Label{}, err
	}
	var label Label
	if err := json.Unmarshal(output, &label); err != nil {
		return Label{}, &Error{Code: ErrorCodeMutationAccepted, Err: fmt.Errorf("decode gh api create label: %w", err)}
	}
	return label, nil
}

func (client *CLIClient) ListIssueLabels(ctx context.Context, repository Repository, number int) ([]Label, error) {
	if number <= 0 {
		return nil, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub issue number is required")}
	}
	return runPagedJSON[Label](ctx, client, fmt.Sprintf("/repos/%s/%s/issues/%d/labels?per_page=100", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number))
}

func (client *CLIClient) AddIssueLabels(ctx context.Context, repository Repository, number int, names []string) ([]Label, error) {
	return client.mutateIssueLabels(ctx, repository, number, httpMethodPost, names)
}

func (client *CLIClient) SetIssueLabels(ctx context.Context, repository Repository, number int, names []string) ([]Label, error) {
	return client.mutateIssueLabels(ctx, repository, number, httpMethodPut, names)
}

const (
	httpMethodPost = "POST"
	httpMethodPut  = "PUT"
)

func (client *CLIClient) mutateIssueLabels(ctx context.Context, repository Repository, number int, method string, names []string) ([]Label, error) {
	if number <= 0 {
		return nil, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub issue number is required")}
	}
	input, err := json.Marshal(IssueLabelsRequest{Labels: names})
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number)
	output, err := client.runWithInput(ctx, input, "api", "--method", method, endpoint, "--input", "-")
	if err != nil {
		return nil, err
	}
	var labels []Label
	if err := json.Unmarshal(output, &labels); err != nil {
		return nil, &Error{Code: ErrorCodeMutationAccepted, Err: fmt.Errorf("decode gh api %s issue labels: %w", strings.ToLower(method), err)}
	}
	if labels == nil {
		labels = []Label{}
	}
	return labels, nil
}
func (client *CLIClient) ReplaceIssueAssignees(ctx context.Context, repository Repository, number int, logins []string) (Issue, error) {
	return client.mutateIssueAssignees(ctx, repository, number, "PATCH", "", logins)
}

func (client *CLIClient) AddIssueAssignee(ctx context.Context, repository Repository, number int, login string) (Issue, error) {
	return client.mutateIssueAssignees(ctx, repository, number, "POST", "/assignees", []string{login})
}

func (client *CLIClient) RemoveIssueAssignee(ctx context.Context, repository Repository, number int, login string) (Issue, error) {
	return client.mutateIssueAssignees(ctx, repository, number, "DELETE", "/assignees", []string{login})
}

func (client *CLIClient) mutateIssueAssignees(ctx context.Context, repository Repository, number int, method, suffix string, logins []string) (Issue, error) {
	if number <= 0 {
		return Issue{}, &Error{Code: ErrorCodeSyncFailed, Err: errors.New("GitHub issue number is required")}
	}
	request := issueAssigneesRequest{Assignees: append([]string{}, logins...)}
	input, err := json.Marshal(request)
	if err != nil {
		return Issue{}, err
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d%s", url.PathEscape(repository.Owner), url.PathEscape(repository.Name), number, suffix)
	output, err := client.runWithInput(ctx, input, "api", "--method", method, endpoint, "--input", "-")
	if err != nil {
		return Issue{}, err
	}
	var issue Issue
	if err := json.Unmarshal(output, &issue); err != nil {
		return Issue{}, &Error{
			Code: ErrorCodeMutationAccepted,
			Err:  fmt.Errorf("decode gh api mutate issue assignees: %w", err),
		}
	}
	return issue, nil
}

// Request exposes a typed gh-api call for provider adapters whose resources do
// not belong in the shared GitHub pull-request or issue models.
func (client *CLIClient) Request(ctx context.Context, method, endpoint string, input, output any) error {
	arguments := []string{"api", "--include"}
	if method != "" && method != "GET" {
		arguments = append(arguments, "--method", method)
	}
	arguments = append(arguments, endpoint)
	var payload []byte
	var err error
	if input != nil {
		payload, err = json.Marshal(input)
		if err != nil {
			return err
		}
		arguments = append(arguments, "--input", "-")
	}
	data, err := client.runReadWithInput(ctx, payload, arguments...)
	if err != nil {
		return err
	}
	if output == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return &Error{Code: ErrorCodeSyncFailed, Err: fmt.Errorf("decode gh api %s: %w", endpoint, err)}
	}
	return nil
}

func (client *CLIClient) GraphQL(ctx context.Context, query string, variables map[string]any, output any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	response, err := client.runReadResponse(ctx, payload, "api", "--include", "graphql", "--input", "-")
	if err != nil {
		return err
	}
	data := response.Body
	if err := graphQLRateLimitError(data, response.Header); err != nil {
		return err
	}
	if output == nil {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return &Error{Code: ErrorCodeSyncFailed, Err: fmt.Errorf("decode gh graphql response: %w", err)}
	}
	return nil
}

func runPagedJSON[T any](ctx context.Context, client *CLIClient, endpoint string) ([]T, error) {
	var result []T
	for pageNumber := 1; ; pageNumber++ {
		var page []T
		separator := "?"
		if strings.Contains(endpoint, "?") {
			separator = "&"
		}
		if err := client.Request(ctx, "GET", endpoint+separator+"page="+strconv.Itoa(pageNumber), nil, &page); err != nil {
			return nil, err
		}
		result = append(result, page...)
		if len(page) < 100 {
			break
		}
	}
	return result, nil
}

func (client *CLIClient) run(ctx context.Context, arguments ...string) ([]byte, error) {
	return client.runWithInput(ctx, nil, arguments...)
}

func (client *CLIClient) runWithInput(ctx context.Context, input []byte, arguments ...string) ([]byte, error) {
	commandName := client.Command
	if commandName == "" {
		commandName = "gh"
	}
	command := exec.CommandContext(ctx, commandName, arguments...)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	output, err := command.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, &Error{
				Code: ErrorCodeGHUnavailable,
				Err:  fmt.Errorf("%s executable was not found: %w", commandName, err),
			}
		}
		if exit, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exit.Stderr))
			if stderr == "" {
				stderr = exit.Error()
			}
			// Keep HTTP headers for callers that distinguish rejection from an uncertain write.
			return output, &Error{
				Code: ErrorCodeSyncFailed,
				Err:  fmt.Errorf("%s %s failed: %s", commandName, strings.Join(arguments, " "), stderr),
			}
		}
		return nil, &Error{
			Code: ErrorCodeGHUnavailable,
			Err:  err,
		}
	}
	return output, nil
}

func ParseRepositoryURL(raw string) (Repository, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return Repository{}, ErrUnsupportedRepository
	}
	if strings.HasPrefix(value, "git@github.com:") {
		return parseOwnerRepo(strings.TrimPrefix(value, "git@github.com:"))
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Hostname() == "github.com" {
		if parsed.Scheme == "https" || parsed.Scheme == "ssh" {
			return parseOwnerRepo(strings.TrimPrefix(parsed.Path, "/"))
		}
	}
	return Repository{}, ErrUnsupportedRepository
}

func parseOwnerRepo(value string) (Repository, error) {
	trimmed := strings.TrimSuffix(strings.Trim(value, "/"), ".git")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Repository{}, ErrUnsupportedRepository
	}
	return Repository{Owner: parts[0], Name: parts[1]}, nil
}
