package localapp

import (
	"context"
	"strings"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/agentsettings"
	"github.com/holark-ai/holark/internal/holons"
	"github.com/holark-ai/holark/internal/prompt_templates"
	"github.com/holark-ai/holark/internal/protocol"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	"github.com/holark-ai/holark/internal/terminals"
)

type commitAgentHolons interface {
	Get(context.Context, string) (holons.Holon, error)
	InspectWorkspaceWithOptions(context.Context, string, holons.InspectOptions) (holons.WorkspaceInspection, error)
	AddCommitAgentWithSelection(context.Context, string, string, string, string, string, holons.AgentSelection) (holons.Holon, string, error)
}
type commitAgentLauncher interface {
	Launch(context.Context, string, string, terminals.Dimensions, string, agentsessions.LaunchOptions) error
	Capabilities(context.Context) []protocol.HarnessCapability
	Fork(context.Context, string, string, string, terminals.Dimensions) error
}

type commitAgentCoordinator struct {
	holons          commitAgentHolons
	agents          commitAgentLauncher
	templates       prompttemplates.Reader
	harnesses       localHarnessPreferences
	work            localCommitWorkReader
	pinPullRequests func(context.Context, holons.Holon)
}

func (c *commitAgentCoordinator) CreateCommitAgent(ctx context.Context, id, sourceID string) (holons.Holon, error) {
	h, err := c.holons.Get(ctx, id)
	if err != nil {
		return holons.Holon{}, err
	}
	if h.HasActiveCommitAgent() {
		return holons.Holon{}, holons.ErrCommitForkInProgress
	}
	source, agentType, err := actionAgentSource(ctx, h, sourceID, c.agents, c.harnesses)
	if err != nil {
		return holons.Holon{}, err
	}
	autoCommit, err := autoCommitEligible(ctx, h, c.work)
	if err != nil {
		return holons.Holon{}, err
	}
	startHead := ""
	if autoCommit {
		inspection, err := c.holons.InspectWorkspaceWithOptions(ctx, id, holons.InspectOptions{SummaryOnly: true})
		if err != nil {
			return holons.Holon{}, err
		}
		startHead = strings.TrimSpace(inspection.HeadCommit)
		if startHead == "" {
			return holons.Holon{}, holons.ErrInvalid
		}
	}
	prompt := commitRequestPrompt(ctx, h, c.templates, c.work, autoCommit)
	_, destinationID, err := c.holons.AddCommitAgentWithSelection(ctx, id, source.ID, prompt, startHead, agentType, holons.AgentSelection{Model: source.Model, Permissions: source.Permissions})
	if err != nil {
		return holons.Holon{}, err
	}
	if err = launchActionAgent(ctx, c.agents, id, source.ID, destinationID); err != nil {
		return holons.Holon{}, err
	}
	if c.pinPullRequests != nil {
		c.pinPullRequests(ctx, h)
	}
	return c.holons.Get(ctx, id)
}

// Only user-directed holons use automatic commit agents and Commit/Rebase tab
// closure. Workflow holons own their prompts and completion lifecycle, even if
// an older agent saved a HEAD or a pending Rebase closure.
func autoCommitEligible(ctx context.Context, h holons.Holon, workReader localCommitWorkReader) (bool, error) {
	switch h.Kind {
	case holons.KindNormal, holons.KindIssue:
		return true, nil
	case holons.KindPullWorker:
		if workReader == nil {
			return false, pullrequestwork.ErrNotFound
		}
		work, err := workReader.WorkForSession(ctx, h.ID)
		if err != nil {
			return false, err
		}
		return work.Mode == pullrequestwork.ModeContinue, nil
	default:
		return false, nil
	}
}

func commitSource(h holons.Holon, sourceID string) (holons.AgentSession, error) {
	var source holons.AgentSession
	for _, a := range h.AgentSessions {
		if a.ID == sourceID && a.HolonID == h.ID && a.ClosedAt == nil {
			source = a
			break
		}
	}
	if source.ID == "" {
		return source, holons.ErrCommitSourceMissing
	}
	if strings.TrimSpace(source.ResumeTarget) == "" {
		return source, holons.ErrCommitSourceNotForkable
	}
	switch protocol.HarnessType(source.AgentType) {
	case protocol.HarnessCodex, protocol.HarnessOpenCode:
	case protocol.HarnessClaudeCode:
		if strings.TrimSpace(source.RolloutPath) == "" {
			return source, holons.ErrCommitSourceNotForkable
		}
	default:
		return source, holons.ErrCommitSourceNotForkable
	}
	return source, nil
}

func commitRequestPrompt(ctx context.Context, h holons.Holon, templates prompttemplates.Reader, workReader localCommitWorkReader, autoCommit bool) string {
	key, artifact := prompttemplates.CommitFollowUpKey, ""
	if autoCommit {
		key = prompttemplates.CommitAgentKey
	}
	if !autoCommit && (h.Kind == holons.KindPullWorker || h.Kind == holons.KindRebase) && workReader != nil {
		if work, err := workReader.WorkForSession(ctx, h.ID); err == nil && (work.Kind == pullrequestwork.KindRebase || work.Mode != pullrequestwork.ModeContinue) {
			key = prompttemplates.PullRequestWorkerAssistedCommitKey
			if work.Kind == pullrequestwork.KindRebase {
				key = prompttemplates.PullRequestRebaseCommitKey
			}
			artifact = pullRequestWorkArtifactPath(work.Kind)
		}
	}
	definition, _ := prompttemplates.DefinitionByKey(key)
	template := definition.DefaultValue
	if templates != nil {
		template = templates.Read(ctx, key)
	}
	return prompttemplates.Render(template, map[string]string{"artifact_path": artifact})
}

// An omitted source starts a fresh conversation. Explicit sources must remain
// forkable so a stale selection cannot silently discard conversation context.
func actionAgentSource(ctx context.Context, h holons.Holon, sourceID string, agents commitAgentLauncher, preferences localHarnessPreferences, workflows ...agentsettings.Workflow) (holons.AgentSession, string, error) {
	if sourceID == "" {
		if preferences == nil {
			return holons.AgentSession{}, "", agentsessions.ErrUnavailable
		}
		workflow := agentsettings.WorkflowManual
		if len(workflows) > 0 {
			workflow = workflows[0]
		}
		selection, err := preferences.Resolve(ctx, workflow, "")
		return holons.AgentSession{Model: selection.Model, Permissions: selection.Permissions}, string(selection.HarnessType), err
	}
	source, err := commitSource(h, sourceID)
	if err != nil {
		return holons.AgentSession{}, "", err
	}
	for _, capability := range agents.Capabilities(ctx) {
		if capability.Type == protocol.HarnessType(source.AgentType) && capability.Available {
			return source, source.AgentType, nil
		}
	}
	return holons.AgentSession{}, "", agentsessions.ErrUnavailable
}

func launchActionAgent(ctx context.Context, agents commitAgentLauncher, holonID, sourceID, destinationID string) error {
	dimensions := terminals.Dimensions{Columns: 120, Rows: 36}
	if sourceID == "" {
		return agents.Launch(ctx, holonID, destinationID, dimensions, "", agentsessions.LaunchOptions{})
	}
	return agents.Fork(ctx, holonID, sourceID, destinationID, dimensions)
}
