package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestwork"
)

type serviceStub struct {
	result         []pullrequestwork.Work
	err            error
	starts         []pullrequestwork.Start
	readiness      pullrequestwork.RebaseReadiness
	readinessErr   error
	readinessCalls []string
}

func (stub *serviceStub) Start(_ context.Context, start pullrequestwork.Start) ([]pullrequestwork.Work, error) {
	stub.starts = append(stub.starts, start)
	return stub.result, stub.err
}

func (*serviceStub) List(context.Context, string, pullrequestwork.Kind) ([]pullrequestwork.Work, error) {
	return nil, nil
}

func (stub *serviceStub) RebaseReadiness(_ context.Context, pullRequestID string) (pullrequestwork.RebaseReadiness, error) {
	stub.readinessCalls = append(stub.readinessCalls, pullRequestID)
	return stub.readiness, stub.readinessErr
}

func TestStartRebaseResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		work pullrequestwork.Work
	}{
		{name: "queued", work: pullrequestwork.Work{Status: pullrequestwork.StatusQueued}},
		{name: "changed direct rebase", work: pullrequestwork.Work{HeadCommit: "source-head", ResultHeadCommit: "rebased-head", Status: pullrequestwork.StatusCompleted}},
		{name: "no-op direct rebase", work: pullrequestwork.Work{HeadCommit: "source-head", ResultHeadCommit: "source-head", Status: pullrequestwork.StatusCompleted}},
		{name: "conflict worker", work: pullrequestwork.Work{HeadCommit: "source-head", SessionID: "holon-rebase", Status: pullrequestwork.StatusRunning}},
		{name: "failed", work: pullrequestwork.Work{Status: pullrequestwork.StatusFailed, Error: "fetch failed"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.work.ID, test.work.PullRequestID, test.work.Kind = "work-1", "pr-1", pullrequestwork.KindRebase
			service := &serviceStub{result: []pullrequestwork.Work{test.work}}
			response := performRebaseRequest(t, service, `{"prompt":"Rebase carefully","mechanical_only":true,"expected_base_commit":"base-commit"}`)
			if response.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var got pullrequestwork.Work
			if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.work) {
				t.Fatalf("response=%+v want=%+v", got, test.work)
			}
			want := pullrequestwork.Start{PullRequestID: "pr-1", Kind: pullrequestwork.KindRebase, Prompt: "Rebase carefully", MechanicalOnly: true, ExpectedBaseCommit: "base-commit"}
			if len(service.starts) != 1 || !reflect.DeepEqual(service.starts[0], want) {
				t.Fatalf("starts=%+v", service.starts)
			}
		})
	}
}

func TestStartRebaseRejectsMalformedRequest(t *testing.T) {
	service := &serviceStub{}
	response := performRebaseRequest(t, service, "{")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(service.starts) != 0 {
		t.Fatalf("starts=%+v", service.starts)
	}
	assertProblemCode(t, response, "invalid_request")
}

func TestStartRebaseMapsStaleHead(t *testing.T) {
	service := &serviceStub{err: pullrequestwork.ErrStaleHead}
	response := performRebaseRequest(t, service, `{}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	assertProblemCode(t, response, "stale_head")
}

func TestGetRebaseReadiness(t *testing.T) {
	service := &serviceStub{readiness: pullrequestwork.RebaseReadiness{
		BaseCommit: "base", HeadCommit: "head", BaseCommitsAhead: 3, BranchFreshness: pullrequestwork.BranchNotUpToDate, RebaseConflictState: pullrequestwork.RebaseClean,
	}}
	mux := http.NewServeMux()
	RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/pr-1/rebase-readiness", nil))
	if response.Code != http.StatusOK || len(service.readinessCalls) != 1 || service.readinessCalls[0] != "pr-1" {
		t.Fatalf("status=%d calls=%v body=%s", response.Code, service.readinessCalls, response.Body.String())
	}
	var got pullrequestwork.RebaseReadiness
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got != service.readiness {
		t.Fatalf("readiness=%+v", got)
	}
}

func TestGetRebaseReadinessMapsFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		code   string
		status int
	}{
		{name: "missing", err: pullrequestwork.ErrPullRequestNotFound, code: "pull_request_not_found", status: http.StatusNotFound},
		{name: "inactive", err: pullrequestwork.ErrPullRequestInactive, code: "pull_request_inactive", status: http.StatusConflict},
		{name: "stale", err: pullrequestwork.ErrStaleHead, code: "stale_head", status: http.StatusConflict},
		{name: "repository", err: pullrequestwork.ErrRebaseReadinessUnavailable, code: "repository_unavailable", status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &serviceStub{readinessErr: test.err}
			mux := http.NewServeMux()
			RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/pr-1/rebase-readiness", nil))
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertProblemCode(t, response, test.code)
		})
	}
}

func performRebaseRequest(t *testing.T, service Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/pr-1/rebase", strings.NewReader(body))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func assertProblemCode(t *testing.T, response *httptest.ResponseRecorder, want string) {
	t.Helper()
	var problem map[string]string
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem["code"] != want {
		t.Fatalf("problem=%v", problem)
	}
}

func (*serviceStub) Queue(context.Context, string) ([]pullrequestwork.Work, error) { return nil, nil }
func (*serviceStub) Cancel(context.Context, string, string) (pullrequestwork.Work, error) {
	return pullrequestwork.Work{}, nil
}
