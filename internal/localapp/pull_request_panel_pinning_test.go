package localapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

type recordingPanelPinner struct {
	ids []string
	err error
}

func (pinner *recordingPanelPinner) Pin(_ context.Context, pullRequestID string) error {
	pinner.ids = append(pinner.ids, pullRequestID)
	return pinner.err
}

type successfulPullRequestCreator struct {
	pullRequest pullrequestlifecycle.PullRequest
}

func (creator successfulPullRequestCreator) Create(context.Context, pullrequestlifecycle.CreatePullRequest) (pullrequestlifecycle.PullRequest, error) {
	return creator.pullRequest, nil
}

func TestSuccessfulPullRequestCreationRemainsSuccessfulWhenPanelPinningFails(t *testing.T) {
	want := pullrequestlifecycle.PullRequest{ID: "pr-created", Status: pullrequestlifecycle.StatusWIP}
	pins := &recordingPanelPinner{err: errors.New("pin failed")}
	creator := pinningPullRequestCreator{creator: successfulPullRequestCreator{pullRequest: want}, pins: pins}

	got, err := creator.Create(t.Context(), pullrequestlifecycle.CreatePullRequest{})
	if err != nil || got.ID != want.ID {
		t.Fatalf("pull request=%+v err=%v", got, err)
	}
	if len(pins.ids) != 1 || pins.ids[0] != want.ID {
		t.Fatalf("pin attempts=%v", pins.ids)
	}
}

func TestSuccessfulHolonAgentLaunchPinsItsPullRequest(t *testing.T) {
	service, store := terminalTestService(t)
	holon := holons.Holon{ID: "pr-holon", Title: "PR Holon", Kind: holons.KindPRMetadata, Status: holons.StatusRunning, BaseCommit: "abc", WorktreePath: t.TempDir(), PullRequestID: "pr-activity", CreatedAt: time.Now().UTC()}
	if err := store.Create(t.Context(), holon); err != nil {
		t.Fatal(err)
	}
	pins := &recordingPanelPinner{}
	runtime := &terminalHolonService{Service: service, agents: &recordingAgentLauncher{}, panelPins: pins}

	launched, err := runtime.launchAgent(t.Context(), holon, "agent", agentsessions.LaunchOptions{})
	if err != nil || launched.PullRequestID != holon.PullRequestID {
		t.Fatalf("holon=%+v err=%v", launched, err)
	}
	if len(pins.ids) != 1 || pins.ids[0] != holon.PullRequestID {
		t.Fatalf("pin attempts=%v", pins.ids)
	}

	pins.ids = nil
	runtime.agents = nil
	if _, err := runtime.launchAgent(t.Context(), holons.Holon{ID: "reused-metadata", PullRequestID: holon.PullRequestID}, "agent", agentsessions.LaunchOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(pins.ids) != 1 || pins.ids[0] != holon.PullRequestID {
		t.Fatalf("agentless pin attempts=%v", pins.ids)
	}
}
