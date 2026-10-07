package main

import "github.com/holark-ai/holark/internal/testfixture/pullrequestfixture"

type fixtureScenario string

const (
	fixtureScenarioDefault             fixtureScenario = ""
	fixtureScenarioPullRequestCreation fixtureScenario = "pull-request-creation"
	fixtureScenarioHarnessVersions     fixtureScenario = "harness-versions"
)

func (scenario fixtureScenario) valid() bool {
	return scenario == fixtureScenarioDefault || scenario == fixtureScenarioPullRequestCreation || scenario == fixtureScenarioHarnessVersions
}

type pullRequestFlow string

const (
	pullRequestFlowActionPriority         pullRequestFlow = "action-priority"
	pullRequestFlowHappyPath              pullRequestFlow = "happy-path"
	pullRequestFlowOutdatedDescription    pullRequestFlow = "outdated-description"
	pullRequestFlowOutdatedReview         pullRequestFlow = "outdated-review"
	pullRequestFlowRebaseClickConflict    pullRequestFlow = "rebase-click-conflict"
	pullRequestFlowRegenerationFailure    pullRequestFlow = "regeneration-failure"
	pullRequestFlowPublicationFailure     pullRequestFlow = "publication-failure"
	pullRequestFlowDescriptionSaveFailure pullRequestFlow = "description-save-failure"
)

func (flow pullRequestFlow) valid() bool {
	switch flow {
	case pullRequestFlowActionPriority, pullRequestFlowHappyPath, pullRequestFlowOutdatedDescription, pullRequestFlowOutdatedReview, pullRequestFlowRebaseClickConflict, pullRequestFlowRegenerationFailure,
		pullRequestFlowPublicationFailure, pullRequestFlowDescriptionSaveFailure:
		return true
	default:
		return false
	}
}

func (flow pullRequestFlow) fixtureFlow() pullrequestfixture.BrowserFlow {
	return pullrequestfixture.BrowserFlow(flow)
}

const rebaseClickConflictLaunchScript = `
document.addEventListener("click", (event) => {
  const button = event.target instanceof Element ? event.target.closest("button") : null;
  if (!button || button.textContent?.trim() !== "Rebase") return;
  if (button.dataset.rebaseClickConflictReady === "true") {
    delete button.dataset.rebaseClickConflictReady;
    delete button.dataset.rebaseClickConflictPending;
    return;
  }
  event.preventDefault();
  event.stopImmediatePropagation();
  if (button.dataset.rebaseClickConflictPending === "true") return;
  button.dataset.rebaseClickConflictPending = "true";
  void (async () => {
    try {
      const match = window.location.pathname.match(/^\/pulls\/([^/]+)$/);
      if (!match) throw new Error("pull request ID is unavailable");
      const response = await fetch("/api/v1/pull-requests/" + match[1] + "/rebase-readiness", { method: "POST" });
      if (!response.ok) throw new Error("readiness refresh returned HTTP " + response.status);
      const readiness = await response.json();
      if (readiness.branch_freshness !== "not_up_to_date" || readiness.rebase_conflict_state !== "clean") {
        throw new Error("expected a clean required rebase preview");
      }
      await new Promise((resolve) => window.setTimeout(resolve, 1000));
      button.dataset.rebaseClickConflictReady = "true";
      button.click();
    } catch (error) {
      delete button.dataset.rebaseClickConflictPending;
      console.error("Could not prepare the rebase click conflict scenario", error);
    }
  })();
}, true);
`
