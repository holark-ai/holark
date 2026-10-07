package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestcomments"
)

type freshnessService struct {
	Service
	synced     []pullrequestcomments.Comment
	local      []pullrequestcomments.Comment
	syncErr    error
	localErr   error
	syncCalls  int
	localCalls int
}

func (service *freshnessService) Sync(context.Context, string) ([]pullrequestcomments.Comment, error) {
	service.syncCalls++
	return service.synced, service.syncErr
}

func (service *freshnessService) ListByPullRequest(context.Context, string) ([]pullrequestcomments.Comment, error) {
	service.localCalls++
	return service.local, service.localErr
}

type freshnessTargets struct{}

func (freshnessTargets) GetCommentTarget(context.Context, string) (pullrequestcomments.PullRequestTarget, error) {
	return pullrequestcomments.PullRequestTarget{ID: "pr-one"}, nil
}

type createService struct {
	Service
	request pullrequestcomments.CreateComment
	calls   int
	err     error
}

func (service *createService) Create(_ context.Context, request pullrequestcomments.CreateComment) (pullrequestcomments.Comment, error) {
	service.request = request
	service.calls++
	if service.err != nil {
		return pullrequestcomments.Comment{}, service.err
	}
	return pullrequestcomments.Comment{ID: "reply-one", PullRequestID: request.PullRequestID, ParentCommentID: request.ParentCommentID, Body: request.Body}, nil
}

func TestExplicitSyncSynchronizesProviderCommentsAndFallsBackToLocalState(t *testing.T) {
	for _, test := range []struct {
		name           string
		service        *freshnessService
		wantBody       string
		wantLocalCalls int
		wantStatus     int
	}{
		{
			name:     "provider synchronization succeeds",
			service:  &freshnessService{synced: []pullrequestcomments.Comment{{ID: "remote", Body: "From GitHub"}}},
			wantBody: "From GitHub",
		},
		{
			name:           "provider synchronization fails",
			service:        &freshnessService{syncErr: errors.New("github unavailable"), local: []pullrequestcomments.Comment{{ID: "local", Body: "Cached locally"}}},
			wantBody:       "Cached locally",
			wantLocalCalls: 1,
		},
		{
			name:     "comment reconciliation fails",
			service:  &freshnessService{syncErr: errors.New("github unavailable"), localErr: errors.New("cached comments unavailable")},
			wantBody: "pull_request_comment_failed", wantLocalCalls: 1, wantStatus: http.StatusInternalServerError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mux := http.NewServeMux()
			RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, test.service, freshnessTargets{})

			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/pr-one/comments/sync", nil)
			mux.ServeHTTP(response, request)

			if test.wantStatus == 0 {
				test.wantStatus = http.StatusOK
			}
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.service.syncCalls != 1 || test.service.localCalls != test.wantLocalCalls {
				t.Fatalf("sync calls = %d, local calls = %d; want 1, %d", test.service.syncCalls, test.service.localCalls, test.wantLocalCalls)
			}
			if body := response.Body.String(); !strings.Contains(body, test.wantBody) {
				t.Fatalf("response body = %q, want comment %q", body, test.wantBody)
			}
		})
	}
}

func TestCreateScopedRequests(t *testing.T) {
	line := 17
	for _, test := range []struct {
		name, body string
		want       pullrequestcomments.CreateComment
	}{
		{"legacy general", `{"body":" note "}`, pullrequestcomments.CreateComment{Body: "note"}},
		{"explicit general", `{"body":"note","scope":"pull_request"}`, pullrequestcomments.CreateComment{Body: "note", Scope: pullrequestcomments.ScopePullRequest}},
		{"file", `{"body":"note","scope":"file","path":"new.go","old_path":"old.go"}`, pullrequestcomments.CreateComment{Body: "note", Scope: pullrequestcomments.ScopeFile, Path: "new.go", OldPath: "old.go"}},
		{"left line", `{"body":"note","scope":"line","path":"new.go","old_path":"old.go","side":"LEFT","line":17,"diff_hunk":"@@ -17 +17 @@\n-old\n+new\n"}`, pullrequestcomments.CreateComment{Body: "note", Scope: pullrequestcomments.ScopeLine, Path: "new.go", OldPath: "old.go", Side: "LEFT", Line: &line, DiffHunk: "@@ -17 +17 @@\n-old\n+new\n"}},
		{"right line", `{"body":"note","scope":"line","path":"new.go","side":"RIGHT","line":17}`, pullrequestcomments.CreateComment{Body: "note", Scope: pullrequestcomments.ScopeLine, Path: "new.go", Side: "RIGHT", Line: &line}},
		{"reply", `{"body":"  Fixed it  ","parent_comment_id":"  parent-one  "}`, pullrequestcomments.CreateComment{Body: "Fixed it", ParentCommentID: "parent-one"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &createService{}
			response := postComment(service, test.body)
			if response.Code != http.StatusCreated {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			test.want.PullRequestID = "pr-one"
			test.want.Origin = pullrequestcomments.UserOrigin()
			if service.calls != 1 || !reflect.DeepEqual(service.request, test.want) {
				t.Fatalf("request = %#v; want %#v", service.request, test.want)
			}
		})
	}
}

func TestCreateRejectsMalformedLocationsBeforeService(t *testing.T) {
	for _, body := range []string{
		`{"body":"note","scope":"unknown"}`,
		`{"body":"note","scope":""}`,
		`{"body":"note","scope":null}`,
		`{"body":"note","path":"a"}`,
		`{"body":"note","PATH":"a"}`,
		`{"body":"note","parent_comment_id":"parent","SCOPE":"file","PATH":"a"}`,
		`{"body":"note","old_path":"a"}`,
		`{"body":"note","side":"RIGHT"}`,
		`{"body":"note","line":1}`,
		`{"body":"note","diff_hunk":"hunk"}`,
		`{"body":"note","scope":"pull_request","path":""}`,
		`{"body":"note","scope":"pull_request","old_path":"a"}`,
		`{"body":"note","scope":"pull_request","side":"RIGHT"}`,
		`{"body":"note","scope":"pull_request","line":null}`,
		`{"body":"note","scope":"pull_request","diff_hunk":""}`,
		`{"body":"note","scope":"file"}`,
		`{"body":"note","scope":"file","path":"  "}`,
		`{"body":"note","scope":"file","path":"a","line":0}`,
		`{"body":"note","scope":"file","path":"a","side":""}`,
		`{"body":"note","scope":"file","path":"a","diff_hunk":""}`,
		`{"body":"note","scope":"line","side":"LEFT","line":1}`,
		`{"body":"note","scope":"line","path":"a","line":1}`,
		`{"body":"note","scope":"line","path":"a","side":"right","line":1}`,
		`{"body":"note","scope":"line","path":"a","side":"LEFT"}`,
		`{"body":"note","scope":"line","path":"a","side":"LEFT","line":null}`,
		`{"body":"note","scope":"line","path":"a","side":"LEFT","line":0}`,
		`{"body":"note","scope":"line","path":"a","side":"LEFT","line":-1}`,
		`{"body":"note","scope":"line","path":"a","side":"LEFT","line":1.5}`,
		`{"body":"note","scope":"line","path":"a","side":"LEFT","line":"1"}`,
		`{"body":"note","parent_comment_id":"parent","scope":"pull_request"}`,
		`{"body":"note","parent_comment_id":"parent","path":""}`,
		`{"body":"note","parent_comment_id":"parent","old_path":null}`,
		`{"body":"note","parent_comment_id":"parent","side":""}`,
		`{"body":"note","parent_comment_id":"parent","line":null}`,
		`{"body":"note","parent_comment_id":"parent","diff_hunk":""}`,
		`{"body":"note","unexpected":true}`,
		`{"body":" "}`,
		`{"body":42}`,
		`{"body":`,
	} {
		t.Run(body, func(t *testing.T) {
			service := &createService{}
			response := postComment(service, body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) || service.calls != 0 {
				t.Fatalf("status = %d, calls = %d, body = %s", response.Code, service.calls, response.Body.String())
			}
		})
	}
}

func TestCreatePreservesServiceErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{pullrequestcomments.ErrPullRequestNotFound, 404, "pull_request_not_found"},
		{pullrequestcomments.ErrCommentNotFound, 404, "pull_request_comment_not_found"},
		{pullrequestcomments.ErrInvalidParent, 400, "invalid_request"},
		{pullrequestcomments.ErrInvalidComment, 400, "invalid_request"},
		{errors.New("failed"), 500, "pull_request_comment_failed"},
	} {
		response := postComment(&createService{err: test.err}, `{"body":"note","scope":"file","path":"a"}`)
		if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
	}
}

func postComment(service *createService, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service, freshnessTargets{})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/pr-one/comments", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(response, request)
	return response
}

func TestGetCommentsReadsCachedStateWithoutProviderSynchronization(t *testing.T) {
	service := &freshnessService{local: []pullrequestcomments.Comment{{ID: "local", Body: "Cached locally"}}}
	mux := http.NewServeMux()
	RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service, freshnessTargets{})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/pr-one/comments", nil))
	if response.Code != http.StatusOK || service.syncCalls != 0 || service.localCalls != 1 || !strings.Contains(response.Body.String(), "Cached locally") {
		t.Fatalf("status=%d sync calls=%d local calls=%d body=%s", response.Code, service.syncCalls, service.localCalls, response.Body.String())
	}
}
