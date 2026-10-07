package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/pullrequestparticipants"
	"github.com/holark-ai/holark/internal/pullrequestparticipants/httpapi"
)

type testRateLimitError struct{ error }

func (testRateLimitError) RateLimited() bool { return true }

func TestRateLimitErrorClassification(t *testing.T) {
	status, response := httpapi.ClassifyError(testRateLimitError{errors.New("limited")})
	if status != http.StatusTooManyRequests || response.Code != "github_rate_limit_exceeded" || response.Message != "GitHub API rate limit exceeded. Try again after the limit resets." {
		t.Fatalf("status=%d response=%+v", status, response)
	}
}

func TestParticipantRoutesUseStrictBodiesAndCombinedSnapshots(t *testing.T) {
	service := &fakeService{snapshot: pullrequestparticipants.Snapshot{PullRequestID: "pr-1", AssigneeHolarkIDs: []string{}, RequestedReviewerHolarkIDs: []string{}}}
	mux := http.NewServeMux()
	httpapi.RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service)

	request := httptest.NewRequest(http.MethodPut, "/api/v1/pull-requests/pr-1/assignees", strings.NewReader(`{"assignee_holark_ids":[" member-a ","member-a"]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d service=%#v body=%s", response.Code, service, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"assignee_holark_ids":[]`) || !strings.Contains(response.Body.String(), `"requested_reviewer_holark_ids":[]`) {
		t.Fatalf("body = %s", response.Body.String())
	}

	for _, body := range []string{`null`, `{}`, `{"requested_reviewer_holark_ids":[]}`, `{"assignee_holark_ids":null}`, `{"assignee_holark_ids":[]} {}`} {
		request := httptest.NewRequest(http.MethodPut, "/api/v1/pull-requests/pr-1/assignees", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("body %q: status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
}

type fakeService struct {
	snapshot pullrequestparticipants.Snapshot
}

func (service *fakeService) Get(context.Context, string) (pullrequestparticipants.Snapshot, error) {
	return service.snapshot, nil
}
func (service *fakeService) GetMany(context.Context, []string) (map[string]pullrequestparticipants.Snapshot, error) {
	return nil, nil
}
func (service *fakeService) Sync(context.Context, string) (pullrequestparticipants.Snapshot, error) {
	return service.snapshot, nil
}
func (service *fakeService) ReplaceAssignees(_ context.Context, id string, ids []string) (pullrequestparticipants.Snapshot, error) {
	return service.snapshot, nil
}
func (service *fakeService) AddAssignee(context.Context, string, string) (pullrequestparticipants.Snapshot, error) {
	return service.snapshot, nil
}
func (service *fakeService) RemoveAssignee(context.Context, string, string) (pullrequestparticipants.Snapshot, error) {
	return service.snapshot, nil
}
func (service *fakeService) ReplaceRequestedReviewers(context.Context, string, []string) (pullrequestparticipants.Snapshot, error) {
	return service.snapshot, nil
}
func (service *fakeService) AddRequestedReviewer(context.Context, string, string) (pullrequestparticipants.Snapshot, error) {
	return service.snapshot, nil
}
func (service *fakeService) RemoveRequestedReviewer(context.Context, string, string) (pullrequestparticipants.Snapshot, error) {
	return service.snapshot, nil
}
