package holarkclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL     string
	BearerToken string
	HTTPClient  *http.Client
}

type IssueStatus string

const (
	IssueStatusOpen   IssueStatus = "open"
	IssueStatusClosed IssueStatus = "closed"
)

type IssueSyncData struct {
	GitHub *IssueGitHubSyncData `json:"github,omitempty"`
}

type IssueGitHubSyncData struct {
	Owner     string `json:"owner,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Number    int    `json:"number,omitempty"`
	URL       string `json:"url,omitempty"`
	RuntimeID string `json:"runtime_id,omitempty"`
	State     string `json:"state,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type Label struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type Issue struct {
	ID                   string        `json:"id"`
	Title                string        `json:"title"`
	Body                 string        `json:"body"`
	Status               IssueStatus   `json:"status"`
	SyncProvider         string        `json:"sync_provider,omitempty"`
	SyncExternalID       string        `json:"sync_external_id,omitempty"`
	SyncData             IssueSyncData `json:"sync_data"`
	Labels               []Label       `json:"labels"`
	LinkedPullRequestIDs []string      `json:"linked_pull_request_ids"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
	ClosedAt             *time.Time    `json:"closed_at,omitempty"`
	SyncedAt             *time.Time    `json:"synced_at,omitempty"`
}

type GitHubMember struct {
	ID         string `json:"id"`
	Login      string `json:"login"`
	AvatarURL  string `json:"avatar_url,omitempty"`
	ProfileURL string `json:"profile_url,omitempty"`
	Permission string `json:"permission"`
	IsMe       bool   `json:"is_me"`
}

type APIError struct {
	GitHubCommentID        string
	GitHubCommentURL       string
	ReconciliationRequired bool
	SafeToRetry            bool
	StatusCode             int
	Code                   string
	Message                string
}

func (err APIError) Error() string {
	if err.GitHubCommentURL != "" {
		return fmt.Sprintf("%s: %s (%s)", err.Code, err.Message, err.GitHubCommentURL)
	}
	if err.Code != "" && err.Message != "" {
		return fmt.Sprintf("%s: %s", err.Code, err.Message)
	}
	if err.Message != "" {
		return err.Message
	}
	if err.Code != "" {
		return err.Code
	}
	return fmt.Sprintf("request failed (%d)", err.StatusCode)
}

func (client Client) ListIssues(ctx context.Context) ([]Issue, error) {
	var issues []Issue
	if err := client.do(ctx, http.MethodGet, "/api/v1/issues", nil, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

func (client Client) ListGitHubMembers(ctx context.Context, projectID string) ([]GitHubMember, error) {
	_ = projectID
	var result []GitHubMember
	err := client.do(ctx, http.MethodGet, "/api/v1/github-members", nil, &result)
	return result, err
}

func (client Client) SyncGitHubMembers(ctx context.Context, projectID string) ([]GitHubMember, error) {
	_ = projectID
	var result []GitHubMember
	err := client.do(ctx, http.MethodPost, "/api/v1/github-members/sync", nil, &result)
	return result, err
}

func (client Client) ListLabels(ctx context.Context) ([]Label, error) {
	var labels []Label
	if err := client.do(ctx, http.MethodGet, "/api/v1/labels", nil, &labels); err != nil {
		return nil, err
	}
	if labels == nil {
		labels = []Label{}
	}
	return labels, nil
}

func (client Client) CreateLabel(ctx context.Context, name, color, description string) (Label, error) {
	request := struct {
		Name        string `json:"name"`
		Color       string `json:"color,omitempty"`
		Description string `json:"description,omitempty"`
	}{Name: name, Color: color, Description: description}
	var label Label
	if err := client.do(ctx, http.MethodPost, "/api/v1/labels", request, &label); err != nil {
		return Label{}, err
	}
	return label, nil
}

func (client Client) AddIssueLabels(ctx context.Context, issueID string, labels []string) (Issue, error) {
	return client.changeIssueLabels(ctx, issueID, "add", labels)
}

func (client Client) RemoveIssueLabels(ctx context.Context, issueID string, labels []string) (Issue, error) {
	return client.changeIssueLabels(ctx, issueID, "remove", labels)
}

func (client Client) changeIssueLabels(ctx context.Context, issueID, action string, labels []string) (Issue, error) {
	request := struct {
		Labels []string `json:"labels"`
	}{Labels: labels}
	var issue Issue
	if err := client.do(ctx, http.MethodPost, "/api/v1/issues/"+url.PathEscape(issueID)+"/labels/"+action, request, &issue); err != nil {
		return Issue{}, err
	}
	if issue.Labels == nil {
		issue.Labels = []Label{}
	}
	return issue, nil
}

func (client Client) GetIssue(ctx context.Context, issueID string) (Issue, error) {
	var issue Issue
	if err := client.do(ctx, http.MethodGet, "/api/v1/issues/"+url.PathEscape(issueID), nil, &issue); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

func (client Client) CreateIssue(ctx context.Context, title, body string) (Issue, error) {
	request := struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{Title: title, Body: body}
	var issue Issue
	if err := client.do(ctx, http.MethodPost, "/api/v1/issues", request, &issue); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

func (client Client) CloseIssue(ctx context.Context, issueID string) (Issue, error) {
	var issue Issue
	if err := client.do(ctx, http.MethodPost, "/api/v1/issues/"+url.PathEscape(issueID)+"/close", nil, &issue); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

func (client Client) ReopenIssue(ctx context.Context, issueID string) (Issue, error) {
	var issue Issue
	if err := client.do(ctx, http.MethodPost, "/api/v1/issues/"+url.PathEscape(issueID)+"/reopen", nil, &issue); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

func (client Client) do(ctx context.Context, method, apiPath string, body any, out any) error {
	baseURL := strings.TrimRight(client.BaseURL, "/")
	if baseURL == "" {
		return errors.New("Holark server URL is required")
	}
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, baseURL+apiPath, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token := strings.TrimSpace(client.BearerToken); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusAccepted || response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeAPIError(response)
	}
	if out == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func decodeAPIError(response *http.Response) error {
	var payload struct {
		Code                   string `json:"code"`
		Message                string `json:"message"`
		GitHubCommentID        string `json:"github_comment_id"`
		GitHubCommentURL       string `json:"github_comment_url"`
		ReconciliationRequired bool   `json:"reconciliation_required"`
		SafeToRetry            bool   `json:"safe_to_retry"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return APIError{StatusCode: response.StatusCode, Message: response.Status}
	}
	return APIError{StatusCode: response.StatusCode, Code: payload.Code, Message: payload.Message, GitHubCommentID: payload.GitHubCommentID, GitHubCommentURL: payload.GitHubCommentURL, ReconciliationRequired: payload.ReconciliationRequired, SafeToRetry: payload.SafeToRetry}
}
