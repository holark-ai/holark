package holarkclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/issues" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		var request struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Title != "Fix issue list" || request.Body != "Body text" {
			t.Fatalf("request = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"issue-1",
			"repository_id":"holark",
			"title":"Fix issue list",
			"body":"Body text",
			"status":"open",
			"sync_data":{"github":{"number":42}},
			"linked_pull_request_ids":[],
			"created_at":"2026-01-01T10:00:00Z",
			"updated_at":"2026-01-01T10:00:00Z"
		}`))
	}))
	defer server.Close()

	issue, err := Client{BaseURL: server.URL}.CreateIssue(context.Background(), "Fix issue list", "Body text")
	if err != nil {
		t.Fatal(err)
	}
	if issue.ID != "issue-1" || issue.Status != IssueStatusOpen || issue.Title != "Fix issue list" {
		t.Fatalf("issue = %#v", issue)
	}
	if issue.SyncData.GitHub == nil || issue.SyncData.GitHub.Number != 42 {
		t.Fatalf("issue sync data = %#v", issue.SyncData)
	}
}

func TestBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cli-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	if _, err := (Client{BaseURL: server.URL, BearerToken: " cli-token "}).ListIssues(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIssueStateEndpoints(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"issue-1",
			"repository_id":"holark",
			"title":"Fix issue list",
			"body":"",
			"status":"open",
			"sync_data":{},
			"linked_pull_request_ids":[],
			"created_at":"2026-01-01T10:00:00Z",
			"updated_at":"2026-01-01T10:00:00Z"
		}`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL}
	if _, err := client.CloseIssue(context.Background(), "issue-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReopenIssue(context.Background(), "issue-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/api/v1/issues/issue-1/close", "/api/v1/issues/issue-1/reopen"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %#v", paths)
	}
	for index := range want {
		if paths[index] != want[index] {
			t.Fatalf("paths = %#v, want %#v", paths, want)
		}
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_request","message":"The issue request is invalid."}`))
	}))
	defer server.Close()

	_, err := Client{BaseURL: server.URL}.CreateIssue(context.Background(), "", "")
	apiErr, ok := err.(APIError)
	if !ok {
		t.Fatalf("err = %T, want APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest || apiErr.Code != "invalid_request" {
		t.Fatalf("apiErr = %#v", apiErr)
	}
}

func TestProjectionPendingIsReturnedAsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"code":"label_projection_pending","message":"GitHub created the label, but Holark has not stored it yet.","safe_to_retry":false}`))
	}))
	defer server.Close()

	_, err := Client{BaseURL: server.URL}.CreateLabel(context.Background(), "bug", "d73a4a", "")
	apiErr, ok := err.(APIError)
	if !ok || apiErr.StatusCode != http.StatusAccepted || apiErr.Code != "label_projection_pending" {
		t.Fatalf("error = %#v, want accepted label_projection_pending APIError", err)
	}
}
