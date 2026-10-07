package pullrequestwork

import (
	"errors"
	"testing"
)

func TestStartPreflightsBeforeCreatingDurableWork(t *testing.T) {
	tests := []Start{
		{PullRequestID: "pr-1", Kind: KindReview},
		{PullRequestID: "pr-1", Kind: KindWorker, Mode: ModeAuto, CommentIDs: []string{"comment-1"}},
	}
	for _, input := range tests {
		t.Run(string(input.Kind), func(t *testing.T) {
			store := &memoryStore{}
			want := errors.New("configured harness unavailable")
			launcher := &launcherStub{preflightErr: want}
			service := newTestService(store, catalogStub{}, launcher, &findingsStub{})
			if _, err := service.Start(t.Context(), input); !errors.Is(err, want) {
				t.Fatalf("error = %v", err)
			}
			if len(store.values) != 0 || launcher.calls != 0 {
				t.Fatalf("work = %+v, launches = %d", store.values, launcher.calls)
			}
		})
	}
}
